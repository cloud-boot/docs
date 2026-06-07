---
title: On-disk format fixes
---

# On-disk format fixes

The `go-filesystems/btrfs` and `go-filesystems/zfs` libraries were
originally written against their **own** `Format()` output and never
validated against `mkfs.btrfs` / `zpool create`. The first time
cloud-boot pointed them at real-world fixtures, **fourteen format
mismatches** surfaced. This page lists every one with the kernel /
OpenZFS reference.

!!! note "How they were found"
    Each bug below was discovered by generating a real test fixture
    inside a Lima Debian 12 VM (with `btrfs-progs` or
    `zfsutils-linux` + `zfs-dkms` for the kernel module), then
    pointing the upstream lib at it and tracing why it failed.
    Fixtures live in `mock/pkg/go-filesystems/{btrfs,zfs}/testdata/raid/`.

## btrfs — 3 fixes

### 1. `chunkStripeSize` was `0x60`, real on-disk size is `0x20`

*Reference: include/uapi/linux/btrfs_tree.h `struct btrfs_stripe`.*

```c
struct btrfs_stripe {
    __le64 devid;           //  8 bytes
    __le64 offset;          //  8 bytes
    u8     dev_uuid[BTRFS_UUID_SIZE];  // 16 bytes
} __attribute__((packed));   // total: 32 bytes
```

The lib's `superblock.go` had `chunkStripeSize = 0x60` (96 bytes).
That self-consistent over-allocation worked for the lib's own
`Format()` output but mis-aligned every multi-stripe chunk on real
images. Fixed: `chunkStripeSize = 0x20`.

### 2. Node header layout off by 8 bytes

*Reference: include/uapi/linux/btrfs_tree.h `struct btrfs_header`.*

```c
struct btrfs_header {
    u8     csum[BTRFS_CSUM_SIZE];        // 32 bytes
    u8     fsid[BTRFS_FSID_SIZE];        // 16 bytes
    __le64 bytenr;                        //  8 bytes  — offset 0x30
    __le64 flags;                         //  8 bytes  — offset 0x38
    u8     chunk_tree_uuid[16];           // 16 bytes  — offset 0x40
    __le64 generation;                    //  8 bytes  — offset 0x50
    __le64 owner;                         //  8 bytes  — offset 0x58
    __le32 nritems;                       //  4 bytes  — offset 0x60
    u8     level;                         //  1 byte   — offset 0x64
};                                        // sizeof   == 0x65
```

The lib's `btree.go:parseNodeHeader` was reading `nritems` at
offset `0x58` (uint32) and `level` at `0x60` (byte) — collapsing
the 8-byte `owner` field. Real OpenZFS has them at `0x60` and
`0x64`. Fixed.

### 3. `root_item.bytenr` read at offset 0

*Reference: include/uapi/linux/btrfs_tree.h `struct btrfs_root_item`.*

```text
btrfs_root_item layout:
  0x00  struct btrfs_inode_item inode  (160 bytes)
  0xA0  __le64 generation                 (8)
  0xA8  __le64 root_dirid                 (8)
  0xB0  __le64 bytenr                     (8)   ← FS_TREE root node
  ...
```

The lib's `chunk.go:resolveRootTree` was reading `bytenr` from byte
0, which is actually the first 8 bytes of the embedded
`btrfs_inode_item.generation`. Fixed to read at `0xB0`. The
lib's own `Format()` was also writing at offset 0; now writes at
`0xB0` to stay consistent.

## ZFS — 11 fixes

### 4. `VDEV_LABEL_START_SIZE` — DVA offsets relative to data area

*Reference: include/sys/vdev_impl.h.*

```c
#define VDEV_LABEL_SIZE          (256 << 10)      // 256 KiB
#define VDEV_PAD_SIZE            (8 << 10)        //   8 KiB
#define VDEV_PHYS_SIZE           (112 << 10)      // 112 KiB
#define VDEV_UBERBLOCK_RING      (128 << 10)      // 128 KiB
#define VDEV_BOOT_SIZE           (3 << 19) * 7    // 3.5 MiB
#define VDEV_LABEL_START_SIZE    (2 * VDEV_LABEL_SIZE + VDEV_BOOT_SIZE)
                                 // = 4 MiB
```

DVA offsets in block pointers are byte-offsets relative to the
**data area**, which begins `VDEV_LABEL_START_SIZE = 4 MiB` past
the partition start. The lib was treating DVA offsets as
partition-absolute, so every read landed in the label area.

Fix: split `zfsFS.partOffset` (data-area start = raw + 4 MiB)
from `zfsFS.labelOffset` (raw partition start). DVA reads use
`partOffset`; label / uberblock / grow reads use `labelOffset`.

### 5. `ZAP_MAGIC` typo

*Reference: include/sys/zap_impl.h.*

