---
title: TamaGo UEFI Phase 2 — OCI pre-boot loader (shape A)
status: design / in-progress (M0 done)
last-updated: 2026-06-07
---

# TamaGo UEFI Phase 2 — OCI pre-boot loader (shape A)

## 1. Problem statement

Phase 1 of [`cloud-boot/tamago-uefi`](https://github.com/cloud-boot/tamago-uefi)
proved that a pure-Go TamaGo binary can act as a PE32+/EFI application on
all four CPU architectures cloud-boot cares about (amd64, arm64,
riscv64, loong64): firmware loads the image, our `cpuinit_<arch>.s`
captures `ImageHandle`/`SystemTable`/`ConOut`, the standard TamaGo runtime
brings up GC + scheduler + goroutines, and `main()` prints over ConOut
and halts. No `ExitBootServices`, no network, no kernel handoff.

**Phase 2 (shape A)** turns that image into a *PXE-class pre-boot agent*
that runs entirely in UEFI Boot Services, fetches a `kernel + initrd`
(plus optional `cmdline`) OCI artifact from a configured registry over
HTTPS, verifies a signature, calls `ExitBootServices`, and jumps to the
loaded Linux kernel using the architecture's Linux/EFI handoff ABI. The
end state replaces `PXE + iPXE + systemd-boot` (the historical three-stage
network-boot chain) with a single statically-linked Go application: one
artifact, one transport (HTTPS+OCI), one trust anchor (the embedded
public key), one ExitBootServices.

This is **path A** in the cloud-boot taxonomy
([`architecture/three-paths.md`](docs/architecture/three-paths.md)) but
*without* the bootstrap Linux kernel — we never go through the Linux
PID-1 stage at all on hypervisors where the firmware exposes the
required Boot Service protocols. On hypervisors that don't (Apple VZ
being the canonical counter-example, see hypervisor matrix), shape A is
not applicable; path C remains the production target there.

Phase 2 ships one EFI application; `cloud-boot/iso` keeps packing it the
same way it packs Phase 1's `BOOT<ARCH>.EFI`.

## 2. Architectural decision: Path X (UEFI Boot Services) vs Path Y (pure-Go stack)

We faced a binary choice for *how to do networking* before
`ExitBootServices`:

- **Path X — UEFI Boot Service protocols.** Drive
  `EFI_DHCP4_PROTOCOL`, `EFI_DNS4_PROTOCOL`,
  `EFI_HTTP_PROTOCOL`, `EFI_TLS_PROTOCOL`,
  `EFI_TCP4_PROTOCOL`, `EFI_MANAGED_NETWORK_PROTOCOL` (all pre-EBS,
  exposed by firmware), and reuse the well-tested NIC drivers and TCP/IP
  stack that EDK2 already runs (`MdeModulePkg/Universal/Network/*`,
  `NetworkPkg/HttpDxe`, `NetworkPkg/TlsDxe`).

- **Path Y — Pure-Go virtio-net + `net/http` over a custom stack.**
  Write a virtio-net driver in pure Go, plumb it into a Go TCP/IP stack
  (e.g. `gvisor.dev/gvisor/pkg/tcpip`), use `crypto/tls` + `net/http`
  for the fetch, then ExitBootServices and hand off.

**Decision: Path X.**

Rationale:

- **Shape A only needs network *pre-EBS*.** Once we hand off to the
  Linux kernel, Linux sets up its own NIC drivers and its own network.
  We don't need to "carry" a Go-side network stack past EBS, so the
  fact that UEFI protocols cease to exist after `ExitBootServices`
  doesn't matter — we will have called EBS and jumped to Linux *seconds*
  after the HTTP fetch returns.
- **Reuse vs. reimplement.** EDK2's `NetworkPkg` has been in production
  on every major UEFI platform for a decade. A virtio-net + TCP/IP +
  TLS stack in pure Go is *months* of work to bring to feature parity
  with EDK2's stack on transient packet loss, MTU discovery, and TLS
  1.3 interop. We get all of that for free by reusing the firmware's
  code paths via well-defined Boot Service handles.
- **Smaller attack surface in our binary.** The Go binary needs only the
  UEFI protocol wrappers + an OCI client. The TCP/IP and TLS code lives
  in firmware and gets the platform's security-fix lifecycle.
