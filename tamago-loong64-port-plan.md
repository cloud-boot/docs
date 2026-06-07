# Porting loong64 (LoongArch64) to TamaGo — plan & recon

Goal: add `GOOS=tamago GOARCH=loong64` to TamaGo so cloud-boot can unify its
UEFI loader on **one** toolchain (today: TamaGo for amd64/arm64/riscv64, TinyGo
for loongarch64). UEFI-first (cloud-boot runs as a `.efi` under firmware).

Recon date: 2026-06-03. Upstream check: **no existing loong64 effort** in
usbarmory/tamago (no issue/PR) — clean slate.

## Why it's bounded (the big risks are already retired)

- **No compiler work.** Upstream Go already has loong64 codegen, and the fork
  `usbarmory/tamago-go` already ships the loong64 *base runtime* from upstream:
  `rt0_linux_loong64.s`, `sys_linux_loong64.s`, `asm_loong64.s`,
  `atomic_loong64.s`, `memmove/memclr_loong64.s`, `tls_loong64.s`,
  `preempt_loong64.{go,s}`, `os_linux_loong64.go`, `stubs_loong64.go`, …
- **`GOOS=tamago` is an overlay**, not per-arch runtime files: there are **no
  `*_tamago*` files** in the fork's `src/runtime`. The OS layer is provided by
  the framework's `goos/` package via `GOOSPKG`. So the fork stays minimal.
- **riscv64 is the template** and it's small: the whole `riscv64/` HW package is
  ~922 lines (Go+asm); the `goos/goos_riscv64.s` entry glue is **12 lines**.
- **kotama** (`usbarmory/kotama`, "tiny GOOS=tamago GOARCH=riscv64 experiment")
  is the minimal boot example to mirror.

## Port surface (three zones)

### ① `usbarmory/tamago-go` (fork) — tiny
Add the `tamago/loong64` pair to the GOOS/GOARCH allowlists:
- `src/internal/platform/supported.go` (+ regenerate `zosarch.go`)
- `src/go/build/syslist.go` and `src/cmd/dist` build config if required

### ② `usbarmory/tamago` `goos/` overlay — minuscule
- `goos/goos_loong64.s` — entry/rt0 glue, mirror `goos_riscv64.s` (~12 lines).

### ③ `usbarmory/tamago` `loong64/` HW package — the real (bounded) work
Mirror `riscv64/` (~700–900 lines Go+asm). LoongArch privileged-arch specifics:

| file | role | LoongArch content |
| --- | --- | --- |
| `loong64.{go,s}` | core ops | CSR read/write (`csrrd`/`csrwr`) |
| `exception.{go,s}` | trap vectors | `CSR.EENTRY`/`ERA`/`ESTAT`/`ECFG` |
| `irq.{go,s}` | interrupts | LoongArch IRQ controller |
| `timer.go` | scheduler tick | constant timer (`CSR.TCFG`/`TVAL`, `rdtime`) |
| `init.{go,s}` | CPU bring-up | (light under UEFI — firmware did most) |
| `features.{go,s}` | CPU id | `CPUCFG` |
| `smp.go` | multicore | stub acceptable initially |
| ~~`pmp.{go,s}`~~ | (RISC-V PMP) | **drop/replace** — LoongArch has its own memory model |

