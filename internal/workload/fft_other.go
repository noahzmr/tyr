//go:build !amd64

package workload

// Other architectures run the butterflies in plain Go.

func useKernels(bool) { useWidth(1) }

func useWidth(int) { ditKernel, difKernel, kernelWidth, KernelName = nil, nil, 0, "Go" }

var _ = []any{ditNarrow, difNarrow, narrowWidth}
