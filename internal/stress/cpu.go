package stress

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/noahzmr/tyr/internal/affinity"
	"github.com/noahzmr/tyr/internal/cpuset"
	"github.com/noahzmr/tyr/internal/workload"
)

// cpuScope resolves the CPUs a CPU test loads: cfg.CPUs (or all online
// CPUs) intersected with the process affinity mask.
func cpuScope(cfg Config, env *Env) ([]int, []string) {
	var warn []string
	var want []int
	if c := env.Baseline.CPU; c != nil {
		for _, l := range c.Logical {
			if l.Online {
				want = append(want, l.ID)
			}
		}
	}
	if cfg.CPUs != "" {
		set, err := cpuset.Parse(cfg.CPUs)
		if err != nil {
			return nil, []string{"invalid CPU list: " + err.Error()}
		}
		var keep []int
		for _, c := range want {
			if set.Contains(c) {
				keep = append(keep, c)
			}
		}
		want = keep
	}
	if allowed, err := affinity.Allowed(); err == nil && !env.Simulated() {
		var keep []int
		for _, c := range want {
			if allowed.Contains(c) {
				keep = append(keep, c)
			}
		}
		if len(keep) < len(want) {
			warn = append(warn, fmt.Sprintf("%d CPUs are outside this process's affinity mask (cgroup/taskset) and are skipped", len(want)-len(keep)))
		}
		want = keep
	}
	return want, warn
}

func cpuPlan(cfg Config, env *Env, engine string) *Plan {
	cpus, warn := cpuScope(cfg, env)
	p := &Plan{Warnings: warn}
	if len(cpus) == 0 {
		p.Blocked = "no CPUs to test"
		return p
	}
	p.Scope = []string{fmt.Sprintf("%d logical CPUs: %s", len(cpus), cpuset.Set(cpus).String())}
	p.Impact = []string{fmt.Sprintf("Loads every selected CPU to 100%% for %s; other work on this host slows down.", fmtDur(cfg.Duration)),
		"Power draw and temperatures rise to their maximum; the guard stops the test at critical temperature."}
	switch engine {
	case EngBuiltin:
		p.Info = append(p.Info, "Each core runs integer, floating-point, FMA, vector, SHA-256, CRC32C and matrix kernels; every result is compared with a precomputed golden value, so a core that computes wrong under load is named.")
	case EngMprime:
		p.Info = append(p.Info, "mprime runs the Prime95 torture test (large FFTs, maximum heat). It stops at the first wrong result and reports 'FATAL ERROR' / 'Hardware failure detected'.")
	case EngStressNG:
		p.Info = append(p.Info, "stress-ng cycles through all CPU stress methods with --verify (each method checks its own results).")
	}
	return p
}

// --- built-in -----------------------------------------------------------------

type cpuBuiltin struct{}

func (cpuBuiltin) Plan(cfg Config, env *Env) *Plan { return cpuPlan(cfg, env, EngBuiltin) }

// corruptCPU lets tests inject a wrong kernel result; nil in production.
var corruptCPU func(cpu int, kernel string, v uint64) uint64

func (cpuBuiltin) Run(ctx context.Context, cfg Config, _ *Plan, rt Runtime) error {
	cpus, _ := cpuScope(cfg, runEnv(ctx))
	var checks atomic.Uint64
	var wg sync.WaitGroup
	for _, c := range cpus {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Runs on its own locked thread; never unlocked (see affinity.Pin).
			if err := affinity.Pin(c); err != nil {
				rt.Logf("cpu%d: %v (skipped)", c, err)
				return
			}
			errs := 0
			for iter := 0; ctx.Err() == nil; iter++ {
				for _, k := range workload.Kernels {
					seed := uint64((c+iter)%workload.Seeds) + 1
					v := k.Run(seed)
					if corruptCPU != nil {
						v = corruptCPU(c, k.Name, v)
					}
					if err := workload.Check(k.Name, seed, v); err != nil {
						if errs < 10 {
							want, _ := workload.Expected(k.Name, seed)
							rt.VerifyError(VerifyError{Subject: fmt.Sprintf("cpu%d", c), Workload: k.Name,
								Expected: fmt.Sprintf("%#016x", want), Got: fmt.Sprintf("%#016x", v), Detail: fmt.Sprintf("seed %d, iteration %d", seed, iter)})
						}
						errs++
					}
					checks.Add(1)
					if ctx.Err() != nil {
						return
					}
				}
			}
		}()
	}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last, lastAt := uint64(0), time.Now()
	rt.Progress(0, fmt.Sprintf("kernel rotation on %d CPUs", len(cpus)))
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case now := <-t.C:
			n := checks.Load()
			rt.Metric("verified_kernels_per_s", "ops_per_second", float64(n-last)/now.Sub(lastAt).Seconds())
			last, lastAt = n, now
		}
	}
}

