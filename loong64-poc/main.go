package main

import (
	"unsafe"

	"github.com/usbarmory/tamago/loong64"
)

// QEMU loongarch 'virt' low RAM (above the reserved boot_info/fdt at 0..2MB).
//go:linkname ramStart runtime/goos.RamStart
var ramStart uint = 0x2000000

//go:linkname ramSize runtime/goos.RamSize
var ramSize uint = 0xe000000

//go:linkname ramStackOffset runtime/goos.RamStackOffset
var ramStackOffset uint = 0x10000

// ns16550a UART on QEMU loongarch 'virt'.
const uartTHR = 0x1fe001e0

//go:linkname printk runtime/goos.Printk
func printk(c byte) {
	*(*byte)(unsafe.Pointer(uintptr(uartTHR))) = c
}

var nsPerTick int64 = 10 // 100 MHz fallback

//go:linkname hwinit1 runtime/goos.Hwinit1
func hwinit1() {
	if f := loong64.CounterFreq(); f > 0 {
		if p := int64(1_000_000_000) / f; p > 0 {
			nsPerTick = p
		}
	}
}

//go:linkname nanotime runtime/goos.Nanotime
func nanotime() int64 { return loong64.Counter() * nsPerTick }

// Timer-seeded splitmix64 PRNG (not crypto-grade: no hardware entropy on QEMU
// virt — a real port would use a HW RNG / virtio-rng).
var rngState uint64

//go:linkname initRNG runtime/goos.InitRNG
func initRNG() { rngState = uint64(loong64.Counter()) ^ 0x9e3779b97f4a7c15 }

func rngNext() uint64 {
	rngState += 0x9e3779b97f4a7c15
	z := rngState
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

//go:linkname getRandomData runtime/goos.GetRandomData
func getRandomData(b []byte) {
	for i := 0; i < len(b); {
		r := rngNext()
		for j := 0; j < 8 && i < len(b); j++ {
			b[i] = byte(r)
			r >>= 8
			i++
		}
	}
}

func worker(id int, out chan<- int) {
	sum := 0
	for i := 1; i <= id*100; i++ {
		sum += i
	}
	out <- sum
}

func main() {
	println("tamago/loong64: runtime up")

	// real monotonic time (stable-timer counter)
	t0 := nanotime()
	x := 0
	for i := 0; i < 5000000; i++ {
		x += i
	}
	dtus := (nanotime() - t0) / 1000
	println("monotonic clock: busy-loop took ~", dtus, "us (>0 => timer advances)")

	// RNG-seeded map + two distinct random samples
	m := make(map[int]int)
	for i := 0; i < 6; i++ {
		m[i] = i * i
	}
	println("map[5]:", m[5])
	r1 := int(rngNext() & 0xffff)
	r2 := int(rngNext() & 0xffff)
	println("PRNG samples (should differ):", r1, r2)

	// goroutines + channel
	out := make(chan int)
	for w := 1; w <= 4; w++ {
		go worker(w, out)
	}
	total := 0
	for w := 0; w < 4; w++ {
		total += <-out
	}
	println("goroutine workers total:", total)

	println("tamago/loong64: DONE")

	// deliberately fault to verify the exception handler catches it (instead of
	// the old silent jump-to-0). Reading a high unmapped address raises an
	// exception, which must vector to exceptionEntry and print "EXC".
	println("triggering a test fault (expect EXC)...")
	bad := (*int)(unsafe.Pointer(uintptr(0xffffffff00000000)))
	_ = *bad
	println("BUG: reached past the fault")
	for {
	}
}
