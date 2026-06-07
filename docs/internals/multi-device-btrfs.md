---
title: btrfs multi-device routing
---

# btrfs multi-device routing

*Algorithm reference: `fs/btrfs/volumes.c:btrfs_map_block`,
Linux v6.12.*

This page walks the path a logical address takes when
`go-filesystems/btrfs` resolves it on a multi-device pool. The
implementation lives in
[`mock/pkg/go-filesystems/btrfs/multidev.go`](https://github.com/go-filesystems/btrfs/blob/main/multidev.go).

## Source files

| File | Role |
| --- | --- |
| `superblock.go` | Parses `chunk_item` entries (including the stripe array) and stores them in `superblock.sysChunks`. |
| `chunk.go` | Walks the chunk tree, appending freshly-discovered chunks to `sysChunks`. |
| `multidev.go` | The `devicePool` `blockBackend` wrapper that routes per-IO reads. |
| `fs.go` | `OpenFromDevices` builds the pool from a slice of backends. |

## Chunk → stripe array

A btrfs chunk maps a logical address range to one or more
**stripes**, each one (`devid`, `offset`) on a leaf device. The
chunk's profile bits in `chunkType` determine how to interpret the
stripe array on a read.

```text
btrfs_chunk_item (48 bytes header + N × 32 bytes stripe):
  0x00  __le64 length          // logical size
  0x10  __le64 stripe_len       // stripe size, e.g. 65536
  0x18  __le64 type             // profile bitmask (RAID0/1/10/5/6/SINGLE)
  0x2C  __le16 num_stripes
  0x2E  __le16 sub_stripes      // RAID10
  0x30  btrfs_stripe[]
        +0x00 devid
        +0x08 offset
        +0x10 dev_uuid (16 bytes)
```

After parsing, an in-memory `chunkMapping` carries everything
needed for routing:

```go
type chunkMapping struct {
    logStart       uint64
    size           uint64
    physStart      uint64        // legacy single-device fast path
    localStripeIdx int           // -1 if no stripe on the local device
    profile        uint64        // chunkType bitmask
    stripeLen      uint64
    subStripes     uint16
    stripes        []chunkStripe // {devID, offset} list
}

type chunkStripe struct {
    devID, offset uint64
}
```

## Per-profile dispatch

`devicePool.readFromChunk` switches on the chunk's profile bits
(masked with `blockGroupProfileMask`):

```go
profile := c.profile & blockGroupProfileMask
switch {
case profile == 0 || profile == blockGroupDup:
    return p.readSingleOrMirror(buf, logical, c)
case profile&(blockGroupRAID1|blockGroupRAID1C3|blockGroupRAID1C4) != 0:
    return p.readSingleOrMirror(buf, logical, c)
case profile == blockGroupRAID0:
    return p.readStriped(buf, logical, c, 0)  // nparity = 0
case profile == blockGroupRAID10:
    return p.readRAID10(buf, logical, c)
case profile == blockGroupRAID5:
    return p.readStriped(buf, logical, c, 1)  // nparity = 1
case profile == blockGroupRAID6:
    return p.readStriped(buf, logical, c, 2)  // nparity = 2
}
```

## SINGLE / DUP / RAID1 / RAID1C{3,4} — `readSingleOrMirror`

Every stripe in the chunk holds the same data. We try them in
order (preferring the local stripe to avoid waking a sibling
backend on healthy mirrors):

```go
inChunk := logical - c.logStart
for each stripe s (local first, then the others):
    if dev := devices[s.devID]; dev != nil:
        if read OK: return data
```

## RAID0 / RAID5 / RAID6 — `readStriped`

```go
stripeNr   := inChunk / stripeLen
stripeOff  := inChunk % stripeLen
numData    := numStripes - nparity
rowNr      := stripeNr / numData
colInRow   := stripeNr % numData

if nparity == 0 {
    // RAID0 — no parity, no rotation
    stripeIdx = colInRow
} else {
    // RAID5/6 — parity rotates LEFT per stripe row
    parityStart := (numStripes - nparity + rowNr) mod numStripes
    stripeIdx   := (parityStart + nparity + colInRow) mod numStripes
}

s     := chunk.stripes[stripeIdx]
dev   := devices[s.devID]
perDev := partOff + s.offset + rowNr*stripeLen + stripeOff
read at perDev
```

If the read crosses a stripe boundary the code splits it: read up
to the boundary on the current column, then recurse on the
remainder (which lands on the next column / row).

!!! note "Healthy-read only"
    `readStriped` reads ONLY data columns; the parity columns are
    skipped. If a data device is missing, the read fails — the
    parity columns aren't combined for reconstruction. That's a
    Reed-Solomon implementation (XOR for RAID5, GF(2^8) for
    RAID6) we haven't shipped yet.

## RAID10 — `readRAID10`

RAID10 stripes data across mirror PAIRS. With `num_stripes = N`
and `sub_stripes = 2`:

- `groups = N / 2` mirror pairs.
- `stripeNr % groups` picks the pair.
- Within the pair, any leg has the data — try each.

```go
groupIdx   := int(stripeNr % uint64(groups))
rowNr      := stripeNr / uint64(groups)
stripeBase := groupIdx * subStripes
perDev     := partOff + rowNr*stripeLen + stripeOff

for leg in [0, subStripes):
    s := chunk.stripes[stripeBase + leg]
    if dev := devices[s.devID]; dev != nil:
        read at perDev + s.offset
```

## Why the `partOff` shift

Stripe offsets in the chunk_item are relative to the **partition
start**, NOT the device file start. For partitioned images (the
GPT/MBR case where the btrfs partition is one slot among several)
`partOff` is the byte offset of the partition within the file; we
add it back before each `ReadAt`.

`chunk.go`'s `loadChunkTree` and `superblock.go`'s
`parseSysChunkArray` both populate `chunkMapping.stripes` from the
on-disk stripe array. The `partOff` adjustment happens at READ
time in `multidev.go`, not at parse time.

## Backward compatibility — `dev_item.devid = 0` test fixtures

The lib's own `Format()`-generated test fixtures historically wrote
`dev_item.devid = 0` in the superblock and `stripe[0].devid = 1`
in the chunk array — which would be inconsistent for real btrfs.
To avoid breaking those self-tests, `pickStripeForDevID` falls
back to `stripe[0]` when `sb.devID == 0`. Real `mkfs.btrfs` always
writes `dev_item.devid >= 1`, so the fallback never triggers on a
real image.

## Failure modes

| Symptom | Cause |
| --- | --- |
| `logical address X not in any known chunk` | Tried to read a RAID0/5/6 logical address whose stripe lives on a device that wasn't in the `OpenFromDevices` slice. Either the device is genuinely missing (degraded read, currently unsupported), or it wasn't discovered by `findBtrfsLegs` (fsid mismatch — see [Multi-device discovery](../filesystems/raid.md)). |
| `pickStripeForDevID returned ok=false` (via `chunk.go` skip) | Same as above, but in the single-leg fast path. The chunk wasn't added to `sysChunks`, so subsequent reads of its logical range fail with the message above. |
| Reads succeed but data is wrong | Suspect `stripeLen` parse or `chunkType` profile detection. The fixtures generated with known-good `mkfs.btrfs` are the ground truth here. |

## Test fixtures

`mock/pkg/go-filesystems/btrfs/testdata/raid/*.tar.zst` — six
profiles, ~70KB to 210KB compressed each. Generated in a Lima
Debian 12 VM:

```sh
truncate -s 128M d{0..N-1}.img
losetup --find --show d0.img …          # one loop dev per leg
mkfs.btrfs -d <profile> -m <profile> -L <profile> -f /dev/loopN …
mount /dev/loop0 /mnt/btr
echo hello-from-<profile> > /mnt/btr/hello.txt
dd if=/dev/urandom of=/mnt/btr/sub/blob.bin bs=1K count=64
umount /mnt/btr
losetup -D
tar c d*.img | zstd -19 > testdata/raid/<profile>.tar.zst
```

The test in `raid_multidev_test.go` extracts the tarball at test
time, opens all legs via `OpenFromDevices`, then verifies
`/hello.txt` content and `/sub/blob.bin` MD5.
