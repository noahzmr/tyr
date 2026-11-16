package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/noahzmr/tyr/internal/stress"
)

// compName is the display name of a stress component.
func compName(c stress.Component) string {
	switch c {
	case stress.CompCPU:
		return "CPU"
	case stress.CompMemory:
		return "Memory"
	case stress.CompStorage:
		return "Storage"
	case stress.CompNetwork:
		return "Network"
	}
	return string(c)
}

// metricValue formats a metric value with its unit.
func metricValue(v float64, unit string) string {
	switch unit {
	case "bytes_per_second":
		return rate(v, "B/s")
	case "bits_per_second":
		return rate(v, "b/s")
	case "percent":
		return fmt.Sprintf("%.0f %%", v)
	case "celsius":
		return fmt.Sprintf("%.0f °C", v)
	case "mhz":
		return fmt.Sprintf("%.0f MHz", v)
	case "ms":
		return fmt.Sprintf("%.1f ms", v)
	case "ops_per_second":
		return rate(v, "/s")
	}
	return fmt.Sprintf("%.0f", v)
}

func rate(v float64, suffix string) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.2f G%s", v/1e9, suffix)
	case v >= 1e6:
		return fmt.Sprintf("%.1f M%s", v/1e6, suffix)
	case v >= 1e3:
		return fmt.Sprintf("%.1f k%s", v/1e3, suffix)
	}
	return fmt.Sprintf("%.0f %s", v, suffix)
}

// Run is the report of one stress test.
func Run(r *stress.Result) string {
	var m md
	m.line("# %s · %s stress test", esc(r.Hostname), compName(r.Component))
	m.line("")
	why := "no wrong results, no new errors, no new findings"
	if len(r.Reasons) > 0 {
		why = r.Reasons[0]
	}
	m.p("**Verdict: %s** · %s", verdict(string(r.Verdict)), esc(why))
	aborted := ""
	if a := r.Aborted; a != nil {
		aborted = a.By + ": " + a.Reason
	}
	m.kv([2]string{"Started", when(r.StartedAt)}, [2]string{"Duration", dur(r.EndedAt.Sub(r.StartedAt))},
		[2]string{"Engine", r.Engine}, [2]string{"Configuration", runConfig(r.Config)}, [2]string{"Aborted", aborted},
		[2]string{"Tyr", r.TyrVersion}, [2]string{"Run", r.RunID}, [2]string{"File", r.File})

	if len(r.Reasons) > 1 {
		m.h(2, "Reasons")
		for _, x := range r.Reasons {
			m.line("- %s", esc(x))
		}
		m.line("")
	}
	if p := r.Plan; p != nil && len(p.Scope)+len(p.Warnings) > 0 {
		m.h(2, "Scope")
		for _, x := range p.Scope {
			m.line("- %s", esc(x))
		}
		for _, x := range p.Warnings {
			m.line("- ▲ %s", esc(x))
		}
		m.line("")
	}
	if len(r.Metrics) > 0 {
		m.h(2, "Metrics")
		var rows [][]string
		for _, x := range r.Metrics {
			rows = append(rows, []string{stress.MetricTitle(x.Name), metricValue(x.Min, x.Unit), metricValue(x.Avg, x.Unit), metricValue(x.Max, x.Unit)})
		}
		m.table([]string{"Metric", ">Min", ">Avg", ">Max"}, rows)
	}
	m.h(2, "Wrong results")
	if len(r.VerifyErrors) == 0 {
		m.p("● None: every checked result was correct.")
	} else {
		var rows [][]string
		for _, v := range r.VerifyErrors {
			rows = append(rows, []string{v.Time.UTC().Format("15:04:05"), v.Subject, v.Workload, v.Expected, v.Got, v.Detail})
		}
		m.table([]string{"Time", "Where", "Workload", "Expected", "Got", "Detail"}, rows)
	}
	var keys []string
	for k, d := range r.CounterDeltas {
		if d != 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		m.h(2, "Counters that changed")
		var rows [][]string
		for _, k := range keys {
			rows = append(rows, []string{k, fmt.Sprintf("%+d", r.CounterDeltas[k])})
		}
		m.table([]string{"Counter", ">Change"}, rows)
	}
	if len(r.NewFindings) > 0 {
		m.h(2, "New alerts after the test")
		var rows [][]string
		for _, f := range r.NewFindings {
			rows = append(rows, []string{status(f.Status), f.Subject, f.Message})
		}
		m.table([]string{"Severity", "Component", "Alert"}, rows)
	}
	if len(r.Log) > 0 {
		m.h(2, "Log")
		lines := r.Log
		if len(lines) > 60 {
			lines = append([]string{fmt.Sprintf("… %d earlier lines in the result file", len(lines)-60)}, lines[len(lines)-60:]...)
		}
		m.p("```\n%s\n```", strings.Join(lines, "\n"))
	}
	return m.String()
}

func runConfig(c stress.Config) string {
	var p []string
	switch c.Component {
	case stress.CompCPU:
		if c.CPUs != "" {
			p = append(p, "CPUs "+c.CPUs)
		} else {
			p = append(p, "all online CPUs")
		}
	case stress.CompMemory:
		p = append(p, fmt.Sprintf("%d %% of available memory", max(c.MemoryPercent, 0)))
	case stress.CompStorage:
		p = append(p, strings.TrimSpace(c.StorageMode+" "+c.Target))
	case stress.CompNetwork:
		p = append(p, fmt.Sprintf("peer %s · %d streams", c.Target, c.Streams))
		if c.Reverse {
			p = append(p, "peer sends")
		}
	}
	return strings.Join(p, " · ")
}
