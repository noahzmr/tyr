package stress

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/noahzmr/tyr/internal/buildinfo"
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
)

// Env is what tests and the engine need from the host.
type Env struct {
	Collector *collector.Env
	Cols      []collector.Collector
	// RunDir receives result files; "" = /var/lib/tyr/runs, falling back
	// to ./tyr-runs when that is not writable.
	RunDir string
	// Baseline is set by Start before Plan/Run are called.
	Baseline *model.Snapshot
	// Interval between monitor samples (default 1s).
	Interval time.Duration

	prepVM vmRaw // /proc/vmstat when Prepare started (background swap rate)
	prepAt time.Time
}

// Simulated reports whether Tyr runs on fixtures (no real load).
func (e *Env) Simulated() bool { return e.Collector != nil && e.Collector.Simulated }

// EventKind classifies run events.
type EventKind int

const (
	EvPhase EventKind = iota
	EvSample
	EvMetric
	EvProgress
	EvVerify
	EvGuard
	EvLog
	EvFinished
)

// Event is one thing that happened during a run.
type Event struct {
	Kind     EventKind
	Time     time.Time
	Phase    string
	Sample   *Sample
	Name     string
	Unit     string
	Value    float64
	Fraction float64
	Text     string
	Verify   *VerifyError
	Result   *Result
}

// Run is a started test.
type Run struct {
	ID     string
	Cfg    Config
	Plan   *Plan
	Start  time.Time
	events chan Event
	cancel context.CancelCauseFunc
	done   chan struct{}
	result *Result

	mu       sync.Mutex
	metrics  map[string]*MetricSummary
	samples  map[string][]sample // values over time, for the result's series
	verify   []VerifyError
	log      []string
	closers  []io.Closer
	abortBy  *Aborted
	progress float64
	stage    string
}

// Events delivers progress to the UI. The channel is closed after the
// EvFinished event. Samples and metrics are dropped (not blocked on) when
// the consumer falls behind.
func (r *Run) Events() <-chan Event { return r.events }

// Abort stops the run (user request).
func (r *Run) Abort(reason string) {
	r.mu.Lock()
	if r.abortBy == nil {
		r.abortBy = &Aborted{By: "user", Reason: reason}
	}
	r.mu.Unlock()
	r.cancel(errors.New(reason))
}

// Status returns progress (0..1) and the current stage.
func (r *Run) Status() (float64, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.progress, r.stage
}

// Wait blocks until the run finished and returns its result.
func (r *Run) Wait() *Result {
	<-r.done
	return r.result
}

func (r *Run) emit(e Event, must bool) {
	e.Time = time.Now()
	if must {
		r.events <- e
		return
	}
	select {
	case r.events <- e:
	default:
	}
}

// --- Runtime implementation ---------------------------------------------------

type runtime struct{ r *Run }

// Progress reports a stage; f < 0 means "use elapsed time".
func (rt runtime) Progress(f float64, stage string) {
	rt.r.mu.Lock()
	if f >= 0 {
		rt.r.progress = f
	}
	rt.r.stage = stage
	rt.r.mu.Unlock()
	rt.r.emit(Event{Kind: EvProgress, Fraction: f, Text: stage}, false)
}

func (rt runtime) Metric(name, unit string, v float64) {
	rt.r.mu.Lock()
	m := rt.r.metrics[name]
	if m == nil {
		m = &MetricSummary{Name: name, Unit: unit}
		rt.r.metrics[name] = m
	}
	m.add(v)
	rt.r.samples[name] = addSample(rt.r.samples[name], sample{t: time.Since(rt.r.Start).Seconds(), v: v})
	rt.r.mu.Unlock()
	rt.r.emit(Event{Kind: EvMetric, Name: name, Unit: unit, Value: v}, false)
}

func (rt runtime) VerifyError(e VerifyError) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	rt.r.mu.Lock()
	if len(rt.r.verify) < 1000 {
		rt.r.verify = append(rt.r.verify, e)
	}
	rt.r.mu.Unlock()
	rt.r.emit(Event{Kind: EvVerify, Verify: &e}, false)
}

func (rt runtime) Track(c io.Closer) {
	rt.r.mu.Lock()
	rt.r.closers = append(rt.r.closers, c)
	rt.r.mu.Unlock()
}

func (rt runtime) Logf(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	rt.r.mu.Lock()
	if len(rt.r.log) < 2000 {
		rt.r.log = append(rt.r.log, time.Now().Format("15:04:05 ")+s)
	}
	rt.r.mu.Unlock()
	rt.r.emit(Event{Kind: EvLog, Text: s}, false)
}

