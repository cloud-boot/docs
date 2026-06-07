---
title: TamaGo UEFI Phase 2 — OCI pre-boot loader (shape A)
status: design / in-progress (M0 done; Path Y M1 in flight)
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

## 2. Architectural pivot — Path X abandoned, Path Y adopted (2026-06-07)

We initially designed Phase 2 around **Path X**: drive
`EFI_DHCP4_PROTOCOL`, `EFI_DNS4_PROTOCOL`, `EFI_HTTP_PROTOCOL`,
`EFI_TLS_PROTOCOL`, and `EFI_TCP4_PROTOCOL` directly from our pure-Go
EFI app and reuse the EDK2 NetworkPkg stack. M0 landed against that
plan; M1 work started; the alternative — **Path Y**, a pure-Go virtio-net
+ TCP/IP + TLS + HTTP stack — was treated as a fallback.

**The pivot.** Path X cannot meet the cross-hypervisor requirement.
Apple VZ firmware does NOT expose the UEFI network protocols
(`EFI_HTTP_PROTOCOL`, `EFI_DHCP4_PROTOCOL`, `EFI_DNS4_PROTOCOL`,
`EFI_TCP4_PROTOCOL`, `EFI_TLS_PROTOCOL` — none of them have published
handles). This is documented in
[`docs/tutorials/vfkit.md`](docs/tutorials/vfkit.md) lines 17-19:

```text
VZ firmware has no HTTP/TCP/DHCP/DNS  →  Path B can't fetch plans
                                          (the loader's networked phases
                                          D–J were abandoned for VZ)
```

The same firmware-protocol gap that killed Path B's networked phases
on VZ also kills Path X for shape A. We could ship Path X for
QEMU+EDK2 only and tell every Apple Silicon user "use shape C
instead", but that splits the multi-hypervisor contract that motivated
shape A in the first place. So we pivot.

**Decision: Path Y (pure-Go networking).** Final stack:

```text
+-----------------------------------------------------------------+
|  Pure-Go OCI client (M7)                                        |
+-----------------------------------------------------------------+
|  Pure-Go HTTPS  (net/http + crypto/tls, stdlib, M5+M6)          |
+-----------------------------------------------------------------+
|  Pure-Go DNS resolver  (M5)                                     |
+-----------------------------------------------------------------+
|  Pure-Go DHCPv4 client  (M4)                                    |
+-----------------------------------------------------------------+
|  gvisor.dev/gvisor/pkg/tcpip  (LinkEndpoint, M3)                |
+-----------------------------------------------------------------+
|  Pure-Go virtio-net driver  (M1 discovery, M2 init+TX/RX)       |
+-----------------------------------------------------------------+
|  UEFI Boot Services — used ONLY for:                            |
|    * PCI IO enumeration (EFI_PCI_IO_PROTOCOL, M1)               |
|    * Memory allocation (already wired)                          |
|    * ExitBootServices + handoff (M8)                            |
+-----------------------------------------------------------------+
```

Rationale:

- **Autonomy.** No dependence on the firmware exposing a network
  protocol family that VZ (and likely other minimal-firmware
  hypervisors) doesn't ship. The only firmware service we still rely
  on is the low-level PCI device enumeration — much harder for a
  firmware to omit since the EFI loader itself needs it.
- **Decoupling from host.** The Go-side stack lives in our binary,
  versioned with our binary, with our security-fix lifecycle. We no
  longer inherit the EDK2 NetworkPkg's release cadence (which is
  multi-month and varies per hypervisor build).
- **Reusability beyond boot.** A pure-Go virtio-net + netstack is
  useful in `cloud-boot/init` (post-handoff initramfs work),
  `cloud-boot/uki` (UKI-time validation), and any other CGO-free TamaGo
  binary cloud-boot ships. Path X's firmware-protocol wrappers are
  scrap once we ExitBootServices.
- **Alignment with project doctrine.** cloud-boot's HARD RULES require
  pure Go, no CGO, no vendoring, building from source. A pure-Go
  netstack is the only direction consistent with that.

Costs we accept:

