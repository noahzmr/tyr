package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/noahzmr/tyr/internal/model"
)

// powerTab (canvas board Power): the power supplies with their state and
// live readings, total draw and cap, redundancy, the draw over the
// session, and the chassis fans. Live data comes from the BMC's Redfish
// service; without it the SMBIOS power supply records are shown.
type powerTab struct {
	snap *model.Snapshot
	w, h int
	keys keyMap
	list *listView
}

func newPowerTab(p *prefs) *powerTab { return &powerTab{keys: newKeyMap(), list: newListView(p)} }

func (p *powerTab) Title() string          { return "Power" }
func (p *powerTab) Module() model.ModuleID { return model.ModPower }
func (p *powerTab) SetSize(w, h int)       { p.w, p.h = w, h }
func (p *powerTab) SetData(s *model.Snapshot) {
	p.snap = s
	p.arrange()
}

func (p *powerTab) Help() []key.Binding {
	return []key.Binding{p.keys.Up, p.keys.Enter, keyCopy}
}

func (p *powerTab) power() *model.Power {
	if p.snap == nil {
		return nil
	}
	return p.snap.Power
}

// supplies: the Power section's, else the SMBIOS inventory.
func (p *powerTab) supplies() []model.PowerSupply {
	if pw := p.power(); pw != nil && len(pw.Supplies) > 0 {
		return pw.Supplies
	}
	if p.snap == nil || p.snap.System == nil {
		return nil
	}
	var out []model.PowerSupply
	for _, x := range p.snap.System.PSUs {
		out = append(out, model.PowerSupply{Name: x.Label(), Present: x.Present, Health: x.StatusText, Manufacturer: x.Manufacturer,
			Model: x.Model, Serial: x.Serial, Firmware: x.Revision, CapacityWatts: x.MaxWatts, Status: x.Health()})
	}
	return out
}

func (p *powerTab) fans() []model.Sensor {
	if p.snap == nil || p.snap.System == nil {
		return nil
	}
	return p.snap.System.Fans
}

func (p *powerTab) arrange() {
	var items []listItem
	for _, x := range p.supplies() {
		items = append(items, listItem{key: x.Name, alt: []string{x.Serial, x.Model, x.Bay}, status: x.Status})
	}
	p.list.arrange(items)
}

func (p *powerTab) supply(name string) (model.PowerSupply, bool) {
	for _, x := range p.supplies() {
		if x.Name == name {
			return x, true
		}
	}
	return model.PowerSupply{}, false
}

func (p *powerTab) Focus(subject string) bool { p.arrange(); return p.list.focus(subject) }

func (p *powerTab) Update(msg tea.KeyPressMsg) tea.Cmd {
	if key.Matches(msg, keyNext) {
		p.list.nextProblem()
		return nil
	}
	p.list.handle(msg, p.keys, max(1, p.h/2))
	return nil
}

func (p *powerTab) Strip(t *Theme, w int) string {
	if p.snap == nil {
		return ""
	}
	if sec := p.power(); sec != nil {
		h := sec.Head()
		return statusStrip(t, h.Status, h.Summary, "", "", w)
	}
	return truncate(t.Unavail("Power", "not collected"), w)
}

func wattsOf(v *float64) string {
	if v == nil {
		return "–"
	}
	return fmt.Sprintf("%.0f W", *v)
}

