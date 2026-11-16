package memory

import (
	"fmt"
	"strings"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/ipmi"
)

func alnum(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ipmiDIMMTemps adds BMC DIMM temperature sensors ("P1-DIMMA1 Temp",
// "DIMMG0_Temp", "P1-DIMMA~D Temp"). A sensor whose name contains a DIMM
// locator is also attached to that DIMM.
func ipmiDIMMTemps(out *model.Memory, sens []ipmi.Sensor, d check.Defaults) {
	for _, s := range sens {
		n := strings.ToLower(s.Name)
		if s.Kind != model.KindTemperature || s.Value == nil || !(strings.Contains(n, "dimm") || strings.Contains(n, "mem")) {
			continue
		}
		m := s.Model()
		if m.Thresholds == nil {
			m.Thresholds = check.TempThresholds(nil, dimmDefaults(d))
			m.Status = model.Worst(s.Status, check.Eval(m.Value, m.Thresholds))
		}
		out.Temps = append(out.Temps, m)
		key := alnum(s.Name)
		for i := range out.DIMMs {
			dm := &out.DIMMs[i]
			if dm.Populated && dm.Locator != "" && strings.Contains(key, alnum(dm.Locator)) {
				dm.TempCelsius = s.Value
			}
		}
		if m.Status >= model.StatusWarn {
			out.Header.Find(m.Status, "dimm_temp", s.Name, "ipmi", fmt.Sprintf("%s at %s", s.Name, check.FmtTemp(*s.Value)))
		}
	}
}

// selFindings reports asserted memory events from the BMC event log. They
// stay until the SEL is cleared, so the message includes the latest date.
func selFindings(out *model.Memory, sel *ipmi.SEL) {
	type agg struct {
		n      int
		latest ipmi.SELEntry
	}
	by := map[model.Status]*agg{}
	for _, e := range sel.Entries {
		if e.Category != "memory" || !e.Asserted || e.Severity < model.StatusWarn {
			continue
		}
		a := by[e.Severity]
		if a == nil {
			a = &agg{}
			by[e.Severity] = a
		}
		a.n++
		if e.Time.After(a.latest.Time) || a.latest.ID == "" {
			a.latest = e
		}
	}
	for _, st := range []model.Status{model.StatusCrit, model.StatusWarn} {
		a := by[st]
		if a == nil {
			continue
		}
		when := "time unknown"
		if !a.latest.Time.IsZero() {
			when = a.latest.Time.Format("2006-01-02 15:04")
		}
		code := "sel_memory_warning"
		if st == model.StatusCrit {
			code = "sel_memory_critical"
		}
		out.Header.Find(st, code, "sel", "ipmi_sel",
			fmt.Sprintf("%d memory %s in BMC SEL, latest %s: %s", a.n, plural(a.n, "event", "events"), when, strings.TrimSpace(a.latest.Event)))
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
