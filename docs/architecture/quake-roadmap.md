<!--
SPDX-License-Identifier: BSD-3-Clause
Copyright (c) 2026 The cloud-boot/docs authors
-->

# Quake roadmap — from DOOM (Phase 3) to Quake on bare metal

**Status:** Draft v1 (2026-06-14). Forward-looking planning document.
The DOOM Phase 3 demo is the baseline ("184 OK / 0 FAILED frames at
640x400 over 50 s on Apple Silicon under QEMU+TCG", see
[DOOM provable protocol](doom-provable-protocol.md)). Everything below
is what comes next.

This document is a roadmap, not a design spec. It is meant to be
technically grounded enough that a contributor could start work
tomorrow from §5; it explicitly avoids prescribing implementation
detail beyond that.

---

## §1 — Goal and non-goals

### Goal

Boot id Software's Quake series (Q1 → Q2 → Q3) on bare metal under
the same TamaGo-UEFI runtime that runs the DOOM Phase 3 demo today,
using only the existing pure-Go go-virtio device stack as its
window-on-the-world. No CGO, no syscalls, no host OS.

### Why Quake (i.e. what does it prove that DOOM does not)

DOOM is a 2D-only sector engine: every frame is a 320x200 8-bit
palette buffer that the engine paints with a software span renderer
and the cloud-boot frontend BGRA-converts into the virtio-gpu
scanout. The whole rendering pipeline lives on the guest CPU; the
host GPU does nothing more than `RESOURCE_FLUSH`. That validates:
2D scanout, AttachBacking, the controlq, virtio-sound at 11025 Hz
mono, virtio-input keyboard. It does **not** validate:

| Capability that DOOM does NOT exercise        | Why Quake exercises it                 |
|-----------------------------------------------|----------------------------------------|
| 3D triangle rasterisation                     | Q1+ are polygon engines; even Q1 soft renderer uses 3D math + spans-of-triangles |
| Host-accelerated GL (virgl)                   | Q1-GL / Q2-GL / Q3 all assume a real OpenGL implementation on the other side |
| Texture upload pressure (BSP lightmaps + skins) | Quake levels carry MiBs of textures vs DOOM's WAD-resident graphics |
| Floating-point hot path                       | Q1's R_DrawSpan is integer; Q2+ matrix math is float — exercises FPU under TamaGo |
| Mouse input                                   | Quake is unplayable on keyboard alone; forces virtio-input mouse bring-up |
| Higher audio mix density                      | Q2/Q3 mix many simultaneous voices vs DOOM's 8-channel SFX |
| UDP networking (multiplayer)                  | Q1/Q2/Q3 all use UDP; would prove a guest UDP stack on bare metal over virtio-net |
| Filesystem density (PAK + nested files)       | DOOM has a single WAD; Quake PAKs have hundreds of nested resources |

Crucially, Q1 has both a software renderer (CPU-only, validates the
"DOOM stack still works at higher complexity" claim) **and** a GL
renderer (validates the virgl path end-to-end with a real, non-toy
workload). That ladder is the load-bearing reason Q1 is the right
next rung after DOOM.

### Non-goals (v1 of this roadmap)

- **Multiplayer.** UDP requires a guest TCP/IP-class stack on bare
  metal; that is its own multi-sprint workstream and is deferred to
  R-quake-MP.
- **Q3 from day one.** Q3 is GL-only and shader-heavy; even with a
  working Q1-GL bring-up it is a separate phase, not a sprint
  extension.
- **Sound music (CD-audio / MIDI / OGG).** SFX only, same as DOOM.
- **Real-time perf parity with ironwail.** A 15-20 FPS bare-metal
  demo on TCG is acceptable for v1; KVM-accelerated perf is a
  separate gate.
- **Mods, custom maps, custom games.** Vanilla shareware/demo PAKs
  only.

---

## §2 — Survey of pure-Go Quake ports (research)

This is the most load-bearing section: the rest of the plan
collapses if a usable upstream exists (we fork it) or expands
considerably if none does (we transpile from C or write from
scratch).

### Quake 1

