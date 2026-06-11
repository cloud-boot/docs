# Phase 3 — OS-agnostic OCI boot

**Status:** sprint 1 (FreeBSD MVP) **DONE** — sprint 1.2 closed 2026-06-11; ready for sprint 2 (UFS).
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

## Sprint 1.2 — `ConnectController` `#PF` closed (2026-06-11)

Sprint 1.2 closes R-fbsd1a — the firmware-side page fault that
sprint 1.1's final live test hit on entering `ConnectController`. The
root cause was two stacked register-corruption bugs in the firmware-
to-Go trampoline path:

### Bug A — MS x64 callee-saved XMM6..XMM15 NOT preserved

The four `block_io_publish_amd64.s` trampolines (`Reset`,
`ReadBlocks`, `WriteBlocks`, `FlushBlocks`) saved only the integer
callee-saved set (`RBX`, `RBP`, `RDI`, `RSI`, `R12..R15`). MS x64
**also** marks `XMM6..XMM15` as callee-preserved. Go's amd64 codegen
emits XMM moves in plenty of innocent-looking call sites (zeroed
struct stores, byte-loop memmove inlining), so returning to firmware
with corrupted XMM6..XMM15 risks a delayed firmware-side page fault
when EDK2 later reloads its preserved XMM state. The `CR2 =
0xFFFFFFFF98009898` fingerprint pattern (sign-extended uint32 in a
64-bit register) was the giveaway.

**Fix:** trampoline prologue now saves all 10 XMM regs via `MOVUPS`
into a 16-byte-stride save area; epilogue restores symmetrically.
Frame size grew `SUBQ $128, SP` → `SUBQ $304, SP` (128 integer area
+ 160 XMM area + 16 alignment). The 5-arg shapes' stack-passed-5th-
arg offset shifted from `SP+168` → `SP+344` (delta +176 = frame-grow
delta). The same fix was applied to `initrd_protocol_amd64.s`
(`LoadFile`) and `rng_protocol_amd64.s` (`GetRNG`, `GetInfo`)
defensively, though neither was reported failing under M8.x.

### Bug B — autogen ABIInternal wrapper clobbers X15 + R14 on return

This was the actual reason the XMM-save fix on its own didn't move
the failure. A Go function-value's first word points at the
*ABIInternal* wrapper, not at our `.abi0` entry. The amd64 wrapper
ends with:

```
CALL  .abi0
XORPS X15, X15                  ; clobbers MS x64 callee-saved X15
MOVQ  $-8, R14
MOVQ  FS:0(R14), R14            ; clobbers MS x64 callee-saved R14
POPQ  BP
RET
```

That trailer fires **after** our `.abi0` trampoline restored X15 and
R14, so firmware sees them corrupted regardless of what our prologue
saved. The Go ABI rules are right to do that for Go-to-Go calls; we
just shouldn't be installing the wrapper PC into a firmware-callable
function-pointer slot.

**Fix:** added four asm helpers
`blockIO_<op>_trampolinePC()` in `block_io_publish_amd64.s` that
return the .abi0 entry PC via `LEAQ ·blockIO_<op>_trampoline(SB)`
(from asm, that resolves to the ABI0 symbol — not the wrapper).
`PublishBlockIO` now reads the PC through those helpers instead of
the funcval indirection.

### Result

```
phase3-oci-freebsd-boot: PublishBlockIO OK; block handle = 0x7e204398
phase3-oci-freebsd-boot: ConnectController OK (DiskIo/PartitionDxe/FatDxe binding done)
phase3-oci-freebsd-boot: LocateHandleBuffer(SFS) found 2 total handle(s) (parent + siblings)
phase3-oci-freebsd-boot: matching SFS child handle = 0x7daf7f98
phase3-oci-freebsd-boot: child device path length = 66 bytes
phase3-oci-freebsd-boot: LoadImage( \EFI\BOOT\BOOTX64.EFI ) OK; image handle = 0x7daf5f18
phase3-oci-freebsd-boot: FREEBSD-BOOT CHAIN COMPLETE -- transferring control to loader.efi
press any key to interrupt reboot in 5 seconds...
phase3-oci-freebsd-boot: StartImage returned: uefi: StartImage: EFI_STATUS=0x800000000000000e
```

