# Phase 3 — OS-agnostic OCI boot

**Status:** sprint 1 (FreeBSD MVP) WIP — landed 2026-06-11.
**Owner:** cloud-boot/tamago-uefi
**Companion repos:** [`go-virtio`](../../../../go-virtio), [`go-filesystems`](../../../../go-filesystems)

## Goal

Boot operating systems other than Linux from OCI artifacts using
`tamago-uefi` as the network-side stub:

| OS         | Sprint | Filesystem dependency      | Status                    |
|------------|--------|-----------------------------|---------------------------|
| Linux      | Phase 2 done | — (kernel via OCI)         | LIVE on 4 arches (M8.15)  |
| FreeBSD    | **3.1**  | FAT (ESP) only for MVP     | sprint 1 WIP              |
| FreeBSD    | 3.2  | + UFS root                  | sprint 2 (needs `go-filesystems/ufs`) |
| NetBSD     | 3.2  | + FFS                       | sprint 2                  |
| OpenBSD    | 3.2  | + FFS                       | sprint 2                  |
| Windows    | 3.3  | + NTFS                      | sprint 3 (needs `go-filesystems/ntfs`) |

Phase 2 proved the end-to-end pipeline against Linux: OCI streaming
+ in-memory artifact + LoadImage + StartImage. Phase 3 extends that
pipeline to OSes that ship as **bootable disk images** (GPT + ESP +
UFS/NTFS root) rather than as standalone EFI-stub kernels.

## Architecture

Phase 3 introduces a new firmware-facing protocol on the publish side:
**EFI_BLOCK_IO_PROTOCOL** (UEFI 2.10 §13.9). The streamed disk-image
bytes become a synthetic Block IO device the firmware then mounts via
its native driver stack:

```
   OCI artifact (single layer, raw disk image bytes)
                       │  reg.FetchBlobStream + SHA-256 verify
                       ▼
   ┌────────────────────────────────────────────┐
   │  imageBytes []byte (in TamaGo heap)         │
   └────────────────────────────────────────────┘
                       │  uefiboard.PublishBlockIO(imageBytes)
                       ▼
   ┌────────────────────────────────────────────┐
   │  EFI_BLOCK_IO_PROTOCOL on a fresh handle    │
   │  Media: BlockSize=512, ReadOnly=1            │
   │  ReadBlocks: per-arch asm trampoline ->     │
   │              ·blockIOReadBlocksGo            │
   │  WriteBlocks: EFI_WRITE_PROTECTED            │
   └────────────────────────────────────────────┘
                       │  uefiboard.ConnectController(handle)
                       ▼
   ┌────────────────────────────────────────────┐
   │  EDK2 auto-binds:                           │
   │    DiskIoDxe     (BlockIo -> DiskIo)        │
   │    PartitionDxe  (parses GPT/MBR/ElTorito)  │
   │    FatDxe        (mounts FAT on ESP child)  │
   └────────────────────────────────────────────┘
                       │  LocateHandleBuffer(SFS_GUID)
                       ▼
   ┌────────────────────────────────────────────┐
   │  EFI_SIMPLE_FILE_SYSTEM_PROTOCOL on the     │
   │  ESP child handle                           │
   └────────────────────────────────────────────┘
                       │  Open + LoadImage(\EFI\BOOT\BOOTX64.EFI)
                       ▼
   ┌────────────────────────────────────────────┐
   │  loader.efi (FreeBSD), boot.efi (NetBSD),   │
   │  bootmgfw.efi (Windows), ...                │
   └────────────────────────────────────────────┘
```

The split is deliberate: TamaGo publishes a **block device**, not a
filesystem. EDK2's PartitionDxe + FatDxe / Iso9660Dxe / etc. handle
the GPT walk + FAT mount natively, so we don't reimplement filesystem
drivers in Go. The Go side only needs to satisfy ReadBlocks at the
LBA level (a 5-line `copy` in nosplit code).

## Sprint 1 — FreeBSD MVP

**Image source:** FreeBSD 14.3-RELEASE amd64 bootonly ISO (412 MiB),
downloaded from `https://download.freebsd.org`. Hybrid GPT+ISO9660
with a FAT12 ESP at LBA 80..4175 containing `\EFI\BOOT\BOOTX64.EFI`
(the FreeBSD loader.efi).

**Pipeline (`phase2_oci_freebsd_boot.go`):**

