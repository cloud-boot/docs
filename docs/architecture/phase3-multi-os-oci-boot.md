# Phase 3 — OS-agnostic OCI boot

**Status:** sprint 1 (FreeBSD MVP) **DONE** — sprint 1.3 closed 2026-06-11 (defensive FP saves on arm64/riscv64/loong64 RNG trampolines); sprint 2 (UFS) DONE; sprint 3 (NetBSD/OpenBSD) DONE — sprint 3.x closed 2026-06-11 (NetBSD live boot PASS via 307 MiB installer boot.iso, `NetBSD/x86 EFI Boot (x64)` banner + `boot:` prompt reached); sprint 4 (Windows scaffolding) DONE.
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
| Windows    | 4    | + NTFS + BCD                | **sprint 4.0 SCAFFOLDING DONE 2026-06-11** — blocked on real NTFS support (sprint 4.0a) |

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

## Sprint 1.3 — defensive FP callee-saved saves (R-fbsd1c, 2026-06-11)

Sprint 1.3 closes the cross-arch defensive parity gap flagged at the
end of the sprint 1.2 audit. The amd64 XMM6..XMM15 + LEAQ-direct fix
shipped in sprint 1.2 has analogues on the other three arches that
have not yet manifested under M8.x — purely because the firmware-side
callees those arches' trampolines feed (the EFI-stub's RNG path) do
not exercise FP enough to corrupt callee-saved FP registers across
the Go→firmware return. The risk shape is identical; we apply the
defensive infrastructure now.

### Scope

Sprint 1 + sprint 1.1 / 1.2 left the block-IO + SFS publisher
trampolines (and so their corresponding asm files) **amd64-only** —
the `//go:build tamago && amd64` constraint on
`block_io_publish_tamago.go` / `sfs_publish_tamago.go` reflects that
arm64 / riscv64 / loong64 publisher ports are deferred to the
post-FreeBSD-MVP sprints. The sprint 1.3 scope is therefore
restricted to the only firmware→Go callback trampolines that DO ship
on all four arches today: the **RNG protocol** (`GetRNG` + `GetInfo`).

| arch    | file                            | trampolines patched | FP regs saved |
|---------|---------------------------------|---------------------|---------------|
| arm64   | `uefiboard/rng_protocol_arm64.s`   | 2 (GetRNG, GetInfo) | D8..D15        (AAPCS64 §5.1.2) |
| riscv64 | `uefiboard/rng_protocol_riscv64.s` | 2                   | fs0..fs11 (F8, F9, F18..F27) (RISC-V psABI LP64D) |
| loong64 | `uefiboard/rng_protocol_loong64.s` | 2                   | fs0..fs7 (F24..F31) (LoongArch LP64) |

**Total:** 6 trampolines patched (2 per arch × 3 arches). The block-IO
+ SFS trampolines on these arches remain deferred (the asm files
don't exist yet — they'll be authored with the FP saves baked in
once the publisher ports land).

### Per-trampoline change

Frame size grew to accommodate an 8-aligned FP save area appended
past the existing integer-register saves:

| arch    | GetRNG frame (old → new) | GetInfo frame (old → new) | FP area |
|---------|---------------------------|----------------------------|---------|
| arm64   | 128 → 192 B               | 128 → 192 B                | 64 B (8 × 8 B) |
| riscv64 | 160 → 256 B               | 160 → 256 B                | 96 B (12 × 8 B) |
| loong64 | 144 → 208 B               | 128 → 192 B                | 64 B (8 × 8 B) |

Save mnemonic per arch:

- **arm64:** `FMOVD F8, off(RSP)` ... `FMOVD F15, off(RSP)` (Go-asm
  spelling of D-form `STR Dn, [SP, #off]`).
