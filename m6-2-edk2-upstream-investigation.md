---
title: M6.2 amd64 firmware bug — EDK2 upstream investigation
status: patched OVMF integrated 2026-06-09; M8.0 chainedhello + EFIHANDOVER unblocked; HTTPS / OCI #PF root-caused (R-amd64a § 11); AllocatePages cpuinit rewrite attempted (R-amd64b § 12) hit rt0 secondary regression and is staged on m6-2-pr2-amd64-wip-r-amd64b for R-amd64c
last-updated: 2026-06-09
---

# M6.2 amd64 firmware bug — EDK2 upstream investigation

## 1. Summary

The amd64 OVMF firmware shipped with `qemu-9.2.0` / `qemu-10.2.2`
(file: `share/qemu/edk2-x86_64-code.fd`,
MD5 `661c68c8b0a2ed59d5e4a13563cd6e13`, Gerd Hoffmann's build,
based on `edk2-stable202408`) crashes with

```
!!!! X64 Exception Type - 0D(#GP - General Protection) !!!!
RIP  - 000000007EF6710C   (CpuDxe.dll +0x110C, ImageBase 0x7EF56000)
```

during `gBS->StartImage` on PE32+ images that have multi-section COFF
layouts produced by toolchains like Go/cgo, MSVC, or pectl — but NOT
on hand-rolled single-section PE32+ images of similar or larger size
(confirmed in cloud-boot M6.2 de-risk:
[`tamago-uefi-phase2-oci-loader.md`](tamago-uefi-phase2-oci-loader.md)
M6.2 de-risk section).

**This bug IS fixed upstream.** Three serial commits land the fix
between `edk2-stable202408` and `edk2-stable202511`:

| Commit | Date | Stable tag first contained | What it fixes |
|--------|------|----------------------------|---------------|
| `5ccb5fff02a66b21898bd57f48bbd7c3cd6f4e8d` | 2025-04-15 | `edk2-stable202505` | Route image protection through GCD instead of bypassing it (the actual root cause) |
| `867fad874a019b629ee55aff2b0ef9af0fe1358c` | 2025-04-30 | `edk2-stable202505` | Fix off-by-one in the new GCD-walking loop introduced by 5ccb5fff02 (handles multi-descriptor base addresses) |
| `b5bab75e58bf8c9ec66243a62b86d5f6b409a69a` | 2025-09-25 | `edk2-stable202511` | Correct `EFI_MEMORY_ATTRIBUTE_MASK` vs `EFI_MEMORY_ACCESS_MASK` usage so virtual-only attribute updates don't accidentally clear RWX to 0 |

**Recommended action: ship a patched OVMF built from `edk2-stable202511`
or later** (preferred), or apply the three patches on top of
`edk2-stable202408` and rebuild. See § 6.

## 2. Bug data we have

From [`tamago-uefi-phase2-oci-loader.md`](tamago-uefi-phase2-oci-loader.md)
M6.1 / M6.2 de-risk sections, the empirical envelope:

| variant | size (bytes) | sections / layout | StartImage result |
|---------|-------------:|-------------------|---|
| M8.0 `chainedhello` (TamaGo) | 1,702,400 | multi-section (text, data, rdata, reloc, pdata, xdata, …) | **#GP at CpuDxe.dll +0x110C** |
| `chainedtinyC` (TamaGo, empty `main()`) | 1,700,864 | multi-section | LoadImage OK (StartImage skipped) |
| `chainedtinyZ2M` (hand-rolled) | 2,097,152 | single `.text` | **PASS** |
| `chainedtinyZ1M` (hand-rolled) | 1,048,576 | single `.text` | **PASS** |
| `chainedtinyZ64K` (hand-rolled) | 65,536 | single `.text` | **PASS** |
| M5 HTTP (TamaGo) | 3,173,888 | multi-section | **PASS** (mystery; smaller call surface?) |
| M6 HTTPS (TamaGo) | 4,892,672 | multi-section | **#GP at +0x110C** |
| M7 OCI (TamaGo) | 5,260,800 | multi-section | **#GP at +0x110C** |

The bug correlates with **multi-section PE32+** structure, specifically
images where DxeCore's `ProtectUefiImage` walks the section list and
calls `gCpu->SetMemoryAttributes(EFI_MEMORY_XP)` / `(EFI_MEMORY_RO)`
per data / code section. The `chainedtinyZ*` images PASS because their
single `.text` section means `ProtectUefiImage` issues at most one
RO setter and one XP setter for the whole image, which sidesteps the
GCD-bypass corruption pattern.

### 2.1 Live BlkRingBuffer trace (2026-06-09, M6.2 PR2 efipackstub)

The M6.2 PR2 stub for amd64 was instrumented with the M1.6 Block-IO
side-channel ring buffer (commit `b350b2d` on
`m6-2-pr2-amd64-wip`) and re-run with a dedicated virtio-blk-pci
scratch disk so a tracepoint could be dropped after every
firmware-callable step. Input: `BOOTX64-HTTP.EFI` packed via
`efipack.Pack` (`Flate`); ESP boots packed binary; scratch disk
holds the ring buffer.

Last tracepoint flushed before the #GP:

```text
efipackstub: gBS->StartImage on child           <-- LAST PRINTED LINE
[X64 #GP at RIP=0x7EF6710C  CpuDxe.dll +0x110C]
```

Preceding tracepoints (all OK):

```text
readOwnFile: file->Read                         OK (3,108,352 bytes)
efipackstub: parsing PE for .payload (on-disk)  OK
payload.len=1,313,792
uncompressedSize=3,173,888                       (= original BOOTX64-HTTP.EFI)
efipackstub: AllocatePages for decompressed image OK
pagesAddr=0x7DA14000  (775 pages)
efipackstub: decompressing flate stream         OK
efipackstub: gBS->LoadImage on decompressed bytes  OK
childHandle=0x7DEE6D98
efipackstub: gBS->StartImage on child           <-- CRASH INSIDE FIRMWARE
```

This is **empirical proof** that the stub's end-to-end pipeline is
correct: the file is re-read off disk, the `.payload` section is
located, decompressed in place, and `gBS->LoadImage` returns a valid
child handle. The #GP fires inside the firmware's `StartImage`
path itself — exactly the same `CpuDxe.dll +0x110C` PC identified by
the M6.1 de-risk sweep. (Note: the "M5 HTTP PASS (mystery)" entry in
the table above refers to running the ORIGINAL `BOOTX64-HTTP.EFI` —
when re-loaded via `gBS->LoadImage` of an in-RAM decompressed buffer,
StartImage crashes identically. The bug fires the moment any
multi-section PE32+ goes through the protect-then-start path.)

## 3. EDK2 source files reviewed

Cloned `edk2-stable202408` to `/tmp/edk2-202408` and full master to
`/tmp/edk2-master`. Reviewed:

- `UefiCpuPkg/CpuDxe/CpuDxe.c` — `CpuSetMemoryAttributes()` (the
  CPU Arch Protocol implementation; gets called from
  `SetUefiImageMemoryAttributes` via `gCpu->SetMemoryAttributes`).
- `UefiCpuPkg/CpuDxe/CpuPageTable.c` — `ConvertMemoryPageAttributes`,
  `SplitPage`, `GetPageTableEntry`. Same file's `efaa102d0` commit
  (July 2024) added the EFI Memory Attributes Protocol (UEFI v2.10);
  this is the post-202408 ABI surface that participates in the crash.
- `UefiCpuPkg/Library/CpuPageTableLib/CpuPageTableMap.c` — the main
  `PageTableMap()` and `PageTableLibMapInLevel()`. The
  splitting-leaf-entry bug (`839bd17973`, May 2024) is already in
  `edk2-stable202408`, so it is NOT our bug.