- **Implementation cost.** We re-implement what EDK2 NetworkPkg
  already does, on top of a pure-Go netstack. gvisor/netstack is
  production-grade (it runs gVisor sandbox traffic in Google
  production) but is not trivial to bring up under TamaGo. M2-M3 are
  the highest-risk milestones now (was M2 TLS under Path X).
- **Binary size.** A pure-Go HTTPS stack + netstack is several MB
  bigger than the Path X wrappers. Acceptable: we're well under the
  EFI image size limits of every firmware we target.
- **CPU cost on the bootstrap.** Software TCP + TLS handshake on a
  single core. Acceptable for a one-time fetch of kernel+initrd.

What we keep from the M0..M1 Path X work (salvaged in the M1-prep
commit):

- `efiCall` widened from 4 to 5 args across all four arches. Still
  needed for `LoadImage` (6 args) at M8 and for `EFI_PCI_IO_PROTOCOL`'s
  config-space accessors at M1.
- `memorymap_tamago.go` NULL-DescriptorVersion fix (R-M0a resolved).
- `efi_events.go` / `efi_events_tamago.go` — async-event plumbing,
  still useful for any `LoadImage`-style 5+-arg path.
- `protocols_tamago.go` — `LocateHandleBuffer` / `HandleProtocol` /
  `LocateProtocol` / service-binding `CreateChild` / `DestroyChild`.
  Used by M1 to walk every controller publishing `EFI_PCI_IO_PROTOCOL`.

What we drop:

- All `EFI_HTTP_PROTOCOL` / `EFI_DHCP4_PROTOCOL` / `EFI_DNS4_PROTOCOL`
  call-site bindings. `http_protocol.go` reverts to its M0 type
  surface (kept only because M5+ might use the spec'd GUID for
  diagnostic comparison; we will not call into the firmware HTTP).
- The Path X risk discussion (R-M2 TLS variability, R-M1 DHCP rebinding
  through `EFI_DHCP4_PROTOCOL`) — replaced by Path Y risks below.

Upstream references for the new direction (read before writing M1+
code):

- UEFI 2.10 spec, §13 (Protocols — PCI Bus Support).
- `edk2.git`:
  - `MdePkg/Include/Protocol/PciIo.h` (the `EFI_PCI_IO_PROTOCOL`
    function table, GUID, Pci/Mem/Io accessor unions, BAR
    attributes).
  - `MdePkg/Include/Uefi/UefiSpec.h` for `EFI_BOOT_SERVICES`
    offsets we already use.
  - `OvmfPkg/VirtioNetDxe/VirtioNet.h` and
    `OvmfPkg/VirtioNetDxe/VirtioNetInitRing.c` — EDK2's own virtio-net
    driver. Pattern reference for capability walk / queue init / MAC
    read; NOT a code copy.
- Virtio 1.1 spec (committee specification 01, 2019-04-11):
  - §4.1 "Virtio Over PCI Bus" — modern device layout, capability
    discovery via PCI cap list, the five VIRTIO_PCI_CAP_* kinds, BAR
    + offset addressing of the common/notify/ISR/device/PCI-cfg
    capabilities.
  - §5.1 "Network Device" — virtio-net device-specific config
    (MAC[6], status, max_virtqueue_pairs) and feature bits.
- For the gvisor netstack adapter at M3:
  - `gvisor.dev/gvisor/pkg/tcpip/stack` (`LinkEndpoint` interface).
  - `gvisor.dev/gvisor/pkg/tcpip/link/channel` (reference adapter
    pattern; ours is simpler — single virtio-net device).
- For the kernel handoff per arch (unchanged from the Path X plan):
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

## 3. Milestones M0 → M8 (Path Y)

Each milestone is a separate agent run with its own scaffolding +
tests + commit. M0 introduced the type surface and the
`GetMemoryMap` probe; M1..M8 build the Path Y stack one layer at a
time, from PCI device discovery up to Linux kernel handoff.

### M0 — Probe + type surface (done)

