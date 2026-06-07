---
title: ext4
---

# ext4

`github.com/go-filesystems/ext4`

Read/write ext4 filesystem driver in pure Go. The most common
chained-distro filesystem; Debian Trixie, Ubuntu Noble, and Alpine
3.21 all default to ext4.

## What works

- Single-device images, raw or wrapped in qcow2 / UDIF.
- Partition table auto-detection — MBR + GPT, with `partIndex = -1`
  meaning "first Linux data partition".
- **Journal replay on open** — `j.ReplayOnOpen` runs at `Open()` time
  when a journal is present, so cloud-boot reads consistent state
  even when the disk was last unmounted uncleanly.
- File lookups, directory listings, `Stat`, `ReadFile`, `ReadLink`.

## API

```go
import fsext4 "github.com/go-filesystems/ext4"

// Open an ext4 image by path.
fs, err := fsext4.Open("/dev/vda3", -1) // -1 = whole image, no partition table
defer fs.Close()

// Or open from a layered BlockBackend (LUKS, qcow2, in-memory).
fs, err := fsext4.OpenFromDevice(dev, -1)
defer fs.Close()

// Use the common filesystem.Filesystem interface.
data, err := fs.ReadFile("/boot/vmlinuz-6.6.9-amd64")
entries, err := fs.ListDir("/boot")
stat, err := fs.Stat("/boot/vmlinuz-6.6.9-amd64")
```

## cloud-boot integration

In a HCL plan, an ext4-rooted disk target looks like:

```hcl
target "primary" {
  disk = {
    device = "/dev/vda3"     # the partition with the rootfs
    fs     = "ext4"
    kernel = "/boot/vmlinuz" # optional — falls back to newest glob
    initrd = "/boot/initrd"  # same
  }
}
```

If `kernel` and `initrd` are unset, `cloud-boot-init` picks the
lexicographically largest match in `/boot/{vmlinuz,Image}-*` and
pairs it with the matching `initrd.img-`/`initramfs-` by suffix.

## LUKS overlay

For LUKS-encrypted ext4, see [LUKS](luks.md). cloud-boot detects the
LUKS magic at offset 0, unlocks via `cloudboot.disk.luks-passphrase=`
(typically supplied through `cloudboot.metadata.url=` to avoid
leaking via `/proc/cmdline`), and opens ext4 on top of the
plaintext `*luks.Device`.

## Not supported

- Multi-device ext4. ext4 doesn't have native multi-device support
  — use `md` (Linux software RAID) or `dm` underneath instead.
  Once the lower layer presents a single block device, cloud-boot
  opens ext4 on top of it the same way.
- Online resize-during-open. Resizing the FS while cloud-boot reads
  it isn't a use case (cloud-boot is read-only at boot time).
