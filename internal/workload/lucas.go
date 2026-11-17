package workload

import (
	"fmt"
	"math"
	"math/bits"
)

// A Prime95-style torture test: Lucas-Lehmer iterations s → s² − 2 mod
// 2^p − 1, the squaring done with floating-point FFTs on numbers of
// thousands to millions of bits. Two checks catch a CPU that computes
// wrong: every FFT result must be close to an integer (round-off below
// 0.4, as Prime95 checks), and the residue after a run must match a known
// value - 0 after p−2 iterations of a Mersenne prime, else a residue
// precomputed with exact integer arithmetic (llResidues, `make golden`).
//
// Small FFTs fit the CPU caches: the most heat. Large FFTs need megabytes
// per core: they load the caches, the memory controller and the DIMMs.

// LLCase is one test: p, how many iterations, the residue after them.
type LLCase struct {
	P       uint
	Iters   uint
	Residue uint64 // low 64 bits of s, 0 ≤ s < 2^p − 1
}

// Mersenne primes: the full test (p − 2 iterations) ends with s = 0.
var smallFFT = []uint{4253, 4423, 9689, 9941, 11213, 19937, 21701, 23209}

// large FFTs: a partial run of a large Mersenne prime, residue from the table.
var largeFFT = []LLCase{{P: 1257787, Iters: 300}, {P: 1398269, Iters: 300}, {P: 2976221, Iters: 120}, {P: 3021377, Iters: 120}, {P: 6972593, Iters: 50}}

// TortureCases lists the cases of a mode: "small", "large" or "blend".
func TortureCases(mode string) []LLCase {
	var small, large []LLCase
	for _, p := range smallFFT {
		small = append(small, LLCase{P: p, Iters: p - 2})
	}
	for _, c := range largeFFT {
		c.Residue = llResidues[c.P]
		large = append(large, c)
	}
	switch mode {
	case "small":
		return small
	case "large":
		return large
	}
	var out []LLCase // blend: alternate
	for i := 0; i < max(len(small), len(large)); i++ {
		if i < len(small) {
			out = append(out, small[i])
		}
		if i < len(large) {
			out = append(out, large[i])
		}
	}
	return out
}

// FFTSize is the FFT length the case uses.
func (c LLCase) FFTSize() int {
	d := (int(c.P) + 15) / 16
	n := 4 // the FFT's last two stages are one radix-4 pass
	for n < 2*d {
		n <<= 1
	}
	return n
}

// LLError is a failed torture case.
type LLError struct {
	Case      LLCase
	Iteration uint
	Roundoff  float64 // > 0.4: an FFT result far from an integer
	Got       uint64
}

func (e *LLError) Error() string {
	if e.Roundoff > 0 {
		return fmt.Sprintf("M%d iteration %d: round-off %.3f > 0.40 (FFT %d)", e.Case.P, e.Iteration, e.Roundoff, e.Case.FFTSize())
	}
	return fmt.Sprintf("M%d after %d iterations: residue %#016x, expected %#016x (FFT %d)", e.Case.P, e.Case.Iters, e.Got, e.Case.Residue, e.Case.FFTSize())
}

// MaxRoundoff is the round-off at which a result counts as wrong.
const MaxRoundoff = 0.40

// --- Lucas-Lehmer ---------------------------------------------------------------------

// llState squares numbers mod 2^p − 1 with balanced 16-bit digits.
type llState struct {
	p      uint
	d      int       // digits
	digit  []int64   // balanced, −2^15 ≤ x < 2^15 (top digit may be larger)
	re, im []float64 // the FFT buffer, split for vector units
	words  []uint64  // the product
	lo     []uint64  // the residue, below 2^p
	plan   *fftPlan
}

func newLL(p uint) *llState {
	d := (int(p) + 15) / 16
	n := LLCase{P: p}.FFTSize()
	return &llState{p: p, d: d, digit: make([]int64, d), re: make([]float64, n), im: make([]float64, n),
		words: make([]uint64, (2*d+2)/4+2), lo: make([]uint64, (int(p)+63)/64+1), plan: planFor(n)}
}

// set loads a small value.
func (s *llState) set(v uint64) {
	clear(s.digit)
	for i := 0; i < 4 && i < s.d; i++ {
		s.digit[i] = int64(v & 0xffff)
		v >>= 16
	}
	s.balance()
}

