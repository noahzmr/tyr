package report

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/stress"
)

// Options choose how charts are written.
type Options struct {
	// Terminal: for glamour/glow, which cannot draw Mermaid: charts become
	// sparklines in a code block. Otherwise Mermaid (GitLab, GitHub,
	// Obsidian, mermaid.live render it).
	Terminal bool
}

// chart is one metric of a stress run over time.
type chart struct {
	title  string
	unit   string // y axis label
	values []float64
	step   float64 // seconds between values
	limit  *float64
	yMin   *float64
	yMax   *float64
}

// chartOrder is the metric order in a report; the component's own
// metrics come first, CPU temperature is always shown for context.
var chartOrder = map[stress.Component][]string{
	stress.CompCPU:     {"cpu_load", "cpu_temp", "verified_kernels_per_s", "bogo_ops_per_s_", "mprime_self_tests_passed", "cpu_freq"},
	stress.CompMemory:  {"verified_bytes_per_s", "mem_used", "memtester_loops", "bogo_ops_per_s_", "cpu_temp", "cpu_load"},
	stress.CompStorage: {"read_bytes_per_s_", "file_write_bytes_per_s", "file_read_bytes_per_s", "read_max_ms_", "disk_read", "disk_write", "cpu_temp"},
	stress.CompNetwork: {"net_test_bits_per_s", "net_link_ratio", "net_tx", "net_rx", "tcp_retransmits", "cpu_temp"},
}

const maxCharts = 6

// runCharts picks and scales the charts of a run.
func runCharts(r *stress.Result) []chart {
	if len(r.Series) == 0 {
		return nil
	}
	var names []string
	for n := range r.Series {
		names = append(names, n)
	}
	sort.Strings(names)
	var picked []string
	for _, pref := range chartOrder[r.Component] {
		for _, n := range names {
			if (n == pref || (strings.HasSuffix(pref, "_") && strings.HasPrefix(n, pref))) && !containsStr(picked, n) && len(picked) < maxCharts {
				picked = append(picked, n)
			}
		}
	}
	var out []chart
	for _, n := range picked {
		s := r.Series[n]
		c := chart{title: stress.MetricTitle(n), step: s.StepSeconds}
		div, unit := scale(s.Unit, s.Values)
		c.unit = unit
		for _, v := range s.Values {
			c.values = append(c.values, round(v/div))
		}
		switch s.Unit {
		case "percent":
			c.yMin, c.yMax = model.F(0), model.F(100)
		case "celsius":
			if lim := cpuLimit(r.Baseline); lim != nil && n == "cpu_temp" {
				c.limit = lim
				c.title += fmt.Sprintf(" (limit %.0f)", *lim)
			}
		}
		out = append(out, c)
	}
	return out
}

func indexOf(ss []string, s string) int {
	for i, x := range ss {
		if x == s {
			return i
		}
	}
	return -1
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// cpuLimit is the lowest CPU package limit of a snapshot.
func cpuLimit(s *model.Snapshot) *float64 {
	var lim *float64
	if s != nil && s.CPU != nil {
		for _, p := range s.CPU.PackageTemps {
			if p.Thresholds != nil && p.Thresholds.UpperCrit != nil && (lim == nil || *p.Thresholds.UpperCrit < *lim) {
				lim = p.Thresholds.UpperCrit
			}
		}
	}
	return lim
}

// scale picks a divisor and axis label for a unit.
func scale(unit string, vals []float64) (float64, string) {
	mx := 0.0
	for _, v := range vals {
		mx = math.Max(mx, v)
	}
	pick := func(base string) (float64, string) {
		switch {
		case mx >= 1e9:
			return 1e9, "G" + base
		case mx >= 1e6:
			return 1e6, "M" + base
		case mx >= 1e3:
			return 1e3, "k" + base
		}
		return 1, base
	}
	switch unit {
	case "celsius":
		return 1, "°C"
	case "percent":
		return 1, "%"
	case "mhz":
		return 1, "MHz"
	case "ms":
		return 1, "ms"
	case "bytes_per_second":
		return pick("B/s")
	case "bits_per_second":
		return pick("b/s")
	case "ops_per_second":
		if mx < 1e6 {
			return 1, "/s"
		}
		return pick("/s")
	}
	return 1, ""
}

func round(v float64) float64 {
	if math.Abs(v) >= 100 {
		return math.Round(v)
	}
	return math.Round(v*10) / 10
}

// timeAxis picks the x unit for a run's length.
func timeAxis(total float64) (float64, string) {
	switch {
	case total >= 3*3600:
		return 3600, "hours"
	case total >= 180:
		return 60, "minutes"
	}
	return 1, "seconds"
}

// writeCharts writes the charts as Mermaid xychart-beta or sparklines.
func writeCharts(m *md, cs []chart, o Options) {
	if len(cs) == 0 {
		return
	}
	m.h(2, "Charts")
	if o.Terminal {
		var lines []string
		w := 0
		for _, c := range cs {
			w = max(w, len([]rune(c.title)))
		}
		for _, c := range cs {
			mn, mx, sum := math.Inf(1), math.Inf(-1), 0.0
			for _, v := range c.values {
				mn, mx, sum = math.Min(mn, v), math.Max(mx, v), sum+v
			}
			note := fmt.Sprintf("min %g · avg %g · max %g %s", mn, round(sum/float64(len(c.values))), mx, c.unit)
			if c.limit != nil {
				note += fmt.Sprintf(" · limit %.0f", *c.limit)
			}
			lines = append(lines, fmt.Sprintf("%-*s  %s  %s", w, c.title, sparkline(c.values, 60, c.yMax), note))
		}
		m.p("```\n%s\n```", strings.Join(lines, "\n"))
		return
	}
	for _, c := range cs {
		total := c.step * float64(len(c.values))
		div, xu := timeAxis(total)
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, v := range c.values {
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
		if c.limit != nil {
			hi = math.Max(hi, *c.limit)
		}
		if c.yMin != nil {
			lo = *c.yMin
		} else {
			lo = math.Floor(math.Min(lo, 0.9*lo))
		}
		if c.yMax != nil {
			hi = math.Max(hi, *c.yMax)
		} else {
			hi = math.Ceil(hi * 1.05)
		}
		if hi <= lo {
			hi = lo + 1
		}
		var b strings.Builder
		b.WriteString("```mermaid\nxychart-beta\n")
		fmt.Fprintf(&b, "    title %q\n", mermaidText(c.title))
		fmt.Fprintf(&b, "    x-axis %q 0 --> %g\n", xu, round(total/div))
		fmt.Fprintf(&b, "    y-axis %q %g --> %g\n", mermaidText(c.unit), round(lo), round(hi))
		fmt.Fprintf(&b, "    line [%s]\n", joinFloats(c.values))
		if c.limit != nil {
			lim := make([]float64, len(c.values))
			for i := range lim {
				lim[i] = *c.limit
			}
			fmt.Fprintf(&b, "    line [%s]\n", joinFloats(lim))
		}
		b.WriteString("```")
		m.p("%s", b.String())
	}
}

func joinFloats(vs []float64) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = fmt.Sprintf("%g", v)
	}
	return strings.Join(parts, ", ")
}

