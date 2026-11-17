package accept

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/report"
)

var verdictText = map[Verdict]string{
	Pass:       "fit for production",
	Remarks:    "passed, with remarks to read",
	Fail:       "not fit for production",
	Incomplete: "incomplete: a test could not run or was stopped",
}

// Markdown is the acceptance report: the verdict first, then what failed,
// the remarks, each step, the limits, and the full health report after the
// load.
func Markdown(r *Result, after *model.Snapshot) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	m := r.Machine
	w("# Acceptance test: %s\n\n", r.Verdict)
	w("**%s** - %s\n\n", r.Verdict, verdictText[r.Verdict])
	w("| | |\n|---|---|\n")
	row := func(k, v string) {
		if v != "" {
			w("| %s | %s |\n", k, strings.ReplaceAll(v, "|", "\\|"))
		}
	}
	row("Machine", strings.TrimSpace(m.Vendor+" "+m.Product))
	row("Serial", m.Serial)
	row("Hostname", r.Hostname)
	row("BIOS", m.BIOS)
	row("CPU", m.CPU)
	if m.MemoryBytes > 0 {
		row("Memory", fmt.Sprintf("%.0f GB", float64(m.MemoryBytes)/(1<<30)))
	}
	row("Disks", fmt.Sprint(m.Disks))
	if m.GPUs > 0 {
		row("GPUs", fmt.Sprint(m.GPUs))
	}
	row("Profile", r.Profile.Name+" - "+r.Profile.Description)
	row("Started", r.StartedAt.UTC().Format("2006-01-02 15:04 UTC"))
	row("Took", r.EndedAt.Sub(r.StartedAt).Round(time.Minute).String())
	row("Tyr", r.TyrVersion)
	w("\n")

	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		w("## %s\n\n", title)
		for _, s := range items {
			w("- %s\n", s)
		}
		w("\n")
	}
	if r.Verdict == Incomplete {
		list("Did not complete", r.Failures)
	} else {
		list("Failures", r.Failures)
	}
	list("Remarks", r.Remarks)

	w("## Steps\n\n| Step | Result | Took | Details |\n|---|---|---|---|\n")
	for _, s := range r.Steps {
		d := s.Summary
		if s.File != "" {
			md := strings.TrimSuffix(s.File, ".json") + ".md"
			d += fmt.Sprintf(" ([%s](%s))", md, md)
		}
		w("| %s | %s | %s | %s |\n", s.Name, s.Verdict, s.Took.Round(time.Second), strings.ReplaceAll(d, "|", "\\|"))
	}
	w("\n")

	if len(r.CounterDeltas) > 0 {
		w("## Counters that grew during the test\n\n| Counter | Growth |\n|---|---|\n")
		keys := make([]string, 0, len(r.CounterDeltas))
		for k := range r.CounterDeltas {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			w("| `%s` | +%d |\n", k, r.CounterDeltas[k])
		}
		w("\n")
	}

	p := r.Profile
	w("## Limits\n\n")
	w("- Critical findings fail the test%s.\n", ignoring(p.Ignore))
	w("- Warnings that fail it: %s.\n", codes(p.FailOnWarn))
	w("- Machine checks, uncorrectable ECC, SMART, I/O and driver errors that grow during the test fail it; so does growth of %s.\n", codes(p.FailCounters))
	if len(p.MaxTempC) > 0 {
		var t []string
		for _, k := range []string{"cpu", "disk", "dimm"} {
			if v, ok := p.MaxTempC[k]; ok && v > 0 {
				t = append(t, fmt.Sprintf("%s %.0f°C", k, v))
			}
		}
		if len(t) > 0 {
			w("- Highest temperature under load: %s.\n", strings.Join(t, ", "))
		}
	}
	if p.Min.MemoryGB > 0 || p.Min.LogicalCPUs > 0 || p.Min.Disks > 0 {
		w("- Required: ")
		var req []string
		if p.Min.MemoryGB > 0 {
			req = append(req, fmt.Sprintf("%.0f GB memory", p.Min.MemoryGB))
		}
		if p.Min.LogicalCPUs > 0 {
			req = append(req, fmt.Sprintf("%d logical CPUs", p.Min.LogicalCPUs))
		}
		if p.Min.Disks > 0 {
			req = append(req, fmt.Sprintf("%d disks", p.Min.Disks))
		}
		w("%s.\n", strings.Join(req, ", "))
	}
	w("\n")
	if after != nil {
		w("---\n\n")
		// the health report after the load, its headings one level down
		for _, line := range strings.Split(report.Snapshot(after), "\n") {
			if strings.HasPrefix(line, "#") {
				line = "#" + line
			}
			w("%s\n", line)
		}
	}
	return b.String()
}

func codes(c []string) string {
	if len(c) == 0 {
		return "none"
	}
	q := make([]string, len(c))
	for i, s := range c {
		q[i] = "`" + s + "`"
	}
	return strings.Join(q, ", ")
}

func ignoring(c []string) string {
	if len(c) == 0 {
		return ""
	}
	return " (ignored: " + codes(c) + ")"
}
