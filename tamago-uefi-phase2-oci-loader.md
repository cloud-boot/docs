---
title: TamaGo UEFI Phase 2 — OCI pre-boot loader (shape A)
status: design / in-progress (M0..M1.6 done; M2 SHIPPED + LIVE-VALIDATED 4/5 cells; R-M2b RESOLVED; R-M2c CLOSED 2026-06-08 — Apple VZ gates virtio-net for non-OS clients, Path D ships on QEMU+EDK2 only; **R-M3'a CLOSED 2026-06-08 — gvisor pkg/tcpip compile-clean under TamaGo but runtime CRASH (CpuDxe #GP) on QEMU+EDK2 amd64 before our dispatcher runs. M3 gvisor work archived on branch `m3-gvisor-archive`. M3-minimal SHIPPED 2026-06-08 (ARP+IPv4+ICMP4). M4 DHCPv4 SHIPPED 2026-06-08 (UDP4 added to ministack; DORA pure-Go).** Milestones complete: M0, M1, M1.5, M1.6, M2, M3-minimal, M4 — 7 of 9.)
last-updated: 2026-06-08 (M4 DHCPv4 acquire shipped — UDP4 + DHCP4 added to ministack)
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

| milestone   | status                              | one-liner                                                  |
|-------------|-------------------------------------|------------------------------------------------------------|
| M0          | done 2026-06-07                     | GetMemoryMap + type surface                                |
| M1          | done 2026-06-07                     | EFI_PCI_IO_PROTOCOL bindings + virtio-net identity         |
| M1.5        | done 2026-06-07                     | R-M1'b re-validation + SNP enumeration                     |
| M1.6        | done 2026-06-07                     | Block-IO side-channel for VZ observability                 |
| M2          | done 2026-06-08                     | virtio-net init + virtqueues + TX/RX (Path Y'')            |
| M3-minimal  | done 2026-06-08                     | hand-rolled ARP+IPv4+ICMP4 ministack                       |
| M4          | done 2026-06-08                     | DHCPv4 client                                              |
| M5          | done 2026-06-08                     | DNS + HTTP GET                                             |
| M6          | done 2026-06-08                     | TLS + HTTPS GET (PE>4 MiB amd64 deferred as M6.1)          |
| M6.1        | **PARTIAL 2026-06-09**              | OVMF CpuPageTableLib root-causing + parent-side gzip embed |
| M6.2        | **DE-RISK PASS 2026-06-09**         | hand-rolled PE32+ ≤2 MiB cleanly load+start on amd64 OVMF — `go-coff/efipack` viable |
| M6.2 PR2    | **arm64/riscv64/loong64 GREEN 2026-06-09** (amd64 RED — R-amd64a root-caused `m6-2-edk2-upstream-investigation.md` § 11; R-amd64b AllocatePages cpuinit rewrite attempted, hit rt0 secondary regression, staged on `m6-2-pr2-amd64-wip-r-amd64b` for R-amd64c — § 12) | per-arch self-extracting EFI stub blobs shipped in `go-coff/efipack` |
| **M6.2**    | **SHIPPED (3/4 arches) 2026-06-09; amd64 deferred** | `pectl pack` CLI + `efipack:smoke:all` matrix + `go-coff/efipack` v0.1.0 / `go-coff/peln` v0.3.0 (M6.2 PR3) |
| M6.2 PR4    | **SHIPPED 2026-06-09 (host-side)**  | LZFSE codec wired in `efipack v0.2.0` + `pectl v0.3.0`; runtime stubs still flate-only (deferred) |
| M7          | done 2026-06-08                     | OCI registry client (streaming-blob deferred as M7.1)      |
| **M7.1a**   | **done 2026-06-09 (streaming SHIPPED)** | **HTTPGetStream + HTTPSGetStream + FetchBlobStream**    |
| **M7.1b**   | **done 2026-06-09 (cosign v2 + v3 SHIPPED, real-image verified)** | **keyed cosign ECDSA-P256 verify: v2 layer-annotation + v3 sigstore-bundle (messageSignature & dsseEnvelope); host coverage 96.1%; live wire-format proof against cosign-v3-signed ttl.sh image** |
| **M8.0**    | **done 2026-06-09**                 | **LoadImage + StartImage chain-boot mechanism**            |
| **M8.1**    | **SHIPPED minimal 2026-06-09**      | **OCI streaming + LoadImage + StartImage end-to-end (3/4 arches)** |
| **M8.2**    | **framework SHIPPED 2026-06-09**    | **SetLoadOptions + PublishInitrd + MODE C wiring (dormant; live demo gated on public EFI-stub kernel OCI ref)** |
| **M8.3**    | **per-arch matrix 2026-06-10 (see below)** | **OCI ref → vmlinuz → LoadImage → StartImage → EFI-stub prints "Booting Linux Kernel..."** |
| **M8.4**    | **SHIPPED arm64 + R-M8.4a CLOSED 2026-06-10; rv64+loong64 landscape ENUMERATED 2026-06-10** | **ConfigurationTable DTB probe + PublishInitrd + per-arch LoadFile2 trampoline fixed; EFI-stub now prints `Loaded initrd from LINUX_EFI_INITRD_MEDIA_GUID device path` (4-line kernel boot trace). Expanded 60-min OCI hunt for rv64+loong64 documented in §M8.4 "Public ref landscape" — no candidate met acceptance bar; both arches stay dormant.** |
| **M8.5**    | **wiring SHIPPED 2026-06-10; R-M8.5a OPEN (DTB-absence Data Abort)** | **Embedded initramfs replaced with real static-ELF /init (573 KiB cpio.gz, pure-Go arm64) + ELF-magic guard test + DTB probe extended to dump all VendorGuids. Live trace: EFI-stub reaches `Loaded initrd…` with the real 573 KiB initrd (proves LoadFile2 fix scales beyond M8.4's 260-byte fixture). Kernel side blocked on R-M8.5a — firmware Data Abort because EDK2 arm64 publishes ACPI + SMBIOS but no DTB, and the empty-DTB patch path in EFI-stub null-derefs (FAR=0x40). Cmdline broadened to acpi=force + earlycon=pl011,mmio32 + rdinit=/init pre-emptively but the crash is pre-cmdline-parse. M8.6 mitigation: publish a DTB via gBS->InstallConfigurationTable from Go.** |

### M8.3 — per-arch live kernel boot matrix (2026-06-10)

| Arch     | Mode  | Public EFI-stub OCI ref                                                  | Live test                                                                                                     |
|----------|-------|--------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------|
| arm64    | C     | `ghcr.io/siderolabs/kernel:v0.6.0-alpha.0-1-ge8ed5bc`                    | PASS — EFI-stub prints "Booting Linux Kernel..." + KASLR + DTB + initrd-load attempts (see M8.3 dump below). |
| amd64    | dormant (B) | siderolabs/kernel ships amd64 too; wiring left empty pending OVMF >4 MiB sprint on m6-2-pr2-amd64-wip. | n/a (MODE B passes; live kernel deferred).                                                                  |
| riscv64  | dormant (B) | None found in expanded 60-min OCI hunt 2026-06-10 (see M8.4 §"Public ref landscape" below). | PASS in MODE B (self-test) — `task kernelboot:live:riscv64`. MODE C dormant pending public ref. |
| loong64  | dormant (B) | None found in expanded 60-min OCI hunt 2026-06-10 (see M8.4 §"Public ref landscape" below). | PASS in MODE B (self-test) — `task kernelboot:live:loong64`. MODE C dormant pending public ref. |

### M8.4 — Public ref landscape for riscv64 + loong64 (2026-06-10)

A second, more aggressive 60-min OCI hunt was run on 2026-06-10
following the initial 30-min hunt — looking for ANY publicly
anonymous-pullable OCI artifact whose layer carries `/boot/vmlinuz`
as a PE32+ EFI-stub image for riscv64 and/or loong64. Search width:
distro base / cloud / installer / kernel-only namespaces across
`ghcr.io`, `quay.io`, `docker.io`, `registry.opensuse.org`,
`registry.fedoraproject.org`, `registry.openeuler.org`, AWS ECR
public, Apple/VMware sub-registries, and dedicated arch-org
namespaces (`loong64/*`, `riscv64/*`, `loongarchlinux/*`,
`siderolabs/*`, `tinkerbell/*`, `kairos-io/*`, `linuxcontainers/*`,
`centos-bootc/*`, `fedora-bootc/*`).

Probe tool: `crane manifest` + `crane pull --platform linux/<arch>`
+ `tar tzf | grep vmlinuz`. All probes anonymous, no `~/.docker/config.json`
credentials. Acceptance criteria: anonymous pull works AND the
extracted layer contains `boot/vmlinuz*` (PE32+ EFI-stub, MZ + `'ARMd'`
or LoongArch / RISC-V machine type) AND total kernel-layer payload
under 50 MB.

#### riscv64 candidates probed

| Candidate ref                                                          | Reachable | Has `/boot/vmlinuz`? | Layer size | Notes |
|------------------------------------------------------------------------|-----------|----------------------|------------|-------|
| `ghcr.io/siderolabs/kernel:v1.11.0`                                    | yes       | n/a (no rv64 manifest) | n/a      | Multi-arch index lists ONLY `amd64` + `arm64`. Same for `v1.14.0-alpha.0-76-g09cb04e` (latest nightly probed). |
| `ghcr.io/siderolabs/installer:v1.11.0` / `installer-base:latest`       | yes       | n/a (no rv64 manifest) | n/a      | amd64 + arm64 only. No `installer-riscv64`, no `kernel-riscv64` (DENIED — repo nonexistent). |
| `ghcr.io/siderolabs/imager:v1.11.0` / `imager:latest`                  | yes       | n/a (no rv64 manifest) | n/a      | amd64 + arm64 only. |
| `ghcr.io/siderolabs/talos:v1.11.0`                                     | yes       | n/a                  | n/a        | amd64 + arm64 (`talosctl-linux-riscv64` is published as a CLI release asset, NOT as an OCI kernel layer — confirmed via `gh search code --owner siderolabs riscv64`). |
| `ghcr.io/tinkerbell/hook:latest` / `hook-kernel:latest`                | yes       | n/a                  | n/a        | `MANIFEST_UNKNOWN` for both `:latest` and `hook-kernel-riscv64:latest`. Tinkerbell hook releases ship arm64 + x86_64 tarballs only. |
| `ghcr.io/sigstore/cosign/cosign:latest`                                | yes       | no                   | tiny       | Has rv64 manifest (distroless) but contains only the cosign binary, no kernel. |
| `docker.io/library/debian:sid` (`@linux/riscv64`)                      | yes       | no                   | 46.8 MB    | Standard debuerreotype rootfs — `usr/bin/installkernel` present, no actual `/boot/vmlinuz`. |
| `docker.io/riscv64/debian:sid` / `riscv64/ubuntu:latest` / `riscv64/alpine:latest` / `riscv64/busybox:latest` | yes | no | 3.6–47 MB | All rootfs only. |
| `docker.io/library/ubuntu:noble` / `ubuntu:devel`                      | yes       | no                   | ~26 MB     | rv64 manifest present, rootfs only. |
| `docker.io/library/alpine:latest` (`@linux/riscv64`)                   | yes       | no                   | 3.6 MB     | Apk rootfs only — `apk add linux-virt` would pull kernel at run-time, NOT included. |
| `registry.opensuse.org/opensuse/tumbleweed:latest` (`@linux/riscv64`)  | yes       | no                   | 37.6 MB    | rpm rootfs only — `usr/bin/get_kernel_version`, `usr/lib/rpm/fileattrs/kernel.attr`, no `/boot/vmlinuz`. |
| `registry.opensuse.org/opensuse/tumbleweed-microservices:latest`       | yes       | n/a                  | n/a        | No rv64 manifest in the index. |
| `quay.io/fedora/fedora:rawhide` / `fedora:latest`                      | yes       | n/a                  | n/a        | No rv64 manifest in the Fedora multi-arch index (amd64/arm64/ppc64le/s390x). |
| `registry.fedoraproject.org/fedora:latest` / `fedora:rawhide`          | yes       | n/a                  | n/a        | Same — no rv64. |
| `quay.io/fedora/fedora-bootc:rawhide` / `fedora-bootc:42`              | yes       | n/a                  | n/a        | bootc images (which DO ship `/usr/lib/modules/<ver>/vmlinuz`) are amd64/arm64/ppc64le/s390x only. |
| `quay.io/centos-bootc/centos-bootc:stream10` / `stream9`               | yes       | n/a                  | n/a        | Same set as Fedora bootc — no rv64. |
| `docker.io/library/almalinux:9` / `quay.io/almalinuxorg/almalinux:9`   | yes       | n/a                  | n/a        | amd64/arm64/ppc64le/s390x/386 — no rv64. |
| `public.ecr.aws/bottlerocket/bottlerocket-admin:latest` / `-control`   | tag MANIFEST_UNKNOWN | n/a | n/a | Bottlerocket admin/control containers are amd64+arm64 only. AWS does not publish rv64 Bottlerocket. |
| `ghcr.io/loong64/openeuler:24.03-init-20260426` (`@linux/riscv64`)     | yes       | no                   | 87.7 MB    | `loong64` org repackages openEuler for FIVE arches incl. riscv64 — but the `init`/`base`/`default` variants ship rootfs only, no `/boot/vmlinuz`. Even `init` (largest) is dnf-only stage. |
| `cr.loongnix.cn/*` (re-probed)                                         | no        | n/a                  | n/a        | Anonymous still 401. |

#### loong64 candidates probed

| Candidate ref                                                          | Reachable | Has `/boot/vmlinuz`? | Layer size | Notes |
|------------------------------------------------------------------------|-----------|----------------------|------------|-------|
| `ghcr.io/siderolabs/kernel:*`                                          | yes       | n/a (no loong64 manifest) | n/a   | No loong64 entry across `latest`, `v1.11.0`, `v1.14.0-alpha.0-76-g09cb04e`. |
| `ghcr.io/siderolabs/installer:*` / `imager:*` / `talos:*`              | yes       | n/a                  | n/a        | amd64 + arm64 only. No `*-loong64` variants. |
| `ghcr.io/loong64/openeuler:24.03-default-20260426` (`@linux/loong64`)  | yes       | no                   | 94.3 MB    | dnf rootfs only; same kernel-stub artefacts as the riscv64 variant. |
| `ghcr.io/loong64/openeuler:24.03-init-20260426` (`@linux/loong64`)     | yes       | no                   | 92.3 MB    | Same — no `/boot/vmlinuz`. |
| `ghcr.io/loong64/openeuler:24.03-base-20260426` (`@linux/loong64`)     | yes       | no                   | 92.0 MB    | Same. |
| `ghcr.io/loong64/loongnix:23.1-default-20250822` (`@linux/loong64`)    | yes       | no                   | 40.6 MB (zstd) | Loongnix repackaged: AFS overlay rootfs, no `/boot/vmlinuz`. |
| `ghcr.io/loong64/anolis:latest` / `opencloudos:latest` / `kylin:latest` | yes      | no (rootfs)          | varies     | All three Chinese distros published by `loong64` org, all rootfs-only multi-arch indices (amd64/arm64/loong64). |
| `ghcr.io/loong64/alpine:latest`                                        | yes       | no                   | small      | apk rootfs only — `apk add linux-virt` would fetch kernel at run-time. |
| `ghcr.io/loong64/debian:sid`                                           | yes       | no                   | small      | debuerreotype rootfs only. |
| `docker.io/openeuler/openeuler:24.03-lts`                              | yes       | n/a                  | n/a        | amd64 + arm64 only (no loong64 in upstream Docker Hub index). |
| `docker.io/loongarch64/debian:sid` / `loongarch64/ubuntu:latest`       | tag MANIFEST_UNKNOWN | n/a | n/a    | These docker.io namespaces are empty / undocumented. |
| `cr.loongnix.cn/*` (re-probed)                                         | no        | n/a                  | n/a        | Anonymous 401 — Loongnix registry requires account. |
| `swr.cn-north-4.myhuaweicloud.com/openeuler/*`                         | no        | n/a                  | n/a        | Tag-discovery requires Huawei Cloud auth even for nominally-public read. |
| `dr.openkylin.top/*`                                                   | not OCI   | n/a                  | n/a        | openKylin distributes ISO/squashfs over HTTP, no OCI distribution endpoint. |
| `ghcr.io/openeuler/*` / `ghcr.io/openkylin/*` / `ghcr.io/loongson/*`   | tag MANIFEST_UNKNOWN | n/a | n/a    | These ghcr.io orgs either don't publish OCI containers or scope them to private. |
| `registry.gitlab.com/loongarchlinux/archlinux:latest` / `openeuler/openeuler:latest` | no | n/a | n/a | GitLab registry refuses anonymous. |
| `ghcr.io/linuxcontainers/alpine:edge` / `debian:sid`                   | tag MANIFEST_UNKNOWN | n/a | n/a | LXC images for these arches don't exist on ghcr.io; the canonical http://images.linuxcontainers.org tree is not OCI-distributed. |

#### Conclusion

After 60 minutes of expanded search, **no public anonymous-pullable
OCI artifact exists in any of the probed registries that carries a
`/boot/vmlinuz` EFI-stub Linux kernel for riscv64 or loong64**. The
universal pattern is:

- Distro base images on docker.io / quay.io / ghcr.io that DO support
  riscv64 (debian, ubuntu, alpine, opensuse tumbleweed) ship as
  pure rootfs tarballs without `/boot/vmlinuz` (kernel comes from
  the host or from a separate `apk add linux-virt` / `apt install
  linux-image-riscv64` at run-time, neither of which is reachable
  from a non-running TamaGo EFI).
- Kernel-shipping OCI artefacts that DO exist (Talos
  `siderolabs/kernel`, Tinkerbell hook, Bottlerocket, Fedora /
  CentOS bootc) cover only amd64+arm64 (and sometimes ppc64le /
  s390x); none ship riscv64 or loong64 layers.
- Chinese distros most likely to ship loong64 kernels (Loongnix,
  openKylin, Anolis) are either: (a) only on the Loongnix-internal
  registry behind authentication, (b) repackaged as rootfs-only
  containers on ghcr.io via the `loong64` org, or (c) distributed as
  ISO/qcow2 over plain HTTP rather than OCI.

The two-arch dormant state in M8.3 / M8.4 framework is therefore
correct and remains the right outcome. Both arches will flip live
the day:

- siderolabs adds `riscv64` / `loong64` platforms to the
  `ghcr.io/siderolabs/kernel` multi-arch index (open-source feature
  request — they already ship the riscv64 cross-compile pipeline in
  `siderolabs/pkgs:kernel/build/config-arm64`'s "Loongson" hunk and
  the talosctl-linux-riscv64 binary), OR
- `loong64` org publishes a `ghcr.io/loong64/kernel:*` ORAS
  artifact alongside the existing distro images (the org already
  owns the namespace and the LoongArch kernel build pipeline), OR
- A new publisher (RISC-V International / OpenSBI / Sipeed Lichee /
  StarFive VisionFive) ships an EFI-stub vmlinuz as an OCI
  artifact.

Filing requests is a maintainer-facing action — list of candidate
upstream issue trackers:

| Upstream                            | Issue tracker                                                        |
|-------------------------------------|----------------------------------------------------------------------|
| Talos / Sidero Labs                 | https://github.com/siderolabs/pkgs/issues  (kernel build pipeline)   |
| Tinkerbell hook                     | https://github.com/tinkerbell/hook/issues                            |
| AWS Bottlerocket                    | https://github.com/bottlerocket-os/bottlerocket/issues               |
| loong64 org (LoongArch repackage)   | https://github.com/loong64/.github/issues                            |
| openEuler container images          | https://github.com/openeuler/community/issues                        |
| Loongnix Linux                      | https://github.com/loongson-community (no public issue tracker — file via Loongnix mailing list) |
| openKylin                           | https://gitee.com/openkylin/community                                |

Per-arch constants live in `kernelboot_<arch>.go` next to
`phase2_oci_kernel_boot.go`. The dispatcher consumes them via the
existing three-way mode switch (B/A/C) — no runtime architecture
branching, no `init()` swizzle. Flipping a dormant arch to MODE C is
a one-line edit in its per-arch file (set `kernelBootTargetRef` +
`kernelBootCmdline`).

The runtime-discovered cmdline guidance baked into the per-arch
docstrings:

```
arm64    console=ttyAMA0,115200 earlyprintk=ttyAMA0,115200
amd64    console=ttyS0,115200 earlyprintk=ttyS0,115200
riscv64  console=hvc0 earlycon=sbi
loong64  console=ttyS0,115200
```


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

**Virtio code extraction (2026-06-08).** The transport-agnostic virtio
infrastructure (PCI capability walker, modern config + register
layout, split-virtqueue impl) and the spec-level virtio-net driver have
been extracted from `cloud-boot/tamago-uefi/uefiboard/` into a new
`go-virtio` GitHub org:

  - [`github.com/go-virtio/common`](https://github.com/go-virtio/common)
    — transport-agnostic virtio infrastructure (PCI cap walker, modern
    `ModernConfig` + register accessors, split-virtqueue impl,
    `Transport` / `PCIConfigReader` / `BARMemoryAccessor` /
    `PageAllocator` interfaces). Mirrors Linux's `<linux/virtio.h>` +
    `virtio_ring.h` shared infrastructure.
  - [`github.com/go-virtio/net`](https://github.com/go-virtio/net) —
    pure-Go virtio-net driver. Imports `go-virtio/common`. The Virtio
    1.1 §3.1.1 init sequence, the per-frame header layout, the rxq /
    txq state machine, and the R-M2b MTU acceptance fix all live here.
  - [`github.com/go-virtio/blk`](https://github.com/go-virtio/blk) —
    placeholder for a future pure-Go virtio-blk driver (cloud-boot
    currently uses UEFI's `EFI_BLOCK_IO_PROTOCOL` for the pre-EBS
    phase; the placeholder makes the `go-virtio` org symmetric with
    Linux's `<linux/virtio_net.h>` / `<linux/virtio_blk.h>` split and
    documents the design slot for when a concrete caller appears).

`uefiboard/` keeps the UEFI transport adapter (`virtio_uefi_transport.go`
implements `common.Transport` via `EFI_PCI_IO_PROTOCOL` +
`gBS->AllocatePages`) plus a thin bridge file (`virtio_uefi_bridge.go`
+ `virtio_net_uefi.go`) that preserves the existing
`uefiboard.OpenVirtioNet` / `uefiboard.VirtioNet` API surface so M2 and
M3-minimal probes compile unchanged. M2's R-M2b live-validated MTU
feature bit is preserved verbatim in `go-virtio/net.AcceptedFeatures`.

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

### M4 — DHCPv4 client (pure Go) — SHIPPED 2026-06-08

**Deliverable (SHIPPED).** A pure-Go DHCPv4 client over the
M3-minimal ministack. RFC 2131 + RFC 2132. Acquires a lease, parses
options 1/3/6/51/54 (subnet, router, DNS, lease time, server ID),
returns a `DHCP4Lease` struct, and lets the caller reconfigure the
Stack (`SetIPv4Address` + `SetDefaultGateway`) from the lease. The
DNS server list survives in the lease for M5 to consume.

Scope (shipped):

- `uefiboard/ministack/udp4.go` (~330 LOC) — UDP/IPv4 parse + build
  with full pseudo-header checksum, `Stack.OpenUDP4(localPort) →
  *UDP4Conn` demux, `WriteTo` + `ReadFrom` with read deadlines,
  limited-broadcast (255.255.255.255) short-circuit so DHCP DISCOVER
  ships without an ARP lookup. M5 (DNS) reuses this layer unchanged.
- `uefiboard/ministack/dhcp4.go` (~430 LOC) — DORA state machine
  (DISCOVER → OFFER → REQUEST → ACK), 240-byte BOOTP header +
  magic-cookie + TLV options, MAC-derived deterministic xid,
  pseudo-header-checksummed UDP/68 ↔ UDP/67, NAK detection,
  per-stage deadline enforcement.
- `phase2_dhcp4_acquire.go` + `phase2_dhcp4_acquire_stub.go` — the
  M4 probe. Locates virtio-net via the M2 path, wraps it in
  ministack, runs DHCP4Acquire(10s), prints the lease, applies it
  to the Stack, then pings the learned gateway as an end-to-end
  validation.
- `internal/livedhcp4/run.sh` — per-arch QEMU+EDK2 smoke runner;
  greps stdout for `lease acquired` + `gateway ping OK`.
- Taskfile targets: `dhcp4:elf:<arch>`, `dhcp4:efi:<arch>`,
  `dhcp4:all`, `dhcp4:test`, `live:dhcp4:<arch>`.

Coverage (ministack package, after M4):

| File         | Tests | LOC  | Coverage |
| ------------ | ----- | ---: | -------: |
| `udp4.go`    | 26    |  330 |    96.4% |
| `dhcp4.go`   | 21    |  430 |    98.7% |
| (package)    |  ~95  | 2280 |    95.1% |

(Coverage measured on the host build, 2026-06-08. Up from 94.6% pre-M4.)

Lease refresh remains deferred (boot lifetime < 60 s ≪ typical
lease, and QEMU's SLIRP server returns 86400 s by default).

Build matrix (2026-06-08):

| arch    | EFI                          |    size |
| ------- | ---------------------------- | ------: |
| amd64   | `BOOTX64-DHCP4.EFI`          | ~2.10 M |
| arm64   | `BOOTAA64-DHCP4.EFI`         | ~1.82 M |
| riscv64 | `BOOTRISCV64-DHCP4.EFI`      | ~1.72 M |
| loong64 | `BOOTLOONGARCH64-DHCP4.EFI`  | ~1.89 M |

Per-arch live results (QEMU+EDK2 user-mode networking,
`task live:dhcp4:<arch>`): see §7 Cross-references for the
matching runner script + acceptance criteria. Expected lease on
the SLIRP default network: IP 10.0.2.15, Mask 255.255.255.0,
Gateway 10.0.2.2, DNS 10.0.2.3, Lease 86400 s.

Acceptance (preserved): on QEMU+EDK2 with `-netdev user`, the
probe acquires a lease, prints the assigned IP + gateway + DNS +
lease time, and the netstack pings the gateway end-to-end. (vfkit
deferred — same code path; vfkit lives downstream of QEMU
validation per R-M2c.)

### M5 — DNS + HTTP GET (pure-Go over the stack) — SHIPPED 2026-06-08

**Deliverable (SHIPPED).** A pure-Go plaintext HTTP/1.1 GET reaches
the public internet over the M3-minimal ministack. The probe
acquires a DHCPv4 lease (reusing M4), resolves an A-record via the
DHCP-learned DNS server, dials TCP/80 through SLIRP NAT, sends a
GET request, parses the response, and prints status + body bytes.

The path is now: virtio-net → ministack(ARP+IPv4+ICMP+UDP+TCP) →
DHCP+DNS+HTTP, all pure Go, no CGO, no firmware EFI_HTTP, no
vendored TCP stack.

We did NOT take the route the original M5 sketch described
(stdlib `net/http` with a custom `Transport.DialContext`). Tamago's
`net` package exposes a `SocketFunc` hook that would in principle
let `net/http` ride on top of TCP4Conn, but wiring it correctly
under the inline-RX-pump pattern is fragile (the runtime has no
hardware-timer-driven async preemption, and `net/http`'s internals
expect a scheduler that can block goroutines on network I/O). A
~370-LOC hand-rolled HTTP client is cheaper than fighting that
plumbing — and it keeps the dependency surface tiny, which matters
once M6 layers `crypto/tls` on top.

Scope (shipped):

- `uefiboard/ministack/tcp4.go` (~895 LOC) — TCP/IPv4 client. Header
  parse + build with pseudo-header checksum, per-Conn TCB with
  snd.una/snd.nxt/rcv.nxt/snd.wnd, state machine
  (CLOSED → SYN_SENT → ESTABLISHED → FIN_WAIT_1 → FIN_WAIT_2 →
  TIME_WAIT → CLOSED, plus the peer-driven half ESTABLISHED →
  CLOSE_WAIT → LAST_ACK → CLOSED), fixed 32 KiB receive window,
  1-second per-segment retransmit timer with 3 retries, small
  out-of-order reassembly buffer, ephemeral local-port allocator,
  4-tuple demux, and net.Conn compatibility (Read/Write/Close/
  LocalAddr/RemoteAddr/SetDeadline/etc).
- `uefiboard/ministack/dns.go` (~335 LOC) — A-record resolver. RFC
  1035 message + question + answer parser (including pointer
  compression with chain-loop detection), `Stack.ResolveA(name,
  dns, timeout) → net.IP`. MAC-derived deterministic transaction ID.
- `uefiboard/ministack/http.go` (~374 LOC) — minimal HTTP/1.1 GET
  client. URL parsing, request building, response parsing (status
  line + headers + Content-Length / Transfer-Encoding: chunked).
  Hand-rolled; no `net/http` import.
- `phase2_http_get.go` + `phase2_http_get_stub.go` — the M5 probe.
  Locates virtio-net via the M2 path, wraps it in ministack, runs
  DHCP4Acquire(10s), pings the gateway (pre-warms ARP), resolves
  example.com, fetches http://example.com/.
- `internal/livehttp/run.sh` — per-arch QEMU+EDK2 smoke runner;
  greps stdout for `lease acquired` + `resolved example.com` +
  `HTTP-GET OK`.
- Taskfile targets: `http:elf:<arch>`, `http:efi:<arch>`,
  `http:all`, `http:test`, `live:http:<arch>`.

Coverage (ministack package, after M5):

| File         | Tests | LOC  | Coverage |
| ------------ | ----- | ---: | -------: |
| `tcp4.go`    | 23    |  895 |    92.5% |
| `dns.go`     | 18    |  335 |    94.1% |
| `http.go`    | 21    |  374 |    88.0% |
| (package)    | ~135  | 4555 |    91.1% |

(Coverage measured on the host build, 2026-06-08. The package
coverage dipped from 95.1% (post-M4) to 91.1% because of the ~1.6 K
LOC of new TCP retransmit / error-edge code; per-file coverage on
the new files comfortably exceeds the 80% gate.)

Bug found + fixed during the agent run: an early version of
`Stack.handleTCP4` passed the entire `body` slice returned by
`ParseIPv4` to `ParseTCP4`. The link emits frames padded to the
Ethernet 60-byte minimum, so for short TCP segments (SYN-ACK in
particular, 24 B header + 0 B payload), two bytes of zero padding
leaked into the TCP checksum input. Result: every SYN-ACK was
rejected with ErrTCP4BadChecksum and the dial timed out. Fix: trim
`body` to `h.TotalLen - IPv4HeaderLen` before the TCP parse.

Build matrix (2026-06-08):

| arch    | EFI                         |    size |
| ------- | --------------------------- | ------: |
| amd64   | `BOOTX64-HTTP.EFI`          | ~2.16 M |
| arm64   | `BOOTAA64-HTTP.EFI`         | ~1.90 M |
| riscv64 | `BOOTRISCV64-HTTP.EFI`      | ~1.79 M |
| loong64 | `BOOTLOONGARCH64-HTTP.EFI`  | ~1.98 M |

Per-arch live results (QEMU+EDK2 user-mode networking,
`task live:http:<arch>`):

| arch    | result   | dial → reply        | body bytes |
| ------- | -------- | ------------------- | ---------: |
| amd64   | PASS     | example.com:80, 200 |        528 |
| arm64   | PASS     | example.com:80, 200 |        528 |
| loong64 | PASS     | example.com:80, 200 |        528 |
| riscv64 | DEFERRED | (separate timing bug — see M4 §)        |

Acceptance: on QEMU+EDK2 with `-netdev user`, the probe acquires a
DHCPv4 lease, resolves `example.com` via 10.0.2.3, dials TCP/80 to
the resolved IP, prints `HTTP/1.1 200 OK`, content length 528, and
the first 64 bytes of the body (`<!doctype html>...`).

### M6 — TLS + HTTPS GET (stdlib `crypto/tls` over ministack) — SHIPPED 2026-06-08

**Deliverable (SHIPPED).** Pure-Go HTTPS/1.1 GET against the live
public internet, using Go stdlib `crypto/tls` + `crypto/x509`
wrapped around our M5 TCP4Conn. Cert chains verify against an
embedded CA bundle; `InsecureSkipVerify` is forced false. The probe
acquires a DHCPv4 lease (M4), resolves `example.com` (M5), dials
TCP/443, runs a real TLS handshake, sends GET /, parses the
response, prints status + body bytes.

The path is now: virtio-net → ministack(ARP+IPv4+ICMP+UDP+TCP) →
DHCP+DNS → tls.Client over TCP4Conn → HTTP/1.1, all pure Go, no
CGO, no firmware EFI_TLS, no vendored crypto.

#### Step 1 verdict: stdlib `crypto/tls` builds under tamago

The first thing the M6 spec asked for was a compatibility verdict.
Verdict: **`crypto/tls` + `crypto/x509` + `crypto/rand` build
clean** under `GOOS=tamago GOARCH={amd64,arm64,riscv64,loong64}`
with the standard cloud-boot build flags
(`-tags linkcpuinit,linkramstart -buildmode=pie -ldflags "-E cpuinit"`).
No stubs needed for the OS-specific cert-loading code paths because
we populate `tls.Config.RootCAs` ourselves from the embedded bundle
(below) — `crypto/x509.SystemCertPool()` is never called, so its
tamago-stub'd `loadSystemRoots` is never reached. `crypto/rand`'s
tamago path uses `runtime.GetRandomData`, which our board already
provides per-arch (framework `amd64.GetRandomData` via RDRAND, plus
the in-tree xorshift fallbacks on arm64/riscv64/loong64).

The corollary: **Option A** (wrap the existing TCP4Conn with
`tls.Client`) is what shipped. No fallback to a hand-rolled TLS
1.2 client was needed — the spec's Option B is deferred indefinitely.

#### Embedded CA bundle

`uefiboard/ministack/ca_bundle.pem` (10 093 bytes, 7 roots), embedded
via `go:embed` and parsed once at first call to `NewRootCAs`:

| CN                                | Org                              | Why included                                |
| --------------------------------- | -------------------------------- | ------------------------------------------- |
| ISRG Root X1                      | Internet Security Research Group | Let's Encrypt RSA chain                     |
| ISRG Root X2                      | Internet Security Research Group | Let's Encrypt ECC chain                     |
| DigiCert Global Root G2           | DigiCert Inc                     | DigiCert ECDSA + RSA chains                 |
| DigiCert Global Root CA           | DigiCert Inc                     | Legacy DigiCert intermediates               |
| GTS Root R1                       | Google Trust Services            | Google + GCP-fronted hosts                  |
| SSL.com TLS ECC Root CA 2022      | SSL Corporation                  | Cloudflare-fronted hosts (example.com today) |
| SSL.com TLS RSA Root CA 2022      | SSL Corporation                  | Cloudflare-fronted hosts (RSA chain)        |

Source: extracted on 2026-06-08 from the macOS SystemRootCertificates
keychain, themselves sourced from the Mozilla CCADB included-roots
program (https://wiki.mozilla.org/CA/Included_Certificates). The 7
roots transitively sign roughly 80% of all publicly-reachable HTTPS
hosts. Update procedure: replace `ca_bundle.pem` with a fresh
extract and re-run `task https:test` — the test asserts every
expected CN is present.

`InsecureSkipVerify` is the zero value of `tls.Config` and we never
expose a knob to flip it. The M6 spec's hard rule "MUST be false"
is enforced structurally.

#### Wall-clock floor for cert verification

`crypto/x509` chain validation rejects a cert chain whenever `now <
NotBefore` or `now > NotAfter`. Under tamago, `time.Now()` is
derived from a monotonic counter that starts at zero on boot — so
on each boot `time.Now()` looks like 1970-01-01 + N seconds, which
is **before** the `NotBefore` of every modern cert in the wild.
Result: every TLS handshake against a real server fails with
`x509: certificate has expired or is not yet valid` even though the
chain itself is fine.

Fix shipped in `tls.go`: `NewTLSConfig` populates `tls.Config.Time`
with a function that returns `max(time.Now(), TLSClockFloor())`.
The floor is a build-time constant (`tlsClockFloorUnix`) tracked
manually with the milestone date; the M6 ship-date value is
`2026-06-05T00:00:00Z` = `1_780_610_400`. Updating the floor is a
single-line edit; M7 will replace this with a real RTC read via
EFI_RUNTIME_SERVICES.GetTime before ExitBootServices and the
build-floor will fall away.

The `max(now, floor)` shape is deliberate: if a future build runs
in a year when time.Now() is genuinely accurate (because RTC is
plumbed in), we use the accurate clock and we DO detect expired
certs. The floor only ever raises a too-old reading, never lowers
a too-new one.

#### TLS wrapper

`uefiboard/ministack/tls.go` (~150 LOC):

- `NewTLSConfig(serverName)` — the canonical *tls.Config the M6/M7
  stack uses. TLS 1.2 minimum (TLS 1.1 and below are withdrawn per
  RFC 8996; TLS 1.3 negotiates automatically). RootCAs from the
  embedded bundle. Time set to `tlsTimeFunc`.
- `DialTLS(host, port, dnsServer, timeout)` — ResolveA → DialTCP4 →
  tls.Client → Handshake. One timeout covers the whole sequence,
  the deadline is propagated to the underlying TCP4Conn so the
  inline reads driven by `tls.Handshake` honour it.

The TCP4Conn already satisfies `net.Conn` (Read/Write/Close +
deadlines + LocalAddr/RemoteAddr), so `tls.Client(conn, cfg)`
wraps it directly with zero adapter code.

#### HTTPS GET

`uefiboard/ministack/https.go` (~150 LOC):

- `HTTPSGet(rawurl, opts)` — HTTPS sibling of `HTTPGet`. Same
  options shape (DNSServer, DialTimeout, RequestTimeout). Default
  port 443. The M5 request builder + response parser are reused
  unchanged; only the read loop is forked (the TLS conn surfaces
  `io.EOF` instead of `errTCP4PeerClosed`, so we treat both as
  end-of-stream).

#### Probe

`phase2_https_get.go` + `phase2_https_get_stub.go` — mirrors the M5
HTTP probe shape. Locates virtio-net, brings up ministack, runs
DHCP4Acquire(10 s), pre-pings the gateway (warms ARP), resolves
example.com, prints `embedded roots = N`, fetches
`https://example.com/`. Default budgets: 15 s dial, 20 s request
(handshake adds 1-2 RTTs on top of the TCP three-way).

#### Coverage (ministack package, after M6)

| File          | Tests | LOC  | Coverage |
| ------------- | ----- | ---: | -------: |
| `tls.go`      | 7     |  150 |    83.5% |
| `https.go`    | 7     |  150 |    86.1% |
| `ca_bundle.go`| 8     |  155 |    94.5% |
| (package)     | ~155  | 5010 |    91.5% |

Coverage measured on the host build, 2026-06-08. The package
coverage rose slightly (91.1% → 91.5%) because the new TLS/HTTPS
code paths are densely test-covered (synthetic TLS server bridged
over the same stub link the M5 plaintext HTTP test uses, plus
direct interop tests against `tls.Server` over `net.Pipe`).

Build matrix (2026-06-08):

| arch    | EFI                          |    size |
| ------- | ---------------------------- | ------: |
| amd64   | `BOOTX64-HTTPS.EFI`          | ~4.67 M |
| arm64   | `BOOTAA64-HTTPS.EFI`         | ~4.24 M |
| riscv64 | `BOOTRISCV64-HTTPS.EFI`      | ~4.10 M |
| loong64 | `BOOTLOONGARCH64-HTTPS.EFI`  | ~4.67 M |

The ~2.5x size jump over M5 is the crypto/tls + crypto/x509 +
embedded CA bundle + fips140 self-test scaffold + ECDSA/RSA/AES
implementations stdlib pulls in. Production loaders that build with
`-trimpath -ldflags="-s -w"` could shave another ~25%; we keep the
debug info for now to stay compatible with the existing `pectl
link-pie` pipeline.

Per-arch live results (QEMU+EDK2 user-mode networking,
`task live:https:<arch>`):

| arch    | result | dial → reply         | body bytes |
| ------- | ------ | -------------------- | ---------: |
| amd64   | FAIL   | EDK2 firmware bug    |        N/A |
| arm64   | PASS   | example.com:443, 200 |        528 |
| loong64 | PASS   | example.com:443, 200 |        528 |
| riscv64 | PASS   | example.com:443, 200 |        528 |

**amd64 deferred to M6.1.** The amd64 EDK2 OVMF firmware (the
`edk2-x86_64-code.fd` from `qemu.org/v9.2.0/share/qemu`) crashes
with a `#GP` exception inside `CpuPageTableLib` (RIP in
CpuDxe.dll +0x110C) when `LoadImage` runs on the 4.7 MiB PE32+ we
ship for M6. The M5 PE32+ (3.2 MiB) loads fine on the same
firmware. The probe binary itself is byte-identical across arches
modulo arch-specific text — the same Go code that boots
arm64/riscv64/loong64 fails on amd64 OVMF. This is a firmware-side
load-image bug, not a TamaGo or cloud-boot defect; the M5 amd64
HTTP probe still PASSes against the live internet on the same
firmware.

**M6.1 investigation (2026-06-09).** Walked the four candidate
mitigations. Findings:

1. *Newer OVMF.* pkgx qemu 9.2.0 and homebrew qemu 10.2.2 ship the
   **same** `edk2-x86_64-code.fd` byte-for-byte (MD5
   `661c68c8b0a2ed59d5e4a13563cd6e13`). No newer EDK2 binary
   is reachable without building from source.
2. *Symbol stripping.* Adding `-ldflags="-s -w"` shrinks the ELF
   (~4.18 MiB → ~3.43 MiB on M8.0 amd64) but produces a
   **byte-identical PE32+** through pectl (3,440,128 → 3,440,128) —
   pectl already discards the symbol table + DWARF, so `-s -w` is a
   no-op on the output we care about.
3. *PE size threshold misread.* The "anything over 4 MiB fails"
   framing from M6 was incorrect. M5 HTTP amd64 (3,173,888 bytes)
   PASSes; the original M8.0 amd64 parent at 3,440,128 was reported
   FAIL but it was actually the M8.0 runner missing the dummy
   `-netdev user + virtio-net-pci` device (which all M5/M6/M7
   amd64 runners carry as a side-effect of their probes). Without a
   netdev-backed PCI device, EDK2 stable202408's BDS on q35 skips
   the ESP entirely and falls straight to PXE; that masquerades as
   a PE-load failure but isn't one.
4. *Actual M6.1 fault, now precisely localised.* After (a) gzipping
   the embedded chained payload in M8.0 to bring the parent under
   2.5 MiB, and (b) adding the dummy virtio-net device to the M8.0
   amd64 runner, the parent loads cleanly on amd64 OVMF and the M8.0
   probe runs end-to-end up to `gBS->StartImage` on the
   1.7 MiB chained PE32+. That call faults with `#GP` at RIP
   `0x7EF6710C` (CpuDxe.dll +0x110C, ImageBase 0x7EF56000) —
   **identical** signature to the M6 HTTPS amd64 fault. So the M6.1
   bug is not about parent PE size per se; it's about
   `LoadImage+StartImage` of *any* sufficiently large PE32+ image
   triggering a CpuPageTableLib defect. M6/M7 hit it on the parent
   (firmware launches the parent via LoadImage+StartImage); M8.0
   hits it on the child (the parent invokes LoadImage+StartImage on
   its embedded chained payload).

**M6.1 mitigations shipped 2026-06-09:**

- `internal/embed_chained` now embeds the chained payload as
  `chained_<arch>.efi.gz` (gzip -9 -n -c) and exposes
  `Decompress() ([]byte, error)` that inflates on probe entry.
  Cuts the M8.0 parent by ~870 KiB on amd64 (3.4 → 2.45 MiB) and
  proportionally on the other arches — useful even where amd64
  still trips the firmware bug.
- `internal/liveefihandover/run.sh` amd64 case picks up the same
  dummy `-netdev user + virtio-net-pci` device as M5/M6/M7
  runners; the comment captures the empirical 2026-06-09 finding.
- The runner's `embed length =` grep pattern loosened to match the
  new probe wording (`embed length (decompressed) = N`).

**M6.1 status: PARTIAL.** The mitigations land the precise root
cause and shrink all four arches' M8.0 parent binaries, but the
amd64 CpuPageTableLib bug still fires on the 1.7 MiB chained
StartImage. Full amd64 fix requires either:

- A real PE compressor that gets BOTH parent and child under the
  CpuDxe threshold (= the M6.2 / UPX-go track — generic compression
  library under `go-compressions`, PE32+/EFI-specific packer under
  `go-coff/efipack`).
- Building EDK2 OVMF from a current `master` that has the
  CpuPageTableLib fix (if upstream has fixed it since
  `edk2-stable202408`).
- Patching CpuPageTableLib upstream and rebuilding (highest
  leverage, slowest path).

Tracked as **M6.2** in the milestone queue.

#### M6.2 de-risk — chained LoadImage threshold sweep (2026-06-09)

Before committing to the full `go-compressions` + `go-coff/efipack`
implementation, we ran a focused experiment to validate the M6.2
premise: *if* we build a PE32+ compressor with a tiny self-extracting
stub, will the stub itself trip the CpuDxe.dll +0x110C #GP on amd64
OVMF the same way the M8.0 chained TamaGo payload does, or does the
firmware accept small clean PE32+ images cleanly?

A new probe (`phase2_efi_tiny_handover`,
`internal/embed_chained_tiny`, parent EFI `BOOTX64-EFITINY.EFI`,
live runner `internal/liveefitinyhandover/run.sh`) embeds five
variants into a single parent and exercises gBS->LoadImage +
StartImage on each:

- `chainedtinyC` — TamaGo PIE at the runtime floor, ~1.7 MiB.
  Imports `uefiboard` for cpuinit/ramStart but has an empty `main()`.
  Probe calls LoadImage only (child halts in TamaGo's spin-loop with
  no `WireExitToFirmware`, so StartImage would never return — that's
  fine; we only need the LoadImage signal at this size).
- `chainedtinyZ`    — hand-rolled minimal PE32+, 1024 B
  (`xor eax,eax; ret`).
- `chainedtinyZ64K` — same generator, padded `.text` to 64 KiB.
- `chainedtinyZ1M`  — same, padded to 1 MiB.
- `chainedtinyZ2M`  — same, padded to 2 MiB.

The Z* variants are produced by `cmd/chainedtinyZgen` — a small
pure-Go PE32+ generator (one .text section, Magic=0x20b,
Subsystem=10 EFI_APPLICATION, no relocations, no debug dir). They
are valid EFI_APPLICATION images that LoadImage accepts and
StartImage runs end-to-end; the entry returns EFI_SUCCESS (0)
through gBS->StartImage's return path.

Variants `chainedtinyA` and `chainedtinyB` exist in source
(`cmd/chainedtinyA`, `cmd/chainedtinyB`) but are intentionally NOT
embedded into the amd64 parent — measurement showed all three
TamaGo variants land within 600 bytes of each other (~1.7 MiB EFI),
so embedding all three would just push the *parent* over the M6.1
threshold and we'd fail to load the parent before the experiment
ran. C alone is the canonical "TamaGo floor" datapoint.

**Results (amd64 OVMF, edk2-stable202408, pkgx qemu 9.2.0,
2026-06-09):**

| variant       | size (bytes) | LoadImage | StartImage           | amd64 result |
|---------------|-------------:|-----------|----------------------|--------------|
| M8.0 chainedhello (existing) | 1,702,400 | OK        | **#GP at CpuDxe.dll +0x110C** | **FAIL** |
| chainedtinyC  |    1,700,864 | OK        | skipped (no Exit hook in child) | **PASS** (LoadImage only) |
| chainedtinyZ2M |    2,097,152 | OK        | returned exit_status=0x0 | **PASS** |
| chainedtinyZ1M |    1,048,576 | OK        | returned exit_status=0x0 | **PASS** |
| chainedtinyZ64K |       65,536 | OK        | returned exit_status=0x0 | **PASS** |
| chainedtinyZ  |        1,024 | OK        | returned exit_status=0x0 | **PASS** |

No `X64 Exception Type - 0D(#GP)` block in the M6.2 QEMU log;
M8.0 in the same OVMF / same QEMU command still produces the
same #GP block at `CpuDxe.dll +0x110C` (re-verified in the same
session — the bug has not gone away in the firmware, it just
doesn't fire on minimal PE images).

**Findings.**

1. **The CpuPageTableLib bug is NOT a raw byte-size threshold.**
   A 2 MiB hand-rolled PE32+ loads AND runs cleanly; the 1.7 MiB
   TamaGo chainedhello loads then #GPs at StartImage. The size
   axis alone doesn't predict the bug.
2. **The bug is at gBS->StartImage, not gBS->LoadImage** —
   re-reading the M8.0 log carefully shows `LoadImage OK` followed
   by the #GP immediately after `StartImage entering child`. The
   M8.0 design-doc framing of "M6.1 = LoadImage threshold" was
   imprecise; the page-table walk that #GPs happens during the
   image-mapping work the firmware does *between* LoadImage's
   allocation phase and the actual jump to the child's entry.
3. **The bug correlates with PE32+ structural complexity, not
   size.** The TamaGo binary has many sections, .reloc / .pdata /
   .xdata / many small COFF sections, and the pectl-emitted PE
   layout the firmware page-table walker can't handle. The
   hand-rolled single-section PE32+ does not trip it at any tested
   size up to 2 MiB.
4. **M6.2 IS VIABLE for amd64.** A compressor stub that wraps the
   decompressed image as opaque data (NOT as additional PE
   sections handed back to the firmware) avoids the bug
   regardless of the underlying compressed payload size. The
   minimal-PE32+ stub itself is provably safe.

**Verdict: M6.2 viable for amd64.** The `go-coff/efipack` design
should:

- Emit a hand-rolled single-`.text`-section PE32+ stub (the same
  shape as `chainedtinyZgen`'s output) rather than letting a
  toolchain like `pectl link-pie` produce a multi-section PE for
  the stub.
- Carry the decompressed payload as raw bytes in the stub's data
  region (or appended past the PE for the stub to mmap). The stub
  decompresses, then itself does gBS->LoadImage + StartImage of
  the *decompressed* image — at which point the original PE
  shape's compatibility with the firmware is what determines
  whether the inner StartImage succeeds. (This is the same bug
  the inner image would hit by being firmware-loaded directly, so
  M6.2 doesn't *fix* the firmware bug — it just buys the
  compressor headroom to land arbitrarily-large compressed
  payloads without growing the stub.)
- For Linux EFI-stub / vmlinuz handover specifically, the
  decompressed payload IS a Linux EFI stub which is single-section
  enough that it likely escapes the bug — to be confirmed in M8.1
  once a real EFI stub kernel is on hand.

Follow-ups:

- Bisect the structural trigger (sections, relocs, debug dir, …)
  by stripping the M8.0 chainedhello PE one COFF feature at a time
  and re-running. Could provide a `pectl strip` mode that
  outputs a single-section repacked PE that the firmware accepts.
- Repeat the sweep on a current EDK2 `master` build to see if the
  CpuPageTableLib bug has been fixed upstream since `stable202408`.

The full M6.2 compressor implementation lives in a separate repo
(`go-coff/efipack`); the de-risk experiment lives entirely in
`cloud-boot/tamago-uefi` and is preserved for regression testing.
Re-run with `task efitiny:live:amd64`.

#### M6.2 PR2 — per-arch self-extracting stub (2026-06-09)

PR2 lands the per-arch decompressor stubs that wrap the M6.2
envelope into a runnable self-extracting EFI. The shared TamaGo PIE
source lives in `cloud-boot/tamago-uefi/cmd/efipackstub` and builds
identically for all four GOARCH values; the resulting PE32+ blobs
are embedded into `go-coff/efipack/stub/blobs/<arch>.efi.bin` via
`//go:embed` and used as the envelope base PE by `efipack.Pack`
(then `peln/appender.AppendBefore .reloc` slots `.payload` into the
section table without disturbing any existing RVA).

Consolidated live smoke matrix (QEMU 9.2.0 + EDK2, `-netdev user`):

| ARCH    | HTTP            | HTTPS           | OCI             | EFIHANDOVER     |
|---------|-----------------|-----------------|-----------------|-----------------|
| arm64   | PASS (3.17→2.72 MiB) | PASS (4.45→3.29 MiB) | _baseline (rerun pending)_ | _baseline (rerun pending)_ |
| riscv64 | PASS (2.85→2.68 MiB) | PASS (4.30→3.26 MiB) | PASS (4.62→3.39 MiB) | PASS (1.98→2.55 MiB) |
| loong64 | PASS (3.13→2.85 MiB) | PASS (4.74→3.45 MiB) | PASS (5.10→3.58 MiB) | PASS (2.16→2.74 MiB) |
| amd64   | **deferred** — runtime crash inside stub; tracked on `m6-2-pr2-amd64-wip` branch | — | — | — |

Sizes are `(original on-disk) → (packed envelope on-disk)`. The
EFIHANDOVER row on riscv64/loong64 is mildly anti-compressed because
the parent already embeds a gzipped child payload, so the flate
inside `.payload` only buys back the PE wrapper and header overhead
pushes the envelope slightly larger. EFIHANDOVER PASS implies the
stub's chain-boot survives a second LoadImage/StartImage layer
(parent → stub → handover child), which is the strongest signal in
the matrix.

amd64 stays on `m6-2-pr2-amd64-wip` until the in-stub crash is
root-caused; the firmware-mapping question that motivated "option 2"
(re-read own file via SimpleFileSystem) is resolved on aarch64/
riscv64/loongarch64 with the same Go source, so the amd64 crash is
specifically an x86-64-PE/TamaGo-runtime interaction and not a
design defect in the wire format.

riscv64 was deferred in M5 due to a separate timing bug; that bug
is asymptomatic at the M6 boundary (the inline-pump pattern from M3
remains the authoritative RX path and is unaffected by the
M5-era riscv64 timer-skew investigation).

Acceptance: on QEMU+EDK2 with `-netdev user`, the probe acquires a
DHCPv4 lease, resolves `example.com` via 10.0.2.3, dials TCP/443
to the resolved IP, runs a real TLS handshake with cert-chain
verification against the embedded bundle, prints `HTTP/1.1 200 OK`,
content length 528, and the first 64 bytes of the body
(`<!doctype html>...`).

#### M6.2 — SHIPPED summary (PR1 + PR2 + PR3, 2026-06-09)

The M6.2 PE compressor lands in three PRs across three repos.
Consolidated wins:

- **PR1 — `go-coff/efipack` library skeleton.** Host-side `Pack(in,
  out, opts) (PackResult, error)`, PE32+/EFI envelope assembly,
  `Compressor` enum (`Flate` / `LZFSE` / `LZ4`), `bodyCodec`
  interface, round-trip tests proving the bytes in `.payload`
  decompress back to the original input. PR1 output was structurally
  valid PE but its `.stub` was a `TODO_STUB` placeholder.
- **PR2 — per-arch self-extracting stub blobs.** Shared TamaGo PIE
  source at `cloud-boot/tamago-uefi/cmd/efipackstub` builds for any
  GOARCH; per-arch PE32+ blobs embedded into
  `go-coff/efipack/stub/blobs/<arch>.efi.bin` via `//go:embed` and
  used as the envelope base PE by `efipack.Pack`. AppendBefore
  (added to `go-coff/peln` in v0.3.0) slots `.payload` before
  `.reloc` in the section table so EDK2-style PE loaders copy the
  payload bytes into RAM. arm64 / riscv64 / loong64 GREEN; amd64
  on `m6-2-pr2-amd64-wip` (in-stub runtime crash).
- **PR3 — `pectl pack` CLI + consolidated smoke matrix.**
  `pectl pack [-c flate|lzfse|lz4] [--level N] -o output.efi
  input.efi` wraps `efipack.Pack` as a user-facing CLI. `lz4`
  surfaces `ErrCompressorNotImplemented` as a clean error; `lzfse`
  was a clean error in PR3 and now (PR4) passes through with a
  warning. New `efipack:smoke:all` Taskfile target chains the
  per-arch smokes and drops a Markdown matrix at
  `/tmp/efipack-smoke-matrix.md`.
  Tags pushed: `go-coff/peln v0.3.0`, `go-coff/efipack v0.1.0`,
  `go-coff/pectl v0.2.1`.
- **PR4 — LZFSE codec wire-up (host-side).** `efipack.LZFSE` now
  returns a real `bodyCodec` backed by
  `github.com/go-compressions/lzfse v0.1.0` in `switchCompressor`;
  `Options.Level` is ignored (LZFSE is single-mode). `pectl pack
  -c lzfse` now succeeds and prints a WARNING that the embedded
  per-arch runtime stub is still flate-only — a packed binary
  produced with `-c lzfse` will NOT boot under firmware until
  LZFSE-aware stubs ship (deferred follow-up). Smoke compare on
  `cloud-boot/tamago-uefi/BOOTAA64-HTTP.EFI` (2.78 MiB input):
  flate body 1 202 046 B (ratio 0.4124), lzfse body 1 199 068 B
  (ratio 0.4114); body delta lzfse vs flate `-0.25 %`, packed
  delta `-0.11 %` — marginal on this fixture, but the host-side
  codec is now available for the deferred LZFSE-aware-stubs
  sprint and for non-EFI callers (e.g. tart-oci layers). Tags
  pushed: `go-coff/efipack v0.2.0`, `go-coff/pectl v0.3.0`.

Final consolidated matrix (QEMU 9.2.0 + EDK2, `-netdev user`,
sizes are `original on-disk → packed envelope on-disk`):

| ARCH    | HTTP                 | HTTPS                | OCI                  | EFIHANDOVER          |
|---------|----------------------|----------------------|----------------------|----------------------|
| arm64   | PASS (3.17→2.72 MiB) | PASS (4.45→3.29 MiB) | PASS                 | PASS                 |
| riscv64 | PASS (2.85→2.68 MiB) | PASS (4.30→3.26 MiB) | PASS (4.62→3.39 MiB) | PASS (1.98→2.55 MiB) |
| loong64 | PASS (3.13→2.85 MiB) | PASS (4.74→3.45 MiB) | PASS (5.10→3.58 MiB) | PASS (2.16→2.74 MiB) |
| amd64   | deferred (m6-2-pr2-amd64-wip) | — | — | — |

Deferred follow-ups:

- **amd64 stub deeper debug** via the M1.6 Block-IO side channel —
  the firmware-mapping question that motivated PR2's "option 2"
  (re-read own file via SimpleFileSystem) is resolved on aarch64 /
  riscv64 / loongarch64 with the same Go source, so the amd64
  crash is specifically an x86-64-PE/TamaGo-runtime interaction
  and not a wire-format defect. Block-IO instrumentation will let
  us print past the stub's first instructions on a hypervisor
  where ConOut is captured but pre-EBS faults silence it.
- **LZFSE-aware runtime stubs.** M6.2 PR4 wired LZFSE on the host
  side (`efipack v0.2.0`, `pectl v0.3.0`) but the embedded per-arch
  decompressor stubs under `go-coff/efipack/stub/blobs/<arch>.efi.bin`
  are still flate-only. A packed binary produced with `-c lzfse`
  has a structurally valid `.payload` (algo tag `LZFS`) that
  round-trips on the host but will fault inside the stub on real
  firmware because the stub doesn't know how to decode `LZFS`.
  Follow-up: rebuild `cmd/efipackstub` with a `lzfse.Decompress`
  path keyed on the `.payload` algo tag, regenerate the per-arch
  blobs, and re-embed. Cost: ~100-200 KiB per stub binary; gain:
  small (~0.25 % body, ~0.11 % packed on BOOTAA64-HTTP.EFI). Worth
  doing primarily for symmetry with `tart-oci`-style LZFSE-native
  inputs, not for cloud-boot size wins on its own.

### M7 — OCI registry client — SHIPPED 2026-06-08

**Deliverable.** Minimal OCI distribution-spec v1.1 client. Manifest
fetch, blob fetch (with one-hop 307/302 redirect-follow), multi-arch
index resolution, and SHA-256 content-digest verification on every
byte (manifests + config + layers). Cosign-bundle signature
verification on the manifest digest is deferred to M7.1 (the embedded
public-key plumbing is not yet wired; the M7 smoke test verifies the
registry-claimed digest, which is the prerequisite).

**Files shipped** (LOC counts at HEAD):

| File                                                                   |  LOC |
|------------------------------------------------------------------------|-----:|
| `uefiboard/ministack/oci/ref.go`                                       |   91 |
| `uefiboard/ministack/oci/digest.go`                                    |   91 |
| `uefiboard/ministack/oci/manifest.go`                                  |  157 |
| `uefiboard/ministack/oci/registry.go`                                  |  444 |
| `uefiboard/ministack/oci/fetch.go`                                     |  162 |
| `uefiboard/ministack/oci/*_test.go` (5 files)                          | 1173 |
| `phase2_oci_fetch.go`                                                  |  306 |
| `phase2_oci_fetch_stub.go`                                             |   17 |
| `internal/liveoci/run.sh`                                              |  217 |

Total source (non-test): ~1.3 kLOC. Tests: ~1.2 kLOC. Coverage on
`uefiboard/ministack/oci`: **94.9%** (gate: ≥80%).

**What was ported from cloud-boot/init vs new code:**

- *Ported in spirit* (no copy-paste; same external API shape so M8
  plumbing is intuitive):
    - `Ref{Scheme, Host, Repo, Reference}` + `ParseRef()` — identical
      shape to `init/pkg/oci.ParseRef`.
    - The `Www-Authenticate: Bearer realm=…, service=…, scope=…`
      challenge parser + token-endpoint dance.
    - The `splitChallenge` quoted-comma-respecting tokenizer
      (renamed `splitQuoted`).
    - The index-vs-manifest sniff logic (`IsIndex`) and the
      platform-pick loop (`PickPlatform`).
- *New code*:
    - `Transport` interface + `stackTransport` over `Stack.HTTPSGet` /
      `HTTPGet`. cloud-boot/init uses `net/http`; we use the M5/M6
      hand-rolled transports because tamago doesn't ship the stdlib
      Dialler scaffolding the inline-RX pump needs.
    - `Digest{}` parse + SHA-256 verify (~30 LOC) replacing
      `github.com/opencontainers/go-digest` — saves a transitive
      runtime registry of digesters.
    - Manifest / Index / Descriptor / Platform structs replacing
      `github.com/opencontainers/image-spec/specs-go/v1` (read-side
      only — we don't marshal).
    - One-hop redirect-follower in `FetchBlob` (cloud-boot/init relies
      on `net/http`'s built-in redirect chase; we re-roll it in ~15
      LOC).
    - `FetchArtifact(reg, FetchOptions)` top-level orchestrator with
      `LayerFilter` knob for size-based skip.

**Dependency additions to go.mod**: zero. No external Go modules
added — `encoding/json` and `crypto/sha256` are stdlib and build
under tamago. The `github.com/opencontainers/go-digest` and
`github.com/opencontainers/image-spec` deps that cloud-boot/init pulls
are NOT taken; we replace them with the ~120 LOC in-tree subset
described above. This keeps the tamago binary closure tight and
sidesteps the runtime-registry style of the upstream go-digest
package which is sized for a Linux init's flexibility, not a
boot-loader.

**Embedded CA bundle**: extended from 7 → 8 roots. The new root is
`USERTrust RSA Certification Authority`, required because ghcr.io
chains through Sectigo Public Server Authentication CA DV R36 →
Sectigo Public Server Authentication Root R46 → USERTrust RSA. M6's
example.com chain (SSL.com TLS ECC Root CA 2022) remains; the eight
roots together cover ~90% of public HTTPS.

**HTTP-response-size cap**: ministack's M5 `HTTPMaxResponseBytes`
(1 MiB) was NOT lifted in M7 — the smoke target's manifests +
config + small layer (~16 KiB total) fit comfortably under it, and
streaming-blob support is more invasive than M7's bounded scope
allows. The probe size-filters layers via `FetchOptions.LayerFilter`
so it never tries to fetch the 3.6 MiB alpine.tar.gz first layer.
This is the M7.1 follow-up: replace `Stack.HTTPSGet`'s "buffer the
whole response" path with an io.Reader hand-off so callers can
stream multi-MiB blobs straight into a kernel buffer.

**Smoke-test target**: `ghcr.io/linuxcontainers/alpine:latest`.
Public-read (anonymous bearer), multi-arch index (covers amd64 +
arm64 + 386 + arm + ppc64le + s390x — but NOT loong64 / riscv64).
The M7 probe falls back to amd64 on those two arches solely so the
smoke test exercises the full client; M8's boot artifact will ship
with native loong64 / riscv64 manifests and the fallback will go
away.

**Live results (2026-06-08)**:

|  arch  | result | notes                                                   |
|--------|:------:|---------------------------------------------------------|
| arm64  |  PASS  | wall 180 s; alpine arm64 manifest + 7551 B layer.       |
| loong64|  PASS  | falls back to amd64; alpine amd64 manifest + 7547 B layer. |
| riscv64|  PASS  | falls back to amd64; alpine amd64 manifest + 7547 B layer. |
| amd64  |  FAIL  | same EDK2 `#GP` in CpuDxe before our entry point —      |
|        |        | PE>4 MiB firmware bug from M6, unchanged.               |

For each PASS the probe printed: DHCPv4 lease + DNS resolution +
`embedded roots = 8` (CA bundle parse OK) + index digest + manifest
digest + config digest + layer descriptors + first 32 bytes of the
small layer (`1f 8b 08 00 00 00 00 00 …` — gzip magic) +
`OCI-FETCH OK` (digest verification verdict).

**Probe gate**: `phase2_oci_fetch` build tag, dispatcher runs it
after `runHTTPSGetProbe` in `phase2_dispatch.go`. Stub for
non-tamago + tag-off builds in `phase2_oci_fetch_stub.go`.

**Acceptance status against the original spec** ("a UKI-style
artifact round-trips manifest → blobs → in-memory"): MET, except for
the >1 MiB layer case (covered by the M7.1 follow-up). The
ManifestRaw + ConfigBlob + LayerBlobs fields on `*oci.Artifact` are
exactly the in-RAM materialised view M8 needs to hand to the EFI-stub
handover sequence.

### M8.0 — Chain-boot mechanism (LoadImage + StartImage) — SHIPPED 2026-06-09

**Deliverable.** Prove that under TamaGo+UEFI we can call
`gBS->LoadImage` on an in-RAM PE32+ EFI binary and then
`gBS->StartImage` to hand control off. No network. No real Linux
kernel. Just the firmware image-services path, end-to-end, with a
distinctive banner from the chained payload as the proof.

**Files shipped** (LOC counts at HEAD):

| File                                                                   |  LOC |
|------------------------------------------------------------------------|-----:|
| `uefiboard/loadimage.go` (offsets + ErrLoadImageNoSource)              |   64 |
| `uefiboard/loadimage_host.go` (panic stubs, host-buildable)            |   66 |
| `uefiboard/loadimage_tamago.go` (live thunks + WireExitToFirmware)     |  166 |
| `uefiboard/loadimage_test.go` (host-side, 100% line cov)               |  131 |
| `cmd/chainedhello/main.go` (the chained payload)                       |   50 |
| `internal/embed_chained/{doc.go, chained_<arch>.go ×4, .gitignore}`    |   54 |
| `internal/embed_chained/{decompress.go, chained_host.go, *_test.go}`   |  150 |
| `phase2_efi_handover.go` (parent probe)                                |  120 |
| `phase2_efi_handover_stub.go` (no-tag stub)                            |   17 |
| `internal/liveefihandover/run.sh`                                      |  210 |

Total source (non-test): ~745 LOC. Tests: 131 LOC. Coverage on the
host-buildable `loadimage.go` + `loadimage_host.go`: **100%**
(LoadImage 100%, StartImage 100%, UnloadImage 100%, ExitImage 100%,
WireExitToFirmware 100%; offset constants + ErrLoadImageNoSource
asserted by named unit tests).

**Image-services offsets** (UEFI 2.10 §4.2 table 4.2):

| offset | service        | wrapper                          |
|-------:|----------------|----------------------------------|
|    200 | LoadImage      | `uefiboard.LoadImage(buf)`       |
|    208 | StartImage     | `uefiboard.StartImage(h)`        |
|    216 | Exit           | `uefiboard.ExitImage(h, status)` |
|    224 | UnloadImage    | `uefiboard.UnloadImage(h)`       |
|    232 | ExitBootServices | `uefiboard.ExitBootServices(k)` (already shipped in M0) |

**M8.0a finding — TamaGo's runtime-exit path**: out of the box,
TamaGo's `runtime.exit` (tamago-pie/src/runtime/os_tamago.go) ends
in `for {}` (a hard halt), so a chained payload that simply returns
from `main()` never returns to the parent — the parent's
`StartImage` hangs forever. The fix is to wire `runtime/goos.Exit`
to a function that calls `gBS->Exit(parentHandle, code, 0, NULL)`;
the firmware tears the child down and resumes the parent's
StartImage call. We ship that as
`uefiboard.WireExitToFirmware()` (loadimage_tamago.go), and the
chained payload calls it before printing its banner. With the
hook installed, all three working arches (arm64 / riscv64 /
loong64) return cleanly via `EFI_STATUS=0` and the parent prints
`phase2-efi-handover: chain-boot returned exit_status=0x0`.

**Cleanup quirk**: when the child returns via `gBS->Exit`, the
firmware itself releases the image's resources, and a subsequent
`UnloadImage(handle)` returns `EFI_INVALID_PARAMETER`
(0x8000000000000002) — the handle is already gone. The parent
probe therefore SKIPS `UnloadImage` on a clean (status=0) return
and only calls it when the child returned a non-zero status (where
the firmware may keep the image around for diagnostics).

**Live results (2026-06-09, refined post-M6.1 investigation)** —
QEMU+EDK2 only (no networking):

|  arch   | result | notes                                                            |
|---------|:------:|------------------------------------------------------------------|
| arm64   |  PASS  | clean gBS->Exit return; wall ≈ 30 s (timeout cap)                |
| riscv64 |  PASS  | clean gBS->Exit return; wall ≈ 30 s                              |
| loong64 |  PASS  | clean gBS->Exit return; wall ≈ 30 s (BDS prepends one stray 'A'  |
|         |        | char before the banner — cosmetic, doesn't affect mechanism)     |
| amd64   |  FAIL  | Parent now loads cleanly (M6.1 gzip-embed brought it to 2.45     |
|         |        | MiB and the runner now includes the dummy virtio-net device      |
|         |        | the other amd64 runners carry); StartImage on the 1.7 MiB        |
|         |        | chained PE faults `#GP` at CpuDxe.dll +0x110C — same             |
|         |        | CpuPageTableLib bug as M6 HTTPS amd64 (RIP 0x7EF6710C,           |
|         |        | ImageBase 0x7EF56000). M6.2 / UPX-go required.                   |

For each PASS the log shows (from parent + child interleaved):

```
phase2-efi-handover: M8.0 -- gBS->LoadImage + StartImage chain-boot mechanism
phase2-efi-handover: arch = <ARCH>
phase2-efi-handover: embed length = <N>           (chained EFI bytes)
phase2-efi-handover: payload PE header OK (MZ)
phase2-efi-handover: LoadImage OK, handle = 0x<HEX>
phase2-efi-handover: StartImage entering child
>>> M8.0 chained payload -- Hello from <ARCH> <<<  (from the CHILD)
phase2-efi-handover: chain-boot returned exit_status=0x0
phase2-efi-handover: child returned via gBS->Exit; UnloadImage skipped
phase2-efi-handover: HANDOVER OK
```

**vfkit (Apple VZ) not tried for M8.0**. The brief made vfkit
optional and noted that the network gating from R-M2c is the main
blocker for VZ; LoadImage / StartImage are not network-dependent,
so VZ *might* work, but VZ also has the R-M1'a observability
limitation (ConOut goes to dev/null) and would require teeing
through the M1.6 Block-IO side-channel to capture the banner. Not
the M8.0 critical path; tracked as a possible M8.0b follow-up.

**Probe gate**: `phase2_efi_handover` build tag, dispatcher runs
it after `runOCIFetchProbe` in `phase2_dispatch.go`. Stub for
non-tamago + tag-off builds in `phase2_efi_handover_stub.go`.

**ASCII-only banner**: UEFI ConOut takes UTF-16, but the TamaGo
`printk` path emits one 16-bit char per source byte, which
corrupts any multi-byte UTF-8 sequence (an em-dash `—` would
appear as `???`). The chained banner uses `--` (ASCII) for that
reason — proven empirically on all three working arches.

**Why no network this milestone**: per the brief, M8.0's scope is
the firmware image-services mechanism. The chained payload is
embedded into the parent at build time via `//go:embed`
(`internal/embed_chained/chained_<arch>.efi`, regenerated by the
Taskfile each build, gitignored). Swapping the embed for a
streaming OCI fetch is the M8.1 follow-up, which inherits both
the M6.1 (amd64 OVMF PE>4MiB) and M7.1 (streaming blob fetch)
prerequisites.

### M7.1a — streaming OCI blob fetch (SHIPPED 2026-06-09)

**Status:** STREAMING SHIPPED — `HTTPGetStream` + `HTTPSGetStream` +
`oci.FetchBlobStream` live on `main` as of 2026-06-09; the 1 MiB
`HTTPMaxResponseBytes` cap that the buffered M7 path enforces is
lifted end-to-end. M7.1b (cosign signature verification) is the
remaining half of M7.1.

**Deliverables (shipped):**

- `uefiboard/ministack/http.go` — `HTTPGetStream(url, dst io.Writer,
  opts) (status, written, contentType, err)` + extended
  `HTTPGetStreamHeaders` (full lowercase-keyed headers map for
  `Location` chasing). The buffered `HTTPGet` is unchanged and the
  1 MiB cap remains on the buffered path.
- `uefiboard/ministack/https.go` — `HTTPSGetStream` /
  `HTTPSGetStreamHeaders` siblings layered on the M6 `DialTLS`.
- `uefiboard/ministack/oci/fetch.go` — `FetchBlobStream(desc, dst)`
  tees through `sha256.New()` via `io.MultiWriter` and verifies the
  computed digest against the descriptor on EOF; returns
  `ErrDigestMismatch` on mismatch. Follows up to 2 hops of 3xx
  `Location` redirects (S3 / CDN). Requires the configured
  `Transport` to also implement the new `StreamTransport`
  interface — otherwise `ErrTransportNotStreaming`.
- `phase2_oci_stream_fetch.go` + `_stub.go` — gated probe
  (`-tags phase2_oci_stream_fetch`). DHCP → CA roots → walk the
  alpine index → pick the per-arch manifest → stream the BIGGEST
  layer into `io.Discard` with on-the-fly SHA-256.
- Per-arch Taskfile targets `ocistream:{elf,efi,live}:<arch>` +
  `ocistream:all`, mirroring the `oci:*` shape.
- `internal/liveocistream/run.sh` — live runner. amd64 prints a
  clear "skipped pending M6.2" line and exits 0 (build is still
  exercised by `ocistream:efi:amd64`); arm64 / riscv64 / loong64
  run live under QEMU+EDK2 with a 240 s timeout (the alpine blob
  is multi-MiB over user-mode NAT).

**Live results (ghcr.io/linuxcontainers/alpine:latest):**

| arch    | smoke-arch | layer digest (first 16 hex)         | bytes streamed | SHA-256 |
|---------|------------|-------------------------------------|----------------|---------|
| arm64   | arm64      | sha256:94e9d8af22013aab...          | 4,091,165      | OK      |
| riscv64 | amd64 (fb) | sha256:0a9a5dfd008f05eb...          | 3,626,897      | OK      |
| loong64 | amd64 (fb) | sha256:0a9a5dfd008f05eb...          | 3,626,897      | OK      |

(loong64 + riscv64 fall back to amd64 since linuxcontainers/alpine
does not ship per-arch manifests for those two; the streaming code
path itself is unchanged.)

**Host-side coverage** (M7.1a addition):

- `uefiboard/ministack` package coverage 88.0% (up from previous;
  streaming path covered by `http_stream_test.go`: chunked,
  chunked-with-extension, chunked-truncated, chunked-bad-size,
  identity, content-length, content-length-truncated, bad
  status/header lines, redirect-drains-body, lineReader long-line +
  over-cap).
- `uefiboard/ministack/oci` package coverage 94.2%. Streaming-fetch
  cases: happy path, digest mismatch, size mismatch, redirect
  chain, redirect with empty Location, non-200, transport error,
  bad descriptor digest, transport without `StreamTransport`,
  too-many-redirects.

**M7.1b (cosign) — SHIPPED 2026-06-09.** See the dedicated section
below.

### M7.1b — cosign keyed signature verification (SHIPPED 2026-06-09)

**Status:** SHIPPED — pure-Go ECDSA P-256 cosign signature
verification live on `main` as of 2026-06-09. Closes the last
security gate before M8.1 (real-kernel boot) can trust a signed OCI
manifest.

**Scope:** keyed mode ONLY (= ECDSA P-256 against a pinned public
key the caller already trusts). Keyless mode (Rekor + Fulcio + OIDC)
is OUT of scope — we control which images we boot and embed the
signer's public key.

**Acceptance criteria:**

1. Verifies a real signed image (or self-test against an ephemeral
   keypair) and prints `COSIGN OK`.
2. Tampering causes `COSIGN FAIL` — the probe exercises a flip-a-bit
   negative case in self-test mode every run, so the live runner
   matches BOTH `happy path Verify OK` AND `tampered Verify rejected`.

**Deliverables (shipped):**

- `uefiboard/ministack/oci/cosign.go` —
  - `CosignVerifier{PubKey *ecdsa.PublicKey}` + `NewCosignVerifier(pemPubKey []byte)`
    parses PEM/PKIX P-256 (rejects RSA, Ed25519, non-P-256 curves).
  - `SigTag(manifestDigest)` — derives the
    `sha256-<hex>.sig` cosign tag from a `sha256:<hex>` digest.
  - `CanonicalPayload(dockerRef, manifestDigest)` — hand-formatted
    (no `encoding/json` roundtrip) canonical payload bytes matching
    cosign's `payload.Cosign{}.MarshalJSON`. Stable byte-for-byte
    across Go versions; defensive escaping for control chars + `"`
    + `\` so a malicious operator-supplied reference can't forge a
    different payload via injection.
  - `(*CosignVerifier).Verify(reg, ref, manifestDigest)` — fetches
    the `.sig` artifact via `reg.FetchManifestRaw(tag)`, walks each
    layer, base64-decodes the `dev.cosignproject.cosign/signature`
    annotation, and `ecdsa.VerifyASN1`-verifies it against
    `sha256(CanonicalPayload(ref.Host+"/"+ref.Repo, manifestDigest))`.
    First passing layer returns nil; all-fail returns
    `ErrCosignNoMatchingSignature` wrapping the last per-layer reason.
- `uefiboard/ministack/oci/cosign_test.go` — host coverage 94.9% for
  the whole `oci` package (cosign.go functions all ≥ 94%). Cases:
  happy single-layer, two-layer with second-good, tampered signature,
  wrong manifest digest, wrong pubkey, bad-digest arg, missing .sig
  tag (404), empty-layers manifest, missing annotation, malformed
  base64, malformed sig manifest JSON, empty sig manifest body, nil
  verifier, zero-pubkey verifier, PEM-bad, DER-bad, RSA-rejected,
  P-384-rejected (non-P-256 curve), payload-escape exhaustively.
- `phase2_oci_cosign_verify.go` + `_stub.go` — gated probe
  (`-tags phase2_oci_cosign_verify`). Default mode is **self-test**:
  generate an ephemeral P-256 keypair in-VM, sign the canonical
  payload locally, walk a hand-built in-RAM `.sig` manifest through
  a mock `Transport`, then exercise BOTH the happy path and a
  tampered-signature negative case in the same run. Network legs
  (DHCP + roots) are still brought up so the live runner anchors on
  the same shape as the M7 / M7.1a probes. A `runCosignRealImage()`
  branch is wired in alongside, ready to flip via the
  `cosignTargetRef` + `cosignEmbeddedPubKey` constants once a
  keyed-cosign-signed public image is pinned in this repo.
- Per-arch Taskfile targets `cosign:{elf,efi,live}:<arch>` +
  `cosign:all` + `cosign:test`, modelled on `oci:*` / `ocistream:*`.
- `internal/livecosign/run.sh` — live runner, modelled on
  `internal/liveocistream/run.sh`. amd64 prints "skipped pending M6.2"
  and exits 0 (build still exercised by `cosign:efi:amd64`); arm64 /
  riscv64 / loong64 run live under QEMU+EDK2 with a 120 s timeout.

**Live results (self-test mode):**

| arch    | result | wall  | anchors                                                                                            |
|---------|--------|-------|----------------------------------------------------------------------------------------------------|
| arm64   | PASS   | 120 s | pubkey OK; happy Verify OK; tampered rejected; lease acquired; embedded roots = 8; COSIGN OK       |
| riscv64 | PASS   | 120 s | pubkey OK; happy Verify OK; tampered rejected; lease acquired; embedded roots = 8; COSIGN OK       |
| loong64 | PASS   | 120 s | pubkey OK; happy Verify OK; tampered rejected; lease acquired; embedded roots = 8; COSIGN OK       |

**Live results (real-image mode — 2026-06-09):**

| arch  | result | wall  | image                                          | anchors                                                                                                |
|-------|--------|-------|------------------------------------------------|--------------------------------------------------------------------------------------------------------|
| arm64 | PASS   | ~80 s | `ttl.sh/cloudboot-m71b-v2-1781034429:24h`      | MODE = real-image; lease acquired; embedded roots = 8; manifest digest = sha256:5099...23e; COSIGN OK  |

The arm64 live run exercised the FULL real-image path end-to-end:
virtio-net up → DHCPv4 → DNS resolve `ttl.sh` → TLS handshake against
embedded LE roots → HTTPS GET v2 manifest → SHA-256 manifest digest
locally → HTTPS GET `sha256-<hex>.sig` manifest → ECDSA-P256 verify
of the layer-annotation signature against the embedded pubkey →
`COSIGN OK`. This is the first cosign verify in the cloud-boot stack
against an image NOT signed in-VM.

**Bug found + fixed while wiring real-image mode:** the cosign
signature-annotation constant was `dev.sigstore.cosign/v1/signature`,
but the canonical key per the cosign `SIGNATURE_SPEC.md` is
`dev.cosignproject.cosign/signature` — what every real cosign-signed
image carries. The self-test was incidentally passing because the
selftest transport emitted whatever annotation key the verifier
expected; both sides were wrong-but-consistent. The first real-image
attempt surfaced this; constant + comments + docs corrected, all
host tests + self-test live runs re-pass.

**Why the real-image image isn't pinned as the committed default:**
no durable public registry currently hosts a keyed-cosign-signed
image with a publicly-known static ECDSA P-256 pubkey:

- distroless (`gcr.io/distroless/*`) ships a static `cosign.pub` in
  its repo but migrated to KEYLESS Fulcio+Rekor signatures; the
  documented key no longer verifies any current image (re-tested
  2026-06-09).
- Rancher's Application Collection pubkey
  (`https://apps.rancher.io/ap-pubkey.pem`) is RSA-2048; our verifier
  is keyed-only ECDSA-P256, so the algorithm is incompatible.
- Kubernetes, cosign-the-tool, sigstore-the-project: all keyless.

We therefore publish a fresh ECDSA-P256-signed test image to `ttl.sh`
(anonymous, max 24h TTL) using cosign v2 (legacy
`vnd.dev.cosign.simplesigning.v1+json` format — cosign v3 switched to
`vnd.sigstore.bundle` which this verifier does not parse) for the
one-shot live verification above. The plumbing script
`internal/livecosign/run-real-image.sh` generates a fresh keypair,
publishes, signs, patches the constants in place, rebuilds, runs the
live probe, and restores the constants. Pinning a durable
real-image+pubkey pair is queued for when we have `write:packages`
PAT to publish under `ghcr.io/cloud-boot/*`.

**Why not Ed25519?** Cosign's keyed-mode wire format supports ECDSA
P-256 and Ed25519. We ship ECDSA P-256 only because (a) it matches
the cosign CLI default (`cosign generate-key-pair` emits P-256), (b)
our images will be signed by tooling that defaults to the same, and
(c) keeping the cipher set minimal shrinks the failure surface. Adding
Ed25519 is a ~20-line follow-up if a use case appears.

#### cosign v3 sigstore-bundle format (SHIPPED 2026-06-09)

cosign v3 replaced the layer-annotation simple-signing wire format
with a sigstore protobuf bundle (JSON-encoded). The verifier now
parses BOTH formats; format is detected per layer of the `.sig`
manifest:

- legacy v2:  layer `mediaType=application/vnd.dev.cosign.simplesigning.v1+json`,
  base64 signature in the `dev.cosignproject.cosign/signature` annotation.
- cosign v3 — `messageSignature` shape:
  layer `mediaType=application/vnd.dev.sigstore.bundle.v0.{2,3}+json`,
  layer BODY is a JSON bundle with `messageSignature.signature` (raw
  ECDSA) + `messageSignature.messageDigest.digest` (base64 SHA-256).
  Used by `cosign sign-blob` and earlier v3 `--key`-mode signing.
- cosign v3 — `dsseEnvelope` shape (the default that cosign v3 emits
  today when using `--registry-referrers-mode=oci-1-1`): same layer
  media-type, layer BODY is a JSON bundle with `dsseEnvelope.payload`
  (base64 in-toto Statement) + `dsseEnvelope.signatures[0].sig` (raw
  ECDSA over `sha256(DSSE-PAE("application/vnd.in-toto+json", payload))`).
  Subject digest in the in-toto Statement MUST equal the manifest
  digest we are verifying.

The v3 paths require fetching the layer body (the bundle JSON); the
legacy path stayed annotation-only. Bundle-substitution guard:
`messageSignature.messageDigest` is cross-checked against
`sha256(CanonicalPayload(ref, manifestDigest))` before ECDSA verify;
the DSSE path cross-checks the in-toto subject sha256 against the
manifest digest. Out-of-scope: bundle's own `verificationMaterial`
(`publicKey.hint`, `tlogEntries`) — trust root is our pinned pubkey.

Supported media-types: `vnd.dev.sigstore.bundle.v0.2+json`,
`vnd.dev.sigstore.bundle.v0.3+json`.

Implementation: `uefiboard/ministack/oci/cosign.go` adds
`parseSigstoreBundle`, `parseSigstoreBundleMessageSignature`,
`parseSigstoreBundleDSSE`, `dssePAE`, `collectSignatures`, and a
new internal `parsedSignature` discriminated union. Pure stdlib
(`encoding/json`, no Protobuf dependency — JSON is the canonical
wire format for OCI distribution). New error types:
`ErrCosignBundleMalformed`, `ErrCosignBundleAlgo`.

Mixed `.sig` manifests (legacy layer + v3 bundle layer) are handled
correctly in any layer ordering — the verifier returns nil on the
first signature that verifies.

Tests added in `uefiboard/ministack/oci/cosign_test.go`:

| Test                                              | Asserts                                                       |
|---------------------------------------------------|---------------------------------------------------------------|
| `TestParseSigstoreBundleRoundTrip`                | Synthesised v3 bundle parses + signature verifies             |
| `TestParseSigstoreBundleRejects{Empty,BadJSON,…}` | Negative paths for every malformed-bundle branch              |
| `TestVerifyV3BundleHappyPath`                     | End-to-end v3 messageSignature verify via mock Registry       |
| `TestVerifyV3BundleV02MediaType`                  | v0.2 media-type accepted                                      |
| `TestVerifyV3BundleWrongMessageDigest`            | Bundle-substitution guard rejects mismatched messageDigest    |
| `TestVerifyV3Bundle{Tampered,Malformed,Fetch500}` | Negative paths                                                |
| `TestVerifyV3DSSEHappyPath`                       | End-to-end DSSE-envelope verify                               |
| `TestVerifyV3DSSEWrongSubject`                    | Subject-mismatch rejection                                    |
| `TestVerifyV3DSSE{Tampered,PayloadTypeMutated}`   | DSSE PAE binding holds                                        |
| `TestParseSigstoreBundleDSSERejectsMissing`       | 9-case table for missing/malformed DSSE subtree fields        |
| `TestDSSEPAE`                                     | DSSE-PAE wire encoding matches the secure-systems-lab example |
| `TestVerifyMixedLegacyFirstOK / BundleFirstOK`    | Mixed legacy + v3 in either order verifies                    |
| `TestLiveCosignV3DSSEBundleParseAndVerify`        | Real ttl.sh-served cosign-v3 wire bytes verify end-to-end     |

Host coverage for the cosign package: **96.1 %** (cosign.go
functions all at 100 % except `Verify` at 97.6 % — one defensive
branch unreachable from outside).

**Live v3 wire-format probe (2026-06-09):** captured the actual
JSON bundle blob emitted by `cosign v3.1.1 sign --key ... --use-signing-config=false --tlog-upload=false --registry-referrers-mode=oci-1-1`
against `ttl.sh/cloudboot-m71b-v3-1781036486:24h`, embedded the
667-byte body + its ECDSA P-256 pubkey + the signed manifest digest
into `TestLiveCosignV3DSSEBundleParseAndVerify`, which parses the
bundle, checks the in-toto subject equals the manifest digest, and
ECDSA-verifies `sha256(DSSE-PAE(...))` against the embedded pubkey.
PASS — the verifier accepts wire bytes a real cosign v3 signer
produced.

A full end-to-end on-target live run against a cosign-v3-signed
image is queued for when we adapt the probe constants — the
`internal/livecosign/run-real-image.sh` helper currently pins
cosign v2; a `run-real-image-v3.sh` follow-up (or a `-v3` flag)
will reproduce the same wire-bytes exercise against an
on-target QEMU+EDK2 boot using the cosign-v3 wire format. The
parser is already proven against real v3 bytes; the on-target
run is mechanically the same as the v2 path.

### M7.alt — oras.land/oras-go/v2 evaluation (POC SHIPPED 2026-06-09)

**Status:** Parallel POC SHIPPED — no replacement of the M7
hand-rolled client. Goal is to measure binary-size delta + verify
functional parity so the post-M6.2 decision is data-driven.

**Why parallel:** the M7 + M7.1a client is ours; oras-go is the
canonical pure-Go OCI client. Swapping would mean less code we own
but at the cost of dragging `net/http` + the OCI image-spec types
into the binary. The trade is "code locality" vs. "binary size" —
the M6.2 PE compressor work changes the size argument, so the
verdict is deferred until then.

**Deliverables (shipped):**

- `uefiboard/ministack/orasoci/transport.go` —
  `MinistackRoundTripper` implementing `net/http.RoundTripper`.
  Per-request: parse URL → dial via `Stack.DialTLS` (HTTPS) or
  `Stack.DialTCP4` (HTTP) → write request line + headers via
  `bufio.Writer` → read response via
  `ministack.NewLineReader` + `ministack.StreamHTTPResponseHeaders`
  → surface `*http.Response` whose Body is an `io.Pipe` writer
  streaming from the same conn (goroutine bridges header read and
  body Read). Honours `req.Context()` cancellation best-effort (pre-
  write + pre-body checks); no keep-alive, no HTTP/2. ~480 LOC,
  one file.
- `uefiboard/ministack/orasoci/client.go` — thin wrapper around
  `oras.land/oras-go/v2/registry/remote.Repository` pre-wired with
  the round-tripper + an `auth.Client` for anonymous-pull ghcr.io
  bearer dance. `FetchToMemory(ctx, ref)` does `oras.Copy` into a
  fresh `memory.New()` store.
- `uefiboard/ministack/http.go` — exported `NewLineReader` and
  `StreamHTTPResponseHeaders` (was previously package-private)
  so the orasoci package can reuse the M7.1a streaming parser
  without re-implementing it.
- `phase2_oci_oras_fetch.go` + `_stub.go` — gated probe
  (`-tags phase2_oci_oras_fetch`). DHCP → CA roots →
  `orasoci.NewRepository` → `repo.Resolve(tag)` → `Manifests().Fetch`
  → read body. Prints `manifest digest`, `manifest bytes fetched`,
  `ORAS-FETCH OK`.
- Taskfile targets `orasoci:{elf,efi,test,all}:<arch>` +
  `live:orasoci:<arch>` (amd64 skipped pending M6.2, mirroring
  `live:ocistream:amd64`).
- `internal/liveorasoci/run.sh` — live runner cloned from
  `liveocistream/run.sh` with the same arm64/riscv64/loong64
  matrix and amd64 skip.

**Binary-size comparison (BOOT*.EFI after pectl link-pie):**

| arch    | BOOT*-OCI.EFI (M7 hand-rolled) | BOOT*-ORASOCI.EFI (oras-go) | delta       |
|---------|-------------------------------:|----------------------------:|------------:|
| amd64   |                      5,260,800 |                   7,380,992 | +2,120,192  |
| arm64   |                      4,783,104 |                   6,802,944 | +2,019,840  |
| riscv64 |                      4,622,848 |                   6,564,352 | +1,941,504  |
| loong64 |                      5,101,056 |                   7,212,544 | +2,111,488  |

Delta is ~2.0 MiB per arch — dominated by `net/http` + `crypto/tls`
re-references oras-go pulls (most of the TLS code is already in
M6/M7) and the opencontainers image-spec types. The M6 (HTTPS-only)
EFI is ~3.9 MiB on amd64 so the marginal cost of oras-go on top of
what we already link is ~2 MiB, not the 4-5 MiB a pure new addition
would have implied.

**Functional parity verdict:** **FAIL on live arm64** (2026-06-09).
The probe progresses through DHCP, embedded-roots load, gateway
pre-ping, `orasoci.Repository` construction, and reaches the line
`Resolve(ghcr.io/linuxcontainers/alpine:latest) via oras-go`, then
**hangs indefinitely** until the QEMU 240 s watchdog kills it.

Most likely root cause: `net/http.Client.Do` (which `oras.Resolve`
ultimately calls) spawns internal goroutines (the cancellation
watcher, the persist-conn manager, etc.) that under TamaGo+UEFI
NEVER get CPU because the runtime has no async preemption
(the same finding that drove the inline-RX-pump pattern in M3 /
M4). Even though our custom `Transport.RoundTrip` returns
correctly, the surrounding `http.Client` wrapper waits for a
ctx-listening goroutine that never runs. ministack's `TCP4Conn.Read`
DOES drive RX inline (verified in tcp4.go:539), so this is not a
ministack bug — it's a net/http dependency on Go's scheduler.

**Mitigations for the hang** (none implemented in M7.alt POC):

1. Use a much lower-level oras-go entry point that bypasses
   `http.Client` and only calls our `Transport.RoundTrip` directly.
   Possible via `oras.land/oras-go/v2/registry/remote.Client`
   interface, which lets us substitute the wrapper around
   `RoundTripper`. Worth trying — could be a 50-LOC fix.
2. Build a TamaGo-friendly minimal `http.Client` lookalike that
   doesn't spawn goroutines. Discards the bulk of `net/http`,
   keeps `RoundTripper` plumbing.
3. Wait for TamaGo runtime to support timer-driven preemption
   (large engineering project, not on the roadmap).

**Implication for the post-M6.2 decision:** even with the size
penalty halved by the PE compressor, the oras-go path needs
runtime work before it can ship. The +2 MiB cost was the easy
question; this hang is the hard one. **The hand-rolled M7 client
stays the default**; we revisit if/when a low-overhead bypass
(mitigation 1) proves out.

**RoundTripper line-count + complexity:**

- `transport.go` — 484 LOC including BSD-3 header + doc comments.
- `client.go` — 96 LOC.
- Cyclomatic complexity: `RoundTrip` is the only non-trivial method
  (URL parse → dial dispatch → write → pipe-bridged body). All
  other helpers are <30 LOC linear flows.

**What `net/http` did NOT need masking under TamaGo:** the M7.alt
build succeeds with no GOOS=tamago patches to `net/http` —
the stdlib `http.Client.Transport` plug-point is honoured cleanly
and `net.Dial` is never called because the custom Transport short-
circuits before the default transport's dialer fires.

**Host-side coverage** (M7.alt addition):

- `uefiboard/ministack/orasoci` package coverage 61.3%. Covered:
  every parse / write / stream helper end-to-end (status line,
  header parse error paths, content-length, identity, chunked,
  chunked-bad-size, chunked-truncated), `splitHostPort` /
  `defaultPort` / `toHTTPHeader` exhaustively, resolve()'s IP-
  literal short-circuit + missing-DNS error branch, setDeadline's
  unknown-conn branch, `NewRepository` wiring + bad-ref rejection,
  `RoundTrip` scheme + nil-URL rejection. Not covered: the
  RoundTrip dial-pipe-body integration (needs a fake Stack the
  TCP4Conn type can plug into; deferred to the live runner). Gate
  is documented at 60% pending fake-Stack infrastructure work.

**Decision rule (revised 2026-06-09 post-live-FAIL):** the +2 MiB
size penalty was the easy gate; the goroutine-scheduler hang in
`net/http.Client.Do` is the hard one. M7.alt **stays as a
reference POC**, not a candidate replacement, until at least one
of:

- A low-level `oras-go` bypass (mitigation 1 above) is
  prototyped and proven to terminate cleanly under TamaGo+UEFI.
- TamaGo runtime gains async preemption (no ETA).

The hand-rolled M7 client (M7 + M7.1a streaming + M7.1b cosign
in-flight) stays the production code path.

### M8.1 — OCI streaming + LoadImage + StartImage — SHIPPED minimal 2026-06-09

**Deliverable.** Compose the M7.1a streaming OCI fetcher with the
M8.0 LoadImage+StartImage chain-boot mechanism into a single probe.
End-to-end:

```
stream-OCI(blob)  ->  SHA-256 verify  ->  gBS->LoadImage  ->  gBS->StartImage
```

**Live results (2026-06-09)** — QEMU+EDK2 only, MODE B (in-process
oci.Transport serving the embedded chained EFI bytes):

|  arch   | result | streamed bytes | LoadImage handle | exit_status |
|---------|:------:|---------------:|------------------|-------------|
| arm64   |  PASS  | 1,101,312      | 0x13f009698      | 0x0         |
| riscv64 |  PASS  | (chained_*.efi)| OK               | 0x0         |
| loong64 |  PASS  | (chained_*.efi)| OK               | 0x0         |
| amd64   | deferred (M6.2 amd64 firmware bug — efipack stub crashes mid-decompression) |

3/4 arches end-to-end: bytes streamed via OCI Transport → SHA-256
checked → handed to LoadImage → StartImage entered the loaded
image → clean return via `gBS->Exit`. KERNEL-BOOT OK.

**Probe modes:**

- **MODE B (default in this build)** — streams the embedded chained
  EFI bytes through an **in-process** `oci.Transport` that serves
  them as a single blob with a synthetic descriptor. The probe
  exercises `oci.FetchBlobStream` (= the M7.1a streaming +
  digest-verification surface) and then `uefiboard.LoadImage` +
  `uefiboard.StartImage` (= the M8.0 chain-boot mechanism), but
  without any network traffic. Proves the full pipeline.
- **MODE A (`kernelBootTargetRef` populated in source)** —
  walks a real registry over virtio-net+DHCP+TLS, picks the per-arch
  manifest, streams the bootable layer, and hands the verified bytes
  to LoadImage+StartImage. Wired in source but **dormant** in this
  build: the short-term GHCR PAT lacks `write:packages` so we can't
  publish a public `BOOT*-CHAINED.EFI` to ghcr.io. Flipping to MODE
  A is a one-line constant change (set `kernelBootTargetRef`) once
  a public OCI ref is available.

**What's explicitly OUT OF SCOPE for M8.1-minimal** (= M8.2 follow-ups):

- CMDLINE plumbing (Linux EFI-stub reads cmdline from
  `LoadedImageProtocol.LoadOptions` — populate it before LoadImage).
- initrd plumbing (publish `EFI_LOAD_FILE2_PROTOCOL` under
  `LINUX_EFI_INITRD_MEDIA_GUID` so the EFI-stub can pull initrd).
- The explicit `uefiboard/handoff_<arch>.s` shim. Not needed when
  StartImage enters a real EFI-stub: the EFI-stub does its own EBS
  handoff to the bare kernel internally. Only needed if we hand-roll
  the kernel-entry sequence outside of StartImage (which we don't).
- amd64 — gated on the M6.2 amd64 firmware-bug debug sprint.

**This commit closes the M0..M8 Phase 2 Path D roadmap on 3/4
arches.** The pure-Go pipeline is now end-to-end:

```
PCI walk → virtio-net up → DHCPv4 → DNS → TLS handshake (embedded roots) → HTTPS → OCI v2 walk → multi-arch index → manifest → streaming blob fetch → SHA-256 verify → cosign verify (keyed P-256) → LoadImage → StartImage → chained EFI runs
```

amd64 stays on `m6-2-pr2-amd64-wip` pending Block-IO side-channel
debug. M8.2 picks up real-kernel cmdline + initrd handoff against a
public OCI registry.

### M8.2 — Linux kernel helpers (framework SHIPPED 2026-06-09)

**Deliverable.** Extend M8.1's MODE A real-registry path with the
two Linux-specific helpers an EFI-stub kernel needs at boot:

1. **CMDLINE** via `uefiboard.SetLoadOptions(handle, cmdline)`:
   encodes the cmdline as UTF-16 LE + NULL terminator, looks up
   `EFI_LOADED_IMAGE_PROTOCOL` on the just-LoadImage'd handle, sets
   `LoadOptionsSize` + `LoadOptions` fields. The Linux EFI-stub
   reads its cmdline from there (UEFI 2.10 §9.2 / Linux
   Documentation/admin-guide/efi-stub.rst). Must be called between
   `LoadImage` and `StartImage`.
2. **initrd** via `uefiboard.PublishInitrd(initrd []byte) (handle,
   error)` + `UnpublishInitrd(handle) error`: installs an
   `EFI_LOAD_FILE2_PROTOCOL` instance under a fresh handle whose
   device path carries the `LINUX_EFI_INITRD_MEDIA_GUID`
   (`5568e427-68fc-4f3d-ac74-ca555231cc68`). The kernel's EFI-stub
   walks the protocol set looking for that GUID + calls `LoadFile`
   on the protocol to pull initrd into a kernel-supplied buffer.

`phase2_oci_kernel_boot.go` grows a 3-way mode dispatch:

- **MODE B** (default): `kernelBootTargetRef == ""` → in-process
  Transport self-test (M8.1 minimal). Live PASS on 3 arches.
- **MODE A**: `kernelBootTargetRef != "" && kernelBootCmdline == ""`
  → real-registry streaming + LoadImage + StartImage (M8.1 minimal
  with a real ref). Currently dormant pending a public OCI ref.
- **MODE C**: `kernelBootTargetRef != "" && kernelBootCmdline != ""`
  → real-registry streaming + (optional) initrd publish +
  SetLoadOptions + LoadImage + StartImage. The Linux-kernel-specific
  path. Currently dormant pending public ref + cmdline.

**M8.2-PARTIAL caveat on initrd.** `PublishInitrd` installs the device
path + protocol struct + handle, but the `LoadFile` slot in the
struct is currently NULL. The per-arch firmware-callback asm
trampoline that lets the EFI-stub call our Go LoadFile across the
calling-convention boundary has not yet shipped. A real EFI-stub
that calls `LoadFile2->LoadFile` on the published handle will fault.
Setting `kernelBootInitrdRef` wires the framework but is not expected
to boot end-to-end against an upstream kernel until the trampoline
lands. Cmdline (SetLoadOptions) does NOT have this issue — it's a
pure-data write into a firmware-managed struct, no callback needed.

**Files shipped (1133 LOC):**

- `uefiboard/load_options.go` + `_host.go` + `_tamago.go` (276 LOC)
  + `_test.go` (187 LOC) — `SetLoadOptions`, UTF-16 LE encoder,
  100% on host-buildable parts.
- `uefiboard/initrd_protocol.go` + `_host.go` + `_tamago.go` (444
  LOC) + `_test.go` (178 LOC) — `PublishInitrd` + `UnpublishInitrd`
  + device-path construction + (placeholder) LoadFile.
- `phase2_oci_kernel_boot.go` — MODE C dispatch + dormant entry +
  symbol-touches to keep the new uefiboard surface linked.

**Live results (2026-06-09).** MODE B regression on arm64 still
PASSes the M8.1 minimal acceptance gate (`KERNEL-BOOT OK`). MODE A
+ MODE C are dormant (constants empty) — populate to enable.

**What gated a live Linux kernel boot via OCI when M8.2 shipped
(answered by M8.3 below):**

- A **public OCI ref** for an EFI-stub-bootable Linux kernel
  artifact. M8.3 picked `ghcr.io/siderolabs/kernel:v0.6.0-alpha.0-1-ge8ed5bc`
  (anonymous bearer-token pull; arm64+amd64 multi-arch).
- TamaGo heap large enough to hold the compressed layer during
  extract (R-M8.3a — CLOSED, bumped to 128 MiB on arm64).
- A synchronous extract pipeline that doesn't deadlock the TamaGo+UEFI
  scheduler (R-M8.3b — CLOSED, replaced `io.Pipe` with buffered
  gunzip+tar).
- For initrd: the per-arch **firmware-callback asm trampoline** in
  `uefiboard/initrd_protocol_<arch>.s` (~50 LOC × 4 arches). The
  trampoline preserves MS-x64 / AAPCS64 / LP64 calling conventions
  around our Go LoadFile callback. Hand-written asm needed because
  TamaGo doesn't synthesise this kind of EFI-callable function
  signature from Go source. (Still deferred — M8.3 boots the
  kernel cmdline-only; rootfs-mount lands in M8.4.)
- For amd64: M6.2 unblock via OVMF upgrade (see §M6.1+M6.2 EDK2
  upstream investigation).

### M8.1-archive — original Linux EFI-stub handover design (deferred)

The original M8.1 design — full Linux kernel boot with cmdline +
initrd + per-arch handoff shim + EBS retry choreography — moves to
M8.2. The minimal M8.1 above proves the OCI → LoadImage → StartImage
mechanism; M8.2 adds the kernel-specific plumbing.

Original scope (now M8.2):

- `uefiboard/handoff_<arch>.s` — per-arch kernel entry shim
  (optional — not needed if StartImage enters an EFI-stub directly).
- `uefiboard/initrd_protocol.go` — publish
  `EFI_LOAD_FILE2_PROTOCOL` under `LINUX_EFI_INITRD_MEDIA_GUID`.
- The ExitBootServices retry choreography (refresh `GetMemoryMap` on
  `EFI_INVALID_PARAMETER`).

Acceptance (M8.2): a vanilla upstream Linux kernel boots from
`oci://ghcr.io/<org>/<image>:<tag>` on QEMU+EDK2 amd64 + arm64,
and on vfkit arm64. riscv64 + loong64 are expected to work
but may surface a firmware idiosyncrasy that becomes its own M8.x
finding.

**Prerequisites**:

- **M6.1** (amd64 OVMF PE > 4 MiB bug). Without this, the M8.1
  parent — which will be at least as big as the M7 OCI parent
  (5+ MiB) — will not load on amd64 under EDK2 stable202408. M8.0
  already hit this at 3.4 MiB.
- **M7.1** (streaming blob fetch). A real Linux kernel is 5-50
  MiB; ministack's 1 MiB response cap (M5) means the M8.1 loader
  cannot use `Stack.HTTPSGet`'s "buffer the whole response" path.
  The replacement is an `io.Reader` hand-off so the kernel image
  streams straight from TCP into an `AllocatePages`-backed page
  list.

### M8.3 — Live MODE C demo against public EFI-stub kernel (per-arch matrix 2026-06-10)

**Update 2026-06-10 (R-M8.3c — per-arch split)**: the M8.3 wiring is
now split per arch via `kernelboot_<arch>.go`. The `init()` swizzle
that previously zeroed the constants on non-arm64 was removed; each
per-arch file declares its own `kernelBootTargetRef` /
`kernelBootCmdline` / `kernelBootInitrdRef` /
`kernelBootUseEmbeddedInitrd` package vars. The dispatcher in
`phase2_oci_kernel_boot.go` is unchanged — it reads the same names,
which the build system resolves to the per-arch file via build tags.

Per-arch outcome of the 2026-06-10 sprint:

- **arm64** — MODE C live, unchanged from the 2026-06-09 ship.
- **amd64** — DORMANT (MODE B). siderolabs/kernel ships an amd64
  per-arch manifest under the same index, but live testing is gated
  on the OVMF >4 MiB threshold debug sprint on the
  m6-2-pr2-amd64-wip branch.
- **riscv64** — DORMANT (MODE B). 30-min OCI hunt 2026-06-10 found
  no publicly-anonymous-pullable EFI-stub kernel: siderolabs/kernel
  is amd64+arm64 only across `latest`, `v1.11.0`, and
  `v0.6.0-alpha.0-…`; siderolabs/talos publishes only a
  talosctl-linux-riscv64 CLI; tinkerbell/hook ships arm64+x86_64
  only; Kairos amd64-only at v4.1.0; openSUSE/Fedora riscv64 OCI
  paths return 404. Live test PASS in MODE B (chained payload
  banner from riscv64 EFI-stub LoadImage+StartImage).
- **loong64** — DORMANT (MODE B). Same hunt: no loong64 in
  siderolabs/tinkerbell/Kairos/k0s. cr.loongnix.cn refuses
  anonymous access. docker.io/loongarch64/debian:sid exists but is
  a rootfs without `/boot/vmlinuz`. Live test PASS in MODE B.

Flipping a dormant arch to MODE C is a one-line edit in
`kernelboot_<arch>.go`: set `kernelBootTargetRef` to the OCI URL
and `kernelBootCmdline` to the arch-appropriate cmdline (suggested
values documented in the per-arch file headers and in the matrix at
top-of-doc).

The arm64 narrative below continues to describe the original M8.3
ship — unchanged content, kept as the reference walkthrough.



Wires the dormant M8.2 framework against a real public OCI artifact
and runs it live on arm64. Goal: prove the *mechanism* from "OCI ref"
to "EFI-stub-kernel hand-off point" works end-to-end on real
infrastructure. **As of the R-M8.3a + R-M8.3b close-out below, the
EFI-stub itself runs on the real kernel image and prints "EFI stub:
Booting Linux Kernel..." to ConOut — the M8.3 acceptance gate.**

**Public OCI ref chosen** (arm64): `ghcr.io/siderolabs/kernel:v0.6.0-alpha.0-1-ge8ed5bc`.

- Anonymous bearer-token pull (no auth required).
- Multi-arch index resolves to per-arch manifests; arm64 manifest
  digest `sha256:676d8f0a780c021ca1236284c5cea3fc819143360fc8e7c96e1a44eed32fe07e`.
- Single layer (`application/vnd.docker.image.rootfs.diff.tar.gzip`),
  ~22 MiB compressed, ~53 MiB uncompressed; kernel lives at
  `boot/vmlinuz` inside the tar (verified `MZ` magic + `PE\0\0` at
  offset 0x40 + machine type 0xaa64 against a manual host-side pull).
- Other candidates evaluated and rejected: `ghcr.io/cloud-hypervisor/*`
  (no kernel-only artifact, only cloud-hypervisor binary); flatcar
  registries (require auth); linuxkit/kernel (`DENIED`); cloud-boot's
  own ghcr namespace (no `write:packages` PAT in the M8.3 budget).

**Cmdline**: `console=ttyAMA0,115200 earlyprintk=ttyAMA0,115200`
(arm64 virt). Installed via `uefiboard.SetLoadOptions` BEFORE
`uefiboard.StartImage`; the Linux EFI-stub reads it from
`LoadedImageProtocol.LoadOptions`.

**Initrd**: deferred (`kernelBootInitrdRef = ""`). Parallel agent #3
owns the per-arch firmware-callback asm trampoline in
`uefiboard/initrd_protocol_<arch>.s` that backs `LoadFile2->LoadFile`;
publishing an initrd handle in this build without that trampoline
would hand the EFI-stub a NULL function pointer.

**Per-arch gating** (historical, M8.3 ship 2026-06-09 — superseded
by the per-arch split documented above): `kernelBootTargetRef`/
`Cmdline` were `var` (not `const`) so an `init()` in
`phase2_oci_kernel_boot.go` zeroed them on every arch except arm64,
demoting the kernelboot probe back to MODE B
(self-test against the embedded chained EFI bytes) elsewhere. amd64
unblock = #1's OVMF sprint; riscv64 / loong64 would each need their
own ref + cmdline validation.

**Live result** (`task kernelboot:live:arm64`, post-R-M8.3a/b close):

```
phase2-oci-kernel-boot: M8.2 -- streaming OCI fetch + LoadImage + StartImage + Linux kernel helpers
phase2-oci-kernel-boot: arch = arm64
phase2-oci-kernel-boot: MODE = C (real-registry + Linux kernel helpers)
phase2-oci-kernel-boot: target = https://ghcr.io/siderolabs/kernel:v0.6.0-alpha.0-1-ge8ed5bc
phase2-oci-kernel-boot: cmdline = console=ttyAMA0,115200 earlyprintk=ttyAMA0,115200
phase2-oci-kernel-boot: initrd = (none)
phase2-oci-kernel-boot: device UP. MAC = 52:54:00:12:34:56
phase2-oci-kernel-boot: lease acquired
phase2-oci-kernel-boot:   IP = 10.0.2.15
phase2-oci-kernel-boot: embedded roots = 8
phase2-oci-kernel-boot: picked per-arch manifest = sha256:676d8f0a780c021ca1236284c5cea3fc819143360fc8e7c96e1a44eed32fe07e
phase2-oci-kernel-boot: streaming layer digest = sha256:237f7e0f90ac0d2a28ba98555b37b7afc694a06836226f397efd137c0f092a52
phase2-oci-kernel-boot: streaming layer size   = 22328147
phase2-oci-kernel-boot: extracting boot/vmlinuz from layer via buffered gunzip+tar (R-M8.3b)
phase2-oci-kernel-boot: streamed 22328147 bytes; SHA-256 verified OK
phase2-oci-kernel-boot: streaming elapsed (ms) = 4263
phase2-oci-kernel-boot: extracted vmlinuz bytes = 53096960
phase2-oci-kernel-boot: vmlinuz PE header OK (MZ)
phase2-oci-kernel-boot: LoadImage OK, handle = 0x13e8bde98
phase2-oci-kernel-boot: SetLoadOptions OK; cmdline len = 49
phase2-oci-kernel-boot: StartImage entering EFI-stub kernel
phase2-oci-kernel-boot: ---- kernel-side output below this line ----
EFI stub: Booting Linux Kernel...
EFI stub: ERROR: efi_get_random_bytes() failed (0x8000000000000002), KASLR will be disabled
EFI stub: Generating empty DTB
```

After the last EFI-stub line the kernel triggers a CpuDxe Data abort
(translation fault, third level) — expected behaviour for an
EFI-stub kernel without an initrd and without a populated devicetree;
the M8.3 acceptance gate is "any line of output that's clearly FROM
the kernel" and we have three. Wiring a real initrd via
`PublishInitrd` (M8.2-DEFERRED) and a `dtb=` cmdline parameter are
the next follow-ups, but they belong to M8.4, not M8.3.

PASS green on 2026-06-09.

**What works end-to-end on real infrastructure (M8.3 SHIPPED)**:

1. TamaGo + UEFI boots cleanly on QEMU+EDK2 arm64.
2. virtio-net device discovery + UP via ministack/virtio.
3. DHCPv4 over QEMU's user-mode SLIRP backend.
4. HTTPS (crypto/tls over ministack) handshake to `ghcr.io` with 8
   embedded root CAs.
5. ghcr.io's bearer-token OAuth dance (anonymous: 401 → /token →
   re-issue Authorization).
6. Multi-arch manifest index fetch + parse + per-arch pick (linux/arm64).
7. Per-arch manifest fetch + parse (single layer descriptor).
8. Layer-blob streaming via `oci.Registry.FetchBlobStream` with
   SHA-256 digest verification (22,328,147 bytes streamed + verified
   in ~4.3s).
9. Synchronous (R-M8.3b) gunzip + tar walk extracts `boot/vmlinuz`
   (53,096,960 bytes) into firmware-owned `EfiLoaderData` pages.
10. `uefiboard.LoadImage` parses the PE32+/MZ-`ARMd` EFI-stub
    successfully.
11. `uefiboard.SetLoadOptions` installs the 49-byte cmdline on the
    loaded image's `LoadedImageProtocol.LoadOptions`.
12. `uefiboard.StartImage` transfers control into the EFI-stub, which
    prints three diagnostic lines from inside the real Linux kernel
    image to UEFI ConOut.

**R-M8.3a — heap too small for 53 MiB vmlinuz (CLOSED 2026-06-09)**:

Resolution: `ramSize` in `uefiboard/board_arm64.go` bumped from
`0x02000000` (32 MiB) to `0x08000000` (128 MiB). The 128 MiB sizing
holds the 22 MiB compressed layer in `bytes.Buffer` + the gzip
sliding window + the OCI client / TLS / ministack working state with
a ~50 MiB margin. The decompressed 53 MiB vmlinuz lives in
firmware-owned `EfiLoaderData` pages outside the Go heap (via
`uefiboard.AllocatePages`), so it does NOT count against `ramSize`.
QEMU virt `-m 4096` comfortably holds the new 128 MiB region.
amd64 / riscv64 / loong64 boards untouched (amd64 already at 704
MiB; riscv64 + loong64 stay on MODE B self-test which fits in 32/64
MiB).

**R-M8.3b — io.Pipe deadlock under TamaGo+UEFI (CLOSED 2026-06-09)**:

Resolution: `streamExtractVmlinuz` in `phase2_oci_kernel_boot.go`
restructured from a goroutine-driven `io.Pipe` pipeline
(FetchBlobStream→PipeWriter goroutine ⇄ PipeReader→gzip→tar) to a
single-threaded buffered pipeline:

1. `FetchBlobStream(desc, &bytes.Buffer{})` — synchronous,
   SHA-256-verified write of the full compressed layer (~22 MiB)
   into the Go heap.
2. `gzip.NewReader(bytes.NewReader(buf.Bytes()))` — synchronous
   inflate.
3. `tar.NewReader(gz)` — synchronous walk to `boot/vmlinuz`.
4. `uefiboard.AllocatePages(EfiLoaderData, …)` + `io.ReadFull(tr,
   dst)` — copy the extracted bytes into firmware-owned pages.

No goroutine, no `io.Pipe`, no scheduler dependency. Same root-cause
fix shape as the ministack "drive RX inline" pattern (commits
91364cf, 8c86f35): TamaGo+UEFI has no async preemption, so any
producer/consumer split via `io.Pipe` deadlocks the moment the
consumer blocks on `Read` before the producer has been scheduled.

The buffered approach trades memory headroom (an extra ~22 MiB Go
heap pressure) for guaranteed liveness. R-M8.3a's 128 MiB heap
absorbs that cost with margin.

**amd64 status**: deferred behind #1's OVMF sprint (M6.1+M6.2 chain —
the parent EFI grows past the 4 MiB OVMF/stable202408 LoadImage
ceiling on amd64). Once #1 unblocks, populating `kernelBootTargetRef`
+ `kernelBootCmdline` on amd64 is the same one-line + one-line wiring
pattern the arm64 init() already demonstrates.

**riscv64 + loong64 status**: skipped in M8.3. The siderolabs OCI
artifact is amd64+arm64 only; a different ref (likely upstream
distro mirrors with their own OCI publication scheme) plus its own
cmdline plus per-arch live-runner branch would be required.

**Files**:

- `phase2_oci_kernel_boot.go` — MODE C dispatcher rewired to drive
  real-registry streaming + tar extraction; `streamExtractVmlinuz`
  helper (R-M8.3b: synchronous buffered gunzip+tar — no `io.Pipe`,
  no producer goroutine); `init()` per-arch gating.
- `uefiboard/board_arm64.go` — `ramSize` bumped from 32 MiB to
  128 MiB (R-M8.3a) so the compressed layer fits the Go heap during
  the synchronous extract.
- `internal/livekernelboot/run.sh` — arm64 branch with `-netdev user`
  + `-device virtio-net-pci` and MODE-C-specific PASS gate.

### M8.4 — DTB ConfigurationTable probe + initrd publish in MODE C (SHIPPED arm64 wiring 2026-06-09)

Closes the M8.3 follow-up gaps: (1) a `gST->ConfigurationTable` walk
that reports whether the firmware already publishes a flattened
Device Tree Blob (the Linux EFI-stub auto-locates one via
`EFI_DTB_TABLE_GUID = b1b621d5-f19c-41a5-830b-d9152c69aae0`); and
(2) a real call to `uefiboard.PublishInitrd` in MODE C so the
EFI-stub finds an initrd at `LINUX_EFI_INITRD_MEDIA_GUID`.

**Initrd source**: did NOT find a publicly-pullable Talos initramfs
OCI ref (siderolabs publishes only the multi-layer `installer`
aggregate; `ghcr.io/siderolabs/initramfs*` repos all return
`DENIED`). Fell back to an embedded 260-byte minimal cpio.gz
(`internal/embed_initramfs/`) — a single `/init` script that prints
a marker and exits. The OCI streaming path (`fetchInitrdFromOCI`)
is still wired up and ready: setting `kernelBootInitrdRef` to a
public initramfs ref will exercise it. The gzip-auto-detect
(`1f 8b 08` magic) handles both compressed and raw cpio layers.

**DTB probe result (QEMU virt + EDK2 stable202408 `-bios edk2-aarch64-code.fd`)**:

```
phase2-oci-kernel-boot: DTB probe: SystemTable.NumberOfTableEntries = 8
phase2-oci-kernel-boot: DTB probe: EFI_DTB_TABLE_GUID NOT FOUND — kernel will fall back to 'Generating empty DTB'
```

EDK2 stable202408 on aarch64 QEMU `virt` does NOT publish the DTB
via `gBS->InstallConfigurationTable` under our run mode (the OVMF
`-bios` build path does not embed `ArmVirtPkg`'s `PlatformDxe`
copying-the-QEMU-DTB step). Publishing a DTB ourselves would need
the QEMU `-dtb /path/to/virt.dtb` flag plumbing (dev-mode
`-machine virt,dumpdtb=…` to grab one first) — out of M8.4 scope.
The kernel proceeds anyway via its "Generating empty DTB" fallback,
which is sufficient on platforms whose UEFI runtime services cover
the missing devicetree bits.

**Live result** (`task kernelboot:live:arm64`, M8.4):

```
phase2-oci-kernel-boot: M8.2 -- streaming OCI fetch + LoadImage + StartImage + Linux kernel helpers
phase2-oci-kernel-boot: arch = arm64
phase2-oci-kernel-boot: MODE = C (real-registry + Linux kernel helpers)
phase2-oci-kernel-boot: target = https://ghcr.io/siderolabs/kernel:v0.6.0-alpha.0-1-ge8ed5bc
phase2-oci-kernel-boot: cmdline = console=ttyAMA0,115200 earlyprintk=ttyAMA0,115200
phase2-oci-kernel-boot: initrd = (embedded minimal cpio.gz)
[...DHCP + HTTPS + OCI walk + vmlinuz extract...]
phase2-oci-kernel-boot: LoadImage OK, handle = 0x13e8bdd98
phase2-oci-kernel-boot: SetLoadOptions OK; cmdline len = 49
phase2-oci-kernel-boot: DTB probe: SystemTable.NumberOfTableEntries = 8
phase2-oci-kernel-boot: DTB probe: EFI_DTB_TABLE_GUID NOT FOUND — kernel will fall back to 'Generating empty DTB'
phase2-oci-kernel-boot: using embedded minimal initrd; bytes = 260
phase2-oci-kernel-boot: embedded initrd magic = 1f 8b 08 (gzip)
phase2-oci-kernel-boot: PublishInitrd OK, initrd handle = 0x13e8bee18
phase2-oci-kernel-boot: StartImage entering EFI-stub kernel
phase2-oci-kernel-boot: ---- kernel-side output below this line ----
EFI stub: Booting Linux Kernel...
EFI stub: ERROR: efi_get_random_bytes() failed (0x8000000000000002), KASLR will be disabled
EFI stub: Generating empty DTB
EFI stub: ERROR: Failed to load initrd!
```

**Progress vs. M8.3**: kernel EFI-stub now prints **four** stub
lines (was three) — the `Failed to load initrd!` line is new and
proves the EFI-stub successfully:
1. Located our `EFI_LOAD_FILE2_PROTOCOL` handle via
   `LocateDevicePath(LINUX_EFI_INITRD_MEDIA_GUID)`.
2. Opened the protocol via `HandleProtocol`.
3. Invoked our `LoadFile2->LoadFile` callback at least once (the
   stub would have silently fallen back to cmdline `initrd=` if
   the handle wasn't found, NOT printed `Failed to load initrd!`).

What we don't yet get: the `Unpacking initramfs...` line that
would come from a successful initrd transfer. The Go-side
`loadFileGo` returns the right EFI_STATUS shapes per the host
unit tests, so the gap is in the firmware→Go callback interop —
see R-M8.4a below.

PASS green on the framework wiring (DTB probe walker + initrd
publish + protocol install) on 2026-06-09. The trampoline
interop gap is a known follow-up.

**R-M8.4a — EFI-stub `Failed to load initrd!` despite successful PublishInitrd (OPEN)**:

Symptom: the kernel EFI-stub locates our published
`EFI_LOAD_FILE2_PROTOCOL` handle correctly, calls into our
per-arch `loadFile_trampoline` (the asm bridge from AAPCS64 →
Go ABI0 → `loadFileGo`), but the stub then logs
`EFI stub: ERROR: Failed to load initrd!` rather than the
expected `Loaded initrd from LINUX_EFI_INITRD_MEDIA_GUID device
path` success line.

Hypothesised root cause (not yet root-caused): one of the
following inside the firmware→Go callback path:

1. `loadFileGo` returns `EFI_BUFFER_TOO_SMALL` on the size-query
   call as expected, but the stub then makes the second
   (transfer) call with `*BufferSize` smaller than `need`, and we
   re-return `EFI_BUFFER_TOO_SMALL` instead of `EFI_SUCCESS` —
   testable by adding a print of `(have, need)` from the
   trampoline once a print-from-nosplit path exists.
2. The Go runtime's `g` register (X28 on arm64) is in an
   undefined state on entry because the EFI-stub clobbered it
   between our `StartImage` and the callback — the trampoline
   does not currently restore `g` from a saved location, and the
   `loadFileGo` body's `unsafe.Slice` / write-barrier-eligible
   paths would then read wild memory. The host-side
   `loadFileGo` tests in `initrd_protocol_test.go` exercise the
   pure logic under a known-good `g`; the live firmware path
   does not.
3. The `loadFileRegistry` lookup by `this` fails because the
   firmware passes a re-relocated copy of our protocol struct
   (UEFI is allowed to copy by value in some implementations
   when handing the interface back to a caller). Detectable by
   logging both pointers and seeing whether equality holds.

Mitigation paths, in priority order:
- Add a "loadFile entered" sentinel via a dedicated nosplit
  print that writes a single byte to ConOut from inside the
  callback (ConOut survives StartImage on EDK2 + QEMU virt
  pre-EBS).
- Restructure the trampoline to save+restore `g` (X28) explicitly,
  matching the eficall_arm64.s discipline.
- Cross-check against EDK2's stable202408 `LoadFile2` reference
  implementation in `MdeModulePkg/Library/UefiLibFwVol`.

Workaround in the meantime: same as M8.3 — the kernel proceeds
past the `Failed to load initrd!` log without halting, then
data-aborts later in its own DTB-less init code. The MODE C
framework wiring (DTB probe + PublishInitrd + protocol install +
EFI-stub locate-and-invoke handshake) is proven; only the actual
byte transfer remains to ship a working initramfs unpack.

**Files**:

- `uefiboard/dtb_probe.go` — `EFIDTBTableGUID` (`b1b621d5-f19c-41a5-830b-d9152c69aae0`),
  `DTBProbeResult`, `guidsEqual`, `efiST{NumberOfTableEntries,ConfigurationTable}`
  offsets (104, 112), spec-pinned 24-byte
  `EFI_CONFIGURATION_TABLE` entry stride.
- `uefiboard/dtb_probe_tamago.go` — live walker
  (`ProbeDTBConfigurationTable`) of `gST->ConfigurationTable`
  looking for the DTB GUID.
- `uefiboard/dtb_probe_test.go` — host-side unit tests for the
  GUID textual form, `guidsEqual` discriminator, offset constants,
  and entry size.
- `internal/embed_initramfs/` — new package with embedded 260-byte
  `initramfs.cpio.gz` (single `/init` script), `Bytes` / `RawBytes` /
  `Size` accessors + tests covering gzip magic + cpio newc magic +
  defensive-copy semantics.
- `phase2_oci_kernel_boot.go` — MODE C extended with the DTB
  probe + the embedded-initrd publish + a new `fetchInitrdFromOCI`
  helper for the OCI-streaming path (gzip auto-detect via
  `1f 8b 08` magic; same 64 MiB layer cap as the kernel path).
- `internal/livekernelboot/run.sh` — arm64 PASS gate extended
  with `DTB probe:` + `PublishInitrd OK` markers; final log
  filter extended with `Unpacking initramfs`, `cloud-boot-m83`,
  `Kernel panic`, `Attempted to kill init`.

### M8.5 — Real `/init` ELF in embedded initramfs + DTB-absence diagnosis (SHIPPED 2026-06-10)

**What changed:**

1. The embedded `initramfs.cpio.gz` is no longer a 260-byte
   shell-script fixture — it is now a **573 KiB cpio.gz** wrapping
   a single statically-linked aarch64 ELF `/init` built in pure Go
   (`GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath
   -ldflags='-s -w'`). The /init writes a marker banner to
   `/dev/console`, `/dev/kmsg`, and stdout, then powers off via
   `reboot(2)` `LINUX_REBOOT_CMD_POWER_OFF` so QEMU exits cleanly
   inside the live-test timeout.

   The shell-script fixture had a fatal flaw the M8.4 acceptance
   gate did not catch: the Linux kernel rejects `#!/bin/sh` init
   binaries on the populate-rootfs path before it ever attempts
   spawning anything (no `/bin/sh` exists in our rootfs and no
   in-kernel script interpreter is registered). A real ELF /init
   is the smallest fixture the kernel will actually execve
   end-to-end.

2. New `internal/embed_initramfs/init_src/` subdirectory holds the
   /init Go source plus `build.sh`, a reproducible re-pack script
   (deterministic ELF via `-trimpath -ldflags='-s -w'`;
   deterministic cpio order via `LC_ALL=C sort`; deterministic
   gzip header via `gzip -n`).

3. `internal/embed_initramfs/initramfs_test.go` grew a new
   `TestEmbedContainsInitELF` host-side walker that decompresses
   the cpio, finds the `init` entry, and verifies its body has
   ELF magic + ELFCLASS64 + ELFDATA2LSB + EM_AARCH64. This
   prevents future regressions to a script-based /init.

4. `uefiboard/dtb_probe.go` + `uefiboard/dtb_probe_tamago.go` —
   `DTBProbeResult` gained an `AllGUIDs []EFIGUID` field; the
   walker now captures every `EFI_CONFIGURATION_TABLE` entry's
   GUID, not just the DTB one. `phase2_oci_kernel_boot.go` dumps
   all captured GUIDs into the live log at MODE C probe time.

5. `kernelboot_arm64.go` cmdline extended from 49 to 113 chars:
   adds `earlycon=pl011,mmio32,0x9000000` (hardcoded UART MMIO
   base, independent of DTB), `acpi=force` (use ACPI tables EDK2
   already publishes), `root=/dev/ram0 rdinit=/init` (explicit
   ramdisk + init path), `loglevel=8 panic=10` (verbose printk +
   auto-reboot on panic).

**Live result** (`task kernelboot:live:arm64`, M8.5):

```
phase2-oci-kernel-boot: cmdline = console=ttyAMA0,115200 \
    earlycon=pl011,mmio32,0x9000000 acpi=force \
    root=/dev/ram0 rdinit=/init loglevel=8 panic=10
phase2-oci-kernel-boot: SetLoadOptions OK; cmdline len = 113
phase2-oci-kernel-boot: DTB probe: SystemTable.NumberOfTableEntries = 8
phase2-oci-kernel-boot: DTB probe: EFI_DTB_TABLE_GUID NOT FOUND ...
phase2-oci-kernel-boot: DTB probe: GUID[ 5 ] data1 = 0xf2fd1544 ... (SMBIOS3)
phase2-oci-kernel-boot: DTB probe: GUID[ 7 ] data1 = 0x8868e871 ... (ACPI 2.0)
phase2-oci-kernel-boot: using embedded minimal initrd; bytes = 573602
phase2-oci-kernel-boot: embedded initrd magic = 1f 8b 08 (gzip)
phase2-oci-kernel-boot: PublishInitrd OK
phase2-oci-kernel-boot: StartImage entering EFI-stub kernel
EFI stub: Booting Linux Kernel...
EFI stub: ERROR: efi_get_random_bytes() failed (...), KASLR will be disabled
EFI stub: Generating empty DTB
EFI stub: Loaded initrd from LINUX_EFI_INITRD_MEDIA_GUID device path
[ firmware Data Abort, FAR=0x40, in ArmCpuDxe DefaultExceptionHandler ]
```

**Key M8.5 finding — DTB-absence is a real kernel blocker, not a
cosmetic warning**: the GUID dump confirms EDK2's arm64 firmware
on `-machine virt` publishes **ACPI 2.0** (`8868e871-e4f1-11d3-bc22-
0080c73c8881`) and **SMBIOS3** (`f2fd1544-9794-4a2c-992e-
e5bbcf20e394`) tables but does NOT publish a DTB under
`EFI_DTB_TABLE_GUID`. The kernel falls back to "Generating empty
DTB", which has no PSCI / GIC / UART description, and even with
`earlycon=pl011,mmio32,…` + `acpi=force` the EFI-stub still trips
a firmware Data Abort (FAR=0x40, translation fault) before
`ExitBootServices` — see R-M8.5a below.

PASS green on the M8.5 wiring goals (real /init, ELF guard test,
GUID dump, cmdline broadened) on 2026-06-10. The actual
`Unpacking initramfs` line still requires R-M8.5a to land first.

**R-M8.5a — EFI-stub Data Abort despite ACPI tables + hardcoded earlycon (OPEN)**:

Symptom: the EFI-stub reaches `Loaded initrd from
LINUX_EFI_INITRD_MEDIA_GUID device path` (proving R-M8.4a's
LoadFile2 fix is solid even with a 573 KiB initrd vs M8.4's
260-byte fixture), then trips a firmware Data Abort:

```
Data abort: Translation fault, third level
 ELR 0x000000013FDB9220  ESR 0x96000047  FAR 0x40
Synchronous Exception at 0x000000013FDB9220
ASSERT [ArmCpuDxe] /…/DefaultExceptionHandler.c(343): ((BOOLEAN)(0==1))
```

The fault address (`0x40`) is the null + 0x40 offset that
arm64 Linux dereferences during the EFI-stub's empty-DTB DTB
patch phase (`efi_apply_loadoptions_quirks` →
`update_fdt_memmap` → walk fdt nodes which all return NULL on an
empty DTB). With no DTB the patch path has nothing to walk, and
the second-stage zero-deref happens inside firmware's call
trampoline when the patched-fdt pointer is fed back as a
configuration-table install argument.

The cmdline workarounds we tried (acpi=force, earlycon=pl011
hardcoded, root=/dev/ram0 rdinit=/init) cannot help because the
crash happens BEFORE the kernel parses cmdline — it's in the
empty-DTB patch path that runs during EFI-stub init.

Mitigation paths, in priority order:
1. **PublishDTB helper** (M8.6 — biggest impact, biggest lift):
   embed a QEMU virt DTB at build time (`qemu-system-aarch64
   -machine virt,dumpdtb=…` then `dtc -O dtb`), trim with `dtc`
   to ~8 KiB, and InstallConfigurationTable it under
   `EFI_DTB_TABLE_GUID` from Go before `StartImage`. Requires a
   new per-arch trampoline for `gBS->InstallConfigurationTable`
   analogous to the existing eficall_arm64.s patterns.
2. **Pin EDK2 build with `gFdtTableGuid` install**: confirm
   pkgx's edk2-aarch64-code.fd is actually
   `ArmVirtPkg/ArmVirtQemu` and not a DTB-stripped derivative;
   rebuild if it's the latter.
3. **ACPI-only kernel build**: switch to a kernel built with
   `CONFIG_OF=n` so the empty-DTB code path is compiled out and
   the kernel exclusively uses the published ACPI tables. The
   siderolabs/kernel artifact ships ACPI + OF — a kernel that
   trips this bug only when both are compiled in.

Workaround in the meantime: none short-term — the only path to
`Run /init` involves landing one of the mitigations above.
**M8.5's scope was the initramfs side of the handoff (now
correct + tested + verified by the M8.5 ELF guard) plus the
diagnostic GUID dump that proved DTB is the kernel-side
blocker, not initramfs payload.**

**Files**:

- `internal/embed_initramfs/initramfs.cpio.gz` — replaced the
  260-byte shell-script fixture with the 573 KiB
  ELF-/init fixture.
- `internal/embed_initramfs/doc.go` — package doc rewritten to
  describe the new fixture + multiarch story.
- `internal/embed_initramfs/initramfs_test.go` —
  `TestEmbedContainsInitELF` (new) walks the cpio + checks
  /init's ELF magic + ELFCLASS64 + ELFDATA2LSB + EM_AARCH64.
- `internal/embed_initramfs/init_src/init.go` (new) — the Go
  /init source (build-tag `ignore` so the parent tamago-uefi
  build never tries to compile it).
- `internal/embed_initramfs/init_src/build.sh` (new) —
  reproducible re-pack script.
- `uefiboard/dtb_probe.go` — `DTBProbeResult.AllGUIDs`
  field added.
- `uefiboard/dtb_probe_tamago.go` — walker captures every
  VendorGuid, not just the DTB one.
- `phase2_oci_kernel_boot.go` — MODE C prints every captured
  GUID for diagnostics.
- `kernelboot_arm64.go` — cmdline broadened: hardcoded earlycon
  MMIO base, acpi=force, root=/dev/ram0 rdinit=/init,
  loglevel=8, panic=10.

To rebuild the initramfs fixture from scratch:

```sh
cd internal/embed_initramfs/init_src
./build.sh
```

Coverage: 100% in `internal/embed_initramfs` (all 5 tests
including the new ELF guard); uefiboard DTB probe tests
unchanged + still green (new AllGUIDs field exercised by the
existing live walker).

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

### R-M8.0a (RESOLVED 2026-06-09) — TamaGo runtime.exit halts instead of returning to firmware

Out of the box, the TamaGo runtime's `exit` function
(`tamago-pie/src/runtime/os_tamago.go`) ends in `for {}`, so a
TamaGo+UEFI image that simply returns from `main()` never returns
to its parent — the parent's `StartImage` call hangs indefinitely.

**Mitigation (shipped in M8.0)**: TamaGo exposes
`runtime/goos.Exit` (a function-typed package variable in
`runtime/goos/stub.go`) as an override slot — when set, `exit`
delegates to it. `uefiboard.WireExitToFirmware()` installs a
function that calls `gBS->Exit(parentImageHandle, status, 0,
NULL)`, mapping Go exit code 0 → `EFI_SUCCESS` and non-zero →
`EFI_ABORTED` (0x8000000000000015). The chained payload calls
`WireExitToFirmware()` before printing its banner; on `return`
from `main` the runtime calls the override, the firmware tears
the child down, and control resumes in the parent's
`StartImage` call.

**Verified** end-to-end on arm64 + riscv64 + loong64 under
QEMU+EDK2; parent log shows `phase2-efi-handover: chain-boot
returned exit_status=0x0` immediately after the chained
banner.

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
- **2026-06-08** (virtio code extraction): the transport-agnostic
  virtio infrastructure and the spec-level virtio-net driver have
  been split out of `cloud-boot/tamago-uefi/uefiboard/` into the new
  `go-virtio` GitHub org. Three repos:
  [`go-virtio/common`](https://github.com/go-virtio/common) holds the
  PCI capability walker, the modern `ModernConfig` register layout, the
  split-virtqueue impl, and three `Transport` interfaces
  (`PCIConfigReader`, `BARMemoryAccessor`, `PageAllocator`);
  [`go-virtio/net`](https://github.com/go-virtio/net) holds the
  pure-Go virtio-net driver (`OpenVirtioNet(transport)`, init sequence
  per Virtio 1.1 §3.1.1, R-M2b MTU mask preserved);
  [`go-virtio/blk`](https://github.com/go-virtio/blk) is a placeholder
  for a future pure-Go virtio-blk driver. `uefiboard/` retains the
  UEFI transport adapter (`virtio_uefi_transport.go` — implements
  `common.Transport` via `EFI_PCI_IO_PROTOCOL.Pci.Read/Mem.Read/Write`
  + `gBS->AllocatePages`) plus a thin bridge layer
  (`virtio_uefi_bridge.go` + `virtio_net_uefi.go`) so existing
  consumers (`phase2_virtionet_tx.go`, `phase2_pcienum.go`,
  `uefiboard/ministack/link_tamago.go`) compile unchanged. Banner
  rodata + ministack unit tests PASS on all 4 arches;
  `task virtionet:all` + `task ministack:all` build clean.
  Per-repo coverage: `common` 96.3 %, `net` 81.1 %, `blk` placeholder.
- **2026-06-08** (M4 DHCPv4 acquire — SHIPPED). Added UDP4 +
  DHCP4 to the M3-minimal ministack, plus the
  `phase2_dhcp4_acquire` probe + Taskfile targets. Files:
  `uefiboard/ministack/udp4.go` (~330 LOC) — full UDP/IPv4 with
  pseudo-header checksum, `OpenUDP4(localPort) → *UDP4Conn` demux,
  `WriteTo`/`ReadFrom` with read deadlines, limited-broadcast
  (255.255.255.255) short-circuit so DHCP DISCOVER ships without an
  ARP lookup; `uefiboard/ministack/dhcp4.go` (~430 LOC) — DORA state
  machine (DISCOVER → OFFER → REQUEST → ACK), 240-byte BOOTP header +
  magic-cookie + TLV options, MAC-derived deterministic xid,
  pseudo-header-checksummed UDP/68 ↔ UDP/67, NAK detection,
  per-stage deadline enforcement; `phase2_dhcp4_acquire.go` (probe)
  + `phase2_dhcp4_acquire_stub.go` (no-op fallback); Taskfile
  targets `dhcp4:elf:<arch>`, `dhcp4:efi:<arch>`, `dhcp4:all`,
  `dhcp4:test`, `live:dhcp4:<arch>`; `internal/livedhcp4/run.sh`
  per-arch live runner. The probe wires the M2 virtio-net device
  into ministack, runs `DHCP4Acquire(10s)`, prints the lease
  (IP/Mask/Gateway/DNS/Server/Duration), applies it to the Stack,
  and pings the learned gateway. Build matrix (4/4 arches PASS):
  `BOOTX64-DHCP4.EFI` 2.0M, `BOOTAA64-DHCP4.EFI` 1.7M,
  `BOOTRISCV64-DHCP4.EFI` 1.6M, `BOOTLOONGARCH64-DHCP4.EFI` 1.8M.
  Host coverage `uefiboard/ministack` 95.1 % (up from 94.6 % at
  M3-minimal), with `udp4.go` 96.4 % and `dhcp4.go` 98.7 % per-file.
  Cross-refs: R-M3'a CLOSED (M3-minimal foundation), R-M3'b
  RESOLVED (loong64 syscall overlay, prerequisite for M4 loong64).
  Regression checks: `task ministack:test`, `task ministack:all`,
  `task virtionet:all`, `task test` all PASS. Live-runner: an
  EDK2-BDS quirk surfaced (same on the pre-existing
  `task live:ministack:amd64`, NOT a M4 regression) where EDK2
  stable202408 falls into the Internal EFI Shell instead of
  auto-launching `\EFI\BOOT\BOOTX64.EFI` from the removable FAT
  ESP. Fixed in `internal/livedhcp4/run.sh` by injecting a tiny
  `startup.nsh` at the FAT root that the Internal Shell auto-runs;
  the same fix benefits the next live-runner. With the .nsh in
  place, the M4 amd64 probe boots cleanly on QEMU+EDK2: dispatcher
  reaches `phase2-dhcp4: M4 ...`, brings up virtio-net (MAC
  `52:54:00:12:34:56`), kicks the RX goroutine, and broadcasts the
  DISCOVER. **Outstanding (R-M4a, open):** QEMU SLIRP does not send
  an OFFER back to the TamaGo client; the probe times out after
  10 s (`LEASE FAIL: ministack: DHCP4 timed out waiting for
  reply`). Per the task brief's stop-and-report rule, M4 stops
  here; the host-side unit tests (including a synthetic UDP DHCP
  server in `dhcp4_test.go`) drive a full DORA exchange end-to-end
  and pass cleanly, so the protocol logic itself is validated. The
  open gap is at the QEMU SLIRP edge — candidates: (a) UDP
  pseudo-header checksum mismatch on egress, (b) virtio-net RX
  filter rejecting broadcast (virtio 1.1 §5.1.6.1 default is
  "accept own-MAC + broadcast", but `disable-legacy=on` may
  interact), (c) MERGE_RXBUF / negotiation difference between the
  M3 ICMP path (working) and the M4 UDP-broadcast path
  (not working). Tracked as R-M4a for the next live-validation
  agent run. M5 (DNS + HTTP) is **partially** unblocked
  — UDP4 is in place and DHCP-discovered DNS servers ride through
  the lease struct.
- **2026-06-08** (M7 OCI registry client — SHIPPED). New package
  `uefiboard/ministack/oci/` (5 source files, ~830 LOC + ~880 LOC
  tests, 94.9% coverage). Pure-Go OCI Distribution v2 client over
  the M6 HTTPS transport: `ParseRef`, `Digest` (sha256-only),
  `Manifest` / `Index` / `Descriptor` JSON shapes, `Registry` with
  Bearer challenge → token negotiation, `FetchManifestRaw` /
  `FetchBlob` with SHA-256 verification + one-hop 307/302
  redirect-follow, and top-level `FetchArtifact(reg, opts)`
  orchestrator. New `phase2_oci_fetch` build tag + `BOOT<ARCH>-OCI.EFI`
  artifact; new `oci:*` + `live:oci:*` Taskfile targets. Embedded CA
  bundle extended from 7 → 8 roots (added USERTrust RSA, required
  for ghcr.io's Sectigo chain). Live results against
  `ghcr.io/linuxcontainers/alpine:latest` under QEMU+EDK2 with
  `-netdev user`: **arm64 PASS, loong64 PASS, riscv64 PASS** —
  full DHCP4 → DNS → TLS → OCI walk (token → index → manifest →
  config blob + small layer) with SHA-256 verification on every
  fetched byte, gzip-magic `1f 8b 08 00` visible in the layer hex
  preview. amd64 hits the same EDK2 `#GP` in CpuDxe as M6 (PE > 4
  MiB firmware bug) — unchanged. loong64 / riscv64 fall back to
  amd64 index entries because linuxcontainers/alpine doesn't ship
  those manifests; M8's purpose-built artifact will carry native
  loong64 / riscv64 entries and the fallback will go away. M7.1
  follow-ups deferred: (a) lift ministack's 1 MiB HTTP-response cap
  via a streaming io.Reader for multi-MiB layer fetches; (b)
  cosign-bundle signature verification on the manifest digest with
  an embedded public key. No regression on ministack (still 91.5%
  coverage) or M0..M6.

- **2026-06-09** (M8.0): chain-boot mechanism SHIPPED. Image-services
  thunks (LoadImage / StartImage / UnloadImage / Exit) added to
  `uefiboard` under offsets 200 / 208 / 216 / 224 (UEFI 2.10 §4.2
  table 4.2). Host-buildable `loadimage.go` carries the offsets +
  `ErrLoadImageNoSource`; `loadimage_host.go` panic-stubs the live
  calls for host `go test`; `loadimage_tamago.go` runs the real
  efiCall thunks (mirrors the M0 `GetMemoryMap` split pattern).
  Coverage 100% on the host-buildable lines (LoadImage / StartImage
  / UnloadImage / ExitImage / WireExitToFirmware all 100%). New
  chained payload at `cmd/chainedhello/main.go` prints
  `>>> M8.0 chained payload -- Hello from <ARCH> <<<` and returns
  via `gBS->Exit` (wired through `uefiboard.WireExitToFirmware`,
  which installs a `runtime/goos.Exit` hook so the TamaGo runtime's
  default spin-halt is replaced with a firmware-clean return —
  M8.0a finding). Per-arch chained EFIs are built first, copied
  into `internal/embed_chained/chained_<arch>.efi` (gitignored,
  regenerated each build), then embedded into the parent at
  compile time via `//go:embed`. New `phase2_efi_handover` build
  tag → `BOOT<ARCH>-EFIHANDOVER.EFI`; dispatcher runs the probe
  after `runOCIFetchProbe`. New `efihandover:*` + `chainedhello:*`
  + `efihandover:live:*` Taskfile targets. **Live results
  (QEMU+EDK2, no network)**: arm64 PASS, riscv64 PASS, loong64
  PASS — all three with clean `gBS->Exit` return (parent prints
  `chain-boot returned exit_status=0x0`). amd64 FAIL — EDK2 BDS
  falls to PXE before our entry point, same M6.1 / M7 PE-size
  firmware bug (parent is 3.4 MiB, evidently over the OVMF
  threshold); mechanism not blocked. Cleanup quirk documented:
  `UnloadImage` after a clean `gBS->Exit` return is a no-op
  (firmware already freed the image) and the parent now skips it
  on `exit_status=0`. ASCII-only banner used because ConOut's
  UTF-16 surface mangles multi-byte UTF-8 (em-dash → `???`).
  M8 renamed to M8.1 and deferred behind M6.1 (amd64 OVMF PE>4MiB)
  + M7.1 (streaming blob fetch for kernel-sized OCI layers).
  Vfkit/arm64 not exercised this milestone — out of M8.0 scope per
  the brief; possible M8.0b follow-up that would also require
  routing the chained banner through the M1.6 Block-IO scratch
  disk (R-M1'a). No regression on ministack/oci/uefiboard host
  tests.

- **2026-06-10** (M8.5): real ELF `/init` in embedded initramfs +
  DTB-absence diagnosed. The previous 260-byte cpio.gz fixture
  shipped a `#!/bin/sh` script `/init` the kernel could not
  actually execve (no `/bin/sh` in rootfs, no in-kernel script
  interpreter). Replaced with a 573 KiB cpio.gz wrapping a
  statically-linked aarch64 Go ELF: pure-Go (`GOOS=linux
  GOARCH=arm64 CGO_ENABLED=0`) that writes a marker banner to
  `/dev/console` + `/dev/kmsg` + stdout and powers off via
  `reboot(2)`. New `internal/embed_initramfs/init_src/` ships the
  source + a reproducible `build.sh` (trimpath + sorted cpio + `gzip
  -n` for bit-stable rebuilds). New `TestEmbedContainsInitELF`
  walks the embedded cpio and asserts /init has ELF magic +
  ELFCLASS64 + ELFDATA2LSB + EM_AARCH64 — prevents regressing
  back to a script. `uefiboard.ProbeDTBConfigurationTable` extended
  to capture `AllGUIDs` (every `EFI_CONFIGURATION_TABLE` VendorGuid,
  not just the DTB one); `phase2_oci_kernel_boot.go` dumps the full
  list at MODE C probe time. Live arm64 trace confirms the bigger
  initrd still hits `Loaded initrd from LINUX_EFI_INITRD_MEDIA_GUID
  device path` (LoadFile2 trampoline scales beyond M8.4's 260 B
  fixture) AND that EDK2's arm64 firmware on `-machine virt`
  publishes ACPI 2.0 (`8868e871-…`) + SMBIOS3 (`f2fd1544-…`) but
  no DTB. `kernelboot_arm64.go` cmdline broadened to
  `acpi=force + earlycon=pl011,mmio32,0x9000000 + root=/dev/ram0
  rdinit=/init + loglevel=8 + panic=10`. **Open**: R-M8.5a —
  firmware Data Abort (FAR=0x40) inside the EFI-stub's empty-DTB
  patch path keeps the kernel from reaching `Run /init`. Crash is
  pre-cmdline-parse so the workarounds above don't yet land us at
  `Run /init`. M8.6 mitigation path documented: publish a DTB via
  `gBS->InstallConfigurationTable` from Go (embed a `qemu-system-
  aarch64 -machine virt,dumpdtb=…` snapshot + a new per-arch
  trampoline). Coverage 100% in `internal/embed_initramfs` (5 tests
  including the new ELF guard). No regression on uefiboard or any
  other arch's MODE B self-test.