**UEFI-first shrinks ①/③**: under firmware, memory/timer come from Boot
Services (model = TamaGo's existing amd64 UEFI path), so the from-scratch
SoC bring-up (`init`/`timer`/`mmu`) is reduced.

## Testing (no hardware needed)
`qemu-system-loongarch64 -M virt` + **EDK2 LoongArch UEFI** firmware → boot the
`.efi`, print via the UEFI `ConOut` SimpleTextOutput protocol. Suitable for a
dev loop and CI.

## Plan
1. **POC**: tamago-go toolchain + `goos_loong64.s` + a minimal `loong64/` +
   tiny `efi_main` that prints "hello" via `ConOut`, booted under QEMU+EDK2.
   This is the real go/no-go.
2. If the POC boots: flesh out exceptions/irq/timer, then port the cloud-boot
   loader's UEFI surface (`LoadImage`/`StartImage`/`LOAD_FILE2`/`BlockIo`/SFS).
3. Upstream coordination with usbarmory (design-first; PRs).

## Progress (2026-06-03) — empirically verified

> ## ✅✅ POC BOOTS — pure-Go bare-metal loong64 prints over UART
> A loong64-patched TamaGo built a static LoongArch ELF that, under
> `qemu-system-loongarch64 -M virt`, brings up the **full Go runtime**
> (scheduler/GC, `main` as a goroutine) and prints `hello from tamago/loong64`.
> Patches: `tamago-loong64-fork.patch` (137 ins, 7 files) +
> `tamago-loong64-framework.patch` (66 ins, 5 files); app+recipe in
> `loong64-poc/`. Bring-up gotchas solved: link above QEMU's reserved low
> memory (boot_info/fdt → text @ 32 MB); DA mode (no DMW yet); **enable FPU**
> (`csrwr EUEN`, WORD-encoded — Go loong64 asm has no CSR mnemonic); UART
> ns16550a @ 0x1fe001e0. **Feasibility is now demonstrated, not just argued.**


- ✅ **Toolchain compiles loong64.** 4-line allowlist patch
  (`cmd/dist/build.go` + `internal/platform/zosarch.go`, mirroring riscv64) →
  rebuilt tamago-go → `GOOS=tamago GOARCH=loong64 go build` works (exit 0).
  The compiler + loong64 base runtime were already present (upstream Go).
- ✅ **Fork-side runtime glue written and compiling clean.** Three files mirror
  the riscv64 tamago set:
  - `src/runtime/os_tamago_loong64.go` (MemRegion/Text/Data + cputicks)
  - `src/runtime/rt0_tamago_loong64.s` (`_rt0_loong64_tamago` → CPUInit;
    `_rt0_tamago_start` → `rt0_loong64_tamago`)
  - `src/runtime/sys_tamago_loong64.s` (`rt0_loong64_tamago` g0/m0 bring-up +
    hwinit0/check/osinit/schedinit/hwinit1 + newproc(mainPC) + mstart; `GetG`;
    `Wake`/`WakeG` stubbed to fall back to the normal scheduler).
  Re-compiling for tamago/loong64 succeeds → the hand-translated loong64 asm is
  syntactically valid. **Patch saved: `tamago-loong64-fork.patch`** (124 insertions).
- ✅ Tooling confirmed: `qemu-system-loongarch64` + EDK2 LoongArch fw present.

### Remaining (the real bring-up work)
- `tamago/goos/goos_loong64.s` (12-line `CPUInit→cpuinit` shim).
- `tamago/loong64/` package: `cpuinit`→bring-up asm that sets DMW/SP and jumps
  to `_rt0_tamago_start`, `CPU.Init` (goos.Exit/Idle), exit, timer, exception.
  **This is where the LoongArch privileged asm lives (CSR/DMW) — the part that
  must be debugged on real boot, not generated blind.**
- A board/env providing the goos hooks (`RamStart/RamSize/RamStackOffset`,
  `Hwinit1`, `Printk` via the `virt` UART `ns16550a @ 0x1fe001e0` or UEFI
  ConOut, `Nanotime`, `InitRNG`/`GetRandomData`) + `main()` printing "hello".
- Link with `-T`/`-R` for the load address; boot under
  `qemu-system-loongarch64 -M virt`; iterate until ConOut/UART prints.

Recommendation: do the bring-up package with **testable iteration** (boot in
QEMU each step), ideally coordinated upstream — not a one-shot blind asm dump.

## Effort
Moderate, bounded systems work (LoongArch asm + CSRs), templated by riscv64.
Hello-world bootable in days; full port in weeks. Not a research project.

## Caveats / decisions
- Migrating loong64 to TamaGo removes the TinyGo split **only if** the rest of
  the loader is also on TamaGo; otherwise weigh the two-toolchain cost.
- Track TamaGo's Go version (currently 1.26.2).
- Upstream willingness/maintenance is the main non-technical unknown.

## Update 2026-06-04 — PIE + go-coff PIE→PE/EFI wrapper (both DONE & verified)

The UEFI path is no longer blocked. Two pieces landed.

> **Upstream scoping (decided in usbarmory/tamago#70).** PIE is **not** part of
> the loong64 upstream proposal. The maintainer asked to keep any binary
> modification *external* (the go-boot model) and to tackle PIE separately, so
> as not to jeopardise the in-flight loong64 upstreaming. The upstream-shaped
> patch (`tamago-loong64-fork.patch`, mirrored by tamago-go PR
> usbarmory/tamago-go#17) is therefore **PIE-free**. The PIE toolchain edits
> live in a **cloud-boot-local overlay**, `tamago-loong64-pie.patch`, applied on
> top of the fork patch only for cloud-boot's own builds — never proposed
> upstream.

### 1. `-buildmode=pie` for `tamago/loong64` (cloud-boot-local overlay)
Three small edits enable gc to emit a position-independent bare-metal image
(captured in the cloud-boot-local `tamago-loong64-pie.patch`, **not** in the
upstream `tamago-loong64-fork.patch`):
- `src/internal/platform/supported.go`: add `tamago/loong64` to the `pie`
  case of `BuildModeSupported` **and** to `InternalLinkPIESupported`
  (TamaGo links internally — no external linker).
- `src/cmd/link/internal/ld/target.go`: add an `IsTamago()` helper.
- `src/cmd/link/internal/loong64/asm.go`: in the PIE TLS-IE relocation path,
  accept `IsTamago()` alongside `IsLinux()`. The existing relaxation then
  resolves the TLS access to an absolute (local-exec) offset — correct for a
  static bare-metal image.

Result: `go build -buildmode=pie` produces an **ET_DYN** with **only
`R_LARCH_RELATIVE`** dynamic relocs (3861 in the PoC), no symbolic/GOT relocs.
It still **boots on QEMU virt** (loaded at its link base ⇒ load bias 0 ⇒ the
RELATIVE relocs are no-ops): full runtime, timer, RNG, goroutines, EXC handler.

### 2. `peln.LinkPIE` + `pectl link-pie` (go-coff)
New pure-Go path that converts such a PIE into PE32+/EFI (NOT the ET_REL
object linker — there are no symbols to resolve):
- maps each `PT_LOAD` → a PE section at `RVA = p_vaddr - ImageBase`;
- pre-applies each `R_LARCH_RELATIVE` (writes the absolute target VA = addend
  into the image) and records an equivalent **`IMAGE_REL_BASED_DIR64`** base
  reloc so firmware rebases at load;
- entry from `e_entry`; machine/RELATIVE-type table covers loong64/amd64/
  arm64/riscv64.
Files: `peln/linker/pie.go` (+ `pie_test.go`, 100% coverage on the new code,
incl. a guarded test against the real TamaGo PIE) and
`pectl/cmd/link_pie.go` (+ test). debug/pe can't *read* machine 0x6264, so the
tests parse the PE headers by hand.

**End-to-end proven:** the 1.6 MB TamaGo loong64 PIE → 1.1 MB `BOOTLOONG64.EFI`
(PE32+, machine 0x6264, subsystem 10, ImageBase 0x2000000, 7948 B of base
relocations). Command:
`pectl link-pie -o BOOTLOONG64.EFI hello-pie.elf`.

### Still remaining for a bootable `.efi` (tamago-side, coupled to issue #70)
A loong64 **UEFI runtime board**: `efi_main` entry honoring the PE/MS x64-style
calling convention loong64 UEFI uses, Boot/Runtime Services + ConOut wiring
(analogous to go-boot's `uefi` package), and ExitBootServices. That is new
TamaGo framework work, not a toolchain/linker gap — the toolchain + wrapper are
now complete.
