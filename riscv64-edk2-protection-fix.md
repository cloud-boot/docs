# riscv64 EDK2 image-protection investigation

## Summary

The README of `cloud-boot/tamago-uefi` reports that EDK2
`edk2-stable202408` faults inside `SetUefiImageMemoryAttributes`
*before* our `cpuinit_riscv64.s` runs, on QEMU `virt` with 4 GiB of
RAM and `OPENSBI + EDK2 RiscV`, with `sepc` inside `CpuDxeRiscV64.efi`
and `stval = 0x180000C0` (one byte past the top of system RAM). We
reproduced the boot under the same pkgx-shipped firmware
(`qemu.org/v9.2.0`, which bundles a prebuilt
`edk2-stable202408-prebuilt.qemu.org` RISC-V firmware payload) and
**could not reproduce the fault**: our `BOOTRISCV64.EFI` is now loaded
all three section-attributes calls run cleanly, control transfers to
the image entry point and the Go runtime prints `DONE`. The
crash documented in the README is no longer observable on the same
firmware + image combination. We did, however, identify one latent
defect in `UefiCpuPkg/Library/BaseRiscVMmuLib/BaseRiscVMmuLib.c` while
auditing the page-table mutation path; the patch
`edk2-riscv64-protection-fix.patch` fixes it. The patch is not a fix
for the original symptom (because the symptom does not currently
reproduce), but it is independently submittable upstream.

## Reproduction commands

The reproduction below uses the QEMU + EDK2 RISC-V firmware that
`pkgx` resolves for the cloud-boot tooling
(`/opt/homebrew/share/qemu/edk2-riscv-code.fd`,
`/opt/homebrew/share/qemu/edk2-riscv-vars.fd`, both shipped by
`qemu.org/v9.2.0`). QEMU is `qemu-system-riscv64 9.2.0`.

```sh
# 1. Stage the EFI app on a FAT ESP that QEMU can mount.
mkdir -p esp-riscv64/EFI/BOOT
cp /Users/david_delavennat/Documents/VCS/GIT/github.com/cloud-boot/tamago-uefi/BOOTRISCV64.EFI \
   esp-riscv64/EFI/BOOT/BOOTRISCV64.EFI

# 2. Take a writable copy of the firmware code+vars (QEMU mmaps them).
cp /opt/homebrew/share/qemu/edk2-riscv-code.fd ./edk2-riscv-code.fd
cp /opt/homebrew/share/qemu/edk2-riscv-vars.fd ./edk2-riscv-vars.fd

# 3. Boot, both with the default cpu and with -cpu max.
qemu-system-riscv64 -machine virt -m 4096 -nographic -serial mon:stdio \
  -drive if=pflash,format=raw,unit=0,file=edk2-riscv-code.fd \
  -drive if=pflash,format=raw,unit=1,file=edk2-riscv-vars.fd \
  -drive file=fat:rw:esp-riscv64,format=raw,if=none,id=esp \
  -device virtio-blk-device,drive=esp

qemu-system-riscv64 -machine virt -cpu max -m 4096 -nographic -serial mon:stdio \
  -drive if=pflash,format=raw,unit=0,file=edk2-riscv-code.fd \
  -drive if=pflash,format=raw,unit=1,file=edk2-riscv-vars.fd \
  -drive file=fat:rw:esp-riscv64,format=raw,if=none,id=esp \
  -device virtio-blk-device,drive=esp
```

## Observed behaviour (2026-06-07)

Identical addresses to the README's failing trace are printed by
`MdeModulePkg/Core/Dxe/Misc/MemoryProtection.c` and the boot proceeds
past the third call without any `!!!! RISCV64 Exception !!!!` banner
from `BaseRiscV64CpuExceptionHandlerLib`:

