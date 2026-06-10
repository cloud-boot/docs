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

## Sprint 1.1 — live validation (2026-06-11)

Sprint 1.1 took the architecturally-complete sprint-1 PoC and put it
on live amd64 silicon under QEMU + OVMF (stable202605).

### Step 1 — first live-fire (predicted-fail diagnostic)

Hit two consecutive cliff edges:

1. **OOM streaming the bootonly ISO.** The 412 MiB FreeBSD-14.3
   bootonly ISO doesn't fit in tamago's 256 MiB `heapReserveSize`
   (`uefiboard/board_amd64.go`). `bytes.Buffer.Write` doubled past
   the cap and `runtime: out of memory: cannot allocate
   4194304-byte block (251428864 in use)` killed the runtime
   mid-stream. **Pivot:** custom 16 MiB ESP-only image (below).

2. **`ConnectController` wrongly indexed to offset 304** in
   `block_io_publish_tamago.go`'s `efiBSConnectController` constant
   — that's `ProtocolsPerHandle`, not `ConnectController` (which is
   at **264** per UEFI 2.10 §4.2 table 4.2). Returned
   `EFI_INVALID_PARAMETER` deterministically on every probe. Fixed
   in this sprint.

3. **`PublishBlockIO` installed only `EFI_BLOCK_IO_PROTOCOL`** — but
   EDK2's `PartitionDxe::DriverBindingSupported` requires
   `EFI_DEVICE_PATH_PROTOCOL` on the same handle. With only BlockIO,
   `ConnectController` couldn't even reach our driver-binding entry.
   Fixed: `PublishBlockIO` now uses
   `InstallMultipleProtocolInterfaces` to install both, plus a
   24-byte vendor-defined media device path (`buildBlockIOPublishDevicePath`,
   GUID `c10ddb00-7e1a-4001-91b0-070cb1ec80b1`).

### Step 2 — SFS-parent filter

Added `uefiboard/sfs_filter.go` (host-buildable) +
`sfs_filter_tamago.go` (live wiring):

- `FindSFSChildOf(parentHandle) → (sfsHandle, devicePathBytes)`
- Walks the firmware `EFI_DEVICE_PATH_PROTOCOL` of each SFS handle
  node-by-node and matches the parent device path as a strict prefix.
- 7 unit tests covering strict-child, exact-match-rejected, sibling-
  rejected, non-node-aligned-rejected, unterminated-prefix-rejected,
  empty-inputs, malformed-length-zero — all GREEN.

Also added `uefiboard/loadimage_sfs_tamago.go` →
`LoadImageFromSFS(parentDP, "\EFI\BOOT\BOOTX64.EFI")`:

- Builds a synthetic `parent DP ++ MEDIA_FILEPATH_DP(efiPath) ++ END`
  device path.
- Calls `gBS->LoadImage(BootPolicy=FALSE, parent, DP, NULL, 0, &out)`
  — the `DevicePath`-driven shape so firmware sets
  `EFI_LOADED_IMAGE.DeviceHandle` to our SFS child handle. Critical:
  FreeBSD's `loader.efi` keys its kernel + `loader.conf` reads off
  `LoadedImage.DeviceHandle`; a `SourceBuffer`-loaded image leaves
  it NULL and the loader fails immediately with "Failed to find
  bootable partition".

### Step 3 — custom ESP image (mfsBSD rejected, hand-craft adopted)

Investigated mfsBSD `mfsbsd-mini-14.0-RELEASE-amd64.img` (37 MiB) —
**rejected**: its GPT carries a `freebsd-boot` partition + UFS root,
NO FAT ESP. mfsBSD is BIOS-boot only; nothing for UEFI to chain.

Pivoted to a hand-crafted minimal disk:

- `internal/livefreebsdboot/buildespimg/main.go` — pure-Go helper.
  Takes an mformat-produced FAT image, wraps it with a
  spec-conformant PMBR + primary GPT header + 128 partition entries
  + backup partition array + backup GPT header. CRC32 (IEEE) over
  the header (with CRC=0) and over the partition entry array per
  UEFI 2.10 §5.3.1-2. Deterministic disk + partition GUIDs derived
  from the inner FAT SHA-256 (so two runs over the same content
  produce byte-identical images — handy for unit tests).
- `internal/livefreebsdboot/run.sh` now extracts `/boot/loader.efi`
  from the FreeBSD source ISO via `xorriso`, drops it into a 16 MiB
  **FAT16** (not FAT32) image at `\EFI\BOOT\BOOTX64.EFI`, wraps with
  `buildespimg`, pushes the resulting 16 MiB image to ttl.sh.

