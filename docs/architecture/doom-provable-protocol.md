# DOOM bare-metal demo — provable test protocol (R-doom1g)

**Status:** v1 shipped 2026-06-14. Supersedes the empirical-only chi²
gate from R-doom1e.

## Motivation

The earlier R-doom1d / R-doom1e gates were empirical: they observed a
behavioural property (audio WAV non-zero, frame histogram divergent
after a keypress) and inferred a stack property (audio works, input
propagates). Empirical inference can be wrong — a non-zero WAV can be
ambient device noise, a histogram divergence can come from anything.

R-doom1g delivers a **provable** protocol: given deterministic inputs
and a versioned reference oracle, every checkpoint reduces to a
mechanical comparison with a single PASS/FAIL outcome. No human
judgement, no chi² threshold tuning, no "looks right".

## Operational criteria

The protocol asserts the following finite set of properties:

| ID  | Property                                                            | Gate |
|-----|---------------------------------------------------------------------|------|
| O-1 | At tic 1, gore.Run has produced frame 1 (engine startup completed)  | A    |
| O-2 | At tic 35, the framebuffer matches `oracle/frame_tic000035.ppm` (host) | A |
| O-3 | At tic 70/140, identical match against committed oracle             | A    |
| O-4 | At tic 350, prndindex/mrndindex match manifest (PRNG drift gate)    | A    |
| O-5 | At tic 1050, deeper-in-engine state still matches oracle            | A    |
| O-6 | Guest virtio-gpu scanout at tic T has histogram(canvas) within     | B    |
|     | chi² tolerance of `guest_oracle/frame_tic<T>.ppm`                   |      |
| O-7 | All re-runs of harvest-reference produce byte-identical oracle.    | A    |

## Determinism prerequisites

The gore engine has three sources of non-determinism in its default
configuration. The protocol pins each one.

| Source             | Default behaviour          | Pinned by                    |
|--------------------|----------------------------|------------------------------|
| PRNG state         | starts at index 0 (already deterministic, by Go zero-value) | `godoom.SeedRandom(seed)` (explicit, audited) |
| Tic clock          | wall-clock `time.Since(start_time)` | `godoom.SetDeterministicTics(true)` + `godoom.ResetClock()` |
| Input timing       | wall-clock `send-key` from QEMU | tic-tagged scripted events processed in `GetEvent` |
| Engine startup time| variable WAD load duration | engine prints "handing off to gore.Run" — runner anchors t=0 there |

Hooks live in `cloud-boot/godoom/seed.go` (NEW file, GPL-2.0 like the
engine — sits beside `doom.go` without modifying the transpiled source)
and are wired through the TamaGo backend via
`tamago.Frontend.SetSeed(uint64)` + `tamago.Frontend.ApplyDeterminism()`.

## Oracle artifact format

Two oracle directories, each committed in git:

### Host-side oracle: `cloud-boot/godoom/oracle/`

Produced by `cloud-boot/godoom/cmd/harvest-reference`. Format:

```
oracle/
├── manifest.json         # checkpoint list with SHA-256 + PRNG state
└── frame_tic<NNNNNN>.ppm # binary P6 PPM, exactly the engine's RGB buffer
```

`manifest.json` schema:

```json
{
  "seed": 0,
  "wad": "doom1.wad",
  "wad_bytes": 28795076,
  "wad_hash": "<sha256>",
  "script_hash": "<sha256>",
  "gore_version": "<git rev>",
  "checkpoints": [
    {
      "tic": 35,
      "frame_hash": "<sha256 of the RGB pixel buffer>",
      "frame_ppm": "frame_tic000035.ppm",
      "prndindex": 0,
      "mrndindex": 0,
      "frame_count": 68,
      "width": 320,
      "height": 200
    }
  ],
  "deterministic": true
}
```

### Guest-side oracle: `cloud-boot/tamago-uefi/internal/livedoomboot/guest_oracle/`

Produced by `bash provable_test.sh amd64` with
`DOOMBOOT_PROVABLE_HARVEST=1`. Format: identical to host oracle, plus a
`tolerance_chi2` field on `manifest.json` (default `50000.0`) which
sets the bounded-tolerance gate for guest-vs-guest comparison.

## Tolerance model

| Comparison                                | Tolerance       | Rationale                          |
|-------------------------------------------|-----------------|------------------------------------|
| Host re-harvest vs committed `oracle/`    | BYTE-EQUAL      | Same engine + WAD + seed must produce identical bytes — any drift is a regression. |
| Guest capture vs committed `guest_oracle/`| chi² ≤ 50000    | TamaGo GC stutter shifts tic boundaries by a few frames; histogram on a 64k-pixel canvas is GC-jitter-insensitive. Threshold calibrated at harvest time, recorded in manifest. |
| PRNG state at checkpoint                  | exact int match | PRNG is a pure table lookup — any mismatch means a missed engine path. |