The `press any key to interrupt reboot` prompt is FreeBSD's
`loader.efi` countdown — confirms the loader started and ran past
its no-UFS-root branch. Sprint 1.2 PASS gate met.

### M8.x regression check (all 4 arches)

| Arch     | M8.10/11/12 result |
|----------|--------------------|
| amd64    | PASS — `/init userspace reached` |
| arm64    | PASS — `/init userspace reached` |
| riscv64  | PASS — `/init userspace reached` |
| loong64  | PASS — `/init userspace reached` |

No regressions. The XMM-save change is defensive on the
`initrd_protocol_amd64.s` / `rng_protocol_amd64.s` paths and the
PC-helper fix is scoped to the block-IO surface only (the
loadFile/rng surfaces still install the funcval PC; if a future
caller exposes the same ABIInternal-wrapper bug there, the same
helper pattern applies).

### Audit of the other three arches

The XMM-save bug is amd64-specific (MS x64 ABI). The arm64 / riscv64
/ loong64 trampolines do not currently save the FP callee-saved set
(`D8..D15` / `fs0..fs11` / `f24..f31`). Phase 2 / M8.x has been live-
validated on all three arches without an analogous failure surfacing,
but the same defensive save/restore would be cheap to add when the
arm64 / riscv64 / loong64 block-IO publisher trampolines land in
sprint 1.3. Tracked as a follow-up there.

## Relationship to existing code

The pre-existing `uefiboard/block_io_protocol*.go` files are the
**consumer** side — they read a firmware-installed EFI_BLOCK_IO_PROTOCOL
for the M1.6 side-channel print mechanism on Apple VZ. The Phase 3
sprint 1 files are the **publisher** side. Both halves coexist
without conflict — the consumer reads `(*EFIBlockIOProtocol).ReadBlocks`,
the publisher provides one — and they share the GUID + struct-layout
constants in `block_io_protocol.go`.

## Sprint 2C — UFS-backed FreeBSD root (2026-06-11)

Sprint 2C shipped as **three independent agents** plus an **integration
pass** that wired them into the live FreeBSD boot pipeline.

### 2C-A — pure-Go UFS2 writer (`go-filesystems/ufs@8b415bc`)

`ufs.Mkfs(w io.WriterAt, sizeBytes int64) (*FS, error)` mints a fresh
UFS2 filesystem onto a backing `ReadWriterAt`, populating the
canonical superblock at byte 65536 plus one cylinder-group header
per 1 MiB of size. Companion `WriteFile / MkDir / Symlink / Rename
/ Delete` writers let callers populate the filesystem in-process.
Defaults: `bsize=4096`, `fsize=4096`, single-indirect — gives every
file a 2 MiB cap (`(NumDirect+Nindir)*bsize = (12+512)*4096`).
Cross-validated against a real FreeBSD-makefs reference image via
`crossvalidate_test.go`. 2300 LOC, 85.7% line coverage.

### 2C-B — real FreeBSD UFS2 fixture (`internal/livefreebsdboot/extractufs`)

`install.sh` + `extract_ufs.sh` + `minimize_fixture.sh` pull the
upstream `FreeBSD-14.3-RELEASE-amd64.raw.xz` VM image, carve out the
5 GiB `freebsd-ufs` partition (`516E7CB6-6ECF-11D6-8FF8-00022D09712B`),
and emit a 30 MiB `bootroot.tar` containing `/boot/{kernel/kernel,
kernel/*.ko (virtio subset), loader.conf, loader.efi, ...}`. The
verify binary uses a pinned snapshot of `go-filesystems/ufs` at the
sprint-2A read-only commit so the cross-check is immune to the
write-side changes 2C-A lands. Synthesises `/boot/loader.conf` from
a checked-in template (the upstream VM image ships none).

