package workload

import (
	"runtime"
	"testing"

	"golang.org/x/sys/cpu"
)

// Every FFT path gives the exact Lucas-Lehmer residues: the vector kernels
// round differently, but the results are whole numbers.
func TestKernelsExact(t *testing.T) {
	type path struct {
		name string
		set  func()
	}
	paths := []path{{"Go", func() { useKernels(false) }}}
	if runtime.GOARCH == "amd64" && cpu.X86.HasAVX2 && cpu.X86.HasFMA {
		paths = append(paths, path{"AVX2", func() { useWidth(4) }})
	}
	if runtime.GOARCH == "amd64" && cpu.X86.HasAVX512F {
		paths = append(paths, path{"AVX-512", func() { useKernels(true) }})
	}
	defer useKernels(true)
	for _, p := range paths {
		p.set()
		for _, c := range []LLCase{{P: 4253, Iters: 4251}, {P: 21701, Iters: 21699}} {
			if err := RunLL(c, nil); err != nil {
				t.Errorf("%s: %v", p.name, err)
			}
		}
		for _, c := range []struct{ p, iters uint }{{9941, 300}, {1257787, 3}} {
			s := newLL(c.p)
			s.set(4)
			worst := 0.0
			for range c.iters {
				worst = max(worst, s.step())
			}
			if got, want := s.residue(), exactLL(c.p, c.iters); got != want || worst > 0.1 {
				t.Errorf("%s M%d: %#x want %#x, round-off %.4f", p.name, c.p, got, want, worst)
			}
		}
		t.Logf("%s: ok (%s)", p.name, KernelName)
	}
}