// mermaidText keeps quotes and line breaks out of Mermaid labels.
func mermaidText(s string) string {
	return strings.NewReplacer(`"`, "'", "\n", " ").Replace(s)
}

// sparkline draws vals in w cells (▁▂▃▄▅▆▇█), scaled to max (or the
// visible maximum).
func sparkline(vals []float64, w int, maxV *float64) string {
	if len(vals) == 0 {
		return ""
	}
	if len(vals) > w { // average down to w cells
		out := make([]float64, w)
		for i := range out {
			lo, hi := i*len(vals)/w, max((i+1)*len(vals)/w, i*len(vals)/w+1)
			s := 0.0
			for _, v := range vals[lo:hi] {
				s += v
			}
			out[i] = s / float64(hi-lo)
		}
		vals = out
	}
	mn, mx := math.Inf(1), math.Inf(-1)
	for _, v := range vals {
		mn, mx = math.Min(mn, v), math.Max(mx, v)
	}
	if maxV != nil {
		mn, mx = 0, *maxV
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for _, v := range vals {
		i := 0
		if mx > mn {
			i = int((v - mn) / (mx - mn) * float64(len(blocks)-1))
		}
		b.WriteRune(blocks[min(len(blocks)-1, max(0, i))])
	}
	return b.String()
}

// writeGantt writes the stress runs as a Mermaid gantt (Markdown only).
func writeGantt(m *md, runs []RunSummary, o Options) {
	if o.Terminal || len(runs) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("```mermaid\ngantt\n    title Stress tests (UTC)\n    dateFormat YYYY-MM-DDTHH:mm:ss\n")
	span := runs[len(runs)-1].Started.Sub(runs[0].Started)
	if span > 20*time.Hour {
		b.WriteString("    axisFormat %m-%d %H:%M\n")
	} else {
		b.WriteString("    axisFormat %H:%M\n")
	}
	// one section per test, in order of the first run of each
	var tests []string
	for _, r := range runs {
		if !containsStr(tests, r.Test) {
			tests = append(tests, r.Test)
		}
	}
	sorted := append([]RunSummary(nil), runs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := indexOf(tests, sorted[i].Test), indexOf(tests, sorted[j].Test)
		return a < b
	})
	section := ""
	for _, r := range sorted {
		if r.Test != section {
			section = r.Test
			fmt.Fprintf(&b, "    section %s\n", mermaidTask(section))
		}
		tag := map[string]string{"PASS": "done, ", "FAIL": "crit, ", "WARN": "active, "}[r.Verdict]
		secs := max(1, int(math.Round(r.seconds)))
		fmt.Fprintf(&b, "    %s :%s%s, %ds\n", mermaidTask(r.Verdict+" · "+r.Engine+" · "+r.Duration), tag,
			r.Started.UTC().Format("2006-01-02T15:04:05"), secs)
	}
	b.WriteString("```")
	m.p("%s", b.String())
}

// mermaidTask removes the characters gantt task names cannot hold.
func mermaidTask(s string) string {
	return strings.NewReplacer(":", " ", "#", " ", ";", " ", "\n", " ").Replace(s)
}
