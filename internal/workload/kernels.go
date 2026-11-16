// Package workload contains deterministic compute kernels whose results are
// checked against an embedded golden table. They are used by the quick
// per-core test and by the CPU stress test. A kernel's result is never
// compared with a value computed on the same core: a faulty core could
// compute the same wrong value twice.
//
// Floating-point kernels are written so that results are bit-identical on
// amd64 and arm64: the Go spec allows the compiler to fuse x*y+z into an FMA
// on some architectures, so FMA is always explicit (math.FMA) and no other
// expression combines a multiply with an add, or only on exact integer values.
package workload

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"math/bits"
)

// Kernel is one verified workload. Run(seed) must be deterministic and take
// a few milliseconds on a current server core.
type Kernel struct {
	Name string
	Desc string
	Run  func(seed uint64) uint64
}

// Seeds is the number of seeds per kernel in the golden table (1..Seeds).
const Seeds = 8

// Kernels lists all kernels in rotation order.
var Kernels = []Kernel{
	{"int", "integer ALU: xorshift, multiply, rotate, divide", intMix},
	{"fp", "floating point: logistic map, sqrt, divide", fpChaos},
	{"fma", "fused multiply-add: Horner polynomial", fmaHorner},
	{"vector", "wide integer lanes (vectorizable loop)", vectorLanes},
	{"sha256", "SHA-256 hash chain (SHA extensions)", sha256Chain},
	{"crc32c", "CRC32C over a mutating buffer (CRC instructions)", crcChain},
	{"matmul", "64x64 matrix multiplication, exact integer-valued floats", matMul},
}

// ByName returns a kernel by name.
func ByName(name string) (Kernel, bool) {
	for _, k := range Kernels {
		if k.Name == name {
			return k, true
		}
	}
	return Kernel{}, false
}

// MismatchError reports a wrong kernel result.
type MismatchError struct {
	Kernel   string
	Seed     uint64
	Expected uint64
	Got      uint64
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("%s seed %d: expected %#016x, got %#016x", e.Kernel, e.Seed, e.Expected, e.Got)
}

// Expected returns the golden result for kernel name and seed (1..Seeds).
func Expected(name string, seed uint64) (uint64, bool) {
	row, ok := golden[name]
	if !ok || seed < 1 || seed > Seeds {
		return 0, false
	}
	return row[seed-1], true
}

// Check verifies a result against the golden table.
func Check(name string, seed, got uint64) error {
	want, ok := Expected(name, seed)
	if !ok {
		return fmt.Errorf("workload: no golden value for %s seed %d", name, seed)
	}
	if got != want {
		return &MismatchError{Kernel: name, Seed: seed, Expected: want, Got: got}
	}
	return nil
}

// --- kernels -------------------------------------------------------------

func splitmix(x *uint64) uint64 {
	*x += 0x9E3779B97F4A7C15
	z := *x
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func intMix(seed uint64) uint64 {
	x := seed*0x9E3779B97F4A7C15 | 1
	var acc uint64 = 0xcbf29ce484222325
	for i := 0; i < 400_000; i++ {
		x ^= x >> 12
		x ^= x << 25
		x ^= x >> 27
		v := x * 0x2545F4914F6CDD1D
		hi, lo := bits.Mul64(v, acc|1)
		acc = bits.RotateLeft64(acc^hi, i&63) + lo
		if i&15 == 0 {
			acc += v / (x>>32 | 1)
		}
	}
	return acc
}

func fpChaos(seed uint64) uint64 {
	x := 0.1 + float64(seed)/97.0
	r := 3.99991
	h := uint64(1469598103934665603)
	for i := 0; i < 300_000; i++ {
		rx := float64(r * x)
		x = float64(rx * float64(1-x))
		s := math.Sqrt(x)
		y := float64(s / float64(1+x))
		h = (h ^ math.Float64bits(y)) * 1099511628211
	}
	return h ^ math.Float64bits(x)
}

var hornerCoeffs = [...]float64{0.3, -1.7, 2.9, -0.11, 1.3, -2.2, 0.77, 1.9, -0.5, 0.123}

func fmaHorner(seed uint64) uint64 {
	x := float64(seed%7)/7.0 - 0.37
	h := uint64(1469598103934665603)
	for i := 0; i < 150_000; i++ {
		p := hornerCoeffs[0]
		for _, c := range hornerCoeffs[1:] {
			p = math.FMA(p, x, c)
		}
		x = p - math.Floor(p) - 0.5 // keep x in [-0.5, 0.5)
		h = (h ^ math.Float64bits(p)) * 1099511628211
	}
	return h
}

func vectorLanes(seed uint64) uint64 {
	const n = 4096
	var a, b, c [n]uint32
	s := seed
	for i := range a {
		r := splitmix(&s)
		a[i], b[i], c[i] = uint32(r), uint32(r>>32)|1, uint32(r>>16)
	}
	for round := 0; round < 200; round++ {
		for i := 0; i < n; i++ {
			a[i] = a[i]*b[i] + c[i]
		}
		for i := 0; i < n; i++ {
			c[i] ^= a[i] >> 7
		}
	}
	var acc uint64
	for i := 0; i < n; i++ {
		acc = acc*31 + uint64(a[i]^c[i])
	}
	return acc
}

func sha256Chain(seed uint64) uint64 {
	var buf [64]byte
	binary.LittleEndian.PutUint64(buf[:], seed)
	sum := sha256.Sum256(buf[:])
	for i := 0; i < 40_000; i++ {
		copy(buf[:32], sum[:])
		binary.LittleEndian.PutUint64(buf[32:], uint64(i))
		sum = sha256.Sum256(buf[:])
	}
	return binary.LittleEndian.Uint64(sum[:8])
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

func crcChain(seed uint64) uint64 {
	buf := make([]byte, 64<<10)
	s := seed
	for i := 0; i < len(buf); i += 8 {
		binary.LittleEndian.PutUint64(buf[i:], splitmix(&s))
	}
	var crc uint32
	var acc uint64
	for i := 0; i < 60; i++ {
		crc = crc32.Update(crc, castagnoli, buf)
		buf[(int(crc)%len(buf)+len(buf))%len(buf)] ^= byte(crc)
		acc = acc<<1 ^ uint64(crc)
	}
	return acc
}

func matMul(seed uint64) uint64 {
	const n = 64
	const mod = 1021
	a := make([]float64, n*n)
	b := make([]float64, n*n)
	c := make([]float64, n*n)
	s := seed
	for i := range a {
		a[i] = float64(splitmix(&s) % mod)
		b[i] = float64(splitmix(&s) % mod)
	}
	for round := 0; round < 6; round++ {
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				// Values < 1021, 64 products: sums < 2^27, exact in float64,
				// so fusion or summation order cannot change the result.
				var sum float64
				for k := 0; k < n; k++ {
					sum += a[i*n+k] * b[k*n+j]
				}
				c[i*n+j] = math.Mod(sum, mod)
			}
		}
		a, c = c, a
	}
	h := uint64(1469598103934665603)
	for _, v := range a {
		h = (h ^ uint64(v)) * 1099511628211
	}
	return h
}