- **Multi-arch parity.** On amd64 / arm64 / riscv64 / loong64, the
  Network Stack Modules in EDK2 are the same code; we don't need to port
  a virtio-net driver per arch. On hardware NICs (Intel, Mellanox, etc.)
  EDK2's UNDI / SNP layer plus the platform's option ROM cover what we
  cannot.

Costs / risks we accept:

- We are **vulnerable to firmware variability**: EDK2 may ship without
  `NetworkPkg` configured in. Some downstreams (Coreboot+Tianocore,
  certain server BMCs) strip it. We *cannot* run shape A there. **This
  is acceptable** — those platforms either run path C (path C runs from
  Linux, not from UEFI, so the firmware's protocol coverage is moot) or
  the operator is told *up-front* "shape A needs UEFI HTTP, your
  firmware doesn't expose it, use shape C".
- We pay one indirect call per UEFI service invocation, with a stack
  switch and ABI translation in `eficall_<arch>.s`. Phase 1 already
  pays this cost per ConOut print and it is invisible.
- **Path Y as a fallback for TLS only**, on platforms whose
  `EFI_TLS_PROTOCOL` is missing or broken: see M2 risks below.

Upstream references (read before writing M1+ code):

- UEFI 2.10 spec, §29 (HTTP Boot), §28 (TLS Protocol),
  §27 (Network Protocols).
- `edk2.git`:
  - `MdePkg/Include/Protocol/Http.h`
  - `MdePkg/Include/Protocol/Tls.h`
  - `MdePkg/Include/Protocol/Dhcp4.h`
  - `MdePkg/Include/Protocol/Dns4.h`
  - `NetworkPkg/HttpDxe/HttpImpl.c` (request/response state machine)
  - `NetworkPkg/TlsDxe/TlsImpl.c` (cipher suite negotiation, cert chain)
  - `MdeModulePkg/Library/UefiBootManagerLib/BmBoot.c` (LoadImage usage)
  - `MdePkg/Include/Uefi/UefiSpec.h` (`EFI_BOOT_SERVICES`,
    `ExitBootServices`, `GetMemoryMap`).
- For the kernel handoff per arch:
  - amd64: `Documentation/arch/x86/boot.rst` (kernel boot protocol)
    and Linux EFI stub semantics.
  - arm64: `Documentation/arch/arm64/booting.rst`,
    `arch/arm64/kernel/efi-entry.S`, `arch/arm64/kernel/image.h`.
  - riscv64: `Documentation/arch/riscv/boot.rst`,
    `arch/riscv/kernel/efi-entry.S`, `arch/riscv/kernel/head.S`.
  - loong64: `Documentation/arch/loongarch/booting.rst`,
    `arch/loongarch/kernel/efi-header.S`.
  - For all four: Linux's `EFI_LOAD_FILE2_PROTOCOL` for the initrd
    handoff (`LINUX_EFI_INITRD_MEDIA_GUID`) — the same protocol
    `cloud-boot/loader` already publishes on path B.

## 3. Milestones M0 → M4

Each milestone is a separate agent run with its own scaffolding +
tests + commit. M0 introduces the type surface but no live calls (beyond
`GetMemoryMap` for the probe); M1..M4 wire one Boot Service family at a
time.

### M0 — Probe + type surface (this milestone)

**Done in this PR.**

Deliverables:

- `uefiboard/ebs.go` — `ExitBootServices(mapKey uintptr) error` thunk.
  Not called from the Phase-1 main flow.
- `uefiboard/memorymap.go` — `MemoryDescriptor` struct + `GetMemoryMap()
  ([]MemoryDescriptor, uintptr, error)` wrapper around
  `gBS->GetMemoryMap`.
- `uefiboard/http_protocol.go` — GUIDs and struct shapes for the UEFI
  HTTP protocol family (`EFI_HTTP_SERVICE_BINDING_PROTOCOL_GUID`,
  `EFI_HTTP_PROTOCOL_GUID`, `EFI_HTTP_REQUEST_DATA`,
  `EFI_HTTP_RESPONSE_DATA`, `EFI_HTTP_MESSAGE`, `EFI_HTTP_TOKEN`,
  `EFI_HTTP_CONFIG_DATA`). No methods yet.
- `--phase2-probe` flag (or `phase2_probe` build tag) in `main.go`
  that calls `GetMemoryMap`, prints a summary (descriptor count + RAM
  totals by memory type) to ConOut, halts. Phase 1 banner remains the
  default behaviour.
- Tests covering ≥80% of `memorymap.go` (parser fed a synthetic buffer)
  and round-trip GUID byte-layout assertions for `http_protocol.go`.
- This design doc, committed to `cloud-boot/docs`.

Acceptance: Phase 1 regression (`task test:multiarch:boot` in
`cloud-boot/iso`) still PASS, host `go test ./uefiboard/...` PASS,
all four `BOOT<ARCH>.EFI` files still link.

### M1 — DHCP4 + HTTP fetch (no TLS yet)

Deliverables:

- `uefiboard/dhcp4_protocol.go` — Service binding handle acquisition,
  config, run-to-completion. Yields an IPv4 address, subnet mask,
  router list, DNS server list.
- `uefiboard/dns4_protocol.go` — resolve a configured registry hostname
  to an IPv4. (Optional in M1 if the test target uses an IP literal.)
- `uefiboard/http_client.go` — minimal Go-side wrapper over the M0
  `http_protocol.go` types: `Get(url string) (*Response, error)` using
  the firmware's `EFI_HTTP_PROTOCOL`. Plaintext HTTP only.
- A `--phase2-fetch http://...` flag that fetches a URL and prints
  the response body length + first 64 bytes. No OCI yet, no TLS.

Acceptance: on QEMU/OVMF with the `user` networking back-end, the
amd64 image fetches an HTTP URL served by a host-side `python -m
http.server`. arm64 + riscv64 + loong64 either repro the fetch or
report a clean "EFI_HTTP_PROTOCOL not available on this firmware",
which gates a Risk-section finding in this doc.

### M2 — TLS + HTTPS (highest-risk milestone)

Deliverables:

- `uefiboard/tls_protocol.go` — `EFI_TLS_PROTOCOL` wrapper.
- Wire `EFI_HTTP_CONFIG_DATA.HttpVersion = EFI_HTTPVERSION_1_1` +
  TLS configuration; verify a single embedded root certificate.
- `--phase2-fetch https://...` reachable.

**Risk (see §5):** UEFI TLS is the wobbliest part of the spec across
firmware versions. The fallback plan is in §5.

### M3 — OCI registry client

Deliverables:

- `uefiboard/oci_client.go` — minimal OCI distribution-spec v1.1
  client (manifest fetch, blob fetch, multi-arch index resolution).
- Content-Digest verification (`sha256:...`) on every blob.
- Signature verification (cosign-compatible bundle signature over
  the manifest digest; embedded public key).
- `--phase2-pull oci://registry/repo:tag` flag that downloads a
  manifest, walks blobs, and stages each in firmware-allocated memory.
  Layout the descriptors needed for M4 handoff.

Acceptance: a UKI-style artifact (one config + two blobs:
`vmlinuz`, `initrd`) round-trips manifest → blobs → in-memory.

### M4 — Post-EBS memory + per-arch Linux handoff

Deliverables:

- `uefiboard/handoff_<arch>.s` — per-arch kernel entry shim. Linux
  EFI stub conventions:
  - amd64: jump to `[kernel + 0x200]` (32-bit entry) with the
    boot_params zero-page set up, or the 64-bit EFI handover entry
    where present.
  - arm64: `image_base + 0` is the entry; X0 = FDT or 0 (we use 0 to
    fall through to EFI stub's runtime services lookup; with M4 we
    will have called EBS so the stub takes the no-EFI path).
  - riscv64: per the arch booting doc, a0 = hartid, a1 = device tree
    or 0. We pass 0 and let the EFI stub fall back.
  - loong64: image entry at offset 0; a0 = pointer to bootparams.
- `uefiboard/initrd_protocol.go` — publish
  `EFI_LOAD_FILE2_PROTOCOL` under `LINUX_EFI_INITRD_MEDIA_GUID` so the
  EFI stub picks the initrd up the modern way (same shape
  `cloud-boot/loader` uses).
- Memory-map → `ExitBootServices` choreography:
  1. `GetMemoryMap` → `mapKey`.
  2. `ExitBootServices(imageHandle, mapKey)`.
  3. If EBS returns `EFI_INVALID_PARAMETER`, refresh `GetMemoryMap` and
     retry (EDK2 will reject EBS if the map changed since GetMemoryMap;
     a single retry is the spec-blessed pattern).
  4. On success, all UEFI services are gone; jump to the kernel entry.

Acceptance: on QEMU/OVMF amd64 + arm64, a vanilla upstream Linux
kernel boots from `oci://localhost:5000/k8s-mainline:latest` and
prints its banner over the OVMF console (same VT100 we used in
Phase 1). riscv64 + loong64 are expected to work but may surface a
firmware idiosyncrasy that becomes its own M4.x finding.

## 4. Five validation checks before declaring shape A complete

These are not unit tests; they are end-to-end gates that block shipping.

1. **Multi-arch parity.** All four arches reach a Linux login prompt
   under QEMU/OVMF using the same EFI binary build pipeline (modulo the
   per-arch `BOOT<ARCH>.EFI` artifact). Recorded under
   `cloud-boot/iso`'s `task test:multiarch:boot` extended with a
   `phase2-oci` profile.
2. **Signature verification correctness.** Tampering one byte in the
   manifest OR a blob OR the signature MUST cause the loader to halt
   *before* `ExitBootServices`, with the failure printed to ConOut.
   A negative-path test fixture is part of M3's CI.
3. **Network-failure modes.** Loss of DHCP lease, registry DNS NXDOMAIN,
   TCP RST during blob fetch, TLS handshake timeout, HTTP 5xx with
   `Retry-After`. Each MUST end in a clean halt (no jump to a partial
   kernel image) and a one-line diagnostic. Soak under QEMU with
   `--netdev user,restrict=on` + injected tc-style perturbation.
4. **EBS handoff correctness.** Boot 100 times in a row across all
   four arches. No flakes. The `mapKey` retry path MUST exercise at
   least once per arch (instrument by deliberately mutating the map
   between `GetMemoryMap` and EBS).
5. **Transient-retry behaviour on the registry.** A 503 with backoff
   from the registry must NOT propagate to a boot failure — exponential
   backoff with jitter, capped at N retries (M3 sets N), then halt.
   Soak with a host-side faulty proxy.

## 5. Risks

### R-M2 (HIGH) — UEFI TLS variability

`EFI_TLS_PROTOCOL` is the least-deployed-in-the-wild part of the
NetworkPkg surface. Known concrete pain points:

- EDK2's `TlsDxe` historically defaulted to TLS 1.0 / 1.1 cipher
  suites; modern registries (Docker Hub, ghcr.io, Quay) require TLS 1.2+
  with AEAD ciphersuites. Some firmwares ship with no AEAD suite at
  all and the handshake fails before we see a cert.
- `EFI_TLS_PROTOCOL.SetSessionData(EfiTlsCipherList, ...)` parameter
  layout has churned: an older edk2 release shipped
  `EFI_TLS_CIPHER_BUFFER` instead of the spec's
  `EFI_TLS_CIPHER`. We can be tripped by either.
- Some platform integrations have NO `EFI_TLS_PROTOCOL` handle at all;
  HTTPS is via the platform's HTTP Boot's built-in TLS rather than an
  exposed protocol.

**Fallback plan**: if `EFI_TLS_PROTOCOL` is missing or its
`SetSessionData` rejects our cipher list, fall back to Go-stdlib
`crypto/tls` running over an `EFI_TCP4_PROTOCOL` socket. The cost is
shipping `crypto/tls` in our PE (a few MB) and CPU-bound handshake on
the bootstrap. Acceptable. The detection logic lives in
`tls_protocol.go`: probe for the handle, if absent, mark TLS-on-TCP4
mode. This MUST be designed up-front so M3's `oci_client.go` is
transport-agnostic.

### R-M0 — `GetMemoryMap` quirks

UEFI's `GetMemoryMap` is one of the few Boot Services that returns
data through *two* output buffers (descriptors + opaque map key) plus
an output `DescriptorSize` that callers MUST respect (descriptors may
be *larger* than `sizeof(EFI_MEMORY_DESCRIPTOR)` if the firmware
includes implementation-private fields after the spec'd ones). Our
parser MUST stride by `DescriptorSize`, not by `sizeof(Go struct)`.
The M0 unit test exercises both `DescriptorSize == sizeof(spec)` and
`DescriptorSize == sizeof(spec) + 8`.

#### R-M0a — **M0 finding 2026-06-07: riscv64 EDK2 requires non-NULL `DescriptorVersion`**

The M0 probe boots end-to-end on **amd64 / arm64 / loong64** under
QEMU + EDK2-stable202408. Concrete numbers from the M0 run:

| arch    | descriptors | DescriptorSize | Conventional RAM | comments                |
| ------- | ----------: | -------------: | ---------------: | ----------------------- |
| amd64   |         119 |             48 |      ~2.09 GiB   | -m 2048                 |
| arm64   |          31 |             48 |      ~4.22 GiB   | -m 4096                 |
| loong64 |          51 |             48 |      ~4.18 GiB   | -m 4096                 |
| riscv64 |        FAIL |              — |              —   | store-access page-fault |

DescriptorSize is **48** on every working arch (40 spec + 8
firmware-private bytes) — exactly the case the M0 parser was built
for, and the case the unit test
`TestParseMemoryMap_LargerStride` covers.

riscv64 faults inside firmware:

    !!!! RISCV64 Exception Type - 000000000000000F
         (EXCEPT_RISCV_STORE_ACCESS_PAGE_FAULT) !!!!
    sepc  = 0x000000008316F61E
    stval = 0xFFFFFFFFFFFFFF00

The `stval` value (effectively `(uintptr)NULL - 256`) is the
canonical "near-NULL plus a small offset" signature: EDK2's
`MdeModulePkg/Core/Dxe/Mem/Page.c::CoreGetMemoryMap` on riscv64
unconditionally writes `*DescriptorVersion =
EFI_MEMORY_DESCRIPTOR_VERSION` on the success path, even when the
caller passed NULL. amd64 / arm64 / loong64 EDK2 builds tolerate the
NULL pointer (either via a guard the riscv64 port lacks, or via the
target's exception model letting the store be silently ignored — most
likely the former: the spec REQUIRES NULL to be rejected with
`EFI_INVALID_PARAMETER`, but the implementation only checks the first
two parameters).

**Mitigation, deferred to M1**: extend `efiCall` from 4-arg to 5-arg
across all four `eficall_<arch>.s` thunks, and pass a non-NULL
`DescriptorVersion` pointer. The 5th integer arg goes in:

- amd64 (MS x64): R10? — no, only RCX/RDX/R8/R9 are register args;
  the 5th is on the stack at `[RSP+32]` (above the 32-byte shadow
  space). Thunk must push it before `CALL (AX)`.
- AArch64 / RISC-V LP64 / LoongArch LP64: the 5th integer arg is in
  X4 / A4 / R8 (A4 register), respectively. Thunk can simply load it
  there before the BL/JALR/JAL.

A defensible alternative is to wrap the 5-arg call site in a per-arch
ASM helper rather than retrofitting all of `efiCall`; the trade-off
is whether *any* other Phase-2 Boot Service we plan to call needs >4
args. Per the UEFI 2.10 spec, AllocatePool (3 args), FreePool (1),
LocateProtocol (3), HandleProtocol (3), LoadImage (6 — yes, this is
the other 5+ arg case we'll need at M4), StartImage (3),
ExitBootServices (2). LoadImage's 6 args force the 5+ arg extension
anyway, so we do it once in M1 rather than twice.

Until that lands, the M0 probe builds emit the same Phase-1 banner +
fault on riscv64 and the design-doc cite this section. amd64 / arm64
/ loong64 carry M1+ forward.

### R-M1 — Boot-time DHCP rebinding

`EFI_DHCP4_PROTOCOL` returns a lease; the firmware does NOT renew it
during our pre-EBS lifetime, but our session can outlast the rebind
window if a blob takes minutes to fetch. For the M1 PoC we accept
this; M3 caps total runtime; M4's retry loop refreshes DHCP if a
network call fails with EFI_TIMEOUT.

### R-M4 — Per-arch handoff calling conventions

The four arches do NOT share an entry shape:

- amd64 has TWO entries (legacy 32-bit setup + 64-bit EFI handover);
  only the latter has been stable in upstream Linux since v6.x.
- arm64 + riscv64 + loong64 each have their own EFI stub entry +
  expected register state.

The M4 plan above pins the canonical Linux references per arch; if a
given Linux release diverges (it has happened on riscv64), document
the divergence here.

## 6. Open questions

Per milestone, revisit at the start of the M-N agent run.

### M0

- *Resolved 2026-06-07*: `GetMemoryMap` may return `EFI_BUFFER_TOO_SMALL`
  on the first call; the canonical pattern is "try, grow, retry". The
  M0 wrapper implements this. Confirmed against EDK2's
  `MdeModulePkg/Library/UefiBootManagerLib/BmMisc.c` (`BmGetMemoryMap`).

### M1

- Does `EFI_DHCP4_PROTOCOL` expose enough to drive a managed
  `EFI_HTTP_PROTOCOL` end-to-end, or do we need a `MNP` config call in
  between? EDK2 `HttpDxe/HttpProto.c::HttpInitProtocol` suggests no
  manual MNP step is needed but it depends on whether the firmware
  pre-creates the MNP child.
- IPv6: Phase 2 ships v4-only. Track v6 as a follow-up.

### M2

- Embedded root CAs: ship a single self-signed CA at build time, or
  parse the `EFI_TLS_CA_CERTIFICATE` UEFI variable? Both are valid;
  defer the call to M2's start.
- If we end up on the TLS-on-TCP4 fallback path, do we still want
  `EFI_HTTP_PROTOCOL` for the HTTP framing (it can run over a TLS
  socket), or do we plumb a tiny HTTP/1.1 client in Go? The latter is
  cheaper code-size if we already pulled in `crypto/tls`.

### M3

- OCI registries chunk blob responses with `Content-Range`/`Range`.
  Required for >2 GiB blobs, not for kernels. Skip in M3, add an
  M3.1 follow-up issue.
- Cosign vs. notation vs. raw PGP for signatures: cosign is the
  current de-facto; the bundle format is stable.

### M4

- `EFI_LOAD_FILE2_PROTOCOL` publication ordering: must precede
  `LoadImage` of the kernel, since the EFI stub looks the GUID up
  via `LocateProtocol` on entry.
- For amd64, the boot_params zero-page is set up by the EFI stub for
  us if we use `LoadImage + StartImage`. Decide whether we use that
  path or hand-roll the boot protocol — `LoadImage+StartImage` is
  simpler and reuses the firmware's PE loader. Default plan:
  `LoadImage` for parity across arches.

## 7. Cross-references

- The bootable ISO assembly stays in `cloud-boot/iso`. Phase 2
  produces a single EFI app; iso's `task iso:multiarch` packs it
  the same way it packs Phase 1's `BOOT<ARCH>.EFI`.
- Phase-1 status table + arch quirks: see
  [`cloud-boot/tamago-uefi/README.md`](https://github.com/cloud-boot/tamago-uefi/blob/main/README.md).
- Path-A / Path-B / Path-C taxonomy:
  [`architecture/three-paths.md`](docs/architecture/three-paths.md).
- Hypervisor protocol coverage matrix:
  [`architecture/hypervisors.md`](docs/architecture/hypervisors.md).
- The cosign-compatible signing format we will verify in M3 is
  documented in the
  [Sigstore bundle spec](https://github.com/sigstore/protobuf-specs).

## 8. Changelog

- **2026-06-07** (M0): doc seeded; scaffolding code + `phase2_probe`
  build tag landed in `cloud-boot/tamago-uefi`. `GetMemoryMap` works
  end-to-end on amd64 / arm64 / loong64 under QEMU/EDK2-stable202408
  — counts + per-type RAM totals print to ConOut, then halt. **One
  surprise**: riscv64 EDK2 faults inside the firmware when
  `DescriptorVersion` is NULL — see §5 R-M0a. Mitigation deferred
  to M1 (extend `efiCall` to 5-arg arity, which we already need for
  `LoadImage` at M4). amd64 / arm64 / loong64 carry forward unblocked.
