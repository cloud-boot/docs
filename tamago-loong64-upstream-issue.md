# Proposal/WIP: loong64 (LoongArch64) bare-metal port — working PoC, seeking design guidance to complete

(Draft for usbarmory/tamago — English, public-facing. Post to the tamago repo
issues; the working patches + demo are the evidence.)

---

**Title:** loong64 (LoongArch64) port: working bare-metal PoC, seeking guidance to land it fully

Hi — I'd like to contribute **`GOOS=tamago GOARCH=loong64`** and have a working
proof-of-concept I'd like to align on before pushing toward a complete,
upstreamable port.

## What works today (verified on QEMU `-M virt`)

A loong64-patched `tamago-go` + a `loong64` framework package boot a pure-Go
program bare-metal and run a **functional Go runtime**:

- boots; **GC/allocator, goroutines + channels, maps, slice growth** all work
- **monotonic `Nanotime`** via the stable timer (`RDTIMED`) + `CPUCFG` frequency
- **RNG** via a timer-seeded PRNG (placeholder for a real entropy source)
- **exception handling**: `CSR.EENTRY` → a handler that reports faults instead of
  the silent jump-to-0 (verified with a deliberate bad-address read)

Demo output:
```
tamago/loong64: runtime up
monotonic clock: busy-loop took ~ 2529 us
map[5]: 25
PRNG samples (should differ): 41920 57918
goroutine workers total: 150500
tamago/loong64: DONE
triggering a test fault (expect EXC)... EXC
```

## How it's structured (mirrors the riscv64 port)

- **tamago-go fork (small):** `tamago/loong64` added to the GOOS/GOARCH allowlist
  (`cmd/dist/build.go`, `internal/platform`), `cmd/link/.../loong64` accepts
  `Htamago`, `os_tamago_loong64.go`, `rt0_tamago_loong64.s`,
  `sys_tamago_loong64.s` (rt0 bring-up + `GetG` + stubbed `Wake`),
  `cpu_loong64_other.go` (osInit stub). Upstream Go already ships loong64 codegen
  + base runtime, so this is glue, not a backend.
- **tamago framework:** `goos/goos_loong64.s` + a `loong64/` package
  (`Init`/`cpuinit` bring-up — FPU enable via WORD-encoded `csrwr EUEN`, SP setup,
  `_rt0_tamago_start`; stable-timer counter; minimal exception entry).

(Patches available; happy to open a PR in whatever shape you prefer.)

## Where I'd like your design guidance (the remaining pieces)

These are coupled to tamago internals / the toolchain, so I want to match your
architecture rather than guess:

1. **Timer-driven preemption / IRQ.** riscv64's `trapHandler` jumps to
   `handleInterrupt` / `systemException` (the runtime's IRQ→`SIGTRAP` signal
   path). What's the intended way to wire a new arch into that — which
   runtime-side hooks must a loong64 trap handler provide, and how should
   context save/restore + `ERTN` integrate with the signal/preempt machinery?
2. **DMW + paging.** For >direct-range RAM and proper cache attributes — any
   preference on the bring-up sequence (DMW0/1 windows + `CRMD.PG`) and the link
   layout vs. the current DA-mode PoC?
3. **UEFI** (longer-term, for a boot-manager use case): a loong64 `uefi` board
   analogous to go-boot — happy to drive this once the core lands.
4. **Naming/structure**: package name `loong64`, CSR-op convention (Go's loong64
   assembler has no `csr*` mnemonic — I WORD-encode; would you prefer adding the
   mnemonics?), and how you'd like the PoC split into reviewable PRs.

(Relocatable-image / `-buildmode=pie` packaging is handled **externally** on the
cloud-boot side — no toolchain change requested here — so it's intentionally out
of scope for this port.)

I'll follow whatever contribution and sign-off process you use, and align on the
review flow once we get to a PR. Is loong64 wanted, and is anyone already on it?
Happy to do the work — just want it shaped to land.
