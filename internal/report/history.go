package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/model"
)

// History is the state of one host across many documents. Stress results
// contribute their run and their baseline/after snapshots.
type History struct {
	Host      string         `json:"host"`
	From      time.Time      `json:"from"`
	To        time.Time      `json:"to"`
	Snapshots []Point        `json:"snapshots"`
	Alerts    []AlertSpan    `json:"alerts"`
	Counters  []CounterTrend `json:"counters"`
	Sensors   []SensorTrend  `json:"sensors"`
	Changes   []Change       `json:"hardware_changes"`
	Runs      []RunSummary   `json:"stress_runs"`
	Files     []string       `json:"files"`
}

// Point is one snapshot in the history.
type Point struct {
	Time    time.Time    `json:"time"`
	Status  model.Status `json:"status"`
	Crit    int          `json:"crit"`
	Warn    int          `json:"warn"`
	Summary string       `json:"summary"`
	Kind    string       `json:"kind"` // report, before stress, after stress
	File    string       `json:"file"`
	snap    *model.Snapshot
}

// AlertSpan is one alert (module + code + component) over time.
type AlertSpan struct {
	Module     string       `json:"module"`
	Code       string       `json:"code"`
	Subject    string       `json:"subject"`
	Message    string       `json:"message"` // as last seen
	Worst      model.Status `json:"worst"`
	FirstSeen  time.Time    `json:"first_seen"`
	LastSeen   time.Time    `json:"last_seen"`
	Seen       int          `json:"seen"` // snapshots it appeared in
	Open       bool         `json:"open"`
	ResolvedAt *time.Time   `json:"resolved_at,omitempty"`
}

// CounterTrend is an error/event counter that changed in the period.
type CounterTrend struct {
	Counter string    `json:"counter"`
	First   uint64    `json:"first"`
	Last    uint64    `json:"last"`
	Change  int64     `json:"change"`
	Reset   bool      `json:"reset"` // went down: a reboot or driver reload
	FirstAt time.Time `json:"first_at"`
	LastAt  time.Time `json:"last_at"`
}

// SensorTrend is a reading across the snapshots.
type SensorTrend struct {
	Sensor   string       `json:"sensor"`
	Unit     string       `json:"unit"`
	Min      float64      `json:"min"`
	Max      float64      `json:"max"`
	Last     float64      `json:"last"`
	Limit    *float64     `json:"limit,omitempty"`
	Worst    model.Status `json:"worst"`
	Readings int          `json:"readings"`
}

// Change is a hardware or firmware change between two snapshots.
type Change struct {
	Time      time.Time `json:"time"`
	Kind      string    `json:"kind"` // added, removed, replaced, firmware
	Component string    `json:"component"`
	Before    string    `json:"before,omitempty"`
	After     string    `json:"after,omitempty"`
}

// RunSummary is one stress test.
type RunSummary struct {
	Started      time.Time `json:"started"`
	Test         string    `json:"test"`
	Engine       string    `json:"engine"`
	Duration     string    `json:"duration"`
	Verdict      string    `json:"verdict"`
	Reason       string    `json:"reason,omitempty"`
	WrongResults int       `json:"wrong_results"`
	MaxCPUTempC  *float64  `json:"max_cpu_temp_celsius,omitempty"`
	File         string    `json:"file"`
	seconds      float64
}

// BuildHistory computes the history of every host in docs.
func BuildHistory(docs []Doc) []History {
	names, by := Hosts(docs)
	var out []History
	for _, n := range names {
		out = append(out, buildHost(n, by[n]))
	}
	return out
}