// --- planning -----------------------------------------------------------------

// Prepare takes the baseline snapshot and builds the plan. It has no side
// effects on the host besides reading sensors.
func Prepare(ctx context.Context, env *Env, cfg Config) (*Plan, Test, error) {
	if cfg.Duration <= 0 {
		cfg.Duration = 5 * time.Minute
	}
	t, err := testFor(cfg, env)
	if err != nil {
		return nil, nil, err
	}
	env.prepVM, env.prepAt = readVM(env), time.Now()
	if env.Baseline == nil {
		env.Baseline = collector.Collect(ctx, env.Collector, env.Cols, nil)
	}
	p := t.Plan(cfg, env)
	p.Component, p.Engine, p.Duration = cfg.Component, cfg.Engine, cfg.Duration
	if hot := critTemps(env.Baseline); len(hot) > 0 && p.Blocked == "" {
		p.Blocked = "already at critical temperature before the test: " + strings.Join(hot, ", ") +
			" (cool the system down, or set the right limit with --crit-temp if this sensor's default is too low)"
	}
	if w := busyWarnings(env); len(w) > 0 {
		p.Warnings = append(p.Warnings, w...)
	}
	return p, t, nil
}

// busyWarnings flags a busy host: load above the CPU count, running VMs.
func busyWarnings(env *Env) []string {
	var out []string
	f := env.Collector.FS
	if s, err := hostfs.String(f, "/proc/loadavg"); err == nil {
		fields := strings.Fields(s)
		if len(fields) > 0 {
			la, _ := strconv.ParseFloat(fields[0], 64)
			n := 1
			if c := env.Baseline.CPU; c != nil && c.ThreadCount > 0 {
				n = c.ThreadCount
			}
			if la > float64(n)*0.5 {
				out = append(out, fmt.Sprintf("the host is busy: load average %.1f on %d CPUs; the test competes with running work", la, n))
			}
		}
	}
	if vms, _ := f.Glob("/sys/fs/cgroup/machine.slice/machine-*.scope"); len(vms) > 0 {
		out = append(out, fmt.Sprintf("%d virtual machines are running on this host", len(vms)))
	}
	return out
}

// --- running ------------------------------------------------------------------

// Start runs a prepared test in the background. Prepare must have been
// called with the same env (it sets the baseline).
func Start(ctx context.Context, env *Env, cfg Config, plan *Plan, t Test) (*Run, error) {
	if plan.Blocked != "" {
		return nil, errors.New(plan.Blocked)
	}
	if cfg.Duration <= 0 {
		cfg.Duration = plan.Duration
	}
	cfg.DurationMS = cfg.Duration.Milliseconds()
	ctx, cancel := context.WithCancelCause(ctx)
	r := &Run{ID: runID(cfg), Cfg: cfg, Plan: plan, Start: time.Now(), events: make(chan Event, 4096), cancel: cancel,
		done: make(chan struct{}), metrics: map[string]*MetricSummary{}, samples: map[string][]sample{}}
	go r.run(ctx, env, t)
	return r, nil
}

func runID(cfg Config) string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102T150405Z") + "-" + string(cfg.Component) + "-" + hex.EncodeToString(b)
}

