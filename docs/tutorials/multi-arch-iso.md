# Multi-arch hybrid ISO — one image for x86_64, arm64, riscv64, loongarch64

`cloud-boot iso` packs several already-built per-arch UKIs into a
single hybrid iso9660 + El Torito + GPT image whose FAT ESP carries
each UKI at the UEFI removable-media fallback path for its arch:

```text
boot.iso  →  GPT partition 2 (FAT16/32 ESP)
                 \EFI\BOOT\BOOTX64.EFI         ← amd64 firmware reads this
                 \EFI\BOOT\BOOTAA64.EFI        ← arm64 firmware reads this
                 \EFI\BOOT\BOOTRISCV64.EFI     ← riscv64 firmware reads this
                 \EFI\BOOT\BOOTLOONGARCH64.EFI ← loongarch64 firmware reads this
```

Firmware on each CPU only ever looks at its own arch's file, so the
same `boot.iso` boots on any of the four supported CPUs.

## Why this is useful

- **One artifact in the registry / fleet manager.** OpenStack Glance,
  S3, or a private mirror only needs to keep `boot.iso` — not
  `boot-amd64.iso` + `boot-arm64.iso` + `boot-riscv64.iso`.
- **Mixed-arch CI matrices.** A single test image runs against
  QEMU x86_64, QEMU aarch64, QEMU riscv64 (and Apple VZ on the arm64
  leg) without job-specific URLs.
- **Per-instance config still works.** The `cloudboot.metadata.url=`
  override path is per-VM not per-ISO; the multi-arch ISO doesn't
  change anything about plan/target resolution.

## Workflow

### 1. Build one UKI per arch

The existing `cloud-boot build --arch <a>` flow produces one UKI
per arch. Repeat once per CPU you care about:

```sh
cloud-boot build --arch amd64       --kernel bzImage-amd64       --plan oci://… -o boot-amd64.efi
cloud-boot build --arch arm64       --kernel Image-arm64         --plan oci://… -o boot-arm64.efi
cloud-boot build --arch riscv64     --kernel Image-riscv64       --plan oci://… -o boot-riscv64.efi
cloud-boot build --arch loongarch64 --kernel vmlinux-loongarch64 --plan oci://… -o boot-loongarch64.efi
```

The per-arch kernel + initramfs + cloud-boot-init come from the
same OCI plan (which is normally a multi-arch image index — see
[`cloud-boot push index`](../reference/plan-hcl.md)). The UKI is
just the `.linux` + `.initrd` + `.cmdline` sections wrapped in the
appropriate per-arch systemd UEFI stub (`linuxx64.efi.stub`,
`linuxaa64.efi.stub`, `linuxriscv64.efi.stub`).

### 2. Assemble the multi-arch ISO

```sh
cloud-boot-iso \
  --uki linux/amd64=boot-amd64.efi             \
  --uki linux/arm64=boot-arm64.efi             \
  --uki linux/riscv64=boot-riscv64.efi         \
  --uki linux/loongarch64=boot-loongarch64.efi \
  -o boot.iso
```

