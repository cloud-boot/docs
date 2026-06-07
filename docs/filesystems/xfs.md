---
title: XFS
---

# XFS

`github.com/go-filesystems/xfs`

Read/write XFS filesystem driver in pure Go. Single-device only —
XFS doesn't ship native multi-device support; layered software RAID
(`md`/`dm`) gives you that one block-device hop below.

## What works

- XFS **v5 superblock format only** (the default since 2014).
- MBR/GPT auto-detect via `partIndex = -1`.
- Inode lookups, directory listings, `Stat`, `ReadFile`, `ReadLink`.
- `MkDir` / `WriteFile` / `Rename` / `DeleteFile` / `DeleteDir`.
- `GrowTo` for resize on top of a growing block device.

## API

```go
import fsxfs "github.com/go-filesystems/xfs"

fs, err := fsxfs.Open("/dev/vda2", -1)
defer fs.Close()

// Or layered:
fs, err := fsxfs.OpenFromDevice(dev, -1)
defer fs.Close()
```

The `BlockBackend` interface is the same shape across all four
filesystems — uniform across `go-filesystems/*` so a single adapter
(e.g. cloud-boot-init's `luksAsXFSBackend`) feeds any FS.

## cloud-boot integration

AlmaLinux 9 cloud images ship XFS for both `/boot` and `/`:

```hcl
target "alma" {
  disk = {
    device = "/dev/vda2"
    fs     = "xfs"
  }
}
```

## LUKS overlay

LUKS-on-XFS works the same way as LUKS-on-ext4 — see
[LUKS](luks.md). The `luksAsXFSBackend` adapter in
`init/cmd/cloud-boot-init/disk_luks_linux.go` wraps the unlocked
`*luks.Device`; `fsxfs.OpenFromDevice(adapter, -1)` opens the
plaintext partition.

## Not supported

- XFS v4 (pre-2014). All current cloud images use v5.
- Multi-device XFS (it doesn't exist).
- Realtime device (`-r`) — rare on cloud images.
