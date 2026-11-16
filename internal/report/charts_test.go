package report

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/stress"
)

func chartRun() *stress.Result {
	temps := make([]float64, 30)
	load := make([]float64, 30)
	for i := range temps {
		temps[i], load[i] = 60+float64(i), 95
	}
	base := &model.Snapshot{Hostname: "h", CPU: &model.CPU{PackageTemps: []model.Sensor{{Name: "CPU0 Package",
		Thresholds: &model.Thresholds{UpperCrit: model.F(100)}}}}}
	start := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	return &stress.Result{Hostname: "h", Component: stress.CompCPU, Engine: "builtin", Verdict: stress.VerdictPass,
		StartedAt: start, EndedAt: start.Add(5 * time.Minute), Baseline: base,
		Series: map[string]stress.MetricSeries{
			"cpu_temp":               {Unit: "celsius", StepSeconds: 10, Values: temps},
			"cpu_load":               {Unit: "percent", StepSeconds: 10, Values: load},
			"verified_kernels_per_s": {Unit: "ops_per_second", StepSeconds: 10, Values: load},
			"gpu_load_0000:01:00.0":  {Unit: "percent", StepSeconds: 10, Values: load},
		}}
}

func TestMermaidCharts(t *testing.T) {
	md := Run(chartRun())
	blocks := regexp.MustCompile("(?s)```mermaid\n(.*?)```").FindAllStringSubmatch(md, -1)
	if len(blocks) != 3 {
		t.Fatalf("%d mermaid blocks, want 3 (gpu load is not a CPU chart)\n%s", len(blocks), md)
	}
	for _, b := range blocks {
		lines := strings.Split(strings.TrimSpace(b[1]), "\n")
		if lines[0] != "xychart-beta" || !strings.HasPrefix(strings.TrimSpace(lines[1]), "title \"") ||
			!strings.Contains(lines[2], `x-axis "minutes" 0 --> 5`) || !strings.HasPrefix(strings.TrimSpace(lines[3]), "y-axis ") {
			t.Errorf("chart header\n%s", b[1])
		}
		var n []int
		for _, l := range lines {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "line ["); ok {
				n = append(n, len(strings.Split(strings.TrimSuffix(v, "]"), ",")))
			}
		}
		if len(n) == 0 || n[0] != 30 || (len(n) == 2 && n[1] != 30) {
			t.Errorf("line lengths %v\n%s", n, b[1])
		}
	}
	if !strings.Contains(md, `title "Hottest CPU °C (limit 100)"`) || !strings.Contains(md, "line [100, 100,") {
		t.Errorf("temperature limit line missing\n%s", md)
	}
	if !strings.Contains(md, `y-axis "%" 0 --> 100`) {
		t.Errorf("percent axis\n%s", md)
	}
	// terminal: sparklines, no Mermaid
	term := RunWith(chartRun(), Options{Terminal: true})
	if strings.Contains(term, "```mermaid") || !strings.Contains(term, "▁") || !strings.Contains(term, "limit 100") {
		t.Errorf("terminal charts\n%s", term)
	}
}

func TestMermaidGantt(t *testing.T) {
	t0 := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	runs := []RunSummary{
		{Started: t0, Test: "CPU", Engine: "builtin", Duration: "5m 0s", Verdict: "PASS", seconds: 300},
		{Started: t0.Add(10 * time.Minute), Test: "Memory", Engine: "memtester", Duration: "1m 0s", Verdict: "FAIL", seconds: 60},
		{Started: t0.Add(20 * time.Minute), Test: "CPU", Engine: "builtin", Duration: "5m 0s", Verdict: "WARN", seconds: 300},
	}
	var m md
	writeGantt(&m, runs, Options{})
	g := m.String()
	for _, want := range []string{"gantt", "dateFormat YYYY-MM-DDTHH:mm:ss", "section CPU", "PASS · builtin · 5m 0s :done, 2026-11-16T10:00:00, 300s",
		"WARN · builtin · 5m 0s :active, 2026-11-16T10:20:00, 300s", "section Memory", "FAIL · memtester · 1m 0s :crit, 2026-11-16T10:10:00, 60s"} {
		if !strings.Contains(g, want) {
			t.Errorf("gantt missing %q\n%s", want, g)
		}
	}
	if strings.Count(g, "section CPU") != 1 {
		t.Errorf("CPU section twice\n%s", g)
	}
	m = md{}
	writeGantt(&m, runs, Options{Terminal: true})
	if strings.TrimSpace(m.String()) != "" {
		t.Error("gantt in terminal mode")
	}
}
