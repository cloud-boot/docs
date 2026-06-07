// Package riscv64edk2 regresses the EDK2-RiscV image-protection bug
// described in `riscv64-edk2-protection-fix.md`.
//
// The test boots cloud-boot's `BOOTRISCV64.EFI` under
// `qemu-system-riscv64 -machine virt` against the pkgx-shipped
// (or user-provided) EDK2 RISC-V firmware and asserts that the
// runtime reaches its `DONE` marker within a fixed time budget.
//
// All preconditions are environmental, so the test self-skips
// (rather than fails) when:
//
//   - `qemu-system-riscv64` is not on $PATH;
//   - `BOOTRISCV64.EFI` is not readable at $RISCV64_EFI
//     (default `../tamago-uefi/BOOTRISCV64.EFI` resolved from this
//     test file's directory);
//   - `edk2-riscv-code.fd` is not readable at $RISCV64_OVMF_CODE
//     (default `/opt/homebrew/share/qemu/edk2-riscv-code.fd`);
//   - `edk2-riscv-vars.fd` is not readable at $RISCV64_OVMF_VARS
//     (default `/opt/homebrew/share/qemu/edk2-riscv-vars.fd`).
//
// To run against a patched EDK2 build, point $RISCV64_OVMF_CODE at
// the new code fd. The test does not require -cpu max; the default
// rv64 cpu is sufficient.
package riscv64edk2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	defaultEFIRel    = "../tamago-uefi/BOOTRISCV64.EFI"
	defaultOVMFCode  = "/opt/homebrew/share/qemu/edk2-riscv-code.fd"
	defaultOVMFVars  = "/opt/homebrew/share/qemu/edk2-riscv-vars.fd"
	doneMarker       = "DONE"
	helloMarkerPart  = "hello from cloud-boot tamago"
	bootTimeoutSec   = 90
	maxOutputBytes   = 1 << 20 // 1 MiB cap to bound test memory
	relativeFromTest = true
)

// thisFileDir returns the directory containing this test source. We
// use runtime.Caller so the test resolves $RISCV64_EFI relative to
// docs/ regardless of where `go test` is invoked from.
func thisFileDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller(0) failed")
	}
	return filepath.Dir(file)
}

func envOr(name, dflt string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return dflt
}