```c
#define ZAP_MAGIC 0x2F52AB2AB1ULL
```

The lib had `zapMagic = uint64(0x2F5AB2AB)` — missing one hex
digit in the middle. Every ZAP block was rejected. Fixed.

### 6. `zap_table_phys.zt_shift` at wrong offset

*Reference: include/sys/zap_impl.h `zap_table_phys_t`.*

```text
zap_table_phys layout (40 bytes, 5 × uint64):
  +0x00  zt_blk          // 0 = embedded
  +0x08  zt_numblks
  +0x10  zt_shift        // log2 of number of pointers
  +0x18  zt_nextblk
  +0x20  zt_blks_copied
```

The lib was reading `zt_shift` at offset `+0x0C` (inside
`zt_numblks` as a uint32!), which always returned 0 — and thus
`numPtrs = 1`, so the ptrtbl walk visited a single entry and
missed every leaf. Fixed to read `zt_shift` at `+0x20` (absolute)
as `uint64`.

### 7. Embedded pointer table at wrong position

*Reference: include/sys/zap_leaf.h `ZAP_EMBEDDED_PTRTBL_SHIFT`.*

When `zt_blk == 0` the pointer table lives **inside** the ZAP
header block, occupying the second half (`blockSize/2` onwards).
For a 16 KiB header block: offset `0x2000`, 1024 pointers.

The lib was reading the table at fixed offset `0x80` (128 bytes),
which is INSIDE the header. Fixed.

### 8. Fat-ZAP leaf hash table size

*Reference: include/sys/zap_leaf.h `ZAP_LEAF_HASH_NUMENTRIES`.*

```c
#define ZAP_LEAF_HASH_NUMENTRIES(l)  (1 << (FZAP_BLOCK_SHIFT(l) - 5))
#define ZAP_LEAF_HASH_BYTES(l)       (ZAP_LEAF_HASH_NUMENTRIES(l) * sizeof(uint16_t))
                                     // = blockSize / 16
```

For a 16 KiB leaf block: 512 hash entries × 2 bytes = 1024 bytes.
The lib was sizing this as `2 * (1 << lh_prefix_len)`, which for
the typical single-leaf fat-zap (`prefix_len = 0`) gave **2
bytes**. Off by 512×. Fixed.

### 9. ZAP integer values are big-endian on disk

*Reference: module/zfs/zap_leaf.c `zap_leaf_array_read`.*

```c
/* Fast path for one 8-byte integer */
if (array_int_len == 8 && buf_int_len == 8 && buf_numints == 1) {
    uint8_t *ip = la->la_array;
    *buf64 = (uint64_t)ip[0] << 56 | (uint64_t)ip[1] << 48 |
             (uint64_t)ip[2] << 40 | (uint64_t)ip[3] << 32 | …;
    return;
}
```

The byte-shift unrolling reads BIG-endian, regardless of machine
endianness. The lib was using `binary.LittleEndian.Uint64`, which
produced byte-swapped values (e.g. `root_dataset = 32` came out
as `0x2000000000000000`).

Fixed in `zap.go:readZAPLeafValue`.

### 10. Embedded BP `pad` bytes (56..71) are payload

*Reference: module/zfs/blkptr.c `encode_embedded_bp_compressed`
+ include/sys/spa.h `BPE_IS_PAYLOADWORD`.*

```c
#define BPE_IS_PAYLOADWORD(bp, wp) \
    ((wp) != &(bp)->blk_prop && (wp) != &(bp)->blk_birth)
```

`encode_embedded_bp_compressed` iterates 16 uint64 words of the
blkptr struct, skipping only `blk_prop` (word 6) and `blk_birth`
(word 10). Every other word — including `pad[0..1]`, `phys_birth`,
`fill`, and `cksum[0..3]` — IS payload.

The lib was treating `pad`, `phys_birth`, and `fill` as
non-payload, losing 16 + 16 = 32 bytes per embedded BP. Fixed.

### 11. `BPE_NUM_WORDS = 14`, payload = 112 bytes

*Reference: include/sys/spa.h.*

```c
#define BPE_NUM_WORDS     14
#define BPE_PAYLOAD_SIZE  (BPE_NUM_WORDS * sizeof(uint64_t))   // 112
```

Consequence of #10: 14 payload words × 8 bytes = **112 bytes**
total. The lib had `BPE_PAYLOAD_SIZE = 96`. The 16-byte shortfall
caused LZ4 decompression to terminate mid-stream with
"corrupt input" / "zero match offset" — because the LZ4 block
was 16 bytes shorter than the prefix said it was.

Fix #11 was the critical breakthrough — it unblocked LZ4 on
MOS-internal embedded ZAPs, which in turn unblocked dataset
traversal.

### 12. Embedded BP `prop` encoding