```text
Loading driver at 0x0017E195000 EntryPoint=0x0017E1E84E8
ProtectUefiImageCommon - 0x7ECE7840
  - 0x000000017E195000 - 0x000000000013C000
SetUefiImageMemoryAttributes - 0x000000017E195000 - 0x0000000000001000 (0x0000000000004000)
SetUefiImageMemoryAttributes - 0x000000017E196000 - 0x0000000000053000 (0x0000000000020000)
SetUefiImageMemoryAttributes - 0x000000017E1E9000 - 0x00000000000E8000 (0x0000000000004000)
hello from cloud-boot tamago/amd64 UEFI board
runtime: go1.26.3 GOOS=tamago GOARCH=amd64
goroutine sum: 499500
DONE - halting
```

(The "tamago/amd64" line is a separate constant-folding issue in our
build pipeline - the image is correctly identified as RISC-V by the
PE header and runs under `qemu-system-riscv64`, but the `runtime.GOARCH`
string baked into the binary is inherited from the amd64 build; not
in scope for this document.)

The runtime reaches `main`, the goroutine-channel smoke test
completes, and `DONE` is printed - same end-state as amd64 and arm64.

The previously-reported fault evidence:

```text
!!!! RISCV64 Exception Type - 000000000000000F(EXCEPT_RISCV_STORE_ACCESS_PAGE_FAULT) !!!!
   sepc  = 0x0000000017FE16CCA   (inside CpuDxeRiscV64.efi)
   stval = 0x00000000180000C0    (1 byte past end of 4 GiB system RAM)
```

does not appear in either run. It is possible that the underlying
defect required a particular combination of (a) older QEMU
(b) older prebuilt EDK2 inside `pkgx`, (c) an interim PE32+ produced
by an older `pectl link-pie` revision. With the current pkgx pin the
fault does not manifest.

## Source audit of the suspected fault path

Even though the symptom does not reproduce, we read the page-table
mutation code that the original `sepc` would have been in. The
relevant chain is:

* `MdeModulePkg/Core/Dxe/Misc/MemoryProtection.c::SetUefiImageMemoryAttributes`
  (stable202408 line 188) - reads the GCD descriptor of `BaseAddress`,
  merges in `EFI_MEMORY_RO` / `EFI_MEMORY_XP` and forwards to
  `gCpu->SetMemoryAttributes`.
* `UefiCpuPkg/CpuDxeRiscV64/CpuDxe.c::CpuSetMemoryAttributes`
  (stable202408 line 306) - thin wrapper over
  `RiscVSetMemoryAttributes`.
* `UefiCpuPkg/Library/BaseRiscVMmuLib/BaseRiscVMmuLib.c::RiscVSetMemoryAttributes`
  (stable202408 line 587) - translates GCD attributes into RISC-V PTE
  bits and calls `UpdateRegionMapping` -> `UpdateRegionMappingRecursive`
  to walk and mutate the page tables that `RiscVMmuSetSatpMode` set
  up during `InitializeCpu`.

In that walk we noted the following defect in
`SetPpnToPte`
(stable202408 line 235):

```c
Ppn = ((Address >> RISCV_MMU_PAGE_SHIFT) << PTE_PPN_SHIFT);
ASSERT (~(Ppn & ~PTE_PPN_MASK));     // bitwise NOT - always non-zero
Entry &= ~PTE_PPN_MASK;
return Entry | Ppn;
```

The very similar idiom in `RiscVMmuSetSatpMode` (stable202408 line
728) uses the correct `!` (logical NOT):

```c
Ppn = (UINT64)TranslationTable >> RISCV_MMU_PAGE_SHIFT;
ASSERT (!(Ppn & ~(SATP64_PPN)));     // logical NOT - intended form
```

`~(Ppn & ~PTE_PPN_MASK)` is zero only when every bit of
`(Ppn & ~PTE_PPN_MASK)` is one - impossible because the low bits of
`Ppn` are zero by construction. The intended check is "no PPN bits
fall outside the mask", which is `!(Ppn & ~PTE_PPN_MASK)`. The bug
makes the assert always pass.

This typo is not the root cause of the previously-reported store
access fault (asserts compile out in `RELEASE` builds and are no-ops
in `DEBUG` here), but it is a real latent defect that hides genuine
PPN-overflow bugs and should be fixed upstream.

