---
title: ZFS
---

# ZFS

`github.com/go-filesystems/zfs`

Read/write ZFS filesystem driver in pure Go. Reads real OpenZFS
2.1.x pools — single-vdev, mirror, and **all three raidz levels**
(raidz1, raidz2, raidz3). With the sibling
[`github.com/go-crypto/zfscrypt`](https://github.com/go-crypto/zfscrypt)
it also reads ZFS **native encryption** (AES-CCM/GCM).

This is the most involved driver in `go-filesystems/*` and the one
where the most pre-existing on-disk format bugs were uncovered.

## Single-vdev / single-leg-mirror API

```go
import fszfs "github.com/go-filesystems/zfs"

// Open the pool's root dataset.
fs, err := fszfs.Open("/dev/vda3", -1)
defer fs.Close()

// Open a specific dataset under the pool.
// IMPORTANT: pool name is implicit (the path is relative to the pool root).
fs, err := fszfs.OpenDataset("/dev/vda3", -1, "ROOT/pve-1")
defer fs.Close()

// Layered (LUKS / qcow2 / in-memory) variants.
fs, err := fszfs.OpenFromDevice(dev, -1)
fs, err := fszfs.OpenFromDeviceDataset(dev, -1, "ROOT/pve-1")
```

A mirror pool can be opened from **any one** of its legs via this
path — both legs hold identical bytes at every DVA offset, so the
existing single-device read code path is correct on a mirror.

## Multi-vdev API (raidz)

```go
import fszfs "github.com/go-filesystems/zfs"

// Open all legs of a multi-vdev pool. devs must be in vdev-id order
// (i.e. zpool-create argument order). Mismatches are detected.
devs := []fszfs.BlockBackend{
    &osFileBackend{f: file0},
    &osFileBackend{f: file1},
    &osFileBackend{f: file2}, // raidz1: 3 legs
}
fs, err := fszfs.OpenFromDevices(devs, -1, "data") // dataset = "data"
defer fs.Close()
```

`OpenFromDevices` reads label 0 from `devs[0]`, parses the
`vdev_tree` NVList, verifies device count matches leaf count, and
builds a `multiVdevPool` that routes reads per topology:

- **mirror**: serve from leg 0 with leg fallback on error.
- **raidz**: per-IO `raidzMapAlloc` (ported from
  `module/zfs/vdev_raidz.c:vdev_raidz_map_alloc`) computes
  `(child, child_offset, child_size)` for every data column;
  parity columns skipped on healthy-path; output buffer is the
  concatenation of data column reads in column order.

### raidz_map geometry (healthy path)

Given a logical IO of size `S` sectors at offset `O` sectors on a
RAID-Z vdev with `dcols` children and `nparity` parity columns,
sector size `1 << ashift`:

```text
data_cols = dcols - nparity
q  = S / data_cols      # full rows
r  = S % data_cols      # remainder
bc = r == 0 ? 0 : r + nparity   # "big" columns in partial row
acols = q == 0 ? bc : dcols     # accessed columns

for c in [0, acols):
    col   = (O + c) mod dcols
    coff  = (O / dcols) << ashift
    if col < (O mod dcols):  coff += sector_size
    rc_size = (c < bc ? q + 1 : q) << ashift
```

Columns `c=0..nparity-1` are PARITY; columns `c=nparity..acols-1`
hold DATA in column order. For a healthy read we read only the
data columns and concatenate.

## Label discovery — `ProbeLabel`

cloud-boot-init's `findZFSVdevs` uses the exported `fszfs.ProbeLabel`
to decode each candidate device's label NVList without opening the
FS:

```go
type LabelInfo struct {
    PoolName     string
    PoolGUID     uint64
    TopGUID      uint64 // GUID of the top-level vdev (mirror/raidz parent)
    ThisGUID     uint64 // GUID of THIS device (this leaf)
    VdevChildren uint64 // number of top-level vdevs in the pool
    Type         string // "file"/"disk" for single-vdev, "mirror"/"raidz" for multi-vdev
    NParity      uint64 // raidz nparity (0 for non-raidz)
    Ashift       uint64
    LeafGUIDs    []uint64 // ordered list of guids from vdev_tree.children
}

info, err := fszfs.ProbeLabel(r, partOff)
```

cloud-boot-init walks `/sys/block`, probes each device, filters by
`PoolName + PoolGUID`, sorts each leg's path into the slot
matching its `ThisGUID` in `LeafGUIDs`, verifies all slots are
filled, then hands the ordered slice to `OpenFromDevices`. See
[`findZFSVdevs` in disk_zfs_linux.go](https://github.com/cloud-boot/init/blob/main/cmd/cloud-boot-init/disk_zfs_linux.go).

## cloud-boot integration

```hcl
target "proxmox" {
  disk = {
    device = "rpool/ROOT/pve-1"  # pool/dataset path
    fs     = "zfs"
    kernel = "/boot/vmlinuz-6.5.13-1-pve"
    initrd = "/boot/initrd.img-6.5.13-1-pve"
  }
}
```

cloud-boot-init splits the device path on the first `/`, treats
the first segment as the pool name (used for `findZFSVdev(s)`),
and passes the remainder as the dataset path to `OpenDataset` /
`OpenFromDevices`.

## LUKS overlay

`luksAsZFSBackend` in cloud-boot-init wraps an unlocked
`*luks.Device`; `fszfs.OpenFromDeviceDataset(adapter, -1, "<dataset>")`
reads ZFS on top of the plaintext. The classic Proxmox VE
LUKS-on-ZFS layout (`rpool/ROOT/pve-1`) is supported end-to-end.

## ZFS native encryption

Pure-Go AES-CCM (RFC 3610 / NIST SP 800-38C — stdlib only ships
GCM) lives in `github.com/go-crypto/ccm`. The ZFS-specific glue
(PBKDF2-HMAC-SHA1 wrap, HKDF-SHA512 per-block, AEAD per-block
decryption) lives in `github.com/go-crypto/zfscrypt`. Both are
consumed by `go-filesystems/zfs`'s `OpenFromDeviceDatasetWithKey`
entry point.

## On-disk format bugs the lib had to fix

Eleven format bugs surfaced when the lib was first pointed at real
`zpool create` output (zfsutils-linux 2.1.11 on Debian 12). All
fixed in 2026-05-22:

1. **`VDEV_LABEL_START_SIZE`** — DVA offsets are relative to the
   data area at 4 MiB (2 × 256 KiB labels + 3.5 MiB boot pad), not
   the partition start.
2. **`ZAP_MAGIC`** — was `0x2F5AB2AB`; real value is
   `0x2F52AB2AB` (one extra digit missing).
3. **`zap_table_phys` layout** — `zt_shift` is at offset `0x20`
   inside the table (uint64), not `0x0C` inside `zt_numblks`.
4. **Embedded pointer table position** — at `blockSize/2`, not at
   fixed offset 128.
5. **Hash table size** — `blockSize/16` (per
   `ZAP_LEAF_HASH_NUMENTRIES`), not `2 × (1 << lh_prefix_len)`.
6. **ZAP integer values are big-endian on disk** —
   `zap_leaf_array_read` writes them via byte-shift unrolling
   regardless of machine endianness; the lib was reading LE.
7. **Embedded BP `pad` bytes (56..71) are payload** — the lib was
   discarding 16 bytes per embedded BP.
8. **Embedded BP `prop` encoding** — LSIZE at bits 0..24, PSIZE at
   bits 25..31 (the lib had them swapped/wrong).
9. **`BPE_NUM_WORDS = 14`** — embedded BP payload is **112 bytes**
   (14 words), not 96 (12 words). `phys_birth` and `fill` ARE
   payload; `BPE_IS_PAYLOADWORD` only excludes `prop` and `birth`.
10. **`SA_MAGIC = 0x002F505A`** — was `0x02F505A5` (one extra
    trailing digit). Made every SA bonus header rejected; without
    this, `ReadFile` returns the full data block (e.g. 512 bytes)
    instead of truncating to the inode-recorded file size.
11. **NVList data-type constants** — UINT64 is 8 (not 11), STRING
    is 9 (not 14), NVLIST_ARRAY is 20 (not 25). All shifted by ~3
    in the lib. Also: the outer XDR header is 4 bytes (encoding
    byte + endian byte + 2 reserved), not 8.

Fix #9 was the critical breakthrough — it made LZ4 succeed on
MOS-internal embedded ZAPs. Fix #10 unblocked `ReadFile` size
truncation. Fix #11 unblocked the vdev_tree NVList walk that
made raidz support possible.

See [Internals / on-disk format fixes](../internals/on-disk-formats.md).
