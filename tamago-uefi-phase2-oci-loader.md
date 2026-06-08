---
title: TamaGo UEFI Phase 2 — OCI pre-boot loader (shape A)
status: design / in-progress (M0..M1.6 done; M2 SHIPPED + LIVE-VALIDATED 4/5 cells; R-M2b RESOLVED; R-M2c CLOSED 2026-06-08 — Apple VZ gates virtio-net for non-OS clients, Path D ships on QEMU+EDK2 only; **R-M3'a CLOSED 2026-06-08 — gvisor pkg/tcpip compile-clean under TamaGo but runtime CRASH (CpuDxe #GP) on QEMU+EDK2 amd64 before our dispatcher runs. M3 gvisor work archived on branch `m3-gvisor-archive`. M3-minimal in progress: hand-rolled pure-Go ARP+IPv4+ICMP+UDP+TCP stack, ~3000 LOC, BSD-3.**)
last-updated: 2026-06-08
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

- `efiCall` widened from 4 to 5 args at M1, then from 5 to 6 args
  at M2 across all four arches. The 5→6 widening is driven by
  `EFI_PCI_IO_PROTOCOL.Mem.Read/Write`, whose six-arg signature
  `(This*, Width, BarIndex, Offset, Count, Buffer*)` is the M2
  virtio-net rail's only MMIO entry point. `LoadImage` (6 args)
  at M8 also fits this envelope.
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

### M1.5 — R-M1'b re-validation + SNP enumeration (done 2026-06-07)

**Deliverable.** Two things shipped:

1. **R-M1'b RESOLVED.** Re-validate the salvaged `protocols_tamago.go`
   thunk against `c6f2716` from a clean rebuild, confirm all 4 arches
   PASS `phase2_pcienum` end-to-end under QEMU+EDK2-stable202408
   (homebrew qemu 10.2.2). Diagnosis recorded in §5 R-M1'b: the
   committed source is correct; the original M1 fault was a stale-
   binary artifact, not a code bug.

2. **SNP enumeration probe.** A sibling `phase2_snpenum` build tag
   that, when set with or without `phase2_pcienum`, walks every
   handle publishing `EFI_SIMPLE_NETWORK_PROTOCOL`, follows
   `*This->Mode`, and prints `HwAddressSize` / `MediaPresent` /
   `State` / `IfType` / `CurrentAddress` (MAC) /
   `PermanentAddress` for each. No driver implementation; the SNP
   wrapper as a `LinkEndpoint` is a future M-step if VZ/UEFI mix
   justifies it. The combined PCI+SNP build is wired as
   `pcisnp:efi:<arch>` in the Taskfile.

Scope:

- `uefiboard/simple_network_protocol.go` — host-buildable surface:
  `EFI_SIMPLE_NETWORK_PROTOCOL_GUID`
  (`A19832B9-AC25-11D3-9A2D-0090273FC14D`), the protocol function-
  table offsets (Revision / Start / Stop / ... / WaitForPacket /
  Mode), the `EFI_SIMPLE_NETWORK_MODE` Go struct mirror of the
  656-byte (Go `unsafe.Sizeof`) firmware-side block, and
  `EFI_MAC_ADDRESS` as a 32-byte buffer. Reference:
  `MdePkg/Include/Protocol/SimpleNetwork.h` (edk2.git
  stable/202408) lines 23..671 and
  `MdePkg/Include/Uefi/UefiBaseType.h` lines 95..97.
- `uefiboard/simple_network_protocol_test.go` — GUID round-trip,
  on-the-wire byte assertion, function-table-offset pinning,
  state-constant pinning, MAC-address sizeof check, full Mode
  struct-layout assertion (every field offset pinned with
  `unsafe.Offsetof`), and a synthetic-buffer MAC-read round-trip.
- `phase2_snpenum.go` (live, `phase2_snpenum && tamago`) +
  `phase2_snpenum_helpers.go` (host-buildable) +
  `phase2_snpenum_stub.go` (stub for builds without the live
  path) + `phase2_snpenum_test.go` (probe-level MAC-hex round-trip
  + synthetic Mode-buffer test).
- `phase2_pcienum.go` refactored so its `runPhase2Probe` becomes
  `runPCIEnumProbe`; `phase2_dispatch.go` owns `runPhase2Probe`
  and calls `runPCIEnumProbe()` then `runSNPEnumProbe()` when
  either probe tag is set. Both can run together in one boot.

Validation matrix (QEMU+EDK2 stable202408, homebrew qemu 10.2.2):

|  arch    | PCI handles | virtio-net via PCI | SNP handles | MAC seen           |
| -------- | ----------: | ------------------: | ----------: | ------------------ |
| amd64    |           6 | 1 (0,0,2,0)         |           3 | 52:54:00:12:34:56  |
| arm64    |           3 | 1 (0,0,1,0)         |           3 | 52:54:00:12:34:56  |
| loong64  |           3 | 1 (0,0,1,0)         |           1 | 52:54:00:12:34:56  |
| riscv64  |           1 | 0 (root bridge only)|           1 | 52:54:00:12:34:56  |

riscv64 is the only arch where virtio-net is NOT visible through
`EFI_PCI_IO_PROTOCOL`; it IS visible through `EFI_SIMPLE_NETWORK_
PROTOCOL` regardless of whether QEMU exposes the device as
`virtio-net-pci` or `virtio-net-device`. This is a known EDK2-side
binding gap recorded as **R-M1.5x** in §5; M2 must detect and
fall back to SNP on riscv64.

Apple VZ via vfkit 0.6.3: R-M1'a observability gap still
**inconclusive**. Three vfkit `virtio-serial` variants tried and
recorded in §5 R-M1'a (`logFilePath` captures 0 bytes, `stdio`
errors out, `pty` errors out). Block IO side-channel deferred to
M1.6 (below).

### M1.6 — VZ observability via Block IO side-channel (done 2026-06-07)

**Deliverable shipped.** A pure-Go `EFI_BLOCK_IO_PROTOCOL` binding
in `uefiboard/` plus a side-channel print-tee that mirrors every
ConOut byte into a 32 KiB ring buffer, flushed to LBA 0 of a
host-pre-staged virtio-blk scratch disk via the firmware's Block
IO `WriteBlocks` + `FlushBlocks`. The host then reads the disk
file post-halt and recovers the probe output. **R-M1'a CLOSED.**
(See R-M1'a in §5 for the full VZ capability matrix surfaced by
the side-channel.)

Source layout (committed):

- `uefiboard/block_io_protocol.go` — type surface: GUID
  `964E5B21-6459-11D2-8E39-00A0C969723B`, function-table offsets
  (Revision=0 / Media=8 / Reset=16 / ReadBlocks=24 / WriteBlocks=32
  / FlushBlocks=40), `EFI_BLOCK_IO_MEDIA` mirror. Cite:
  `MdePkg/Include/Protocol/BlockIo.h` (edk2.git stable/202408)
  lines 15..18 (GUID), 128..200 (Media), 214..230 (protocol struct).
- `uefiboard/block_io_protocol_tamago.go` — live `BlockIOMedia` /
  `BlockIOReadBlocks` / `BlockIOWriteBlocks` / `BlockIOFlushBlocks`
  thunks via the salvaged 5-arg `efiCall` (same `bs + offset`
  idiom as M1.5).
- `uefiboard/blkprintk.go` — `BlkRingBuffer` (32 KiB ring with
  wrap, 16-byte header `(writeCount, payloadLen)`, monotonic
  counter, auto-flush at 4 KiB). Host-buildable; 100 % unit-tested
  including wrap, decode round-trip, and oversized-frame rejection.
- `uefiboard/blkprintk_tamago.go` — `Flush` (calls
  `BlockIOWriteBlocks` + `BlockIOFlushBlocks`) and `BlkPrintk(b)`
  (per-byte append + sentinel-or-threshold auto-flush).
- `uefiboard/board.go` — `printk` now mirrors to `BlkSink` when
  non-nil; default `nil` preserves Phase-1 ConOut-only behaviour
  bit-for-bit.
- `phase2_blkprintk.go` — probe gated on `phase2_blkprintk`:
  walks `EFI_BLOCK_IO_PROTOCOL` handles, reads LBA 0 of each
  writable bare-disk, picks the first with the 16-byte
  `BlkPrintkScratchMagic = "cloudboot-M1.6\0\0"`, binds the ring.
- `phase2_dispatch.go` — extends the M1/M1.5 dispatcher to run
  `runBlkPrintkSetup` BEFORE the PCI walk + SNP walk so the tee
  captures both, and `runBlkPrintkTeardown` AFTER so the sentinel
  flush lands the final frame.
- `cmd/blkprintk-seed` — host CLI to pre-stage a scratch disk with
  the magic marker. Required before the QEMU/VZ launch.
- `cmd/blkprintk-recover` — host CLI to decode the scratch file
  post-halt and print the recovered payload.

Build target:
`task blkprintk:all` → `BOOTX64-BLKPRINT.EFI`,
`BOOTAA64-BLKPRINT.EFI`, `BOOTRISCV64-BLKPRINT.EFI`,
`BOOTLOONGARCH64-BLKPRINT.EFI`.

Launch protocol (vfkit example):

```sh
go run ./cmd/blkprintk-seed -out scratch.img -size-mib 1
# stage esp.img with BOOTAA64.EFI = the BLKPRINT EFI
vfkit --memory 2048 \
  --bootloader efi,variable-store=nvram.fd,create \
  --device virtio-blk,path=esp.img \
  --device virtio-blk,path=scratch.img \
  --device virtio-net,nat,mac=52:54:00:01:02:03 \
  --device virtio-serial,logFilePath=vm.log
# after halt:
go run ./cmd/blkprintk-recover -in scratch.img
```

**Magic-marker safety.** The probe never writes to the ESP
(boot disk) on any hypervisor, because the host pre-stages the
sentinel `"cloudboot-M1.6\0\0"` ONLY on the dedicated scratch
image. Any other writable bare-disk handle the firmware exposes
(typically the ESP image when the ISO is also a virtio-blk drive)
fails the magic check and is skipped. The protection is
unconditional — bypassing it requires explicit host action to
write the magic to a non-scratch disk.

Acceptance gate met on all 4 QEMU+EDK2 arches AND on Apple VZ via
vfkit 0.6.3 (arm64-only on this Mac). R-M1'a CLOSED with full
capability matrix; see §5.

### M2 — virtio-net init + virtqueues + send/recv one frame

