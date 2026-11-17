package workload

import (
	"math"
	"sync"
)

// The FFT of the torture test, laid out for vector units: real and
// imaginary parts in separate arrays, the twiddles of each stage in one
// contiguous row, so a stage is a run of identical butterflies over
// consecutive elements - 8 at a time with AVX-512, 4 with AVX2 (FMA), one
// in plain Go. All paths compute the same transform; the torture test's
// residues are exact integers, so they match bit for bit.

// fftPlan holds the per-stage twiddles of one size, shared read-only by
// every worker. There is no bit reversal: the forward transform is
// decimation in frequency (natural order in, bit-reversed out), the inverse
// decimation in time (bit-reversed in, natural out), and squaring in
// between does not care about the order - as in Prime95.
type fftPlan struct {
	n int
	// stage s (block size 2^(s+1)) uses w_k = e^(−2πik/size), k < size/2
	wr, wi, wiInv [][]float64
}

var plans sync.Map // int → *fftPlan

func planFor(n int) *fftPlan {
	if p, ok := plans.Load(n); ok {
		return p.(*fftPlan)
	}
	p := &fftPlan{n: n}
	for size := 2; size <= n; size <<= 1 {
		half := size / 2
		wr, wi, wv := make([]float64, half), make([]float64, half), make([]float64, half)
		for k := range half {
			s, c := math.Sincos(-2 * math.Pi * float64(k) / float64(size))
			wr[k], wi[k], wv[k] = c, s, -s
		}
		p.wr, p.wi, p.wiInv = append(p.wr, wr), append(p.wi, wi), append(p.wiInv, wv)
	}
	v, _ := plans.LoadOrStore(n, p)
	return v.(*fftPlan)
}

// butterflyFunc runs half butterflies of one block, u = re/im[0:half],
// v = re/im[half:2·half]. The vector kernels need half to be a multiple of
// their width.
type butterflyFunc func(ur, ui, vr, vi, wr, wi []float64)

var (
	ditKernel, difKernel butterflyFunc // nil: plain Go only
	kernelWidth          int
	// narrower kernels for stages the wide ones cannot take (AVX2 next to
	// AVX-512 for blocks of 4)
	ditNarrow, difNarrow butterflyFunc
	narrowWidth          int
	// KernelName names the FFT code in use ("AVX-512 FMA", "AVX2 FMA", "Go").
	KernelName = "Go"
)

// DIT: (u, v) → (u + w·v, u − w·v)
func ditGo(ur, ui, vr, vi, wr, wi []float64) {
	for k := range ur {
		tr := vr[k]*wr[k] - vi[k]*wi[k]
		ti := vr[k]*wi[k] + vi[k]*wr[k]
		ur[k], vr[k] = ur[k]+tr, ur[k]-tr
		ui[k], vi[k] = ui[k]+ti, ui[k]-ti
	}
}

// DIF: (u, v) → (u + v, (u − v)·w)
func difGo(ur, ui, vr, vi, wr, wi []float64) {
	for k := range ur {
		dr, di := ur[k]-vr[k], ui[k]-vi[k]
		ur[k], ui[k] = ur[k]+vr[k], ui[k]+vi[k]
		vr[k], vi[k] = dr*wr[k]-di*wi[k], dr*wi[k]+di*wr[k]
	}
}

// stage runs one stage over all blocks: through the vector kernel when the
// blocks are wide enough, else in plain Go - narrow stages (the first and
// last few) in one loop over all blocks, without a call per block.
func stage(re, im, wr, wi []float64, half int, dif bool) {
	n := len(re)
	vec, plain := ditKernel, ditGo
	if dif {
		vec, plain = difKernel, difGo
	}
	if vec != nil && half%kernelWidth != 0 {
		vec = ditNarrow
		if dif {
			vec = difNarrow
		}
		if vec == nil || half%narrowWidth != 0 {
			vec = nil
		}
	}
	if vec != nil {
		for start := 0; start < n; start += 2 * half {
			vec(re[start:start+half], im[start:start+half], re[start+half:start+2*half], im[start+half:start+2*half], wr, wi)
		}
		return
	}
	if half >= 16 {
		for start := 0; start < n; start += 2 * half {
			plain(re[start:start+half], im[start:start+half], re[start+half:start+2*half], im[start+half:start+2*half], wr, wi)
		}
		return
	}
	for start := 0; start < n; start += 2 * half {
		for k := 0; k < half; k++ {
			u, v := start+k, start+k+half
			if dif {
				dr, di := re[u]-re[v], im[u]-im[v]
				re[u], im[u] = re[u]+re[v], im[u]+im[v]
				re[v], im[v] = dr*wr[k]-di*wi[k], dr*wi[k]+di*wr[k]
			} else {
				tr := re[v]*wr[k] - im[v]*wi[k]
				ti := re[v]*wi[k] + im[v]*wr[k]
				re[u], re[v] = re[u]+tr, re[u]-tr
				im[u], im[v] = im[u]+ti, im[u]-ti
			}
		}
	}
}

// fft transforms (re, im) in place. Forward: natural order in, bit-reversed
// out; inverse (conjugate twiddles, unscaled): bit-reversed in, natural out.
func (p *fftPlan) fft(re, im []float64, inverse bool) {
	stages := len(p.wr)
	if !inverse {
		for s := stages - 1; s >= 2; s-- {
			stage(re, im, p.wr[s], p.wi[s], 1<<s, true)
		}
		radix4DIF(re, im)
		return
	}
	radix4DIT(re, im)
	for s := 2; s < stages; s++ {
		stage(re, im, p.wr[s], p.wiInv[s], 1<<s, false)
	}
}

// radix4DIF is the last two forward stages (blocks of 4 and 2) fused:
// their twiddles are 1 and −i, so there is no multiplication at all.
func radix4DIF(re, im []float64) {
	for g := 0; g+4 <= len(re); g += 4 {
		r, i := re[g:g+4:g+4], im[g:g+4:g+4]
		a0r, a0i := r[0]+r[2], i[0]+i[2]
		a2r, a2i := r[0]-r[2], i[0]-i[2]
		a1r, a1i := r[1]+r[3], i[1]+i[3]
		a3r, a3i := i[1]-i[3], -(r[1] - r[3]) // (x1 − x3)·(−i)
		r[0], i[0] = a0r+a1r, a0i+a1i
		r[1], i[1] = a0r-a1r, a0i-a1i
		r[2], i[2] = a2r+a3r, a2i+a3i
		r[3], i[3] = a2r-a3r, a2i-a3i
	}
}

// radix4DIT is the first two inverse stages (blocks of 2 and 4) fused;
// twiddles 1 and +i.
func radix4DIT(re, im []float64) {
	for g := 0; g+4 <= len(re); g += 4 {
		r, i := re[g:g+4:g+4], im[g:g+4:g+4]
		c0r, c0i := r[0]+r[1], i[0]+i[1]
		c1r, c1i := r[0]-r[1], i[0]-i[1]
		c2r, c2i := r[2]+r[3], i[2]+i[3]
		tr, ti := -(i[2] - i[3]), r[2]-r[3] // (x2 − x3)·(+i)
		r[0], i[0] = c0r+c2r, c0i+c2i
		r[2], i[2] = c0r-c2r, c0i-c2i
		r[1], i[1] = c1r+tr, c1i+ti
		r[3], i[3] = c1r-tr, c1i-ti
	}
}

// square squares every element: (a + bi)² = a² − b² + 2abi.
func square(re, im []float64) {
	for i := range re {
		a, b := re[i], im[i]
		re[i], im[i] = a*a-b*b, 2*a*b
	}
}
