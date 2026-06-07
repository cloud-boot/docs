# Windows target — `CloudBootTarget=windows`

cloud-boot's loader can chain-load the **Windows Boot Manager**
instead of a Linux kernel or a BSD loader.efi. The mechanism is the
same as the [BSD targets](bsd-targets.md) — `LoadImage(DevicePath,
SourceBuffer=NULL)` with a composite EFI_DEVICE_PATH so the chained
binary's `LoadedImage.FilePath` is populated.

## Selecting the Windows target

Set the EFI variable `CloudBootTarget` to `windows`. The loader
routes the FAT-ESP lookup to:

1. **Vendor path**: `\EFI\Microsoft\Boot\bootmgfw.efi` —
   where `Setup.exe` writes the Boot Manager on every supported
   edition (Windows 10, 11, Server 2016+).
2. **Fallback path**: `\EFI\BOOT\BOOTX64.EFI` on amd64,
   `\EFI\BOOT\BOOTAA64.EFI` on arm64 — where Windows-To-Go sticks,
   recovery media, and PE-based installers put the binary.

The cascade is identical in shape to the [FreeBSD two-step](bsd-targets.md#the-two-path-cascade-for-freebsd),
and the self-handle skip in `tryAllHandles` prevents the obvious
infinite loop against our own `BOOTAA64.EFI` / `BOOTX64.EFI`.

## Why it shares the BSD code path

Windows Boot Manager **introspects its own `LoadedImage.FilePath`**
to find the NTFS partition that holds `\Windows\System32\winload.efi`
and the BCD store. Without a populated FilePath (which is what
`LoadImage(SourceBuffer=…)` gives you), the Boot Manager walks
through disks blindly looking for a BCD — exactly the failure mode
FreeBSD's loader hits.

So the gate in `tryLoadFromHandle` is the semantic
`wantsFilePathHandoff()` =
`isBSDTarget() || isWindowsTarget()`. Linux UKIs stay on the
SourceBuffer path (the EFI stub doesn't introspect FilePath); BSDs
and Windows go through the DevicePath branch.

## Status

| What | State |
| --- | --- |
| Loader routing | ✅ committed (`windowsTag`, `isWindowsTarget`, vendor + fallback cascade in `_start`) |
| Unit test of the gate | ☐ pending (would add to `loader/cmd/efi-loader/main_test.go` if there were one) |
| E2E QEMU boot to Windows desktop | ☐ pending — see *Test recipe* below |
| Apple VZ E2E | ☐ pending (same reasoning as BSD: Linux works on VZ, structurally nothing blocks Windows) |

## Test recipe (QEMU/OVMF, arm64)

There's no "official Windows cloud image" in qcow2/raw form, but
two free legit paths exist:

| Source | Format | Free? | Notes |
| --- | --- | --- | --- |
| Microsoft Edge dev VMs ([download](https://developer.microsoft.com/en-us/microsoft-edge/tools/vms/)) | VHDX (Hyper-V) | 90-day eval | **Easiest** — Win10/11 pre-installed |
| Windows Server eval ([Eval Center](https://www.microsoft.com/en-us/evalcenter/)) | ISO | 180-day eval | Need to install once, then commit the qcow2 |
| Hyper-V Server 2019 | ISO | gratis perpetual | Lite — headless, smallest image |
| Windows-To-Go via `WinToUSB` | raw partition | personal use OK | Reproduces the `\EFI\BOOT\BOOT<arch>.EFI` fallback path |

For the loader's `CloudBootTarget=windows` smoke test, the dev VMs
are the path of least resistance — they boot directly without a
manual install.

### One-shot test script

`loader/scripts/test-windows.sh` automates the dev-VM smoke test:

```sh
# 1. Download the dev VM once (manual, after agreeing to the eval
#    terms at https://developer.microsoft.com/en-us/microsoft-edge/tools/vms/).
#    Place the VHDX at $HOME/Downloads/MSEdge-Win11.vhdx (or pass
#    --vhdx <path>).
loader/scripts/test-windows.sh --vhdx ~/Downloads/MSEdge-Win11.vhdx

# 2. The script:
#    - Converts the VHDX to raw via qemu-img convert.
#    - Builds the loader (task -d ../ link-amd64) into BOOTX64.EFI.
#    - Stages a tiny FAT ESP containing our BOOTX64.EFI + a NVRAM
#      seed that pre-sets CloudBootTarget=windows.
#    - Launches qemu-system-x86_64 with OVMF, the ESP, and the
#      Windows disk.
#    - Tails the serial console for the magic strings
#      "LoadImage(DevicePath) OK" (our loader handed off) and
#      "Loading kernel..." or "Windows" (Boot Manager started).
#    - Times out after 4 minutes if either marker doesn't appear.
```

### Expected serial output

```text
  our DeviceHandle captured
  target from EFI var CloudBootTarget: windows
  trying UKI \EFI\Microsoft\Boot\bootmgfw.efi
  skipping self device handle
  found UKI, size = 0x...
  LoadImage(DevicePath) OK, child handle = 0x...
StartImage...
  (Windows Boot Manager takes over — graphics console blanks the serial)
```

If the vendor path missed (e.g. the disk image is Windows-To-Go
style with the binary at `\EFI\BOOT\BOOTX64.EFI`):

```text
  windows: vendor path missed, trying fallback \EFI\BOOT\boot<arch>.efi
  trying UKI \EFI\BOOT\BOOTX64.EFI
  ...
```

## The `windows-stub` shortcut for routing tests

The cloud-boot org publishes a tiny **BSD-licensed PE32+ binary**
that mimics Windows Boot Manager's first ~10 ms of startup — just
enough to test the loader's `CloudBootTarget=windows` routing
end-to-end without any Microsoft binary on disk.

Source: [`cloud-boot/windows-image/stub/`](https://github.com/cloud-boot/windows-image/tree/main/stub).
Published OCI artifact:
[`ghcr.io/cloud-boot/windows-stub:latest`](https://github.com/orgs/cloud-boot/packages/container/windows-stub).

The stub:

1. Opens `LoadedImageProtocol` on its own ImageHandle.
2. **Asserts `LoadedImage.FilePath` is non-NULL** — proves the
   loader did `LoadImage(DevicePath, SourceBuffer=NULL)` rather
   than the SourceBuffer path. (A regression in
   `buildFilePath()` upstream would surface here as a hard fail
   instead of silently passing.)
3. Prints `Windows Boot Manager (cloud-boot stub)` to ConOut —
   matches `test-windows.sh`'s grep regex.
4. Calls `ResetSystem(EfiResetShutdown)` so QEMU exits cleanly.

Use it via `--stub` mode of the test script:

```sh
loader/scripts/test-windows.sh --stub
# Pulls ghcr.io/cloud-boot/windows-stub, stages a 64 MiB FAT disk
# with the stub at \EFI\Microsoft\Boot\bootmgfw.efi, runs the
# full QEMU + loader handoff sequence, greps the serial for both
# "LoadImage(DevicePath) OK" and "Windows Boot Manager".
```

For real-Windows runs (full Boot Manager startup, BCD parsing,
graphics console), use the same script with `--vhdx <path>` after
downloading a dev VM under your own EULA.

## What's NOT done yet

- **Live boot to Windows desktop.** The routing code paths
  Windows-style; the LoadImage handoff is the same as FreeBSD's
  (verified end-to-end). No structural reason Windows wouldn't
  reach the Boot Manager graphics under OVMF — but no run has been
  captured yet.
- **TPM / Secure Boot.** Modern Windows (Server 2022+, 11) refuses
  to install without a TPM and a Secure Boot key chain. Booting
  an *already-installed* image like a dev VM doesn't require this
  at runtime, so the test recipe above sidesteps the issue. For
  fresh Windows installs under QEMU the operator needs to add
  `-tpm-tis-device` + the Microsoft UEFI CA keys to OVMF_VARS.
- **Apple VZ.** Untested. The Linux path works on VZ; the
  protocol set VZ exposes (`BlockIO` + `SimpleFileSystem`) is
  exactly what the FilePath-handoff branch needs. No code
  difference, just no runner script yet.

## See also

- [BSD targets](bsd-targets.md) — the parent design pattern.
- [Three boot paths](../architecture/three-paths.md) — where
  the Windows branch sits in the overall architecture.
- [`loader/README.md`](https://github.com/cloud-boot/loader)
  for the implementation-level commentary.
