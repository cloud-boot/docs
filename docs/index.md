---
title: cloud-boot — overview
---

# cloud-boot

**Boot unmodified OS cloud images on KVM/QEMU, Apple
`Virtualization.framework`, and OpenStack — no per-image rebuild, no
custom signing, no kernel-side ZFS module.**

cloud-boot ships **two complementary tracks** that all land on the
same end state — a stock distro userspace from an unmodified cloud
image. Phase 1 is the Linux-side UKI toolchain with three boot
paths ; Phase 2 is a pure-Go bare-metal UEFI loader that drives the
whole networked-OCI pipeline from inside Boot Services.

<div class="grid cards" markdown>

-   :material-rocket-launch:{ .lg .middle } __Phase 1 · Path A — kexec__

    ---

    The original flow. A UKI starts `cloud-boot-init` (Go PID 1),
    resolves a plan, and `kexec`s the distro kernel. Works wherever
    `kexec_file_load` works — **KVM, QEMU, OpenStack, bare metal**.

-   :material-apple:{ .lg .middle } __Phase 1 · Path C — menu-then-reboot__

    ---

    The Apple-VZ target. `cloud-boot-init` runs in Linux PID 1,
    materialises the chosen kernel/initrd on a writable FAT ESP,
    writes `Boot0001` + `BootOrder` via `efivarfs`, and calls
    `reboot(2)`. The firmware then loads the staged target on the
    next pass. **No kexec — works on `Virtualization.framework`
    where `kexec_file_load` is trapped.**

-   :material-flash:{ .lg .middle } __Phase 1 · Path B — TinyGo UEFI loader__

    ---

    A TinyGo PE/COFF UEFI application that stays inside Boot
    Services and `LoadImage`s the distro kernel straight out of an
    ext4 / xfs / btrfs / UFS2 rootfs. Useful on QEMU/OVMF and EDK2
    hardware ; six Linux families + FreeBSD + NetBSD verified.

