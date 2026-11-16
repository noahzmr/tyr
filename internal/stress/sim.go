package stress

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// simTest stands in for every test in --fixtures mode: it puts no load on
// the machine but emits realistic metrics, optionally one verify error, so
// the whole flow (plan, live view, guard, verdict, result file) can be
// demonstrated and tested.
type simTest struct{}

func (simTest) Plan(cfg Config, env *Env) *Plan {
	if cfg.Engine == EngOfflineCore {
		return offlineCorePlan(cfg, env, true)
	}
	p := &Plan{Scope: []string{fmt.Sprintf("simulated %s test (fixtures mode, no real load)", cfg.Component)},
		Impact: []string{"Nothing: fixtures mode only replays recorded data."}}
	if cfg.Component == CompMemory {
		p.Info = append(p.Info, memtest86Info(env))
	}
	if cfg.Component == CompNetwork && cfg.Target == "" {
		p.Scope = append(p.Scope, "(a real run needs `tyr net-peer` on another host)")
	}
	return p
}

func (simTest) Run(ctx context.Context, cfg Config, _ *Plan, rt Runtime) error {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	start := time.Now()
	injected := false
	m := map[Component]struct {
		name, unit string
		scale      float64
	}{
		CompCPU:     {"verified_kernels_per_s", "ops_per_second", 52000},
		CompMemory:  {"verified_bytes_per_s", "bytes_per_second", 9.5e9},
		CompStorage: {"read_bytes_per_s_sda", "bytes_per_second", 2.1e8},
		CompNetwork: {"net_test_bits_per_s", "bits_per_second", 9.4e9},
	}[cfg.Component]
	name, unit, scale := m.name, m.unit, m.scale
	stages := map[Component][]string{
		CompCPU:     {"kernel rotation: int, fp, fma", "kernel rotation: vector, sha256", "kernel rotation: crc32c, matmul"},
		CompMemory:  {"pass 1: random", "pass 2: walking ones", "pass 3: moving inversions", "pass 4: address in address"},
		CompStorage: {"sda pass 1", "sda pass 1", "sda pass 2"},
		CompNetwork: {"4 streams to peer"},
	}[cfg.Component]
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			el := time.Since(start).Seconds()
			f := min(1, el/cfg.Duration.Seconds())
			rt.Progress(f, stages[int(f*float64(len(stages)))%len(stages)])
			v := scale * (0.92 + 0.08*math.Sin(float64(i)/5))
			rt.Metric(name, unit, v)
			if cfg.Component == CompNetwork {
				rt.Metric("net_link_ratio", "percent", v/1e10*100)
			}
			if cfg.SimError && !injected && f > 0.5 {
				injected = true
				e := map[Component]VerifyError{
					CompCPU:     {Subject: "cpu12", Workload: "fma", Expected: "0x3fd5c28f5c28f5c3", Got: "0x3fd5c28f5c28f5c7"},
					CompMemory:  {Subject: "0x7f3a2f40a18", Workload: "moving inversions", Expected: "0x5555555555555555", Got: "0x5555555555555455"},
					CompStorage: {Subject: "/dev/sda @ 1.2 TiB", Workload: "surface read", Detail: "input/output error"},
					CompNetwork: {Subject: "stream 2", Workload: "tcp payload", Detail: "1 corrupted frames (CRC32C)"},
				}[cfg.Component]
				e.Detail = strings.TrimSpace(e.Detail + " (simulated, --sim-error)")
				rt.VerifyError(e)
			}
		}
	}
}
