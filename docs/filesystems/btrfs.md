---
title: btrfs
---

# btrfs

`github.com/go-filesystems/btrfs`

Read/write btrfs filesystem driver in pure Go. Single-device **and**
multi-device — all six common profiles (`single`, `raid0`, `raid1`,
`raid10`, `raid5`, `raid6`) verified against real `mkfs.btrfs`
output.

## Single-device API

```go
import fsbtrfs "github.com/go-filesystems/btrfs"

fs, err := fsbtrfs.Open("/dev/vda3", -1)
defer fs.Close()

// Or layered:
fs, err := fsbtrfs.OpenFromDevice(dev, -1)
defer fs.Close()
```

For a SINGLE / DUP / RAID1 / RAID1Cn / RAID10 leg, the single-device
opener works — it reads `dev_item.devid` from the superblock and
picks the chunk stripe whose `devid` matches. Each chunk has a copy
of the data on every mirror leg, so any one leg can serve all
reads.

For RAID0 / RAID5 / RAID6 the single-device opener returns a clear
"logical address not in any known chunk" error — those profiles
have data striped across multiple devices and need
`OpenFromDevices`.

## Multi-device API

```go
import fsbtrfs "github.com/go-filesystems/btrfs"

// Open multiple legs of a multi-device pool.
devs := []fsbtrfs.BlockBackend{
    &osFileBackend{f: file0},
    &osFileBackend{f: file1},
    &osFileBackend{f: file2}, // raidz1: 3 legs
}
fs, err := fsbtrfs.OpenFromDevices(devs, -1)
defer fs.Close()
```

The lib builds an internal `devicePool` that routes reads
per-chunk based on the chunk's profile bits (`chunkType`) and its
stripe array. Algorithm reference: `fs/btrfs/volumes.c:btrfs_map_block`
(Linux kernel v6.12).

### Profile dispatch

| Profile bit | Mechanism |
| --- | --- |
| `BTRFS_BLOCK_GROUP_RAID0` (0x08) | `readStriped(nparity=0)` — stripe N goes to device `N % numStripes` at offset `(N / numStripes) * stripeLen + stripeOff`. |
| `BTRFS_BLOCK_GROUP_RAID1` (0x10) | `readSingleOrMirror` — every stripe is a mirror copy; try each in order. |
| `BTRFS_BLOCK_GROUP_DUP` (0x20) | `readSingleOrMirror` with 2 stripes on the same device. |
| `BTRFS_BLOCK_GROUP_RAID10` (0x40) | `readRAID10` — `groups = num/sub_stripes`; pick group by `stripe_nr % groups`, try each leg in the mirror group. |
| `BTRFS_BLOCK_GROUP_RAID5` (0x80) | `readStriped(nparity=1)` — parity column position rotates LEFT per stripe row. |
| `BTRFS_BLOCK_GROUP_RAID6` (0x100) | `readStriped(nparity=2)` — two rotating parity columns. |

For RAID5/6 the **healthy-read** path skips the parity columns and
reads only the data columns in column order. Degraded reads (one
or more legs missing) would need Reed-Solomon reconstruction, which
isn't shipped yet.

## cloud-boot multi-leg discovery

cloud-boot-init's `findBtrfsLegs` (in
`init/cmd/cloud-boot-init/disk_btrfs_linux.go`) auto-discovers
multi-device btrfs pools at boot:

1. Read the primary device's superblock (`/proc/cmdline:cloudboot.disk=`).
2. Extract the 16-byte `fsid` from the superblock at offset `0x20`.
3. Walk `/sys/block` via `listWholeDisks()`.
4. For each candidate, read the magic at superblock offset `0x40`
   (`_BHRfS_M`) — if it matches, compare the fsid.
5. Group matching devices, hand the slice to `fsbtrfs.OpenFromDevices`.

## On-disk format bugs the lib had to fix

The btrfs driver was originally written against its own
self-consistent `Format()` output and never validated against
real `mkfs.btrfs` images. Reading real images surfaced three
on-disk format mismatches, all fixed in 2026-05-21:

1. **`chunkStripeSize` was `0x60` (96)** — the real on-disk
   `btrfs_stripe` is **32 bytes** (`devid:8 + offset:8 +
   dev_uuid:16`). The 96-byte assumption made every multi-stripe
   chunk's stripe array misalign.
2. **Node header layout was off by 8 bytes** — the lib was reading
   `nritems` / `level` at offsets `0x58` / `0x60`. The real layout
   has `owner` at `0x58` then `nritems` at `0x60` (uint32) and
   `level` at `0x64` (uint8).
3. **`root_item.bytenr` was read at offset 0** — the real position
   is `0xB0`, after the embedded `inode_item` (160 bytes) +
   `generation` (8) + `root_dirid` (8).

Once these landed, the existing `Format()`-based self-tests were
updated to match the real layout. See [Internals / on-disk format
fixes](../internals/on-disk-formats.md) for the full story.

## cloud-boot integration

```hcl
target "fedora" {
  disk = {
    device = "/dev/vda3"
    fs     = "btrfs"
  }
}
```

For a RAID5 install:

```hcl
target "btrfs-raid5" {
  disk = {
    # any one leg — cloud-boot-init finds the others via fsid scan
    device = "/dev/vda3"
    fs     = "btrfs"
  }
}
```

## LUKS overlay

`luksAsBTRFSBackend` in cloud-boot-init wraps an unlocked
`*luks.Device`; `fsbtrfs.OpenFromDevice(adapter, -1)` reads btrfs
on top of the plaintext. Currently the LUKS-on-btrfs path uses the
single-leg opener; multi-leg LUKS-on-RAID-btrfs would need each leg
unlocked individually and fed through `OpenFromDevices`.