Deliverables (shipped in
[5b5573c](https://github.com/cloud-boot/tamago-uefi/commit/5b5573c) +
salvaged in
[cfa6dca](https://github.com/cloud-boot/tamago-uefi/commit/cfa6dca)):

- `uefiboard/ebs.go` — `ExitBootServices(mapKey uintptr) error` thunk.
- `uefiboard/memorymap.go` + `memorymap_tamago.go` —
  `GetMemoryMap()`, with the post-salvage 5-arg form passing a real
  `*uint32` DescriptorVersion (resolves R-M0a riscv64 fault).
- `uefiboard/http_protocol.go` — GUIDs + struct shapes for the EFI HTTP
  family. Retained as M0 type surface only; Path Y does NOT call into
  firmware HTTP.
- `phase2_probe` build tag → `GetMemoryMap` smoke test. Phase 1
  banner remains the default behaviour.

Acceptance (re-validated after the salvage): host `go test
./uefiboard/...` PASS, `task elf:*` + `task probe:elf:*` link on all
four arches.

### M1 — virtio-net device discovery + identity (this milestone)

**Deliverable.** `EFI_PCI_IO_PROTOCOL` bindings + a probe binary
(`phase2_pcienum` build tag) that walks every controller publishing
the protocol, reports VID/DID/Class/Subsystem/(Seg,Bus,Dev,Fn) for
each, identifies virtio devices (vendor 0x1AF4), walks their virtio
PCI capability list, and for virtio-net devices (DID 0x1000 legacy
or 0x1041 modern) reads and prints the MAC address from the
device-specific config.

Scope:

- `uefiboard/pci_io_protocol.go` — `EFI_PCI_IO_PROTOCOL_GUID`
  (`4cf5b200-68b8-4ca5-9eec-b23e3f50029a`) and the protocol function
  table. M1 uses `Pci.Read` / `Pci.Write` (config-space access),
  `GetLocation`, `Attributes`, `GetBarAttributes`. The IO / Mem /
  Map / Unmap accessors are stubbed at the type-surface level only;
  M2 wires them.
- `uefiboard/virtio_pci.go` — virtio PCI constants (vendor 0x1AF4,
  legacy DID 0x1000, modern DID 0x1041 for net), the five
  `VIRTIO_PCI_CAP_*` kinds (common / notify / ISR / device / PCI
  config), and a pure host-buildable capability-list walker.
- `phase2_pcienum.go` + `phase2_pcienum_stub.go` (build tag
  `phase2_pcienum`, mirrors the `phase2_probe` shape).

Risk this milestone validates:

- **R-M1'a** (new) — Does Apple VZ firmware publish
  `EFI_PCI_IO_PROTOCOL` handles for its virtio-net devices? If not,
  Path Y itself is in danger; we'd need to walk
  `EFI_PCI_ROOT_BRIDGE_IO_PROTOCOL` directly or use an entirely
  different enumeration path. **The M1 acceptance gate is: vfkit
  surfaces at least one virtio-net device with a non-zero MAC.**

Acceptance: probe prints "virtio-net 1AF4:1040 or 1AF4:1041 BAR4@…
MAC=…" on QEMU+EDK2 for all four arches, and on Apple VZ via vfkit
(arm64 only — Mac Apple Silicon host).

### M2 — virtio-net init + virtqueues + send/recv one frame

**Deliverable.** A pure-Go virtio-net driver capable of sending one
ARP request and receiving the reply. No TCP/IP stack yet — just raw
Ethernet frame in / frame out over the virtio rings.

Scope:

- Feature negotiation against the virtio-net device (VERSION_1, MAC,
  STATUS, MRG_RXBUF as a minimum).
- `EFI_PCI_IO_PROTOCOL.Mem.Read/Write` for BAR-mapped MMIO config.
- Virtqueue allocation (split-ring layout, 1.1 spec §2.6). Two
  queues: RX[0] and TX[1]. Indirect descriptors deferred.
- A blocking `SendFrame([]byte) error` and `RecvFrame() ([]byte,
  error)`.
- Memory: allocate ring buffers via
  `gBS->AllocatePages(EfiBootServicesData)`; lifetime ends at
  ExitBootServices.

Risks:

- Cache coherency on arm64 / riscv64 / loong64 when DMA-style writes
  cross between firmware and our Go-side ring buffers. UEFI requires
  the firmware-allocated memory to be cache-coherent for boot-services
  use; we don't add barriers, we lean on that.
- IRQ vs. polling. M2 polls the used-ring index (firmware doesn't
  give us an EFI_EVENT for virtio-net out of the box). Polling cost
  is acceptable for a one-time fetch.

Acceptance: an ARP request emitted by our driver gets an ARP reply
visible in our RX ring on QEMU+EDK2 (any arch) and on vfkit (arm64).

### M3 — gvisor netstack `LinkEndpoint` + ARP + IPv4 + ICMP echo

**Deliverable.** A `gvisor.dev/gvisor/pkg/tcpip/stack.LinkEndpoint`
adapter wired to the M2 virtio-net driver, with an IPv4 stack
configured for a static address. Acceptance gate: an ICMP echo to a
known host returns a valid reply through the stack.

Scope:

- `go.mod` adds `gvisor.dev/gvisor` as a normal dependency (not
  vendored — HARD RULE).
- `uefiboard/netlink.go` — `LinkEndpoint` wrapper. Pure Go, no
  unsafe beyond what gvisor itself uses.
- Wire stack: `stack.New` with `ipv4.NewProtocol`,
  `icmp.NewProtocol4`, attach the link endpoint, configure a static
  IP on a NIC handle.
- A `phase2_icmp` build tag → ICMP echo probe binary.

Risks this milestone validates:

- **R-M3'a** (new) — Does gvisor/netstack work under TamaGo? It uses
  `unsafe.Pointer` arithmetic and `sync` primitives (mutexes, atomic
  loads). TamaGo provides both, so this should work, but the gvisor
  build has historically depended on package-init runtime hooks that
  we should verify before committing to the dependency. **Acceptance
  gate: stack initialises and ICMP round-trips on QEMU.**

Acceptance: ICMP echo round-trips on QEMU+EDK2 (amd64 at minimum,
arm64 strongly preferred) and on vfkit (arm64).

### M4 — DHCPv4 client (pure Go)

**Deliverable.** A pure-Go DHCPv4 client over the netstack. RFC
2131. Acquires lease, parses options 1/3/6 (subnet, router,
DNS), reconfigures the netstack with the assigned IP + gateway, and
caches the DNS server list for M5.

Scope:

- `uefiboard/dhcp4.go` — pure Go DISCOVER / OFFER / REQUEST / ACK
  state machine, raw-UDP socket on the netstack (server port 67 /
  client port 68).
- Lease refresh deferred (the boot lifetime is < 60 s, well within
  lease times).

Acceptance: on QEMU+EDK2 with `-netdev user`, the probe acquires a
lease, prints the assigned IP, and the netstack can route ICMP
through the assigned gateway. On vfkit with `--device
virtio-net,nat`, same.

### M5 — DNS + HTTP GET (Go stdlib over the stack)

**Deliverable.** A plaintext HTTP/1.1 GET reaches a host-side
`python -m http.server` over our stack, using `net/http` with a
custom `Transport.DialContext` that resolves names through a pure-Go
DNS client and dials through the gvisor netstack.

Scope:

- `uefiboard/dns.go` — pure Go DNS A-record resolver over UDP/53,
  using the DHCP-obtained DNS server list.
- `uefiboard/http_client.go` — `net/http.Client` configured with a
  custom `Transport`. No firmware EFI_HTTP involvement.

Risk: TamaGo's `net/http` import surface — needs the standard
library to compile cleanly with `GOOS=tamago`. Pre-validate during
the M5 agent run.

Acceptance: probe fetches `http://10.0.2.2:8000/hello.txt`, prints
status code + body length + first 64 bytes.

### M6 — TLS + HTTPS GET

**Deliverable.** `crypto/tls` over our netstack; HTTPS GET against a
host-side server with a known self-signed cert pinned in our build.

Scope:

- One embedded CA cert (build-time constant). `tls.Config.RootCAs`
  populated; `InsecureSkipVerify` MUST be false.
- TLS 1.3 only.

Acceptance: probe fetches `https://10.0.2.2:8443/hello.txt` and
verifies status + body.

### M7 — OCI registry client

**Deliverable.** Minimal OCI distribution-spec v1.1 client. Manifest
fetch, blob fetch (with `Content-Range`/`Range` support), multi-arch
index resolution, content-digest verification on every byte, and
cosign-bundle signature verification on the manifest digest.

Scope:

- Port the pure-Go OCI pieces already prototyped in
  `cloud-boot/init` (do NOT modify that repo per task instruction —
  copy the file shapes by hand if needed).
- Embedded public key (build-time constant).

Acceptance: a UKI-style artifact (config + vmlinuz + initrd blobs)
round-trips manifest → blobs → in-memory.

### M8 — Linux EFI-stub handover (post-EBS)

**Deliverable.** Memory map → ExitBootServices → jump to the loaded
Linux kernel, per-arch.

Scope (same as the old Path X M4):

- `uefiboard/handoff_<arch>.s` — per-arch kernel entry shim.
- `uefiboard/initrd_protocol.go` — publish
  `EFI_LOAD_FILE2_PROTOCOL` under `LINUX_EFI_INITRD_MEDIA_GUID`.
- The ExitBootServices retry choreography (refresh `GetMemoryMap` on
  `EFI_INVALID_PARAMETER`).

Acceptance: a vanilla upstream Linux kernel boots from
`oci://localhost:5000/k8s-mainline:latest` on QEMU+EDK2 amd64 +
arm64, and on vfkit arm64. riscv64 + loong64 are expected to work
but may surface a firmware idiosyncrasy that becomes its own M8.x
finding.

## 4. Five validation checks before declaring shape A complete

These are not unit tests; they are end-to-end gates that block shipping.
Under Path Y, the gate names shifted (M3 → M7 for signature verification;
M4 → M8 for EBS handoff) but the substance of each gate is the same.

1. **Multi-arch parity.** All four arches reach a Linux login prompt
   under QEMU/OVMF using the same EFI binary build pipeline (modulo the
   per-arch `BOOT<ARCH>.EFI` artifact). Recorded under
   `cloud-boot/iso`'s `task test:multiarch:boot` extended with a
   `phase2-oci` profile. **arm64 additionally must pass on Apple VZ
   via vfkit** — Path Y's whole motivation.
2. **Signature verification correctness.** Tampering one byte in the
   manifest OR a blob OR the signature MUST cause the loader to halt
   *before* `ExitBootServices`, with the failure printed to ConOut.
   A negative-path test fixture is part of M7's CI.
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
   backoff with jitter, capped at N retries (M7 sets N), then halt.
   Soak with a host-side faulty proxy.

## 5. Risks

### R-M1'a (HIGH, this milestone) — Does VZ expose `EFI_PCI_IO_PROTOCOL` for virtio-net?

The whole Path Y plan rests on UEFI exposing per-controller PCI IO
handles for the platform's virtio devices. On EDK2 (QEMU + OVMF /
EDK2-stable202408 + virt) this is the normal way: the PciBus driver
walks the root bridge, publishes an `EFI_PCI_IO_PROTOCOL` instance
per function, and we `LocateHandleBuffer(EFI_PCI_IO_PROTOCOL_GUID,
...)` to find them. Apple VZ uses its own firmware (not EDK2) and
its protocol coverage is documented as sparse on networking
(`docs/tutorials/vfkit.md`).

**Open question this milestone validates.** Does VZ ship at least one
`EFI_PCI_IO_PROTOCOL` handle whose Pci.Read returns vendor 0x1AF4 +
device 0x1040/0x1041 ? If yes, M2..M8 carry forward. If no, we need
to investigate alternatives:

- Walk `EFI_PCI_ROOT_BRIDGE_IO_PROTOCOL` (UEFI 2.10 §13.2) directly
  and enumerate PCI config space ourselves.
- Use `gBS->LocateHandleBuffer(AllHandles, NULL, NULL, &count, &h)` +
  iterate every protocol on every handle, looking for any
  virtio-flavoured GUID.
- As a last resort: speak to the virtio-net device through
  `EFI_SIMPLE_NETWORK_PROTOCOL` if VZ publishes it — but that
  re-introduces a firmware-stack dependency Path Y was trying to
  remove.

**Acceptance gate**: the M1 vfkit run reports at least one virtio-net
device with a non-zero MAC. If it doesn't, the M1 agent run STOPS
and reports.

**Live M1 finding 2026-06-07 (QEMU+EDK2)**: `EFI_PCI_IO_PROTOCOL` IS
published by EDK2 stable202408 on QEMU on every arch tested. amd64
ran the full probe end-to-end: 7 handles enumerated, 1 virtio-net at
(Seg,Bus,Dev,Fn) = (0,0,2,0) with DID 0x1000 (transitional), all 5
VIRTIO_PCI_CAP_* entries detected, DeviceCfg locator at
BAR4+offset=8192. arm64 / riscv64 / loong64 enumerated handles
(arm64=3, riscv64=1, loong64=3) but then triggered R-M1'b (below).

**Live M1 finding 2026-06-07 (Apple VZ via vfkit)**: the binary
boots; vfkit's `virtio-serial,logFilePath=` sink captured no output.
The VZ EFI firmware's ConOut binding does not appear to route to a
host-visible virtio-console port. Without observable output we
cannot confirm whether VZ publishes `EFI_PCI_IO_PROTOCOL`. The M1
acceptance gate (vfkit reports virtio-net MAC) is therefore
**inconclusive** on VZ as of this milestone. Two possible
resolutions: (a) reach the netstack in M3 and exfiltrate over UDP;
(b) probe an alternative pre-EBS observability channel (memory-mapped
ring buffer the host inspects post-shutdown). Tracked as a M2 / M3
prerequisite.

### R-M1'b (NEW, MEDIUM, this milestone surfaced) — Per-arch HandleProtocol divergence

The salvaged `protocols_tamago.go` `HandleProtocol` thunk works
end-to-end on amd64 (validated by the M1 probe processing all 7 PCI
handles cleanly) but faults on arm64 / riscv64 / loong64 with a
non-canonical / misaligned function-pointer call target:

|  arch    | LocateHandleBuffer | HandleProtocol | Fault address                |
| -------- | -----------------: | -------------: | ---------------------------- |
| amd64    |     7 handles, OK  |   OK (full M1) | n/a                          |
| arm64    |     3 handles, OK  |        FAULT   | ELR = 0x910003FDA9BB7BFD     |
| riscv64  |     1 handle,  OK  |        FAULT   | sepc = 0x00880E0A2E486715C   |
| loong64  |     3 handles, OK  |        FAULT   | ERA = 0x29C0E07802FEC063     |

The fault PCs on all three failing arches contain instruction-bytes
patterns when decoded — i.e., the thunk dereferenced a code address
as if it were a function-pointer slot. Hypotheses to investigate
in M2:

* `efiBSHandleProtocol = 152` is correct per UEFI 2.10 (spec-pinned),
  but the per-arch EDK2 build may pad the gBS struct differently.
  Confirm by re-deriving the offset against the locally-running EDK2
  binary (`OpenProtocol` at +280 might land differently; spot-check).
* The arm64 / riscv64 / loong64 thunks load 8 bytes via `MOVD (R8),R9`
  vs amd64's `CALL (AX)` (which is x86's `call qword ptr [rax]`).
  Semantically identical, but the latter is a single uop and
  forecloses any intervening register clobber. Investigate whether
  the load itself races with anything Go-side.
* The `bs` value captured by `getBootServices()` may have been
  reloaded incorrectly between LocateHandleBuffer and HandleProtocol
  on the failing arches (TamaGo's GC moving package vars between
  println and HandleProtocol — should not happen for `uint64` vars
  in .data, but verify).

M1 still ships the PCI IO bindings (validated on amd64) + the cap
walker (host-tested, 97.4% coverage). The arm64 / riscv64 / loong64
HandleProtocol fault gates the END-TO-END virtio-net identity demo
on those arches; that gating is held over to M2's investigation.

### R-M3'a (MEDIUM) — gvisor/netstack under TamaGo

`gvisor.dev/gvisor/pkg/tcpip` is the only mature pure-Go TCP/IP stack
in the ecosystem. It uses `unsafe.Pointer` arithmetic and `sync`
primitives heavily. TamaGo provides both, so this should work, but:

- gvisor relies on `init()` ordering and a few package globals that
  expect a normal Linux/POSIX runtime. TamaGo's `goos=tamago` may
  miss a hook.
- gvisor's `tcpip/link/*` adapters assume socket-style FDs in places
  (rawfile, fdbased). We use the `channel` adapter or write our own
  `LinkEndpoint` directly — no FDs involved.

**Mitigation if it doesn't compile/run under TamaGo at M3:** drop
gvisor and write a minimal pure-Go IPv4 + TCP + UDP stack ourselves
(scope: ARP, IPv4 send/recv, UDP for DHCP+DNS, TCP for HTTP). This
is a few weeks of work — preferred over Path X relapse.

### R-M0 (resolved) — `GetMemoryMap` quirks + riscv64 NULL-deref

Two facets, both resolved in the M0+salvage commits:

- **Stride.** UEFI's `GetMemoryMap` returns an output `DescriptorSize`
  that callers MUST respect (descriptors may be *larger* than
  `sizeof(EFI_MEMORY_DESCRIPTOR)` if the firmware includes
  implementation-private fields after the spec'd ones). Our parser
  strides by `DescriptorSize`. M0 unit test
  `TestParseMemoryMap_LargerStride` covers both 40-byte and 48-byte
  strides.
- **R-M0a (riscv64 NULL-DescriptorVersion fault, resolved).** M0
  observed:

  | arch    | descriptors | DescriptorSize | Conventional RAM | comments                |
  | ------- | ----------: | -------------: | ---------------: | ----------------------- |
  | amd64   |         119 |             48 |      ~2.09 GiB   | -m 2048                 |
  | arm64   |          31 |             48 |      ~4.22 GiB   | -m 4096                 |
  | loong64 |          51 |             48 |      ~4.18 GiB   | -m 4096                 |
  | riscv64 |        FAIL |              — |              —   | store-access page-fault |

  Root cause: the 4-arg `efiCall` thunk left A4 with whatever value
  the Go runtime had spilled there. EDK2 stable202408's
  `CoreGetMemoryMap` reads A4 as the `DescriptorVersion` OUT pointer
  and stores `*DescriptorVersion = EFI_MEMORY_DESCRIPTOR_VERSION` on
  the success path. amd64 / arm64 / loong64 happened to leave the
  position zero so EDK2's NULL guard kicked in; riscv64 didn't get
  that luck. **Fix (shipped in
  [cfa6dca](https://github.com/cloud-boot/tamago-uefi/commit/cfa6dca))**:
  widen `efiCall` to 5 args across all four arches and pass a
  real `*uint32` for DescriptorVersion. Re-validation in the M1
  agent's regression run.

### R-M8 — Per-arch handoff calling conventions (unchanged from Path X plan)

The four arches do NOT share an entry shape:

- amd64 has TWO entries (legacy 32-bit setup + 64-bit EFI handover);
  only the latter has been stable in upstream Linux since v6.x.
- arm64 + riscv64 + loong64 each have their own EFI stub entry +
  expected register state.

The M8 plan pins the canonical Linux references per arch; if a
given Linux release diverges (it has happened on riscv64), document
the divergence here.

## 6. Open questions

Per milestone, revisit at the start of the M-N agent run.

### M0

- *Resolved 2026-06-07*: `GetMemoryMap` may return `EFI_BUFFER_TOO_SMALL`
  on the first call; the canonical pattern is "try, grow, retry". The
  M0 wrapper implements this. Confirmed against EDK2's
  `MdeModulePkg/Library/UefiBootManagerLib/BmMisc.c` (`BmGetMemoryMap`).

### M1 (Path Y — virtio-net device discovery)

- Does VZ publish `EFI_PCI_IO_PROTOCOL` handles? See R-M1'a. Probe
  answers this in the M1 run.
- Legacy (DID 0x1000) vs modern (DID 0x1041) virtio-net: QEMU+EDK2
  exposes both depending on `-device` qualifier. VZ is expected to
  expose modern only. Probe prints both DIDs to be sure.
- IPv6: Phase 2 ships v4-only. Track v6 as a follow-up.

### M2 (virtio-net init + queues)

- Cache-coherency requirements for the ring buffers on arm64 /
  riscv64 / loong64. UEFI Boot Services memory is meant to be
  cache-coherent; we don't add barriers and lean on that. Verify on
  the first non-amd64 run.
- MTU: stick to 1500 in M2; M3 may renegotiate via netstack.

### M3 (gvisor netstack)

- Does gvisor's `tcpip` package compile cleanly under
  `GOOS=tamago`? See R-M3'a. If not, swap in a hand-rolled minimal
  stack.
- Static IP for the M3 probe, or skip directly to M4 DHCP? Plan:
  static (10.0.2.15) on QEMU `user` net, defer DHCP to M4.

### M4 (DHCPv4)

- Lease refresh: skip in M4 (boot lifetime < 60 s). Track as a
  follow-up if a registry pull stretches past lease time.

### M5 (DNS + HTTP)

- Does TamaGo's `net/http` import surface compile clean? Pre-verify
  at the start of M5.

### M6 (TLS)

- Embedded root CAs: ship a single self-signed CA at build time
  (constant). `InsecureSkipVerify` MUST be false in shipped builds.

### M7 (OCI client)

- OCI registries chunk blob responses with `Content-Range`/`Range`.
  Required for >2 GiB blobs, not for kernels. Skip in M7, add an
  M7.1 follow-up issue.
- Cosign vs. notation vs. raw PGP for signatures: cosign is the
  current de-facto; the bundle format is stable.

### M8 (EFI-stub handover)

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
- The cosign-compatible signing format we will verify in M7 is
  documented in the
  [Sigstore bundle spec](https://github.com/sigstore/protobuf-specs).

## 8. Changelog

- **2026-06-07** (M0): doc seeded; scaffolding code + `phase2_probe`
  build tag landed in `cloud-boot/tamago-uefi`. `GetMemoryMap` works
  end-to-end on amd64 / arm64 / loong64 under QEMU/EDK2-stable202408
  — counts + per-type RAM totals print to ConOut, then halt. **One
  surprise**: riscv64 EDK2 faults inside the firmware when
  `DescriptorVersion` is NULL — see §5 R-M0a. Mitigation deferred
  to the next milestone (extend `efiCall` to 5-arg arity, which we
  already need for `LoadImage` at the end of the arc). amd64 / arm64
  / loong64 carry forward unblocked.
- **2026-06-07** (Path Y pivot + M1): Path X (UEFI Boot Service
  network protocols) abandoned — Apple VZ firmware does not expose
  `EFI_HTTP_PROTOCOL` / `EFI_DHCP4_PROTOCOL` / `EFI_DNS4_PROTOCOL` /
  `EFI_TCP4_PROTOCOL` / `EFI_TLS_PROTOCOL`, so the multi-hypervisor
  contract that motivates shape A cannot be met by Path X. Path Y
  adopted: pure-Go virtio-net + gvisor/netstack + Go stdlib DHCP /
  DNS / HTTP / TLS, layered above the lowest-level UEFI services
  (PCI IO enumeration + AllocatePages + ExitBootServices). Salvage
  commit
  [cfa6dca](https://github.com/cloud-boot/tamago-uefi/commit/cfa6dca)
  keeps the 5-arg `efiCall`, the GetMemoryMap NULL-DescriptorVersion
  fix (resolves R-M0a — riscv64 GetMemoryMap now succeeds), and the
  protocol-handler service-binding helpers. M1 lands the
  `EFI_PCI_IO_PROTOCOL` bindings + virtio PCI capability discovery
  + `phase2_pcienum` probe. M1..M8 milestone list rewritten in §3.
- **2026-06-07** (M1 live findings): R-M1'a partially answered —
  EDK2-stable202408 on QEMU publishes `EFI_PCI_IO_PROTOCOL` on every
  arch; M1 amd64 ran end-to-end (7 handles, virtio-net 1AF4:1000 at
  (0,0,2,0), all 5 VIRTIO_PCI_CAP_* entries walked, DeviceCfg
  locator BAR4+offset=8192). VZ via vfkit boots the binary but its
  ConOut routing did not surface output through
  `virtio-serial,logFilePath` — VZ-side M1 acceptance gate
  inconclusive; observability fix tracked as an M2/M3 prerequisite.
  R-M1'b NEW: per-arch HandleProtocol fault on arm64 / riscv64 /
  loong64 (amd64 works); investigation deferred to M2. R-M0a
  end-to-end RESOLVED (riscv64 ran past GetMemoryMap, into the M1
  enumeration before R-M1'b's HandleProtocol fault).
