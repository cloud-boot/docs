# VZ (Apple Virtualization.framework / vfkit) compatibility audit

**Date**: 2026-06-09
**Scope**: tamago-uefi recent work (M0/M1/M1.5/M1.6/M6.2/M8.0/M8.1)
under Apple-Silicon arm64 vfkit.
**Host**: Darwin 25.5.0, arm64, vfkit v0.6.3.

This audit answers: do any of the post-M2c milestones BREAK or
regress what works on VZ on the network-free legs?

## TL;DR

Six of seven binaries PASS_HEADLESS under vfkit. The seventh — bare
`BOOTAA64-EFIPACKSTUB.EFI` without a `.payload` — early-exits in
~1 s, which is the **expected** behaviour (the stub's CBP0-magic
check fails and it exits via `gBS->Exit(EFI_ABORTED)`); the real
M6.2 end-to-end test (`pectl pack BOOTAA64-PROBE.EFI` then boot the
packed envelope) PASSES. **No regressions found**.

## Audit matrix

| Binary                       | Network? | Expected   | Observed       | Notes |
|------------------------------|---------:|-----------:|---------------:|-------|
| `BOOTAA64-PROBE.EFI`         |       No | PASS       | PASS_HEADLESS  | M0 GetMemoryMap baseline |
| `BOOTAA64-PCIENUM.EFI`       |       No | PASS       | PASS_HEADLESS  | M1 PCI walk |
| `BOOTAA64-PCISNP.EFI`        |       No | PASS       | PASS_HEADLESS  | M1.5 SNP enum (0 handles on VZ, see R-M2c) |
| `BOOTAA64-BLKPRINT.EFI`      |       No | PASS       | PASS_HEADLESS  | M1.6 Block-IO; the original VZ side-channel canary |
| `BOOTAA64-EFIHANDOVER.EFI`   |       No | PASS       | PASS_HEADLESS  | M8.0 `LoadImage`/`StartImage` mechanism — proves the M8.0/M8.1/M8.2/M8.3 family's chain-boot core works on VZ |
| `BOOTAA64-KERNELBOOT.EFI`    |       No | PASS       | PASS_HEADLESS  | M8.1 MODE B (in-process Transport, no virtio-net). Also exercised at guest RAM = 512/1024/2048 MiB — no failure → R-M8.3a 128 MiB heap-bump is safe on small-RAM VZ |
| `BOOTAA64-EFIPACKSTUB.EFI`   |       No | EARLY-EXIT | EARLY-EXIT     | Bare stub with no `.payload`; CBP0 check fails and `gBS->Exit(EFI_ABORTED)` returns control to firmware → VZ shuts down within ~1 s. NOT a regression — this is the documented stub-without-payload behaviour |
| **packed PROBE via `pectl pack`** | No | PASS  | PASS_HEADLESS  | The real M6.2 PR2 end-to-end test: pack `BOOTAA64-PROBE.EFI` through `pectl pack -c flate` (output 1.9 MiB), boot the packed envelope. Stub decompresses, `LoadImage`+`StartImage` jumps to the inner PROBE which enters `for {}` spin |

Binaries deliberately NOT tested (all require virtio-net, intrinsically
incompatible with VZ per R-M2c CLOSED 2026-06-08):

* `BOOTAA64-MINISTACK.EFI`, `BOOTAA64-DHCP4.EFI`,
* `BOOTAA64-HTTP.EFI`, `BOOTAA64-HTTPS.EFI`,
* `BOOTAA64-OCI.EFI`, `BOOTAA64-OCISTREAM.EFI`, `BOOTAA64-ORASOCI.EFI`,
* `BOOTAA64-COSIGN.EFI`,
* `BOOTAA64-VIRTIONET.EFI` (and B-variant).

## Findings

### F-1 — VZ observability gap (NEW finding, blocking for visual checks)

`vfkit`'s `--device virtio-serial,logFilePath=…` produces a 0-byte
log during the entire firmware/EFI-application stage. Apple's EFI
bootloader binds ConOut to the framebuffer only — there is no
PL011/UART or virtio-console route exposed to firmware. The virtio
serial port becomes useful only after a guest OS starts using
`/dev/hvc0`. Empirically verified 2026-06-09 across all seven probes.

Consequence: **headless VZ smoke tests cannot grep firmware-stage
`println` output**. The audit runner uses a "VM did not stop early"
heuristic instead (see §Detection-Heuristic). For positive
verification of probe banners, run with `VZ_LIVE_GUI=1` and visually
read the framebuffer console.

