---
title: Hypervisor matrix
---

# Hypervisor matrix

cloud-boot targets three hypervisor families. Each one allows a
different subset of UEFI protocols and kernel syscalls, which is why
**three** boot paths exist in the first place.

## At a glance

| Hypervisor | Path A · kexec | Path B · pure UEFI | Path C · reboot |
| --- | :---: | :---: | :---: |
| **QEMU/KVM** (x86_64 + aarch64) | ✓ | ✓ | ✓ |
| **Apple `Virtualization.framework`** (via `vfkit`) | ✗ trapped | ✗ no net | **✓ primary** |
| **OpenStack** (Nova + libvirt + KVM) | ✓ | ✓ | ✓ |

## QEMU/KVM

The development daily-driver. Both architectures, all three paths.

```bash
# Path A
qemu-system-aarch64 -machine virt -cpu max \
  -bios QEMU_EFI.fd \
  -drive file=boot.efi,format=raw \
  -drive file=trixie.qcow2,if=virtio
```

OVMF on QEMU/KVM ships the full UEFI protocol set we want — `HTTP`,
`TCP4`, `DHCP4`, `DNS4`, `BlockIO`, `SimpleFileSystem`,
`SimpleNetwork`, `LoadFile2`, etc. — so Path B works out of the
box. virtio-net negotiates `FEATURES_OK` happily from both UEFI and
Linux kernel contexts.

## Apple `Virtualization.framework` (via vfkit)

The **primary Apple-VZ target — Path C only**. Two hard constraints
make A and B impossible:

!!! warning "VZ traps `kexec_file_load(2)`"
    On Apple Silicon, `Virtualization.framework` silently fails the
    syscall. Any boot pipeline that relies on "small init → kexec
    into distro kernel" dead-ends on the Mac. That's why Path C
    exists: it writes UEFI `BootOrder` and calls `reboot(2)` so the
    next boot loads the target via the firmware's normal Boot
    Manager — which IS allowed.

!!! warning "VZ firmware ships almost nothing"
    OVMF-on-VZ exposes only `BlockIO`, `SimpleFileSystem`,
    `SimpleNetwork` — **no `HTTP`, `TCP4`, `DHCP4`, `DNS4`.**
    Worse, the virtio-net device rejects `FEATURES_OK` from any
    UEFI-context client. There is no way to fetch a plan from
    pure-UEFI. That's why the Path B loader's networked phases
    (D–J) were abandoned.

The Path C two-disk dance under vfkit:

```bash
# 1. immutable boot.iso + writable menu-cache.raw
cloud-boot build --arch aarch64 --iso boot.iso \
  --cmdline "cloudboot.exit=reboot cloudboot.plan=oci://…"
uki/scripts/make-cache-disk.sh menu-cache.raw 256

# 2. boot. init picks the target, stages
#    \EFI\Linux\T-* on menu-cache.raw, writes
#    Boot0001 + BootOrder via efivarfs, reboots.
vfkit --bootloader efi,variable-store=nvram.fd \
      --device virtio-blk,path=boot.iso,readonly \
      --device virtio-blk,path=menu-cache.raw \
      --device virtio-blk,path=debian-trixie.raw \
      --device virtio-net,nat,mac=auto
```

The `--gui` flag works once the cloud kernel variant carries
`DRM_VIRTIO_GPU` + `FRAMEBUFFER_CONSOLE` + `VIRTIO_INPUT` (the
`kernel/cloud-<arch>.config` already does).

## OpenStack

Nova + libvirt + KVM under the hood — same surface as plain
QEMU/KVM, with two operator-friendly conveniences on top.

### Keystone application credentials

Long-lived per-instance credentials, scoped to a project, rotatable
without touching the boot artifact. cloud-boot exchanges them at
boot time:

```text
cloudboot.openstack.auth-url        = https://keystone.example.com:5000/v3
cloudboot.openstack.app-cred-id     = $AC_ID
cloudboot.openstack.app-cred-secret = $AC_SECRET
```

The resulting bearer token is **reused** for:

- per-instance metadata service GETs
- pulls from Harbor (or any OCI registry with keystone-auth) for
  kernel/initrd OCI artifacts and HCL plan artifacts

### `cloudboot.metadata.url=` — push config without rebuilding the ISO

```bash
cloudboot.metadata.url=http://169.254.169.254/openstack/latest/meta_data.json
```

The JSON `cloudboot` block in the metadata response can carry the
plan reference, the active target, the exit mode, LUKS passphrases,
keymap, anything. The `boot.iso` stays immutable across rotations.

```json
{
  "cloudboot": {
    "plan":   "harbor.example.com/boot/prod:latest",
    "target": "primary",
    "exit":   "reboot",
    "keymap": "fr-mac"
  }
}
```

See [Reference / metadata-URL JSON](../reference/metadata-url.md) for
the full schema.

## Bare-metal UEFI

Path A works on every machine with a sane UEFI firmware that exposes
`kexec` from Linux. Path C is more interesting on bare metal: the
firmware's NVRAM is real and the `BootOrder` write survives across
power cycles, so a once-staged target keeps booting until you reset
the entry.

Path B depends on what the platform firmware exposes. EDK2-based
firmwares with full virtio-blk + ext4-readable BlockIO chains run
the loader's six-distro cascade fine. Less complete firmwares may
miss `LoadFile2` or `SimpleNetwork` and fall back to staging via
Path C anyway.

## The protocol matrix

What you get from each combination's UEFI:

| UEFI protocol | OVMF + QEMU | OVMF + VZ | EDK2 hardware |
| --- | :---: | :---: | :---: |
| `BlockIO` | ✓ | ✓ | ✓ |
| `SimpleFileSystem` | ✓ | ✓ | ✓ |
| `LoadFile2` | ✓ | ✓ (host-supplied only) | usually ✓ |
| `SimpleNetwork` | ✓ | ✓ but `FEATURES_OK` rejected | ✓ |
| `HTTP` / `TCP4` | ✓ | ✗ | varies |
| `DHCP4` / `DNS4` | ✓ | ✗ | varies |
| `RNG` | ✓ | ✓ | ✓ |
| `EFI_RUNTIME_SERVICES_GETVARIABLE` | ✓ | ✓ (efivarfs OK in Linux) | ✓ |

The "VZ" column is why Path C exists. The "QEMU" column is why the
loader's full cascade runs on KVM. The "EDK2 hardware" column
explains why Path B is the diagnostic surface — it depends on the
platform.
