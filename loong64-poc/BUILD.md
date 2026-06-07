# tamago/loong64 hello-world POC — VERIFIED BOOTING (2026-06-03)

Pure-Go bare-metal "hello" on QEMU LoongArch `virt`, via a loong64-patched
TamaGo. Prints `hello from tamago/loong64` over the ns16550a UART.

## Apply
- `tamago-loong64-fork.patch`   → usbarmory/tamago-go (branch `latest`), then `cd src && ./make.bash`
  (upstream-shaped, PIE-free — mirrors tamago-go PR usbarmory/tamago-go#17).
- `tamago-loong64-framework.patch` → usbarmory/tamago (adds goos/goos_loong64.s + loong64/ pkg)
- `main.go` here = the board+app (RAM/UART wiring + main).

> The hello PoC boots via `-kernel` in DA mode and needs **no PIE**. The
> relocatable-image (`.efi`) path uses a **cloud-boot-local** overlay,
> `../tamago-loong64-pie.patch`, applied on top of the fork patch — kept out of
> the upstream proposal per usbarmory/tamago#70 (maintainer wants binary
> modification external). It feeds go-coff's `pectl link-pie` ELF(PIE)→PE32+/EFI
> wrapper. Don't fold it back into `tamago-loong64-fork.patch`.

## Build
```sh
export GOOSPKG=github.com/usbarmory/tamago
GOOS=tamago GOARCH=loong64 <tamago-go>/bin/go build -ldflags "-T 0x2010000 -R 0x1000" -o hello.elf .
```

## Run
```sh
qemu-system-loongarch64 -M virt -m 256 -nographic -kernel hello.elf
# => hello from tamago/loong64
```

## Bring-up notes (the non-obvious bits)
- Link ABOVE QEMU virt's reserved low memory (boot_info 0–0x100000, fdt
  0x100000–0x200000): RAM region + text at 0x2000000 (32 MB).
- QEMU enters -kernel in direct-address (DA) mode (VA==PA): no DMW/paging
  needed for this POC.
- LoongArch boots with the FPU OFF → `·Init` must enable it
  (`csrwr Rx, EUEN(0x2)`, WORD-encoded; Go's loong64 asm has no CSR mnemonic).
- UART (ns16550a) THR @ 0x1fe001e0.

## Remaining for a real port
exceptions/timer/IRQ, DMW + paging (for >256 MB / proper cache attrs), the
UEFI variant (efi_main + ConOut) for cloud-boot, RNG, upstream coordination.

## Status of the production-port checklist (2026-06-03)

Verified functional on QEMU loongarch `virt` (DA mode, -kernel):
- ✅ boots; GC/allocator, **goroutines + channels**, maps, slice growth all work
- ✅ **#1 real monotonic Nanotime** — stable-timer counter (RDTIMED) + CPUCFG freq
- ✅ **#3 real RNG** — timer-seeded splitmix64 (replaced the constant stub;
  NOT crypto-grade — needs a HW entropy source / virtio-rng for production)

Remaining — scoped precisely, each with a real blocker (NOT done; not fakeable
blind):
- ✅ **#2 exception/trap handling.** Done — `CSR.EENTRY` set (WORD-encoded csrwr)
  to a handler that announces faults on the UART and halts; verified by a
  deliberate bad-address read printing `EXC` instead of the old silent jump-to-0.
  (QEMU honored the address — the 4 KiB-align concern was moot here. A real port
  would still align it, add full context save/restore, and dispatch on `ESTAT`.)
- ⏳ **#1 timer-driven preemption.** Now blocked on the *full* IRQ machinery:
  enabling the timer interrupt (TCFG/ECFG/CRMD.IE) means the handler must do
  context save/restore + `ESTAT` dispatch + `ERTN` (else the first tick hits the
  halt-handler and stops). Plus Go-scheduler preempt integration. All-or-nothing
  deep asm. (Cooperative + channel scheduling already works without it.)
- ⏳ **#4 DMW + paging.** Re-link at a DMW virtual window (0x9000…) + set
  DMW0/DMW1 + enable PG in bring-up, then jump high. High silent-hang risk blind;
  DA mode already covers QEMU virt's ≤256 MB, so this is for real HW / >direct
  range.
- 🔄 **#5 UEFI variant — CORRECTION.** My earlier "Go linker can't emit loong64
  PE" was wrong: **go-coff/peln** (sibling repo) is a pure-Go ELF→PE32+/EFI
  linker that ALREADY supports loongarch64 (machine 0x6264 + full R_LARCH_*
  relocs, tests green). So PE/EFI output is solved. Caveat: peln consumes
  **ET_REL relocatable objects** = TinyGo's `tinygo build -o main.o` output.
  The gc toolchain (TamaGo) emits Go-format objects / ELF *executables*, not a
  single ET_REL LLVM object, so it can't feed peln directly. Therefore:
    • **loong64 UEFI = TinyGo + go-coff** — cloud-boot's EXISTING pipeline
      (loader/ + tinygo-loongarch64-uefi overlay + go-coff loong64 reloc). This
      capability already exists; #5 is effectively available today via TinyGo.
    • **loong64 UEFI via TamaGo** would need a new gc-ELF-executable→PE wrapper
      (peln is object-input/TinyGo-shaped) — a small go-coff addition, the only
      real gap for unifying UEFI onto TamaGo.

Path: these want iterative on-hardware/QEMU debugging (and, for #5, toolchain
work) + upstream collaboration — not blind one-shot asm.
