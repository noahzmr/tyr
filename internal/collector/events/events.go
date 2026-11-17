// Package events collects one timeline of the BMC's event logs (HPE IML,
// iLO event log, SEL via Redfish; the IPMI SEL when Redfish cannot be read)
// and the hardware messages of the kernel log.
package events

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/model"
)

const maxEvents = 1500

// Collector is the events collector.
type Collector struct{}

// New returns an events collector.
func New() *Collector { return &Collector{} }

func (*Collector) ID() model.ModuleID { return model.ModEvents }

func (c *Collector) Collect(ctx context.Context, env *collector.Env, _ model.Section) (model.Section, error) {
	out := &model.Events{Logs: []model.EventLog{}, Entries: []model.Event{}}
	h := &out.Header

	// BMC: Redfish logs, else the IPMI SEL
	bmc := false
	if !env.IsVM() {
		logs, evs, st := env.RedfishLogs(ctx)
		st.Name = "redfish"
		h.Sources = append(h.Sources, st)
		if len(logs) > 0 {
			bmc = true
			out.Logs = append(out.Logs, logs...)
			out.Entries = append(out.Entries, evs...)
		}
		if !bmc {
			sel, sst := env.SEL(ctx)
			h.Sources = append(h.Sources, sst)
			if sel != nil {
				n := 0
				for _, e := range sel.Entries {
					sev := e.Severity
					if !e.Asserted && sev > model.StatusOK {
						sev = model.StatusOK // deasserted: the condition went away
					}
					msg := strings.TrimSpace(e.Event)
					if !e.Asserted {
						msg += " (deasserted)"
					}
					out.Entries = append(out.Entries, model.Event{Time: e.Time.UTC(), Log: "BMC SEL", Source: "ipmi", ID: e.ID, Severity: sev,
						Class: e.SensorType, Component: e.Sensor, Message: msg})
					n++
				}
				out.Logs = append(out.Logs, model.EventLog{Name: "BMC SEL", Source: "ipmi", Entries: n})
			}
		}
	} else {
		h.SetSource("bmc", model.AvailNotApplicable, "virtual machine ("+env.Virt+")")
	}

	// kernel log: the hardware messages Tyr classifies
	if klog, kst := env.KernelLog(ctx); klog != nil {
		n := 0
		for _, e := range klog.Entries {
			out.Entries = append(out.Entries, model.Event{Time: e.Time.UTC(), Log: "kernel log", Source: "kernel", Severity: e.Severity(),
				Class: string(e.Category), Component: e.Subject, Message: strings.TrimSpace(e.Message)})
			n++
		}
		out.Logs = append(out.Logs, model.EventLog{Name: "kernel log", Source: "kernel", Entries: n})
		h.Sources = append(h.Sources, kst)
	} else {
		h.Sources = append(h.Sources, kst)
	}

	sort.SliceStable(out.Entries, func(i, j int) bool { return out.Entries[i].Time.Before(out.Entries[j].Time) })
	if len(out.Entries) > maxEvents {
		out.Entries = out.Entries[len(out.Entries)-maxEvents:]
	}
	findings(h, out, env.Now())
	h.Summary = summary(out, env.Now())
	h.Rollup()
	return out, nil
}

// open: a BMC event of the last 30 days, warning or critical, not marked
// repaired.
func open(e model.Event, now time.Time) bool {
	return e.Source != "kernel" && e.Severity >= model.StatusWarn && e.Severity != model.StatusUnknown &&
		(e.Repaired == nil || !*e.Repaired) && now.Sub(last(e)) <= 30*24*time.Hour
}

func last(e model.Event) time.Time {
	if e.Updated.After(e.Time) {
		return e.Updated
	}
	return e.Time
}

// findings: open BMC events per log, grouped by message, worst first. The
// kernel's messages are already findings of their modules.
func findings(h *model.Header, out *model.Events, now time.Time) {
	type grp struct {
		log, msg string
		worst    model.Status
		n        int
		last     time.Time
	}
	by := map[string]*grp{}
	var order []*grp
	for _, e := range out.Entries {
		if !open(e, now) {
			continue
		}
		k := e.Log + "|" + e.Message
		g := by[k]
		if g == nil {
			g = &grp{log: e.Log, msg: e.Message}
			by[k] = g
			order = append(order, g)
		}
		g.worst = model.Worst(g.worst, e.Severity)
		g.n += max(1, e.Count)
		if l := last(e); l.After(g.last) {
			g.last = l
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].worst != order[j].worst {
			return order[i].worst > order[j].worst
		}
		return order[i].last.After(order[j].last)
	})
	for i, g := range order {
		if i == 12 {
			h.Find(model.StatusWarn, "bmc_events", "bmc", "redfish", fmt.Sprintf("… %d more open BMC events (Events tab)", len(order)-12))
			break
		}
		times := ""
		if g.n > 1 {
			times = fmt.Sprintf(" (%d×)", g.n)
		}
		h.Find(g.worst, "bmc_events", g.log, "bmc", fmt.Sprintf("%s %s: %s%s", g.log, g.last.Local().Format("2006-01-02 15:04"), g.msg, times))
	}
}

func summary(out *model.Events, now time.Time) string {
	var parts []string
	for _, l := range out.Logs {
		parts = append(parts, fmt.Sprintf("%s %d", shortLog(l.Name), l.Entries))
	}
	crit, warn := 0, 0
	for _, e := range out.Entries {
		if open(e, now) {
			if e.Severity == model.StatusCrit {
				crit++
			} else {
				warn++
			}
		}
	}
	s := strings.Join(parts, " · ")
	if crit+warn > 0 {
		s += fmt.Sprintf(" · %d open (30 days)", crit+warn)
	}
	if s == "" {
		return "no event log readable"
	}
	return s
}

func shortLog(name string) string {
	if i := strings.Index(name, " ("); i > 0 {
		return name[:i]
	}
	return name
}
