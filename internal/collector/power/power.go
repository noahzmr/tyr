// Package power collects the server's power: supplies with their state and
// readings, total draw and cap, redundancy. From the BMC's Redfish service
// when it can be read, else the SMBIOS power supply records plus the
// BMC event log's power supply events.
package power

import (
	"context"
	"fmt"
	"strings"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/model"
)

// Collector is the power collector.
type Collector struct{}

// New returns a power collector.
func New() *Collector { return &Collector{} }

func (*Collector) ID() model.ModuleID { return model.ModPower }

func (c *Collector) Collect(ctx context.Context, env *collector.Env, _ model.Section) (model.Section, error) {
	out := &model.Power{Supplies: []model.PowerSupply{}}
	h := &out.Header
	rp, st := env.RedfishPower(ctx)
	st.Name = "redfish"
	h.Sources = append(h.Sources, st)
	if rp != nil {
		*out = *rp // the cache's copy stays untouched: slices are copied below
		out.Header = model.Header{Sources: []model.SourceState{st}}
		h = &out.Header
		out.Supplies = append([]model.PowerSupply(nil), rp.Supplies...)
		out.Redundancy = append([]model.Redundancy(nil), rp.Redundancy...)
		out.Source = "redfish"
	} else if !env.IsVM() {
		out.Source = "smbios"
		tables, dst := env.DMI(ctx)
		h.Sources = append(h.Sources, dst)
		if tables != nil {
			for _, p := range tables.PowerSupplies() {
				f := model.PSUFRU{Location: p.Location, Name: p.Name, StatusText: p.Status, Present: p.Present}
				out.Supplies = append(out.Supplies, model.PowerSupply{Name: f.Label(), Present: p.Present, Health: p.Status,
					Manufacturer: p.Manufacturer, Model: p.Model, Serial: p.Serial, Firmware: p.Revision, CapacityWatts: p.MaxWatts, Status: f.Health()})
			}
		}
	}
	findings(ctx, env, h, out)
	h.Summary = summary(out)
	h.Rollup()
	return out, nil
}

func summary(p *model.Power) string {
	if len(p.Supplies) == 0 {
		return "no power supply data"
	}
	ok := 0
	for _, s := range p.Supplies {
		if s.Status == model.StatusOK {
			ok++
		}
	}
	s := fmt.Sprintf("%d/%d PSUs OK", ok, len(p.Supplies))
	if p.ConsumedWatts != nil {
		s += fmt.Sprintf(" · %.0f W", *p.ConsumedWatts)
		if p.CapacityWatts != nil && *p.CapacityWatts > 0 {
			s += fmt.Sprintf(" of %.0f W", *p.CapacityWatts)
		}
	}
	return s
}

// findings: supplies not OK (with what the BMC says: no AC input or a
// failed unit), and redundancy lost.
func findings(ctx context.Context, env *collector.Env, h *model.Header, out *model.Power) {
	var selEvents []string
	if out.Source != "redfish" && !env.IsVM() {
		if sel, _ := env.SEL(ctx); sel != nil {
			for _, e := range sel.Entries {
				if e.Category == "psu" && e.Asserted && e.Severity >= model.StatusWarn {
					when := ""
					if !e.Time.IsZero() {
						when = e.Time.Local().Format("2006-01-02 15:04") + " "
					}
					selEvents = append(selEvents, when+strings.TrimSpace(e.Sensor+": "+e.Event))
				}
			}
		}
	}
	for _, s := range out.Supplies {
		if s.Status < model.StatusWarn {
			continue
		}
		var facts []string
		if s.Serial != "" {
			facts = append(facts, "S/N "+s.Serial)
		}
		if s.Health != "" {
			facts = append(facts, s.Health)
		}
		if s.Detail != "" && !strings.EqualFold(s.Detail, "ok") {
			facts = append(facts, "BMC state "+s.Detail)
		}
		if s.InputVolts != nil {
			v := fmt.Sprintf("input %.0f V", *s.InputVolts)
			if *s.InputVolts == 0 {
				v += " (no AC input: cable, PDU or breaker)"
			}
			facts = append(facts, v)
		}
		if s.OutputWatts != nil {
			facts = append(facts, fmt.Sprintf("output %.0f W", *s.OutputWatts))
		}
		msg := s.Name + ": " + strings.Join(facts, " · ")
		switch {
		case len(selEvents) > 0:
			msg += " · latest BMC power supply event: " + selEvents[len(selEvents)-1]
		case out.Source == "smbios":
			msg += " (SMBIOS: failed, or no AC input - the BMC tells which)"
		}
		h.Find(s.Status, "psu_status", s.Name, out.Source, msg)
	}
	for _, r := range out.Redundancy {
		if r.Status >= model.StatusWarn {
			h.Find(r.Status, "psu_redundancy", r.Name, out.Source, fmt.Sprintf("%s (%s): %s - one more failure and the server loses power", r.Name, orDash(r.Mode), r.Health))
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
