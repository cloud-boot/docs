# DOOM bare-metal demo — provable test protocol (R-doom1g / R-doom1h)

**Status:** v1 shipped 2026-06-14 (R-doom1g: GATE A + GATE B). v1.1
shipped 2026-06-14 (R-doom1h: GATE C-1 + GATE C-2 audio gates).
Supersedes the empirical-only chi² gate from R-doom1e and the
empirical "WAV non-zero" check inherited from R-doom1d.

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
| O-8 | The engine emits the SAME sequence of CacheSound + PlaySound calls  | C-1  |
|     | (name, channel, vol, sep, lump sha256) given a fixed WAD + seed     |      |
|     | + script; re-harvested `audio_log.json` is byte-equal to oracle.    |      |
| O-9 | The guest's WAV output envelope (per-second RMS) lies within        | C-2  |
|     | ±6 dB of `oracle/reference.wav` for ≥95% of one-second windows.     |      |

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
├── frame_tic<NNNNNN>.ppm # binary P6 PPM, exactly the engine's RGB buffer
├── audio_log.json        # R-doom1h: deterministic CacheSound/PlaySound
│                         # event stream (tic, op, name, channel, vol,
│                         # sep, lump sha256). Byte-equal across runs.
└── reference.wav         # R-doom1h: reference guest WAV (44.1 kHz
                          # stereo 16-bit PCM, captured once via run.sh
                          # with DOOMBOOT_LIVE_AUDIO_WAV). Used by
                          # GATE C-2 audio_verify.py.
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
| Host re-harvest vs committed `audio_log.json` | BYTE-EQUAL  | Same WAD + seed deterministically pin every CacheSound / PlaySound call the engine emits. Drift implies a p_random consumer changed or a sound module path was added/removed. |
| Guest WAV vs committed `reference.wav`    | per-second RMS within ±6 dB for ≥95% of 1 s windows | See "Audio gate metric (GATE C-2)" below — chosen for tolerance to TamaGo GC scheduling jitter while remaining sensitive to genuine "wrong sound played" regressions. |

## Failure-mode taxonomy

| Symptom                                    | Diagnosis                       | Assertion                                |
|--------------------------------------------|---------------------------------|------------------------------------------|
| GATE A FAIL on `frame_tic*.ppm` SHA mismatch | PRNG drift OR engine change OR WAD change | `wad_hash` in manifest, gore rev recorded |
| GATE A FAIL on `prndindex` mismatch        | input timing drift (event consumed at wrong tic) | per-checkpoint `prndindex` recorded |
| GATE B FAIL with chi² just over threshold  | TamaGo GC stutter exceeded budget | re-harvest to recalibrate threshold |
| GATE B FAIL with chi² massively over       | virtio-gpu scanout broken (R-doom1c-style failure) | per-checkpoint chi² printed |
| Engine main loop never starts              | UEFI / virtio-input init failure | runner times out at 30s, dumps serial log |
| GATE C-1 FAIL: audio_log.json byte mismatch | p_random consumer drift, sound module rewire, or WAD lump rehash | per-entry diff via `diff -u oracle/audio_log.json fresh/audio_log.json` |
| GATE C-2 FAIL: per-window RMS exceeds ±6 dB | Audio plumbing change in go-virtio/sound mixer, virtio-sound device config drift, or genuine "wrong SFX played" | `audio_verify.py` prints max_diff_db + mean_diff_db; window-by-window debug via `--window-seconds 0.25` |

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

This writes `manifest.json` + `frame_tic*.ppm` (for GATE A) AND
`audio_log.json` (for GATE C-1) in a single pass.

### Regenerate the GATE C-2 reference WAV

```bash
cd cloud-boot/tamago-uefi
task doomboot:efi:amd64
DOOMBOOT_LIVE_AUDIO_WAV=/tmp/doom-ref.wav \
    DOOMBOOT_LIVE_TIMEOUT=30 \
    bash internal/livedoomboot/run.sh amd64
cp /tmp/doom-ref.wav ../godoom/oracle/reference.wav
git -C ../godoom add oracle/reference.wav \
    && git -C ../godoom commit -m "godoom: refresh GATE C-2 reference.wav"
```

### Manually verify GATE C-1 (host audio-event byte-equal)

```bash
cd cloud-boot/godoom
GOWORK=off go run ./cmd/harvest-reference \
    -wad embedwad/doom1.wad -seed 0 -checkpoints 1,35,70,140,350,1050 \
    -out /tmp/fresh -verify-audio-log oracle/audio_log.json
# Exit 0 = byte-equal; exit 1 = diff (the harvester prints the
# oracle vs fresh sha256 prefixes).
```