func readable(p string) bool {
	if p == "" {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// stageESP copies the EFI binary into a fresh directory layout that
// QEMU's "fat:rw:" backend can serve as an ESP. Caller is responsible
// for cleanup.
func stageESP(t *testing.T, efiPath, espRoot string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(espRoot, "EFI", "BOOT"), 0o755); err != nil {
		t.Fatalf("mkdir ESP: %v", err)
	}
	src, err := os.Open(efiPath)
	if err != nil {
		t.Fatalf("open %s: %v", efiPath, err)
	}
	defer src.Close()
	dstPath := filepath.Join(espRoot, "EFI", "BOOT", "BOOTRISCV64.EFI")
	dst, err := os.Create(dstPath)
	if err != nil {
		t.Fatalf("create %s: %v", dstPath, err)
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatalf("copy EFI: %v", err)
	}
}

// stageFirmware copies the (read-only) code/vars fd files into the
// scratch directory because QEMU may write to the vars file. Returns
// the writable paths.
func stageFirmware(t *testing.T, codeIn, varsIn, scratch string) (string, string) {
	t.Helper()
	codeOut := filepath.Join(scratch, "edk2-riscv-code.fd")
	varsOut := filepath.Join(scratch, "edk2-riscv-vars.fd")
	for _, c := range [][2]string{{codeIn, codeOut}, {varsIn, varsOut}} {
		src, err := os.Open(c[0])
		if err != nil {
			t.Fatalf("open firmware %s: %v", c[0], err)
		}
		dst, err := os.Create(c[1])
		if err != nil {
			src.Close()
			t.Fatalf("create firmware %s: %v", c[1], err)
		}
		if _, err := io.Copy(dst, src); err != nil {
			src.Close()
			dst.Close()
			t.Fatalf("copy firmware: %v", err)
		}
		src.Close()
		dst.Close()
	}
	return codeOut, varsOut
}

// TestRiscv64EDK2BootReachesDone asserts the runtime marker arrives.
// On a buggy firmware this test fails with the captured tail of QEMU
// output; on a working firmware it completes in well under
// bootTimeoutSec.
func TestRiscv64EDK2BootReachesDone(t *testing.T) {
	qemu, err := exec.LookPath("qemu-system-riscv64")
	if err != nil {
		t.Skipf("qemu-system-riscv64 not on PATH: %v", err)
	}

	efi := envOr("RISCV64_EFI", filepath.Join(thisFileDir(t), defaultEFIRel))
	code := envOr("RISCV64_OVMF_CODE", defaultOVMFCode)
	vars := envOr("RISCV64_OVMF_VARS", defaultOVMFVars)

	for _, p := range [][2]string{
		{"RISCV64_EFI", efi},
		{"RISCV64_OVMF_CODE", code},
		{"RISCV64_OVMF_VARS", vars},
	} {
		if !readable(p[1]) {
			t.Skipf("%s not readable at %q (set $%s to override)", p[0], p[1], p[0])
		}
	}

	scratch := t.TempDir()
	espRoot := filepath.Join(scratch, "esp-riscv64")
	stageESP(t, efi, espRoot)
	codeFd, varsFd := stageFirmware(t, code, vars, scratch)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeoutSec*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, qemu,
		"-machine", "virt",
		"-m", "4096",
		"-nographic",
		"-serial", "mon:stdio",
		"-drive", fmt.Sprintf("if=pflash,format=raw,unit=0,file=%s", codeFd),
		"-drive", fmt.Sprintf("if=pflash,format=raw,unit=1,file=%s", varsFd),
		"-drive", fmt.Sprintf("file=fat:rw:%s,format=raw,if=none,id=esp", espRoot),
		"-device", "virtio-blk-device,drive=esp",
	)

	// Capture stdout (serial). Stderr is QEMU diagnostics.
	var out, errBuf bytes.Buffer
	cmd.Stdout = &capWriter{buf: &out, max: maxOutputBytes}
	cmd.Stderr = &capWriter{buf: &errBuf, max: maxOutputBytes}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start qemu: %v", err)
	}

	// Goroutine: poll stdout for the DONE marker and kill QEMU as
	// soon as we see it (so the test finishes promptly).
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		deadline := time.Now().Add(bootTimeoutSec * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(out.String(), doneMarker) {
				_ = cmd.Process.Kill()
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		_ = cmd.Process.Kill()
	}()

	waitErr := cmd.Wait()
	<-doneCh

	stdoutStr := out.String()
	stderrStr := errBuf.String()

	// Expected: at least the hello marker appears, then DONE.
	helloOK := strings.Contains(stdoutStr, helloMarkerPart)
	doneOK := strings.Contains(stdoutStr, doneMarker)
	faultObserved := strings.Contains(stdoutStr, "RISCV64 Exception") ||
		strings.Contains(stdoutStr, "EXCEPT_RISCV_")

	if doneOK {
		// Killed-by-signal is fine when we killed QEMU on success.
		return
	}

	// Build a useful failure message bounded in size.
	tail := lastN(stdoutStr, 4*1024)
	errTail := lastN(stderrStr, 1024)
	switch {
	case faultObserved:
		t.Fatalf("EDK2 RISC-V image-protection regression: firmware faulted before runtime reached %q.\n--- tail of QEMU serial ---\n%s\n--- tail of QEMU stderr ---\n%s\n--- qemu wait error ---\n%v",
			doneMarker, tail, errTail, waitErr)
	case !helloMarkerPart_in(stdoutStr):
		t.Fatalf("runtime never printed hello marker; suspect the image never reached cpuinit. waitErr=%v\n--- tail of QEMU serial ---\n%s\n--- tail of QEMU stderr ---\n%s",
			waitErr, tail, errTail)
	case helloOK && !doneOK:
		t.Fatalf("runtime printed hello but never reached %q within %ds; suspect a regression past cpuinit. waitErr=%v\n--- tail of QEMU serial ---\n%s",
			doneMarker, bootTimeoutSec, waitErr, tail)
	default:
		t.Fatalf("runtime did not reach %q within %ds. waitErr=%v\n--- tail of QEMU serial ---\n%s\n--- tail of QEMU stderr ---\n%s",
			doneMarker, bootTimeoutSec, waitErr, tail, errTail)
	}
}

// helloMarkerPart_in is a tiny indirection so the failure switch
// above stays readable.
func helloMarkerPart_in(s string) bool {
	return strings.Contains(s, helloMarkerPart)
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// capWriter bounds the buffer's growth to `max` bytes to keep failing
// tests from holding hundreds of MiB of QEMU log in RAM.
type capWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.buf.Len() >= w.max {
		return len(p), nil // silently discard once cap reached
	}
	room := w.max - w.buf.Len()
	if room < len(p) {
		w.buf.Write(p[:room])
		return len(p), nil
	}
	return w.buf.Write(p)
}

// errIsKilled is exported only for documentation: a signal-induced
// exit on success is not a test failure. We do not actually inspect
// the error type because we treat the presence of `DONE` as the only
// success signal, and absence as the only failure signal.
var errIsKilled = errors.New("qemu killed after DONE marker observed")