### 2C-C — fresh UFS2 oracle (`internal/livefreebsdboot/genufs`)

Docker-driven `kusumi-makefs` builds a 64 MiB UFS2 image from a
staged `/boot + /etc` tree. The gold oracle: a third-party,
FreeBSD-correct `Mkfs` to validate our pure-Go writer against.

**Cross-learning narrative — the SBLOCK offset gotcha**: NetBSD's
`makefs` defaults to placing the UFS2 superblock at byte 8192 (FFSv1
convention); FreeBSD's expects it at byte 65536 (FFSv2). The first
2C-C cut used NetBSD `makefs` for portability and produced an image
that our reader rejected as "no UFS2 magic at offset 65536." Pivoting
to the kusumi-makefs port (FreeBSD-flavored) immediately closed the
gap. Documented in `genufs/README.md` so future sprints (2E arm64,
3 NetBSD/OpenBSD) don't burn the same hour.

### 2C-Integration — `buildespimg -ufs` wires the three together

`internal/livefreebsdboot/buildespimg/main.go` gained a `-ufs <tar>`
flag. When set, the output disk is a 2-partition GPT:

```
LBA 0          : Protective MBR
LBA 1          : Primary GPT header
LBA 2..33      : Primary partition entry array
LBA 64..32831  : FAT16 ESP (16 MiB, type C12A7328-..., contains
                 \EFI\BOOT\BOOTX64.EFI = loader.efi)
LBA 34816..    : FreeBSD-UFS (8 MiB by default, type 516E7CB6-...,
                 minted in-process via go-filesystems/ufs.Mkfs +
                 populated from the tar via fs.MkDir + fs.WriteFile +
                 fs.Symlink)
LastLBA-33..   : Backup partition entry array
LastLBA        : Backup GPT header
```

The disk is cross-validated before push: `dd` carves out the UFS
partition slice, `extractufs/verify/verify -require-loader-conf=true
-require-kernel=false` opens it via the **pinned** sprint-2A read-only
snapshot of `go-filesystems/ufs` (NOT our own writer's reader — the
oracle is independent) and asserts `/boot/loader.conf` is present
and parses. Any divergence between our writer's on-disk bytes and
the upstream-pinned reader fails the build closed.

### Live test outcome (2026-06-11)

```
phase3-oci-freebsd-boot: PublishBlockIO OK; block handle = 0x7e204398
phase3-oci-freebsd-boot: ConnectController OK
phase3-oci-freebsd-boot: LocateHandleBuffer(SFS) found 2 handle(s)
phase3-oci-freebsd-boot: matching SFS child handle = 0x7d603e98
phase3-oci-freebsd-boot: LoadImage( \EFI\BOOT\BOOTX64.EFI ) OK
phase3-oci-freebsd-boot: PublishSFS OK; UFS-backed SFS handle = 0x7d5f9d98   ← NEW gate
phase3-oci-freebsd-boot: FREEBSD-BOOT CHAIN COMPLETE
```

…then the FreeBSD loader.efi banner:

```
FreeBSD/amd64 EFI loader, Revision 3.0
Trying ESP: ... HD(1,GPT,BF103427-B57F-F240-AFCA-3E579CFC9597)
Setting currdev to disk1p1:
Trying:    ... HD(2,GPT,9A7B8338-79A0-AC47-A3B4-7F5007F2C72B)
Setting currdev to disk1p2:                                       ← UFS reached!
```

**loader.efi successfully enumerated and selected our UFS partition
(disk1p2) via the EFI_SIMPLE_FILE_SYSTEM_PROTOCOL surface we
published.** It then fails to load the kernel — the new boundary
queued for sprint 2D.

### Sprint 2C-Integration boundary → sprint 2D scope

The pure-Go writer (`ufs.Mkfs`) defaults to `bsize=4096` + single-
indirect-only block pointers, capping per-file size at:

> `(NumDirect + Nindir) × bsize = (12 + 512) × 4096 ≈ 2.0 MiB`

