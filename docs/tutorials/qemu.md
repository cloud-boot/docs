---
title: Quickstart — QEMU/KVM
---

# Quickstart — QEMU/KVM

The fastest path to a working boot. We'll build a UKI for aarch64
on a Mac (cross-compile) and watch QEMU `kexec` straight into
Debian Trixie. The same flow works for x86_64 — just swap the
arch flag.

## 1. Build the UKI

```bash
# from the cloud-boot repo root
cd uki
go run . build \
    --arch aarch64 \
    --out  boot.efi \
    --cmdline "cloudboot.exit=kexec cloudboot.disk=/dev/vda2"
```

What `cloud-boot build` does, in order:

1. Cross-compiles `init/cmd/cloud-boot-init` for `linux/<arch>`,
   statically linked, no cgo.
2. Builds a CPIO initramfs containing just that binary at `/init`.
3. Concatenates `stub.efi` (the TinyGo UEFI stub from
   `go-coff/stub`) with the cloud kernel and the initramfs into a
   PE/COFF binary.
4. PEC-signs (`go-coff/pec sign`) so secure-boot-enabled firmwares
   accept it.

The resulting `boot.efi` is a self-contained UKI — drop it where
the firmware expects to find `\EFI\BOOT\BOOTAA64.EFI` and you're
done.

## 2. Grab a distro image

```bash
curl -L -o trixie.qcow2 \
  https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-arm64.qcow2
```

Convert to raw if you prefer — QEMU reads either.

## 3. Boot

=== "aarch64 with OVMF"

    ```bash
    qemu-system-aarch64 \
        -machine virt -cpu max -m 2G \
        -bios QEMU_EFI.fd \
        -drive file=boot.efi,format=raw,if=virtio,readonly=on \
        -drive file=trixie.qcow2,format=qcow2,if=virtio \
        -nographic
    ```

=== "x86_64 with OVMF"

    ```bash
    qemu-system-x86_64 \
        -machine q35,accel=kvm -cpu host -m 2G \
        -bios /usr/share/OVMF/OVMF_CODE.fd \
        -drive file=boot.efi,format=raw,if=virtio,readonly=on \
        -drive file=trixie.qcow2,format=qcow2,if=virtio \
        -nographic
    ```

You should see (approximately):

```text
EFI stub: UKI loaded; jumping to cloud-boot-init …
[init] cloud-boot v… built …
[init] cmdline: cloudboot.exit=kexec cloudboot.disk=/dev/vda2 …
[init] opening disk /dev/vda2 (auto-detect fs)
[init] ext4: partition table = none; opened whole image
[init] disk-fs: reading kernel "/boot/vmlinuz-6.6.9-amd64" + initrd "/boot/initrd.img-6.6.9-amd64"
[init] disk-fs: staged kernel=… B initrd=… B; kexec'ing
[    0.000000] Booting Linux on physical CPU 0x0000000000 [0x000f0510]
[    0.000000] Linux version 6.6.9-amd64 (debian@buildd) …
```

The bootstrap kernel is gone; you're now running Debian Trixie's
own 6.6 kernel. Log in as `debian` (default cloud-image user) once
networking comes up.

## 4. (Optional) Use a network-served plan

For dynamic boot decisions, push the plan to an OCI registry and
point cloud-boot at it. The init binary speaks OCI natively.

```bash
# Write a plan
cat > prod.hcl <<'EOF'
default_target = "primary"

locals {
  registry = "ghcr.io/me/cloud"
  console  = arch == "arm64" ? "ttyAMA0" : "ttyS0"
}

target "primary" {
  version = "6.6"
  label   = "Production Linux ${self.version}"
  index   = "${local.registry}/linux:${self.version}"
  cmdline = "console=${local.console} ro root=/dev/vda1"
}

target "rescue" {
  arch    = "amd64"
  kernel  = "${local.registry}/rescue:latest"
  cmdline = ["console=${local.console}", "single", "rd.break"]
}
EOF

cloud-boot push plan ghcr.io/me/cloud-plan:prod -f prod.hcl

# Rebuild boot.efi pointing at the plan instead of a disk
cloud-boot build --arch aarch64 --out boot.efi \
  --cmdline "cloudboot.plan=oci://ghcr.io/me/cloud-plan:prod"

# Boot — init now pulls the plan over the network and kexecs into
# the kernel/initrd named by it.
qemu-system-aarch64 -machine virt -cpu max -m 2G \
    -bios QEMU_EFI.fd \
    -drive file=boot.efi,format=raw,if=virtio,readonly=on \
    -nic user,model=virtio-net-pci
```

The bootstrap kernel must reach the registry — that's why this
example uses QEMU's `-nic user` (user-mode networking with NAT).
For tighter control, swap in `-netdev tap,...`.

## 5. Multi-arch indexes

`cloud-boot push index` builds a manifest list so the same plan
target name resolves to `amd64`-vs-`arm64`-specific artifacts
automatically. Inside the HCL, `arch` is a [locals
scope](../reference/plan-hcl.md) variable, so:

```hcl
locals {
  console = arch == "arm64" ? "ttyAMA0" : "ttyS0"
}
```

resolves at plan-evaluation time on the booting guest.

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| `Failed to load BOOTAA64.EFI` | Wrong arch for the firmware. `-bios` and `--arch` must match. |
| `no kernel found at {/,/boot/}{vmlinuz-*,Image-*}` | The disk's rootfs partition layout differs. Set `cloudboot.disk.kernel=` and `.initrd=` explicitly. |
| Kernel panic after `kexec` | The bootstrap kernel doesn't support `kexec_file_load` on this arch. Confirm `CONFIG_KEXEC_FILE=y` in the kernel config. |

## Next

- [Apple VZ via vfkit](vfkit.md) for the Path C / `reboot(2)` flow.
- [OpenStack with Keystone AC](openstack.md) for per-instance
  metadata-driven plans.
- [Reference / cmdline](../reference/cmdline.md) for every
  `cloudboot.*` knob.