func buildHost(host string, docs []Doc) History {
	h := History{Host: host}
	add := func(s *model.Snapshot, kind, file string) {
		if s == nil || s.Timestamp.IsZero() {
			return
		}
		p := Point{Time: s.Timestamp, Status: s.Status, Summary: s.Summary, Kind: kind, File: file, snap: s}
		for _, f := range findings(s) {
			switch f.Status {
			case model.StatusCrit:
				p.Crit++
			case model.StatusWarn:
				p.Warn++
			}
		}
		h.Snapshots = append(h.Snapshots, p)
	}
	for _, d := range docs {
		h.Files = append(h.Files, d.Path)
		if d.Snap != nil {
			add(d.Snap, "report", d.Path)
			continue
		}
		r := d.Run
		add(r.Baseline, "before stress", d.Path)
		add(r.After, "after stress", d.Path)
		rs := RunSummary{Started: r.StartedAt, Test: compName(r.Component), Engine: r.Engine, Duration: dur(r.EndedAt.Sub(r.StartedAt)),
			Verdict: string(r.Verdict), WrongResults: len(r.VerifyErrors), File: d.Path, seconds: r.EndedAt.Sub(r.StartedAt).Seconds()}
		if len(r.Reasons) > 0 {
			rs.Reason = r.Reasons[0]
		}
		for _, m := range r.Metrics {
			if m.Name == "cpu_temp" && m.Samples > 0 {
				rs.MaxCPUTempC = model.F(m.Max)
			}
		}
		h.Runs = append(h.Runs, rs)
	}
	sort.SliceStable(h.Snapshots, func(i, j int) bool { return h.Snapshots[i].Time.Before(h.Snapshots[j].Time) })
	sort.SliceStable(h.Runs, func(i, j int) bool { return h.Runs[i].Started.Before(h.Runs[j].Started) })
	if n := len(h.Snapshots); n > 0 {
		h.From, h.To = h.Snapshots[0].Time, h.Snapshots[n-1].Time
	}
	for _, r := range h.Runs {
		if h.From.IsZero() || r.Started.Before(h.From) {
			h.From = r.Started
		}
		if r.Started.After(h.To) {
			h.To = r.Started
		}
	}
	h.Alerts = alertSpans(h.Snapshots)
	h.Counters = counterTrends(h.Snapshots)
	h.Sensors = sensorTrends(h.Snapshots)
	h.Changes = hardwareChanges(h.Snapshots)
	return h
}

func alertSpans(pts []Point) []AlertSpan {
	spans := map[string]*AlertSpan{}
	var order []string
	for i, p := range pts {
		present := map[string]bool{}
		for _, f := range findings(p.snap) {
			if f.Status == model.StatusOK {
				continue
			}
			k := string(f.Module) + "|" + f.Code + "|" + f.Subject + "|" + digitless(f.Message)
			if present[k] {
				continue // the same alert twice in one snapshot
			}
			present[k] = true
			s := spans[k]
			if s == nil {
				s = &AlertSpan{Module: moduleNames[f.Module], Code: f.Code, Subject: f.Subject, FirstSeen: p.Time}
				spans[k] = s
				order = append(order, k)
			}
			s.Message, s.LastSeen, s.Worst = f.Message, p.Time, model.Worst(s.Worst, f.Status)
			s.Seen++
			s.ResolvedAt = nil
		}
		// an alert is resolved only where its module was collected again
		for _, k := range order {
			s := spans[k]
			mod := strings.SplitN(k, "|", 2)[0]
			if !present[k] && s.ResolvedAt == nil && p.Time.After(s.LastSeen) && p.snap.Section(model.ModuleID(mod)) != nil {
				t := pts[i].Time
				s.ResolvedAt = &t
			}
		}
	}
	var out []AlertSpan
	for _, k := range order {
		s := spans[k]
		s.Open = s.ResolvedAt == nil
		out = append(out, *s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Open != out[j].Open {
			return out[i].Open
		}
		if out[i].Worst != out[j].Worst {
			return out[i].Worst > out[j].Worst
		}
		return out[i].FirstSeen.Before(out[j].FirstSeen)
	})
	return out
}

// digitless masks numbers, so "at 93°C" and "at 95°C" are one alert.
func digitless(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return '#'
		}
		return r
	}, s)
}

