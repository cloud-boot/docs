---
title: Components
---

# Components

cloud-boot is a [GitHub organisation](https://github.com/cloud-boot)
that holds four core repositories plus the sibling orgs they consume.
This page is the "what does each thing actually do" reference.

## The core repos

### `init/` — PID 1 in the initramfs

[github.com/cloud-boot/init](https://github.com/cloud-boot/init)

Go binary embedded in the bootstrap initramfs. Runs as PID 1 once the
UKI hands off. Owns:

- **Two terminal sinks**, gated by `cloudboot.exit=`:
    - `kexec` (Path A) — calls `kexec_file_load(2)` then
      `kexec(LINUX_REBOOT_CMD_KEXEC)`.
    - `reboot` (Path C) — stages `\EFI\Linux\<target>-{vmlinuz,initrd}`
      on the cache disk, writes `Boot0001` + `BootOrder` via
      `efivarfs`, calls `reboot(LINUX_REBOOT_CMD_RESTART)`.
- **Plan resolution** — HCL with `local.*` and `self.*` references,
  multi-arch OCI indexes, OpenStack Keystone application-credential
  auth with the bearer token reused for metadata + OCI.
- **Disk-mode openers** for every filesystem the chained distro
  might use: ext4, XFS, btrfs (single + multi-device RAID0/1/10/5/6
  via `fsid` discovery), ZFS (single + mirror via single-leg open,
  raidz1/2/3 via `OpenFromDevices`).
- **LUKS1/LUKS2 unlock** (`github.com/go-fde/luks`) — ext4 or ZFS on
  top of LUKS, passphrase via metadata-URL to keep
  `/proc/cmdline` clean.
- **ZFS native encryption** via `github.com/go-crypto/zfscrypt`
  (AES-CCM/GCM, PBKDF2-HMAC-SHA1 wrap, HKDF-SHA512 per-block).
- **Metadata-URL overrides** (`cloudboot.metadata.url=`) — pull a
  JSON `cloudboot` block from any HTTP endpoint, no `boot.iso`
  rebuild needed when the plan moves.
- **Console keymap** support (`cloudboot.keymap=fr` / `fr-mac` / `us`).
- DHCP, LLDP, cosign — all the network / signing primitives.

### `uki/` — Host-side toolchain

[github.com/cloud-boot/uki](https://github.com/cloud-boot/uki)

Cobra CLI plus helper scripts. The CLI exposes:

| Command | What it does |
| --- | --- |
| `cloud-boot build` | Cross-compiles `init`, builds an initramfs, links a UKI (`stub.efi` + cloud kernel + initramfs), and emits a hybrid GPT/El-Torito ISO whose appended GPT partition 2 is byte-identical to the embedded FAT ESP. |
| `cloud-boot push artifact` | Pushes a kernel/initrd artifact as an OCI blob. |
| `cloud-boot push plan` | Pushes an HCL plan as an OCI artifact. |
| `cloud-boot push index` | Builds a multi-arch OCI index (manifest list). |
| `cloud-boot push modpack` | Pushes a kernel-modules tarball alongside an artifact. |
| `cloud-boot label` | Writes ext4 volume labels on raw / qcow2 / UDIF-UDRW disks via [`go-diskimages`](https://github.com/go-diskimages/diskimage). |

Helper scripts:

- `uki/scripts/make-cache-disk.sh` — idempotent writable
  `menu-cache.raw` for Path C. Re-runs safely.
- `uki/scripts/reset-cloud-boot.sh` — in-band NVRAM cleaner to
  return to the menu after a staged target was set.

### `loader/` — Pure-UEFI bootloader (TinyGo)

[github.com/cloud-boot/loader](https://github.com/cloud-boot/loader)

TinyGo PE/COFF UEFI application. Stays inside Boot Services from
`BOOTAA64.EFI` to `StartImage`. Reads ext4 / XFS / btrfs from raw
block devices via the same `go-filesystems/*` libraries `init`
uses. Publishes `EFI_LOAD_FILE2_PROTOCOL` under
`LINUX_EFI_INITRD_MEDIA_GUID` so the Linux EFI stub picks up the
initrd via `LoadFile2`. Cmdline staged in the `CloudBootCmdline`
EFI variable, patched into `LoadedImage.LoadOptions` before
`StartImage`.

**Phases A–C committed**; Phases D–J (HTTP, OCI, DHCP, …) were
abandoned for Apple VZ after the VZ firmware was confirmed to ship
no `HTTP` / `TCP4` / `DHCP4` / `DNS4` protocols and virtio-net
rejects `FEATURES_OK` from any UEFI-context client. The loader
still ships the six-distro cascade on QEMU/OVMF and EDK2 hardware
where its protocol assumptions hold. The networked phases are
revisited by [`tamago-uefi/`](#tamago-uefi--pure-go-unikernel-bootloader-tamago)
below, which brings its own pure-Go virtio-net + netstack so it can
ignore the firmware's missing protocols.

### `tamago-uefi/` — Pure-Go unikernel bootloader (TamaGo)

[github.com/cloud-boot/tamago-uefi](https://github.com/cloud-boot/tamago-uefi)

The Phase 2 successor to `loader/`'s networked phases. Same shape
(PE32+/EFI app, lives in Boot Services until `StartImage`) but
written in **standard Go on the TamaGo bare-metal runtime**, not
TinyGo. Multi-arch (amd64 / arm64 / loong64 / riscv64) end-to-end,
with the full Go runtime (GC, scheduler, goroutines, `crypto/tls`,
`net/http`, `sync`) available pre-EBS. Implements `Path D` — see
the [TamaGo UEFI architecture page](tamago-uefi.md) for the
narrative and decisions, and the
[Phase 2 design doc](https://github.com/cloud-boot/docs/blob/main/tamago-uefi-phase2-oci-loader.md)
for milestone status + risk register.

### `iso/` — Multi-arch hybrid ISO assembler

[github.com/cloud-boot/iso](https://github.com/cloud-boot/iso)

Standalone CLI + library that packs one or more PE32+/EFI binaries
(`BOOTX64.EFI` / `BOOTAA64.EFI` / `BOOTLOONGARCH64.EFI` /
`BOOTRISCV64.EFI`) into a single hybrid iso9660 + El Torito + GPT
image with a FAT ESP. Generic — works for tamago-uefi unikernels,
classic UKIs, BSD `loader.efi`. The QEMU + EDK2 boot harness
(`pkg/multiarchboot`) drives `qemu-system-<arch>` against each
arch's matching OVMF and asserts the runtime banner; used by both
`tamago-uefi`'s e2e tests and `uki`'s future ISO-step.

### `kernel/` — Reproducible bootstrap kernel

[github.com/cloud-boot/kernel](https://github.com/cloud-boot/kernel)

No Go module — just Dockerfiles. Builds two variants per arch:

| Variant | Size | What's in it |
| --- | --- | --- |
| `disk-<arch>` | ~7-9 MiB | virtio + ext4 + kexec + GPT/MBR + simpledrm. The minimum needed for Path A. |
| `cloud-<arch>` | ~9-11 MiB | Adds `VFAT_FS`, `EFIVAR_FS`, `VIRTIO_CONSOLE`, `FUTEX`/`SIGNALFD`/`TIMERFD`/`EVENTFD`, `DRM_VIRTIO_GPU` + `FRAMEBUFFER_CONSOLE` + `VIRTIO_INPUT`. Used for Path C (including `vfkit --gui`). |

The `cloud-arm64` variant boots both QEMU virt and Apple VZ.

!!! note "FS modules"
    Since the [pivot to userland FS drivers](../filesystems/index.md),
    `CONFIG_EXT4_FS` / `CONFIG_XFS_FS` / `CONFIG_BTRFS_FS` are
    **dropped** from `disk-<arch>.config`. `CONFIG_VFAT_FS` stays
    (needed by Path C to write the reboot sink's ESP). ZFS is read
    entirely from userland.

## Sibling orgs

cloud-boot consumes a handful of pure-Go libraries grouped under
sibling GitHub orgs:

| Org | Repos | Role |
| --- | --- | --- |
| [`go-coff`](https://github.com/go-coff) | `pe`, `pec`, `stub` | PE/COFF library, CLI (`pec append` / `pec sign`), and the TinyGo UEFI stub the UKI starts at. |
| [`go-filesystems`](https://github.com/go-filesystems) | `ext4`, `xfs`, `btrfs`, `zfs`, `interface` | The userland FS drivers. Each one exposes `Open(path, partIndex)` + `OpenFromDevice(BlockBackend, partIndex)` + `OpenFromDevices([]BlockBackend, partIndex, ...)` for multi-device. See [Filesystem drivers](../filesystems/index.md). |
| [`go-fde`](https://github.com/go-fde) | `luks` | Pure-Go LUKS1/LUKS2 unlock — Argon2 / PBKDF2 / AES-XTS. |
| [`go-crypto`](https://github.com/go-crypto) | `ccm`, `zfscrypt` | AES-CCM (RFC 3610 / NIST SP 800-38C — stdlib only ships GCM) + ZFS native encryption (PBKDF2-HMAC-SHA1 wrap, HKDF-SHA512 per-block, AES-CCM/GCM AEAD). |
| [`go-diskimages`](https://github.com/go-diskimages) | `qcow2`, `diskimage` | qcow2 reader/writer + UDIF-UDRW reader for `cloud-boot label` on macOS disk images. |

## How they wire together

=== "Build time"

    ```mermaid
    flowchart LR
        K[kernel/<br>Image / bzImage] --> U[uki/cloud-boot build]
        I[init/<br>cloud-boot-init binary] --> U
        STUB[go-coff/stub<br>UEFI stub] --> U
        U -->|"PEC append + sign"| EFI[BOOTAA64.EFI]
        EFI -->|"hybrid GPT/El-Torito"| ISO[(boot.iso)]
        PLAN[plan.hcl] -->|"uki/cloud-boot push plan"| OCI[(OCI artifact)]
    end
    ```

=== "Run time (Path A)"

    ```mermaid
    flowchart LR
        FW[Firmware] --> EFI[BOOTAA64.EFI]
        EFI --> STUB[go-coff/stub<br>UEFI stub]
        STUB --> INIT[cloud-boot-init<br>PID 1]
        INIT --> PLAN["fetch plan via<br>cloudboot.plan=oci:// or<br>cloudboot.metadata.url"]
        PLAN --> DISK{Plan target}
        DISK -->|"target.disk"| FS["go-filesystems/*<br>open ext4/xfs/btrfs/zfs<br>read /boot/vmlinuz"]
        DISK -->|"target.kernel"| OCI[Pull OCI artifact]
        FS --> KEXEC[kexec_file_load + LINUX_REBOOT_CMD_KEXEC]
        OCI --> KEXEC
    end
    ```

=== "Run time (Path C)"

    ```mermaid
    flowchart LR
        FW[Firmware] --> EFI[BOOTAA64.EFI<br>same UKI]
        EFI --> INIT[cloud-boot-init<br>PID 1]
        INIT --> NET[virtio-net plan fetch<br>FEATURES_OK ok in kernel]
        NET --> MENU[menu UI or<br>cloudboot.target=]
        MENU --> STAGE["materialise on<br>menu-cache.raw FAT ESP:<br>\EFI\Linux\T-vmlinuz.efi<br>+ \EFI\Linux\T-initrd"]
        STAGE --> NVRAM["efivarfs:<br>Boot0001 = LoadOption(...)<br>BootOrder = [0001, ...existing]"]
        NVRAM --> REBOOT[reboot LINUX_REBOOT_CMD_RESTART]
        REBOOT --> FW
        FW -.->|next pass| TARGET["BootOrder honoured<br>\EFI\Linux\T-vmlinuz.efi<br>Linux EFI stub<br>ExitBootServices<br>distro userspace"]
    end
    ```