func (p *powerTab) View(t *Theme) string {
	if p.snap == nil {
		return ""
	}
	w := p.w
	pw := p.power()
	live := pw != nil && pw.Source == "redfish"
	var out []string
	note := "SMBIOS · no live readings (BMC Redfish not readable)"
	if live {
		note = "BMC Redfish"
	}
	out = append(out, t.HeadingRight("Power supplies", "", note, w))
	if len(p.supplies()) == 0 {
		out = append(out, t.Unavail("power supplies", "no Redfish data and no SMBIOS type 39 records"))
	} else {
		cols := []column{{"PSU", -1}, {"Model", -1}, {"Serial", -1}, {"State", -1}, {">Input", 7}, {">Output", 8}, {">Rating", 8}, {"Firmware", 0}}
		if w < 100 {
			cols = []column{{"PSU", -1}, {"State", -1}, {">Input", 7}, {">Output", 8}, {"Serial", 0}}
		}
		for i := range p.list.shown {
			x, _ := p.supply(p.list.shown[i].key)
			state := x.Health
			if x.State != "" && x.State != "Enabled" {
				state = strings.TrimSpace(state + " " + x.State)
			}
			in := "–"
			if x.InputVolts != nil {
				in = fmt.Sprintf("%.0f V", *x.InputVolts)
				if *x.InputVolts == 0 {
					in = t.StatusText(model.StatusCrit, in)
				}
			}
			cells := map[string]string{"PSU": x.Name, "Model": strings.TrimSpace(x.Manufacturer + " " + x.Model), "Serial": orDash(x.Serial),
				"State": t.StatusText(x.Status, orDash(state)), ">Input": in, ">Output": wattsOf(x.OutputWatts), ">Rating": wattsOf(x.CapacityWatts),
				"Firmware": orDash(x.Firmware)}
			var r row
			for _, c := range cols {
				r.cells = append(r.cells, cells[c.title])
			}
			p.list.shown[i].row = r
		}
		out = append(out, strings.Split(p.list.render(t, cols, w, len(p.list.shown)+1), "\n")...)
	}

	// totals, redundancy, the draw over the session
	if live {
		draw := wattsOf(pw.ConsumedWatts)
		if pw.CapacityWatts != nil {
			draw += t.Dim.Render(" of " + wattsOf(pw.CapacityWatts) + " capacity")
		}
		avg := ""
		if pw.AvgWatts != nil {
			avg = wattsOf(pw.AvgWatts) + t.Dim.Render(fmt.Sprintf("  min %s · max %s · last %d min", wattsOf(pw.MinWatts), wattsOf(pw.MaxWatts), pw.IntervalMin))
		}
		limit := t.Sub.Render("none")
		if pw.LimitWatts != nil {
			limit = wattsOf(pw.LimitWatts)
		}
		rows := []kvPair{{"Total draw", draw}, {"Average", avg}, {"Power cap", limit}}
		for _, r := range pw.Redundancy {
			rows = append(rows, kvPair{"Redundancy", t.StatusText(r.Status, orDash(r.Health)) + t.Dim.Render(" · "+strings.TrimSpace(r.Name+" "+r.Mode))})
		}
		out = append(out, "", kvBlock(t, rows, ""))
		if hist := t.sess.history("power/draw"); len(hist) > 1 && p.h >= 30 {
			spec := chartSpec{Title: "Total power draw · W", Value: wattsOf(pw.ConsumedWatts), Series: hist, Color: t.Series[0],
				Window: time.Duration(len(hist)) * 5 * time.Second, YFmt: func(v float64) string { return fmt.Sprintf("%.0f", v) }}
			out = append(out, "", brailleChart(t, spec, w, 6))
		}
	}

	// fans
	fans := p.fans()
	if len(fans) > 0 {
		reporting := 0
		for _, f := range fans {
			if f.Value != nil {
				reporting++
			}
		}
		out = append(out, "", t.HeadingRight("Fans", "", fmt.Sprintf("%d of %d reporting", reporting, len(fans)), w))
		per := 3
		if w < 100 {
			per = 2
		}
		cw := w / per
		var line []string
		for i, f := range fans {
			cell := t.Dot(f.Status) + " " + pad(f.Name, 6) + " " + sensorGaugeLine(t, f, max(6, cw-22))
			line = append(line, padCell(truncate(cell, cw-2), cw, false))
			if len(line) == per || i == len(fans)-1 {
				out = append(out, strings.Join(line, ""))
				line = nil
			}
		}
	} else {
		out = append(out, "", t.Heading("Fans", "", w), t.Unavail("fans", "no fan sensors (IPMI or hwmon)"))
	}

	if sec := p.power(); sec != nil {
		out = append(out, "", findingsSection(t, "Power", sec.Head().Findings, sec.Head().Sources, max(3, p.h-len(out)-1), w))
	}
	return strings.Join(out, "\n")
}

// Detail: the selected supply's page.
func (p *powerTab) Detail(t *Theme, w int) (string, string) {
	it, ok := p.list.current()
	if !ok {
		return "", ""
	}
	x, _ := p.supply(it.key)
	doc := newDoc(t, w)
	doc.section("Identity", t.Badge(x.Status))
	doc.kv([2]string{"Name", x.Name}, [2]string{"Bay", x.Bay}, [2]string{"Manufacturer", x.Manufacturer}, [2]string{"Model", x.Model},
		[2]string{"Serial", t.Bold.Render(x.Serial)}, [2]string{"Part number", x.PartNumber}, [2]string{"Spare part", x.SparePart},
		[2]string{"Firmware", x.Firmware}, [2]string{"Type", x.Type}, [2]string{"Rating", wattsOf(x.CapacityWatts)})
	doc.section("State", "")
	in := ""
	if x.InputVolts != nil {
		in = fmt.Sprintf("%.0f V %s", *x.InputVolts, x.InputType)
		if *x.InputVolts == 0 {
			in = t.StatusText(model.StatusCrit, "0 V - no AC input: check the cable, the PDU outlet and its breaker")
		}
	}
	doc.kv([2]string{"Health", t.StatusText(x.Status, x.Health)}, [2]string{"State", x.State}, [2]string{"BMC state", x.Detail},
		[2]string{"Input", in}, [2]string{"Output", wattsOf(x.OutputWatts)})
	if pw := p.power(); pw == nil || pw.Source != "redfish" {
		doc.add(hang("   ", t.Sub.Render("No live readings: the BMC's Redfish service is not readable (see Findings · sources). SMBIOS only says present / OK / critical."), w-1))
	}
	if sec := p.power(); sec != nil {
		doc.findings(model.ModPower, relatedFindings(sec.Findings, x.Name))
	}
	return x.Name, doc.String()
}

func (p *powerTab) SelectedSerial() (string, string) {
	it, ok := p.list.current()
	if !ok {
		return "", ""
	}
	x, _ := p.supply(it.key)
	return x.Name, x.Serial
}

func (p *powerTab) PageInfo() (pageInfo, bool) {
	it, ok := p.list.current()
	if !ok {
		return pageInfo{}, false
	}
	x, _ := p.supply(it.key)
	return pageInfo{path: []string{x.Name}, status: x.Status, facts: nonEmpty(x.Model, sn(x.Serial))}, true
}
