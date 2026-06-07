---
title: metadata-URL JSON
---

# metadata-URL JSON

The per-instance metadata service is the **canonical place for
per-instance configuration**. cloud-boot fetches it once at boot
and treats values in the `cloudboot` block as overrides of the
kernel cmdline.

The URL defaults to the OpenStack metadata service
(`http://169.254.169.254/openstack/latest/meta_data.json`), but any
HTTP(S) endpoint serving a compatible JSON document works.

## Schema

```json
{
  "...": "// rest of metadata, untouched",
  "cloudboot": {
    "plan":   "harbor.example.com/boot/prod:latest",
    "target": "primary",
    "exit":   "reboot",
    "keymap": "fr",

    "disk": {
      "device":          "/dev/vda3",
      "fs":              "zfs",
      "dataset":         "rpool/ROOT/pve-1",
      "kernel":          "/boot/vmlinuz-6.5.13-1-pve",
      "initrd":          "/boot/initrd.img-6.5.13-1-pve",
      "luks_passphrase": "correct horse battery staple"
    },

    "openstack": {
      "auth_url":        "https://keystone.example.com:5000/v3",
      "app_cred_id":     "13af9ad7e9...",
      "app_cred_secret": "S3cr3t!..."
    },

    "net": {
      "skip":   false,
      "static": "10.0.0.5/24:10.0.0.1:1.1.1.1",
      "lldp":   true
    }
  }
}
```

## Key map — JSON → cmdline equivalent

| JSON path | Cmdline key |
| --- | --- |
| `cloudboot.plan` | `cloudboot.plan=` |
| `cloudboot.target` | `cloudboot.target=` |
| `cloudboot.exit` | `cloudboot.exit=` |
| `cloudboot.keymap` | `cloudboot.keymap=` |
| `cloudboot.disk.device` | `cloudboot.disk=` |
| `cloudboot.disk.fs` | `cloudboot.disk.fs=` |
| `cloudboot.disk.dataset` | `cloudboot.disk.device=` (for ZFS — the cmdline key carries the dataset path) |
| `cloudboot.disk.kernel` | `cloudboot.disk.kernel=` |
| `cloudboot.disk.initrd` | `cloudboot.disk.initrd=` |
| `cloudboot.disk.luks_passphrase` | `cloudboot.disk.luks-passphrase=` |
| `cloudboot.openstack.auth_url` | `cloudboot.openstack.auth-url=` |
| `cloudboot.openstack.app_cred_id` | `cloudboot.openstack.app-cred-id=` |
| `cloudboot.openstack.app_cred_secret` | `cloudboot.openstack.app-cred-secret=` |

JSON keys use **snake_case** (`luks_passphrase`, `auth_url`); cmdline
keys use **kebab-case** (`luks-passphrase`, `auth-url`). The
mapping is fixed; cloud-boot doesn't accept either form in either
context.

## Why this exists

1. **Per-instance secrets**. LUKS passphrases, Keystone application
   credentials, ZFS encryption keys — none of these should appear
   in `/proc/cmdline` (which is world-readable). The metadata-URL
   keeps them in the per-instance metadata blob, which is normally
   gated by the cloud's metadata service ACLs.
2. **Per-instance configuration without rebuilding the ISO**. The
   `boot.iso` stays immutable across plan changes. Operator pushes
   a new `meta_data.json` (or changes the Heat template, or
   updates the `user-data` field) → next reboot picks up the
   change. No Glance churn.
3. **Rotating credentials**. Same point: rotate the Keystone AC,
   update the JSON, reboot — done.

## Loading the JSON

cloud-boot-init fetches the URL at most once per boot. The fetch
uses the Go `net/http` client with a 10-second timeout. If the
Keystone AC fields are set in either cmdline or metadata, the
bearer token is acquired BEFORE the GET so the metadata service
can be protected by Keystone too (some clouds gate it that way).

If the fetch fails, cloud-boot logs a warning and falls back to
cmdline-only values. That's deliberate: a misconfigured or
unreachable metadata service shouldn't strand the instance from
booting at all — the cmdline acts as the safe fallback.

## Variable references from HCL

Plans can reference the metadata-URL response via the `meta.*`
scope:

```hcl
target "proxmox-fde" {
  disk = {
    device          = "/dev/vda3"
    fs              = "zfs"
    dataset         = "rpool/ROOT/pve-1"
    luks-passphrase = "${meta.cloudboot.luks_passphrase}"
  }
}
```

`meta.*` resolves against the **entire** metadata response — not
just the `cloudboot` block — so existing keys like
`meta.instance_id` and `meta.availability_zone` are available too.
This is useful for keying secrets per-host:

```hcl
locals {
  secret_key = "luks_passphrase_${meta.instance_id}"
}
target "fde" {
  disk = {
    luks-passphrase = lookup(meta.cloudboot.secrets, local.secret_key, "")
  }
}
```