// balance moves digits into [−2^15, 2^15).
func (s *llState) balance() {
	var carry int64
	for i := 0; i < s.d; i++ {
		x := s.digit[i] + carry
		carry = 0
		if i < s.d-1 && x >= 1<<15 {
			x -= 1 << 16
			carry = 1
		}
		s.digit[i] = x
	}
}

// step computes s² − 2 mod 2^p − 1 and returns the largest round-off.
func (s *llState) step() float64 {
	re, im := s.re, s.im
	for i := range re {
		if i < s.d {
			re[i] = float64(s.digit[i])
		} else {
			re[i] = 0
		}
		im[i] = 0
	}
	s.plan.fft(re, im, false)
	square(re, im)
	s.plan.fft(re, im, true)
	scale := 1 / float64(len(re))
	clear(s.words)
	var worst float64
	var carry int64
	bit := 0
	// coefficients → base 2^16 digits (with carries) → 64-bit words
	for k := 0; k < 2*s.d+2; k++ {
		var c int64
		if k < len(re) {
			x := re[k] * scale
			r := math.Round(x)
			if e := math.Abs(x - r); e > worst {
				worst = e
			}
			c = int64(r)
		}
		t := c + carry
		digit := t & 0xffff
		carry = (t - digit) >> 16
		s.words[bit/64] |= uint64(digit) << (bit % 64)
		bit += 16
	}
	s.reduce()
	return worst
}

// reduce folds the product mod 2^p − 1, subtracts 2 and loads the
// digits. The product is below 2^(2p): lo + hi is below 2^(p+1), so one
// fold of the bit above p (2^p ≡ 1) brings it below 2^p.
func (s *llState) reduce() {
	p := int(s.p)
	w, lo := s.words, s.lo
	nw := (p + 63) / 64
	clear(lo)
	copy(lo[:nw], w[:nw])
	if r := p % 64; r != 0 {
		lo[nw-1] &= 1<<r - 1
	}
	sh := p % 64
	var carry uint64
	for i := 0; i < nw; i++ {
		src := p/64 + i
		var h uint64
		if src < len(w) {
			h = w[src] >> sh
			if sh != 0 && src+1 < len(w) {
				h |= w[src+1] << (64 - sh)
			}
		}
		lo[i], carry = bits.Add64(lo[i], h, carry)
	}
	lo[nw] = carry
	if lo[p/64]>>sh&1 == 1 { // ≥ 2^p: − 2^p + 1
		lo[p/64] &^= 1 << sh
		var c uint64 = 1
		for i := 0; i < len(lo) && c != 0; i++ {
			lo[i], c = bits.Add64(lo[i], 0, c)
		}
	}
	small := lo[0] < 2
	for i := 1; i < len(lo) && small; i++ {
		small = lo[i] == 0
	}
	if small { // 0 or 1: + (2^p − 1) − 2
		v := lo[0]
		for i := 0; i < nw; i++ {
			lo[i] = ^uint64(0)
		}
		if sh != 0 {
			lo[nw-1] = 1<<sh - 1
		}
		lo[0] -= 2 - v
	} else {
		var b uint64
		lo[0], b = bits.Sub64(lo[0], 2, 0)
		for i := 1; i < len(lo) && b != 0; i++ {
			lo[i], b = bits.Sub64(lo[i], 0, b)
		}
	}
	for i := 0; i < s.d; i++ {
		b := i * 16
		s.digit[i] = int64(lo[b/64] >> (b % 64) & 0xffff)
	}
	s.balance()
}

// residue is the low 64 bits of s in [0, 2^p − 1).
func (s *llState) residue() uint64 {
	p := int(s.p)
	all := true // s = 2^p − 1 is 0
	for i := 0; i < p/64; i++ {
		all = all && s.lo[i] == ^uint64(0)
	}
	if p%64 != 0 {
		all = all && s.lo[p/64] == 1<<(p%64)-1
	}
	if all {
		return 0
	}
	return s.lo[0]
}

// RunLL runs one case; tick is called every iteration (return false to
// stop early: then the result is not checked).
func RunLL(c LLCase, tick func(iter uint) bool) error {
	s := newLL(c.P)
	s.set(4)
	for i := uint(1); i <= c.Iters; i++ {
		if r := s.step(); r > MaxRoundoff {
			return &LLError{Case: c, Iteration: i, Roundoff: r}
		}
		if tick != nil && !tick(i) {
			return nil
		}
	}
	if got := s.residue(); got != c.Residue {
		return &LLError{Case: c, Got: got}
	}
	return nil
}