| Project                                              | Scope                       | License       | Pure Go? | State (2026-06)            |
|------------------------------------------------------|-----------------------------|---------------|----------|----------------------------|
| [`darkliquid/ironwail-go`](https://github.com/darkliquid/ironwail-go) | **Full client** port of ironwail (QuakeSpasm fork) | GPL-2.0       | Yes (CGO=0 stated goal; uses gogpu) | **Active**, 816 commits, "100% behavioral parity" target; AI-assisted; renders maps via `mise run smoke-map-start`; unreviewed by upstream Ironwail authors |
| [`matttproud/go-quake`](https://github.com/matttproud/go-quake) | Quake I **server only**, NetQuake protocol | Apache-2.0    | Yes      | Stub; explicitly "INCOMPLETE"; 12 commits; useful only as a protocol reference |
| `id-Software/Quake` (upstream C) → `ccgo` transpile  | Full client                 | GPL-2.0       | After transpile, generated Go (no CGO) | Hypothetical; same approach gore used on doomgeneric |

**Finding:** `ironwail-go` is the only pure-Go Quake 1 client that
actually loads and renders a level. It is GPL-2.0, which is
compatible with cloud-boot's fork-then-isolate pattern (godoom
already follows it: GPL boundary preserved by living in its own
subrepo, BSD-3 wrappers everywhere else). Its dependence on `gogpu`
is the key technical risk — see §3.

### Quake 2

| Project                                              | Scope                                | License       | Pure Go?            | State (2026-06)            |
|------------------------------------------------------|--------------------------------------|---------------|---------------------|----------------------------|
| [`samuelyuan/go-quake2`](https://github.com/samuelyuan/go-quake2) | **Map renderer only** — loads BSP + textures, no game loop | MIT           | No (uses go-gl + GLFW, CGO) | 44 commits; useful only as a BSP-loader reference |
| [`packetflinger/q2master`](https://github.com/packetflinger/q2master) | Q2 master server                     | Likely GPL    | Yes                 | Active; protocol reference only |
| `id-Software/quake2` (upstream C) → `ccgo` transpile | Full client                          | GPL-2.0       | After transpile     | Hypothetical |

**Finding:** **No pure-Go Quake 2 client exists.** The closest
artefact is a CGO+OpenGL BSP viewer that does not run gameplay.
Q2 thus requires either (a) a fresh `ccgo` transpile of upstream
yquake2/q2pro (analogous to how gore was produced from
doomgeneric) or (b) an AI-assisted hand port in the
ironwail-go style. Either is L-tier effort.

### Quake 3

| Project                                              | Scope                                | License       | Pure Go?       | State (2026-06)            |
|------------------------------------------------------|--------------------------------------|---------------|----------------|----------------------------|
| [`icedream/go-q3net`](https://pkg.go.dev/github.com/icedream/go-q3net) | Q3 wire **protocol library** only (Writer / Header / Message / OOB) | GPL-2.0+      | Yes            | WIP; protocol reference only |
| `criticalstack/quake-kube`                           | Kubernetes orchestration around ioq3 binary | Apache-2.0 | Yes (orchestration) | Orchestrates a C binary; not a Go engine |
| [`0xBrsm/NexQuake`](https://github.com/0xBrsm/NexQuake) | Browser + relay stack; engine is **C compiled to WASM** | Mixed | No (engine is C-WASM) | Active 2026; the Go is a relay, not the engine |
| `ioquake/ioq3` (upstream C) → `ccgo` transpile       | Full client                          | GPL-2.0       | After transpile | Hypothetical |

**Finding:** **No pure-Go Quake 3 client exists, even as a stub.**
Every Q3-in-Go artefact in the wild is either a protocol shim or a
relay/orchestrator around the original C binary. NexQuake is the
shape Q3 takes today in a "Go ecosystem" context — but its engine
is unmodified C running in WASM, which doesn't help a bare-metal
TamaGo runtime that has no JS/WASM host.

### Summary of §2

```
Q1: ironwail-go exists, GPL-2.0, pure Go, depends on gogpu (PURE GO WebGPU framework with a software-renderer mode)
Q2: NOTHING usable. Must transpile or port.
Q3: NOTHING usable. Must transpile or port. Much harder than Q2 (shader-heavy GL3 pipeline).
```

This single result reshapes the plan: Q1 is "fork + integrate";
Q2 and Q3 are "build the upstream first, then integrate".

---

## §3 — Per-game gap analysis (cloud-boot stack vs Quake requirements)

What the current cloud-boot / go-virtio stack already provides
(2026-06-14):

| Capability                  | Where                                  | State                                       |
|-----------------------------|----------------------------------------|---------------------------------------------|
| 2D BGRA scanout             | `go-virtio/gpu.SetupFramebuffer`/`Flush` | Validated 100% cov, real device             |
| 3D virgl context + clear    | `go-virtio/gpu.OpenVirtioGPU3D`/`ClearScreen` | Validated (M1); hand-encoded virgl, no Mesa |
| soft3d (pure-Go raster)     | `go-virtio/gpu/soft3d/soft3d.go` (385 LoC) | Triangle + textured triangle only; **not** a renderer suitable for a real engine |
| Venus (Vulkan-over-virtio)  | `go-virtio/venus`                     | Infrastructure + pixel readback validated   |
| Audio (11025 Hz mono S16)   | `go-virtio/sound`                     | DOOM SFX path validated                     |
| Keyboard (HID)              | `go-virtio/input`                     | DOOM keymap validated                       |
| Mouse                       | n/a                                    | **GAP** — `go-virtio/input` device is keyboard-only on the demo path |
| Network L2 frames           | `go-virtio/net`                       | Header/buffer/TX/RX; no IP, no UDP          |
| UDP / TCP stack             | n/a                                    | **GAP** — `gvisor-tap-vsock` is the leading candidate but not yet wired |
| Embedded WAD/PAK FS         | `cloud-boot/godoom/embedwad`           | Pattern reusable for PAK0/PAK1              |

### Per-game gap matrix

| Requirement                       | Q1 (soft)         | Q1 (GL)              | Q2 (soft)         | Q2 (GL)               | Q3 (GL3)                  |
|-----------------------------------|-------------------|----------------------|-------------------|-----------------------|---------------------------|
| 2D scanout (final blit)           | OK                | OK                   | OK                | OK                    | OK                        |
| 3D triangle rasterisation         | engine does it on CPU; we just blit | OK (host GPU) | engine does it on CPU; we just blit | OK (host GPU)  | n/a (no soft renderer)    |
| OpenGL ≤1.2 (immediate mode)      | n/a               | **needs virgl + Mesa-shaped command stream** | n/a | **needs virgl + Mesa-shaped command stream** | n/a |
| OpenGL 2.x+ (shaders, VBOs)       | n/a               | n/a                  | n/a               | n/a                   | **GAP** — virgl can carry it but we have hand-encoded clears only |
| Vulkan                            | n/a               | n/a                  | n/a               | n/a                   | n/a (no Q3-Vulkan)        |
| Audio (SFX mixer)                 | OK (same shape as DOOM) | OK            | needs more voices but same shape | same | OpenAL-shaped, heavier |
| Keyboard                          | OK                | OK                   | OK                | OK                    | OK                        |
| Mouse                             | **GAP**           | **GAP**              | **GAP**           | **GAP**               | **GAP**                   |
| UDP (multiplayer)                 | non-goal v1       | non-goal v1          | non-goal v1       | non-goal v1           | non-goal v1               |
| PAK filesystem                    | **embed pattern from godoom** | same        | same              | same                  | same                      |

### The GL gap, called out

This is the dominant technical risk. The go-virtio/gpu 3D path is
end-to-end validated for `ClearScreen` only — a 3-command virgl
buffer (`CREATE SURFACE` + `SET_FRAMEBUFFER_STATE` + `CLEAR`). An
actual GL program (even Q1-GL's fixed-function pipeline) demands
hundreds of distinct virgl opcodes plus the equivalent of a Mesa
state tracker on the guest side to turn `glBegin / glVertex /
glTexCoord` into a virgl command stream. That state tracker does
not exist in pure Go anywhere we have found.

Three plausible routes through this:

1. **soft3d-only:** target Q1-software exclusively, never engage
   the host GPU. Avoids the whole problem. Caps us at ~Q1
   performance forever; doesn't validate virgl on a real workload.
2. **Borrow a Go state tracker.** `gogpu/wgpu` is a pure-Go WebGPU
   implementation that already targets multiple backends (Vulkan,
   D3D12, Metal, GLES, software). If we can route its software
   backend's command stream into a "virgl-shim" backend, we get a
   GL-class pipeline. This is precisely what `ironwail-go` does;
   the architectural fit is real.
3. **Hand-extend gpu3d.** Encode each GL call we need as a virgl
   opcode pair (`SUBMIT_3D` + the payload). Tractable for Q1-GL
   (a few dozen distinct draw shapes); painful for Q2; impossible
   in human time for Q3.

The recommended sequence in §4 picks routes 1 → 2 in stages.

---

## §4 — Recommended sequence

A phased plan, smallest first. Each phase has prerequisites,
effort tier (S = 1-2 weeks, M = 1 month, L = 2-3 months, XL = quarter+),
and a validation criterion in the shape of the DOOM provable
protocol where possible.

### Phase Q-1a — Quake 1 software renderer, single map

| Field            | Value                                           |
|------------------|-------------------------------------------------|
| Prerequisites    | None beyond Phase 3 DOOM stack                  |
| Effort           | **M** (1 month)                                 |
| Validation       | Boot `e1m1`, render 35 tics from a fixed start; tic-N framebuffer matches a host-side oracle (byte-equal on host, chi^2-bounded on guest); same shape as R-doom1g |
| Risk             | ironwail-go integration friction; PAK embed; mouse-not-needed-yet (turn `+mlook 0`); pure-soft renderer choice means ignoring `gogpu`'s WebGPU path |
| Output           | `cloud-boot/tamago-uefi/phaseQ1_oci_quake1_soft_boot.go` analogous to `phase3_oci_doom_boot.go` |

### Phase Q-1b — Quake 1 GL renderer (virgl path)

| Field            | Value                                           |
|------------------|-------------------------------------------------|
| Prerequisites    | Q-1a; **plus** gogpu's software backend wired into a "virgl shim" GL-class output |
| Effort           | **L** (2-3 months)                              |
| Validation       | Same oracle as Q-1a but on the GL renderer; guest chi^2 vs guest oracle; first end-to-end proof that virgl works on a real workload |
| Risk             | The virgl shim is the load-bearing unknown; if hand-encoding draws turns out to need ~Mesa-tier complexity, fall back to Q-1a indefinitely |
| Output           | `phaseQ1gl_oci_quake1_gl_boot.go` + a `go-virtio/gpu/virglshim/` subpackage |

### Phase Q-mouse — virtio-input mouse bring-up

| Field            | Value                                           |
|------------------|-------------------------------------------------|
| Prerequisites    | None (parallel to Q-1a/Q-1b)                    |
| Effort           | **S** (1-2 weeks)                               |
| Validation       | A scripted mouse-delta event sequence steers the player; guest scanout at tic N matches the oracle frame produced under the same script |
| Risk             | Low — virtio-input already drives keyboard; mouse is the same wire shape with a different evdev code stream |

### Phase Q-2 — Quake 2 (soft + GL via shim)

| Field            | Value                                           |
|------------------|-------------------------------------------------|
| Prerequisites    | Q-1b; **plus** a Pure-Go Q2 engine (transpile of yquake2/q2pro with `ccgo`, or AI-assisted hand port). This sub-project is itself L-tier. |
| Effort           | **XL** (quarter)                                |
| Validation       | Same oracle shape; first map (`base1`) renders 70 tics within tolerance |
| Risk             | Q2 engine does not exist; whichever route we take for §2 produces our most expensive dependency |
| Output           | `cloud-boot/goquake2` (analogous to `cloud-boot/godoom`) + `phaseQ2_oci_quake2_boot.go` |

### Phase Q-3 — Quake 3

| Field            | Value                                           |
|------------------|-------------------------------------------------|
| Prerequisites    | Q-2; **plus** a Pure-Go Q3 engine (no upstream exists; `ccgo` of ioq3 is the only realistic route); **plus** the virgl shim must reach GL2-shader-class capability |
| Effort           | **XL** (quarter+)                               |
| Validation       | Boot `q3dm1`, render demo loop, byte-equal-on-host / chi^2 on guest at checkpoint tics |
| Risk             | Triple-stacked: untranspiled engine, untested shader-class virgl path, much higher texture/audio pressure. Honest assessment: don't promise dates until Q-2 lands |

### Phase Q-MP — multiplayer (any of Q1/Q2/Q3)

| Field            | Value                                           |
|------------------|-------------------------------------------------|
| Prerequisites    | Guest UDP stack on bare metal over virtio-net (gvisor-tap-vsock is the leading candidate per the weft project notes) |
| Effort           | **L** (separate workstream)                     |
| Validation       | Out of scope for v1 of this roadmap             |

### Phase ladder, visually

```
DOOM (done) ──> Q-1a (soft) ──> Q-1b (GL via virgl shim) ──> Q-2 (soft+GL) ──> Q-3
                    │                                               │
                    └── Q-mouse (parallel, S)                       └── Q-MP (parallel, L)
```

---

## §5 — First-sprint scoping ("DOOM works → Q1-soft renders one map")

This is the concrete work for Phase Q-1a, in dependency order. The
target is e1m1 rendering 35 tics matched against a host oracle.

1. **Decide the engine fork.** Smoke-build `darkliquid/ironwail-go`
   off a `CGO_ENABLED=0 GOOS=linux` Go toolchain. Confirm it (a)
   compiles without any C dependency and (b) renders e1m1 to a
   file-backed framebuffer. If yes, fork into
   `github.com/cloud-boot/goquake1` (GPL-2.0 preserved, same
   convention as godoom). If no, fall back to a `ccgo` transpile
   of WinQuake.

2. **Force the software renderer.** Disable the gogpu/WebGPU
   default path; we only want the CPU span renderer for Q-1a.
   Confirm the engine still boots a map.

3. **Define a frontend interface mirroring `gore.DoomFrontend`.**
   Mechanical shape: `DrawFrame(*image.RGBA)`, `PlaySound(...)`,
   `GetEvent(*Event) bool`, plus the determinism hooks (`SetSeed`,
   `ApplyDeterminism`, `FrameCount`) that R-doom1g already
   established as the contract for the provable protocol. Put it
   under `cloud-boot/goquake1/backend/tamago/frontend.go`.

4. **PAK embed.** Lift the `cloud-boot/godoom/embedwad/` pattern.
   Drop `pak0.pak` (shareware/demo) under
   `cloud-boot/goquake1/embedpak/pak0.pak` behind an `embedpak`
   build tag; expose `embedpak.PAK0() []byte`.

5. **Wire the frontend's GPU adapter.** Same shape as
   `godoom.GPUAdapter`: take the BGRA framebuffer the engine
   produces (Quake's soft renderer outputs an 8-bit palette
   buffer; the palette-to-BGRA conversion runs in the adapter,
   exactly like DOOM's), copy into the `go-virtio/gpu` device
   backing, `Flush()`.

6. **Wire the frontend's sound adapter.** Quake SFX is 11025 Hz
   mono 8-bit unsigned — identical to DOOM's dmx format. The
   `soundWriteShim` from `phase3_oci_doom_boot.go` is reusable
   verbatim; only the upload pipeline changes (PAK lump enumeration
   instead of WAD).

7. **Wire the frontend's input adapter.** Quake needs WASD + mouse;
   for Q-1a we punt mouse to Q-mouse and bind W/A/S/D + Space +
   Ctrl + Esc + Enter via the existing HID-usage shim. `+mlook 0`
   in the autoexec to disable mouse-look.

8. **Add the probe.** `phaseQ1_oci_quake1_soft_boot.go` analogous
   to `phase3_oci_doom_boot.go`: PCI scan for the same three
   virtio devices, pre-detach EDK2 auto-bound drivers, build the
   frontend, hand off to `goquake1.Run`.

9. **Provable-protocol harness.** Mechanically port
   `cloud-boot/godoom/cmd/harvest-reference` to
   `cloud-boot/goquake1/cmd/harvest-reference`; checkpoint tics
   {1, 35, 70, 140, 350}; host oracle byte-equal, guest oracle
   chi^2 ≤ TBD (calibrate at harvest time, same shape as DOOM's
   50000.0 threshold).

10. **CI gate.** New workflow `.github/workflows/quake1-provable.yml`
    mirroring `doom-provable.yml`. PASS = all gates clear; FAIL =
    per-checkpoint diff on stderr.

### What is hard about Q-1a (honest)

- **PAK embedding size.** Shareware PAK0 is ~18 MiB; that goes
  into the TamaGo binary as a const slice. Build-time RAM
  ceiling on the toolchain may bite. Mitigation: build with
  `-tags embedpak` only when shipping the demo image.
- **Renderer determinism.** Quake's soft renderer is integer up
  to and including span fill, but its visibility setup uses
  `gettimeofday` for the `r_speeds` counters; the timing path
  needs the same pinning treatment R-doom1g applied to DOOM
  (`SetDeterministicTics` analogue).
- **ironwail-go provenance.** It is unreviewed by upstream
  Ironwail authors and AI-assisted; we will need our own audit
  pass and our own test coverage targets before we can claim
  cloud-boot quality on it (the org's 100% coverage rule
  applies — see go-deltasync conventions).
- **Frame size.** Quake renders 320x200 by default but is
  trivially resized; the BGRA framebuffer scales fine but the
  oracle PPMs grow ~4-9x vs DOOM's; budget for it.

---

## §6 — Open questions (user decisions before work starts)

| #   | Question                                                                                       | Why it blocks                              |
|-----|-----------------------------------------------------------------------------------------------|--------------------------------------------|
| Q-1 | **Engine choice for Q-1a.** Fork `darkliquid/ironwail-go` (GPL-2.0, AI-assisted, unreviewed by upstream) OR `ccgo`-transpile WinQuake from `id-Software/Quake` (GPL-2.0, mechanical translation, larger up-front effort)? | Determines week-1 work shape and audit posture |
| Q-2 | **PAK source.** Ship shareware `pak0.pak` (legally redistributable, demo episode E1 only) OR require operator-supplied `pak0.pak` + `pak1.pak` (full game)? | Affects what we can commit to the demo image |
| Q-3 | **Performance target.** Is "15-20 FPS on TCG, full speed on KVM" acceptable for Q-1a, or do we gate the phase on a specific FPS floor? | Drives whether soft renderer is "good enough" or Q-1b becomes mandatory |
| Q-4 | **Multiplayer scope.** Confirmed out of scope for v1 of this roadmap, or should Q-MP move earlier? | Affects whether to start gvisor-tap-vsock integration in parallel |
| Q-5 | **GL strategy decision point.** Commit now to route 2 (gogpu virgl-shim) for Q-1b, or defer until Q-1a lands and we have a real measurement of soft-renderer ceiling? | Affects whether to bring `gogpu` into the dependency graph for Q-1a |
| Q-6 | **Q2 engine route.** When we get to Q-2, default to `ccgo`-transpile of yquake2/q2pro (mechanical, reproducible) OR AI-assisted hand port (faster initial bring-up, harder to audit)? | Sets the precedent for how cloud-boot acquires non-existent pure-Go engines going forward |
| Q-7 | **Q3 in v1 of the roadmap at all?** Q3 is XL+ and depends on a virgl shim we haven't built. Drop it from the v1 roadmap and add when Q-2 lands? | Affects scope of this very document |

---

## Appendix A — References

- [DOOM provable test protocol (R-doom1g)](doom-provable-protocol.md) — the
  provable-protocol shape every Quake phase inherits.
- [Phase 3 OCI-DOOM-boot probe](https://github.com/cloud-boot/tamago-uefi/blob/main/phase3_oci_doom_boot.go) —
  the template every `phaseQ*` probe will mirror.
- [godoom TamaGo frontend](https://github.com/cloud-boot/godoom/blob/main/backend/tamago/frontend.go) —
  the frontend interface shape we will mechanically copy.
- [go-virtio/gpu](https://github.com/go-virtio/gpu) — 2D scanout +
  hand-encoded virgl 3D path + soft3d.
- [darkliquid/ironwail-go](https://github.com/darkliquid/ironwail-go) —
  the load-bearing upstream for Q-1.
- [gogpu/gogpu](https://github.com/gogpu/gogpu) — pure-Go WebGPU framework
  with software backend; the most realistic route to a virgl shim.
- [AndreRenaud/gore](https://github.com/AndreRenaud/gore) — the pure-Go DOOM
  engine cloud-boot/godoom forks; established the `ccgo` transpile precedent.
