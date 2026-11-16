package stress

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/collector/all"
	"github.com/noahzmr/tyr/internal/execx"
	"github.com/noahzmr/tyr/internal/fixture"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/netpeer"
)

// fakeRT records what a test reports.
type fakeRT struct {
	mu      sync.Mutex
	verify  []VerifyError
	metrics map[string][]float64
	logs    []string
}

func newRT() *fakeRT { return &fakeRT{metrics: map[string][]float64{}} }

func (r *fakeRT) Progress(float64, string) {}
func (r *fakeRT) Metric(n, _ string, v float64) {
	r.mu.Lock()
	r.metrics[n] = append(r.metrics[n], v)
	r.mu.Unlock()
}
func (r *fakeRT) VerifyError(e VerifyError) {
	r.mu.Lock()
	r.verify = append(r.verify, e)
	r.mu.Unlock()
}
func (r *fakeRT) Track(io.Closer) {}
func (r *fakeRT) Logf(f string, a ...any) {
	r.mu.Lock()
	r.logs = append(r.logs, f)
	r.mu.Unlock()
}

// hostEnv is a real-host env with a synthetic baseline.
func hostEnv(base *model.Snapshot) *Env {
	return &Env{Collector: &collector.Env{FS: hostfs.New("/"), Exec: execx.Host(), Now: time.Now, Opts: collector.DefaultOptions()}, Baseline: base}
}

func twoCPUs() *model.Snapshot {
	return &model.Snapshot{Hostname: "t", CPU: &model.CPU{Logical: []model.LogicalCPU{{ID: 0, Online: true}, {ID: 1, Online: true}}}}
}

func TestCPUBuiltinDetectsWrongResults(t *testing.T) {
	env := hostEnv(twoCPUs())
	ctx, cancel := context.WithTimeout(withEnv(context.Background(), env), 400*time.Millisecond)
	defer cancel()
	rt := newRT()
	if err := (cpuBuiltin{}).Run(ctx, Config{Component: CompCPU}, nil, rt); err != nil {
		t.Fatal(err)
	}
	if len(rt.verify) != 0 {
		t.Fatalf("healthy run reported %v", rt.verify)
	}
	corruptCPU = func(cpu int, k string, v uint64) uint64 {
		if cpu == 1 && k == "fma" {
			return v ^ 4
		}
		return v
	}
	defer func() { corruptCPU = nil }()
	ctx2, cancel2 := context.WithTimeout(withEnv(context.Background(), env), 400*time.Millisecond)
	defer cancel2()
	rt = newRT()
	_ = (cpuBuiltin{}).Run(ctx2, Config{Component: CompCPU}, nil, rt)
	if len(rt.verify) == 0 || rt.verify[0].Subject != "cpu1" || rt.verify[0].Workload != "fma" {
		t.Fatalf("corruption not attributed: %+v", rt.verify)
	}
	if len(rt.verify) > 10 {
		t.Errorf("errors per CPU not capped: %d", len(rt.verify))
	}
}

func TestMemoryBuiltinPatterns(t *testing.T) {
	base := twoCPUs()
	base.Memory = &model.Memory{TotalBytes: 16 << 30, AvailableBytes: 1<<30 + 128<<20}
	env := hostEnv(base)
	if size, _ := memSize(Config{}, env); size != 128<<20 {
		t.Fatalf("size %d, want 128 MiB (keep 1 GiB free)", size)
	}
	run := func() *fakeRT {
		ctx, cancel := context.WithTimeout(withEnv(context.Background(), env), 1500*time.Millisecond)
		defer cancel()
		rt := newRT()
		if err := (memBuiltin{}).Run(ctx, Config{Component: CompMemory}, nil, rt); err != nil {
			t.Fatal(err)
		}
		return rt
	}
	if rt := run(); len(rt.verify) != 0 || len(rt.metrics["verified_bytes_per_s"]) == 0 {
		t.Fatalf("healthy: errors %v metrics %v", rt.verify, rt.metrics)
	}
	corruptMem = func(w []uint64, pass int) { w[len(w)/2] ^= 1 << 17 }
	defer func() { corruptMem = nil }()
	rt := run()
	if len(rt.verify) == 0 || !strings.Contains(rt.verify[0].Detail, "1 bits differ") || !strings.HasPrefix(rt.verify[0].Subject, "0x") {
		t.Fatalf("bit flip not found: %+v", rt.verify)
	}
	// too little memory: refuse
	base.Memory.AvailableBytes = 1 << 30
	if p := (memBuiltin{}).Plan(Config{}, env); p.Blocked == "" {
		t.Error("plan should refuse when only the reserve is available")
	}
}

