---
title: Plan HCL schema
---

# Plan HCL schema

cloud-boot's plan is an HCL document. It declares one or more
**targets**, each describing how to obtain a kernel + initrd, plus
optional `local.*` and `default_target`. The whole document is
evaluated at boot time after the plan has been fetched from
`cloudboot.plan=`.

## Minimal example

```hcl
default_target = "primary"

target "primary" {
  index   = "ghcr.io/me/cloud/linux:6.6"
  cmdline = "console=ttyAMA0 ro root=/dev/vda1"
}
```

## Full example

```hcl
default_target = "primary"

locals {
  registry = "harbor.example.com/boot"
  console  = arch == "arm64" ? "ttyAMA0" : "ttyS0"

  # locals can reference each other but not self.*
  base_args = "console=${local.console} ro"
}

target "primary" {
  version = "6.6"
  label   = "Production Linux ${self.version}"
  index   = "${local.registry}/linux:${self.version}"
  cmdline = "${local.base_args} root=/dev/vda1"
  modpack = "${local.registry}/linux-modules:${self.version}"
}

target "rescue" {
  arch    = "amd64"
  kernel  = "${local.registry}/rescue:latest"
  cmdline = ["${local.base_args}", "single", "rd.break"]
}

target "alma" {
  label = "AlmaLinux 9 on XFS"
  disk = {
    device = "/dev/vda2"
    fs     = "xfs"
    kernel = "/boot/vmlinuz-5.14.0-503.el9_5.x86_64"
    initrd = "/boot/initramfs-5.14.0-503.el9_5.x86_64.img"
  }
  cmdline = "console=ttyS0 ro root=UUID=…"
}

target "proxmox" {
  label = "Proxmox VE on ZFS"
  disk = {
    device          = "/dev/vda3"
    fs              = "zfs"
    dataset         = "rpool/ROOT/pve-1"
    kernel          = "/boot/vmlinuz-6.5.13-1-pve"
    initrd          = "/boot/initrd.img-6.5.13-1-pve"
    luks-passphrase = "${meta.cloudboot.luks_passphrase}"
  }
  cmdline = "root=ZFS=rpool/ROOT/pve-1 boot=zfs"
}
```

## Top-level keys

| Key | Type | Meaning |
| --- | --- | --- |
| `default_target` | string | Name of the `target {}` block to use when `cloudboot.target=` is unset. |
| `locals { … }` | block | Named values reusable inside `target {}`. See [scoping](#scoping). |
| `target "<name>" { … }` | block(s) | One or more boot targets. |

## `target { }` attributes

A target can be either **OCI-sourced** (kernel/initrd pulled from a
registry) or **disk-sourced** (read from a chained distro's
filesystem). The opener picks the mode based on which attributes
are present.

| Attribute | Type | Meaning |
| --- | --- | --- |
| `label` | string | Human-readable name for the menu. Defaults to `name`. |
| `version` | string | Free-form version string, referenced via `self.version`. |
| `arch` | string | Optional arch filter — target is hidden on non-matching guests. Accepts `amd64`, `arm64`. |
| `cmdline` | string \| list(string) | Kernel command line. List members are space-joined. |
| **OCI source** | | |
| `index` | string | OCI manifest list (multi-arch). cloud-boot picks the matching arch. |
| `kernel` | string | Direct OCI artifact reference (used when `index` is absent). |
| `initrd` | string | Direct OCI artifact reference for the initrd. |
| `modpack` | string | Optional OCI artifact for matching kernel modules. |
| **Disk source** | | |
| `disk = { device = …, fs = …, kernel = …, initrd = …, dataset = …, luks-passphrase = … }` | block | All `cloudboot.disk.*` cmdline keys, available as plan attributes. See the cmdline reference for semantics. |

## Scoping

cloud-boot's HCL evaluator exposes the following scopes:

| Scope | Available where | Contains |
| --- | --- | --- |
| `arch` | locals + targets | Guest arch string: `amd64` or `arm64`. |
| `local.*` | inside `target { }` and other `locals { }` | Values defined in the top-level `locals { }` block. |
| `self.*` | inside the same `target { }` | The target's own attributes (e.g. `self.version`). Useful for templating: `"${local.registry}/linux:${self.version}"`. |
| `meta.*` | inside `target { }` | Parsed metadata-URL JSON (the entire response, not just the `cloudboot` block). Useful for `${meta.cloudboot.luks_passphrase}`. |

## Multi-arch OCI indexes

When you set `index = "..."` (not `kernel = "..."`), cloud-boot
issues a HEAD against the OCI ref to get the manifest list, then
picks the descriptor whose platform matches the guest. The
HCL never needs an `arch == …` branch for kernel selection — the
registry handles it via the manifest's `platform.architecture`.

```hcl
locals { registry = "harbor.example.com/boot" }
target "primary" {
  # one ref for both arm64 and amd64 — cloud-boot picks the right one
  index   = "${local.registry}/linux:6.6"
  cmdline = "console=${arch == "arm64" ? "ttyAMA0" : "ttyS0"}"
}
```

## Validation

Plan validation happens at boot. Errors abort the boot **before**
`kexec` / `reboot(2)`, so a syntactically broken plan can't strand
the guest in a half-staged state. The validator checks:

- `default_target` names a defined target.
- Every target has either an OCI source (`index` or `kernel`) **or**
  a `disk` block, never both.
- Disk-source `fs` is one of the supported four.
- `local.*` references resolve.
- `self.*` references are local to their own `target {}` block.
- `arch` values, when present, are `amd64` or `arm64`.