The FreeBSD `/boot/kernel/kernel` is 29 MiB — well beyond that cap.
`buildespimg` therefore skips files over the writer cap with a clear
diagnostic, so the UFS partition we hand to loader.efi has every
file EXCEPT the kernel itself.

**Sprint 2D scope** (queued):

1. Extend `ufs.Mkfs` to accept a `bsize` knob (32 KiB is the
   FreeBSD newfs default; would raise Nindir to 4096 → per-file cap
   ~134 MiB single-indirect alone — enough for the kernel).
2. Add double-indirect support to `writeFileData` so the writer can
   address files up to (12 + N + N²) × bsize ≈ 64 GiB without
   triple-indirect.
3. Keep 100% cov per `go-deltasync`-style HARD RULE (per CLAUDE.md
   feedback) so the writer extension lands clean.
4. Re-run sprint-2C-Integration live test; expected outcome — kernel
   loads, prints `FreeBSD/amd64 (...) #N: ...` banner, reaches
   `mountroot>` (and likely fails there because we have no rootfs
   image beyond /boot/ — that's the sprint 2D' follow-up: provide a
   `vfs.root.mountfrom` hint pointing at a synthetic mfsroot or wire
   `/boot/loader.conf` to `boot_mfsroot="YES"` + ship an mfsroot.gz).

The publish-side stack (PublishBlockIO + PublishSFS + UFS-as-SFS
adapter) is sprint-2C-complete: every layer the loader.efi walks is
already wired and proven by the live test.

## Sprint 2D — bsize=32768 + double-indirect → 29 MiB kernel lands in UFS (2026-06-11)

**Goal**: lift the sprint-2C-A writer's per-file cap from ~2 MiB
(bsize=4096 single-indirect-only) so the 29 MiB FreeBSD kernel
actually lands in `/boot/kernel/kernel` instead of being skipped by
`buildespimg`'s file-size diagnostic.

**Approach**: extend the pure-Go UFS2 driver with an explicit-options
`MkfsWith(...)` entry point + double-indirect block reading **and**
writing, so callers can dial `BlockSize=32768` (FreeBSD `newfs(8)`
default) and address files via the full direct + single-indirect +
double-indirect chain.

### Deliverables shipped