1. virtio-net + DHCP + ministack + roots (same as Phase 2 MODE C)
2. OCI fetch of single-layer `application/vnd.cloud-boot.diskimage.raw.v1`
3. Sanity: protective MBR signature + GPT magic "EFI PART"
4. Pad image to 512-byte multiple
5. `PublishBlockIO(imageBytes)` — synthetic EFI_BLOCK_IO_PROTOCOL
6. `ConnectController(handle)` — drives PartitionDxe + FatDxe binding
7. `LocateHandleBuffer(SFS_GUID)` — finds the ESP child handle

**Sprint 1 PASS gate:** SFS child handle surfaced after ConnectController.
The MVP CHAIN COMPLETE line proves the architecture works.

**Sprint 1 explicit out-of-scope:**

- arm64 / riscv64 / loong64 (publisher trampolines are amd64-only;
  follow-up: write `block_io_publish_arm64.s` etc.)
- Picking the SFS handle whose parent device-path matches the
  published block handle (sprint 1.1 — defer-discriminating helper
  needed)
- Actual `LoadImage \EFI\BOOT\BOOTX64.EFI` call (sprint 1.1)
- UFS root mount so FreeBSD loader.efi can find its kernel
  (sprint 2 — needs `go-filesystems/ufs`)
- Full FreeBSD multi-user boot (sprint 2)

## Code layout (sprint 1)

- `uefiboard/block_io_publish.go` — host-buildable surface
  (errors, registry, GUIDs, struct layout)
- `uefiboard/block_io_publish_handlers.go` — Go-side Read/Write/Reset/Flush
  handlers (host-buildable, 25 host tests cover all paths)
- `uefiboard/block_io_publish_tamago.go` — `PublishBlockIO` /
  `UnpublishBlockIO` / `ConnectController` (tamago amd64 only)
- `uefiboard/block_io_publish_amd64.s` — 4 reverse-direction MS-x64
  asm trampolines (Reset/Read/Write/Flush)
- `uefiboard/block_io_publish_host.go` — host stubs for non-amd64-tamago
- `uefiboard/block_io_publish_test.go` — host tests (GUID round-trip,
  handler semantics, registry behavior)
- `phase2_oci_freebsd_boot.go` — probe (tagged
  `phase3_oci_freebsd_boot && tamago && amd64`)
- `phase2_oci_freebsd_boot_stub.go` — no-op stub for off-tag builds
- `internal/livefreebsdboot/run.sh` — live runner
- `internal/livefreebsdboot/pushfreebsd/main.go` — anonymous ttl.sh push
  helper for disk-image artifacts

## Relationship to existing code

The pre-existing `uefiboard/block_io_protocol*.go` files are the
**consumer** side — they read a firmware-installed EFI_BLOCK_IO_PROTOCOL
for the M1.6 side-channel print mechanism on Apple VZ. The Phase 3
sprint 1 files are the **publisher** side. Both halves coexist
without conflict — the consumer reads `(*EFIBlockIOProtocol).ReadBlocks`,
the publisher provides one — and they share the GUID + struct-layout
constants in `block_io_protocol.go`.

## Roadmap

| Sprint | Target                                    | Gap to close |
|--------|-------------------------------------------|---------------|
| 1.1    | LoadImage from discovered SFS              | parent-handle device-path filter |
| 1.2    | arm64 / riscv64 / loong64 publisher trampolines | port `block_io_publish_amd64.s` |
| 2      | FreeBSD with UFS root                     | `go-filesystems/ufs` + filesystem publish |
| 2      | NetBSD / OpenBSD                          | FFS support in go-filesystems |
| 3      | Windows                                    | `go-filesystems/ntfs` + UEFI-stub special-casing |

## References

- UEFI 2.10 §13.9 (EFI_BLOCK_IO_PROTOCOL)
- UEFI 2.10 §7.3.12 (ConnectController)
- UEFI 2.10 §13.4 (EFI_SIMPLE_FILE_SYSTEM_PROTOCOL)
- MdePkg/Include/Protocol/BlockIo.h (edk2.git stable/202408)
- MdePkg/Include/Protocol/SimpleFileSystem.h (edk2.git stable/202408)
- MdeModulePkg/Core/Dxe/Hand/DriverSupport.c (`CoreConnectController`)
- FreeBSD 14.3-RELEASE amd64 bootonly ISO layout (verified 2026-06-11)