## Failure-mode taxonomy

| Symptom                                    | Diagnosis                       | Assertion                                |
|--------------------------------------------|---------------------------------|------------------------------------------|
| GATE A FAIL on `frame_tic*.ppm` SHA mismatch | PRNG drift OR engine change OR WAD change | `wad_hash` in manifest, gore rev recorded |
| GATE A FAIL on `prndindex` mismatch        | input timing drift (event consumed at wrong tic) | per-checkpoint `prndindex` recorded |
| GATE B FAIL with chi² just over threshold  | TamaGo GC stutter exceeded budget | re-harvest to recalibrate threshold |
| GATE B FAIL with chi² massively over       | virtio-gpu scanout broken (R-doom1c-style failure) | per-checkpoint chi² printed |
| Engine main loop never starts              | UEFI / virtio-input init failure | runner times out at 30s, dumps serial log |
| Audio underrun                             | (not in v1 — empirical only)    | follow-up R-doom1h                       |

## How to run

### Generate / regenerate the host oracle

```bash
cd cloud-boot/godoom
GOWORK=off go run ./cmd/harvest-reference \
    -wad embedwad/doom1.wad \
    -seed 0 \
    -checkpoints 1,35,70,140,350,1050 \
    -gore-version "$(git rev-parse HEAD)" \
    -out oracle/
git add oracle/ && git commit -m "godoom: regenerate provable-protocol oracle"
```

### Run the provable test

```bash
cd cloud-boot/tamago-uefi
task doomboot:efi:amd64    # build the probe
bash internal/livedoomboot/provable_test.sh amd64
```

Exit code 0 = all gates PASS; non-zero = at least one gate FAIL with a
per-checkpoint diff report on stderr.

### Generate / regenerate the guest oracle

```bash
cd cloud-boot/tamago-uefi
DOOMBOOT_PROVABLE_HARVEST=1 bash internal/livedoomboot/provable_test.sh amd64
git add internal/livedoomboot/guest_oracle/ && git commit -m \
    "livedoomboot: regenerate guest oracle for provable-protocol gate B"
```

## Provability taxonomy (honest)

| Gate                       | Provability class    | Notes                                |
|----------------------------|----------------------|--------------------------------------|
| Engine PRNG seed → output  | BYTE-EQUAL provable  | Proved by `TestSeedRandom_DeterministicSequence` in `godoom/seed_test.go` + GATE A re-harvest. |
| Engine tic clock → output  | BYTE-EQUAL provable  | Proved by GATE A re-harvest (same checkpoints, same bytes, every run). |
| Engine output → guest framebuffer | BOUNDED-TOLERANCE provable | GATE B chi² gate. Bounded because TamaGo GC + wall-clock-driven probe means tic boundaries jitter. |
| Guest framebuffer → audio output | EMPIRICAL (v1) | No audio oracle in v1 — uses the existing R-doom1d WAV non-zero check. R-doom1h is the follow-up to lift this to bounded-tolerance. |

## Adding a new checkpoint

1. Decide the engine tic at which the new property should hold (e.g.,
   "after Enter at tic 200, the skill-select screen is visible at
   tic 230").
2. Add a `<tic>:keydown:KEY` / `<tic>:keyup:KEY` pair to the input
   script (if any) under `cloud-boot/godoom/docs/oracle-script.txt`.
3. Re-run `harvest-reference` with the new checkpoint added to
   `-checkpoints`, commit the updated `oracle/`.
4. Re-run `provable_test.sh amd64` with `DOOMBOOT_PROVABLE_HARVEST=1`
   to refresh `guest_oracle/` and commit it.
5. The CI workflow (`.github/workflows/doom-provable.yml`) picks up
   the new checkpoint automatically — no workflow change needed.

## What this protocol does NOT prove

- **Audio fidelity**: v1 still relies on the empirical "WAV non-zero
  RMS" check inherited from R-doom1d. Lifting this is R-doom1h.
- **Real player input latency**: provable_test.sh measures *correctness*
  of the input pipeline (the right frames appear after the right
  inputs), not *responsiveness* (how many ms between QMP send-key
  and frame change). The latter is an empirical micro-benchmark
  (R-doom1f territory).
- **Long-term stability**: checkpoints stop at tic 1050 (~30 s of
  engine time). R-doom1c's gpu-virtqueue back-pressure manifests
  later; that is a separate gate.