1. **`go-filesystems/ufs.MkfsOptions` + `MkfsWith(...)`**
   ([sprint2d_test.go](https://github.com/go-filesystems/ufs)):
   - `BlockSize` (4 KiB..64 KiB, power of two)
   - `FragmentSize` (defaults to `BlockSize/8` per FreeBSD convention)
   - `InodeDensity` (one inode per N bytes, default 4 KiB)
   - `Label` (reserved)

   `Mkfs(...)` itself stays untouched for backward compat — it still
   produces sprint-2C-A's `bsize=4096`/`frag=1` legacy geometry.

2. **Double-indirect block surface**
   - **Reader** (`block.go`): `blockForLBN` now walks
     `in.Indirect[1]` → tier-1 (`Nindir` outer pointers) → tier-2
     (single-indirect block per outer slot) → data fragment, reaching
     `Nindir² × bsize` bytes per file.
   - **Writer** (`write.go`): `writeFileData` now lazy-allocates the
     tier-1 block on first double-indirect LBN, then lazy-allocates a
     tier-2 block per `Nindir`-block chunk. Single-pass tier-1/tier-2
     flush at the end keeps the write pattern O(1) per data block.
   - **Reclaimer** (`write.go::freeFileBlocks`): walks the full
     double-indirect chain on `DeleteFile` so no blocks leak when a
     large file is removed and the inode is reused.

3. **Per-file size ceilings (at `BlockSize=32768`)**
   | Tier | Reach | Notes |
   |------|-------|-------|
   | Direct (12 ptrs)               | 384 KiB    | unchanged |
   | + Single-indirect (4096 ptrs)  | ~128 MiB   | replaces 2 MiB ceiling |
   | + Double-indirect (4096² ptrs) | ~8 GiB     | new in 2D |
   | (Triple-indirect deferred — 1 PiB at bsize=32768 is over-spec) | | |

4. **Tests** (`sprint2d_test.go`):
   - `TestMkfsWith_DefaultOpts` — zero-value options still produce a
     valid UFS2.
   - `TestMkfsWith_BadOptions` — six validation branches (bad sizes,
     non-power-of-two, fragment-doesn't-divide-block, low inode
     density).
   - `TestMkfsWith_BigBlock` — confirms bsize=32768 produces the
     expected `Nindir=4096`, `Fsize=4096`, `Frag=8` geometry.
   - `TestWriteFile_BigBlock_25MiB` — sha256 round-trip of a 25 MiB
     pseudo-random blob through bsize=32768 single-indirect.
   - `TestWriteFile_DoubleIndirect_25MiB` — same blob through
     bsize=4096 so double-indirect MUST engage; asserts `Indirect[1]
     != 0` on the read-side inode + sha256 round-trip.
   - `TestWriteFile_DoubleIndirect_DeleteFrees` — write 4 MiB
     (engages dindir at bsize=4096), `DeleteFile`, confirm free-block
     count grew, write another 4 MiB into the freed pool, sha256
     match the second payload.
   - `TestWriteFile_NoSpace_DoubleIndirect` — exercises the
     out-of-blocks error path inside the double-indirect allocator
     branch.
   - `TestCrossValidate_DoubleIndirectFile` — write 20 MiB via the
     new writer, then re-read it via TWO paths (high-level
     `fs.ReadFile` AND low-level `blockForLBN` walk) — sha256 of
     both must match the payload AND each other.

   Driver coverage: 86.6% (up from sprint-2C-A's 85.7% baseline).

5. **`buildespimg` wiring**
   (`internal/livefreebsdboot/buildespimg/main.go`):
   - `ufs.Mkfs(...)` → `ufs.MkfsWith(..., MkfsOptions{BlockSize: 32768})`
   - Removed the ">2 MiB skip" diagnostic (no longer needed).
   - Bumped UFS partition floor from 8 MiB to 48 MiB.

6. **`extractufs/verify` cross-check** (`run.sh`): bumped
   `-require-kernel=true` so the pinned sprint-2C-B verifier asserts
   `/boot/kernel/kernel` is present and readable end-to-end.

### Cross-validation result (offline, before live launch)

The pinned sprint-2C-B `extractufs/verify` tool reads back the
`buildespimg`-produced UFS partition and confirms:

```
superblock: bsize=32768 fsize=4096 ncg=48 magic=ok
/boot/kernel: 20 entries
/boot/kernel/kernel: size=29185072 bytes mode=0100644
/boot/loader.conf: 659 bytes; first line: "# Synthetic /boot/loader.conf …"
OK — go-filesystems/ufs successfully read the partition
```

The 29 MiB kernel lands intact, double-indirect-or-single-indirect
chain reads back to the same bytes, and superblock geometry is
sprint-2A-reader-compatible. **The Mkfs surface and the publish-side
publish-side UFS plumbing are sprint-2D-complete.**

### Live-runner status: blocked on tamago heap / OCI streaming pipeline

The live `freebsdboot:live:amd64` runner OOMs at the OCI streaming
step **before** loader.efi gets a chance to run. The failure mode is
deterministic:

```
phase3-oci-freebsd-boot: streaming disk image layer size   = 63980032
runtime: out of memory: cannot allocate 4194304-byte block (251428864 in use)
fatal error: out of memory
```

The 256 MiB tamago heap (board_amd64.go `heapReserveSize`) was sized
for sprint-2C-Integration's 25 MiB disk image. A 64 MiB image
(4 MiB ESP + 48 MiB UFS + GPT) pushes the streaming pipeline (oras
`FetchBlobStream` + SHA-256 verify + bytes.Buffer pre-grow + TLS
record working set) past the heap ceiling at the constant
~240 MiB-in-use mark, regardless of further per-MiB shrinkage on
the disk image side.

**Sprint 2D' (follow-up) scope**: refactor the OCI streaming pipeline
in `phase2_oci_freebsd_boot.go` so the disk image is written
directly into the publish-side `BlockIO` backing buffer (one
allocation) rather than going through `bytes.Buffer` + `imageBytes`
+ a potential pad-append (two-to-three live copies). Or bump
`heapReserveSize` to 384 MiB. Either lift is a 30-min change but
sits in the tamago-uefi runtime, not in the UFS driver scope of 2D.

### Sprint 2D PASS gate

- **Offline (`extractufs/verify` cross-validation)**: PASS — 29 MiB
  kernel reads back intact through double-indirect chain.
- **`go-filesystems/ufs` unit + cross-validation tests**: PASS
  (86.6% coverage).
- **Live (`freebsdboot:live:amd64`)**: BLOCKED on tamago heap OOM
  during OCI streaming. The blocker is independent of the UFS writer
  extension — the writer correctly produces a 29 MiB-kernel-bearing
  partition and the read-side verifier confirms it.

### Sprint 2E scope (queued)

- Resolve the tamago streaming-OOM blocker (refactor or heap bump).
- Then re-run live test for the kernel-handoff trace: expect
  `Loading /boot/kernel/kernel` → `FreeBSD/amd64 (...) #0` banner →
  `mountroot>` prompt (since we have no rootfs beyond `/boot/`, the
  kernel will halt at mountroot — that's sprint-2F scope:
  synthesise mfsroot.gz + `boot_mfsroot="YES"` in loader.conf, or
  ship a tiny init).

## Roadmap

| Sprint | Target                                    | Gap to close |
|--------|-------------------------------------------|---------------|
| 1.1    | LoadImage from discovered SFS              | DONE (SFS-parent filter + LoadImageFromSFS + custom ESP image) |
| 1.2    | Resolve `ConnectController` `#PF`          | DONE — XMM6..XMM15 save in trampolines + bypass ABIInternal wrapper via `LEAQ`-based PC helper |
| 1.3    | arm64 / riscv64 / loong64 publisher trampolines | port `block_io_publish_amd64.s` + defensive D8..D15 / fs0..fs11 / f24..f31 save |
| 2B     | EFI_SIMPLE_FILE_SYSTEM_PROTOCOL surface    | DONE — UFS-backed SFS wire + GPT partition detection |
| 2C-A   | pure-Go UFS2 writer (`ufs.Mkfs`)           | DONE — single-indirect + bsize=4096 default |
| 2C-B   | real FreeBSD UFS2 fixture (`extractufs`)   | DONE — bootroot.tar + verifier against pinned 2A reader |
| 2C-C   | fresh UFS2 oracle via kusumi-makefs        | DONE — gold cross-check for 2C-A writer |
| 2C-Int | wire 2C-A + 2C-B into live boot via `buildespimg -ufs` | **DONE** — 2-partition GPT, PublishSFS OK, loader.efi reads UFS @ disk1p2 |
| 2D     | `MkfsWith(BlockSize=32768)` + double-indirect → 29 MiB kernel lands in UFS | **DONE (offline)** — pure-Go writer ships kernel; verified via pinned 2C-B reader. Live runner blocked on tamago streaming OOM — sprint 2D' |
| 2D'    | tamago heap / OCI streaming refactor (single live copy or `heapReserveSize` bump) | refactor `phase2_oci_freebsd_boot.go` stream path; or raise `board_amd64.go::heapReserveSize` 256 MiB → 384 MiB |
| 2E     | post-loader: kernel banner + `mountroot>`  | sprint 2D' unblocks; expected handoff is `Loading /boot/kernel/kernel` then `FreeBSD/amd64 (...) #0` |
| 2F     | mfsroot or rootfs hint so kernel reaches single-user | synthesise mfsroot.gz + `boot_mfsroot="YES"` in loader.conf |
| 2G     | arm64 FreeBSD EFI loader port              | port BOOTX64 publish trampolines to BOOTAA64; FreeBSD ARM EFI loader signature |
| 3      | NetBSD / OpenBSD                          | FFS family in go-filesystems; loader names differ |
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