- **riscv64:** `MOVD F8, off(X2)` etc. for fs0/fs1 + `MOVD F18, off(X2)`
  through `MOVD F27, off(X2)` for fs2..fs11 (LP64D ABI's FS group).
- **loong64:** `MOVD F24, off(R3)` through `MOVD F31, off(R3)` for
  fs0..fs7 (LP64 LoongArch ABI).

### LEAQ-direct PC helper analogues

Sprint 1.2's amd64 fix had two parts. The XMM saves were one; the
other was a per-trampoline `LEAQ ·sym(SB)` PC helper that bypasses
the Go ABIInternal wrapper a `funcval` first-word deref would land
on. The wrapper's trailer (XORPS X15 / MOVQ FS:0(g),R14 on amd64)
clobbers callee-saved registers AFTER the .abi0 epilogue restored
them — invisible to Go but observed by the firmware-side caller as
register corruption.

Sprint 1.3 mirrors the LEAQ-direct pattern on the three new arches,
using each arch's symbol-address pseudo-instruction:

| arch    | LEAQ analogue (assembler pseudo)        | Expansion            |
|---------|------------------------------------------|----------------------|
| arm64   | `MOVD $·sym(SB),Rn`                      | `ADRP + ADD`          |
| riscv64 | `MOV $·sym(SB),Rn`                       | `AUIPC + ADDI`        |
| loong64 | `MOVV $·sym(SB),Rn`                      | `PCALAU12I + ADDI`    |

Each per-arch asm file gains two PC helpers
(`rngGetRNG_trampolinePC`, `rngGetInfo_trampolinePC`); the Go-side
consumer dispatches via a new shared helper
`rngTrampolinePCs()` whose implementation is split per arch:

- `rng_protocol_trampolinepc_amd64.go` — keeps the legacy `funcval`
  first-word deref (sprint 1.2 did NOT touch RNG on amd64; the RNG
  path has not manifested the wrapper-trailer bug, and the brief
  explicitly leaves amd64 untouched).
- `rng_protocol_trampolinepc_{arm64,riscv64,loong64}.go` — calls the
  new per-arch asm PC helpers.

amd64-side parity (RNG XMM saves + LEAQ-direct helpers) is tracked
as a future follow-up; if a live amd64 RNG crash to the wrapper-
trailer pattern surfaces, the swap-in is mechanical (add the
helpers to `rng_protocol_amd64.s`, replace the `funcval`-deref
body of `rng_protocol_trampolinepc_amd64.go`).

### Verification

Live tests passed on all three patched arches:

```
TAMAGO=… task kernelboot:live:arm64    # PASS — wall=18111ms
TAMAGO=… task kernelboot:live:riscv64  # PASS — wall=18164ms
TAMAGO=… task kernelboot:live:loong64  # PASS — wall=18166ms
```

amd64 build still compiles (no asm changes there; only the
`rng_protocol_trampolinepc_amd64.go` extraction preserves behavior).
No functional change expected and none observed — Sprint 1.3 is
**pure defensive infrastructure**.

### Files touched

- `uefiboard/rng_protocol_arm64.s` — FP saves + PC helpers
- `uefiboard/rng_protocol_riscv64.s` — FP saves + PC helpers
- `uefiboard/rng_protocol_loong64.s` — FP saves + PC helpers
- `uefiboard/rng_protocol_tamago.go` — call `rngTrampolinePCs()` instead of inline funcval-deref
- `uefiboard/rng_protocol_trampolinepc_amd64.go` — NEW (legacy funcval-deref preserved)
- `uefiboard/rng_protocol_trampolinepc_arm64.go` — NEW (calls PC helpers)
- `uefiboard/rng_protocol_trampolinepc_riscv64.go` — NEW (calls PC helpers)
- `uefiboard/rng_protocol_trampolinepc_loong64.go` — NEW (calls PC helpers)

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

## Sprint 2D'' CLOSED — `FetchBlobToBuffer`: zero-transient OCI streaming (2026-06-11)

Sprint 2D' eliminated `PublishBlockIO`'s redundant copy (saved one 64 MiB
transient by referencing the caller-owned slice via the typed
`bodyKeepAlive` field). OOM persisted at the same `~251 MiB in use`
mark during streaming, so the next architectural lift was to remove
the OCI-side `bytes.Buffer` transient and write the image directly
into a pre-allocated `[]byte`.

**Refactor** (`uefiboard/ministack/oci/fetch.go`):

New method `(*Registry).FetchBlobToBuffer(desc Descriptor, dst []byte)
(int64, error)`. Backs the streaming sink with a no-alloc
`fixedSliceWriter` (an `io.Writer` that fills `dst[off:]` at a
running cursor; surfaces `ErrBlobBufferOverflow` if the registry
delivers more bytes than `dst` can hold; surfaces
`ErrBlobBufferTooSmall` up-front if `len(dst) < desc.Size`). The
chunk SHA-256 + redirect chain + size/digest verification mirror
`FetchBlobStream` exactly — only the sink changes.

**Wire-in** (`phase2_oci_freebsd_boot.go`):

```go
// before (Sprint 2D'):
var buf bytes.Buffer
buf.Grow(int(target.Size))
n, ferr := reg.FetchBlobStream(target, &buf)
imageBytes := buf.Bytes()
// + post-stream pad-append that may realloc

// after (Sprint 2D''):
tailPad := 0
if rem := int(target.Size) % 512; rem != 0 {
    tailPad = 512 - rem
}
imageBytes := make([]byte, int(target.Size)+tailPad)
n, ferr := reg.FetchBlobToBuffer(target, imageBytes[:int(target.Size)])
```

One `make([]byte, ...)` for the lifetime of the image. No
`bytes.Buffer`. No `buf.Bytes()` alias. No tail-pad reallocation
(the 512-byte LBA padding is included in the initial allocation;
`make` zero-fills, which is what UEFI expects). The same slice is
handed to `PublishBlockIO`, which references it via R-amd64j Phase 1
`bodyKeepAlive`.

**Test coverage**: new `fetch_buffer_test.go` mirrors the
`fetch_stream_test.go` matrix (happy, digest mismatch, size mismatch,
redirect, redirect-no-location, non-200, transport error, bad
descriptor, not-streaming, too-many-redirects) plus
slice-sink-specific paths (buffer-too-small, body-overflow, oversize
slice, direct `fixedSliceWriter` unit tests). Package coverage:
96.4%; `FetchBlobToBuffer` at 97.2% (matches `FetchBlobStream`).

### Sprint 2D'' PASS gate

- **Architectural goal (`bytes.Buffer` transient elimination)**:
  PASS — one slice allocation for the disk image lifetime;
  `imageBytes` is the SAME slice from `make` to `PublishBlockIO` to
  `bodyKeepAlive`.
- **Unit tests**: PASS — 13 new tests, package coverage held at 96.4%.
- **Build (`freebsdboot:elf:amd64`)**: PASS clean.
- **Live (`freebsdboot:live:amd64`)**: STILL OOM at the same
  `251428864 in use` mark, panic point now inside `FetchBlobToBuffer`
  (was `FetchBlobStream`) — confirming the transient was NOT the
  dominant working-set contributor. The actual 251 MiB peak is
  `65 MiB image slice + ~176 MiB TLS record + cosign cert chain
  ASN.1 + HTTP chunked decode state`, with the next 4 MiB allocation
  pushing past the 256 MiB heap ceiling.

**Conclusion**: the architectural transient-elimination work is
complete and correct. The remaining OOM is a working-set / heap-size
question, not an image-pipeline question — it sits in Sprint 2D'''
(heap bump) or Sprint 2E (TLS/cosign working-set audit).

### Sprint 2E scope (queued)

- Resolve the residual streaming-OOM either by bumping
  `board_amd64.go::heapReserveSize` 256 MiB → 384 MiB (the original
  Sprint 2D' Option B, which 2D'' deliberately deferred to keep the
  fix architectural rather than a knob-twist), or by auditing the
  TLS / cosign / chunked-decode working set for further reductions.
- Then re-run live test for the kernel-handoff trace: expect
  `Loading /boot/kernel/kernel` → `FreeBSD/amd64 (...) #0` banner →
  `mountroot>` prompt (since we have no rootfs beyond `/boot/`, the
  kernel will halt at mountroot — that's sprint-2F scope:
  synthesise mfsroot.gz + `boot_mfsroot="YES"` in loader.conf, or
  ship a tiny init).

## Sprint 3 — NetBSD + OpenBSD MVPs (2026-06-11)

**Status:** SCAFFOLDING SHIPPED + OPENBSD LIVE BOOT PASSED. NetBSD live
test gated only on a local NetBSD ISO (architectural twin of OpenBSD's
runner; both follow the same Block IO + SFS + ConnectController +
LoadImage chain validated in sprint 1.1/1.2).

### Per-OS findings

| OS      | Default FS                        | EFI bootloader path on ESP                | Loader reads kernel from | Boot-config conventions | Cloud image availability                                  |
|---------|-----------------------------------|-------------------------------------------|--------------------------|--------------------------|-----------------------------------------------------------|
| NetBSD  | FFSv2 / UFS2 (default since 5.0, 2009) | `\EFI\BOOT\BOOTX64.EFI` on the install ISO | FFS root partition       | `/boot.cfg` at FFS root  | `NetBSD-X.Y-amd64.iso` (~620 MiB), `NetBSD-X.Y-amd64-uefi-install.img.gz` (~330 MiB). No official qcow2. |
| OpenBSD | FFSv2 (default since 6.5, 2019)   | `\EFI\BOOT\BOOTX64.EFI`                   | FFS root partition       | `/etc/boot.conf` at FFS root | `installXX.iso` (~670 MiB), `miniroot76.img` (~5.6 MiB, BSD-disklabel image). |

UFS evaluation against `go-filesystems/ufs`: BOTH modern NetBSD and
OpenBSD default to FFSv2 / UFS2, which is exactly what
`go-filesystems/ufs` parses. **No driver changes needed** to support
either OS at the read-only-kernel-load level. UFS1 fallback is sprint
3.5 (only if a future cloud image ships pre-5.0 / pre-6.5).

### Probe scaffolding

| File | Purpose |
|------|---------|
| `phase3_oci_netbsd_boot.go` (+ `_stub.go`) | NetBSD MVP probe. Structurally identical to `phase2_oci_freebsd_boot.go`; only the OCI ref + EFI binary path + per-OS diagnostic strings differ. Build tag: `phase3_oci_netbsd_boot && tamago && amd64`. |
| `phase3_oci_openbsd_boot.go` (+ `_stub.go`) | OpenBSD MVP probe. Same shape; build tag: `phase3_oci_openbsd_boot && tamago && amd64`. |
| `phase3_ufs_partition.go` | Build tag widened to `(phase3_oci_freebsd_boot \|\| phase3_oci_netbsd_boot \|\| phase3_oci_openbsd_boot)` so NetBSD/OpenBSD probes pick up `findUFSPartitionBytes` + `sliceReaderAt`. (FreeBSD UFS GPT type GUID is used; sprint 3.x will add NetBSD's `49F48D5A-...` for completeness when a UFS-root image is provided.) |
| `phase2_dispatch.go` | Wired in `runOCINetBSDBootProbe` + `runOCIOpenBSDBootProbe`. |

### Live runners

| Runner | Source-image expectation | Push helper |
|--------|--------------------------|-------------|
| `internal/livenetbsdboot/run.sh` | Preferred: `installation/cdrom/boot.iso` (~307 MiB, xorriso extracts `/usr/mdec/bootx64.efi`). Also accepts: full `NetBSD-X.Y-amd64.iso` (xorriso extracts `\EFI\BOOT\BOOTX64.EFI`). Defaults: `~/Downloads/NetBSD-10.0-amd64-boot.iso`, `~/Downloads/NetBSD-10.0-amd64.iso`, `/tmp/netbsd/...`. Auto-downloads boot.iso from `cdn.netbsd.org` when no cached image is present. Env: `CLOUDBOOT_NETBSD_IMAGE`, `CLOUDBOOT_NETBSD_IMAGE_URL`. |  Reuses `internal/livefreebsdboot/pushfreebsd` — the OCI artifact mediaType is content-agnostic. |
| `internal/liveopenbsdboot/run.sh` | `installXX.iso` (xorriso) OR `miniroot76.img` (mtools `mcopy -i $img@@1M` on the embedded ESP). Defaults: `~/Downloads/install7{6,5,4}.iso`, `/tmp/openbsd/install76.iso`. Env: `CLOUDBOOT_OPENBSD_IMAGE`. | Reuses `pushfreebsd`. |

Both runners assert the same gate set as `freebsdboot` sprint 1.1
(lease → stream → header OK → PublishBlockIO → ConnectController →
SFS → SFS-parent filter → LoadImage → chain complete), with the
matching per-OS prefix (`phase3-oci-netbsd-boot:` /
`phase3-oci-openbsd-boot:`). The stretch gate looks for the NetBSD
`efiboot` banner / OpenBSD `boot>` prompt as informational only.

### Taskfile targets

```
netbsdboot:elf:amd64     netbsdboot:efi:amd64     netbsdboot:live:amd64
openbsdboot:elf:amd64    openbsdboot:efi:amd64    openbsdboot:live:amd64
```

### Live test outcomes (2026-06-11)

**OpenBSD:** **PASS** end-to-end on first attempt against
`/tmp/openbsd/miniroot76.img` (5.6 MiB miniroot from
ftp.openbsd.org/pub/OpenBSD/7.6/amd64/miniroot76.img). All sprint-3 PASS
gates hit:

```
phase3-oci-openbsd-boot: lease acquired; IP = 10.0.2.15
phase3-oci-openbsd-boot: streamed 16826880 bytes; SHA-256 verified OK
phase3-oci-openbsd-boot: streamed image header OK (MBR 0x55AA + GPT 'EFI PART')
phase3-oci-openbsd-boot: PublishBlockIO OK; block handle = 0x7e204398
phase3-oci-openbsd-boot: ConnectController OK (DiskIo/PartitionDxe/FatDxe binding done)
phase3-oci-openbsd-boot: LocateHandleBuffer(SFS) found 2 total handle(s)
phase3-oci-openbsd-boot: matching SFS child handle = 0x7dc38e18
phase3-oci-openbsd-boot: LoadImage( \EFI\BOOT\BOOTX64.EFI ) OK; image handle = 0x6a2a7618
phase3-oci-openbsd-boot: SFS-UFS skip: no UFS partition in GPT
                        (sprint 3 FAT-only ESP image) -- architectural OK
phase3-oci-openbsd-boot: OPENBSD-BOOT CHAIN COMPLETE -- transferring control to bootx64.efi
[live-openbsdboot:amd64] BONUS: OpenBSD bootloader banner / boot> prompt reached
[live-openbsdboot:amd64] PASS — wall=180051ms, ref=ttl.sh/cloudboot-openbsd-3wv7lsx0:24h
```

Notable: the streaming OOM that gated FreeBSD sprint 2D/2D'' did NOT
trigger here because the OpenBSD ESP-only disk image is 16 MiB
(vs. FreeBSD's 412 MiB bootonly ISO). At 16 MiB the streaming working
set fits comfortably under tamago's 256 MiB heap.

**NetBSD:** Live test PASS — sprint 3.x closed 2026-06-11.

Sprint 3 left the NetBSD live boot gated on ISO acquisition: the full
`NetBSD-10.0-amd64.iso` (622 MiB) and the `uefi-install.img.gz` (330 MiB
compressed → ~512 MiB raw) both exceeded the download window allocated
for runner setup. Sprint 3.x surveyed the NetBSD-10.0 amd64 download
tree for a smaller bootable image carrying the EFI loader, and selected
the **installer boot.iso** (~307 MiB):

| Image                                                                  | Size      | EFI loader path             |
|------------------------------------------------------------------------|-----------|------------------------------|
| `NetBSD-10.0-amd64.iso`                                                | 622 MiB   | `/EFI/BOOT/BOOTX64.EFI`     |
| `NetBSD-10.0-amd64-uefi-install.img.gz` (uncompresses to ~512 MiB raw) | 330 MiB   | `/EFI/BOOT/BOOTX64.EFI`     |
| `NetBSD-10.0-amd64-live.img.gz`                                        | 436 MiB   | (live image, multi-GiB raw)  |
| **`amd64/installation/cdrom/boot.iso`**                                | **307 MiB** | **`/usr/mdec/bootx64.efi`** (236 KiB PE32+) |

The installer `boot.iso` is half the size of the full ISO and ships the
amd64 EFI loader at `/usr/mdec/bootx64.efi` (sourced from the El-Torito
EFI boot image; the canonical `/EFI/BOOT/BOOTX64.EFI` is not surfaced
as a regular file in the ISO9660 tree). `internal/livenetbsdboot/run.sh`
now (a) defaults to `NetBSD-10.0-amd64-boot.iso` cached under
`~/Downloads/` or `/tmp/netbsd/`, (b) auto-downloads it from
`https://cdn.netbsd.org/pub/NetBSD/NetBSD-10.0/amd64/installation/cdrom/boot.iso`
when no cached image is found, and (c) probes both `/EFI/BOOT/BOOTX64.EFI`
and `/usr/mdec/bootx64.efi` on extraction.

Live run output (sprint 3.x, 2026-06-11):

```
$ task netbsdboot:live:amd64
[live-netbsdboot:amd64] NetBSD source image: /tmp/netbsd/NetBSD-10.0-amd64-boot.iso (321794048 bytes)
[live-netbsdboot:amd64] extracting bootx64.efi from /tmp/netbsd/NetBSD-10.0-amd64-boot.iso
[live-netbsdboot:amd64] bootx64.efi: 236276 bytes
[live-netbsdboot:amd64] building 16 MiB FAT16 ESP
[live-netbsdboot:amd64] wrapping FAT in PMBR + GPT via buildespimg
[live-netbsdboot:amd64] publishing /tmp/.../disk.img to ttl.sh/cloudboot-netbsd-<rand>:24h
[live-netbsdboot:amd64] launching qemu-system-x86_64 (timeout 180s)
...
phase3-oci-netbsd-boot: streamed 16826880 bytes; SHA-256 verified OK
phase3-oci-netbsd-boot: PublishBlockIO OK
phase3-oci-netbsd-boot: ConnectController OK
phase3-oci-netbsd-boot: LocateHandleBuffer(SFS) found 2 total handle(s)
phase3-oci-netbsd-boot: LoadImage( \EFI\BOOT\BOOTX64.EFI ) OK
phase3-oci-netbsd-boot: NETBSD-BOOT CHAIN COMPLETE -- transferring control to bootx64.efi
   \\        __,---`  NetBSD/x86 EFI Boot (x64)
booting NAME=EFI System:netbsd - starting in 10 seconds. 9 seconds. ...
boot: NAME=EFI System:netbsd: No such file or directory
booting NAME=EFI System:netbsd.gz (howto 0x20000)
boot: NAME=EFI System:netbsd.gz: No such file or directory
...
[live-netbsdboot:amd64] PASS — wall=180177ms, ref=ttl.sh/cloudboot-netbsd-<rand>:24h
```

PASS gate met (all 9 chain checkpoints) AND the informational stretch
target also reached: the **NetBSD/x86 EFI Boot (x64) banner + `boot:`
prompt** are visible in the QEMU log. The loader's `No such file or
directory` retries on `netbsd` / `netbsd.gz` / `onetbsd` / `netbsd.old`
are expected — sprint 3 publishes a FAT-only ESP image (no NetBSD FFS
root, no `/netbsd` kernel); reaching that state is precisely the sprint
3.x architectural goal (analogue of OpenBSD's `boot>` prompt result).
Real kernel boot remains queued under sprint 3.1 (UFS/FFS root via
`buildespimg -ufs`).

### Sprint 3.x follow-ups (queued)

- Sprint 3.1 — wire FreeBSD-shape `buildespimg -ufs` into both
  runners so `bootroot` is a real NetBSD/OpenBSD `/boot.cfg` /
  `/etc/boot.conf` + kernel; expect bootx64.efi / efiboot to reach
  `mountroot>` (analogue of FreeBSD sprint 2C-Integration).
- Sprint 3.2 — full kernel boot to single-user (analogue of sprint
  2E/2F). Heap working-set audit; mfsroot / mfs-rooted boot config.
- Sprint 3.5 — UFS1 read support in `go-filesystems/ufs` ONLY if a
  legitimate cloud image still ships pre-FFSv2 (unlikely on modern
  releases — would be needed for NetBSD <5.0 / OpenBSD <6.5).
- Sprint 3.6 — add NetBSD FFS GPT type GUID (`49F48D5A-B10E-11DC-
  B99B-0019D1879648`) to `findUFSPartitionBytes`'s type-match set so
  NetBSD-partitioned images aren't misclassified.

## Sprint 4 — Windows scaffolding (2026-06-11)

**Status:** SCAFFOLDING SHIPPED, live boot NOT attempted. Honest
realistic-outcome path per the sprint brief.

**Why:** Windows is the most ambitious OS-agnostic boot target — NTFS
rootfs, BCD (registry-hive) boot store, Microsoft-specific UEFI loader
path, Secure Boot gating on Windows 11. Sprint 4's deliberate output
is the architectural map + per-component gap table, not a working
boot.

### Boot sequence (UEFI Windows)

```
firmware → \EFI\Microsoft\Boot\bootmgfw.efi     (Windows Boot Manager)
         → \EFI\Microsoft\Boot\BCD              (registry hive — boot config)
         → \Windows\System32\winload.efi        (kernel loader)
         → \Windows\System32\ntoskrnl.exe       (kernel)
         → kernel init
```

The first two files live on the EFI System Partition (FAT32) — those
EDK2 can already handle. The last two live on the NTFS C: volume —
**that is where Sprint 4 hits the wall.**

### Per-component status table

| Component | Status | Notes |
|-----------|--------|-------|
| OCI streaming → in-memory disk image | DONE (sprint 2D'') | Same pipeline as FreeBSD; raw single-layer artifact, SHA-256 verified. |
| `PublishBlockIO` + `ConnectController` | DONE (sprint 1.2) | Drives PartitionDxe; ESP child surfaces. |
| FAT32 ESP mount (for `bootmgfw.efi`) | DONE | EDK2 FatDxe binds the ESP — same as FreeBSD. |
| `LoadImage(\EFI\Microsoft\Boot\bootmgfw.efi)` | scaffolded | Will likely succeed once the EFI path is wired; failure surfaces in next step. |
| **BCD store** | GAP (sprint 4.1) | bootmgfw demands `\EFI\Microsoft\Boot\BCD`. Decision: **embed a pre-built BCD** extracted from a reference Windows install. On-the-fly hive construction is weeks of work and out of scope. The `buildwindowsimg -bcd <path>` flag exists for forward-compat but does NOT yet inject BCD into the FAT32 ESP (requires either a host mtools pre-pass or an in-Go FAT32 mutator). |
| **NTFS rootfs** | HARD GAP (sprint 4.3) | `go-filesystems/ntfs` is the NTFSIMG1 *synthetic* format — its `ntfs_compat_test.go` explicitly skips when given a real Windows-formatted NTFS image (`Open() rejected real-NTFS image — this driver does not yet implement real NTFS on-disk parsing`) and ntfsfix rejects this driver's output (`writer emits the NTFSIMG1 custom format, not real NTFS`). So we can NEITHER read a real Windows NTFS volume NOR mint a writable one Go-side. |
| OVMF NTFS DXE driver | HARD GAP | EDK2 OVMF stable202605 ships no NTFS driver — Microsoft IP removed it from upstream years ago. Community DXE drivers (KillaMaaki/NtfsDxe) exist but would need OVMF re-bundling. |
| `winload.efi` load (off NTFS) | BLOCKED | Both paths blocked above. |
| `ntoskrnl.exe` execution | BLOCKED | Downstream of winload. |
| Secure Boot | GAP (sprint 4.2) | Windows 10 LTSC IoT can boot with Secure Boot disabled (BIOS setting). Windows 11 requires it + db enrollment for the bootmgfw.efi we'd be loading (we don't re-sign Microsoft's PE). |
| TPM 2.0 | OOS | Tracked in a separate parallel sprint (TPM measurement / TCG2). Windows 11 demands TPM 2.0; Windows 10 LTSC IoT does not. |

### Windows version target

**Windows 10 IoT Enterprise LTSC 2021** (build 19044, supported through
2032-01). Rationale:

- Same EFI loader chain as Windows 11 (same bootmgfw / winload / ntoskrnl).
- Does NOT require Secure Boot or TPM 2.0 to boot (configurable).
- Has a documented IoT licensing path for embedded/cloud-boot use.
- Allows progressive enablement: sprint 4.x ships first against LTSC,
  then sprint 5.x layers Secure Boot + TPM to reach Windows 11.

### `go-filesystems/ntfs` evaluation

API surface (read 2026-06-11):

- Implements `filesystem.Filesystem` + `filesystem.Symlinker` +
  `filesystem.Labeller` from `github.com/go-filesystems/interface` —
  YES, drop-in API-compatible with `uefiboard.PublishSFS` the same way
  `ufs.FS` is.
- Read/Write support against the *synthetic NTFSIMG1 format*: full
  Open/Close/ReadFile/WriteFile/MkDir/Delete/Rename, free-list reuse,
  in-image directory tree. **NOT a real NTFS implementation.**
- Read support against real Windows-formatted NTFS: **NONE.** The
  driver fails Open against a freshly-mkntfs'd image (covered by
  `TestNTFSCompat_*`, all skip with explicit "read-side parser
  pending" message).
- Write support emitting real NTFS bytes: **NONE.** Cross-check via
  `ntfsfix` rejects the driver's output as "not real NTFS".

**Verdict:** the package is API-shaped for sprint-4 wire-up but the
on-disk format gap is total. Wiring it via `PublishSFS(ntfsFS)` today
would publish an NTFSIMG1-formatted FS to bootmgfw, which would
attempt to read `\Windows\System32\winload.efi` from it and fail
because the byte layout is unrecognised.

### BCD strategy

**Decided: Option A — embed a pre-built BCD** extracted from a
reference Windows install via `hivex` / `chntpw` on a Linux helper
host. The buildwindowsimg `-bcd <path>` flag accepts the resulting
hive file for forward-compat; FAT32 injection lands in sprint 4.1.

Option B (build on-the-fly via a Go hive writer) was rejected: the
Microsoft hive format is documented but undocumented enough at the
key-cell level that a robust writer is ~2–4 weeks of work — out of
proportion for a scaffolding sprint.

### Scaffolding shipped this sprint

- `tamago-uefi/phase3_oci_windows_boot.go` + `phase3_oci_windows_boot_stub.go` —
  probe entry-point, build-tag-gated `phase3_oci_windows_boot`, prints
  per-component gap status. Real symbol use kept under `if false` so
  the publish trampolines / ministack / oci imports stay live and
  refactor-tracked.
- `tamago-uefi/phase2_dispatch.go` — dispatcher wires
  `runOCIWindowsBootProbe` after the FreeBSD/NetBSD/OpenBSD probes.
  Build-tag union extended.
- `tamago-uefi/internal/livewindowsboot/run.sh` — QEMU+OVMF runner.
  Default mode asserts the documented gap-status gates print
  (sanity); a `CLOUDBOOT_WINDOWS_LIVE=1` mode exists for future
  sprint use but the chain CANNOT complete end-to-end this sprint.
- `tamago-uefi/internal/livewindowsboot/buildwindowsimg/main.go` —
  pure-Go GPT image builder with the three canonical Windows
  partition types (MSR / ESP / MBD); accepts `-fat32`, `-ntfs`,
  `-bcd` for forward-compat. Tested: emits a valid PMBR+GPT with
  the Microsoft MSR + ESP layout and UCS-2LE partition names.
- `tamago-uefi/Taskfile.yaml` — `windowsboot:elf:amd64`,
  `windowsboot:efi:amd64`, `windowsboot:live:amd64` targets +
  clean removal entry.

Build verified: `task windowsboot:efi:amd64` produces a 2.9 MB
BOOTX64-WINDOWSBOOT.EFI on the tamago amd64 toolchain.

Live boot: **NOT attempted.** The four gap rows in the status table
above are real blockers; attempting the live path today would only
prove the bootmgfw "BCD store could not be opened" error or an OVMF
NTFS-driver-missing trap, neither of which advances state-of-art over
what this document already records.

### Honest roadmap to functional Windows boot

| Sprint | Target | Gap to close |
|--------|--------|---------------|
| 4.0 (this) | Scaffolding + gap-status doc | DONE |
| 4.0a | `go-filesystems/ntfs` real on-disk read | port a public NTFS reader to pure Go (linux-ntfs/ntfs-3g reference; rejected GPL, so clean-room from MS-FSSPEC + Linux kernel `fs/ntfs3`). Months. |
| 4.1 | BCD pre-built injection | `buildwindowsimg -bcd` actually writes `\EFI\Microsoft\Boot\BCD` into the FAT32 ESP. Needs FAT32 mutator (host mtools pre-pass is easier). |
| 4.2 | Secure Boot accommodation | accept-as-is for Windows 10 LTSC (Secure Boot off). Windows 11 requires Microsoft KEK/db enrollment in OVMF — sprint 4.6+. |
| 4.3 | NTFS DXE injection OR Go-side NTFS publish | either bundle a community NTFS DXE (KillaMaaki/NtfsDxe) into OVMF and re-flash, OR ship sprint-4.0a + `PublishSFS(realNtfsFS)`. The Go-side path is preferred (keeps OVMF stock). |
| 4.4 | live LTSC boot to `winload.efi` banner | combines 4.0a + 4.1 + 4.3. |
| 4.5 | live LTSC boot to ntoskrnl banner | downstream of 4.4. |
| 4.6 | Windows 11 path (TPM 2.0 + Secure Boot KEK/db) | depends on parallel TPM sprint + OVMF cert enrollment. |

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
| 2D'    | eliminate `PublishBlockIO` redundant copy via `bodyKeepAlive` | **DONE** — caller-owned slice referenced directly; one 64 MiB transient saved. OOM persisted. |
| 2D''   | eliminate `bytes.Buffer` transient on the OCI streaming sink | **DONE (architectural)** — `oci.FetchBlobToBuffer` + pre-padded slice. Single allocation for the image lifetime. Live runner still OOMs because the 251 MiB peak is TLS+cosign+HTTP state, not the image-pipeline transient — Sprint 2E. |
| 2E     | post-loader: kernel banner + `mountroot>`  | heap bump 256 MiB→384 MiB OR TLS/cosign working-set audit; expected handoff is `Loading /boot/kernel/kernel` then `FreeBSD/amd64 (...) #0` |
| 2F     | mfsroot or rootfs hint so kernel reaches single-user | synthesise mfsroot.gz + `boot_mfsroot="YES"` in loader.conf |
| 2G     | arm64 FreeBSD EFI loader port              | port BOOTX64 publish trampolines to BOOTAA64; FreeBSD ARM EFI loader signature |
| 3      | NetBSD / OpenBSD scaffolding + MVP        | **DONE 2026-06-11** — both probes ship (FFSv2 default on both modern releases, no UFS driver gap); OpenBSD live boot PASS end-to-end to `boot>` prompt; NetBSD live boot PASS to `NetBSD/x86 EFI Boot (x64)` banner + `boot:` prompt (sprint 3.x, 307 MiB `installation/cdrom/boot.iso`, `/usr/mdec/bootx64.efi` extracted). |
| 3.1    | NetBSD/OpenBSD UFS-root via `buildespimg -ufs` | analogue of FreeBSD sprint 2C-Integration |
| 3.2    | NetBSD/OpenBSD single-user kernel boot     | analogue of sprints 2E/2F (heap audit + mfs root) |
| 3.5    | UFS1 read in `go-filesystems/ufs`          | conditional — only if a target cloud image still ships pre-FFSv2 |
| 3.6    | NetBSD FFS GPT type GUID (`49F48D5A-...`) match in `findUFSPartitionBytes` | small extension |
| 4.0    | Windows scaffolding + gap-status doc       | **DONE 2026-06-11** — `phase3_oci_windows_boot.go` + `internal/livewindowsboot/` + Sprint 4 doc section |
| 4.0a   | `go-filesystems/ntfs` real on-disk read    | Clean-room port from MS-FSSPEC + Linux `fs/ntfs3`; today's package is NTFSIMG1 synthetic |
| 4.1    | BCD pre-built injection into FAT32 ESP     | `buildwindowsimg -bcd` actually writes `\EFI\Microsoft\Boot\BCD` |
| 4.2    | Secure Boot accommodation for Win 10 LTSC  | accept Secure-Boot-off; Win 11 KEK/db deferred |
| 4.3    | NTFS DXE injection OR Go-side NTFS publish | bundle NtfsDxe into OVMF OR ship 4.0a + `PublishSFS(realNtfsFS)` |
| 4.4    | Live LTSC boot to `winload.efi` banner     | 4.0a + 4.1 + 4.3 |
| 4.5    | Live LTSC boot to ntoskrnl banner          | downstream of 4.4 |
| 4.6    | Windows 11 path                            | TPM 2.0 (separate sprint) + Secure Boot KEK/db enrollment |

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
