---
title: Kernel cmdline knobs
---

# `cloudboot.*` cmdline knobs

Every value cloud-boot recognises on the kernel command line. All
keys are namespaced under `cloudboot.` so they coexist cleanly with
the rest of `/proc/cmdline`.

!!! tip "Where they come from"
    The cmdline is set at `cloud-boot build --cmdline …` time
    (baked into the UKI's `.cmdline` PE section) **and** can be
    overridden per-instance via `cloudboot.metadata.url=`. The
    metadata-URL JSON wins — see [Reference / precedence](index.md#precedence).

## Top-level control

| Key | Values | Meaning |
| --- | --- | --- |
| `cloudboot.exit` | `kexec` (default) · `reboot` | Which terminal sink to use. `kexec` = Path A; `reboot` = Path C (writes `BootOrder` and calls `reboot(2)`). |
| `cloudboot.target` | `<name>` | Name of the target to boot. Without this, the menu is shown (Path C) or the plan's `default_target` is used (Path A). |
| `cloudboot.keymap` | `us` (default) · `fr` · `fr-mac` | Console keymap loaded before the menu UI shows. |

## Disk-mode source

Pick exactly one source: `cloudboot.disk=…` (read kernel/initrd from
a chained distro's rootfs) or `cloudboot.plan=…` (pull plan from an
OCI registry).

### `cloudboot.disk` family

| Key | Meaning |
| --- | --- |
| `cloudboot.disk=` | The block device or partition holding the rootfs (e.g. `/dev/vda2`, `/dev/disk/by-label/cloudimg-rootfs`). |
| `cloudboot.disk.fs=` | Filesystem to open: `ext4` (default), `xfs`, `btrfs`, `zfs`. |
| `cloudboot.disk.device=` | For ZFS only: dataset path (e.g. `rpool/ROOT/pve-1`). The pool name is implicit (the device is set via `cloudboot.disk=`). |
| `cloudboot.disk.kernel=` | Absolute path of the kernel inside the rootfs. Default: lex-largest match of `/boot/{vmlinuz,Image}-*`. |
| `cloudboot.disk.initrd=` | Absolute path of the initrd. Default: pair the kernel's suffix with `/boot/{initrd.img-,initramfs-,initrd-}*`. |
| `cloudboot.disk.luks-passphrase=` | LUKS passphrase if the device is encrypted. **Prefer setting this via metadata-URL to keep it out of `/proc/cmdline`.** |

### `cloudboot.plan` family

| Key | Meaning |
| --- | --- |
| `cloudboot.plan=` | OCI reference for the HCL plan (e.g. `oci://ghcr.io/me/cloud-plan:trixie` or `harbor.example.com/boot/prod:latest`). |
| `cloudboot.plan.modpack=` | Optional: OCI reference for a kernel-modules tarball pinned to the same version as the kernel. |

## OpenStack / Keystone auth

For both metadata pulls and OCI registry pulls when the registry
supports keystone-auth.

| Key | Meaning |
| --- | --- |
| `cloudboot.openstack.auth-url=` | Keystone v3 endpoint, e.g. `https://keystone.example.com:5000/v3`. |
| `cloudboot.openstack.app-cred-id=` | Application credential id. |
| `cloudboot.openstack.app-cred-secret=` | Application credential secret. |

## metadata-URL override

| Key | Meaning |
| --- | --- |
| `cloudboot.metadata.url=` | HTTP(S) URL of a JSON document carrying a top-level `cloudboot` block. Default: `http://169.254.169.254/openstack/latest/meta_data.json` if running on OpenStack. |

The metadata-URL value **wins** over everything in the cmdline, so
this is the right place for per-instance secrets and rotating
configuration. See [metadata-URL JSON](metadata-url.md).

## Boot-time logging

| Key | Meaning |
| --- | --- |
| `cloudboot.debug=` | `1` to enable verbose init logs (chunk maps, plan resolution, label NVList decode). |
| `cloudboot.quiet=` | `1` to suppress the menu's startup banner. |

## DHCP / LLDP

The init binary attempts DHCP automatically on every virtio-net
interface; you don't normally need to configure anything. Two
escape hatches:

| Key | Meaning |
| --- | --- |
| `cloudboot.net.skip=` | `1` to skip DHCP entirely (use static IP via `cloudboot.net.static=`). |
| `cloudboot.net.static=` | `<ip>/<prefix>:<gateway>:<dns>` — only honoured when `skip=1`. |
| `cloudboot.net.lldp=` | `1` to send an LLDP frame announcing the cloud-boot version + the booting target name. Useful for switch-side inventory. |

## Examples

=== "Plain disk on QEMU"

    ```text
    cloudboot.exit=kexec
    cloudboot.disk=/dev/vda2
    ```

=== "ZFS-on-LUKS Proxmox"

    ```text
    cloudboot.exit=kexec
    cloudboot.disk=/dev/vda3
    cloudboot.disk.fs=zfs
    cloudboot.disk.device=rpool/ROOT/pve-1
    cloudboot.metadata.url=http://169.254.169.254/openstack/latest/meta_data.json
    # LUKS passphrase comes from the metadata JSON, never the cmdline
    ```

=== "OCI plan + Apple VZ"

    ```text
    cloudboot.exit=reboot
    cloudboot.plan=oci://ghcr.io/me/cloud-plan:prod
    cloudboot.target=primary
    ```

=== "OpenStack with Keystone AC"

    ```text
    cloudboot.exit=reboot
    cloudboot.openstack.auth-url=https://keystone.example.com:5000/v3
    cloudboot.openstack.app-cred-id=$AC_ID
    cloudboot.openstack.app-cred-secret=$AC_SECRET
    cloudboot.metadata.url=http://169.254.169.254/openstack/latest/meta_data.json
    ```