// --- stress-ng ----------------------------------------------------------------

type cpuStressNG struct{}

func (cpuStressNG) Plan(cfg Config, env *Env) *Plan { return cpuPlan(cfg, env, EngStressNG) }

func (cpuStressNG) Run(ctx context.Context, cfg Config, _ *Plan, rt Runtime) error {
	cpus, _ := cpuScope(cfg, runEnv(ctx))
	rt.Progress(0, "stress-ng --cpu all methods")
	return stressNG(ctx, cfg, rt, "cpu", "--cpu", strconv.Itoa(len(cpus)), "--cpu-method", "all", "--taskset", cpuset.Set(cpus).String())
}

// --- mprime (Prime95) ---------------------------------------------------------

type cpuMprime struct{}

func (cpuMprime) Plan(cfg Config, env *Env) *Plan {
	p := cpuPlan(cfg, env, EngMprime)
	if cfg.CPUs != "" {
		p.Warnings = append(p.Warnings, "mprime picks its own CPUs: the CPU selection only sets the number of torture threads")
	}
	return p
}

// mprimeFail matches the torture test's failure lines.
func mprimeFail(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "fatal error") || strings.Contains(l, "hardware failure") || strings.Contains(l, "possible hardware errors") ||
		(strings.Contains(l, "error") && strings.Contains(l, "rounding"))
}

func (cpuMprime) Run(ctx context.Context, cfg Config, _ *Plan, rt Runtime) error {
	cpus, _ := cpuScope(cfg, runEnv(ctx))
	dir, err := os.MkdirTemp("", "tyr-mprime-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// No PrimeNet, torture test only, one thread per selected CPU.
	_ = os.WriteFile(filepath.Join(dir, "prime.txt"), []byte("V24OptionsConverted=1\nV30OptionsConverted=1\nStressTester=1\nUsePrimenet=0\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "local.txt"), []byte(fmt.Sprintf("TortureThreads=%d\nTortureType=4\n", len(cpus))), 0o600)
	rt.Progress(0, fmt.Sprintf("Prime95 torture test, %d threads", len(cpus)))
	var passed atomic.Int64
	err = runExternalIn(ctx, rt, dir, EngMprime, []string{"-t", "-W" + dir}, func(_, line string) {
		switch {
		case mprimeFail(line):
			rt.VerifyError(VerifyError{Subject: "cpu", Workload: "mprime", Detail: strings.TrimSpace(line)})
		case strings.Contains(strings.ToLower(line), "passed"):
			rt.Metric("mprime_self_tests_passed", "count", float64(passed.Add(1)))
		}
	})
	if err != nil {
		return fmt.Errorf("mprime: %w", err)
	}
	return nil
}

// --- context plumbing -----------------------------------------------------------

type envKey struct{}

// withEnv makes the env available to Run (tests re-derive their scope).
func withEnv(ctx context.Context, env *Env) context.Context {
	return context.WithValue(ctx, envKey{}, env)
}

func runEnv(ctx context.Context) *Env {
	if e, ok := ctx.Value(envKey{}).(*Env); ok {
		return e
	}
	return &Env{}
}