**FAT16-vs-FAT32 finding:** OVMF stable202605's `FatDxe` accepted
the FAT16 16 MiB ESP first try; an earlier 32 MiB FAT32 ESP
silently failed `LoadImage(\EFI\BOOT\BOOTX64.EFI)` with `Not Found`
(BdsDxe banner) despite the partition + FAT being structurally
valid (macOS `hdiutil pmap` saw it). Cluster-size or BPB rev
mismatch with EDK2's FAT driver — not investigated further; FAT16
is the right call at this size anyway.

### Step 4 — current live state

Confirmed end-to-end progress as of 2026-06-11:

| Stage | Result |
|-------|--------|
| OCI fetch + SHA-256 verify of 16 MiB image | PASS |
| MBR + GPT header sanity check               | PASS |
| `PublishBlockIO` (BlockIO + DevicePath both installed) | PASS |
| `ConnectController` (post offset-fix)        | **page fault in firmware** |

The synthetic disk image itself was independently verified to boot
under OVMF without our publish-side chain: dropped into a QEMU IDE
drive directly, it produces

```
FreeBSD/amd64 EFI loader, Revision 3.0
   Load Path: \EFI\BOOT\BOOTX64.EFI
   ...
   Failed to find bootable partition
```

which is the documented sprint-1.1 PASS gate (loader banner +
graceful failure on no UFS root). So the image, GPT, and FAT are
firmware-acceptable end-to-end.

The remaining `ConnectController` page fault is a firmware-side
driver-binding fault (`#PF`, `CR2=0xFFFFFFFF98009898`,
`RIP=0xA5023`) hit immediately on entering EDK2's driver-binding
chain. Both the SFS-parent filter and `LoadImageFromSFS` are
unreachable until that's resolved.

Working theories (in priority order for sprint 1.2):

1. MS-x64 XMM6..XMM15 callee-saved registers are NOT preserved by
   the four `block_io_publish_amd64.s` trampolines (they save only
   the integer set). If EDK2 uses any XMM lane around the BlockIO
   call (`memcpy` SSE inlining is common), state corruption could
   send a sign-extended garbage pointer into a downstream
   dereference — exactly matching the `0xFFFFFFFF........` pattern
   of CR2.
2. Trampoline frame size (`SUBQ $128, SP`) leaves the stack
   misaligned by 8 entering the Go-side handler; ABI0 doesn't
   require 16-byte alignment, but if Go's per-G state walking
   needs an aligned SP, that may explain why the fault doesn't
   show up in our host unit tests.
3. EDK2 may attempt a `BlockIo->Reset()` from inside
   `ConnectController` and our reset trampoline's lookup against
   the registry hits a stale entry.

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
| 1.1    | LoadImage from discovered SFS              | DONE (SFS-parent filter + LoadImageFromSFS + custom ESP image) — blocked on `ConnectController` `#PF` |
| 1.2    | Resolve `ConnectController` `#PF`          | XMM6..XMM15 save in trampolines + frame-alignment audit |
| 1.3    | arm64 / riscv64 / loong64 publisher trampolines | port `block_io_publish_amd64.s` |
| 2      | FreeBSD with UFS root                     | `go-filesystems/ufs` + filesystem publish (so loader.efi finds a kernel) |
| 3      | NetBSD / OpenBSD                          | FFS support in go-filesystems |
| 4      | Windows                                    | `go-filesystems/ntfs` + UEFI-stub special-casing |

## References

- UEFI 2.10 §4.2 (EFI Boot Services Table — function-pointer offsets)
- UEFI 2.10 §5.3 (GPT)
- UEFI 2.10 §7.3.12 (ConnectController)
- UEFI 2.10 §10.3 (Device Path)
- UEFI 2.10 §13.4 (EFI_SIMPLE_FILE_SYSTEM_PROTOCOL)
- UEFI 2.10 §13.9 (EFI_BLOCK_IO_PROTOCOL)
- MdePkg/Include/Protocol/BlockIo.h (edk2.git stable/202408)
- MdePkg/Include/Protocol/SimpleFileSystem.h (edk2.git stable/202408)
- MdeModulePkg/Core/Dxe/Hand/DriverSupport.c (`CoreConnectController`)
- MdeModulePkg/Universal/Disk/PartitionDxe/Partition.c (`PartitionInstallChildHandle`, `DriverBindingSupported`)
- FreeBSD 14.3-RELEASE amd64 bootonly ISO layout (verified 2026-06-11)
- mfsBSD-mini-14.0-RELEASE-amd64.img layout (rejected for ESP-only use; BIOS-boot only)
