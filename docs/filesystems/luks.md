---
title: LUKS overlay
---

# LUKS overlay (full-disk encryption)

`github.com/go-fde/luks`

Pure-Go LUKS1/LUKS2 unlock. cloud-boot detects the LUKS magic at
offset 0 of the configured `cloudboot.disk=` device, unlocks the
volume with the passphrase, and opens any of the four supported
filesystems on top of the plaintext.

## Why it lives in cloud-boot-init

LUKS is layered **below** the filesystem. The init flow:

1. Open the configured device.
2. `detectLUKS(devPath)` sniffs the first 8 bytes for LUKS1
   (`LUKS\xba\xbe\x00\x01`) or LUKS2 (`LUKS\xba\xbe\x00\x02`)
   magic.
3. If LUKS, call `openFSWithLUKS(p, devPath, passphrase)` which:
    - Unlocks via `luks.Open(devPath, []byte(pass))` → `*luks.Device`.
    - Wraps the `*luks.Device` in a per-FS `BlockBackend` adapter
      (`luksAsBlock` for ext4, `luksAsXFSBackend` for XFS, …).
    - Calls the FS opener (`fsext4.OpenFromDevice(adapter, -1)`,
      etc.) on the plaintext.

The adapter pattern is necessary because each `go-filesystems/*`
lib defines its **own** `BlockBackend` type. The method set is
identical across the four (ReadAt + WriteAt + Sync + Size +
Truncate + Close), so the adapter is a 30-line shim.

## Source

```go
// detectLUKS returns true when devPath's first 8 bytes match a LUKS
// header magic. Non-fatal: I/O errors return false (the FS opener
// will surface a clearer error than a half-baked LUKS check).
func detectLUKS(devPath string) bool {
    f, err := os.Open(devPath)
    if err != nil { return false }
    defer f.Close()
    var hdr [8]byte
    if _, err := f.ReadAt(hdr[:], 0); err != nil { return false }
    return bytes.Equal(hdr[:], luks1Magic) || bytes.Equal(hdr[:], luks2Magic)
}
```

## Configuration

| Cmdline knob | Meaning |
| --- | --- |
| `cloudboot.disk=` | The encrypted block device. |
| `cloudboot.disk.fs=` | Filesystem on top of the plaintext: `ext4`, `xfs`, `btrfs`, `zfs`. |
| `cloudboot.disk.luks-passphrase=` | The passphrase. **Do not pass this via the kernel cmdline directly — set it through `cloudboot.metadata.url=` so it never reaches `/proc/cmdline` after boot.** |

## Supported overlays

| Inner FS | Adapter | Status |
| --- | --- | --- |
| ext4 | `luksAsBlock` | ✓ Production-ready. |
| XFS | `luksAsXFSBackend` | ✓ Production-ready. |
| btrfs | `luksAsBTRFSBackend` | ✓ Single-leg only (multi-device LUKS-on-RAID-btrfs would need per-leg unlock). |
| ZFS | `luksAsZFSBackend` | ✓ Single-vdev only (LUKS-on-multi-vdev-ZFS would need per-leg unlock + `OpenFromDevices`). |

## Example: ZFS-on-LUKS Proxmox VE

The canonical Proxmox VE install with FDE — root on ZFS, ZFS on
LUKS. The dataset path `rpool/ROOT/pve-1` includes the pool name;
cloud-boot splits on the first `/` and feeds the remainder to
`fszfs.OpenFromDeviceDataset`.

```hcl
target "proxmox-fde" {
  disk = {
    device           = "/dev/vda3"
    fs               = "zfs"
    dataset          = "rpool/ROOT/pve-1"
    luks-passphrase  = "$(meta.cloudboot.luks_passphrase)"
  }
}
```

```json
{
  "cloudboot": {
    "luks_passphrase": "correct horse battery staple"
  }
}
```

## Limitations

- **Read-only LUKS in cloud-boot-init.** The adapters' `Truncate`
  method returns an error; writes go through but most production
  cloud-boot scenarios are read-only.
- **No TPM-sealed passphrases** yet. The passphrase must arrive
  via cmdline or metadata-URL. TPM unsealing would mean wiring
  systemd-cryptenroll-style policy verification into PID 1 init,
  which isn't a current priority.
- **No multi-keyslot policy.** The lib tries the supplied
  passphrase against every keyslot in order; the first match
  wins.
- **No multi-device LUKS-on-RAID** yet. The single-leg adapters
  are wired; per-leg unlock followed by `OpenFromDevices` is the
  shape of the follow-up.
