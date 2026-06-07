---
title: Tutorials
---

# Tutorials

Three end-to-end walk-throughs, one per hypervisor target.

<div class="grid cards" markdown>

-   :material-rocket-launch:{ .lg .middle } [__QEMU/KVM__](qemu.md)

    ---

    The fastest path to a working boot. Build a UKI, point QEMU at
    it, watch it `kexec` straight into Debian Trixie. Both x86_64
    and aarch64 in five minutes.

-   :material-apple:{ .lg .middle } [__Apple VZ via vfkit__](vfkit.md)

    ---

    The Path C production flow on Apple Silicon. `boot.iso`
    (read-only) + `menu-cache.raw` (writable ESP) + virtio-net
    plan fetch + `efivarfs` write + `reboot(2)`. The full
    menu-then-reboot dance.

-   :material-cloud:{ .lg .middle } [__OpenStack with Keystone AC__](openstack.md)

    ---

    Push a `boot.iso` into Glance once, then push different plans
    via `cloudboot.metadata.url` per instance. Keystone application
    credentials give per-instance scoped tokens for both metadata
    and OCI registry pulls.

</div>

## What you'll need

- **Go 1.25+** to cross-compile `cloud-boot-init`.
- **TinyGo 0.39+** to build the UEFI stub and the loader (Path B
  only).
- **mtools** / **xorriso** / **dosfstools** to assemble the hybrid
  GPT/El-Torito ISO. The `uki/cloud-boot build` command shells out
  to these.
- For Path A on KVM: a recent **QEMU** (>= 8.0) with OVMF firmware
  for your guest arch.
- For Path C on Apple VZ: **vfkit** from
  [Crc-org/vfkit](https://github.com/crc-org/vfkit) — `brew install
  vfkit` on macOS.
- For OpenStack: a project + an **application credential** (id +
  secret) with permission to read the per-instance metadata and
  pull from your OCI registry.

## Where things live

```
cloud-boot/
├── init/                # Go PID-1 binary
├── uki/                 # host-side build tool ("cloud-boot")
├── loader/              # pure-UEFI bootloader (Path B)
├── kernel/              # bootstrap kernel Dockerfiles
└── docs/                # this site
```

All Go modules use `replace` directives during local development so
the four core repos plus the `go-coff` / `go-filesystems` /
`go-crypto` / `go-fde` siblings coexist in one workspace. Once
published to the `cloud-boot` org on GitHub the modules resolve
via Go's normal `go.mod` flow.
