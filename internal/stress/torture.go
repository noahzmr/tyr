package stress

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/noahzmr/tyr/internal/affinity"
	"github.com/noahzmr/tyr/internal/workload"
)

// EngTorture is Tyr's Prime95-style torture test: Lucas-Lehmer iterations
// with floating-point FFTs, every iteration's round-off and every residue
// checked (see workload.RunLL).
const EngTorture = "torture"

// FFT modes, as in Prime95.
var FFTModes = []string{"blend", "small", "large"}

func fftMode(cfg Config) string {
	for _, m := range FFTModes {
		if cfg.FFT == m {
			return m
		}
	}
	return "blend"
}

func fftRange(cases []workload.LLCase) (lo, hi int) {
	for i, c := range cases {
		n := c.FFTSize()
		if i == 0 || n < lo {
			lo = n
		}
		hi = max(hi, n)
	}
	return lo, hi
}

func kOrM(n int) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%dM", n>>20)
	}
	return fmt.Sprintf("%dK", n>>10)
}

type cpuTorture struct{}

func (cpuTorture) Plan(cfg Config, env *Env) *Plan {
	p := cpuPlan(cfg, env, EngTorture)
	if p.Blocked != "" {
		return p
	}
	mode := fftMode(cfg)
	cases := workload.TortureCases(mode)
	lo, hi := fftRange(cases)
	what := map[string]string{"small": "in the CPU caches: the most heat and power", "large": "megabytes per CPU: caches, memory controller and DIMMs",
		"blend": "small and large in turn: cores, caches and memory"}[mode]
	p.Scope = append(p.Scope, fmt.Sprintf("%s FFTs %s–%s, %s", mode, kOrM(lo), kOrM(hi), what))
	cpus, _ := cpuScope(cfg, env)
	if mode != "small" {
		mib := hi * 16 >> 20
		p.Impact = append(p.Impact, fmt.Sprintf("Large FFTs use about %d MiB per CPU (%d MiB for %d CPUs).", mib, mib*len(cpus), len(cpus)))
	}
	p.Info = []string{
		"Like Prime95's torture test: Lucas-Lehmer iterations on Mersenne numbers of 4,000 to 7 million bits, squared with floating-point FFTs. " +
			"Every iteration must round to whole numbers (round-off below 0.40) and every run must end at its known residue - a core that computes wrong is named.",
		"Tyr's FFT is portable Go without hand-written AVX code: it runs cooler than Prime95 / mprime. For the most heat install mprime (engine mprime).",
	}
	return p
}

func (cpuTorture) Run(ctx context.Context, cfg Config, _ *Plan, rt Runtime) error {
	cpus, _ := cpuScope(cfg, runEnv(ctx))
	cases := workload.TortureCases(fftMode(cfg))
	var iters atomic.Uint64
	var wg sync.WaitGroup
	var errs atomic.Int32
	for wi, c := range cpus {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Runs on its own locked thread; never unlocked (see affinity.Pin).
			if err := affinity.Pin(c); err != nil {
				rt.Logf("cpu%d: %v (skipped)", c, err)
				return
			}
			tick := func(uint) bool {
				iters.Add(1)
				return ctx.Err() == nil
			}
			for i := wi; ctx.Err() == nil; i++ {
				lc := cases[i%len(cases)]
				err := workload.RunLL(lc, tick)
				var le *workload.LLError
				if errors.As(err, &le) && errs.Add(1) <= 10 {
					v := VerifyError{Subject: fmt.Sprintf("cpu%d", c), Workload: fmt.Sprintf("Lucas-Lehmer M%d, FFT %s", lc.P, kOrM(lc.FFTSize())), Detail: le.Error()}
					if le.Roundoff == 0 {
						v.Expected, v.Got = fmt.Sprintf("%#016x", lc.Residue), fmt.Sprintf("%#016x", le.Got)
					}
					rt.VerifyError(v)
				}
			}
		}()
	}
	lo, hi := fftRange(cases)
	rt.Progress(0, fmt.Sprintf("%s FFTs %s–%s on %d CPUs", fftMode(cfg), kOrM(lo), kOrM(hi), len(cpus)))
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last, lastAt := uint64(0), time.Now()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case now := <-t.C:
			n := iters.Load()
			rt.Metric("fft_iterations_per_s", "ops_per_second", float64(n-last)/now.Sub(lastAt).Seconds())
			last, lastAt = n, now
		}
	}
}