-   :material-language-go:{ .lg .middle } __Phase 2 — pure-Go TamaGo loader__

    ---

    A pure-Go bare-metal UEFI application on the real Go runtime
    via [TamaGo](https://github.com/usbarmory/tamago). PCI walk →
    virtio-net → DHCPv4 → DNS → TLS (CCADB roots) → HTTPS → OCI
    Distribution v2 → cosign verify → `LoadImage` → `StartImage` →
    real Debian 13 userspace. **Live end-to-end Linux userspace
    on all four arches** (amd64 + arm64 + riscv64 + loong64) as of
    2026-06-10 — 16-18 s wall-clock from a cold DHCP lease.
    `R-amd64j` closed the amd64 saga via an `initrd=` cmdline
    workaround for the EDK2 OVMF amd64 `LoadFile2` quirk ; the
    other three arches use the `LoadFile2` protocol the kernel
    prefers.

</div>

## What this site covers

| Section | What you'll find |
| --- | --- |
| [Architecture](architecture/index.md) | The three boot paths, the four core repos (`init` / `uki` / `loader` / `kernel`), and which hypervisor accepts which path. |
| [Filesystem drivers](filesystems/index.md) | Pure-Go ext4, XFS, btrfs (single + RAID0/1/10/5/6), ZFS (single + mirror + RAID-Z1/2/3), LUKS1/LUKS2 overlay. |
| [Tutorials](tutorials/index.md) | Bootable hello-world on QEMU, on vfkit/Apple Silicon, and on an OpenStack instance with Keystone application credentials. |
| [Reference](reference/index.md) | Every `cloudboot.*` kernel-cmdline knob, the HCL plan schema, the metadata-URL JSON shape, and the NVRAM-reset recipe. |
| [Internals](internals/index.md) | The 14 on-disk format bugs the userland FS drivers had to fix to read real `mkfs.btrfs` / `zpool create` output, plus the RAID-Z stripe-geometry port. |

## Quick facts

<div class="grid cards" markdown>

-   __11 RAID profiles supported__

    ---

    | btrfs | ZFS |
    | --- | --- |
    | single, raid0, raid1, raid10, raid5, raid6 | single, mirror, raidz1, raidz2, raidz3 |

    All healthy-path reads verified against real `mkfs.btrfs`
    / `zpool create` fixtures from a Debian 12 +
    `zfsutils-linux` 2.1.11 VM.

-   __No kernel modules required__

    ---

    Every filesystem path is a **pure-Go userland driver**
    statically linked into `cloud-boot-init`. The bootstrap kernel
    can drop `CONFIG_{EXT4,XFS,BTRFS,ZFS}_FS` entirely, saving
    several MiB.

-   __Six Linux families + three BSDs__

    ---

    **Linux**: Debian Trixie · Ubuntu Noble · Fedora 41 ·
    AlmaLinux 9 · openSUSE Leap Micro 6.2 · Alpine 3.21. Covers
    ext4, ext4+gz, btrfs (root + subvols), XFS, and ZFS-rooted
    (Proxmox / Ubuntu ZSYS).

    **BSD** (via `CloudBootTarget=freebsd|netbsd|openbsd`):
    FreeBSD 14.3 (UFS2 / — login prompt on QEMU ✓), NetBSD 10.0
    (FFS — `login:` on QEMU ✓), OpenBSD 7.x (FFS — routing in
    tree, no arm64 cloud image to E2E test). The loader hands off
    to the BSD's own `loader.efi` via UEFI `LoadImage(DevicePath)`
    so no UFS2/FFS reader is needed in our binary.

-   __One ISO, four CPU architectures__

    ---

    `cloud-boot iso` assembles a single hybrid iso9660 + GPT image
    that embeds `BOOTX64.EFI` + `BOOTAA64.EFI` + `BOOTRISCV64.EFI`
    + `BOOTLOONGARCH64.EFI` in one FAT ESP. UEFI firmware on each
    CPU reads only its own arch's file — so the same `boot.iso`
    runs on **x86_64, arm64, riscv64, and loongarch64** hosts
    without rebuilding.

    The pure-UEFI loader (Path B) is linked with our own
    [`go-coff/peln`](https://github.com/go-coff/peln) COFF linker,
    which supports riscv64 (`0x5064`) and loongarch64 (`0x6264`)
    — the long-standing `lld-link` blockers for `BOOTRISCV64.EFI`
    and `BOOTLOONGARCH64.EFI` are moot here.
    See [Multi-arch ISO](tutorials/multi-arch-iso.md).

-   __Three hypervisor backends__

    ---

    QEMU/KVM (all three paths) · Apple
    `Virtualization.framework` via `vfkit` (**Path C only**) ·
    OpenStack Nova/libvirt/KVM with Keystone application
    credentials and metadata-URL configuration.

</div>

## A 60-second tour

=== "Path A on QEMU/KVM"

    ```bash
    # build a bootable UKI + push the plan as an OCI artifact
    cloud-boot build --arch aarch64 --out boot.efi
    cloud-boot push  oci://ghcr.io/me/cloud-plan:trixie

    # boot — init kexecs straight into the distro
    qemu-system-aarch64 -machine virt -cpu max \
      -bios QEMU_EFI.fd -drive file=boot.efi,format=raw \
      -drive file=trixie.qcow2,if=virtio
    ```

=== "Path C on Apple VZ"

    ```bash
    # 1. immutable boot.iso + writable menu-cache.raw
    cloud-boot build --arch aarch64 --iso boot.iso \
      --cmdline "cloudboot.exit=reboot cloudboot.plan=oci://…"
    uki/scripts/make-cache-disk.sh menu-cache.raw 256

    # 2. boot. init picks the target, writes
    #    \EFI\Linux\T-* + Boot0001/BootOrder, reboots.
    vfkit --bootloader efi,variable-store=nvram.fd \
          --device virtio-blk,path=boot.iso,readonly \
          --device virtio-blk,path=menu-cache.raw \
          --device virtio-blk,path=debian-trixie.raw \
          --device virtio-net,nat,mac=auto
    ```

=== "ZFS-on-LUKS Proxmox"

    ```bash
    # Boot a Proxmox VE install whose root sits on an
    # LUKS-encrypted ZFS pool. The disk path includes the
    # nested dataset; LUKS passphrase is fetched from the
    # per-instance metadata URL, never via /proc/cmdline.

    cloudboot.disk = /dev/vda3
    cloudboot.disk.fs = zfs
    cloudboot.disk.device = rpool/ROOT/pve-1
    cloudboot.disk.luks-passphrase = $(metadata)
    cloudboot.metadata.url = http://169.254.169.254/.../meta_data.json
    ```

See [Tutorials](tutorials/index.md) for the full walk-throughs.

## License

[BSD-3-Clause](https://github.com/cloud-boot/init/blob/main/LICENSE) —
the same licence as the rest of the cloud-boot org and the sibling
`go-coff`, `go-filesystems`, `go-crypto`, `go-fde` orgs.
