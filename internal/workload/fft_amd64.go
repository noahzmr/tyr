package workload

import "golang.org/x/sys/cpu"

// The vector kernels (fft_amd64.s): AVX-512F processes 8 butterflies per
// instruction, AVX2 with FMA 4.
func init() { useKernels(true) }

//go:noescape
func ditAVX512(ur, ui, vr, vi, wr, wi *float64, n int)

//go:noescape
func difAVX512(ur, ui, vr, vi, wr, wi *float64, n int)

//go:noescape
func ditAVX2(ur, ui, vr, vi, wr, wi *float64, n int)

//go:noescape
func difAVX2(ur, ui, vr, vi, wr, wi *float64, n int)

func wrap(f func(ur, ui, vr, vi, wr, wi *float64, n int)) butterflyFunc {
	return func(ur, ui, vr, vi, wr, wi []float64) { f(&ur[0], &ui[0], &vr[0], &vi[0], &wr[0], &wi[0], len(ur)) }
}

// useKernels picks the widest kernels the CPU has; width forces one (0:
// the best, 4: AVX2, 1: plain Go) - tests compare all of them.
func useKernels(on bool) { useWidth(map[bool]int{true: 0, false: 1}[on]) }

func useWidth(width int) {
	ditKernel, difKernel, kernelWidth, KernelName = nil, nil, 0, "Go"
	ditNarrow, difNarrow, narrowWidth = nil, nil, 0
	switch {
	case width == 1:
	case cpu.X86.HasAVX512F && width != 4:
		ditKernel, difKernel, kernelWidth, KernelName = wrap(ditAVX512), wrap(difAVX512), 8, "AVX-512 FMA"
		if cpu.X86.HasAVX2 && cpu.X86.HasFMA {
			ditNarrow, difNarrow, narrowWidth = wrap(ditAVX2), wrap(difAVX2), 4
		}
	case cpu.X86.HasAVX2 && cpu.X86.HasFMA:
		ditKernel, difKernel, kernelWidth, KernelName = wrap(ditAVX2), wrap(difAVX2), 4, "AVX2 FMA"
	}
}