The typo is present in both `edk2-stable202408` and current `master`
(commit `b158dad` at fetch time + `c86451e7..0199530a` range on
`master`). No commit between `edk2-stable202408` and `master`
addresses it. The only RISC-V MMU change after `edk2-stable202408`
was `0199530a UefiCpuPkg: BaseRiscVMmuLib: Fix the logic toggling the
interrupt state`, which is unrelated.

## The fix

The fix is the smallest possible change - swap `~` for `!`:

```diff
-  ASSERT (~(Ppn & ~PTE_PPN_MASK));
+  ASSERT (!(Ppn & ~PTE_PPN_MASK));
```

This is the patch `edk2-riscv64-protection-fix.patch`. It is staged
as a single commit against `edk2-stable202408`. The commit message
explains the reasoning and contrasts with the correct idiom a few
lines below.

## Whether mainline already addresses this

No. The same typo is present on `master` as of the fetch performed
for this investigation. A targeted git-log against the file shows
only `0199530a` has touched `BaseRiscVMmuLib.c` since
`edk2-stable202408`, and that commit changes the interrupt-state
toggling in `RiscVMmuSetSatpMode`, not `SetPpnToPte`.

## Upstream contribution plan (when authorized)

* **Mailing list**: `devel@edk2.groups.io` (TianoCore devel list).
* **Recommended commit prefix**: `UefiCpuPkg/BaseRiscVMmuLib:` (matches
  existing commit history for this file).
* **CC list** (per
  `Maintainers.txt` for `UefiCpuPkg`):
    * Ray Ni `<ray.ni@intel.com>` - `UefiCpuPkg` maintainer.
    * Rahul Kumar `<rahul1.kumar@intel.com>` - `UefiCpuPkg` reviewer.
    * Sunil V L `<sunilvl@ventanamicro.com>` - RISC-V code reviewer
      (he is the author of much of `BaseRiscVMmuLib.c`).
    * Andrei Warkentin `<andrei.warkentin@intel.com>` - additional
      RISC-V reviewer.
* **DCO**: the patch carries a real `Signed-off-by` per upstream
  policy (`david.delavennat@polytechnique.edu`); the GitHub noreply
  alias is not used because the EDK2 DCO requires a verifiable
  identity.
* **No `Co-Authored-By` trailer**: EDK2 does not accept co-author
  trailers; only `Signed-off-by` is expected.
* **Timing**: hold the submission until cloud-boot has reviewed the
  patch and the analysis here. Once approved, post to
  `devel@edk2.groups.io` with `git send-email` from a checkout that
  has the patch applied on top of `master`, and CC the people above.

## Regression-test gate

We ship `riscv64_edk2_boot_test.go` alongside this document. It is
skip-gated on:

* `qemu-system-riscv64` being on `$PATH`,
* a `BOOTRISCV64.EFI` being readable at the path pointed to by
  `$RISCV64_EFI` (defaults to
  `../../tamago-uefi/BOOTRISCV64.EFI` relative to the test file),
* a code firmware at `$RISCV64_OVMF_CODE` (defaults to
  `/opt/homebrew/share/qemu/edk2-riscv-code.fd`),
* a vars firmware at `$RISCV64_OVMF_VARS` (defaults to
  `/opt/homebrew/share/qemu/edk2-riscv-vars.fd`).

The test boots QEMU, captures serial output, and asserts that the
runtime marker `DONE` appears within a fixed time budget. When the
fault is present, the runtime never gets to print `DONE` and the
test fails with the captured trailing log.

To exercise the test against a patched EDK2 build, point
`$RISCV64_OVMF_CODE` at the firmware built from a tree that has
`edk2-riscv64-protection-fix.patch` applied.

## File map

* `edk2-riscv64-protection-fix.patch` - the single-commit patch
  against `edk2-stable202408`.
* `riscv64-edk2-protection-fix.md` - this document.
* `riscv64_edk2_boot_test.go` - skip-gated regression test.