### Manually verify GATE C-2 (guest WAV bounded tolerance)

```bash
cd cloud-boot/tamago-uefi
python3 internal/livedoomboot/audio_verify.py \
    --reference ../godoom/oracle/reference.wav \
    --capture /tmp/some-capture.wav \
    --tolerance-db 6 --min-ratio 0.95 --window-seconds 1.0
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
| Engine audio event stream | BYTE-EQUAL provable | GATE C-1 (R-doom1h). Proved by `harvest-reference -verify-audio-log` re-running the engine and diffing audio_log.json byte-for-byte. |
| Engine audio events → guest WAV | BOUNDED-TOLERANCE provable | GATE C-2 (R-doom1h). Per-second RMS within ±6 dB for ≥95% of 1-s windows vs `oracle/reference.wav`. Bounded for the same reason GATE B is. |

## Audio gate metric (GATE C-2)

GATE C-2 compares a WAV captured by QEMU's `-audiodev wav` backend
against `oracle/reference.wav` using a **per-second RMS envelope
tolerance**: each 1-second window's RMS (converted to dBFS with a
silence floor of 1.0) must lie within ±6 dB of the corresponding
window in the reference, for ≥95% of windows. The metric lives in
`cloud-boot/tamago-uefi/internal/livedoomboot/audio_verify.py`.

**Why this metric over the alternatives:**

DOOM Phase 1 audio is short SFX bursts (pistol shots, monster grunts,
door open/close) over silence — no continuous music, no sustained
spectral content. The guest's audio output is the deterministic
engine event stream (proved byte-equal by GATE C-1) processed through
`go-virtio/sound`'s PCM mixer and emitted via virtio-sound-pci. The
*content* of each SFX is identical between runs; the *timing* may
shift by up to ~100 ms due to TamaGo GC pauses, tamago scheduler
jitter, and virtqueue draining latency.

| Metric                     | Verdict | Reason                                                                 |
|----------------------------|---------|------------------------------------------------------------------------|
| Sample-level byte-equal    | rejected | Even 1 ms of jitter destroys it; uninformative.                       |
| Cross-correlation          | rejected | Hyper-sensitive to <100 ms phase shifts; would FAIL on GC stutter even when audio is otherwise identical. |
| Spectral fingerprint (FFT Pearson r) | rejected | Designed for sustained content. DOOM's "burst over silence" pattern means most FFT windows hit the noise floor; the few SFX-bearing windows dominate r in unpredictable ways depending on window alignment. |
| Per-second RMS envelope    | **chosen** | Each 1-s window absorbs <1 s of jitter transparently. Log-amplitude is a standard audio measure. "X dB tolerance for Y% of windows" is an unambiguous bounded-tolerance contract. |

**Calibration evidence:** two independent 30-second captures from the
same probe build produced `ratio=1.000`, `max_diff=0.4 dB`,
`mean_diff=0.0 dB`. A capture vs synthetic silence produced
`max_diff=83.2 dB`, comfortably FAIL. The ±6 dB / 95% threshold is
~15× the observed jitter, leaving headroom for cross-host variance
without going so loose that "wrong SFX played" slips through.

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

- **Perceptual audio quality**: GATE C-2 proves *energy envelope
  fidelity* in dBFS — it cannot distinguish a 440 Hz pure tone from
  a band-limited noise burst of the same RMS. If the go-virtio/sound
  mixer ever starts emitting white noise at the right energy, GATE C-2
  will PASS. The combination of GATE C-1 (the engine asked for the
  RIGHT lump) + GATE C-2 (the host received audio with the right
  envelope) is the strongest empirical+provable bound the protocol
  offers; sample-accurate WAV equality is intentionally NOT a goal
  (QEMU's wav backend resamples and TamaGo can't pin the audiodev's
  internal clock).
- **Music**: Phase 1 of the demo is SFX-only (engine plays no MUS
  tracks). When music lands (planned R-doom2a), GATE C-2's metric
  may need re-justification — continuous content unlocks spectral
  fingerprinting which is otherwise overkill here.
- **Real player input latency**: provable_test.sh measures *correctness*
  of the input pipeline (the right frames appear after the right
  inputs), not *responsiveness* (how many ms between QMP send-key
  and frame change). The latter is an empirical micro-benchmark
  (R-doom1f territory).
- **Long-term stability**: checkpoints stop at tic 1050 (~30 s of
  engine time). R-doom1c's gpu-virtqueue back-pressure manifests
  later; that is a separate gate.
