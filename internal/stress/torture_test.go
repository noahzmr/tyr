package stress

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTortureEngine(t *testing.T) {
	env := fixtureEnv(t, "server-degraded")
	found := false
	for _, e := range Engines(CompCPU, env) {
		if e.Name == EngTorture {
			found = e.Available
		}
	}
	if !found {
		t.Fatal("torture engine not offered in fixtures mode")
	}
	// the plan on a real host env: mode, sizes, memory, the honest note
	host := hostEnv(twoCPUs())
	p := cpuTorture{}.Plan(Config{Component: CompCPU, Engine: EngTorture, FFT: "large", Duration: time.Minute}, host)
	all := strings.Join(append(append(p.Scope, p.Impact...), p.Info...), "\n")
	for _, want := range []string{"large FFTs 256K–1M", "16 MiB per CPU", "round-off below 0.40", "mprime"} {
		if !strings.Contains(all, want) {
			t.Errorf("plan lacks %q:\n%s", want, all)
		}
	}
	if p := (cpuTorture{}).Plan(Config{Component: CompCPU, Engine: EngTorture, FFT: "small"}, host); strings.Contains(strings.Join(p.Impact, " "), "MiB") {
		t.Errorf("small FFTs claim memory use: %v", p.Impact)
	}
	// a short real run: iterations counted, no false errors
	rt := newRT()
	ctx, cancel := context.WithTimeout(withEnv(context.Background(), host), 1500*time.Millisecond)
	defer cancel()
	if err := (cpuTorture{}).Run(ctx, Config{Component: CompCPU, Engine: EngTorture, FFT: "small", CPUs: "0"}, nil, rt); err != nil {
		t.Fatal(err)
	}
	if len(rt.verify) != 0 || len(rt.metrics["fft_iterations_per_s"]) == 0 || rt.metrics["fft_iterations_per_s"][0] <= 0 {
		t.Errorf("verify %v metrics %v", rt.verify, rt.metrics)
	}
}
