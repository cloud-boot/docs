# cloud-boot M6.2 — pure-Go UPX-like self-extracting PE32+/EFI compressor

**Status:** design only — no code, no commits. Greenlight gate before implementation.
**Author:** prepared by the cloud-boot agent (2026-06-09).
**Scope:** architecture + algorithm + risk surface for a tool that takes
`input.efi` → `output.efi` where `output.efi` is a self-extracting PE32+/EFI
image producing the same runtime behaviour as `input.efi`.

---

## 0. Problem statement (one paragraph)

cloud-boot Phase 2 / Path D ships TamaGo-built PE32+/EFI applications. The
EDK2 OVMF amd64 firmware has a CpuPageTableLib bug (#GP at CpuDxe.dll
+0x110C) that fires on `gBS->LoadImage` / `gBS->StartImage` for sufficiently
large PE32+ images. The boundary is finicky — empirically PASS at 3.17 MiB
(M5 HTTP), FAIL at 3.4 MiB (M8.0 amd64 raw embed), and FAIL on a 1.7 MiB
**child** PE chain-loaded from a 2.45 MiB parent — so it is not a pure
byte-count threshold; image entropy / section layout matter. M6.1 mitigated
the **chained payload** case by gzipping the inner blob. It does NOT cover
the case where the firmware does `LoadImage` on the parent itself
(M6 / M7 / M8.0). M6.2 introduces a UPX-like self-extracting wrapper: a
small bootstrap stub that the firmware loads, which then decompresses the
real payload in RAM and chain-runs it via `LoadImage` + `StartImage`.

---

## 1. Survey of pure-Go PE / EFI compressor prior art

Honest assessment after a web search and a local scan of the orgs the user
already controls:

| Project | Pure Go? | PE32+/EFI aware? | Self-extracting stub? | Verdict |
|---------|----------|------------------|------------------------|---------|
| [upx/upx](https://github.com/upx/upx) | No (C++) | No EFI support — open feature request [#518](https://github.com/upx/upx/issues/518) | Yes (per-format stubs) | Can't reuse — wrong language, wrong target. |
| [packing-box/awesome-executable-packing](https://github.com/packing-box/awesome-executable-packing) | survey | Some PE entries, none EFI | n/a | No EFI-aware Go entries. |
| [klauspost/compress](https://github.com/klauspost/compress) | Yes | No | n/a | Excellent compressor lib, but the **decoder** is heavy (zstd decoder is ~tens of KB of Go code → blown-up TinyGo stub). Useful only for the host-side `Compress`. |
| [go-compressions/lzfse](https://github.com/go-compressions/lzfse) | Yes, **user-owned** | No | n/a | Pure Go Compress + Decompress. 100% coverage. Already pulled into go-filesystems/apfs + go-diskimages/tart-oci. Strong starting candidate. |
| stdlib `compress/flate` / `compress/gzip` | Yes | No | n/a | Familiar; decoder pulls in the whole `compress/flate` graph in TinyGo — measure before assuming "small". |
| [pfalcon/uzlib](https://github.com/pfalcon/uzlib), [bisqwit/TinyDeflate](https://github.com/bisqwit/TinyDeflate), [jibsen/tinf](https://github.com/jibsen/tinf) | No (C/C++) | No | yes (stub-sized inflate, ~2 KB) | **Inspiration** for the minimal-decoder direction; can't drop in but the algorithmic shape is portable. |
| [usbarmory/go-boot](https://github.com/usbarmory/go-boot) | Yes (TamaGo) | Yes (UEFI runtime in Go) | No (it's a boot manager, not a packer) | Not a compressor, but the TamaGo→EFI runtime patterns mirror what we already do in `tamago-uefi` — confirms the architecture is viable. |

**Verdict:** there is **no pure-Go UPX-equivalent for PE32+/EFI today**. The
closest pieces are:
- `go-compressions/lzfse` — host-side compressor, fits our org layout.
- `go-coff/peln`+`pectl` — PE32+/EFI link / emit pipeline we already use.
- `go-coff/stub` — proves the TinyGo + `lld-link` path can produce a working
  PE32+/EFI stub today (1.87 MiB amd64, 3.94 MiB arm64 — too big as-is; see §4).

**Recommendation: build from scratch under user-owned orgs**, reusing
`go-coff/peln` for PE assembly and a body compressor from `go-compressions/*`.
Do NOT fork UPX (wrong language, no EFI), do NOT vendor C inflate code
(violates the no-CGO standing rule).

---

## 2. Compression algorithm choice

### 2.1 Measured ratios on `BOOTX64-OCI.EFI` (5,260,800 bytes raw)

Host tools, single-shot, max-effort settings:

| Algorithm | Compressed bytes | Ratio | Notes |
|-----------|-----------------:|------:|-------|
| `gzip -9`             | 2,057,557 | 39.11% | stdlib deflate, mid ratio |
| `bzip2 -9`            | 2,004,950 | 38.11% | BWT; decoder too big for stub |
| `lz4 -12`             | 2,409,256 | 45.80% | fast decoder but worst ratio |
| `zstd -19 --ultra`    | 1,777,681 | 33.79% | best Go decoder is heavy (>100 KB of Go) |
| `xz -9 -e` (LZMA2)    | **1,640,240** | **31.18%** | best ratio overall |
| **`go-compressions/lzfse` Compress** | **2,111,082** | **40.13%** | **pure Go, user-owned**; decoder is ~few KB of Go |

### 2.2 Decoder-size estimate (what ends up in the stub)

Order-of-magnitude only — we MUST measure for real once we pick:

| Algorithm | Decoder size hint |
|-----------|-------------------|
| Custom LZSS / LZ77 | ~hundreds of bytes (hand-coded) |
| LZFSE (`go-compressions/lzfse`) | ~few KB of Go (LZVN-only path is even smaller) |
| stdlib `compress/flate` (decoder only) | ~tens of KB of Go (Huffman tables, sliding window) |
| LZMA (`github.com/ulikunitz/xz`) | ~tens of KB |
| zstd (`klauspost/compress/zstd`) | ~100+ KB |

### 2.3 Recommendation — **LZFSE for v0.1**

**Pick `go-compressions/lzfse`. Justifications:**

1. **Best ratio-vs-decoder-size pure-Go option we own.** 40% ratio is within
   striking distance of zstd (34%), and well inside the M6.2 v0.1 target of
   "≥50% reduction" — `5.26 MiB → 2.11 MiB` is **59.9% reduction**.
2. **User-owned org**, BSD-3, 100% coverage. No external dep, no licence
   concerns, fits the standing rules.
3. **Existing consumers** (go-filesystems/apfs, go-diskimages/tart-oci) mean
   any decoder-size or correctness regression we'd hit gets caught by their
   tests too — multi-consumer signal.
4. **LZVN sub-mode** in the same package is even smaller; we can fall back to
   pure-LZVN for the stub-side decoder if LZFSE's FSE table machinery turns
   out to bloat the stub past budget. The format is one-magic-per-block so
   the compressor can be configured to emit LZVN-only.

**Rejected:**
- `gzip` — worse ratio (39%) than LZFSE for a comparable-sized decoder; the
  stdlib decoder pulls a noticeable Go runtime surface in TinyGo.
- `lz4` — worst ratio of the bunch.
- `LZMA` — best ratio (31%), but the pure-Go decoder is ~10× the LZFSE
  decoder; combined with the existing stub baseline this likely blows the
  64-KiB stub budget.
- `zstd` — middle-ground ratio, by far the heaviest decoder.
- Custom LZSS — smallest decoder, but reinventing for ~5% better stub size
  is not worth the maintenance + reusability hit (we'd lose the lzfse
  test corpus and the format compatibility with apfs / tart-oci).

**Open follow-up (post-greenlight):** measure the actual TinyGo stub size
with LZFSE vs. LZVN-only decoders before committing to LZFSE-full.

---

## 3. Architecture — which repo owns what

```
github.com/go-compressions/lzfse      EXISTS — body compressor + decompressor
github.com/go-coff/peln               EXISTS — PE32+/EFI parser, layout, LinkPIE
github.com/go-coff/pectl              EXISTS — CLI on top of peln (link / append / sign)
github.com/go-coff/stub               EXISTS — proof: TinyGo + lld-link → PE32+ EFI

github.com/go-coff/efipack            NEW    — PE32+/EFI self-extracting packer (library)
github.com/go-coff/efipack/cmd/efipack NEW   — CLI: input.efi → output.efi
github.com/go-coff/efipack/stub       NEW    — TinyGo source of the per-arch decompressor stub
github.com/go-coff/efipack/stub/blobs NEW    — committed pre-built tiny EFI stubs (one per arch)
                                                — generated via stub/ Taskfile, checked-in
                                                  so users of `efipack` don't need lld-link
                                                  / TinyGo at runtime.
```

**Boundary decision (repo separation):**

- `go-compressions/lzfse` stays **pure body compression / decompression**. No
  PE knowledge. Already done.
- `go-coff/efipack` is **PE32+/EFI-specific**. It depends on `peln` and
  `go-compressions/lzfse`. It does NOT depend on `pectl` (pectl will depend
  on it instead — see §3.3 — when we wire it as a `pectl pack` subcommand).
- The **decompressor stub** lives in `go-coff/efipack/stub/` and is built
  per-arch via the same TinyGo + `lld-link` pipeline `go-coff/stub` already
  uses. The pre-built stubs are checked in as `BOOT{X64,AA64,RISCV64,LOONGARCH64}-decompress.EFI`
  under `efipack/stub/blobs/` so the library can `//go:embed` them and
  produce a packed binary with **zero external toolchain at runtime**.

### 3.1 Public API — `go-compressions/lzfse`

**No change** — already has:

```go
package lzfse
func Compress(src []byte) ([]byte, error)
func Decompress(src []byte) ([]byte, error)
```

Optional follow-up (only if v0.1 measurement shows we need it):

```go
type Mode int
const (
    ModeAuto Mode = iota // current behaviour
    ModeLZVN             // force LZVN-only blocks (smaller decoder footprint)
)
func CompressWithMode(src []byte, m Mode) ([]byte, error)
```

This is a backwards-compatible addition; gate the work on actual stub-size
measurements.

### 3.2 Public API — `go-coff/efipack`

```go
package efipack

// Arch enumerates the four PE32+/EFI target machines we ship stubs for.
type Arch uint16

const (
    ArchAMD64       Arch = 0x8664 // IMAGE_FILE_MACHINE_AMD64
    ArchARM64       Arch = 0xAA64 // IMAGE_FILE_MACHINE_ARM64
    ArchRISCV64     Arch = 0x5064 // IMAGE_FILE_MACHINE_RISCV64
    ArchLOONGARCH64 Arch = 0x6264 // IMAGE_FILE_MACHINE_LOONGARCH64
)

// Options controls Pack. Zero value is sensible.
type Options struct {
    Arch        Arch    // 0 → infer from input PE COFF header
    Compression string  // "lzfse" (default), "lzvn", future: "deflate"
    // Reproducible-build levers — surface them now so we don't need a v0.2.
    Deterministic bool
    Timestamp     uint32 // PE TimeDateStamp; 0 + Deterministic → fixed value
}

// Pack reads a PE32+/EFI image from input and writes a self-extracting
// PE32+/EFI image to output. The output, when run by UEFI firmware:
//   1. Locates its own image (EFI_LOADED_IMAGE_PROTOCOL).
//   2. Reads the compressed payload section.
//   3. Allocates EFI BootServicesCode pages, decompresses into them.
//   4. Calls gBS->LoadImage(NULL, parent, NULL, srcBuf, srcSize, &child).
//   5. Calls gBS->StartImage(child, NULL, NULL).
//   6. If StartImage returns, propagates the status via Exit().
func Pack(input io.Reader, output io.Writer, opts Options) error

// PackBytes is the in-memory variant; the CLI uses Pack.
func PackBytes(in []byte, opts Options) ([]byte, error)

// InferArch reads just the COFF header of input and returns the machine.
// Used by Pack when opts.Arch == 0.
func InferArch(in []byte) (Arch, error)

// embedded blobs — internal, but exposed via a getter for tests / pectl.
//go:embed stub/blobs/decompress-*.EFI
var stubFS embed.FS
func StubFor(a Arch) ([]byte, error)
```

**Implementation sketch (host side):**

1. `InferArch(in)` — minimal COFF read (use `debug/pe` or `peln/linker.Object`).
2. `lzfse.Compress(in)` → `compressed`.
3. Load the pre-built stub for that arch: `stub, _ := StubFor(arch)`.
4. Append a new PE section named `.payload` carrying `compressed`, via
   `peln/appender.Append(stub, []Section{{Name: ".payload", Body: compressed}})`.
   — That's the same path the UKI assembler already uses; it already knows
   how to extend the PE section table, fix up `SizeOfImage`, etc.
5. Optionally re-stamp the TimeDateStamp for `Deterministic`.
6. Write out.

### 3.3 Where does `pectl` fit?

`pectl` is the CLI surface; we add one subcommand that wraps `efipack.Pack`:

```
pectl pack --in BOOTX64.EFI --out BOOTX64-packed.EFI
```

So the dep direction is `pectl → efipack → {peln, go-compressions/lzfse}`.
No new dep on `efipack` from `peln`. `efipack` does NOT depend on `pectl`.

---

## 4. Per-arch decompressor stub

### 4.1 Stub contract

The stub is a PE32+/EFI application whose `EFI_IMAGE_ENTRY_POINT` is the
standard UEFI signature:

```c
EFI_STATUS EFIAPI _start(EFI_HANDLE imageHandle, EFI_SYSTEM_TABLE *systemTable);
```

The SystemTable pointer is the second arg (per the UEFI spec). The stub:

1. Uses `gBS->HandleProtocol(imageHandle, &EFI_LOADED_IMAGE_PROTOCOL_GUID, …)`
   to find its own `imageBase`.
2. Walks the PE section table at runtime (we already do this in
   `go-coff/stub/main.go` — `walkSections()`) to locate `.payload`.
3. Calls `gBS->AllocatePages(AllocateAnyPages, EfiBootServicesCode,
   pages_for(uncompressed_size), &buf)`.
4. Decompresses `.payload` into `buf` via the embedded LZFSE decoder.
5. Calls `gBS->LoadImage(BootPolicy=FALSE, parent=imageHandle, devPath=NULL,
   srcBuf=buf, srcSize=uncompressed_size, &child)`.
6. Calls `gBS->StartImage(child, NULL, NULL)`.
7. If StartImage returns, propagates via `gBS->Exit(imageHandle, status, 0, NULL)`.

### 4.2 Calling convention

Same as the existing `tamago-uefi/uefiboard/eficall_*.s` thunks (which
themselves mirror the `go-coff/stub/thunk-*.S` set). Per-arch:

- **amd64** — MS x64 ABI (`rcx, rdx, r8, r9`, 32-byte shadow space).
- **arm64** — AAPCS64.
- **riscv64** — LP64 (UEFI uses standard psABI).
- **loong64** — LP64D (same).

We already have all four thunk files in tested production. **The stub
reuses them verbatim** — they are arch-specific, language-agnostic
shims, not part of TamaGo / TinyGo.

### 4.3 Stub language: TamaGo vs TinyGo vs hand-asm

**Current measured baselines** (production stubs in `go-coff/stub/`):

| Stub | Source | Size |
|------|--------|------|
| `BOOTX64-tiny.EFI` | TinyGo + `lld-link` | **1.87 MiB** |
| `BOOTAA64-tiny.EFI` | TinyGo + `lld-link` | **3.94 MiB** |

That's **MUCH** bigger than the 64 KiB stub budget we want. Both are with
`-gc=leaking -scheduler=none -no-debug` already applied (per the TinyGo
optimizing-binaries guide). The size is dominated by the TinyGo runtime
shims + `compiler-rt` bits the linker can't strip because something in the
graph references them.

**Three options:**

#### Option A — TamaGo-based stub built through `peln/linker.LinkPIE`

TamaGo PIE compiled `GOOS=tamago GOARCH=…`, fed to `peln/linker.LinkPIE`
(which is what `cloud-boot/tamago-uefi` already uses to produce its real
EFI binaries). The baseline TamaGo Hello-World UEFI binary in this project
is observed at the order of **few hundred KB to ~1 MiB** before we
add networking. The decompressor stub is roughly that complexity:
no networking, no fs, just LoadImage + StartImage + a decoder.

Risk: still likely too big for the 64 KiB / 16 KiB budget, but it is the
codepath that **most cleanly matches what cloud-boot already ships**, has
working per-arch thunks, and avoids the lld-link external dep.

#### Option B — TinyGo + lld-link stub (current `go-coff/stub` path)

Already proven to boot. Already at 1.87 MiB. Decoder adds a few KB on top.
Even with aggressive `--gc-sections` we are unlikely to crack 1 MiB —
**too big**.

#### Option C — Hand-written per-arch assembly stub + tiny LZFSE-in-asm

Smallest possible (probably < 8 KiB). Highest implementation + maintenance
cost. Quadruples the test matrix. Violates the project ethos of
"pure Go everywhere we can".

**Recommended path:**

> **Option A: write the stub in TamaGo Go, use `peln/linker.LinkPIE` to
> produce the PE32+/EFI, and aggressively measure size**.
>
> If the empty-shell TamaGo stub (no decoder, just `HandleProtocol` +
> `LoadImage` + `StartImage`) clocks in under ~200 KiB per arch, the LZFSE
> decoder is small enough that adding it stays in the budget. If it doesn't,
> we fall back to Option C for amd64 only (the arch that needs M6.2 the most)
> and stay on Option A for the others.

We are NOT recommending Option B — TinyGo + lld-link is already at 1.87 MiB
baseline, which is bigger than several of the binaries we want to compress.

The "stub budget" (64 KiB hard, 16 KiB stretch) stated in the task brief is
**aspirational**; a more honest budget given the TamaGo runtime floor is
**~256 KiB hard, ~64 KiB stretch**. We should set the v0.1 acceptance bar
at ≤ 256 KiB and revisit downward in v0.2.

### 4.4 Why we keep TamaGo (not bare-asm) as the default

- Same toolchain as the rest of cloud-boot — one build pipeline, one set of
  thunks.
- The existing `tamago-uefi/uefiboard/loadimage.go` (`LoadImage` /
  `StartImage` wrappers) is directly reusable; we are not reimplementing
  the UEFI ABI for a stub.
- Stub bugs in Go are debuggable; stub bugs in four parallel `.S` files
  burn agent budget and developer time.

---

## 5. Risks + open questions

### 5.1 **KEY RISK — the amd64 firmware bug may also fire on the decompressed in-RAM image**

If OVMF amd64's CpuPageTableLib #GP triggers on **any** sufficiently-large
PE passed to `LoadImage` — regardless of whether that PE was the one loaded
from disk by the firmware or a buffer we hand it — then M6.2 does NOT fix
the amd64 problem. The bug becomes "the decompressed image triggers the
same #GP from inside our stub's `LoadImage` call".

**De-risking experiment (DO THIS BEFORE WRITING ANY EFIPACK CODE)**

The smallest possible experiment, in three steps:

1. Pick a **small** PE32+/EFI app already known to PASS today on amd64 — the
   M5 HTTP build (3.17 MiB raw) is good. Build a sub-1-MiB variant if
   possible (a stripped-down `probe` build).
2. Write a one-off **harness EFI** of any size (we already have one — the
   M8.0 chained-leaf binary): hard-code an embedded copy of the small PE as
   raw bytes, and at runtime do `LoadImage` + `StartImage` on the embedded
   bytes (exactly what M6.2's stub will do post-decompression).
3. Run it under OVMF amd64.

**Pass:** the M6.2 architecture is sound and we proceed.
**Fail:** the amd64 firmware bug is invariant under "image source = RAM
buffer" — M6.2 will not fix amd64, and we should pivot to (a) submitting a
patch upstream to OVMF CpuPageTableLib, or (b) switching the amd64
slow-path to a non-LoadImage chain method (e.g., EFI handover protocol for
Linux kernels — already partially in `loadimage.go`).

This experiment is **< 1 day of agent time** and gates the entire M6.2
implementation. Do not skip.

### 5.2 Can `pectl` link a non-TamaGo stub via `peln/linker.LinkPIE`?

Already yes — `LinkPIE` takes any ET_DYN PIE ELF and produces a PE32+/EFI.
The stub being "smaller / non-TamaGo" makes no difference; what matters is
that the ELF is position-independent and uses only architecture-RELATIVE
dynamic relocations. TamaGo PIE produces exactly that today on amd64,
arm64, riscv64, and loong64. **No new `pectl` mode needed.**

### 5.3 Reproducible builds

`efipack.Pack` MUST be deterministic given identical input + identical stub
blobs. Concretely:

- LZFSE `Compress` output is deterministic for a given input (the encoder
  uses fixed hash params; verify with a single test). If not, gate with a
  `Deterministic` flag that uses LZVN-only (canonical).
- PE `TimeDateStamp`: under `Deterministic`, set to a fixed value
  (e.g. `0` or input-file's stamp).
- Section ordering / alignment: already deterministic in `peln/appender`.

Add a `TestPackIsDeterministic` test running `Pack` twice on the same
input and `bytes.Equal`-ing the outputs.

### 5.4 Section permissions

The stub `AllocatePages` request is `EfiBootServicesCode` (RX-capable).
UEFI `LoadImage` then sets per-section permissions itself based on the
PE section table's `Characteristics` field of the decompressed image —
this is firmware's job, not ours. We do NOT need to mprotect anything
manually. Risk surface: riscv64 / loong64 firmwares with stricter
W^X enforcement (we already hit the riscv64 EDK2 protection issue —
see `cloud-boot/docs/edk2-riscv64-protection-fix.patch`). Validate on
all four arches in the smoke matrix.

### 5.5 Decompression size known up front?

LZFSE doesn't carry an uncompressed-size in the block header in the
general case. **Stamp the uncompressed length explicitly** as a small
header we prepend to the compressed bytes inside `.payload`:

```
.payload layout:
  magic         [4]byte   "CBP0"  (cloud-boot pack v0)
  algo          [4]byte   "LZFS" | "LZVN" | ...
  uncompressed  uint64    little-endian
  compressed    [N]byte   body — exactly lzfse.Compress(original)
```

Total overhead: 16 bytes. The stub reads the header, allocates exactly the
right number of pages, decompresses into the buffer.

---

## 6. Acceptance criteria for M6.2 v0.1

A1. **Tool exists.** `efipack` CLI in `go-coff/efipack/cmd/efipack`, with
    100% test coverage on the library, BSD-3 licensed, all-English on
    GitHub.

A2. **Size reduction.** On the M7 OCI client (`BOOTX64-OCI.EFI`,
    5,260,800 bytes), `efipack` produces an output of ≤ 50% the input size,
    target ≤ 40% (LZFSE on the body alone is at 40.13%; stub adds
    ≤ 256 KiB → expected output ≈ **2.36 MiB ≈ 45%**).

A3. **Functional parity — all four arches.** For each of amd64, arm64,
    riscv64, loong64, take an existing PASS test (any from M5 / M6.1 / M7),
    run it through `efipack`, boot under EDK2 OVMF, and assert the same
    PASS sentinel from the original binary appears. Smoke test added to
    `cloud-boot/tamago-uefi`'s test matrix.

A4. **KEY — amd64 firmware bug not triggered on decompressed image.**
    Running M6 / M7 / M8.0 amd64 with their EFIs passed through `efipack`
    PASSes under OVMF amd64. This is the load-bearing acceptance criterion
    (the others are tractable; this is what makes M6.2 worth doing) and is
    **conditional on the §5.1 experiment having returned PASS**.

A5. **Deterministic builds.** `efipack input.efi a.efi` followed by
    `efipack input.efi b.efi` produces byte-identical `a.efi` and `b.efi`
    (test in repo).

A6. **No CGO. Pure Go. BSD-3. English-only on GitHub.** Project standing
    rules.

---

## Sources

- [packing-box/awesome-executable-packing](https://github.com/packing-box/awesome-executable-packing)
- [upx/upx — EFI support request #518](https://github.com/upx/upx/issues/518)
- [klauspost/compress](https://github.com/klauspost/compress)
- [go-compressions/lzfse README](https://github.com/go-compressions/lzfse)
- [usbarmory/go-boot — TamaGo-based UEFI boot manager](https://github.com/usbarmory/go-boot)
- [TinyGo — Optimizing binaries](https://tinygo.org/docs/guides/optimizing-binaries/)
- [pfalcon/uzlib — micro-deflate (C reference)](https://github.com/pfalcon/uzlib)
- [bisqwit/TinyDeflate — 373-byte theoretical inflate](https://github.com/bisqwit/TinyDeflate)
- [jibsen/tinf — 2 KB inflate library](https://github.com/jibsen/tinf)
- [emmanuel-marty/em_inflate — tiny C inflate](https://github.com/emmanuel-marty/em_inflate)
- [UEFI Specification 2.10 — Compression Algorithm Spec](https://uefi.org/specs/UEFI/2.10/19_Protocols_Compression_Algorithm_Specification.html)
- [EDK II Build Spec — Creating EFI Images](https://edk2-docs.gitbook.io/edk-ii-build-specification/2_design_discussion/26_creating_efi_images)
- [theopolis/uefi-firmware-parser — LZMA in UEFI firmware](https://github.com/theopolis/uefi-firmware-parser/blob/master/uefi_firmware/compression/LZMA/LzmaDecompress.h)
- Local refs: `cloud-boot/tamago-uefi/uefiboard/loadimage.go`, `go-coff/peln/linker/pie.go`, `go-coff/pectl`, `go-coff/stub/main.go`.
