---
title: Quickstart — Apple VZ (vfkit)
---

# Quickstart — Apple VZ via vfkit

The Path C / menu-then-reboot flow on Apple Silicon. We'll use
[`vfkit`](https://github.com/crc-org/vfkit) — Apple's officially
supported front-end for `Virtualization.framework` — to boot an
immutable `boot.iso` against a writable `menu-cache.raw` cache
disk, then reboot into Debian Trixie.

## Why this and not Path A

```text
VZ traps kexec_file_load(2)  →  Path A is out
VZ firmware has no HTTP/TCP/DHCP/DNS  →  Path B can't fetch plans
                                          (the loader's networked phases
                                          D–J were abandoned for VZ)
Path C: networking + FS walks happen in a Linux kernel
        target loaded by firmware on next pass via BootOrder  →  ✓
```

See [Architecture / hypervisors](../architecture/hypervisors.md) for
the protocol matrix and a longer discussion.

## 1. Install vfkit + dependencies

```bash
brew install vfkit qemu xorriso mtools dosfstools
```

(QEMU is used as a build-time helper for the host-side tools; the
*runtime* is vfkit.)

## 2. Build the immutable boot.iso

```bash
cd cloud-boot/uki
go run . build \
    --arch aarch64 \
    --iso  boot.iso \
    --cmdline "cloudboot.exit=reboot cloudboot.plan=oci://ghcr.io/me/cloud-plan:prod"
```

The output is a hybrid GPT/El-Torito ISO. The El-Torito boot
catalogue points at the UKI on the appended GPT FAT partition, so
the same file boots equally from CD-ROM emulation, USB, or as a
virtio-blk read-only disk.

## 3. Build the writable cache disk

```bash
cd cloud-boot/uki/scripts
./make-cache-disk.sh ../menu-cache.raw 256
```

This creates a 256 MiB GPT disk with one FAT32 partition labelled
`cloud-boot-cache`. The label is what `cloud-boot-init` looks for
when staging targets. The script is **idempotent** — re-running it
won't clobber existing `Boot####` entries.

## 4. Boot

```bash
cd cloud-boot
vfkit \
    --bootloader efi,variable-store=nvram.fd \
    --device virtio-blk,path=uki/boot.iso,readonly \
    --device virtio-blk,path=uki/menu-cache.raw \
    --device virtio-blk,path=trixie.raw \
    --device virtio-net,nat,mac=auto \
    --device virtio-serial,logFilePath=vm.log
```

What happens:

1. Firmware loads `\EFI\BOOT\BOOTAA64.EFI` from `boot.iso` (UKI).
2. UKI runs `cloud-boot-init` as PID 1.
3. virtio-net comes up, init fetches the plan over OCI.
4. The plan resolves to a target with `disk` source pointing at
   `/dev/vda3` (or whichever virtio-blk index Trixie landed at).
5. `cloud-boot-init` reads `/boot/vmlinuz-*` and
   `/boot/initrd.img-*` via the ext4 userland driver, writes them
   to `\EFI\Linux\<target>-vmlinuz.efi` + `<target>-initrd` on the
   cache ESP.
6. Writes `Boot0001` (LoadOption pointing at `<target>-vmlinuz.efi`
   with `initrd=<target>-initrd <cmdline>` as load options) and
   `BootOrder = [0001, ...existing]` via `efivarfs`.
7. `reboot(LINUX_REBOOT_CMD_RESTART)`.
8. vfkit restarts. UEFI Boot Manager honours `BootOrder`, runs
   `<target>-vmlinuz.efi` from the cache ESP, distro kernel takes
   over via `ExitBootServices`.

You'll see the bootstrap kernel boot once, then the Trixie kernel
boots immediately on the second pass — no second `cloud-boot-init`
run.

## 5. `--gui` framebuffer console

vfkit can show the guest's framebuffer in a Cocoa window if the
guest kernel ships the right virtio-gpu drivers. The cloud kernel
variant already has them; see
[`kernel/cloud-arm64.config`](https://github.com/cloud-boot/kernel):

```
CONFIG_DRM_VIRTIO_GPU=y
CONFIG_FRAMEBUFFER_CONSOLE=y
CONFIG_VIRTIO_INPUT=y
```

```bash
vfkit --gui \
    --bootloader efi,variable-store=nvram.fd \
    --device virtio-blk,path=uki/boot.iso,readonly \
    --device virtio-blk,path=uki/menu-cache.raw \
    --device virtio-blk,path=trixie.raw \
    --device virtio-net,nat,mac=auto
```

A `vfkit` window opens showing the boot console. Useful for
keyboard-driven menus — `cloudboot.exit=reboot` without an
explicit `cloudboot.target=` puts the user at an interactive menu.

## 6. Reset to the menu

Once a target has been staged, the firmware will keep booting it.
To go back to the menu (e.g. to pick a different target), clear
the NVRAM:

=== "In-band (from inside the guest)"

    ```bash
    cd /sys/firmware/efi/efivars
    sudo efibootmgr -b 0001 -B
    sudo efibootmgr --bootorder $(efibootmgr | sed -n 's/^BootOrder: //p' | sed 's/0001,*//; s/,*0001//' )
    sudo reboot
    ```

    Or use the convenience script `uki/scripts/reset-cloud-boot.sh`.

=== "Out-of-band (delete the vfkit variable store)"

    ```bash
    rm nvram.fd
    # next boot will start from the menu since BootOrder is empty
    ```

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| `vfkit: error: bootloader: file not found: nvram.fd` | First run — vfkit creates `nvram.fd` automatically; re-run. |
| Bootstrap kernel hangs at `[init] virtio-net: timeout` | Plan URL unreachable. Confirm `--device virtio-net,nat,mac=auto` is present, that the host has internet, and that the registry doesn't require auth that you haven't supplied. |
| Second-pass kernel doesn't appear, vfkit keeps re-booting the UKI | The Boot0001 write failed. Check `cloud-boot-init` logs in `vm.log`. Common cause: `menu-cache.raw` doesn't have the `cloud-boot-cache` partition label. Re-run `make-cache-disk.sh`. |
| `findESPDevice: no partition labelled cloud-boot-cache` | Same as above. |

## Next

- [OpenStack with Keystone AC](openstack.md) for production plans.
- [Reference / NVRAM reset](../reference/nvram-reset.md) for the
  full ceremony.
- [Filesystem drivers / RAID discovery](../filesystems/raid.md) for
  what happens when the chained distro is on btrfs RAID or ZFS
  raidz.
