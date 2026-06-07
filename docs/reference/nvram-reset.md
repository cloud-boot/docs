---
title: NVRAM reset
---

# NVRAM reset

Once a Path C target has been staged, the firmware's UEFI Boot
Manager keeps booting it on every subsequent reboot — because that's
exactly what `BootOrder` is supposed to do. To return the guest to
the **menu** state (so a different target can be picked), the
`Boot0001` variable and the matching `BootOrder` entry have to be
cleared.

cloud-boot ships two recipes for this: **in-band** (run from inside
the guest, useful for users) and **out-of-band** (run on the
compute host, useful for operators).

## In-band — `reset-cloud-boot.sh`

Source: [`uki/scripts/reset-cloud-boot.sh`](https://github.com/cloud-boot/uki/blob/main/scripts/reset-cloud-boot.sh)

```bash
#!/usr/bin/env bash
# Remove the cloud-boot-staged target from NVRAM so the next boot
# returns to the menu.
set -euo pipefail

# 1. Identify the cloud-boot-managed entry. We always write 0001.
sudo efibootmgr -b 0001 -B

# 2. Remove 0001 from BootOrder, preserving the rest.
order=$(efibootmgr | sed -n 's/^BootOrder: //p')
cleaned=$(echo "$order" | sed 's/0001,*//; s/,0001//')
sudo efibootmgr --bootorder "$cleaned"

# 3. Optional: also remove the cmdline EFI vars cloud-boot wrote.
sudo rm -f /sys/firmware/efi/efivars/CloudBootCmdline-*
sudo rm -f /sys/firmware/efi/efivars/CloudBootTarget-*

# 4. Reboot.
sudo reboot
```

The script is idempotent — it's safe to run when there's no
`Boot0001` entry, when `BootOrder` doesn't contain `0001`, or both.

Operator-friendly variant: run it as a systemd one-shot triggered
by a touch-file. The cloud-boot-init binary writes
`/var/lib/cloud-boot/reset-on-next-boot` if it sees a specific
metadata-URL key (`cloudboot.reset_on_next_boot: true`); the unit
notices, runs the script, deletes the touch-file.

## Out-of-band — libvirt

For operators with compute-node access (e.g. ops engineers driving
Heat / Magnum / Ironic), the NVRAM is a regular file on disk next
to the libvirt domain XML.

### virsh recipe

```bash
# 1. Find the domain.
virsh list --all | grep my-instance

# 2. Tear down the domain definition + its NVRAM.
sudo virsh undefine my-instance --keep-nvram=no

# 3. Re-define from the on-disk XML (libvirt won't auto-recreate
#    NVRAM if undefine deleted it).
sudo virsh define /etc/libvirt/qemu/my-instance.xml

# 4. Start the domain. Firmware re-initialises NVRAM with empty
#    BootOrder, so cloud-boot's menu shows on the next boot.
sudo virsh start my-instance
```

### qemu native

If you're driving QEMU directly (no libvirt), the NVRAM is the
file passed to `--bootloader efi,variable-store=<path>`. Delete it
and re-launch:

```bash
rm /var/run/cloud-boot/nvram-vm123.fd
qemu-system-aarch64 -bios QEMU_EFI.fd ... \
    -drive if=pflash,format=raw,file=/var/run/cloud-boot/nvram-vm123.fd
```

QEMU recreates the NVRAM template from `QEMU_VARS.fd` automatically
on first access.

### vfkit

vfkit's `--bootloader efi,variable-store=<path>` works the same
way:

```bash
rm nvram.fd
vfkit --bootloader efi,variable-store=nvram.fd ...
```

## What gets cleared, what doesn't

A `reset-cloud-boot.sh` run clears:

- `Boot0001` — the cloud-boot-managed LoadOption.
- `BootOrder` — the cloud-boot entry removed from the list.
- `CloudBootCmdline` — the cmdline string passed to Linux EFI stub.
- `CloudBootTarget` — the active target name.

It does **NOT** clear:

- The chained distro's own NVRAM entries (Debian, Fedora, etc.
  install their own `BootXXXX` entries pointing at
  `\EFI\debian\grubaa64.efi` and friends — those remain).
- `SetupVariables` / `BootCurrent` / `BootNext` — these are
  hypervisor-managed.
- Secure Boot variables (`db`, `dbx`, `KEK`, `PK`).

## How often does this matter?

- **Path A users** never run it — `kexec` leaves no NVRAM trail.
- **Path C users** run it when changing the active target. In
  steady-state production where the target rarely changes, it's
  invoked maybe once per maintenance window.
- The `cloudboot.target=<new-target>` cmdline knob is honoured on
  the **first** boot after a reset; subsequent boots without the
  knob fall through to the menu. So a sequence "reset → boot
  with `cloudboot.target=Y`" picks Y and stages it, no
  interactive step needed.
