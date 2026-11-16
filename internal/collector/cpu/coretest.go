package cpu

import (
	"context"
	"fmt"
	"time"

	"github.com/noahzmr/tyr/internal/affinity"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/workload"
)

// CoreResult is the quick core test outcome for one logical CPU.
type CoreResult struct {
	State  model.CoreTestState
	Detail string
}

// Corrupt lets tests inject a wrong kernel result; nil in production.
type Corrupt func(cpu int, kernel string, v uint64) uint64

// RunCoreTest pins one worker to each CPU in cpus and runs every workload
// kernel once with a CPU-dependent seed, checking each result against the
// golden table. CPUs outside allowed are skipped. Workers that do not finish
// before timeout are reported as TIMEOUT; they stop at their next kernel
// boundary once ctx is cancelled.
func RunCoreTest(ctx context.Context, cpus []int, allowed func(int) bool, timeout time.Duration, corrupt Corrupt) map[int]CoreResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type res struct {
		cpu int
		r   CoreResult
	}
	out := make(map[int]CoreResult, len(cpus))
	ch := make(chan res, len(cpus))
	started := 0
	for _, c := range cpus {
		if allowed != nil && !allowed(c) {
			out[c] = CoreResult{State: model.CoreTestSkip, Detail: "not in process CPU affinity mask (cgroup/taskset)"}
			continue
		}
		started++
		go func() { ch <- res{c, testOne(ctx, c, corrupt)} }()
	}
	for i := 0; i < started; i++ {
		select {
		case r := <-ch:
			out[r.cpu] = r.r
		case <-ctx.Done():
			for _, c := range cpus {
				if _, ok := out[c]; !ok {
					out[c] = CoreResult{State: model.CoreTestTimeout, Detail: fmt.Sprintf("no result within %v", timeout)}
				}
			}
			return out
		}
	}
	return out
}

func testOne(ctx context.Context, cpu int, corrupt Corrupt) CoreResult {
	// Runs on its own locked thread; never unlocked, see affinity.Pin.
	if err := affinity.Pin(cpu); err != nil {
		return CoreResult{State: model.CoreTestSkip, Detail: err.Error()}
	}
	seed := uint64(cpu%workload.Seeds) + 1
	for _, k := range workload.Kernels {
		if ctx.Err() != nil {
			return CoreResult{State: model.CoreTestTimeout, Detail: "interrupted before " + k.Name}
		}
		v := k.Run(seed)
		if corrupt != nil {
			v = corrupt(cpu, k.Name, v)
		}
		if err := workload.Check(k.Name, seed, v); err != nil {
			return CoreResult{State: model.CoreTestFail, Detail: err.Error()}
		}
	}
	if cur, err := affinity.Current(); err == nil && cur != cpu {
		return CoreResult{State: model.CoreTestFail, Detail: fmt.Sprintf("worker migrated to cpu%d despite pinning", cur)}
	}
	return CoreResult{State: model.CoreTestPass}
}