func TestStorageFileTestDetectsCorruption(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rt := newRT()
	if err := fileTest(ctx, dir, 16<<20, rt); err != nil {
		t.Fatal(err)
	}
	if len(rt.verify) != 0 {
		t.Fatalf("healthy file test: %v", rt.verify)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("file test left %d files behind", len(ents))
	}
	corruptFile = func(f *os.File, pass int) {
		if pass == 1 {
			_, _ = f.WriteAt([]byte("garbage"), 3<<20+100)
			_ = f.Sync()
		}
	}
	defer func() { corruptFile = nil }()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	rt = newRT()
	_ = fileTest(ctx2, dir, 16<<20, rt)
	if len(rt.verify) == 0 || !strings.Contains(rt.verify[0].Detail, "CRC32C") {
		t.Fatalf("corruption not found: %+v", rt.verify)
	}
}

func TestSurfaceScanReadsDevice(t *testing.T) {
	// A regular file on a real filesystem stands in for a block device.
	dir, err := os.MkdirTemp(".", ".scan-")
	if err != nil {
		t.Skip(err)
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(p, make([]byte, 8<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	rt := newRT()
	err = surfaceScan(ctx, hostEnv(&model.Snapshot{}), model.Disk{Name: "img", Path: p, SizeBytes: 8 << 20}, rt)
	if err != nil && strings.Contains(err.Error(), "O_DIRECT") {
		t.Skip("filesystem without O_DIRECT")
	}
	if err != nil || len(rt.verify) != 0 {
		t.Fatalf("scan: %v %v", err, rt.verify)
	}
	if len(rt.metrics["read_bytes_per_s_img"]) == 0 && !strings.Contains(strings.Join(rt.logs, "\n"), "full pass") {
		t.Errorf("no read progress: %v %v", rt.metrics, rt.logs)
	}
}

func TestNetworkAgainstNetPeer(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &netpeer.Server{Token: "secret"}
	sctx, stop := context.WithCancel(context.Background())
	defer stop()
	go func() { _ = srv.Serve(sctx, l) }()
	env := hostEnv(&model.Snapshot{})
	for _, reverse := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(withEnv(context.Background(), env), 1500*time.Millisecond)
		rt := newRT()
		cfg := Config{Component: CompNetwork, Target: l.Addr().String(), Token: "secret", Streams: 2, Reverse: reverse, Duration: time.Second}
		if err := (netBuiltin{}).Run(ctx, cfg, nil, rt); err != nil {
			t.Fatalf("reverse=%v: %v", reverse, err)
		}
		cancel()
		if len(rt.verify) != 0 || len(rt.metrics["net_test_bits_per_s"]) == 0 {
			t.Fatalf("reverse=%v: verify %v metrics %v", reverse, rt.verify, rt.metrics)
		}
		if !strings.Contains(strings.Join(rt.logs, "\n"), "receiver verified") {
			t.Errorf("no receiver stats: %v", rt.logs)
		}
		time.Sleep(200 * time.Millisecond) // peer ends the session
	}
	ctx, cancel := context.WithTimeout(withEnv(context.Background(), env), time.Second*3)
	defer cancel()
	err = (netBuiltin{}).Run(ctx, Config{Target: l.Addr().String(), Token: "wrong", Streams: 1}, nil, newRT())
	if err == nil || !strings.Contains(err.Error(), "wrong token") {
		t.Fatalf("wrong token: %v", err)
	}
	if p := netPlan(Config{Component: CompNetwork}, env, EngStressNG); len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "does not send traffic through a network card") {
		t.Error("stress-ng network plan must say it does not test the NIC")
	}
}

// fakeTool installs an executable script named name in a PATH directory.
func fakeTool(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestStressNGOutputAndExit(t *testing.T) {
	fakeTool(t, "stress-ng", `echo "stress-ng: info:  [11] dispatching hogs: 2 cpu"
echo "stress-ng: fail:  [12] cpu: fft verification failed" >&2
echo "stress-ng: metrc: [11] cpu   1000   10.00   9.80   0.10   100.50   1.0"
exit 2
`)
	rt := newRT()
	err := stressNG(context.Background(), Config{Duration: time.Second}, rt, "cpu", "--cpu", "2")
	var ef *EngineFailure
	if !errors.As(err, &ef) {
		t.Fatalf("exit 2 should be an engine failure: %v", err)
	}
	if len(rt.verify) != 1 || !strings.Contains(rt.verify[0].Detail, "verification failed") {
		t.Errorf("verify errors %+v", rt.verify)
	}
	if v := rt.metrics["bogo_ops_per_s_cpu"]; len(v) != 1 || v[0] != 100.5 {
		t.Errorf("metrics %v", rt.metrics)
	}
	if !mprimeFail("FATAL ERROR: Rounding was 0.5, expected less than 0.4") || !mprimeFail("Hardware failure detected, consult stress.txt file.") || mprimeFail("Self-test 448K passed!") {
		t.Error("mprime failure matcher")
	}
}

func TestExternalEngineKilledWithChildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	fakeTool(t, "stress-ng", "sleep 300 & echo $! > "+pidFile+"\ntrap '' TERM\nwhile true; do sleep 1; done\n")
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	start := time.Now()
	_ = stressNG(ctx, Config{Duration: time.Second}, newRT(), "cpu")
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("stop took %v", d)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	_, _ = jsonNumber(strings.TrimSpace(string(b)), &pid)
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(pid, 0); err == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("child %d of the engine survived the stop", pid)
	}
}