func (r *Run) run(ctx context.Context, env *Env, t Test) {
	defer close(r.done)
	defer close(r.events)
	rt := runtime{r}
	base := env.Baseline
	baseCounters := base.Counters()
	baseVM := readVM(env)
	// Background swap-out rate before the test (desktops and busy hosts
	// swap a little all the time); the guard only counts swapping above it.
	if env.prepVM.ok && baseVM.ok && baseVM.pswpout >= env.prepVM.pswpout {
		if dt := time.Since(env.prepAt).Seconds(); dt > 0.2 {
			baseVM.swapRate = float64(baseVM.pswpout-env.prepVM.pswpout) / dt
			if baseVM.pswpin >= env.prepVM.pswpin {
				baseVM.swapInRate = float64(baseVM.pswpin-env.prepVM.pswpin) / dt
			}
		}
	}
	baseVM.at = time.Now()

	if r.Cfg.Component == CompMemory && !env.Simulated() {
		// Should memory run out after all, the OOM killer takes the test
		// (and the engines it starts, which inherit this), not a service.
		if restore, err := preferOOMKill(); err == nil {
			defer restore()
		} else {
			rt.Logf("could not make the test the OOM killer's first choice: %v", err)
		}
	}
	r.emit(Event{Kind: EvPhase, Phase: "load"}, true)
	loadCtx, stopLoad := context.WithTimeout(ctx, r.Cfg.Duration)
	defer stopLoad()

	rules := append(DefaultRules(), extraRules(r.Cfg, env)...)
	var guardTrip string
	var guardOnce sync.Once
	monDone := make(chan struct{})
	go func() {
		defer close(monDone)
		r.monitor(loadCtx, env, base, baseCounters, baseVM, rules, func(reason string) {
			guardOnce.Do(func() {
				guardTrip = reason
				r.mu.Lock()
				if r.abortBy == nil {
					r.abortBy = &Aborted{By: "guard", Reason: reason}
				}
				r.mu.Unlock()
				r.emit(Event{Kind: EvGuard, Text: reason}, true)
				stopLoad()
			})
		})
	}()

	testDone := make(chan error, 1)
	go func() { testDone <- t.Run(withEnv(loadCtx, env), r.Cfg, r.Plan, rt) }()
	var testErr error
	select {
	case testErr = <-testDone:
	case <-loadCtx.Done():
		select {
		case testErr = <-testDone:
		case <-time.After(5 * time.Second):
			testErr = errors.New("test did not stop within 5s of cancellation")
		}
	}
	stopLoad()
	<-monDone
	r.closeTracked()

	r.emit(Event{Kind: EvPhase, Phase: "after"}, true)
	// Let sensors settle a moment, then take the after snapshot with a
	// fresh context (the run context may be cancelled by the user).
	actx, acancel := context.WithTimeout(context.Background(), 60*time.Second)
	after := collector.Collect(actx, env.Collector, env.Cols, nil)
	acancel()

	res := r.verdict(env, base, after, baseCounters, testErr, guardTrip)
	r.result = res
	if err := writeResult(env, res); err != nil {
		res.Log = append(res.Log, "could not write result file: "+err.Error())
	}
	r.emit(Event{Kind: EvFinished, Result: res}, true)
}

func (r *Run) closeTracked() {
	r.mu.Lock()
	cs := r.closers
	r.closers = nil
	r.mu.Unlock()
	for _, c := range cs {
		_ = c.Close()
	}
}

func extraRules(cfg Config, env *Env) []Rule {
	switch cfg.Component {
	case CompMemory:
		return []Rule{RuleMemoryPressure()}
	case CompStorage:
		return []Rule{RuleNewSMARTErrors(storageDisks(cfg, env))}
	}
	return nil
}

// monitor samples sensors and counters, feeds the guard and emits samples.
func (r *Run) monitor(ctx context.Context, env *Env, base *model.Snapshot, baseCounters map[string]uint64, baseVM vmRaw, rules []Rule, trip func(string)) {
	interval := env.Interval
	if interval <= 0 {
		interval = time.Second
	}
	prev := base
	disk := newDiskRates(env)
	start := time.Now()
	var swapNoted int64
	rt := runtime{r}
	for {
		t0 := time.Now()
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		snap := collector.Collect(cctx, env.Collector, env.Cols, prev)
		cancel()
		if ctx.Err() != nil {
			return
		}
		prev = snap
		s := Sample{Snap: snap, Deltas: deltas(baseCounters, snap.Counters()), VM: vmDelta(env, baseVM, r.Cfg)}
		if mib := s.VM.SwapOutPages * 4 / 1024; s.VM.Valid && mib >= swapNoted+256 { // say so, but do not stop
			swapNoted = mib
			rt.Logf("the kernel moved %d MiB of other processes' idle memory to swap to make room", mib)
		}
		for _, rule := range rules {
			if ok, why := rule.Eval(base, s); ok {
				trip(why)
				return
			}
		}
		hostMetrics(rt, snap, disk)
		el := time.Since(start)
		f := min(1, el.Seconds()/r.Cfg.Duration.Seconds())
		r.mu.Lock()
		r.progress = max(r.progress, f)
		r.mu.Unlock()
		r.emit(Event{Kind: EvSample, Sample: &s}, false)
		select {
		case <-ctx.Done():
			return
		case <-time.After(max(0, interval-time.Since(t0))):
		}
	}
}

func deltas(base, cur map[string]uint64) map[string]int64 {
	out := map[string]int64{}
	for k, v := range cur {
		b, ok := base[k]
		if ok && v != b {
			out[k] = int64(v) - int64(b)
		}
	}
	return out
}