**Path choice: Path Y'' adopted.** Per the M1.6 capability matrix
(QEMU+EDK2 amd64/arm64/loong64: PCI IO + SNP; QEMU+EDK2 riscv64:
SNP only; Apple VZ: PCI IO only), no single rail covers all
five cells. M2 implements the **virtio-net pure-Go rail** (Path Y).
M2.1 follows with the **SNP wrapper** (Path Y'). M2.2 ships the
**runtime chooser** that selects between them per-handle.

|  hypervisor / arch       |  PCI IO  |   SNP   |  M2 (virtio-net pure-Go) |  M2.1 (SNP wrapper)  |
| ------------------------ | :------: | :-----: | :----------------------: | :------------------: |
| QEMU+EDK2 amd64          |    yes   |   yes   |          PRIMARY         |       fallback       |
| QEMU+EDK2 arm64          |    yes   |   yes   |          PRIMARY         |       fallback       |
| QEMU+EDK2 loong64        |    yes   |   yes   |          PRIMARY         |       fallback       |
| QEMU+EDK2 riscv64        |  NO\*    |   yes   |        not viable        |       PRIMARY        |
| Apple VZ (vfkit) arm64   |    yes   |   NO    |          PRIMARY         |     not viable       |

\* riscv64 EDK2's PciBus driver binds the root bridge but not
virtio-net; see R-M1.5x. M2.1's SNP wrapper is the chosen
work-around (an EDK2 patch is being staged in parallel but the
SNP path is already universally available).

**Deliverable.** A pure-Go virtio-net driver capable of sending one
ARP request and receiving the reply. No TCP/IP stack yet — just raw
Ethernet frame in / frame out over the virtio rings.

**M2 changelog (2026-06-07).**

- **`uefiboard/pci_mem_io.go`** — `EFI_PCI_IO_PROTOCOL.Mem.Read/Write`
  thunks at 8/16/32/64-bit widths, routing through the M2-widened
  6-arg `efiCall` envelope. Reference: MdePkg/Include/Protocol/PciIo.h
  (edk2.git stable/202408).
- **`uefiboard/eficall_<arch>.s`** — widened 5→6 args on all four
  architectures (amd64 stack slot at [RSP+0x28]; arm64 X5; loong64
  A5; riscv64 A5). All existing call sites updated to pass `0` for
  the new trailing slot. The widening continues the M1 invariant
  ("every position MUST hold a defined value") that fixed the
  riscv64 EDK2 stale-A4 fault. The Go declaration is now
  `func efiCall(fn, a0, a1, a2, a3, a4, a5 uint64) (status uint64)`.
- **`uefiboard/alloc_pages.go`** — `gBS->AllocatePages` /
  `gBS->FreePages` thunks (UEFI 2.10 §7.2). M2 uses
  `EfiBootServicesData + AllocateAnyPages` for virtqueue + DMA buffer
  allocations (lifetime ends at ExitBootServices, exactly what M2
  needs).
- **`uefiboard/virtio_modern.go`** + **`virtio_modern_tamago.go`** —
  Virtio 1.1 §4.1.5.1 COMMON_CFG register layout + the parsed
  `VirtioModernConfig` struct + `InitVirtioModernConfig(pciIO)`
  which walks the capability list, locates COMMON / NOTIFY / ISR /
  DEVICE / PCI cfg caps, reads the
  `notify_off_multiplier` from the extended NOTIFY_CFG cap (Virtio
  1.1 §4.1.4.4), and returns a populated config. Per-register
  accessors (DeviceFeatures64, SetDriverFeatures64, DeviceStatus,
  SelectQueue, QueueSize, QueueNotifyOff, SetQueueDesc/Driver/Device,
  SetQueueEnable, NotifyQueue) route through PciIo.Mem.Read/Write
  against (CommonCfgBAR, offset+reg). `DeviceCfgRead8` enforces the
  R-M1.6a bounds-check (VZ ships length=17 vs QEMU's larger).
- **`uefiboard/virtqueue.go`** + **`virtqueue_tamago.go`** — split
  virtqueue layout per Virtio 1.1 §2.6 (descriptor table, available
  ring, used ring on one contiguous AllocatePages allocation; 4-byte
  alignment on the used ring; `unsafe.Slice` views for direct read/
  write of the on-the-wire layout). `Virtqueue.PostAvail` publishes
  via an `atomic.StoreUint32` on the 4-byte avail-ring header word —
  this gives release semantics on every supported Go arch (amd64
  TSO, arm64 STLR, loong64 dbar, riscv64 fence rw,w). `PollUsed`
  pairs it with an `atomic.LoadUint32` acquire on the used-ring
  header. The cache-coherency story documented in
  `virtqueue_tamago.go`: UEFI 2.10 §2.3.x requires
  EfiBootServicesData to be cache-coherent during Boot Services,
  which is the only window M2 runs in.
- **`uefiboard/virtio_net.go`** + **`virtio_net_tamago.go`** —
  `OpenVirtioNet(pciIO)` performs the full 7-step Virtio 1.1 §3.1.1
  init sequence: RESET → ACKNOWLEDGE → DRIVER → feature
  negotiation (we accept VIRTIO_NET_F_MAC + VIRTIO_NET_F_STATUS +
  VIRTIO_F_VERSION_1; nothing else — including NOT
  VIRTIO_NET_F_MRG_RXBUF so the RX path is single-buffer-per-packet)
  → FEATURES_OK + verification → rxq (idx 0) + txq (idx 1) setup
  → DRIVER_OK → MAC read. RX ring pre-posted with 16 × 1518-byte
  buffers + notify. `TransmitFrame` prepends the 12-byte
  `virtio_net_hdr` (Virtio 1.1 §5.1.6 — always 12 bytes on a
  VERSION_1 device regardless of MRG_RXBUF), enqueues to txq,
  notifies, polls for completion. `ReceiveFrame` polls the rxq used
  ring, strips the header, refills the descriptor's buffer.
- **`phase2_virtionet_tx.go`** — new M2 probe binary, gated on
  `-tags phase2_virtionet_tx` and composing with `phase2_blkprintk`
  for VZ observability. Locates the first VID:DID 1AF4:1041 modern
  virtio-net device, opens it, transmits **two** ARP requests
  (QEMU NAT `10.0.2.15 → 10.0.2.2` AND VZ NAT
  `192.168.64.2 → 192.168.64.1`; the wrong-subnet broadcast is
  harmless), polls for replies for 3 attempts × 500000 iterations,
  prints the first 64 bytes + source MAC of every captured frame.
- **`Taskfile.yaml`** — `virtionet:elf:<arch>` + `virtionet:efi:<arch>`
  + `virtionet:all` targets. Tag set composed:
  `linkcpuinit,linkramstart,phase2_pcienum,phase2_snpenum,phase2_blkprintk,phase2_virtionet_tx`.
- **Tests.** `virtio_modern_test.go` (12 tests; COMMON_CFG register
  offsets, ParseVirtioCaps happy/edge/error paths including R-M1.6a
  VZ-shape DeviceCfg length=17, PerQueueNotifyOffset arithmetic,
  status + feature bit values). `virtqueue_test.go` (15 tests; layout
  arithmetic at size=16 and size=256, descriptor read/write
  round-trips, AddBuffer + PostAvail + PollUsed state machine,
  ring-wrap behaviour over 5 fill/drain rounds, ErrQueueFull /
  ErrInvalidIdx surfacing). `virtio_net_test.go` (12 tests; header
  prepend/strip round-trip, feature acceptance happy/missing-VERSION_1/
  missing-MAC paths, MAC6 stringification, queue indices, accepted
  feature mask). Coverage: **uefiboard 98.3%** (up from M1.6's 98.0%);
  virtio_modern.go 98.6%, virtio_net.go 97.5%, virtqueue.go 99.1%.

**Live validation results (2026-06-07).** Five-cell live boot
campaign executed against the M2 EFI binaries built from
`task virtionet:all` (cloud-boot/tamago-uefi@5f4951e). Harness:
homebrew `qemu-system-{x86_64,aarch64,loongarch64,riscv64}` 10.x
with the matching homebrew EDK2 firmware at
`/opt/homebrew/share/qemu/edk2-*.fd`
(`edk2-x86_64-code.fd`, `edk2-aarch64-code.fd`,
`edk2-loongarch64-code.fd`, `edk2-riscv-code.fd`) plus vfkit 0.6.3
for the Apple VZ cell. ESP image per arch is a 16 MiB FAT with
`/EFI/BOOT/BOOT<ARCH>.EFI` = the M2 `BOOT<ARCH>-VIRTIONET.EFI`.
Wall-clock measured from QEMU/vfkit launch to first `DONE` byte
on serial (QEMU) or to scratch-disk-flush of the probe's terminal
line (VZ).

| cell                     | init | TX | RX (frame)                                  | wall-clock to DONE | status |
| ------------------------ | :--: | :-: | :----------------------------------------- | :----------------: | :----: |
| QEMU+EDK2 amd64          |  OK  | OK | ARP reply from `52:55:0a:00:02:02` (NAT GW) |       1.87 s       |  PASS  |
| QEMU+EDK2 arm64          |  OK  | OK | ARP reply from `52:55:0a:00:02:02` (NAT GW) |       5.92 s       |  PASS  |
| QEMU+EDK2 loong64        |  OK  | OK | ARP reply from `52:55:0a:00:02:02` (NAT GW) |       5.59 s       |  PASS  |
| QEMU+EDK2 riscv64 (transitional) | n/a | n/a | n/a (clean "no modern virtio-net" halt) |       6.54 s       |  PASS (M2.1 deferred) |
| Apple VZ vfkit arm64 (initial 2026-06-07 boot) | FAIL | -  | -                                           |       ~0.2 s       |  FAIL (R-M2b) |
| Apple VZ vfkit arm64 (post-R-M2b 2026-06-07)   | OK   | FAIL (poll budget) | n/a (TX never published a used-ring entry) |       ~30 s        |  PARTIAL — init OK, TX deferred (R-M2c) |
| Apple VZ vfkit arm64 (post-R-M2c narrow 2026-06-07) | OK | FAIL (device never reads avail; Case IV) | n/a (verified via 50000-poll byte-dump) |       ~50 s        |  PARTIAL — R-M2c open (Case IV) |

**M2 milestone status: 4/5 PRIMARY cells fully PASS; VZ cell INIT-OK
after R-M2b RESOLVED; VZ TX OPEN (R-M2c diagnosed as Case IV — see
below).** The VZ rail can now bring the modern virtio-net device all
the way through DRIVER_OK, publishes the device MAC, and writes the
negotiated feature mask `0x100010028` (= `MTU | MAC | STATUS |
VERSION_1`) successfully — but the busy-poll path doesn't observe a
used-ring entry within the M2 poll budget on either TX ARP. The
R-M2c live diagnostic narrow (also 2026-06-07) confirmed via
byte-level dumps that the device never reads the avail ring at all,
regardless of doorbell width (uint16 / uint32), doorbell offset
(per-queue / shared), feature-mask width (narrow / wide), or DMA
window (high / 2-GiB-RAM). Production VZ acceptance therefore stays
DEFERRED; R-M2c is open as a documented Case IV. Recommended path
forward: skip M2 on VZ and adopt M2.1's SNP wrapper (which the VZ
firmware does publish — see §5).

**Post-R-M2c-narrow regression run (2026-06-07).** Re-built
`BOOT<ARCH>-VIRTIONET.EFI` with the R-M2c instrumentation +
defensive `PciIO.Attributes(Enable, Memory|BusMaster)` +
TX-poll-budget bump (10000 → 200000). All four QEMU+EDK2 cells
re-tested live; ARP echo from `52:55:0a:00:02:02` received in
RX attempt 0 on every PRIMARY cell. RISC-V cell binds the modern
virtio-net under `disable-legacy=on,disable-modern=off` (the M2-era
"bonus finding") and also passes end-to-end. No regression.

| cell                     | post-R-M2c result |
| ------------------------ | :---------------: |
| QEMU+EDK2 amd64          | PASS (TX OK, ARP reply RX) |
| QEMU+EDK2 arm64          | PASS (TX OK, ARP reply RX) |
| QEMU+EDK2 loong64        | PASS (TX OK, ARP reply RX) |
| QEMU+EDK2 riscv64 (modern) | PASS (TX OK, ARP reply RX) |
| Apple VZ vfkit arm64     | PARTIAL — R-M2c Case IV open |

The post-R-M2b VZ cell also re-confirms the M1.6 side-channel: the
diagnostic dump (`vnet device feats: lo=0x300119ab hi=0x00000005`)
plus the full M2 init trace + the TX/RX timeout lines all land on
the scratch disk and recover cleanly via `cmd/blkprintk-recover`.

The QEMU NAT MAC `52:55:0a:00:02:02` (transcribed `52:55:0a:00:02:02`)
is QEMU user-mode networking's synthetic gateway MAC for the default
`10.0.2.0/24` subnet (`10.0.2.2 = 0a:00:02:02` with the
`52:55` user-mode OUI prefix), exactly as expected.

Key per-cell findings:

- **amd64 / arm64 / loong64** — all three PRIMARY cells pass M2's
  full acceptance: device located at VID:DID `0x1AF4:0x1041`, init
  sequence runs cleanly through DRIVER_OK, negotiated features
  `0x100010020` = `MAC | STATUS | VERSION_1` exactly as designed,
  TX of both probe ARPs (10.0.2.x and 192.168.64.x) succeeds, the
  QEMU-flavoured one elicits a reply within RX attempt 0 (always
  64 bytes, the second ARP's broadcast lands in the wrong subnet
  and is silently dropped — RX attempt 1 times out, expected).
  The probe then halts on `DONE`.

  Caveat (build flag): QEMU's default `-device virtio-net-pci` (no
  qualifiers) publishes the **transitional** virtio device (VID:DID
  `0x1AF4:0x1000`), which M2's probe correctly rejects because the
  modern PCI-cap layout the driver depends on isn't published in
  legacy mode. The five PASS QEMU+EDK2 cells above (and the M2
  acceptance criterion as stated in §3 M2) all use `-device
  virtio-net-pci,...,disable-legacy=on,disable-modern=off`, which
  forces the modern device. This requirement was not previously
  spelled out in §3 M2 — flagged below as a documentation gap, not
  a code defect; M2.2's runtime chooser will need to surface this
  to operators. Confirmed by a control run on amd64 without
  `disable-legacy=on`: the M2 probe surfaces "no modern virtio-net
  device found among 7 handles" and halts on `DONE` — identical
  shape to the riscv64 transitional cell — i.e. M2's behaviour
  when only a legacy device is present is the *intended* clean
  diagnostic.

- **riscv64 (transitional)** — under the default `-device
  virtio-net-pci,...` (no `disable-legacy=on`), the device surfaces
  as `0x1AF4:0x1000` (legacy/transitional). The M2 probe correctly
  prints `"no modern virtio-net device found among 2 handles — M2
  rail does not apply to this hypervisor"` and halts on `DONE`. No
  spin, no fault. This is the M2 acceptance shape for riscv64 per
  the §3 capability matrix; production riscv64 traffic will run on
  M2.1's SNP wrapper.

  **Surprise bonus finding — R-M1.5x is more nuanced than recorded.**
  When the same probe is launched against `-device
  virtio-net-pci,...,disable-legacy=on,disable-modern=off`,
  EDK2-stable202408 on riscv64 (qemu 10.2.2)
  **does** bind the modern virtio-net to `EFI_PCI_IO_PROTOCOL`
  (VID:DID `0x1AF4:0x1041` at (0,0,1,0) with the 4 standard virtio
  caps) and the M2 probe runs the **full** virtio-net rail
  end-to-end — init OK, TX OK, ARP reply from `52:55:0a:00:02:02`
  captured in RX attempt 0, 6.52 s wall-clock to `DONE`. So R-M1.5x
  is not an unconditional EDK2 riscv64 PciBus binding gap — it's a
  legacy-device binding gap. Modern (`disable-legacy=on`)
  virtio-net IS bound. Updated below.

- **Apple VZ (vfkit) arm64** — **NEW M2 FAILURE surfaced**.
  Block-IO side-channel recovers the full probe output (R-M1.6
  end-to-end PASS confirmed once more). PCI IO enumeration finds 5
  handles: Apple host bridge `0x106B:0x1A05` + modern virtio-net
  `0x1AF4:0x1041` (Rev 0x01) at (0,0,1,0) + virtio-rng `0x1043` +
  two virtio-blk `0x1042`. The virtio-net device has the 4 standard
  caps with DeviceCfg length=17 (R-M1.6a shape). The M2 probe
  picks the device, enters `OpenVirtioNet`, but fails at step 5
  (FEATURES_OK verification):

  ```text
  phase2-virtionet-tx: M2 — pure-Go virtio-net rail
  phase2-virtionet-tx: LocateHandleBuffer(EFI_PCI_IO_PROTOCOL_GUID)
  phase2-virtionet-tx: handles= 5
  phase2-virtionet-tx: found modern virtio-net at handle 2404546456 VID:DID = 0x1af4 : 0x1041
  phase2-virtionet-tx: bringing up device (init sequence per Virtio 1.1 §3.1.1)
  phase2-virtionet-tx: OpenVirtioNet FAILED: uefi: virtio-net: FEATURES_OK status bit didn't stick after DriverFeature write
  ```

  Diagnosis: per Virtio 1.1 §3.1.1, after the driver writes
  FEATURES_OK to DeviceStatus it MUST read back DeviceStatus and
  abort if the device cleared FEATURES_OK — which signals the
  device rejected the driver's feature subset. M2 negotiates
  `MAC | STATUS | VERSION_1` only (mask
  `VirtioNetAcceptedFeatures` = `(1<<5) | (1<<16) | (1<<32) =
  0x100010020`). The QEMU+EDK2 PASS cells reach
  `negotiated features (hex) = 0x100010020` and DRIVER_OK — so
  the negotiation logic is correct; VZ's virtio-net implementation
  evidently *requires* additional bits the M2 mask doesn't accept.
  Most likely candidate: `VIRTIO_F_ACCESS_PLATFORM` (bit 33, AKA
  `VIRTIO_F_IOMMU_PLATFORM`), which modern hardened backends often
  require (the driver MUST then route DMA through the platform
  IOMMU translation — for our case, the EFI Boot Services
  identity-mapped allocation already satisfies the contract). A
  secondary candidate is `VIRTIO_F_RING_PACKED` (bit 34) — Apple's
  newer virtio backends have been observed to default to
  packed-ring; that one is heavier (M2's split-ring layout would
  need to be rewritten). Without per-device feature dumping
  (which M2's probe doesn't expose — it only prints the
  negotiated mask after the AND), we cannot disambiguate from the
  side-channel output alone; that's tracked as the immediate M2.1
  prerequisite. Filed as **R-M2b** below.

  Recovery shape: the probe halts cleanly via the M1.6 sentinel
  ("phase2-blkprintk: final flush via sentinel byte" appears in the
  recovered output). No spin, no fault — VZ runtime stays
  responsive until the harness kills the VM.

Scope:

- Feature negotiation against the virtio-net device (VERSION_1, MAC,
  STATUS). MRG_RXBUF intentionally NOT accepted (simplifies RX —
  single-buffer-per-packet).
- `EFI_PCI_IO_PROTOCOL.Mem.Read/Write` for BAR-mapped MMIO config.
- Virtqueue allocation (split-ring layout, 1.1 spec §2.6). Two
  queues: RX[0] and TX[1]. Indirect descriptors deferred.
- A blocking `TransmitFrame([]byte) error` and `ReceiveFrame(budget
  int) ([]byte, error)`.
- Memory: allocate ring buffers via
  `gBS->AllocatePages(EfiBootServicesData)`; lifetime ends at
  ExitBootServices.

Risks:

- Cache coherency on arm64 / riscv64 / loong64 when DMA-style writes
  cross between firmware and our Go-side ring buffers. UEFI requires
  the firmware-allocated memory to be cache-coherent for boot-services
  use; we don't add manual barriers — we lean on that, plus the
  release-store/acquire-load on the avail/used ring header words via
  `sync/atomic`.
- IRQ vs. polling. M2 polls the used-ring index (firmware doesn't
  give us an EFI_EVENT for virtio-net out of the box). Polling cost
  is acceptable for a one-time fetch.
- **R-M1.6a (LOW, RESOLVED 2026-06-07 by M2 live boots)** — VZ's
  virtio-net device-cfg `length=17` is shorter than QEMU's. M2's
  `DeviceCfgRead8` enforces the bounds-check against
  `cfg.DeviceCfgLength`. Live VZ boot confirms the M1.6a shape
  (DeviceCfg length=17 recovered from the side-channel) and the
  M2 init reaches `OpenVirtioNet` past the cfg-cap walk without
  any cap-bounds violation. (The init then fails further on for
  an unrelated reason — R-M2b below.) MAC read (offset 0..5) is
  well within VZ's 17-byte cfg, as predicted. The bounds-check
  fires only on hypothetical fields past offset 17 which neither
  M2 nor any current cell exercises.
- **R-M2a (MEDIUM, RESOLVED 2026-06-07 by M2 live boots)** —
  `efiCall` was widened 5→6 args. Every existing call site was
  updated to pass `0` for the new trailing slot. Live boot
  campaign across all 5 cells exercised the widened envelope
  through the entire Phase-1 cpuinit + runtime bring-up + M0
  GetMemoryMap + M1 PCI walk + M1.5 SNP walk + M1.6 Block-IO
  + M2 virtio-net Mem.Read/Write + AllocatePages paths. No
  stale-register dereference, no firmware fault, no MS-x64
  stack-slot ABI mismatch surfaced on any arch. The amd64 cell
  in particular ran the full 6-arg envelope against
  `PciIo.Mem.Read/Write` thousands of times during the virtio
  init + ARP RX without incident. Closed.
- **R-M2b (HIGH, RESOLVED 2026-06-07 by accepted-features-mask widening).**
  Live VZ boot surfaced this regression seam: after `OpenVirtioNet`
  wrote `MAC | STATUS | VERSION_1` to DriverFeature and set
  FEATURES_OK on DeviceStatus, the read-back DeviceStatus had
  FEATURES_OK cleared — the device rejected our negotiated subset.

  Diagnosis (live empirical narrow): extended `phase2_virtionet_tx`
  to dump the device-offered feature bitmap before the AND mask via
  the M1.6 blkprintk side-channel (the dump is `vnet device feats:
  lo=0xXXXXXXXX hi=0xXXXXXXXX`, recoverable on VZ via
  `cmd/blkprintk-recover`). VZ vfkit 0.6.3 arm64 publishes
  `lo=0x300119ab hi=0x00000005` — i.e. set bits =
  `{0, 1, 3, 5, 7, 8, 11, 12, 16, 28, 29, 32, 34}`. Two observations
  from this bitmap:

    * VZ **does** offer `VIRTIO_F_RING_PACKED` (bit 34); this is the
      validation report's primary hypothesis.
    * VZ does **NOT** offer `VIRTIO_F_ACCESS_PLATFORM` (33),
      `VIRTIO_F_NOTIFICATION_DATA` (38), `VIRTIO_F_RING_RESET` (40),
      `VIRTIO_F_IN_ORDER` (35), `VIRTIO_F_ORDER_PLATFORM` (36),
      or `VIRTIO_F_SR_IOV` (37) — so none of those can be the
      required bit.

  A second-pass diagnostic in the same probe iteratively walks each
  candidate accepted-features mask and reads DeviceStatus after
  writing FEATURES_OK, reporting which mask survives the handshake.
  The narrow established empirically that:

    * Baseline `MAC | STATUS | VERSION_1` (= `0x100010020`): **FAILS**
      (FEATURES_OK clears, status reads back `0x03` = ACK | DRIVER).
    * "Everything offered except RING_PACKED" (= `lo=0x300119ab,
      hi=0x00000001`): **STICKS** (status reads `0x0b` = ACK |
      DRIVER | FEATURES_OK). So `RING_PACKED` is **NOT** required —
      this is **Case A**, not Case B from the validation report's
      decision tree.
    * Iterating "+single-bit-on-baseline" through every offered bit:
      ONLY `+bit3 (VIRTIO_NET_F_MTU)` makes FEATURES_OK stick. Every
      other simpler bit candidate (CSUM, GUEST_CSUM, GUEST_TSO4,
      GUEST_TSO6, HOST_TSO4, HOST_TSO6, HASH_REPORT, plus the
      reserved bit 29) on its own leaves FEATURES_OK cleared.

  **Fix:** widen `uefiboard.VirtioNetAcceptedFeatures` in
  `virtio_net.go` to include `VIRTIO_NET_F_MTU` (bit 3). The bit is
  informational per Virtio 1.1 §5.1.3 (the device publishes its MTU
  in `virtio_net_config.mtu`; the driver MAY read it); M2 doesn't
  read the field and continues to use the default `VirtioNetMaxFrameSize`
  (= 1518 bytes) for the rxq buffer size. On QEMU+EDK2 the bit is
  NOT offered by the device unless the operator sets
  `host_mtu=` on the `virtio-net-pci` device — so accepting it is a
  no-op on the four QEMU PASS cells (negotiated mask stays
  `0x100010020` exactly as before).

  Diff shape:
    * `uefiboard/virtio_modern.go`: add
      `VirtioNetFeatureMTU uint64 = 1 << 3`.
    * `uefiboard/virtio_net.go`: extend
      `VirtioNetAcceptedFeatures = VirtioNetFeatureMTU | …` (one OR).
    * `uefiboard/virtio_net_test.go`: pin the mask shape + add a
      `TestAcceptFeatures_DeviceMissingMTU` test that exercises the
      QEMU path (device doesn't offer MTU; negotiation succeeds
      without it).
    * `uefiboard/virtio_modern_test.go::TestFeatureBits`: pin
      `VirtioNetFeatureMTU = 1 << 3`.

  Live post-fix VZ boot confirms FEATURES_OK sticks
  (`status=0x0b`), `OpenVirtioNet` returns success, the device MAC
  is read cleanly (`52:54:00:11:22:33`), negotiated features are
  `0x100010028` (= `MTU | MAC | STATUS | VERSION_1`). The TX poll
  on VZ then exhausts its budget without observing a used-ring
  entry — that's a new, separate finding tracked as **R-M2c**
  below; the R-M2b regression seam (FEATURES_OK rejection) is
  closed.

  The diagnostic feature-dump is kept in production (4 lines in
  `phase2_virtionet_tx.go` after the PCI handle is located: open a
  transient `VirtioModernConfig`, reset, ACK | DRIVER, read
  `DeviceFeatures64`, print the two 32-bit halves). It's a useful
  one-line "what does this host's virtio-net offer" smoke test for
  any future hypervisor cell.

- **R-M2c (DIAGNOSED AS CASE IV, OPEN — 2026-06-07) — VZ vfkit
  virtio-net TX descriptor never returns on the used ring.**
  Surfaced by the post-R-M2b VZ boot (init OK, device UP, MAC read,
  FEATURES_OK stuck). After `TransmitFrame` writes the descriptor,
  posts the avail-ring index, and writes the per-queue notify
  doorbell, the busy-poll on the used-ring `idx` exhausts its
  budget (now 200000 polls, bumped from the M2-initial 10000) without
  observing the device's publish.

  **Live diagnostic narrow (2026-06-07).** Extended
  `phase2_virtionet_tx.go` with a full byte-level dump of every
  layer between the driver and the device, recovered via the M1.6
  blkprintk side-channel. The dump captures:

    1. **PCI command register pre+post `OpenVirtioNet`** = `0x0016`
       on VZ (MemEn=1, BusMaster=1). EDK2's PciBus driver
       pre-enables both attributes at firmware bind time; explicit
       `PciIO.Attributes(Enable, Memory|BusMaster)` is a no-op
       (kept as a defensive guard regardless). Bus-master-not-enabled
       hypothesis: **RULED OUT**.

    2. **Per-queue notify cap geometry**: `BAR=0`,
       `offset=0x4000`, `length=8`, `multiplier=4`. RX doorbell at
       `BAR0+0x4000`, TX doorbell at `BAR0+0x4004`. Address
       arithmetic matches Virtio 1.1 §4.1.4.4 exactly.

    3. **Post-`setupQueue` register read-back** (re-select queue,
       read `QueueDesc`/`QueueDriver`/`QueueDevice`/`QueueEnable`):
       VZ correctly stored every 64-bit MMIO write. `QueueEnable`
       reads back as `0x0001` for both rxq and txq. The 64-bit
       address-publish step is **NOT** the bug.

    4. **TX descriptor + avail-ring header bytes after `AddBuffer`**:
       `desc[1] = addr=0xef065000, len=0x36, flags=0, next=0`
       (correct single-segment TX descriptor — VirtqDescF{Next,
       Write,Indirect} all clear, exactly as spec-compliant TX
       requires). `avail[0..8] = flags=0, idx=2, ring[0]=0,
       ring[1]=1` (correct: two descriptors published, idx
       incremented atomically with release semantics via
       `PostAvail`'s `atomic.StoreUint32` on the header word).

    5. **Used-ring bytes immediately before AND after notify, AND
       every 1000 polls through 50000**: `used[0..16] = all zeros`
       at every sample. `UsedIdx(atomic)` and `UsedIdx(raw)` always
       agree (`0x0000`) — cache-coherency hypothesis (Case II in
       the validation brief): **RULED OUT** (if a barrier-deficit
       were dropping our avail.idx visibility to the device, we'd
       also expect the symmetric used.idx visibility to be
       compromised; both directions read consistently from RAM).

    6. **Doorbell-shape sweep**: the probe submits three additional
       buffers and retries with (a) uint32@per-queue-offset,
       (b) uint16@offset-0 (shared-doorbell hypothesis), (c)
       uint32@offset-0. Each followed by a 5000-poll budget.
       **No combination produces a used-ring publication.** Case I
       (wrong notify width) and the spec's `multiplier=0` shared-
       doorbell interpretation: **RULED OUT**.

    7. **Wide-mask retry**: re-open the device with
       `VirtioNetAcceptedFeaturesNarrow` (everything VZ offers
       minus VIRTIO_F_RING_PACKED — i.e. all 11 set bits including
       the Apple-private bits 28 and 29). FEATURES_OK sticks
       cleanly (negotiated mask `0x1300119ab`), device reaches
       DRIVER_OK, but the same TX submission still produces no
       used-ring publication. Hypothesis "Apple wants additional
       feature bits acked beyond R-M2b's narrow set" (Case IV
       sub-hypothesis): **RULED OUT** at the FEATURES_OK level.

    8. **DMA address-window narrow**: a one-off branch swapped the
       virtqueue + DMA buffer allocator to
       `AllocatePagesBelow(EfiBootServicesData, count, 0xBFFFFFFF)`
       to force every allocation into the 2 GiB guest-RAM region
       (default `AllocateAnyPages` lands at `0xef000000+`, well
       above the 2 GiB RAM ceiling for `-memory 2048`). With
       allocations confirmed at `0xbfffe000 / 0xbffff000` (i.e.
       inside guest RAM), TX still doesn't progress. Hypothesis
       "VZ doesn't DMA to firmware-private high memory": **RULED OUT**.
       The defensive constant `VirtioDMAAddressCeiling=0xFFFFFFFF`
       + the `AllocatePagesBelow` helper are kept for future narrows;
       the production allocator stays on `AllocateAnyPages`.

  **Conclusion: Case IV.** The device accepts FEATURES_OK, stores
  every queue address correctly, has BusMaster + Memory enabled, and
  acknowledges DRIVER_OK on a clean status read-back — but VZ's
  host-side virtio-net implementation does NOT process avail-ring
  submissions from a UEFI-context client driving the device through
  `EFI_PCI_IO_PROTOCOL.Mem.Read/Write`. This aligns with the
  long-standing observation in the cloud-boot README about the
  loader: *"virtio-net rejects FEATURES_OK from any UEFI-context
  client"* — pre-R-M2b that was the FEATURES_OK clearance; post-R-M2b
  the rejection has moved to the TX dispatch path. The behaviour is
  consistent with Apple Virtualization.framework requiring its
  virtio-net device to be driven from a Linux-kernel-class guest
  rather than from UEFI Boot Services context.

  **Canonical R-M2c diagnostic recovery (VZ, 2026-06-07).** Excerpt
  from `cmd/blkprintk-recover -in scratch.img` showing every layer
  of the failed TX submission:

  ```text
  phase2-virtionet-tx: PCI command register (pre-open) = 0x0016 (MemEn=1 BusMaster=1)
  phase2-virtionet-tx: vnet device feats: lo=0x300119ab hi=0x00000005
  phase2-virtionet-tx: device UP. MAC = 72:20:43:d4:38:09
  phase2-virtionet-tx: negotiated features (hex) = 0x100010028
  phase2-virtionet-tx: PCI command register (post-open) = 0x0016 (MemEn=1 BusMaster=1)
  phase2-virtionet-tx: PciIO attributes (post-open) = 0xc600
  phase2-virtionet-tx: notify cfg: BAR=0 offset=0x4000 length=0x8 multiplier=0x4
  phase2-virtionet-tx: rxq notify_off=0x0000 doorbell BAR-offset=0x4000 base phys=0xef078000
  phase2-virtionet-tx: txq notify_off=0x0001 doorbell BAR-offset=0x4004 base phys=0xef077000
  phase2-virtionet-tx: readback rxq  QueueDesc=0xef078000  QueueDriver=0xef078100  QueueDevice=0xef078128  QueueEnable=0x0001
  phase2-virtionet-tx: readback txq  QueueDesc=0xef077000  QueueDriver=0xef077080  QueueDevice=0xef077098  QueueEnable=0x0001
  phase2-virtionet-tx: diag: pre-AddBuffer: NextAvailIdx=0x0001 UsedIdx(atomic)=0x0000 UsedIdx(raw)=0x0000
  phase2-virtionet-tx: diag: AddBuffer descIdx=0x0001
  phase2-virtionet-tx:   desc[0]= 00 50 06 ef 00 00 00 00 36 00 00 00 00 00 00 00   # addr=0xef065000 len=0x36 flags=0 next=0
  phase2-virtionet-tx:   avail[0..8]= 00 00 02 00 00 00 01 00                       # flags=0 idx=2 ring[0]=0 ring[1]=1
  phase2-virtionet-tx:   used[0..16] (pre-notify)= 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00
  phase2-virtionet-tx: diag: doorbell write: BAR=0 offset=0x4004 value=0x0001
  phase2-virtionet-tx: diag: notify OK; entering poll
  phase2-virtionet-tx:   used[0..16] (post-notify)= 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00
  ... 50 samples of "UsedIdx(atomic)=0x0000 UsedIdx(raw)=0x0000" elided ...
  phase2-virtionet-tx: diag: final DeviceStatus= 0x0f                              # ACK|DRIVER|FEATURES_OK|DRIVER_OK
  phase2-virtionet-tx: sweep uint32@perQ : no completion in 5000 polls
  phase2-virtionet-tx: sweep uint16@offset0 : no completion in 5000 polls
  phase2-virtionet-tx: sweep uint32@offset0 : no completion in 5000 polls
  phase2-virtionet-tx: wide-mask OpenVirtioNet OK. negotiated=0x1300119ab
  phase2-virtionet-tx: wide-mask transmitFrameDiag FAILED: TX poll timeout
  ```

  Every byte the driver wrote was stored correctly, every register
  the device exposes reads back as expected, and the device's
  status reflects DRIVER_OK — yet the TX descriptor is never
  consumed.

  **Plausible next steps (NOT attempted in this narrow):**

    a. Implement packed-ring (Virtio 1.1 §2.7) and ack
       VIRTIO_F_RING_PACKED. Apple offers it; the diagnostic-only
       `VirtioNetAcceptedFeaturesWithPacked` constant pins the
       shape for that future probe. This is a large rewrite —
       descriptor table, avail/used semantics, wrap counter — and
       defers from the M2 split-ring driver.

    b. Skip M2's virtio-net rail on VZ entirely and adopt the SNP
       wrapper (M2.1) when it exists. The VZ EFI firmware DOES
       publish EFI_SIMPLE_NETWORK_PROTOCOL (per the §5 capability
       matrix), so M2.1 will likely be the supported Apple-VZ road
       — exactly the position the README's loader-row already
       takes for Path D-J on VZ.

    c. Investigate vfkit / `Virtualization.framework`-side
       gating: does the framework expose a configuration knob to
       enable virtio-net for UEFI clients? Outside this repo's
       scope.

  **Acceptance** for R-M2c CLOSE: an ARP reply from
  `192.168.64.1` (vfkit's NAT gateway) appears in the M2 probe's
  RX poll on VZ. **Not met.** R-M2c is documented as Case IV +
  open; VZ remains BLOCKED at TX with the diagnostic dump as the
  canonical artifact. The QEMU 4-arch PASS cells are not affected
  (live regression confirmed 2026-06-07 — see "live validation
  results (post-R-M2c narrow)" below).

Acceptance: an ARP request emitted by our driver gets an ARP reply
visible in our RX ring on QEMU+EDK2 (any arch with PCI IO virtio-net,
configured `disable-legacy=on,disable-modern=off`). VZ init-OK
acceptance MET 2026-06-07 with the R-M2b mask widening (FEATURES_OK
sticks, OpenVirtioNet returns success, MAC reads cleanly); full VZ
ARP-echo acceptance DEFERRED to R-M2c (TX descriptor publish on the
used ring). riscv64 acceptance deferred to M2.1's SNP rail.

### M2.1 — SNP wrapper (Path Y' rail, pending)

**Deliverable.** A thin Go wrapper around the firmware's
`EFI_SIMPLE_NETWORK_PROTOCOL.Transmit` / `Receive` that exposes
the SAME `LinkEndpoint`-compatible TX/RX API as M2's virtio-net
driver. This is the riscv64 path (where PCI IO virtio-net doesn't
bind under stable/202408 EDK2) and the fallback path on the three
QEMU+EDK2 arches where SNP is also published.

Scope:

- `uefiboard/simple_network_protocol_tamago.go` (live thunks for
  Initialize / Transmit / Receive / Shutdown — the type-surface
  in `simple_network_protocol.go` is already in place from M1.5).
- `phase2_snp_tx` build tag → SNP-rail probe binary (mirror of M2's
  `phase2_virtionet_tx` probe but driving SNP instead of the
  virtio-net rail).
- Per-arch Taskfile targets.

Out of scope for M2.1: the runtime chooser between the two rails —
that's M2.2.

Acceptance: same ARP echo as M2, but specifically on QEMU+EDK2
riscv64 (the only platform where M2's virtio-net rail won't
apply).

### M2.2 — Unified LinkEndpoint + runtime chooser (pending)

**Deliverable.** A `uefiboard.LinkEndpoint` interface that both M2's
`VirtioNet` and M2.1's `SNPDriver` satisfy, plus a probe-time
chooser that:

1. Enumerates EFI_PCI_IO_PROTOCOL handles; if any is a modern
   virtio-net (VID:DID 1AF4:1041), use M2's rail on that handle.
2. Otherwise enumerate EFI_SIMPLE_NETWORK_PROTOCOL handles; if any
   is present + MAC non-zero, use M2.1's rail.
3. Otherwise return ErrNoNetworkAvailable and the caller surfaces
   a clean diagnostic.

The chooser is small (~50 LOC) and entirely runtime — no
build-tag splits, no per-hypervisor codepath at the user-visible
API.

Scope:

- `uefiboard/linkendpoint.go` — the interface + the chooser.
- `phase2_netchoose` build tag → unified probe binary running the
  same M2 ARP TX/RX through whichever rail the chooser picked.
- Acceptance: the unified probe runs successfully on all 5 cells
  of the M1.6 capability matrix.

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

### R-M1'a (RESOLVED 2026-06-07 M1.6) — VZ capability matrix surfaced via Block IO side-channel

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

**M1.5 re-validation 2026-06-07**: with a fresh rebuild of c6f2716,
all four arches now PASS the PCI IO probe end-to-end on QEMU+EDK2
(see R-M1'b RESOLVED below). amd64 = 6 handles, virtio-net 1AF4:1000
at (0,0,2,0); arm64 = 3 handles, virtio-net at (0,0,1,0); loong64 =
3 handles, virtio-net at (0,0,1,0); riscv64 = 1 handle (PCI root
bridge only — see R-M1.5x below for the riscv64-specific binding
gap). The R-M1'a question "does VZ expose EFI_PCI_IO_PROTOCOL on
virtio-net?" still requires the VZ-side observability fix tracked
below before it can be answered for VZ.

**Live M1 + M1.5 finding 2026-06-07 (Apple VZ via vfkit 0.6.3)**:
the M1 PCI-IO probe and the M1.5 PCI+SNP probe both boot on VZ
under vfkit but their ConOut output is not captured by any vfkit
observability sink that ships in the homebrew build (0.6.3). The
M1.5 agent tried:

* `--device virtio-serial,logFilePath=/tmp/log.txt` — VM runs, log
  file created but stays at 0 bytes. Same as the M1 result; the
  VZ EFI firmware does not bind ConOut to vfkit's virtio-serial
  port. (Run command: `vfkit -m 2048 --bootloader
  efi,variable-store=...,create --device virtio-blk,path=...iso
  --device virtio-net,nat,mac=52:54:00:01:02:03 --device
  virtio-serial,logFilePath=/tmp/vfkit-log.txt`.)
* `--device virtio-serial,stdio` — vfkit fails immediately with
  `Error: operation not supported by device`. The stdio backend
  needs a configured terminal that the harness's stdin can't
  provide.
* `--device virtio-serial,pty` — vfkit fails with `Error: unable to
  open "/dev/ptmx": device not configured`. The macOS sandbox the
  binary runs under doesn't expose `/dev/ptmx`.
* `mac=auto` shorthand — vfkit 0.6.3 rejects this with `Error:
  address auto: invalid MAC address`. Use an explicit MAC literal.
* Block-IO side-channel (probe writes its output to a known LBA on
  a writable virtio-blk; host inspects the disk file post-halt) —
  **NOT yet attempted**. Would require adding
  `EFI_BLOCK_IO_PROTOCOL` bindings + a write helper to
  `uefiboard/`; that's a small but non-trivial piece of code and
  is deferred to **M1.6 (NEW, see §3 placeholder)**. The Block IO
  path is promising because VZ exposes virtio-blk reliably (the
  M1.5 vfkit boots load `BOOTAA64.EFI` from a virtio-blk disk, so
  the firmware's Block IO stack works end-to-end).

The VZ-side M1 acceptance gate (vfkit reports virtio-net MAC) is
therefore **still inconclusive** as of M1.5. R-M1'a is held over
to M1.6, which lands the Block IO side-channel as the
observability mechanism.

**M1.6 resolution (2026-06-07).** The Block-IO side-channel
(see §3 M1.6) WORKS on Apple VZ — vfkit 0.6.3 exposes a writable
virtio-blk via `EFI_BLOCK_IO_PROTOCOL`, `WriteBlocks` succeeds,
`FlushBlocks` commits the data, and the post-halt scratch file
matches the probe's captured ConOut text exactly. The recovered
output reveals VZ's protocol coverage:

**VZ (vfkit 0.6.3) capability matrix on arm64:**

|  protocol                            | published?  | notes |
| ------------------------------------ | :---------: | ----- |
| `EFI_BLOCK_IO_PROTOCOL`              | **YES**     | 2+ handles (ESP, scratch); writes commit; closes R-M1'a. |
| `EFI_PCI_IO_PROTOCOL`                | **YES**     | 5 handles: Apple host bridge (0x106b:0x1a05), virtio-net (0x1af4:0x1041 — **modern**, not legacy), virtio-rng (0x1043), virtio-blk x2 (0x1042). |
| `EFI_SIMPLE_NETWORK_PROTOCOL`        | **NO**      | `LocateHandleBuffer` returns `EFI_NOT_FOUND` (0x800...0e). VZ firmware does not bind SNP to its virtio-net. |
| ConOut → `virtio-serial,logFilePath` | **NO**      | Log file stays at 0 bytes. Same on `stdio` / `pty` (vfkit errors out). |

**Recovered probe output** (excerpt from scratch.img after vfkit
run; full text in the M1.6 commit body):

```
phase2-pcienum: handles= 5
phase2-pcienum: handle 1 = ...
phase2-pcienum:   VID:DID = 0x1af4 : 0x1041 Class = 0x02/0x00/0x00 Rev = 0x01
phase2-pcienum:   --> VIRTIO device (vendor 0x1AF4)
phase2-pcienum:     CapList pointer = 0x40
phase2-pcienum:     cap 0 type= CommonCfg BAR= 0 offset= 0      length= 56
phase2-pcienum:     cap 1 type= ISRCfg    BAR= 0 offset= 4096   length= 1
phase2-pcienum:     cap 2 type= NotifyCfg BAR= 0 offset= 16384  length= 8
phase2-pcienum:     cap 3 type= DeviceCfg BAR= 0 offset= 32768  length= 17
phase2-pcienum: done. virtio-net devices found = 1
phase2-snpenum: LocateHandleBuffer FAILED: EFI_STATUS=0x800000000000000e
```

**Implication for Path Y.** On Apple VZ, the M2 pure-Go virtio-net
init path **is viable** via PCI IO — virtio-net is a modern (1.0+)
device, exposed through standard `EFI_PCI_IO_PROTOCOL`, with the
four spec-mandated virtio capabilities (CommonCfg, ISRCfg,
NotifyCfg, DeviceCfg) present and BAR-mapped. The MAC read is M2
work (needs `PciIo.Mem.Read` against BAR0+32768), unchanged from
the M1 design.

**Implication for an SNP-first prod implementation.** A
Path-Y'-only (SNP-first) implementation is **NOT a viable
single-path solution** because VZ doesn't publish SNP. To support
VZ at all, Path Y (pure-Go virtio-net) is required somewhere in
the implementation. See §3 M2 path-choice discussion.

### R-M1'b (RESOLVED 2026-06-07 M1.5) — Per-arch HandleProtocol divergence

**Status: RESOLVED.** The original M1 report flagged a non-canonical /
misaligned function-pointer call target on arm64 / riscv64 / loong64,
with fault PCs decoding to ARM / RISC-V / LoongArch instruction-byte
patterns (ELR = 0x910003FDA9BB7BFD on arm64, sepc = 0x00880E0A2E486715C
on riscv64, ERA = 0x29C0E07802FEC063 on loong64).

The M1.5 agent could not reproduce the fault on a clean rebuild of
the M1 commit (c6f2716) from a clean toolchain (TamaGo go1.26.3,
pectl at d2e185d). All four arches PASS the `phase2_pcienum` probe
end-to-end under QEMU+EDK2-stable202408 (homebrew qemu 10.2.2):

|  arch    | LocateHandleBuffer | HandleProtocol | Probe result            |
| -------- | -----------------: | -------------: | ----------------------- |
| amd64    |     6 handles, OK  |   OK (full M1) | virtio-net 1AF4:1000 at (0,0,2,0); 5 caps walked |
| arm64    |     3 handles, OK  |   OK (full M1) | virtio-net 1AF4:1000 at (0,0,1,0); 5 caps walked |
| loong64  |     3 handles, OK  |   OK (full M1) | virtio-net 1AF4:1000 at (0,0,1,0); 5 caps walked |
| riscv64  |     1 handle,  OK  |   OK (full M1) | root-bridge only — virtio-net not bound to PCI IO on riscv64 EDK2 (see R-M1.5x below) |

Stress test on loong64 ran 10 boots back-to-back, all PASS, no
fault recurrence.

**Diagnosis.** The committed source for the salvaged thunks
(`eficall_<arch>.s`) and the `protocols_tamago.go` wrappers is
correct as committed:

* The slot-address idiom (`fn = bs + efiBSHandleProtocol`, thunk does
  `MOVD (R8), R9; BL (R9)` — load function pointer from the slot, then
  indirect-call) is the same idiom `memorymap_tamago.go`'s
  `GetMemoryMap` uses on all 4 arches at offset 56, and that worked
  end-to-end under M0 (R-M0a resolved by the same salvage commit).
  Pinning the live values via a side-channel debug
  probe (`debug_tamago.go` scratch file used during M1.5
  investigation; deleted before commit) confirmed
  `bs = 0xF477B68` on loong64 and `*(bs+152) = 0xF454FFC` —
  a sane firmware code address in the same range as
  `*(bs+40) = 0xF45B6D4` (AllocatePages) and
  `*(bs+312) = 0xF454048` (LocateHandleBuffer). The gBS
  struct layout in EDK2-stable202408
  (`MdeModulePkg/Core/Dxe/DxeMain/DxeMain.c` lines 41..93) is
  spec-conformant; HandleProtocol sits at offset 152 exactly as
  `efi_events.go`'s `efiBSHandleProtocol = 152` claims.

* The 5-arg ABI is correct per arch (AAPCS64 X0..X4 on arm64; LP64
  A0..A4 on riscv64; LP64 R4..R8 on loong64; MS x64 RCX/RDX/R8/R9 +
  stack at [RSP+0x20] on amd64 with shadow space). All four thunks
  correctly load fn+0(FP) into the indirect-call register, then
  dereference and call.

* The probe-level call shape in `protocols_tamago.go`
  (`HandleProtocol`, `LocateHandleBuffer`, `LocateProtocol`,
  `ServiceBindingCreateChild`, `ServiceBindingDestroyChild`)
  matches `memorymap_tamago.go`'s shape and `pci_io_protocol_tamago.go`'s
  shape. No idiom misapplication.

**Best explanation for the M1 fault pattern.** The original M1
agent's PCIENUM binaries on disk at the time of the live-finding
report (2026-06-07 14:29) were a build snapshot pre-dating the
final state of commit c6f2716 — specifically a partially-rebuilt
state where some arch's PCIENUM EFI was older than its
matching `protocols_tamago.go` source. A clean `task pcienum:all`
followed by a fresh ISO and a fresh QEMU boot reproduces all 4
arches PASS, as the M1.5 validation matrix confirms. Hashes of
the M1.5-rebuilt artifacts are reproducible across rebuilds
(deterministic `-trimpath` Go output), so the M1.5 binary is the
canonical c6f2716 output.

**Hardening.** No code-level fix was needed — the salvaged thunks
and wrappers ship as-is. The M1.5 changelog adds the
`phase2_snpenum` probe (M1.5 deliverable) as an independent
cross-validation: SNP also goes through
`LocateHandleBuffer` + `HandleProtocol`, and it PASSES on all 4
arches (handle counts: amd64=3, arm64=3, loong64=1, riscv64=1;
MAC `52:54:00:12:34:56` consistent across all 4 — the QEMU
virtio-net default). If R-M1'b were a real per-arch idiom bug,
the SNP path would have surfaced it too.

### R-M1.5x (CONFIRMED + NARROWED 2026-06-07 by M2 live boots) — riscv64 EDK2 binds virtio-net to PCI IO only when the device is modern (disable-legacy=on)

On riscv64 QEMU `-machine virt` + edk2-stable202408, the M1.5
`phase2_pcienum` probe under the original test config (`-device
virtio-net-pci` with no qualifiers, i.e. transitional device
VID:DID `0x1AF4:0x1000`) sees only the PCI root bridge handle
(VID 0x1b36 = Red Hat QEMU); no virtio-net device shows up via
`EFI_PCI_IO_PROTOCOL`. The SNP walk DOES surface the same
virtio-net device (1 handle, MAC 52:54:00:12:34:56), regardless
of whether the device is configured as `virtio-net-pci` or
`virtio-net-device` (MMIO transport).

**M2 live-boot refinement (2026-06-07):** under `-device
virtio-net-pci,...,disable-legacy=on,disable-modern=off`,
which forces the device to publish only the **modern** PCI layout
(VID:DID `0x1AF4:0x1041`), EDK2-stable202408 on riscv64 with
QEMU 10.x **does** bind it to `EFI_PCI_IO_PROTOCOL` (2 handles
total: root bridge + virtio-net at (0,0,1,0) with all 4 standard
modern virtio caps walked), and the M2 probe runs the full
pure-Go virtio-net rail end-to-end (init OK, TX OK, ARP reply
from `52:55:0a:00:02:02` captured in RX attempt 0, halts on
`DONE` in 6.52 s). So R-M1.5x is not an unconditional
EDK2 riscv64 PciBus binding gap — it's a *legacy/transitional*
binding gap.

Implication update:
* For QEMU+EDK2 riscv64 deployments that can pass
  `disable-legacy=on,disable-modern=off`, the pure-Go M2 virtio-net
  rail IS viable on riscv64 (PRIMARY); M2.1's SNP wrapper becomes
  the fallback rather than the only option.
* For deployments that cannot (legacy backward-compatible
  hardware, older OVMF), M2.1's SNP wrapper remains the only
  riscv64 path.
* The capability matrix in §3 M2 should be amended to add a
  device-mode column. Deferred to M2.2's runtime chooser doc.

The original failure mode (no PCI IO virtio-net under
`-device virtio-net-pci` default = transitional) remains an
EDK2-side binding gap on the riscv64 leg: the PciBus driver doesn't
bind the PCI IO protocol to *legacy/transitional* virtio-net even
when the device is on a
PCI bus.

Implication for Path Y: on riscv64, **SNP must be the fallback**
when PCI IO doesn't surface the device. Either:
* M2 detects this case via `LocateHandleBuffer(SNP_GUID)` and
  uses SNP's `Transmit` / `Receive` to drive the device (gives
  up the pure-Go-from-driver-up plan on riscv64 only); or
* an EDK2 patch is staged upstream to extend the PciBus driver's
  bindings on riscv64.

Track this as a riscv64-specific divergence; amd64 / arm64 /
loong64 retain the PCI-IO-first plan.

### R-M2a (NEW, MEDIUM) — efiCall widening 5→6 introduces a regression seam

M2 widened `efiCall` from 5 to 6 arguments to fit
`EFI_PCI_IO_PROTOCOL.Mem.Read/Write`'s six-arg signature. Every
existing call site was updated to pass `0` for the new trailing
slot — Go's call-site arity check catches a missed update at
compile time, so a wrong-arity call CAN'T silently link. But there
are two failure modes that would NOT surface at compile time:

1. **Stale-register reuse on riscv64.** Same class of bug as M0/M1's
   4→5 widening: if any future caller passes uninitialised /
   nonsense in the new a5 slot, EDK2 on riscv64 may try to
   dereference it as a NULL-relative pointer and fault inside the
   firmware. Mitigation: caller code MUST pass an explicit `0`
   or a real pointer; never use a stale Go variable.

2. **MS x64 stack-slot ABI mismatch.** The amd64 thunk now stores
   the 5th and 6th args at `[RSP+0x20]` and `[RSP+0x28]` above the
   32-byte shadow space, with 48 bytes total reservation to keep
   RSP 16-byte aligned after the CALL push. A miscount of any
   constant here would clobber the firmware's stack. Mitigation:
   the thunk's frame size + every offset is asserted by inspection
   in `eficall_amd64.s`; regression watch via live boot of the
   M1.6 BlkPrintk EFI on QEMU+EDK2 amd64 (the most thoroughly-
   exercised path).

Status: **RESOLVED 2026-06-07 by M2 live boots**. All five cells
(QEMU+EDK2 amd64/arm64/loong64/riscv64-modern + Apple VZ vfkit
arm64) exercised the 6-arg envelope extensively — the QEMU
PASS cells ran `PciIo.Mem.Read/Write` thousands of times through
virtio init + ARP RX without any firmware fault, stale-register
crash, or stack-slot corruption. The VZ cell ran the same
envelope through the cap walk, COMMON_CFG init, DeviceFeatures64
read, and SetDriverFeatures64 write before failing for an
unrelated R-M2b reason. No symptom of either failure mode listed
below surfaced on any arch. Closed.

### R-M2b (NEW, HIGH) — Apple VZ virtio-net FEATURES_OK doesn't stick after MAC|STATUS|VERSION_1 negotiation

Surfaced by the M2 live-boot campaign on vfkit 0.6.3 (arm64).
The M2 probe locates VZ's modern virtio-net device (VID:DID
`0x1AF4:0x1041` at (0,0,1,0), 4 standard caps walked, DeviceCfg
length=17 = R-M1.6a shape), enters `OpenVirtioNet`, drives the
init sequence through RESET → ACKNOWLEDGE → DRIVER → feature
negotiation (writes `0x100010020` = MAC|STATUS|VERSION_1 to
DriverFeature) → writes FEATURES_OK to DeviceStatus → reads
DeviceStatus back and finds FEATURES_OK **cleared**. Per
Virtio 1.1 §3.1.1, this means the device rejected the driver's
feature subset. `OpenVirtioNet` returns `ErrFeaturesNotOK`; the
probe halts cleanly via the M1.6 sentinel.

Working hypothesis (NOT yet confirmed; requires a device-features
dump from a follow-up VZ run to disambiguate): VZ's virtio-net
implementation requires one or more of the following bits the
M2 mask does not accept:

* `VIRTIO_F_ACCESS_PLATFORM` (bit 33, AKA
  `VIRTIO_F_IOMMU_PLATFORM`) — hardened backends often require
  this; semantically requires the driver to route DMA through
  the platform IOMMU. In our case EFI BootServicesData is
  identity-mapped and the requirement is satisfied trivially —
  adding the bit to `VirtioNetAcceptedFeatures` should suffice
  without a driver semantic change.
* `VIRTIO_F_RING_PACKED` (bit 34) — newer Apple backends have
  been observed to default to packed-ring transport. If VZ
  requires this, the M2 split-ring layout in `virtqueue.go` needs
  a full rewrite to packed-ring semantics — significantly more
  work and a real M2.0.1 (not a 1-line mask widening).
* `VIRTIO_F_RING_RESET` (bit 40) — moderate complexity to honour.
* `VIRTIO_F_NOTIFICATION_DATA` (bit 38) — driver writes a 32-bit
  composite payload to NotifyCfg instead of the queue index;
  requires a small NotifyQueue change.

**Disambiguation** requires extending `phase2_virtionet_tx.go`'s
probe with a 1-line `println` immediately after
`cfg.DeviceFeatures64()` to dump the device-offered bitmap to
the M1.6 side-channel — VZ ConOut still doesn't route, so the
side-channel is the only observable path. Once the offered
bitmap is known, the mask in
`uefiboard/virtio_net.go::VirtioNetAcceptedFeatures` can be
widened to cover the required bit (under the constraint that
no driver-side semantic change is necessary; if the required
bit demands real driver work, it becomes M2.0.1 proper).

**Scope of the FAIL.** VZ is the only cell where M2's
acceptance gate is not met. The other 4 cells (3× QEMU+EDK2
PASS + riscv64 transitional clean-halt) are all green. The
production multi-hypervisor contract requires VZ; until R-M2b
is resolved, **Apple VZ users cannot run the pure-Go virtio-net
rail and have no SNP fallback** (VZ doesn't publish
`EFI_SIMPLE_NETWORK_PROTOCOL` — R-M1'a's original symptom
covers this). VZ is therefore the only platform where shape A
is currently blocked at M2.

Status: **HIGH**. Triage path:
1. Add 1-line device-features dump to `phase2_virtionet_tx.go`,
   rebuild VIRTIONET EFIs, re-boot VZ via vfkit + scratch
   side-channel; capture the offered bitmap.
2. If the required bit is ACCESS_PLATFORM or
   NOTIFICATION_DATA: widen
   `VirtioNetAcceptedFeatures` and re-validate (small M2.0.1
   patch, ~5 LOC).
3. If the required bit is RING_PACKED: rewrite `virtqueue.go`
   for packed-ring (M2.0.1 proper, ~300 LOC + tests).
4. Either way: re-confirm all 4 currently-PASS cells still PASS
   after the change.

### R-M2c (CLOSED 2026-06-08) — Apple VZ gates virtio-net TX for ALL non-OS clients

Two parallel experiments were run live on vfkit (Apple Silicon arm64)
to determine whether Apple VZ's virtio-net could be driven to TX by a
TamaGo unikernel client. Both **failed**.

**M2-A (RING_PACKED + packed-ring virtqueue, PR #1, branch
`m2-a-ring-packed`).** Accepts `VIRTIO_F_RING_PACKED` (bit 34) on the
feature mask; implements the packed-ring layout per Virtio 1.1 §2.7.
Under vfkit, `FEATURES_OK` sticks with RING_PACKED accepted
(`status=0x0f`), the descriptor table + driver-event-suppression
areas are populated, the doorbell is written — but the device never
flips the `USED` flag on the TX descriptor across 50 000 poll
iterations. The same M2-A binary on QEMU+EDK2 arm64 with
`-device virtio-net-pci,packed=on` works end-to-end (TX OK,
ARP reply from `52:55:0a:00:02:02`). So the packed-ring code is
sound; VZ is the failing side.

**M2-B (post-ExitBootServices direct MMIO, PR #2, branch
`m2-b-post-ebs`).** Captures all PCI cap / BAR / feature / ring
state pre-EBS via the M1.6 blkprintk side-channel, then calls
`gBS->ExitBootServices`, then drives virtio-net by direct
`unsafe.Pointer` MMIO from bare-metal Go (no Boot Services). The
pre-EBS dump on vfkit is clean — `writeCount=1 payloadBytes=4043`,
ending at "flushing blkprintk side-channel pre-EBS" — the last
println before the EBS call. vfkit runs the VM for the full 60 s
watchdog window after EBS (consistent with the probe sitting in
`postEBSHalt` spin loop). `sudo tcpdump -i bridge100 -n arp` for
60 s captures **no ARP marker** at src `169.254.2.66` / dst
`169.254.99.99`. The post-EBS TX submission did not result in a
frame on the wire.

**Joint interpretation.** Pre-R-M2c the cloud-boot loader README
had this note: *"virtio-net rejects FEATURES_OK from any
UEFI-context client"* on VZ. Combined with R-M2b's MTU-bit
discovery and now R-M2c's empirical finding that even a packed-ring
client (M2-A) and a post-EBS direct-MMIO client (M2-B) both fail,
the gate is broader than "UEFI-context": **Apple VZ's virtio-net
back-end services only OS-recognized guest drivers** (Linux's
in-kernel virtio-net being the verified working one). Anything
else — pre-EBS UEFI, post-EBS bare-metal, packed-ring, split-ring
— is silently dropped at the device boundary. This matches Apple's
broader pattern of gating VZ-emulated devices to "supported guest"
profiles.

**Verdict + scope clarification.**

- **Path D ships on QEMU+EDK2** (4 arches PASS end-to-end via the
  M2 split-ring rail; ARP reply observed on QEMU NAT).
- **VZ does NOT get Path D.** Path C (UKI menu-then-reboot) remains
  the Apple Silicon production rail.
- Both PRs (#1, #2) close as REFERENCE / archive. The branches
  stay (don't delete) so anyone else can resume from the captured
  context if the VZ device profile ever opens up.
- M3+ resume on the QEMU/cloud target only; we don't carry an
  unsupported-VZ branch through gvisor netstack, DHCP, OCI, etc.
- M2.1 (SNP wrapper) and M2.2 (chooser) become low-priority — SNP
  isn't published on VZ (R-M1'a matrix); on QEMU+EDK2 modern devices
  PCI IO covers all 4 arches. M2.1 retains marginal value for
  legacy-only firmware setups.

**Branch references for the archived work.**

- `cloud-boot/tamago-uefi` branches: `m2-a-ring-packed`,
  `m2-b-post-ebs` (last commits before close: M2-A `1252423`,
  M2-B `2fe25ae` after the Taskfile diagnosis round).
- Closed PR threads carry the empirical evidence for future readers.

### R-M3'a (CLOSED 2026-06-08) — gvisor/netstack incompatible with TamaGo+UEFI runtime

**Status: CLOSED — gvisor dropped. Falling back to a hand-rolled
minimal pure-Go IPv4 + UDP + TCP stack ("M3-minimal").**

Two-phase validation produced a misleading compile-clean pass and
then a hard runtime failure:

**Compile-clean check (Step 0+1, agent verdict 2026-06-08).** Pinned
gvisor at `v0.0.0-20260604230326-c7dbb92365cd` (last `go` branch HEAD
before the upstream-broken `pkg/tcpip/stack/bridge_test.go` of
2026-06-05). With that tag,
`GOOS=tamago GOARCH=<arch> go build` succeeds cleanly on
amd64 / arm64 / riscv64 with the standard
`linkcpuinit,linkramstart` build-tag set. loong64 fails at
`syscall/fd_tamago.go: undefined: write` — that's a tamago-pie
overlay gap (no `zsyscall_tamago_loong64.go`), filed separately as
R-M3'b. The agent's report described this as "gvisor compiles clean
under TamaGo on amd64 / arm64 / riscv64", which was true but
incomplete.

**Live runtime check (this milestone close, 2026-06-08).** The M3
`BOOTX64-NETSTACK.EFI` (gvisor LinkEndpoint adapter + ICMP-ping
probe, ~4.4 MB) booted under QEMU q35 + EDK2 stable202408. BdsDxe
loaded our EFI cleanly:

```
BdsDxe: loading Boot0001 "UEFI QEMU HARDDISK QM00011 " ...
BdsDxe: starting Boot0001 "UEFI QEMU HARDDISK QM00011 " ...
!!!! X64 Exception Type - 0D(#GP - General Protection) ...
RIP - 000000007EF6710C ... ImageBase=000000007EF56000 (CpuDxe.dll)
```

No `phase2-netstack-ping:` line ever fires — the #GP triggers
inside the firmware's `CpuDxe.dll` at offset `0x1110C` BEFORE our
binary reaches the dispatcher entry. The same QEMU command line
boots the M2 `BOOTX64-VIRTIONET.EFI` (no gvisor) clean — full
banner, goroutine sum, M1.5 PCI walk + SNP enumeration, M2
virtio-net device discovery all run to completion. So the gvisor
import is the precipitating change.

**Best interpretation.** TamaGo+UEFI is not the same runtime as
TamaGo+bare-metal. Under UEFI, EDK2's CpuDxe owns the IDT, timer,
and interrupt model; our `cpuinit_<arch>.s` sets up SP + the FPU
and tail-calls into the TamaGo rt0 path without taking those over.
gvisor's package init paths and scheduler use timer-driven
preemption and stricter scheduler invariants that work fine on
usbarmory bare-metal (where TamaGo owns the entire CPU) but
desynchronize EDK2's CpuDxe service routines when the firmware
later takes a timer interrupt. The first timer tick after gvisor
init finds the IDT or stack in a state CpuDxe can't handle and
faults.

**Mitigation, per the original design doc:**

> *"Mitigation if it doesn't compile/run under TamaGo at M3:
>   drop gvisor and write a minimal pure-Go IPv4 + TCP + UDP
>   stack ourselves (scope: ARP, IPv4 send/recv, UDP for DHCP+DNS,
>   TCP for HTTP). This is a few weeks of work — preferred over
>   Path X relapse."*

**M3-minimal scope.**

- ARP request/reply (Ethernet II broadcast, 28-byte ARP frame).
- IPv4 send/recv: header construction + checksum, MTU 1500, no
  fragmentation, single-interface route table.
- ICMP4 Echo Request / Echo Reply (for the ping probe).
- UDP4 send/recv on a single ephemeral port (M4 DHCPv4, M5 DNS).
- TCP4 client-side state machine (SYN → ESTABLISHED → data → FIN);
  no server, no listen, no congestion control beyond a constant
  window (suffices for short HTTP fetches, ~M5-M7 scope).
- ~3000 LOC total; per-arch agnostic; pure-Go on top of our
  existing `uefiboard.VirtioNet` rail. BSD-3-Clause (our own
  code).

**Architectural commitments retained.**

- Pure-Go end-to-end (R-M3'a verdict reinforces our pure-Go
  doctrine — we no longer ship a multi-MB Apache 2.0 dep that
  we couldn't even run).
- gvisor stays available for any TamaGo+bare-metal consumer (not
  our context); the LinkEndpoint adapter we wrote works at the
  interface level — anyone porting gvisor to a runtime where it
  actually runs can crib from that code.

**Archived artifact.**

The M3 gvisor work is preserved on branch
[`m3-gvisor-archive`](https://github.com/cloud-boot/tamago-uefi/tree/m3-gvisor-archive)
(commit `e209c49`). `main` rolls back the gvisor-specific code
and starts fresh on M3-minimal.

### R-M3'b (RESOLVED 2026-06-08) — tamago-pie loong64 overlay missing zsyscall_tamago_loong64.go

Surfaced as a side-finding during the M3 gvisor compile-clean
probe (2026-06-08). On `GOOS=tamago GOARCH=loong64`, importing
`gvisor.dev/gvisor/pkg/tcpip` fails at `syscall/fd_tamago.go`
with `undefined: write`, because tamago-pie's loong64 overlay
does not ship a `zsyscall_tamago_loong64.go` companion to
`syscall/zsysnum_tamago_loong64.go`. This is OUR overlay gap
from the earlier loong64 port (`tamago-loong64-fork.patch`),
not a gvisor problem.

**Fix (2026-06-08).** Investigation revealed that the
`syscall/` portion of the loong64 overlay had not been applied
to the local tamago-pie checkout at all — three files were
missing rather than just one:

- `src/syscall/zsyscall_tamago_loong64.go` (the `write` stub —
  the symbol `fd_tamago.go:166` was looking for)
- `src/syscall/zsysnum_tamago_loong64.go` (`SYS_WRITE = 1`)
- `src/syscall/asm_tamago_loong64.s` (`Syscall` ABI thunk to
  `runtime·syscall`)

All three files are already documented in
`cloud-boot/docs/tamago-go-loong64-core.patch` (lines 468–535
of the patch); they were simply absent from the local checkout.
Each is a direct mirror of its riscv64 sibling. No patch update
was needed — the patch was already authoritative; the local
checkout had drifted from it.

Build matrix verification (2026-06-08, all four ministack EFIs):

| arch    | EFI                               | size   |
| ------- | --------------------------------- | -----: |
| amd64   | `BOOTX64-MINISTACK.EFI`           |  ~2.0M |
| arm64   | `BOOTAA64-MINISTACK.EFI`          |  ~1.7M |
| riscv64 | `BOOTRISCV64-MINISTACK.EFI`       |  ~1.6M |
| loong64 | `BOOTLOONGARCH64-MINISTACK.EFI`   |  ~1.8M |

This unblocks M4 (DHCP), M5 (DNS), M6 (TLS), and M7 (OCI) on
loong64, all of which transitively import `net` (and thus
`syscall`).

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

- ~~Does VZ publish `EFI_PCI_IO_PROTOCOL` handles? See R-M1'a.
  Probe answers this in the M1 run.~~ **Resolved 2026-06-07
  (M1.6)**: VZ vfkit 0.6.3 publishes 5 PCI IO handles including
  a modern virtio-net (0x1af4:0x1041) with all 4 standard virtio
  caps. SNP NOT published.
- Legacy (DID 0x1000) vs modern (DID 0x1041) virtio-net: QEMU+EDK2
  exposes both depending on `-device` qualifier. ~~VZ is expected
  to expose modern only.~~ **Confirmed (M1.6)**: VZ exposes the
  modern variant (DID 0x1041, Rev 0x01).
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
- **2026-06-07** (M1.5): R-M1'b RESOLVED — clean rebuild of
  c6f2716 passes the `phase2_pcienum` probe end-to-end on all 4
  arches under QEMU+EDK2 (loong64 stress-tested for 10 sequential
  boots, zero faults). Diagnosis: the committed source is
  correct; the original M1 fault came from a stale-binary state
  at agent-run time, not from a code bug. SNP enumeration probe
  added (`phase2_snpenum` build tag, sibling to `phase2_pcienum`,
  composable in a single binary via `phase2_dispatch.go`). All 4
  arches PASS SNP walk; MAC `52:54:00:12:34:56` (QEMU virtio-net
  default) consistently reported. R-M1.5x NEW: on riscv64
  QEMU+EDK2, the PciBus driver does not bind virtio-net to
  `EFI_PCI_IO_PROTOCOL` — SNP fallback required on that arch.
  R-M1'a (VZ observability) re-tested under vfkit 0.6.3 with
  three `virtio-serial` variants (`logFilePath`/`stdio`/`pty`);
  all three fail or capture 0 bytes. M1.6 placeholder added:
  Block IO side-channel for VZ observability, pulled forward from
  what would otherwise have been M2/M3 virtqueue infrastructure.
  Host go test ./uefiboard/... + phase2 helpers PASS;
  coverage 96.5% maintained.
- **2026-06-07** (M1.6): R-M1'a RESOLVED. Block-IO side-channel
  shipped: `EFI_BLOCK_IO_PROTOCOL` bindings + GUID +
  `EFI_BLOCK_IO_MEDIA` mirror in `uefiboard/block_io_protocol.go`
  (cite `MdePkg/Include/Protocol/BlockIo.h` edk2.git stable/202408
  lines 15..18, 128..200, 214..230); live ReadBlocks /
  WriteBlocks / FlushBlocks thunks in
  `block_io_protocol_tamago.go`; 32 KiB ring buffer with
  monotonic write counter + auto-flush in `blkprintk.go`; tee
  through `printk` in `board.go` gated on a nil-default
  `BlkSink` so Phase-1 / M0 / M1 / M1.5 behaviour is preserved
  bit-for-bit. Probe binary `BOOT<arch>-BLKPRINT.EFI` built
  via `task blkprintk:all` runs the M1.5 pcienum+snpenum walks
  inside the tee. Magic-marker `cloudboot-M1.6\0\0` at LBA 0
  of the scratch disk identifies the dedicated scratch image
  unambiguously so the probe never clobbers the ESP. Host CLIs
  `cmd/blkprintk-seed` (pre-stage) + `cmd/blkprintk-recover`
  (post-halt decode) ship with the probe. All 4 QEMU+EDK2
  arches PASS the Block-IO side-channel; ConOut output bytes
  match the disk-recovered payload exactly. **Apple VZ vfkit
  0.6.3 (arm64): side-channel WORKS end-to-end** —
  `virtio-serial,logFilePath` stays at 0 bytes (R-M1'a's
  original symptom) but the scratch disk recovers the full
  probe output. VZ capability matrix recovered: 5
  `EFI_PCI_IO_PROTOCOL` handles (Apple host bridge, modern
  virtio-net 0x1041, virtio-rng, 2× virtio-blk); virtio-net
  device-cfg locator BAR0+32768 with the 4 standard virtio caps
  (CommonCfg/ISRCfg/NotifyCfg/DeviceCfg); `EFI_SIMPLE_NETWORK_
  PROTOCOL` NOT published (LocateHandleBuffer returns
  EFI_NOT_FOUND = 0x800...000e). Implication: Apple VZ
  supports Path Y (pure-Go virtio-net via PCI IO) but NOT
  Path Y' (SNP-first); a Path-Y'-only prod implementation
  cannot cover VZ. M2 direction surfaced (§3 M2):
  Path Y'' (both) recommended for full coverage, ~3300 LOC
  + ~50 LOC chooser. Path-choice deferred to user. Host
  `go test ./uefiboard/... ./cmd/...` PASS;
  uefiboard coverage 98.0 % (up from 96.5 %),
  blkprintk-seed 80.0 %, blkprintk-recover 91.7 %.
- **2026-06-07** (M2): Path Y'' adopted; virtio-net pure-Go
  rail SHIPPED. Widened `efiCall` 5→6 args across all four
  arches (amd64/arm64/loong64/riscv64) so
  `EFI_PCI_IO_PROTOCOL.Mem.Read/Write(This*, Width, BarIndex,
  Offset, Count, Buffer*)` fits the envelope. New
  `uefiboard/pci_mem_io.go` (8/16/32/64-bit typed Mem accessors),
  `alloc_pages.go` (gBS->AllocatePages thunk),
  `virtio_modern.go` + `virtio_modern_tamago.go` (Virtio 1.1
  §4.1.5.1 COMMON_CFG register layout + `InitVirtioModernConfig`
  walker + per-register accessors including the R-M1.6a-safe
  `DeviceCfgRead8`), `virtqueue.go` + `virtqueue_tamago.go` (split
  virtqueue layout per Virtio 1.1 §2.6 with `unsafe.Slice` views
  + `atomic.StoreUint32`/`LoadUint32` release/acquire on the
  ring headers + `AllocDMABuffer` allocator), `virtio_net.go`
  + `virtio_net_tamago.go` (full Virtio 1.1 §3.1.1 init sequence,
  feature negotiation accepting only MAC/STATUS/VERSION_1,
  16-buffer pre-posted rxq, `TransmitFrame`/`ReceiveFrame`).
  Probe `phase2_virtionet_tx` (`BOOT<arch>-VIRTIONET.EFI`) opens
  the first 1AF4:1041 device, transmits two ARP requests (one
  per probable NAT layout: QEMU 10.0.2.x + VZ 192.168.64.x),
  polls RX for replies + prints captured frames. All four arches
  build green via `task virtionet:all`. Phase-1 / M0 / M1 /
  M1.5 / M1.6 EFIs rebuild cleanly with the 6-arg widening; the
  banner rodata regression test PASSES on all four arches.
  Host `go test ./uefiboard/... ./cmd/... ./internal/bannertest/...`
  PASS; uefiboard coverage **98.3 %** (up from 98.0 %),
  virtio_modern.go 98.6 %, virtio_net.go 97.5 %,
  virtqueue.go 99.1 %. R-M2a (efiCall widening regression risk)
  introduced + MEDIUM — mitigated by Go's call-site
  type-checking, awaits live re-validation of all five matrix
  cells via the iso harness. M2.1 (SNP wrapper) and M2.2
  (unified LinkEndpoint + chooser) scoped + queued.
- **2026-06-07** (M2 live validation): 5-cell boot campaign run
  on `cloud-boot/tamago-uefi@5f4951e`. Harness: homebrew
  `qemu-system-{x86_64,aarch64,loongarch64,riscv64}` + EDK2
  firmwares from `/opt/homebrew/share/qemu/edk2-*.fd` + vfkit
  0.6.3. **Results**: amd64 / arm64 / loong64 = PASS (full M2
  acceptance: init OK, MAC `52:54:00:12:34:56`, TX OK, RX
  captured ARP reply from QEMU NAT gateway MAC
  `52:55:0a:00:02:02`, wall-clocks 1.87 / 5.92 / 5.59 s).
  riscv64 = PASS for the M2 acceptance shape (transitional
  device under default `-device virtio-net-pci` → probe
  surfaces clean "no modern virtio-net" diagnostic + halt in
  6.54 s; production riscv64 traffic deferred to M2.1 SNP
  rail as documented in §3 M2). **Bonus finding**: with
  `disable-legacy=on,disable-modern=off`, riscv64 EDK2
  stable202408 + QEMU 10.x **does** bind modern virtio-net to
  PCI IO and runs the M2 rail end-to-end (6.52 s to DONE); R-M1.5x
  narrowed to legacy-device-only. **Apple VZ vfkit arm64 = FAIL**:
  the M2 probe correctly locates the modern virtio-net
  (`0x1AF4:0x1041`), walks all 4 caps (DeviceCfg length=17
  confirming R-M1.6a shape — bounds-check holds), but
  `OpenVirtioNet` fails at step 5 (FEATURES_OK status bit
  doesn't stick after the driver writes
  `0x100010020` = MAC|STATUS|VERSION_1). Per Virtio 1.1 §3.1.1
  this means VZ requires at least one feature bit the M2 mask
  doesn't accept; most-likely candidates ACCESS_PLATFORM (bit
  33), RING_PACKED (bit 34), NOTIFICATION_DATA (bit 38), or
  RING_RESET (bit 40). Filed as **R-M2b (HIGH)** in §5; needs a
  follow-up VZ probe with a 1-line `println` of the
  device-offered bitmap dumped via the M1.6 side-channel to
  disambiguate. **Risk status updates**:
  R-M2a RESOLVED (live boots exercised 6-arg envelope across
  all 5 cells without any fault), R-M1.6a RESOLVED (live VZ
  boot confirmed DeviceCfg length=17 shape, bounds-check
  holds), R-M1.5x CONFIRMED + NARROWED (legacy/transitional
  binding gap only — modern PCI binding works), R-M2b NEW
  (HIGH, VZ blocking). **M2 milestone status: SHIPPED +
  VALIDATED 4/5 cells; VZ cell is the one open issue before
  M2.1.** Iso harness was NOT modified for this validation;
  the boots were driven by ad-hoc per-arch `qemu-system-<arch>`
  invocations following the shape documented in
  `cloud-boot/iso/pkg/multiarchboot/multiarchboot.go`
  + a vfkit invocation for VZ. ESP images were built with
  `mformat` / `mmd` / `mcopy` (the same toolchain
  `cloud-boot/iso` uses for the ISO ESP). Validation captures
  retained in operator's workspace, not committed.
- **2026-06-08** (M2-B silent-fail diagnosis): R-M2c Option B branch
  `cloud-boot/tamago-uefi@m2-b-post-ebs` appeared to be silent on
  Apple VZ via vfkit — `blkprintk-recover` reported
  `writeCount=0 payloadBytes=0` after a 15+ minute run, suggesting
  the M2-B binary never wrote its first pre-EBS line. Diagnosis:
  the silence was a **Taskfile staging regression**, not a code or
  VZ-level gate. Two bugs in the `live:vz:m2b` task:
  (1) the scratch image was created with plain `dd if=/dev/zero`
  and never stamped with `uefiboard.BlkPrintkScratchMagic` —
  `phase2_blkprintk.go:runBlkPrintkSetup` binds the side-channel
  ring ONLY to the disk whose first 16 bytes match the
  `cloudboot-M1.6\0\0` sentinel, so an unmarked scratch made the
  probe degrade to ConOut-only, and Apple VZ captures 0 bytes of
  ConOut (R-M1'a's original symptom); (2)
  `cmd/blkprintk-recover` takes `-in <path>` but the Taskfile
  passed the path positionally, which printed `usage:` + exit 2
  instead of dumping the scratch — masking the actual contents.
  Fix in `cloud-boot/tamago-uefi@f76f78b`: insert
  `cd "$TASKFILE_DIR" && go run ./cmd/blkprintk-seed -out
  $SCRATCH -size-mib 1` before the ESP staging, and pass `-in`
  to the recover invocation. Post-fix `task live:vz:m2b
  TIMEOUT=30` recovers `writeCount=1 payloadBytes=4043` with
  the full pre-EBS dump ending at `phase2-m2b: flushing
  blkprintk side-channel pre-EBS` — the last println before
  `ExitToBareMetal`. The M2-B binary is NOT silent; it runs
  through `CapturePreEBS` cleanly on VZ (MAC read, BAR-base
  resolution, feature negotiation, queue page allocation, scratch
  allocation) and reaches the EBS boundary. Whether the post-EBS
  TX path then unblocks VZ virtio-net is the R-M2c Option B
  question proper and is unaffected by this fix; the host-side
  observable is the ARP marker (`src 169.254.2.66 / dst
  169.254.99.99 / payload "M2B!"`), which requires sudo tcpdump
  on the vfkit NAT bridge (`SUDO_CAPTURE=1`) and is the next
  validation step. The QEMU 4-arch PASS cells are unaffected
  (M2-B binary uses the same pre-EBS pipeline; only post-EBS
  behaviour differs from M2).