func jsonNumber(s string, v *int) (bool, error) { return true, json.Unmarshal([]byte(s), v) }

func fixtureEnv(t *testing.T, name string) *Env {
	t.Helper()
	cenv, _, err := fixture.Load("../../testdata/fixtures/"+name, collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return &Env{Collector: cenv, Cols: all.Collectors(), RunDir: t.TempDir(), Interval: 100 * time.Millisecond}
}

func runSim(t *testing.T, env *Env, cfg Config, during func(*Run)) *Result {
	t.Helper()
	plan, test, err := Prepare(context.Background(), env, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Start(context.Background(), env, cfg, plan, test)
	if err != nil {
		t.Fatal(err)
	}
	if during != nil {
		go during(r)
	}
	kinds := map[EventKind]int{}
	for e := range r.Events() {
		kinds[e.Kind]++
	}
	res := r.Wait()
	if kinds[EvFinished] != 1 || kinds[EvSample] == 0 || kinds[EvMetric] == 0 {
		t.Errorf("events %v", kinds)
	}
	return res
}

func TestLifecycleVerdicts(t *testing.T) {
	env := fixtureEnv(t, "server-healthy")
	res := runSim(t, env, Config{Component: CompCPU, Engine: EngBuiltin, Duration: 800 * time.Millisecond}, nil)
	if res.Verdict != VerdictPass || res.File == "" || res.Baseline == nil || res.After == nil {
		t.Fatalf("pass run: %s %v file=%q", res.Verdict, res.Reasons, res.File)
	}
	b, _ := os.ReadFile(res.File)
	var back Result
	if err := json.Unmarshal(b, &back); err != nil || back.RunID != res.RunID || back.Verdict != VerdictPass {
		t.Fatalf("result file: %v %+v", err, back.RunID)
	}
	if fi, _ := os.Stat(res.File); fi.Mode().Perm() != 0o600 {
		t.Errorf("result file mode %v", fi.Mode().Perm())
	}

	env = fixtureEnv(t, "server-healthy")
	res = runSim(t, env, Config{Component: CompMemory, Engine: EngBuiltin, Duration: 1200 * time.Millisecond, SimError: true}, nil)
	if res.Verdict != VerdictFail || len(res.VerifyErrors) != 1 || !strings.Contains(res.Summary, "verification error") {
		t.Fatalf("sim error: %s %v", res.Verdict, res.Reasons)
	}
	if len(res.Plan.Info) == 0 || !strings.Contains(strings.Join(res.Plan.Info, " "), "memtest86+") {
		t.Errorf("memory plan should explain memtest86+: %v", res.Plan.Info)
	}

	env = fixtureEnv(t, "server-healthy")
	res = runSim(t, env, Config{Component: CompStorage, Engine: EngBuiltin, Duration: time.Minute}, func(r *Run) {
		time.Sleep(300 * time.Millisecond)
		r.Abort("user pressed esc")
	})
	if res.Verdict != VerdictAborted || res.Aborted == nil || res.Aborted.By != "user" || res.Verdict.ExitCode() != 3 {
		t.Fatalf("abort: %s %+v", res.Verdict, res.Aborted)
	}
	if h := History(env, 10); len(h) != 1 || h[0].RunID != res.RunID || h[0].Baseline != nil {
		t.Errorf("history %+v", h)
	}
	if _, err := testFor(Config{Component: CompCPU, Engine: EngStressNG}, env); err == nil {
		t.Error("external engines must not run in fixtures mode")
	}
}

func cpuAt(v, crit float64) *model.Snapshot {
	return &model.Snapshot{CPU: &model.CPU{PackageTemps: []model.Sensor{{Name: "CPU0 Package", Value: &v, Status: model.StatusCrit,
		Thresholds: &model.Thresholds{UpperWarn: model.F(crit - 10), UpperCrit: model.F(crit)}}}}}
}

func TestGuardRules(t *testing.T) {
	hot := 101.0
	snap := &model.Snapshot{CPU: &model.CPU{PackageTemps: []model.Sensor{{Name: "CPU0 Package", Value: &hot, Status: model.StatusCrit}}}}
	cases := []struct {
		rule Rule
		s    Sample
		trip bool
	}{
		{RuleTempCritical(), Sample{Snap: snap}, true},
		// at its own 100 °C limit a CPU throttles: the guard lets it run …
		{RuleTempCritical(), Sample{Snap: cpuAt(101, 100)}, false},
		// … and stops it 5 °C past the limit
		{RuleTempCritical(), Sample{Snap: cpuAt(105, 100)}, true},
		{RuleTempCritical(), Sample{Snap: &model.Snapshot{}}, false},
		{RuleNewMCE(), Sample{Deltas: map[string]int64{"mce.exceptions.cpu3": 1}}, true},
		{RuleNewMCE(), Sample{Deltas: map[string]int64{"throttle.cpu3": 5}}, false},
		{RuleNewUncorrectable(), Sample{Deltas: map[string]int64{"edac.mc0.dimm1.ue": 1}}, true},
		{RuleNewUncorrectable(), Sample{Deltas: map[string]int64{"edac.mc0.dimm1.ce": 9}}, false},
		{RuleNewSMARTErrors([]string{"sda"}), Sample{Deltas: map[string]int64{"disk.sda.smart.pending": 2}}, true},
		{RuleNewSMARTErrors([]string{"sda"}), Sample{Deltas: map[string]int64{"disk.sdb.smart.pending": 2}}, false},
		{RuleMemoryPressure(), Sample{VM: VMStat{Valid: true, OOMKills: 1}}, true},
		{RuleMemoryPressure(), Sample{VM: VMStat{Valid: true, SwapOutPages: 5000}}, true},
		{RuleMemoryPressure(), Sample{VM: VMStat{Valid: true, MemAvailable: 100 << 20, ReserveBytes: 512 << 20}}, true},
		{RuleMemoryPressure(), Sample{VM: VMStat{Valid: true, MemAvailable: 4 << 30, ReserveBytes: 512 << 20}}, false},
	}
	for i, c := range cases {
		if trip, why := c.rule.Eval(nil, c.s); trip != c.trip {
			t.Errorf("case %d %s: trip=%v (%s)", i, c.rule.Name(), trip, why)
		}
	}
	// The AMD laptop sits at 96 °C: below its 100 °C Tjmax it may run …
	env := fixtureEnv(t, "laptop-amd-nonroot")
	plan, _, err := Prepare(context.Background(), env, Config{Component: CompCPU, Engine: EngBuiltin, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked != "" {
		t.Errorf("96 °C on a 100 °C CPU refused: %q", plan.Blocked)
	}
	if g := CPUGuardTempC(env.Baseline); g == nil || *g != 105 {
		t.Errorf("guard point %v, want 105", g)
	}
	// … with an explicit --crit-temp 90 the host is already past the guard
	// point (90 + 5) and is refused before the test.
	env = fixtureEnv(t, "laptop-amd-nonroot")
	env.Collector.Opts.Thresholds = check.Defaults{WarnTempC: 75, CritTempC: 90, Explicit: true}
	plan, _, err = Prepare(context.Background(), env, Config{Component: CompCPU, Engine: EngBuiltin, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Blocked, "critical temperature") {
		t.Errorf("hot laptop not refused with --crit-temp 90: %q", plan.Blocked)
	}
}

func TestVerdictCPUAtLimitIsWarn(t *testing.T) {
	base := cpuAt(60, 100)
	base.CPU.PackageTemps[0].Status = model.StatusOK
	after := cpuAt(100, 100)
	after.CPU.Find(model.StatusCrit, "cpu_temp", "CPU0 Package", "k10temp", "CPU0 Package at 100°C (crit 100°C)")
	r := &Run{ID: "t", Cfg: Config{Component: CompCPU, Engine: EngBuiltin}, Start: time.Now(),
		metrics: map[string]*MetricSummary{"cpu_temp": {Name: "cpu_temp", Max: 100, Samples: 30}}}
	res := r.verdict(nil, base, after, nil, nil, "")
	if res.Verdict != VerdictWarn || len(res.Reasons) != 1 || !strings.Contains(res.Reasons[0], "reached its 100°C limit") {
		t.Errorf("verdict %s %v", res.Verdict, res.Reasons)
	}
	// below the limit the CPU being hot right after the load is no reason
	r.metrics["cpu_temp"].Max = 99
	if res := r.verdict(nil, base, after, nil, nil, ""); res.Verdict != VerdictPass || len(res.Reasons) != 0 {
		t.Errorf("verdict %s %v", res.Verdict, res.Reasons)
	}
}
