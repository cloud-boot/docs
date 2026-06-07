# BSD targets — FreeBSD / NetBSD / OpenBSD

cloud-boot can hand off to a BSD's own `loader.efi` instead of a
Linux kernel. The mechanism lives entirely in the
[loader](https://github.com/cloud-boot/loader) (Path B) — Path A
(kexec) and Path C (UKI menu-then-reboot) are Linux-only since
they assume a Linux EFI stub or a Linux PID 1.

## Selecting a BSD target

Set the EFI variable `CloudBootTarget` to one of:

| Value | OS | ESP path tried | Status |
| --- | --- | --- | --- |
| `freebsd` | FreeBSD | `\EFI\freebsd\loader.efi` → `\EFI\BOOT\bootaa64.efi` | ✓ verified to GENERIC login on QEMU |
| `netbsd` | NetBSD | `\EFI\BOOT\bootaa64.efi` | ✓ verified to `login:` on QEMU |
| `openbsd` | OpenBSD | `\EFI\BOOT\bootaa64.efi` | code in tree; no arm64 cloud image to E2E test |

Tested on FreeBSD 14.3-RELEASE arm64, NetBSD 10.0 (GENERIC64) arm64,
QEMU + OVMF.

## How it differs from the Linux branch

The Linux branch (`CloudBootTarget=ext4` / `xfs` / `btrfs` /
`zfs` / `ext4-direct` / …) reads the distro kernel into a pool
buffer with our pure-Go FS driver and calls:

```
LoadImage(SourceBuffer=<bytes>, SourceSize=<n>, DevicePath=NULL)
```

…then `StartImage`. The Linux EFI stub doesn't care where it
came from.

The BSD branch instead builds a composite `EFI_DEVICE_PATH` of the
form `<volume DP nodes> ‖ FILEPATH(loaderPath) ‖ END` and calls:

```
LoadImage(SourceBuffer=NULL, DevicePath=<composite>)
```

The firmware reopens the file via `SimpleFileSystem` on the named
volume and — critically — populates the chained image's
`LoadedImage.FilePath` with that DevicePath. **FreeBSD's
`loader.efi` introspects its own `FilePath` to deduce `currdev`**
(the device its boot modules live on). Without a populated
`FilePath`, the BSD loader walks `disk0:` then `net0:` blindly
and can stall.

That branch is gated by `isBSDTarget()` =
`freebsd || openbsd || netbsd`, all three reach the same
DevicePath-based `LoadImage` path.

## Why we don't need a UFS2 / FFS reader

The BSD `loader.efi` is itself capable of reading UFS2 (FreeBSD)
or FFS (NetBSD / OpenBSD) — that's how it pulls `/boot/loader`
(stage 3) and the BSD kernel afterwards. cloud-boot only has to
get **the BSD's own loader binary** out of the FAT-formatted
EFI System Partition, which is the same FAT we already read for
Linux UKIs. So no new on-disk format support is required in our
binary.

## The two-path cascade for FreeBSD

FreeBSD installs the EFI loader in **two different places**
depending on deployment shape:

- `bsdinstall` (metal installs, "FreeBSD-as-the-installed-OS")
  writes the loader to `\EFI\freebsd\loader.efi` and adds an EFI
  boot entry pointing there.
- The official **cloud images** put it at the EFI
  removable-media fallback path `\EFI\BOOT\bootaa64.efi`.

`CloudBootTarget=freebsd` tries the vendor path first, then the
fallback path. Same `LoadImage(DevicePath)` code for both — only
the path component changes.

NetBSD and OpenBSD only use the fallback path, so they're
single-step.

## The self-handle skip

FreeBSD's cloud images install their bootloader at the EFI
removable-media fallback path `\EFI\BOOT\bootaa64.efi` — the
*same* path that holds **our own** `BOOTAA64.EFI` on the
cloud-boot ESP. Without a self-handle skip, the cascade would
re-`LoadImage` ourselves in an infinite loop.

The loader captures its own `DeviceHandle` via
`LoadedImageProtocol` on the parent `imageHandle` at the top of
`_start`, and `tryAllHandles` drops that handle from the
iteration before testing any file.

## Verified handoffs

### FreeBSD 14.3 (QEMU + OVMF)

```text
  our DeviceHandle captured
  trying UKI \EFI\freebsd\loader.efi
  skipping self device handle
  freebsd: vendor path missed, trying fallback \EFI\BOOT\bootaa64.efi
  trying UKI \EFI\BOOT\bootaa64.efi
  skipping self device handle
  found UKI, size = 0x00000000000D071C
  LoadImage OK
StartImage...
    Reading loader env vars from /efi/freebsd/loader.env
FreeBSD/arm64 EFI loader, Revision 3.0
```

### NetBSD 10.0 (QEMU + OVMF)

```text
  target from EFI var CloudBootTarget: netbsd
  trying UKI \EFI\BOOT\bootaa64.efi
  skipping self device handle
  LoadImage(DevicePath) OK, child handle = 0x00000000BF02C398
StartImage...
   \\        __,---`  NetBSD/evbarm efiboot (arm64)
booting netbsd - starting in 5 seconds. 4 … 3 … 2 … 1 … 0
[   1.0000000] NetBSD 10.0 (GENERIC64) #0: Thu Mar 28 08:33:33 UTC 2024
[   1.0000040] dk1 at ld5: "netbsd-root", 2891776 blocks at 196608, type: ffs
NetBSD/evbarm (arm64) (constty)
login:
```

## What's not done yet

- **Apple VZ E2E.** The BSD branch ships in the same loader
  binary as the Linux branch. The Linux six-distro cascade works
  under Apple VZ; the BSD branch hasn't been E2E-tested there
  because we lack a vfkit BSD test recipe. The code path is
  identical though, and VZ-OVMF exposes the same protocols
  (`BlockIO` + `SimpleFileSystem`), so nothing structural blocks
  it.
- **OpenBSD verification.** Routing code is in tree and
  unit-tested. OpenBSD doesn't publish pre-installed arm64 cloud
  images, but the project's
  [`install76.img`](https://cdn.openbsd.org/pub/OpenBSD/7.6/arm64/install76.img)
  is bootable under QEMU. A scripted auto-installer produces
  `openbsd76-arm64.qcow2` reproducibly — see
  [`loader/scripts/install-openbsd-arm64.sh`](https://github.com/cloud-boot/loader/blob/main/scripts/install-openbsd-arm64.sh).
  The script:
    1. Downloads `install76.img` + `bsd.rd` from cdn.openbsd.org.
    2. Crafts a tiny `site76.tgz` carrying `auto_install.conf`
       (hostname, timezone, no users, console=com0).
    3. Boots QEMU with the installer image plus a blank 8 GiB
       qcow2 and a site CD-ROM; OpenBSD's installer notices the
       site set and runs unattended.
    4. After `halt -p`, the qcow2 is ready for the test run
       (`CloudBootTarget=openbsd` + ESP with our loader).
  The whole thing is ~10 min on a moderns host; the resulting
  qcow2 is committed under `loader/testdata/` as a zstd-
  compressed tarball.
- **BSD-specific cmdline patching.** `patchChildCmdline` is
  still called for BSD targets — it's safe (the BSD loaders
  ignore `LoadOptions` they don't understand) but a future
  refinement could skip it.

## See also

- [Three boot paths](../architecture/three-paths.md) for where
  the BSD branch sits in the overall architecture.
- [Kernel cmdline (`cloudboot.*`)](cmdline.md) for the broader
  set of `cloudboot.target` / `CloudBootTarget` values.
- [`loader/README.md`](https://github.com/cloud-boot/loader)
  for the implementation-level commentary.