`cloud-boot-iso` lives in [`cloud-boot/iso`](https://github.com/cloud-boot/iso)
as a standalone binary (it was previously a subcommand of
`cloud-boot`; the assembler logic is generic and applies to any
PE32+/EFI app, UKI or tamago unikernel alike). Each
`--uki linux/<arch>=<path>` entry resolves to the right
`BOOT<ARCH>.EFI` filename. You can omit arches you don't need:

```sh
# x86_64 + arm64 only — no riscv64 leg
cloud-boot-iso \
  --uki linux/amd64=boot-amd64.efi \
  --uki linux/arm64=boot-arm64.efi \
  -o boot.iso
```

### 3. Boot it

```sh
# QEMU on x86_64 — reads /EFI/BOOT/BOOTX64.EFI
qemu-system-x86_64  -machine q35,accel=kvm -cpu host -m 1G \
                    -bios OVMF_CODE.fd  -drive file=boot.iso,format=raw

# QEMU on arm64 — reads /EFI/BOOT/BOOTAA64.EFI
qemu-system-aarch64 -machine virt -cpu max -m 1G \
                    -bios QEMU_EFI.fd   -drive file=boot.iso,format=raw

# QEMU on riscv64 — reads /EFI/BOOT/BOOTRISCV64.EFI
qemu-system-riscv64 -machine virt -m 1G \
                    -bios opensbi.fw -drive file=boot.iso,format=raw

# QEMU on loongarch64 — reads /EFI/BOOT/BOOTLOONGARCH64.EFI
qemu-system-loongarch64 -machine virt -cpu la464 -m 1G \
                    -bios QEMU_EFI-loongarch64.fd \
                    -drive file=boot.iso,format=raw

# Apple VZ on arm64 (via vfkit) — reads /EFI/BOOT/BOOTAA64.EFI
vfkit --bootloader efi,variable-store=nvram.fd \
      --device virtio-blk,path=boot.iso,readonly
```

## ESP sizing

`cloud-boot iso` sums every UKI's size, multiplies by 1.5×, adds
an 8 MiB FAT floor, and rounds up to the next 16 MiB. A typical
3-arch ISO with ~20 MiB UKIs each lands at a 96–112 MiB ESP — well
under the 4 GiB FAT16 ceiling.

## Why per-arch UKIs and not one fat UKI

UEFI does not have a multi-arch executable format — `BOOTX64.EFI`
and `BOOTAA64.EFI` are different PE32+ machine codes (0x8664 vs
0xAA64). Each arch needs its own EFI binary. cloud-boot's UKI also
embeds a kernel + initrd + init, all of which are arch-specific
(an arm64 kernel won't run on an x86_64 CPU). So the natural
boundary is one UKI per arch, then merging at the ESP layer where
firmware does the per-arch selection.

## The RISC-V and LoongArch branches

The pure-UEFI loader (Path B) and the UKI's systemd EFI stub
(Path A / C) both produce PE32+ output. The traditional blockers
were `lld-link` (LLVM's COFF linker) shipping no `/machine:riscv64`
and no `/machine:loongarch64`. The cloud-boot stack uses its own
pure-Go COFF linker, `go-coff/peln`, which has both:

- **amd64** — `0x8664`
- **arm64** — `0xaa64`
- **riscv64** — `0x5064` (R_RISCV_{HI20, LO12_I, LO12_S, PCREL_HI20,
  PCREL_LO12_I/S, BRANCH, JAL, CALL, CALL_PLT, RVC_BRANCH, RVC_JUMP,
  RELAX, 32, 64})
- **loongarch64** — `0x6264` (R_LARCH_{ABS_HI20, ABS_LO12, ABS64_LO20,
  ABS64_HI12, PCALA_HI20, PCALA_LO12, PCALA64_LO20, PCALA64_HI12,
  B16, B21, B26, 32, 64, RELATIVE, RELAX, ALIGN, MARK_LA,
  MARK_PCREL})

Each backend's relocation table is unit-tested in
`peln/linker/reloc_<arch>_test.go`. Invoke via
`pectl link --machine <arch> …`.

For the riscv64 path specifically, the TinyGo runtime gap for
`goarch=riscv64` is filled by the shim in
[`tinygo-riscv64-uefi/runtime/arch_riscv64.go`](https://github.com/cloud-boot/tinygo-riscv64-uefi)
(`align`, `TargetBits`, `callInstSize`, `getCurrentStackPointer`, …).
LoongArch needs an analogous shim — see the stub Taskfile.

End-to-end the path for either arch is:

```text
tinygo build (per-arch runtime shim)            →  main-<arch>.o     (COFF/PE)
clang -target <arch>-pc-windows-gnu             →  thunk-<arch>.o    (COFF/PE)
pectl link --machine <arch>                     →  BOOT<ARCH>.EFI    (PE32+)
cloud-boot iso --uki linux/<arch>=…             →  multi-arch boot.iso
```

The stub Taskfile (in `go-coff/stub/Taskfile.yaml`) still needs
`compile-riscv64` + `link-riscv64` + `compile-loongarch64` +
`link-loongarch64` wire-ups to drive this end-to-end; the linker
backends and CLI are tested in isolation today.