func counterTrends(pts []Point) []CounterTrend {
	trends := map[string]*CounterTrend{}
	var keys []string
	for _, p := range pts {
		for k, v := range p.snap.Counters() {
			t := trends[k]
			if t == nil {
				t = &CounterTrend{Counter: k, First: v, FirstAt: p.Time}
				trends[k] = t
				keys = append(keys, k)
			} else if v < t.Last {
				t.Reset = true
			}
			t.Last, t.LastAt = v, p.Time
		}
	}
	var out []CounterTrend
	for _, k := range keys {
		t := trends[k]
		t.Change = int64(t.Last) - int64(t.First)
		if t.Change != 0 || t.Reset {
			out = append(out, *t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Change != out[j].Change {
			return out[i].Change > out[j].Change
		}
		return out[i].Counter < out[j].Counter
	})
	return out
}

func sensorTrends(pts []Point) []SensorTrend {
	trends := map[string]*SensorTrend{}
	var keys []string
	add := func(name, unit string, v *float64, st model.Status, th *model.Thresholds) {
		if v == nil {
			return
		}
		t := trends[name]
		if t == nil {
			t = &SensorTrend{Sensor: name, Unit: unit, Min: *v, Max: *v}
			trends[name] = t
			keys = append(keys, name)
		}
		t.Min, t.Max, t.Last = min(t.Min, *v), max(t.Max, *v), *v
		t.Worst = model.Worst(t.Worst, st)
		t.Readings++
		if th != nil && th.UpperCrit != nil {
			t.Limit = model.F(*th.UpperCrit)
		}
	}
	for _, p := range pts {
		s := p.snap
		if c := s.CPU; c != nil {
			for _, x := range c.PackageTemps {
				add(x.Name, "°C", x.Value, x.Status, x.Thresholds)
			}
		}
		if m := s.Memory; m != nil {
			// a DIMM's status covers ECC too: take its temperature alert only
			tempSt := map[string]model.Status{}
			for _, f := range m.Findings {
				if strings.Contains(f.Code, "temp") {
					loc := strings.Fields(f.Subject + " ")[0]
					tempSt[loc] = model.Worst(tempSt[loc], f.Status)
				}
			}
			for _, d := range m.DIMMs {
				add(d.Locator, "°C", d.TempCelsius, tempSt[d.Locator], nil)
			}
		}
		if st := s.Storage; st != nil {
			for _, d := range st.Disks {
				add(d.Name, "°C", d.TempCelsius, check.Eval(d.TempCelsius, d.TempThreshold), d.TempThreshold)
			}
		}
		if a := s.Accel; a != nil {
			for _, list := range [][]model.Accelerator{a.GPUs, a.NPUs} {
				for _, x := range list {
					for _, t := range x.Temps {
						if !t.Shared {
							add(strings.ToUpper(x.Kind)+" "+x.PCIAddress+" "+t.Name, "°C", t.Value, t.Status, t.Thresholds)
						}
					}
				}
			}
		}
		if y := s.System; y != nil {
			for _, t := range y.Temps {
				add(t.Name, "°C", t.Value, t.Status, t.Thresholds)
			}
			for _, f := range y.Fans {
				add(f.Name, "rpm", f.Value, f.Status, nil)
			}
		}
	}
	var out []SensorTrend
	for _, k := range keys {
		out = append(out, *trends[k])
	}
	return out
}

func hardwareChanges(pts []Point) []Change {
	var out []Change
	var prev *Point
	for i := range pts {
		p := &pts[i]
		if len(p.snap.Inventory) == 0 {
			continue
		}
		if prev != nil {
			out = append(out, diffInventory(prev.snap, p.snap, p.Time)...)
			out = append(out, diffFirmware(prev.snap, p.snap, p.Time)...)
		}
		prev = p
	}
	return out
}

func compID(c model.Component) string { return c.Category + " " + c.Location }

func compDesc(c model.Component) string {
	return strings.TrimSpace(strings.TrimSpace(c.Vendor+" "+c.Model) + " " + map[bool]string{true: "S/N " + c.Serial, false: ""}[c.Serial != ""])
}

func diffInventory(a, b *model.Snapshot, at time.Time) []Change {
	cats := func(s *model.Snapshot) map[string]bool {
		m := map[string]bool{}
		for _, c := range s.Inventory {
			m[c.Category] = true
		}
		return m
	}
	ca, cb := cats(a), cats(b)
	before, after := map[string]model.Component{}, map[string]model.Component{}
	for _, c := range a.Inventory {
		before[compID(c)] = c
	}
	for _, c := range b.Inventory {
		after[compID(c)] = c
	}
	var out []Change
	for _, c := range b.Inventory {
		id := compID(c)
		old, ok := before[id]
		switch {
		case !ok && ca[c.Category]:
			out = append(out, Change{Time: at, Kind: "added", Component: id, After: compDesc(c)})
		case !ok:
		case old.Serial != "" && c.Serial != "" && old.Serial != c.Serial, old.Model != "" && c.Model != "" && old.Model != c.Model:
			out = append(out, Change{Time: at, Kind: "replaced", Component: id, Before: compDesc(old), After: compDesc(c)})
		case old.Firmware != "" && c.Firmware != "" && old.Firmware != c.Firmware:
			out = append(out, Change{Time: at, Kind: "firmware", Component: id, Before: old.Firmware, After: c.Firmware})
		}
	}
	for _, c := range a.Inventory {
		if _, ok := after[compID(c)]; !ok && cb[c.Category] {
			out = append(out, Change{Time: at, Kind: "removed", Component: compID(c), Before: compDesc(c)})
		}
	}
	return out
}

func diffFirmware(a, b *model.Snapshot, at time.Time) []Change {
	var out []Change
	cmp := func(what, x, y string) {
		if x != "" && y != "" && x != y {
			out = append(out, Change{Time: at, Kind: "firmware", Component: what, Before: x, After: y})
		}
	}
	if a.System != nil && b.System != nil {
		cmp("BIOS", strings.TrimSpace(a.System.BIOSVersion+" "+a.System.BIOSDate), strings.TrimSpace(b.System.BIOSVersion+" "+b.System.BIOSDate))
		cmp("BMC", a.System.BMCFirmware, b.System.BMCFirmware)
		cmp("kernel", a.System.Kernel, b.System.Kernel)
	}
	if a.CPU != nil && b.CPU != nil {
		cmp("CPU microcode", a.CPU.Microcode, b.CPU.Microcode)
	}
	return out
}

// HistoryMarkdown renders the histories with Mermaid charts.
func HistoryMarkdown(hs []History, skipped []Skipped) string {
	return HistoryMarkdownWith(hs, skipped, Options{})
}

// HistoryMarkdownWith renders the histories.
func HistoryMarkdownWith(hs []History, skipped []Skipped, o Options) string {
	var parts []string
	for _, h := range hs {
		parts = append(parts, historyMD(h, o))
	}
	out := strings.Join(parts, "\n---\n")
	if len(skipped) > 0 {
		var m md
		m.h(2, "Files not used")
		var rows [][]string
		for _, s := range skipped {
			rows = append(rows, []string{s.Path, s.Reason})
		}
		m.table([]string{"File", "Reason"}, rows)
		out += m.String()
	}
	return out
}

func historyMD(h History, o Options) string {
	var m md
	m.line("# %s · history", esc(h.Host))
	m.line("")
	worst := model.StatusOK
	if n := len(h.Snapshots); n > 0 {
		worst = h.Snapshots[n-1].Status
	}
	open := 0
	for _, a := range h.Alerts {
		if a.Open {
			open++
		}
	}
	m.p("**Latest status: %s** · %d open %s · %s to %s · %d %s · %d stress %s", status(worst), open, plural(open, "alert", "alerts"),
		when(h.From), when(h.To), len(h.Snapshots), plural(len(h.Snapshots), "snapshot", "snapshots"), len(h.Runs), plural(len(h.Runs), "run", "runs"))

	if n := len(h.Snapshots); n > 0 {
		healthSection(&m, h.Snapshots[n-1].snap, "Component health (latest, "+when(h.Snapshots[n-1].Time)+")")
	}
	if len(h.Snapshots) > 0 {
		m.h(2, "Status over time")
		var rows [][]string
		for _, p := range h.Snapshots {
			sum := p.Summary
			if r := []rune(sum); len(r) > 90 {
				sum = string(r[:89]) + "…"
			}
			rows = append(rows, []string{when(p.Time), status(p.Status), fmt.Sprint(p.Crit), fmt.Sprint(p.Warn), p.Kind, sum})
		}
		m.table([]string{"Time", "Status", ">✖", ">▲", "Kind", "Summary"}, rows)
	}

	m.h(2, "Alerts timeline")
	if len(h.Alerts) == 0 {
		m.p("● No alerts in this period.")
	} else {
		var rows [][]string
		for _, a := range h.Alerts {
			state := "open"
			if !a.Open {
				state = "resolved " + when(*a.ResolvedAt)
			}
			rows = append(rows, []string{status(a.Worst), a.Module, a.Subject, a.Message, when(a.FirstSeen), when(a.LastSeen), fmt.Sprint(a.Seen), state})
		}
		m.table([]string{"Worst", "Module", "Component", "Alert (last seen)", "First seen", "Last seen", ">Seen", "State"}, rows)
	}

	m.h(2, "Counter trends")
	if len(h.Counters) == 0 {
		m.p("● No error or event counter changed.")
	} else {
		var rows [][]string
		for _, c := range h.Counters {
			note := ""
			if c.Reset {
				note = "reset in between (reboot or driver reload)"
			}
			rows = append(rows, []string{c.Counter, fmt.Sprint(c.First), fmt.Sprint(c.Last), fmt.Sprintf("%+d", c.Change), dur(c.LastAt.Sub(c.FirstAt)), note})
		}
		m.table([]string{"Counter", ">First", ">Last", ">Change", ">Over", "Note"}, rows)
	}

	if len(h.Sensors) > 0 {
		m.h(2, "Sensor trends")
		var rows [][]string
		for _, s := range h.Sensors {
			lim := ""
			if s.Limit != nil {
				lim = fmt.Sprintf("%.0f %s", *s.Limit, s.Unit)
			}
			rows = append(rows, []string{s.Sensor, fmt.Sprintf("%.0f %s", s.Min, s.Unit), fmt.Sprintf("%.0f %s", s.Max, s.Unit),
				fmt.Sprintf("%.0f %s", s.Last, s.Unit), lim, status(s.Worst), fmt.Sprint(s.Readings)})
		}
		m.table([]string{"Sensor", ">Min", ">Max", ">Last", ">Limit", "Worst", ">Readings"}, rows)
	}

	m.h(2, "Hardware changes")
	if len(h.Changes) == 0 {
		m.p("● No parts added, removed or replaced, no firmware changes.")
	} else {
		var rows [][]string
		for _, c := range h.Changes {
			rows = append(rows, []string{when(c.Time), c.Kind, c.Component, c.Before, c.After})
		}
		m.table([]string{"Time", "Change", "Component", "Before", "After"}, rows)
	}

	m.h(2, "Stress tests")
	if len(h.Runs) == 0 {
		m.p("No stress results in these files.")
	} else {
		writeGantt(&m, h.Runs, o)
		var rows [][]string
		for _, r := range h.Runs {
			t := ""
			if r.MaxCPUTempC != nil {
				t = fmt.Sprintf("%.0f °C", *r.MaxCPUTempC)
			}
			rows = append(rows, []string{when(r.Started), r.Test, r.Engine, r.Duration, verdict(r.Verdict), fmt.Sprint(r.WrongResults), t, r.Reason})
		}
		m.table([]string{"Started", "Test", "Engine", ">Duration", "Verdict", ">Wrong results", ">Max CPU", "Reason"}, rows)
	}
	return m.String()
}