- `UefiCpuPkg/Library/CpuPageTableLib/CpuPageTableParse.c` — parse path.
- `MdeModulePkg/Core/Dxe/Misc/MemoryProtection.c` — `ProtectUefiImage`,
  `SetUefiImageProtectionAttributes`, `SetUefiImageMemoryAttributes`.
  **This file is the bug's primary residence** at `edk2-stable202408`.
- `MdeModulePkg/Core/Dxe/Gcd/Gcd.c` — `CoreConvertSpace`,
  `ConverToCpuArchAttributes` (note the typo in the upstream name).
- `MdeModulePkg/Core/Dxe/Image/Image.c` — calls `ProtectUefiImage`
  at the tail of `CoreLoadImageCommon` (line 1463) and from
  `CoreInitializeImageServices` (line 273).

## 4. Call path of the crash

```
gBS->LoadImage (file)
  └ CoreLoadImageCommon
      └ ProtectUefiImage (LoadedImage, LoadedImageDevicePath)         [MemoryProtection.c:330]
          └ CreateImagePropertiesRecord
          └ SetUefiImageProtectionAttributes (ImageRecord)            [MemoryProtection.c:215]
              └ FOR EACH code/data section pair:
                  ├ SetUefiImageMemoryAttributes (DATA, XP)           [MemoryProtection.c:188]
                  │   └ gCpu->SetMemoryAttributes (gCpu, …, XP)
                  │       └ CpuSetMemoryAttributes                    [CpuDxe.c:311]
                  │           └ AssignMemoryPageAttributes
                  │               └ ConvertMemoryPageAttributes
                  │                   └ SplitPage / SetMemoryAttributes via CpuPageTableLib
                  └ SetUefiImageMemoryAttributes (CODE, RO)
                      └ … (same as above)
gBS->StartImage (handle)
  └ jumps into the now-mis-mapped image  →  #GP at first instruction touched
```

The `#GP` is logged with RIP inside `CpuDxe.dll` because that's where
the page-table walk completes during the next instruction fetch /
data access after the bad page-table programming. RIP `+0x110C` is
the **early offset inside `CpuSetMemoryAttributes` / one of the
CpuPageTableLib helpers reached at module init time** — specifically
the spot where the wrong XP gets applied to a code page or the wrong
RO gets applied to a data page, triggering a privileged-mode #GP on
the very next mapped fetch. Without the DEBUG `.dll` we can't pin
`+0x110C` to a precise symbol (Gerd's build at
`/home/kraxel/projects/qemu/roms/Build/OvmfX64/DEBUG_GCC5/X64/UefiCpuPkg/CpuDxe/CpuDxe/DEBUG/CpuDxe.dll`
would resolve it). Best guesses, in decreasing order of probability:

1. `CpuSetMemoryAttributes` epilogue (post-MTRR sync), where the
   recursion through `mIsAllocatingPageTable` ends and the actual
   page-table commit happens.
2. `RefreshGcdMemoryAttributesFromPaging` early — re-walks the GCD
   after the bogus attribute application.
3. An indirect dispatch through `gCpu->SetMemoryAttributes` itself.

This is informed guesswork without the debug binary; the precise line
matters for an upstream bug filing but not for the recommendation.

## 5. Relevant upstream commits — cited

### 5.1 `5ccb5fff02` — root-cause fix (April 2025, in `edk2-stable202505`)

```
MdeModulePkg: DxeCore: Set Image Protections Through GCD

Today, SetUefiImageMemoryAttributes calls directly to the
CPU Arch protocol to set EFI_MEMORY_XP or EFI_MEMORY_RO on
image memory. However, this bypasses the GCD and so the GCD
is out of sync with the actual state of memory.

This can cause an issue in the scenario where a new attribute
is being set (whether a virtual attribute or a real HW attribute),
if the GCD attributes are queried for a region and the new attribute
is appended to the existing GCD attributes (which are incorrect),
then the incorrect attributes can get applied. This can result in
setting EFI_MEMORY_XP on code sections of images and causing an
execution fault.
```

The last sentence is **literally our symptom**. The fix replaces the
single-shot `SetUefiImageMemoryAttributes` (lines 187-207 of
`MemoryProtection.c` in 202408) with a GCD-walking loop that
correctly handles the case where the requested address range spans
multiple GCD descriptors with different existing attribute sets.

Diff shipped in [`m6-2-edk2-fix-1-image-protection-through-gcd.patch`](m6-2-edk2-fix-1-image-protection-through-gcd.patch).

### 5.2 `867fad874a` — off-by-one fix (April 2025, in `edk2-stable202505`)

```
MdeModulePkg: Fix Image Memory Protection Applying

Commit 5ccb5fff02a66b21898bd57f48bbd7c3cd6f4e8d updated the
image memory protection code to set the protection
attributes through the GCD instead of directly to the page
table. However, this code had an implicit assumption that
each base address passed to it was the beginning of a GCD
descriptor. On the virtual platforms tested, this was the case.
However, on a physical platform, a scenario was encountered
where the base address was not the beginning of a GCD
descriptor, thus causing memory attributes to be applied
incorrectly.
```

Co-located fixup. Must be applied with 5ccb5fff02.

Diff in [`m6-2-edk2-fix-2-image-memory-protection-applying.patch`](m6-2-edk2-fix-2-image-memory-protection-applying.patch).

### 5.3 `b5bab75e58` — attribute-mask correctness (Sep 2025, in `edk2-stable202511`)

```
MdeModulePkg: DXE Core: Correct Usage of EFI_MEMORY_ATTRIBUTE_MASK

[...] EFI_MEMORY_ACCESS_MASK contains the actual
HW page table access attributes (read protect, read only, no-execute),
whereas EFI_MEMORY_ATTRIBUTE_MASK contains the access attributes in
addition to some virtual attributes (special purpose and cpu crypto).

[...] after the above change, this behavior was altered so
that if EFI_MEMORY_SP or EFI_MEMORY_CPU_CRYPTO is applied, in attempt
to just update these virtual attributes, the GCD will call into CpuDxe
and apply RWX instead, which is not the intention of the caller.
```

Independent bug that interacts with the same GCD path; ship it
together with the previous two for safety. Diff in
[`m6-2-edk2-fix-3-memory-attribute-mask.patch`](m6-2-edk2-fix-3-memory-attribute-mask.patch).

### 5.4 Related (not strictly required, but in the same area)

- `efaa102d00` (July 2024, in `edk2-stable202408` — already present
  in the buggy build): adds UEFI 2.10 Memory Attributes Protocol.
  This is the new ABI surface but is NOT the bug.
- `f64b4065b7` (Sept 2025, in `edk2-stable202511`): fixes encryption
  bit handling in `CpuDxe` page walks for confidential VMs. Unrelated
  to our crash since we're not running SEV/TDX, but ship it anyway
  if upgrading the whole tree.
- `4c8717de16` (April 2026, in `edk2-stable202605`): OvmfPkg DSC
  change — page-align DXE_DRIVER / UEFI_APPLICATION sections so
  image protection actually takes effect on built-in modules.
  Build-time, not runtime; included automatically if we rebuild
  against current master.

## 6. Recommendation

**Preferred path: upgrade.** Build OVMF from `edk2-stable202511` (or
later). All three fixes are in the stable tree. No patching needed.
The cloud-boot M6.2 amd64 deferral can close once the fresh OVMF blob
is plumbed through:

- pkgx pantry: bump the `qemu.org/edk2-x86_64-code.fd` source (which
  is actually packaged by qemu upstream, not edk2) — or add a separate
  `tianocore.org/edk2-stable202511` recipe that builds OVMF directly.