// hostMetrics records the host-level series every test shows: CPU load,
// hottest temperature, average frequency, memory used, disk and NIC
// throughput.
func hostMetrics(rt runtime, s *model.Snapshot, disk *diskRates) {
	if c := s.CPU; c != nil {
		var sum, n, fsum, fn float64
		for _, l := range c.Logical {
			if l.UtilizationPercent != nil {
				sum += *l.UtilizationPercent
				n++
			}
			if l.FreqMHz != nil {
				fsum += *l.FreqMHz
				fn++
			}
		}
		if n > 0 {
			rt.Metric("cpu_load", "percent", sum/n)
		}
		if fn > 0 {
			rt.Metric("cpu_freq", "mhz", fsum/fn)
		}
		if c.MaxTempC != nil {
			rt.Metric("cpu_temp", "celsius", *c.MaxTempC)
		}
	}
	if m := s.Memory; m != nil && m.TotalBytes > 0 {
		rt.Metric("mem_used", "percent", float64(m.UsedBytes)/float64(m.TotalBytes)*100)
	}
	if rd, wr, ok := disk.sample(); ok {
		rt.Metric("disk_read", "bytes_per_second", rd)
		rt.Metric("disk_write", "bytes_per_second", wr)
	}
	if n := s.Network; n != nil {
		var rx, tx float64
		have := false
		for _, x := range n.Interfaces {
			if x.Kind == "physical" && x.RxBps != nil && x.TxBps != nil {
				rx += *x.RxBps
				tx += *x.TxBps
				have = true
			}
		}
		if have {
			rt.Metric("net_rx", "bits_per_second", rx)
			rt.Metric("net_tx", "bits_per_second", tx)
		}
	}
	if a := s.Accel; a != nil {
		for _, g := range a.GPUs {
			if g.UtilizationPercent != nil {
				rt.Metric("gpu_load_"+g.PCIAddress, "percent", *g.UtilizationPercent)
			}
		}
	}
}

// --- verdict ------------------------------------------------------------------