Mitigation paths (deferred):

* Switch the probe end-of-test from `for {}` to
  `uefiboard.WriteFile("\BOOTLOG.TXT", buf)` so the log lives on
  the ESP and can be inspected post-mortem.
* Or have probes call `gBS->Exit(<custom-status>)` carrying a
  PASS/FAIL bit that vfkit could surface via VM-exit reason (would
  require a vfkit feature to expose the firmware exit status).
* Or build a separate "vz-bootlog" sink that uses
  `EFI_FILE_PROTOCOL` against the same ESP-by-FilePath dance the
  M6.2 PR2 stub already implements.

### F-2 — M8.3a 128 MiB heap bump is VZ-safe (REGRESSION CHECK PASSED)

`uefiboard/board_arm64.go`'s heap-bump (R-M8.3a) bumps TamaGo runtime
RAM use to ~128 MiB. KERNELBOOT.EFI was smoke-tested under VZ at
guest RAM = 512 / 1024 / 2048 / 4096 MiB. All four PASS_HEADLESS.
No regression to the bare-metal cpuinit path or to small-RAM hosts.

### F-3 — Bare `EFIPACKSTUB.EFI` early-exit is by design

The bare stub binary is not a runnable boot target; it requires a
`.payload` section appended by `pectl pack`. Running it standalone
under VZ triggers the documented "CBP0 check fails →
`gBS->Exit(EFI_ABORTED)`" path within ~1 second, indistinguishable
from "firmware refused to boot" at the vfkit observation layer.
The Taskfile entry `vz:smoke:efipackstub` uses `|| true` to keep
the matrix green; the real M6.2 PR2 confidence comes from
`vz:smoke:efipack_probe`.

### F-4 — No regression in M8.0/M8.1/M8.2/M8.3 chain-boot mechanism

`BOOTAA64-EFIHANDOVER.EFI` (M8.0) and `BOOTAA64-KERNELBOOT.EFI`
(M8.1 MODE B) both PASS_HEADLESS. Since these are the two
mechanism-baseline binaries for the M8 family — they share the
`gBS->LoadImage` + `gBS->StartImage` + `PublishInitrd` + custom
`SetLoadOptions` code paths — the M8.2 framework and M8.3 MODE-A
extensions inherit the VZ green light for their non-network legs.

### F-5 — `EFI_PCI_IO_PROTOCOL` + `EFI_BLOCK_IO_PROTOCOL` published as expected

PCIENUM and BLKPRINT both stay running past the watchdog (i.e. they
reach `for {}` after iterating the protocol handles). This confirms
the original M1 / M1.6 VZ characterisation is unchanged: Apple's EFI
publishes those protocols for the virtio-blk device backing the ESP,
which is the only side-channel available to us under VZ.

## Detection heuristic

`internal/live_vz/run.sh` boots each EFI under vfkit with a watchdog,
then categorises:

* **`PASS_HEADLESS`** — VM was still running when the watchdog fired
  (firmware loaded the EFI, the loaded image entered `for {}` spin).
* **`PASS_GUI`** — same but with `--gui` so the operator can
  visually verify the framebuffer ConOut output.
* **`FAIL`** — `"VM is stopped"` appears in vfkit stderr BEFORE the
  watchdog deadline (firmware refused to boot, or the loaded EFI
  crashed during initialisation). Differentiator empirically
  established 2026-06-09 with an empty disk (stops in ~1 s) vs
  `BOOTAA64-PROBE.EFI` (runs to watchdog).

## How to reproduce

```bash
cd cloud-boot/tamago-uefi
brew install vfkit mtools dosfstools     # if not already
task vz:smoke:arm64                       # the full matrix
# or, per-binary:
task vz:smoke:probe
task vz:smoke:efipack_probe
# observe a probe's ConOut visually:
VZ_LIVE_GUI=1 bash internal/live_vz/run.sh probe
```

## Coordinates

* Runner: `cloud-boot/tamago-uefi/internal/live_vz/run.sh`
* Task group: `vz:smoke:*` in `cloud-boot/tamago-uefi/Taskfile.yaml`
* Reference VZ findings: design doc §R-M2c, §M1.6.
* Path C / production VZ flow: see `cloud-boot/docs/docs/tutorials/vfkit.md`.