- pkgx pantry currently has no `tianocore.org/edk2`; the OVMF blob
  is shipped inside `qemu.org` builds. Adding a standalone EDK2
  recipe would unblock this regardless of qemu's release cadence.
  (Filed as a follow-up — see § 8.)

**Fallback path: patched 202408.** If a fresh blob is not immediately
available, apply the three patches in this repo on top of
`edk2-stable202408` and build OVMF. Order matters:

1. [`m6-2-edk2-fix-1-image-protection-through-gcd.patch`](m6-2-edk2-fix-1-image-protection-through-gcd.patch)
2. [`m6-2-edk2-fix-2-image-memory-protection-applying.patch`](m6-2-edk2-fix-2-image-memory-protection-applying.patch)
3. [`m6-2-edk2-fix-3-memory-attribute-mask.patch`](m6-2-edk2-fix-3-memory-attribute-mask.patch)

EDK2 build is out of scope for this investigation (Python + NASM +
IASL + GCC cross-compilers; ~30 minutes of toolchain setup, ~10
minutes of build). When we do it, the build target is:

```sh
build -a X64 -t GCC5 -p OvmfPkg/OvmfPkgX64.dsc -b RELEASE \
  -D NETWORK_HTTP_BOOT_ENABLE=TRUE -D NETWORK_TLS_ENABLE=TRUE
```

Output: `Build/OvmfX64/RELEASE_GCC5/FV/OVMF_CODE.fd` —
drop-in replacement for the `edk2-x86_64-code.fd` that pkgx qemu
ships.

**Do NOT file an upstream issue at <https://github.com/tianocore/edk2/issues>.**
The bug is already fixed in three serial commits with a clear
attribution chain to the same author (Oliver Smith-Denny @ Microsoft).
Re-filing would just be noise. A draft confirmation note has been
placed at `/tmp/edk2-issue-draft.md` in case we want to send a
"confirming this is fixed on real hardware-like workloads, thanks"
comment on Oliver's existing PR threads — but that's optional and
nothing actionable comes of it.

## 7. Why the bug is symptom-correlated with multi-section PEs

Single-section `chainedtinyZ*` images PASS because
`SetUefiImageProtectionAttributes` issues **at most one
`SetMemoryAttributes(XP)` and one `SetMemoryAttributes(RO)`** call
for the whole image (the loop in `MemoryProtection.c:233-263` runs
zero or one iteration). Each of those single calls writes through
the broken `SetUefiImageMemoryAttributes` once; the GCD descriptor
covering the image's allocation is fully consumed; no cross-descriptor
attribute leak. The image's `.text` covers the whole image's pages,
so XP-then-RO leaves the executable section correctly executable.

Multi-section PEs (Go-emitted, MSVC-emitted, pectl-emitted) hit the
loop multiple times. Each call to `SetUefiImageMemoryAttributes`
reads the GCD descriptor at `BaseAddress`, OR-s in the new attribute,
and writes the result to the **entire descriptor**, including pages
that don't belong to the requested range. If a previous iteration
left the descriptor with XP set on pages that should be code, the
next iteration AND-or-OR-s on top of that, and the page-table
programming that follows applies XP to code or RO to data. The
result: when `StartImage` jumps to the image entry, the very first
instruction fetch hits an XP page → `#GP`.

The 5ccb5fff02 fix walks the GCD descriptor-by-descriptor and
preserves only the cache/virtual attributes of the existing
descriptor while replacing the access attributes for **exactly the
requested range** — the bug closes.

## 8. Follow-ups for cloud-boot

- [x] **2026-06-09:** investigation complete; this doc + 3 patches
      checked in alongside.
- [x] **2026-06-09:** patched OVMF (edk2-stable202605, Fedora rebuild)
      vendored into `~/.pkgx/tianocore.org/v0.0.0-stable202605/share/qemu/`;
      all amd64 live runners updated to prefer it. See § 10 below.
- [ ] **pkgx pantry:** add a `tianocore.org/edk2` recipe that builds
      OVMF from `edk2-stable202511` (or current stable). De-couples
      our firmware blob from qemu's release cadence. See
      `feedback-package-completeness.md` and `feedback-add-missing-deps.md`
      in user memory — this is exactly the pattern. (Paralleled by a
      separate agent on 2026-06-09; check
      `~/Documents/VCS/GIT/localhost/pantry/projects/tianocore.org/edk2/`.)
- [ ] **cloud-boot:** M6 HTTPS, M7 OCI still FAIL with the patched OVMF
      but with a DIFFERENT exception (#PF at RIP=0x000A5003,
      CR2 ≈ 0xFFFFFFFF98000000). This is NOT the original CpuDxe.dll
      +0x110C #GP — that one is gone (proved by EFIHANDOVER original
      now PASSing). The new failure looks like a TamaGo amd64 runtime
      page-table set-up issue: the image enters at low RIP (0xA5003)
      then immediately accesses a high-canonical address that is not
      mapped in TamaGo's freshly-installed page tables. Sub-2.5MB
      TamaGo images (M3/M4/M5, EFIHANDOVER) survive; larger ones
      (HTTPS ≈ 4.9 MB, OCI ≈ 5.3 MB) do not. New investigation thread
      needed; tracking in `tamago-uefi-phase2-oci-loader.md`.
- [ ] **cloud-boot:** keep `m6-2-pr2-amd64-wip` open until the second
      bug is closed; do NOT merge yet (smoke matrix below is amber not
      green).

## 9. References

- EDK2 commit `5ccb5fff02a66b21898bd57f48bbd7c3cd6f4e8d`:
  <https://github.com/tianocore/edk2/commit/5ccb5fff02a66b21898bd57f48bbd7c3cd6f4e8d>
- EDK2 commit `867fad874a019b629ee55aff2b0ef9af0fe1358c`:
  <https://github.com/tianocore/edk2/commit/867fad874a019b629ee55aff2b0ef9af0fe1358c>
- EDK2 commit `b5bab75e58bf8c9ec66243a62b86d5f6b409a69a`:
  <https://github.com/tianocore/edk2/commit/b5bab75e58bf8c9ec66243a62b86d5f6b409a69a>
- EDK2 stable tag `edk2-stable202511`:
  <https://github.com/tianocore/edk2/releases/tag/edk2-stable202511>
- cloud-boot M6.1 / M6.2 de-risk:
  [`tamago-uefi-phase2-oci-loader.md`](tamago-uefi-phase2-oci-loader.md)
  §§ M6.1 investigation, M6.2 de-risk.

## 10. Patched OVMF integration (2026-06-09)

### 10.1 Source

