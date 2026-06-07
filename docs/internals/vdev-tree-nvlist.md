---
title: vdev label NVList
---

# vdev label NVList

The ZFS vdev label is the only piece of on-disk state that doesn't
go through the DMU. It carries the pool's identity, the
`vdev_tree` topology, and the uberblock ring. Multi-vdev support in
cloud-boot lives or dies on parsing this NVList correctly.

## Label layout

Each leaf device carries **four** identical labels: two at the
start, two at the end. Each label is 256 KiB. The layout:

```text
label (256 KiB):
    0x00000  pad                 (8 KiB)
    0x02000  boot env            (8 KiB)
    0x04000  phys (NVList)       (112 KiB)
    0x20000  uberblock ring      (128 KiB = 128 × 1 KiB slots)
```

The NVList region at offset `0x04000` is what we parse.

For a device with partition offset `partOff`, the labels live at:

```text
label 0:  partOff + 0
label 1:  partOff + 256 KiB
label 2:  partOff + (size - 512 KiB)
label 3:  partOff + (size - 256 KiB)
```

## XDR encoding

The NVList is XDR-encoded (`NV_ENCODE_XDR = 1`). Outer header is
**4 bytes**:

```text
+0  encoding byte  (1 = XDR)
+1  endian byte    (1 = LE)
+2  reserved
+3  reserved
```

Then the inner NVList header (8 bytes BE: `version` + `nvflags`),
then the nvpairs, then 8 zero bytes as a terminator.

Each nvpair:

```text
+0  encoded_size (int32 BE) — total bytes of this pair
+4  decoded_size (int32 BE) — ignored
+8  name_len     (int32 BE) — length of name including the null padding
+12 name         — padded to 4-byte boundary
+   type         (int32 BE) — DATA_TYPE_*
+4  nelem        (int32 BE) — number of elements
+   value        — type-dependent encoding
```

## What's in label 0's NVList

For a 3-device raidz1 (the test fixture):

```text
top-level NVList (13 pairs):
    version        = 5000           (uint64)
    name           = "tp3"          (string)
    state          = 1              (uint64)
    txg            = 38             (uint64)
    pool_guid      = 11561120…      (uint64)
    errata         = 0
    hostid         = 913955857
    hostname       = "lima-zfs-fix"
    top_guid       = 13844752…
    guid           = 2356164…       (uint64 — THIS leg's guid)
    vdev_children  = 1              (uint64)
    vdev_tree      = (nvlist nested)
    features_for_read = (nvlist nested)

vdev_tree (11 pairs):
    type            = "raidz"
    id              = 0
    guid            = 13844752…   (matches top_guid for the active root)
    nparity         = 1
    metaslab_array  = 256
    metaslab_shift  = 25
    ashift          = 9
    asize           = 388497408
    is_log          = 0
    create_txg      = 4
    children        = (nvlist array, 3 elements)

children[0]: type="file", id=0, guid=2356164…, path="…/d0.img"
children[1]: type="file", id=1, guid=6304913…, path="…/d1.img"
children[2]: type="file", id=2, guid=8843504…, path="…/d2.img"
```

`top_guid` matches the parent `vdev_tree.guid` so cloud-boot can
distinguish the root vdev's identity from any one leaf's identity.

## How cloud-boot-init uses it

`fszfs.ProbeLabel` decodes the label NVList without opening the FS:

```go
info, _ := fszfs.ProbeLabel(devReader, partOff)
// info.PoolName, info.PoolGUID, info.ThisGUID
// info.Type ("raidz" / "mirror" / "file" / …)
// info.NParity, info.Ashift
// info.LeafGUIDs[] (in vdev-id order)
```

`findZFSVdevs` walks `/sys/block`, probes each candidate, groups
by `PoolName + PoolGUID`, and sorts each leg's path into the slot
matching its `ThisGUID` in `LeafGUIDs`. The resulting ordered
slice is fed to `OpenFromDevices`, which verifies the count
matches the leaf count before building the pool.

## Decoder source

The XDR decoder is in
[`mock/pkg/go-filesystems/zfs/nvparse.go`](https://github.com/go-filesystems/zfs/blob/main/nvparse.go).

```go
// decodeNVList decodes a top-level XDR NVList. The 4-byte outer
// nvs_header (encoding byte + endian byte + 2 reserved) is consumed
// first; the inner header (version + nvflags = 8 bytes BE) is
// handled by decodeInnerNVList.
func decodeNVList(b []byte) (parsedNVList, error)

// decodeInnerNVList decodes the inner XDR sequence: [version,
// nvflags] + nvpairs + 8-byte terminator. Used both for the
// top-level inner list and for nested DATA_TYPE_NVLIST values.
func decodeInnerNVList(b []byte) (parsedNVList, error)
```

The exported types are `parsedNVPair` and `parsedNVList` with
helpers for value extraction:

```go
p.uint64Value()        // DATA_TYPE_UINT64
p.stringValue()        // DATA_TYPE_STRING
p.nvlistValue()        // DATA_TYPE_NVLIST (nested)
p.nvlistArrayValue()   // DATA_TYPE_NVLIST_ARRAY (vdev_tree.children)
```

## Data-type constants (the bug)

OpenZFS's canonical numbering (from `include/sys/nvpair.h`):

```c
DATA_TYPE_UINT64       =  8
DATA_TYPE_STRING       =  9
DATA_TYPE_NVLIST       = 19
DATA_TYPE_NVLIST_ARRAY = 20
```

The lib had `UINT64 = 11`, `STRING = 14`, `NVLIST_ARRAY = 25` —
all shifted by ~3 from the real values. This was fix #14 in the
on-disk format saga; see [On-disk format fixes](on-disk-formats.md#14-nvlist-data-type-constants--outer-header-size).

## Why this isn't generic

The decoder only supports the subset of nvpair types ZFS labels
actually use:

- `DATA_TYPE_UINT64` (8)
- `DATA_TYPE_STRING` (9)
- `DATA_TYPE_NVLIST` (19)
- `DATA_TYPE_NVLIST_ARRAY` (20)

Other types (arrays of uint64, booleans, hrtimes, etc.) are
silently passed through as raw `data` bytes. Adding support for a
new type is a switch case in the decoder helper.

The encoder side (`nvlist.go`) has the same subset because that's
what `cloud-boot-init` and the lib's own `Format()` need to emit.