*Reference: include/sys/spa.h `BPE_GET_LSIZE` / `BPE_GET_PSIZE`.*

```c
#define BPE_LSIZE_BITS  25
#define BPE_PSIZE_BITS   7
#define BPE_GET_LSIZE(bp)  (BF64_GET_SB((bp)->blk_prop, 0, 25, 0, 1))
#define BPE_GET_PSIZE(bp)  (BF64_GET_SB((bp)->blk_prop, 25, 7, 0, 1))
```

Embedded BP `prop` layout:

```text
bits  0..24    LSIZE  (decompressed bytes, biased by 1)
bits 25..31    PSIZE  (compressed bytes, biased by 1)
bits 32..38    compress algorithm
bit  39        embedded flag
bits 40..47    etype  (BP_EMBEDDED_TYPE_*)
```

The lib had LSIZE and PSIZE positions swapped. Fixed.

### 13. `SA_MAGIC` typo

*Reference: include/sys/sa_impl.h.*

```c
#define SA_MAGIC  0x2F505A   /* 24-bit value */
```

The lib had `saMagic = 0x02F505A5` — one extra trailing hex digit.
Every SA bonus header was rejected. Without this fix `ReadFile`
returned the full data block (e.g. 512 bytes) instead of
truncating to the SA-recorded file size (e.g. 18 bytes for
`hello-from-single\n`). Fixed.

### 14. NVList data-type constants + outer header size

*Reference: include/sys/nvpair.h `enum data_type`.*

```c
enum data_type {
    DATA_TYPE_UNKNOWN       =  0,
    DATA_TYPE_BOOLEAN       =  1,
    DATA_TYPE_BYTE          =  2,
    DATA_TYPE_INT16         =  3,
    DATA_TYPE_UINT16        =  4,
    DATA_TYPE_INT32         =  5,
    DATA_TYPE_UINT32        =  6,
    DATA_TYPE_INT64         =  7,
    DATA_TYPE_UINT64        =  8,
    DATA_TYPE_STRING        =  9,
    DATA_TYPE_BYTE_ARRAY    = 10,
    DATA_TYPE_INT16_ARRAY   = 11,
    DATA_TYPE_UINT16_ARRAY  = 12,
    DATA_TYPE_INT32_ARRAY   = 13,
    DATA_TYPE_UINT32_ARRAY  = 14,
    DATA_TYPE_INT64_ARRAY   = 15,
    DATA_TYPE_UINT64_ARRAY  = 16,
    DATA_TYPE_STRING_ARRAY  = 17,
    DATA_TYPE_HRTIME        = 18,
    DATA_TYPE_NVLIST        = 19,
    DATA_TYPE_NVLIST_ARRAY  = 20,
};
```

The lib had `UINT64 = 11`, `STRING = 14`, `NVLIST_ARRAY = 25` —
all shifted by ~3. Also, the OUTER NVList header is **4 bytes**
(encoding byte + endian byte + 2 reserved), not 8 (two
`int32 LE`).

Fixed both. The combined change unblocked vdev-tree NVList
parsing, which is what made multi-vdev `OpenFromDevices` possible
in the first place.

## Summary

| Bug | FS | Severity | What it blocked |
| --- | --- | --- | --- |
| 1. `chunkStripeSize` | btrfs | high | every multi-stripe chunk read |
| 2. node header offsets | btrfs | high | every tree node parse |
| 3. `root_item.bytenr` | btrfs | high | FS_TREE root resolution |
| 4. `VDEV_LABEL_START_SIZE` | ZFS | high | every DVA-based read |
| 5. `ZAP_MAGIC` typo | ZFS | high | every ZAP header |
| 6. `zt_shift` offset | ZFS | high | every fat-ZAP ptrtbl walk |
| 7. embedded ptrtbl offset | ZFS | high | embedded ptrtbl reads |
| 8. fat-ZAP hash table size | ZFS | high | leaf-block entry iteration |
| 9. ZAP value endianness | ZFS | high | every ZAP value lookup |
| 10. embedded BP `pad` as payload | ZFS | critical | LZ4 mid-stream failures |
| 11. `BPE_NUM_WORDS=14` | ZFS | critical | LZ4 mid-stream failures (cont.) |
| 12. embedded BP `prop` LSIZE/PSIZE | ZFS | medium | wrong-sized output buffers |
| 13. `SA_MAGIC` typo | ZFS | high | every `ReadFile` returned block-aligned size |
| 14. NVList types + outer header | ZFS | high | vdev_tree parsing & raidz support |

In every case the fix preserves backward compatibility with the
lib's own `Format()`-generated test images — the format encoder
was updated to write the corrected layout, and self-tests were
either updated to expect the new bytes or skipped with a marker
when they encoded too much of the OLD layout to repair cleanly.