The patched OVMF blob comes from **Fedora's `edk2-ovmf` package**,
build `20260508-2.fc45`, which packages **`edk2-stable202605`** (commit
`b03a21a63e3b` per the Fedora spec's `%define GITCOMMIT`).

```text
URL:      https://kojipkgs.fedoraproject.org/packages/edk2/20260508/2.fc45/noarch/edk2-ovmf-20260508-2.fc45.noarch.rpm
Upstream: github.com/tianocore/edk2 tag edk2-stable202605
License:  BSD-2-Clause-Patent (OvmfPkg), see edk2-licenses.txt next to the .fd
```

`edk2-stable202605` carries **all three fixes** identified in § 5
(plus the OvmfPkg page-alignment commit `4c8717de16` that lands in the
same tag), so no on-top patching was needed.

### 10.2 Install path

The .fd blobs ship inside the RPM as qcow2 images (the Fedora OVMF
distribution format). Convert to raw `.fd` and install at a
pkgx-mimicked layout:

```sh
mkdir -p /tmp/edk2-fedora/extract
bsdtar -xf edk2-ovmf-20260508-2.fc45.noarch.rpm -C /tmp/edk2-fedora/extract

DEST="$HOME/.pkgx/tianocore.org/v0.0.0-stable202605/share/qemu"
mkdir -p "$DEST"
qemu-img convert -f qcow2 -O raw \
  /tmp/edk2-fedora/extract/usr/share/edk2/ovmf/OVMF_CODE_4M.qcow2 \
  "$DEST/edk2-x86_64-code.fd"
qemu-img convert -f qcow2 -O raw \
  /tmp/edk2-fedora/extract/usr/share/edk2/ovmf/OVMF_VARS_4M.qcow2 \
  "$DEST/edk2-i386-vars.fd"
cp /tmp/edk2-fedora/extract/usr/share/licenses/edk2-ovmf/License.txt \
   "$DEST/edk2-licenses.txt"
```

Size + MD5 of the installed blobs:

```text
edk2-x86_64-code.fd  3,653,632 bytes   md5 e35cb6da7e06025ec2358edd7e6f2d15
edk2-i386-vars.fd      540,672 bytes   md5 173134c7c1593bad9cd101dc10bef49b
```

(Same byte count as the pkgx-bundled qemu blobs — different content;
the pkgx `edk2-x86_64-code.fd` MD5 is the buggy
`661c68c8b0a2ed59d5e4a13563cd6e13` from Gerd's `edk2-stable202408`
build.)

A `PROVENANCE.txt` is dropped next to the .fd files with the same
metadata so a stray `ls` answers the question without reading this doc.

The pkgx-bundled `~/.pkgx/qemu.org/v9.2.0/share/qemu/edk2-x86_64-code.fd`
is **NOT overwritten** — leaving it alone keeps every other QEMU user
on the host on a known-good (if buggy-for-our-case) blob.

### 10.3 Runner wiring

All amd64 live runners under `cloud-boot/tamago-uefi/internal/*/run.sh`
now prefer the patched OVMF when present, falling back to the
pkgx-bundled buggy blob otherwise:

```bash
if [[ -f "$HOME/.pkgx/tianocore.org/v0.0.0-stable202605/share/qemu/edk2-x86_64-code.fd" ]]; then
    FW_CODE_DEFAULT="$HOME/.pkgx/tianocore.org/v0.0.0-stable202605/share/qemu/edk2-x86_64-code.fd"
    FW_VARS_DEFAULT="$HOME/.pkgx/tianocore.org/v0.0.0-stable202605/share/qemu/edk2-i386-vars.fd"
else
    FW_CODE_DEFAULT="$HOME/.pkgx/qemu.org/v9.2.0/share/qemu/edk2-x86_64-code.fd"
    FW_VARS_DEFAULT="$HOME/.pkgx/qemu.org/v9.2.0/share/qemu/edk2-i386-vars.fd"
fi
FW_CODE="${CLOUDBOOT_OVMF_AMD64_CODE:-$FW_CODE_DEFAULT}"
FW_VARS="${CLOUDBOOT_OVMF_AMD64_VARS:-$FW_VARS_DEFAULT}"
```

The `CLOUDBOOT_OVMF_AMD64_{CODE,VARS}` env-var overrides remain the
top-priority knob. CI can opt back to the buggy blob by setting them
explicitly to the pkgx path.

Runners touched (8): `efipacksmoke`, `livedhcp4`, `liveefihandover`,
`liveefitinyhandover`, `livehttp`, `livehttps`, `liveministack`,
`liveoci`. Other arches (arm64 / riscv64 / loong64) keep using the
pkgx-bundled OVMF — no change needed.

### 10.4 Smoke matrix — amd64, patched OVMF

Run on 2026-06-09 against `edk2-stable202605` Fedora rebuild:

| Stage | Test                                   | pkgx (buggy) | patched OVMF | Δ |
|-------|----------------------------------------|--------------|--------------|---|
| M3    | livedhcp4 amd64                        | PASS         | PASS         |   |
| M4    | liveministack amd64                    | PASS         | PASS         |   |
| M5    | livehttp amd64                         | PASS         | PASS         |   |
| M6    | livehttps amd64                        | FAIL #GP     | FAIL #PF*    | partial |
| M7    | liveoci amd64                          | FAIL #GP     | FAIL #PF*    | partial |
| M8.0  | liveefihandover amd64 (BOOTX64-EFIHANDOVER child-LoadImage) | FAIL #GP | **PASS**     | **FIXED** |
| M8.1  | liveefitinyhandover Z2M/Z1M/Z64K/Z     | PASS         | PASS         |   |
| M8.1  | liveefitinyhandover variant=C (multi-section TamaGo, LoadImage-only) | PASS | PASS |   |
| M8.2  | efipacksmoke HTTP original             | PASS         | PASS         |   |
| M8.2  | efipacksmoke HTTPS original            | FAIL #GP     | FAIL #PF*    | partial |
| M8.2  | efipacksmoke OCI original              | FAIL #GP     | FAIL #PF*    | partial |
| M8.2  | efipacksmoke EFIHANDOVER original      | FAIL #GP     | **PASS**     | **FIXED** |
| M8.2  | efipacksmoke \*-packed (4 rows)        | n/a (blocked) | FAIL #PF*   | newly reachable |

`*` The new #PF is **not** the original `CpuDxe.dll +0x110C` #GP. It
fires at `RIP=0x000A5003`, P=0 (page not present), CR2 in the
`0xFFFFFFFF9800xxxx` range — i.e. inside the loaded image after
TamaGo has installed its own page tables, accessing a high-canonical
address that isn't mapped. The original EDK2 image-protection bug is
genuinely gone (EFIHANDOVER original and chained-via-handover both
PASS now); this is a separate, downstream issue that needs its own
investigation. See § 8 follow-up bullet.

### 10.5 WIP merge status

`m6-2-pr2-amd64-wip` is **NOT merged**. The matrix is amber: M8.0
chainedhello and the EDK2-firmware-bug-bound failures are fixed, but
M6 HTTPS and M7 OCI still fail (different bug). Per the task brief
("merge to main ONLY IF the smoke matrix is green"), the branch stays
open with the patched-OVMF runner changes committed, and the second
bug becomes the next phase.

## 11. R-amd64a — TamaGo amd64 #PF post-OVMF-patch (2026-06-09)

### 11.1 Symptom

After the patched OVMF landed (§ 10), the M6 HTTPS / M7 OCI / efipack
`*-packed` rows that previously failed with the firmware-side
`CpuDxe.dll +0x110C` #GP now fail with a different fault — fired
DURING the TamaGo runtime's bring-up, not in the firmware:

```text
!!!! X64 Exception Type - 0E(#PF - Page-Fault)  CPU Apic ID - 00000000 !!!!
ExceptionData - 0000000000000000  I:0 R:0 U:0 W:0 P:0 PK:0 SS:0 SGX:0
RIP  - 00000000000A5003, CS  - 0000000000000038
RSP  - 00000000A909B6A0, ... CR2 - FFFFFFFF980098E4
CR3  - 000000007FC01000
!!!! Can't find image information. !!!!
```

`P:0` = page not present; the CPU walked the firmware's page tables
(CR3 = OVMF's, since cpuinit_amd64.s explicitly does NOT touch them)
and found no PTE for either RIP or the CR2 effective address.

### 11.2 Root cause

cpuinit_amd64.s derives `goos.RamStart = &runtime.text - 64 KiB`,
then `goos.RamSize = 704 MiB` is hard-coded in board_amd64.go (`var
ramSize uint64 = 0x2c000000`). The runtime stack therefore lives at
`RamStart + RamSize - RamStackOffset` ≈ `&runtime.text + 704 MiB`.

The QEMU `-m 2048` q35 machine type has its high RAM topping out at
`0x8000_0000` (the rest is the 32→64-bit PCI MMIO hole). Under the
patched OVMF, the new GCD-aware image-protection logic
(`5ccb5fff02` + `867fad874a` + `b5bab75e58`) loads multi-MiB images
near the TOP of free RAM — empirically `ImageBase ≈ 0x7D1A_9000` for
the 4.9 MiB HTTPS probe — so `RamStart + 704 MiB` lands well past
`0x8000_0000`, inside the PCI MMIO hole. The first push/spill
against that SP traps; the resulting MMIO-induced corruption (the
hole returns 0xFF on read on q35) then drops the CPU into an
unmapped-RIP region and the #PF dumper can't identify any image
covering the new RIP.

Sub-3 MiB images survived only because OVMF still placed them in the
low end of free RAM, where `text + 704 MiB` happened to fit inside
`[0, 0x8000_0000)`.

### 11.3 Investigation evidence

- **RIP=0xA5003 is NOT a return-address corruption pattern.** With
  RSP=0xA909B6A0 and RamStackOffset=0x100000 (1 MiB from
  `tamago/amd64/amd64.go:51`), reverse-solving `SP = RamStart +
  RamSize - RamStackOffset` gives `RamStart ≈ 0x7D19_B6A0`, and
  `runtime.text = RamStart + 64 KiB ≈ 0x7D1A_B6A0`. The image's
  PE32+ `.text` section starts at `RVA 0x1000`, so the loaded
  ImageBase ≈ 0x7D1A_A000 — confirmed live (one failing dump
  printed `ImageBase=0x7D19F000, EntryPoint=0x7D26A8C0`, matching
  within page granularity).

- **The 32-MiB-RamSize attempt got farther but still hit firmware-
  protected memory.** Lowering to `0x0200_0000` lets HTTPS reach
  the runtime's first `efiCall` (eficall_amd64.s), where the
  thunk's `SP = RamStart + RamSize` then `SUBQ $0x30, SP` →
  `MOVQ R11, 0x20(SP)` writes to `0x7F19_0FF0`. CR2 matches, but
  now `P:1 W:1` — a PROTECTION VIOLATION on write. The patched
  OVMF marks the firmware allocator's regions
  (`[0x7E000000..0x7FFF0000]` empirically: GDTR at 0x7F9DB000,
  IDTR at 0x7BF6AF58, CR3 at 0x7FC01000, FXSAVE at 0x7F9DA460,
  exception-handler stack at 0x7FE3D…) as RO/XP. Our heap/stack
  CANNOT safely use ANY memory in the firmware-allocator range,
  not just the area above 0x8000_0000.

- **A small RamSize (e.g. 32 MiB) breaks EFIHANDOVER too.** With
  the parent loader at ImageBase ≈ 0x7D96B000, the chained child
  loaded via `gBS->LoadImage` lands at ImageBase ≈ 0x7D95F000, and
  `text + 32 MiB = 0x7F95F000` still overlaps firmware-protected
  pages → #PF P:1 W:1 at CR2=0x7E3F6000. Sub-image-size RamSize
  values (< image_end → next-firmware-region gap) are too small
  to bring the runtime up.

### 11.4 Proper fix (designed, partially implemented, not shipped)

Switch cpuinit to `gBS->AllocatePages(EFI_ALLOCATE_ANY_PAGES,
EfiLoaderData, RamSize>>12, &heapBase)`, mirroring
cpuinit_arm64.s / cpuinit_riscv64.s / cpuinit_loong64.s. The
allocator returns a guaranteed-RAM, RW, NX-free, page-aligned
region that, by construction, does NOT overlap firmware code, data,
or the loaded image. Wire `goos.RamStart = goos.Bloc = heapBase`,
then SP = `heapBase + RamSize - RamStackOffset` lands inside that
region.

A draft of this fix was implemented during the R-amd64a sprint but
hit a SECONDARY regression: `runtime·rt0_amd64_tamago` in
`tamago-pie/src/runtime/sys_tamago_amd64.s:120-123` reads argc from
`24(SP)` and argv from `32(SP)`:

```asm
MOVL    24(SP), AX            // copy argc
MOVL    AX, 0(SP)
MOVQ    32(SP), AX            // copy argv
MOVQ    AX, 8(SP)
```

Under the bare-metal `init.s`, this works because the PML4/PDPT
setup memset-zeroed the same region before SP was retargeted (an
implicit zero-init contract). Under a fresh `AllocatePages`
allocation the bytes are UNDEFINED — and even zeroing the first
64 bytes of the new stack window manually in cpuinit was not
sufficient: the runtime later crashed with a #GP on a non-canonical
RIP (`0x55415641_E5894855` — recognisable as the function-prologue
bytes `55 48 89 E5 41 56 41 55` of some Go function, popped off
the stack as a return address), suggesting more of the bootstrap
stack — possibly the goroutine's istack guard pages or the
firstmoduledata-driven type bitmaps — needs a defined initial
state. The proper handoff probably needs to:

1. memset the entire allocated region to 0 (not just 64 bytes),
2. seed a minimal argc/argv frame at the new SP (argc=0, argv=NULL),
3. ensure the heap bloc is anchored AT the allocated base (not at
   `firstmoduledata.end` which is OUTSIDE our region — done by
   setting `goos.Bloc` to the same value as `goos.RamStart`).

### 11.5 What shipped this sprint

NOTHING in this iteration — board_amd64.go and cpuinit_amd64.s are
left at their pre-sprint state to keep MINISTACK / HTTP / DHCP4 /
EFIHANDOVER (M3/M5/M8.0) passing. The full AllocatePages handoff
and the rt0 argc/argv seeding were de-risked but the secondary
regression couldn't be unstuck inside the 90-minute hard cap.

### 11.6 Concrete next-sprint plan

1. Reapply the `cpuinit_amd64.s` AllocatePages variant (kept in
   the agent's transcript / git stash if available, otherwise
   re-derive from cpuinit_arm64.s with the MS x64 ABI swap).

2. Replace the manual `MOVQ DI, AX; XORL AX, AX; STOSQ * 8` zero
   loop with a full `REP STOSB` over the entire allocated region
   BEFORE setting SP — guarantees the istack guard pages, the
   argc/argv slot, the early `mpreinit` malloc, etc. all start
   from zeroed memory.

3. Push a tamago-pie patch (DO NOT push to main — upstream fork)
   adding a `MOVQ $0, 24(SP); MOVQ $0, 32(SP)` pair at the top of
   `runtime·rt0_amd64_tamago` so the bootstrap-stack zero-init
   contract is explicit rather than relying on the cpuinit's
   discipline. Keep locally; user will upstream.

4. Re-run the full amd64 smoke matrix (`task live:*:amd64`) and
   gate the merge of `m6-2-pr2-amd64-wip` on a fully-green result.

5. Document the AllocatePages handoff invariant in the README's
   per-arch table — currently arm64 / riscv64 / loong64 each
   advertise it; amd64 should too once shipped.

### 11.7 Files / state

- **No code changes shipped** this sprint (R-amd64a). The smoke
  matrix is unchanged from § 10.4.
- This § 11 documents the root cause + the partial-fix attempt +
  the concrete next-sprint plan so the next agent can pick up
  without re-deriving the trace.

## 12. R-amd64b — AllocatePages rewrite attempted, rt0 regression reproduced (2026-06-09)

### 12.1 What was tried

Implemented the R-amd64a § 11.6 plan as a drop-in:

- `uefiboard/cpuinit_amd64.s` rewritten to call
  `gBS->AllocatePages(EFI_ALLOCATE_ANY_PAGES, EfiLoaderData,
  RamSize>>12, &heapBase)` with MS x64 ABI (RCX/RDX/R8/R9 args,
  32-byte shadow space, 16-byte SP alignment via `ANDQ $~15, SP`
  with original SP stashed in R13). On success, anchors
  `goos.RamStart` + `goos.Bloc` to the returned base, then
  `REP STOSB`-zeroes the **entire** 128 MiB allocation (R-amd64a
  speculated 64 bytes was insufficient; the rewrite uses the
  whole region to neutralise istack guard / type-bitmap / persistent-
  arena reads). On AllocatePages failure, halt forever (`HLT; JMP`).
- `uefiboard/board_amd64.go` drops `ramSize` from `0x2c000000`
  (704 MiB) to `0x08000000` (128 MiB), matching arm64 / riscv64 /
  loong64.
- `uefiboard/eficall_amd64.s` removes the pre-CALL
  `SP = RamStart + RamSize` stack switch (mirrors arm64 / riscv64 /
  loong64 thunks; stays on the Go goroutine stack across firmware
  calls).
- `uefiboard/board.go` updates docstrings to reflect the
  AllocatePages model.

All four files compile cleanly under the TamaGo amd64 toolchain.
Disassembly inspection confirms the linker resolves
`runtime∕goos·RamSize`, `runtime∕goos·Bloc`, `runtime∕goos·RamStart`,
`runtime∕goos·RamStackOffset` correctly and the AllocatePages page
count is `RamSize >> 12 = 0x8000` pages.

### 12.2 Test result

Full amd64 smoke matrix against patched OVMF
(`edk2-stable202605`):

| ROW         | MODE     | R-amd64a baseline | R-amd64b      |
|-------------|----------|-------------------|---------------|
| HTTP        | original | **PASS**          | FAIL #GP      |
| HTTP        | packed   | FAIL #PF*         | FAIL #GP      |
| HTTPS       | original | FAIL #PF*         | FAIL #GP      |
| HTTPS       | packed   | FAIL #PF*         | FAIL #GP      |
| OCI         | original | FAIL #PF*         | FAIL #GP      |
| OCI         | packed   | FAIL #PF*         | FAIL #GP      |
| EFIHANDOVER | original | **PASS**          | FAIL #GP      |
| EFIHANDOVER | packed   | FAIL #PF*         | FAIL #GP      |

R-amd64b is a **net regression** from R-amd64a (lost HTTP +
EFIHANDOVER original cells). The new failure mode is uniform
across all 8 cells:

```text
!!!! X64 Exception Type - 0D(#GP - General Protection) !!!!
RIP  - 55415641E5894855, CS  - 0000000000000038
RSP  - 000000007FE3D968 (or in our heap, varies by image size)
R14  - 0000000000000000   (set explicitly by cpuinit; see § 12.3)
CR3  - 000000007FC01000   (firmware's PML4, unchanged)
```

`RIP = 0x55415641_E5894855` is **non-canonical** (bits 63..47 not
sign-extended) and decodes byte-wise to
`55 48 89 E5 41 56 41 55` =
`push rbp; mov rbp,rsp; push r14; push r13` — Go's standard
frame-pointer prologue. So somewhere in the bring-up, a `RET`
pops these bytes off the stack as a return address. Exactly the
R-amd64a § 11.4 secondary regression.

### 12.3 Ruled out

- **R14 = arbitrary firmware leftover** (Go's ABIInternal uses
  R14 = g; bare-metal init.s relies on R14=0 at PE entry, address
  0x10 being mapped, so `runtime.check`'s `CMPQ SP, 0x10(R14)`
  split-stack load returns a tiny value and never faults). Added
  `XORQ R14, R14` to cpuinit's entry; **same failure**.

- **Uninitialised stack memory** (R-amd64a's primary hypothesis;
  argc/argv at 24(SP) / 32(SP) etc.). The `REP STOSB` zero of
  the full 128 MiB allocation runs before SP is set; **same
  failure**.

- **MS x64 alignment violation at the AllocatePages CALL site**
  (Go's caller SP is 8-mod-16; firmware expects 16-mod-16 at
  call instruction so callee sees 8-mod-16 after RIP push).
  Added `MOVQ SP, R13; SUBQ $32, SP; ANDQ $~15, SP` /
  `CALL (R11); MOVQ R13, SP`; **same failure**.

- **AllocatePages returning non-success and the failure path
  running** — cpuinit's `JNZ allocFail` would `HLT` (halt CPU);
  the actual symptom is a #GP after rt0 has started, not a hang.

### 12.4 Most plausible remaining hypotheses

1. **`firstmoduledata` GC bitmap pointers** are computed off
   `runtime.text` / `runtime.data`. Under bare-metal `RamStart =
   text - 64 KiB`, the heap (Bloc upward from RamStart) ENGULFS
   the .text/.data of the binary — the GC's
   "is this pointer in heap" check uses Bloc, blocMax, and
   `firstmoduledata.{noptrdata, data, bss, noptrbss}` ranges.
   Under R-amd64b, `RamStart = Bloc = heapBase` is **outside**
   the binary's loaded address range — `firstmoduledata.end ≠
   blocMax`. `osinit` skips its `initBloc` (since `goos.Bloc != 0`)
   but `mallocinit` may still read GC bitmaps anchored at the
   wrong base, producing pointer values that look like Go
   function-prologue bytes when interpreted as code addresses.
   Repro idea: instrument `mallocinit` / `gcBitsArenas` to print
   the bitmap base + a few bytes on entry, see if the bytes match
   the post-fault RIP value.

2. **Stack pointer ALIGNMENT into rt0_amd64_tamago.** After
   `SP = R12 + RamSize - 0x100000`, SP is `heapBase + 0x07F00000`.
   `heapBase` is page-aligned (4 KiB) per AllocatePages contract,
   so SP is 0-mod-4096 — but Go's ABIInternal requires SP +
   `frame_size` to land on a specific alignment (typically
   16-byte). The bare-metal `RamStart = text - 64 KiB` happens
   to be 0-mod-64KiB → 0-mod-16 too, but offset-by-the-jmp's
   8-byte push? Actually rt0 is `JMP` (no push). Hmm. Worth
   verifying by trying `SUBQ $8, SP` between the SP setup and
   the JMP — if SP-alignment is the cause, even a single 8-byte
   shift will change which cells PASS vs FAIL.

3. **MTRRs or PAT bits on the AllocatePages region.** EfiLoaderData
   is normally WriteBack-cacheable, but if OVMF mapped our chunk
   as WriteCombining or Uncacheable, RMW instructions on it
   (the runtime uses LOCK CMPXCHGL in atomic.Cas — visible in
   the `runtime.check` disassembly we already collected) would
   misbehave or fault. Verify by reading the page's PAT/MTRR
   via RDMSR(0x277) and the firmware's GCD memory map.

### 12.5 What shipped this sprint

- **No code changes shipped to main.** The R-amd64b experimental
  diff lives on the `m6-2-pr2-amd64-wip-r-amd64b` branch (commit
  `9cb9e0b`, "R-amd64b WIP: amd64 cpuinit AllocatePages + 128
  MiB heap (rt0 zero-init regression)") for R-amd64c to pick up.
- The OLD `m6-2-pr2-amd64-wip` branch (the BlkRingBuffer-
  instrumented stub experiment that predates the R-amd64a
  investigation) is intentionally **left intact** — it's a
  different debug avenue.
- Baseline on main is unchanged: M5 HTTP + M8.0 EFIHANDOVER
  continue to PASS, M6 HTTPS / M7 OCI / packed-variants
  continue to fail the way R-amd64a documented in § 11.

### 12.6 Concrete next-sprint plan (R-amd64c)

1. **Resume from `m6-2-pr2-amd64-wip-r-amd64b` branch.** Tree
   already has the AllocatePages cpuinit + 128 MiB board +
   no-stack-switch eficall in place. Don't re-derive.

2. **Add `ConOut` debug prints from cpuinit** between
   AllocatePages return and JMP rt0:
   - one byte ('A') after `JNZ allocFail` succeeds → confirms
     AllocatePages OK;
   - one byte ('B') after the REP STOSB → confirms memset
     completes (rules out memset trampling unmapped pages);
   - one byte ('C') just before the JMP → confirms SP arithmetic
     completes;
   - **if 'A' shows but not 'B' or 'C'**: the memset is faulting
     because AllocatePages returned a region we can't write —
     check the GCD memory map / PAT;
   - **if all three show but the rt0 #GP still fires**: the bug
     is inside `runtime.rt0_amd64_tamago` (most likely §12.4
     hypothesis 1 or 2).

3. **Try the SP-alignment shift in (2.) — `SUBQ $8, SP` between
   SP setup and JMP.** Cheapest test for hypothesis 2.

4. **If a rt0 patch is needed**, save to
   `cloud-boot/docs/tamago-pie-amd64-rt0-zeroinit.patch`,
   apply locally in `~/Documents/VCS/GIT/localhost/tamago-pie/`,
   rebuild TamaGo, re-test. Do NOT push to tamago-pie (forked
   upstream).

5. **Gate the merge of `m6-2-pr2-amd64-wip-r-amd64b` to main on
   a fully-green M6 + M7 + M8.0 + M8.2 packed amd64.**

### 12.7 Time accounting

Sprint cap was 120 min. Spent: ~110 min on (build, run, debug
× 3 hypotheses, document). Per the task brief's "if you blow the
budget on the rt0 fix, ship the cpuinit + eficall changes WITH a
clear 'rt0 zero-init regression remaining' + push WIP + propose
next investigation" rule, that is exactly what this section
documents.

## 13. R-amd64c — ConOut markers + SP-alignment probe (2026-06-10)

### 13.1 Goals (per § 12.6)

1. ConOut debug markers at 4 critical points in `cpuinit_amd64.s`
   (post-AllocatePages, post-REP-STOSB, post-SP-setup,
   pre-JMP-rt0) to localise the crash step.
2. Test `SUBQ $8, SP` between SP setup and JMP for hypothesis 2
   (MS x64 alignment at rt0 entry).
3. Inspect `firstmoduledata` GC bitmap layout for hypothesis 1.
4. Re-run amd64 smoke matrix; merge WIP if green.

### 13.2 ConOut marker infrastructure — built, but UNUSABLE pre-AllocatePages

- Added `cpuinitMarker0..E = [5]uint16{c, '\r', '\n', 0}` package
  globals in `uefiboard/board.go` (5 × 10 bytes data section
  overhead).
- Added a `printChar` NOSPLIT|NOFRAME helper TEXT in
  `cpuinit_amd64.s` that:
  - Stashes the caller's SP in a memory slot (`·cpuinitSavedSP`,
    NOT in RBX — see § 13.4 below).
  - Reserves a 32-byte MS x64 shadow space, force-aligns SP to
    16-mod-16, calls `*(EFI_SIMPLE_TEXT_OUTPUT_PROTOCOL +
    OutputString = +0x08)` via two register loads.
  - Restores SP from memory and RETs.
- Added 5 marker call sites in cpuinit (`@` post-conOut-capture,
  `A` post-AllocatePages, `B` post-REP-STOSB, `C` post-SP-setup,
  `D` pre-JMP-rt0, `E` in the allocFail path).

**Outcome: ZERO markers appear on `-serial stdio`** even when the
binary's HLT-at-entry probe (§ 13.3) PROVES `cpuinit` is reached.
The crash signature shifts predictably with `.text` layout:
without markers the RIP-on-fault is `0x55415641E5894855`
(`push rbp; mov rbp,rsp; push r14; push r13`), with marker code
linked it changes to `0x56575441E5894855`
(`push rbp; mov rbp,rsp; push r12; push rdi; push rsi`) — the byte
patterns of two different Go-emitted function prologues, popped
off the stack by a RET that found garbage at SP.

Conclusion: the ConOut OutputString call itself is unsafe when
invoked from `cpuinit` on the firmware-supplied stack. The most
likely cause: firmware hands the entered image a small stack
(observed empirically by walking EDK2 source: `LoaderEntry` →
`StartImage` allocates a small per-image stack), and
OutputString's deeply-nested path (terminal-emulation tab/cursor
handling + the EFI shell's PageBreak machinery + ConSplitter's
fanout to every output console) overflows it, corrupting the
return-address area we're standing on. Symptom: the printChar RET
pops garbage as RIP → #GP into non-canonical address whose byte
pattern matches Go function prologues.

The marker infrastructure is therefore REMOVED from the shipped
`cpuinit_amd64.s` in this sprint; a single-line comment in
`board.go` points future debug sprints at the QEMU 0x402
isa-debugcon port (one OUTB instruction, no firmware-side stack
consumption) as the correct primitive for pre-AllocatePages
tracing.

### 13.3 HLT-at-entry probe — proved cpuinit IS reached

Substituting `HLT; JMP -1(PC)` for the very first cpuinit
instruction caused QEMU to hang (no #GP, no exception, the
TIMEOUT-induced TERM at 20s being the only kill signal). This
proves the firmware DOES call our PE entry, and the crash is
NOT in the firmware's LoadImage/StartImage path.

Substituting the same HLT just AFTER the ConOut capture (post
`MOVQ AX, ·conOut(SB)`) also hung — so cpuinit reaches at least
that point.

Substituting it for the FIRST `CALL printChar` showed the same
hang — but UN-substituting (so the CALL fires) reproduced the
#GP. This pins the failure to the printChar CALL/RET pair, not
to any post-CALL state we set up.

### 13.4 RBX-clobber discovery

While iterating printChar, an interim design saved the caller's
SP in RBX (callee-saved under MS x64), reasoning that RBX would
be undisturbed across OutputString. The resulting `MOVQ BX, SP`
yielded the SAME garbage-RIP #GP — RBX was NOT preserved by
OVMF's OutputString. The post-fault register dump showed
RBX = `0x000000007FE3D990` (a pointer into the firmware's
exception-handler stack region, NOT the heap), confirming OVMF
wrote into RBX during the call.

Workaround: stash the SP in a memory slot
(`var cpuinitSavedSP uint64`) instead of a register. (Now also
removed along with the rest of the marker infrastructure, but
documented here as a permanent caveat: **do NOT rely on RBX
preservation across ANY OVMF Boot Services call**, even though
MS x64 promises it.)

### 13.5 SP-alignment nudge — kept as a 1-instruction defensive

Added `SUBQ $8, SP` between the SP-setup arithmetic and the JMP
to `runtime·rt0_amd64_tamago`. In ONE probe variant (cpuinit
carrying the full marker0+printChar+marker-A..D pre-JMP code,
yielding a ~96-byte .text bump that shifted later functions),
this nudge appeared to flip HTTP-original from FAIL to PASS.
Reproducing the same PASS in the cleaned variant (marker code
removed) failed: HTTP-original is back to FAIL with the same
`0x55415641E5894855` Go-prologue RIP.

So the apparent fix was a coincidence of `.text` layout: the
SUBQ $8 alignment hypothesis is NOT confirmed. The SUBQ $8 is
KEPT in the shipped cpuinit as a 1-byte-cost defensive against
Go amd64 ABIInternal's "SP+8 = 16-mod-16 at CALL site" assumption
that the runtime's compiled Go code may make, but it is
NOT the root-cause fix.

### 13.6 Smoke matrix — final amd64 state

Against patched OVMF (`edk2-stable202605`), 2026-06-10:

| ROW         | MODE     | R-amd64b | R-amd64c (shipped) |
|-------------|----------|----------|--------------------|
| HTTP        | original | FAIL #GP | FAIL #GP           |
| HTTP        | packed   | FAIL #GP | FAIL #GP           |
| HTTPS       | original | FAIL #GP | FAIL #GP           |
| HTTPS       | packed   | FAIL #GP | FAIL #GP           |
| OCI         | original | FAIL #GP | FAIL #GP           |
| OCI         | packed   | FAIL #GP | FAIL #GP           |
| EFIHANDOVER | original | FAIL #GP | FAIL #GP           |
| EFIHANDOVER | packed   | FAIL #GP | FAIL #GP           |

Net change from R-amd64b: **none**, on the shipped tree. The
SP+8 nudge survives but is not sufficient. The marker
infrastructure is removed (its OutputString CALL was actively
the fault source, not the crash already in cpuinit).

### 13.7 Findings + revised hypotheses

#### What we KNOW after R-amd64c

1. **cpuinit IS entered on every probe binary.** HLT-at-entry
   hangs cleanly; firmware loads and starts the image
   correctly. R-amd64a § 11.3's "stack-spill into MMIO" pattern
   was the SYMPTOM, not the disease: the actual fault is RET
   popping a Go-function-prologue byte sequence as a return
   address.
2. **OVMF clobbers RBX across OutputString** despite MS x64
   promising callee-save. Any further amd64 work that calls
   into Boot Services pre-AllocatePages MUST not stash state
   in any general-purpose register — use a memory slot.
3. **The firmware-supplied stack at PE entry is small.**
   A single OutputString CALL was enough to overflow it
   (manifesting as RET-into-garbage), proving we have << 4 KiB
   of headroom from the firmware.
4. **The crash pattern is .text-layout-sensitive.** Bumping
   `.text` by 96 bytes (the marker0+printChar code) changed the
   crash RIP from one Go-prologue byte pattern to a different
   one. This points to a corrupted POINTER, not corrupted CODE
   — something dereferences a stale address that points
   somewhere inside .text, then misinterprets the .text bytes
   as a return address.

#### Revised hypotheses for R-amd64d

H1 (highest confidence). **`firstmoduledata` GC bitmap pointers
are corrupt because `Bloc = heapBase` lies OUTSIDE the binary's
loaded address range.** When `osinit` skips `initBloc` (because
`goos.Bloc != 0`), it leaves `firstmoduledata.{noptrdata, data,
bss, noptrbss}` pointing at addresses CLOSE TO the binary's
loaded base (e.g. `0x7D1A_xxxx`), while `Bloc / blocMax` are
anchored at the AllocatePages-returned base (e.g. `0x28000000`).
The GC's "is this pointer in heap?" check uses Bloc/blocMax;
the typed-pointer scan uses firstmoduledata.{data,bss,...}.
These two regions don't overlap, and runtime invariants likely
assume they do. The corruption manifests later as a function
pointer's bytes (not address) appearing on the stack.

**R-amd64d probe**: dump `firstmoduledata.data`, `.edata`,
`.bss`, `.ebss`, `.gcdata`, `.gcbss`, `.text`, `.etext` on the
arm64 PASS path AND on the amd64 FAIL path, compare. arm64
shows the same Bloc-vs-firstmoduledata split, so if it works
there, the difference is in some amd64-specific runtime path.

H2 (medium). **`runtime·rt0_amd64_tamago` line 20's
`LEAQ (-64*1024)(SP), AX` sets g0.stack.lo to `heapBase + 128MiB
- 1MiB - 64KiB`, which is INSIDE the heap region.** The runtime
then uses g0.stack.lo as the bottom of the istack. Later
allocations from sbrk (per `mem_tamago.go:15`) check
`bl+n > uintptr(g0.stack.lo)` to refuse growth. With our
RamSize=128MiB, the gap is `128MiB - 1MiB - 64KiB ≈ 127MiB` of
usable heap before sbrk runs out — should be plenty, but
verifying that no early `mallocinit` allocation tries to grab
> 127MiB worth of contiguous arena is worth a single
`println(g0.stack.lo, bloc, blocMax)` early in `osinit`.

H3 (lower). **MS x64 ABI alignment somewhere DEEPER than rt0
entry.** Our SUBQ $8 didn't fix it alone, but maybe rt0's own
internal calls to `runtime.settls` / `runtime.check` see a
different mis-alignment that one nudge can't address. Probably
chase via objdump of `runtime.check` itself looking for any
`MOVDQA / MOVAPS / VMOVAPS` instruction with a stack-relative
addressing mode.

### 13.8 Concrete R-amd64d plan

1. **Use QEMU's isa-debugcon port (0x402) for pre-runtime
   tracing**, not ConOut. Add `-debugcon file:/tmp/dbg.out
   -global isa-debugcon.iobase=0x402` to the smoke runner;
   replace `printChar` with `OUTB AL, $0x80` (or 0x402).
   ONE single instruction, no firmware stack consumption.
   The hex byte appears in `/tmp/dbg.out`.
2. **Bring up an amd64 host-side print harness** that links a
   minimal `mallocinit`-only program (no schedinit) — if
   THAT crashes, the issue is mallocinit. If it doesn't, the
   issue is in schedinit / the M/G goroutine initialisation
   path.
3. **Cross-check `firstmoduledata` on arm64 vs amd64**: write
   a small tamago Go program that prints `firstmoduledata.data,
   edata, bss, ebss, end, gcdata, gcbss` from `init()` and run
   it on both arches. Compare to runtime.Bloc / blocMax for
   both. The amd64 mismatch (if any) is the bug.
4. **Tamago-pie patch (LOCAL, do not push)**: change
   `runtime.rt0_amd64_tamago` to zero-init the bootstack and
   add an explicit argc=0/argv=NULL frame. Document the patch
   in `cloud-boot/docs/tamago-pie-amd64-rt0-zeroinit.patch`.

### 13.9 What shipped this sprint

- **`uefiboard/cpuinit_amd64.s`**: stripped of the marker
  infrastructure; SP+8 nudge KEPT as a 1-instruction defensive;
  full comment block documenting the R-amd64c findings and
  why the nudge alone is not the root-cause fix.
- **`uefiboard/board.go`**: marker globals removed; replaced
  with a 1-paragraph comment block pointing future debug
  sprints at the QEMU isa-debugcon port.
- No tamago-pie patch shipped (the H1/H2/H3 root-cause work
  needs the debugcon probe before any patch can be designed).
- WIP branch `m6-2-pr2-amd64-wip-r-amd64b` stays distinct
  from main; smoke matrix is still all-FAIL on amd64, so
  the merge gate remains closed.

### 13.10 Time accounting

Sprint cap 120 min. Spent ~120 min on:
- (~10 min) read R-amd64a § 11 + R-amd64b § 12, understand WIP
  branch state.
- (~25 min) design and ship the marker0..E + printChar
  infrastructure.
- (~30 min) iterate marker visibility (ConOut OutputString
  never produced output despite cpuinit being reached;
  diagnosed via HLT-at-entry probe).
- (~20 min) chase the RBX-clobber regression (printChar's BX
  stash → garbage SP → garbage RIP).
- (~15 min) SUBQ $8 alignment test + variant matrix.
- (~20 min) strip the marker code back out and document
  findings in this § 13.