func (r *Run) verdict(env *Env, base, after *model.Snapshot, baseCounters map[string]uint64, testErr error, guardTrip string) *Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	res := &Result{SchemaVersion: "1.0", TyrVersion: buildinfo.Version, Hostname: base.Hostname, RunID: r.ID,
		Component: r.Cfg.Component, Engine: r.Cfg.Engine, Config: r.Cfg, Plan: r.Plan, StartedAt: r.Start, EndedAt: time.Now(),
		VerifyErrors: append([]VerifyError{}, r.verify...), Log: append([]string{}, r.log...), Baseline: base, After: after,
		CounterDeltas: deltas(baseCounters, after.Counters()), NewFindings: []model.Finding{}, Reasons: []string{}}
	for _, m := range r.metrics {
		res.Metrics = append(res.Metrics, *m)
	}
	sort.Slice(res.Metrics, func(i, j int) bool { return res.Metrics[i].Name < res.Metrics[j].Name })
	res.Series = map[string]MetricSeries{}
	for name, ss := range r.samples {
		if s, ok := resample(ss, res.EndedAt.Sub(r.Start).Seconds(), seriesPoints); ok {
			s.Unit = r.metrics[name].Unit
			res.Series[name] = s
		}
	}
	baseKeys := map[string]bool{}
	for _, f := range base.Findings() {
		baseKeys[f.Key()] = true
	}
	for _, f := range after.Findings() {
		if !baseKeys[f.Key()] && f.Status != model.StatusOK {
			res.NewFindings = append(res.NewFindings, f)
		}
	}
	res.Aborted = r.abortBy

	fail, warn := []string{}, []string{}
	if n := len(res.VerifyErrors); n > 0 {
		fail = append(fail, fmt.Sprintf("%d verification %s (wrong results)", n, plural(n, "error", "errors")))
	}
	if guardTrip != "" {
		fail = append(fail, "safety guard stopped the test: "+guardTrip)
	}
	if testErr != nil && !errors.Is(testErr, context.Canceled) && !errors.Is(testErr, context.DeadlineExceeded) {
		var ee *EngineFailure
		if errors.As(testErr, &ee) {
			fail = append(fail, ee.Error())
		} else if res.Aborted == nil {
			res.Aborted = &Aborted{By: "error", Reason: testErr.Error()}
		}
	}
	// A CPU at its own limit under full load throttles itself: that is a
	// cooling observation (WARN), not a failure. Past the limit the guard
	// has already stopped the test.
	if lim, m := cpuLimitC(base), r.metrics["cpu_temp"]; lim != nil && m != nil && m.Samples > 0 && m.Max >= *lim {
		warn = append(warn, fmt.Sprintf("CPU reached its %.0f°C limit under load (max %.0f°C): it throttles there, cooling cannot keep it below", *lim, m.Max))
	}
	for _, f := range res.NewFindings {
		if f.Code == "cpu_temp" || f.Code == "core_temp" {
			continue // the after-snapshot is taken while the CPU is still hot from the load
		}
		switch f.Status {
		case model.StatusCrit:
			fail = append(fail, "new finding: "+f.Message)
		case model.StatusWarn:
			warn = append(warn, "new finding: "+f.Message)
		}
	}
	keys := make([]string, 0, len(res.CounterDeltas))
	for k := range res.CounterDeltas {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d := res.CounterDeltas[k]
		if d <= 0 {
			continue
		}
		switch {
		case strings.HasPrefix(k, "mce.") || strings.HasSuffix(k, ".ue") || k == "mem.hardware_corrupted_bytes" ||
			(strings.Contains(k, ".smart.") && !strings.HasSuffix(k, "power_on_hours")) || strings.HasSuffix(k, ".io_errors") ||
			strings.HasSuffix(k, ".kernel_errors") || strings.HasSuffix(k, "ecc.ue"):
			fail = append(fail, fmt.Sprintf("%s +%d", k, d))
		case strings.HasSuffix(k, ".ce") || strings.HasPrefix(k, "throttle.") || strings.Contains(k, "crc") ||
			strings.HasSuffix(k, "rx_errors") || strings.HasSuffix(k, "tx_errors") || strings.HasSuffix(k, "carrier_changes") ||
			strings.HasSuffix(k, "link_failures"):
			warn = append(warn, fmt.Sprintf("%s +%d", k, d))
		}
	}
	if m, ok := r.metrics["net_link_ratio"]; ok && m.Samples > 0 && m.Avg < 90 {
		warn = append(warn, fmt.Sprintf("network throughput %.0f%% of the link speed (below 90%%)", m.Avg))
	}
	switch {
	case len(fail) > 0:
		res.Verdict = VerdictFail
	case res.Aborted != nil && res.Aborted.By != "guard":
		res.Verdict = VerdictAborted
	case len(warn) > 0:
		res.Verdict = VerdictWarn
	default:
		res.Verdict = VerdictPass
	}
	res.Reasons = append(fail, warn...)
	if res.Aborted != nil && res.Aborted.By == "user" {
		res.Reasons = append(res.Reasons, "aborted by user")
	}
	if res.Aborted != nil && res.Aborted.By == "error" {
		res.Reasons = append(res.Reasons, "could not run: "+res.Aborted.Reason)
	}
	el := res.EndedAt.Sub(res.StartedAt).Round(time.Second)
	sum := fmt.Sprintf("%s stress (%s) %s", res.Component, res.Engine, fmtDur(el))
	if len(res.Reasons) > 0 {
		sum += ": " + res.Reasons[0]
		if len(res.Reasons) > 1 {
			sum += fmt.Sprintf(" (+%d more)", len(res.Reasons)-1)
		}
	}
	res.Summary = sum
	return res
}

// EngineFailure is a test failure reported by an external engine (stress-ng
// exit status, mprime "FATAL ERROR", memtester FAILURE).
type EngineFailure struct{ Msg string }

func (e *EngineFailure) Error() string { return e.Msg }

func fmtDur(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	return fmt.Sprintf("%dm%02ds", m, s)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// --- result files -------------------------------------------------------------

// RunDirs returns where results are written / read, in order.
func RunDirs(env *Env) []string {
	if env != nil && env.RunDir != "" {
		return []string{env.RunDir}
	}
	return []string{"/var/lib/tyr/runs", "tyr-runs"}
}

func writeResult(env *Env, res *Result) error {
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	var last error
	for _, d := range RunDirs(env) {
		if err := os.MkdirAll(d, 0o700); err != nil {
			last = err
			continue
		}
		p := filepath.Join(d, res.RunID+".json")
		if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
			last = err
			continue
		}
		res.File = p
		return nil
	}
	return last
}

// History lists previous results (newest first), without the snapshots.
func History(env *Env, limit int) []Result {
	var out []Result
	seen := map[string]bool{}
	for _, d := range RunDirs(env) {
		files, _ := filepath.Glob(filepath.Join(d, "*.json"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			var r struct {
				Result
				Baseline json.RawMessage `json:"baseline"`
				After    json.RawMessage `json:"after"`
			}
			if json.Unmarshal(b, &r) != nil || r.RunID == "" || seen[r.RunID] {
				continue
			}
			seen[r.RunID] = true
			r.Result.File = f
			r.Result.Baseline, r.Result.After, r.Result.Log = nil, nil, nil
			out = append(out, r.Result)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
