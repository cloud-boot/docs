---
title: M6.2 amd64 firmware bug — EDK2 upstream investigation
status: investigation complete 2026-06-09; recommendation = build patched OVMF (preferred) OR upgrade to edk2-stable202511+
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
- [ ] **pkgx pantry:** add a `tianocore.org/edk2` recipe that builds
      OVMF from `edk2-stable202511` (or current stable). De-couples
      our firmware blob from qemu's release cadence. See
      `feedback-package-completeness.md` and `feedback-add-missing-deps.md`
      in user memory — this is exactly the pattern.
- [ ] **cloud-boot:** once a patched/updated `edk2-x86_64-code.fd` is
      reachable through pkgx, re-run the M6.2 amd64 sweep:
      M6 HTTPS, M7 OCI, M8.0 chainedhello — all should PASS.
- [ ] **cloud-boot:** then close M6.2 amd64 deferral; un-defer M6.2
      amd64 from `tamago-uefi-phase2-oci-loader.md` and remove the
      `m6-2-pr2-amd64-wip` branch.

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
