package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/noahzmr/tyr/internal/model"
)

// powerTab (canvas board Power). The Power collector (PSU sensors, RAPL,
// redundancy) does not exist yet: the tab shows the PSU inventory from
// SMBIOS and the fans the System collector reads, and every missing
// reading as ○ unavailable with the reason.
type powerTab struct {
	snap *model.Snapshot
	w, h int
	keys keyMap
	list *listView
}

const powerMissing = "collector not implemented yet"

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

func (p *powerTab) psus() []model.PSUFRU {
	if p.snap == nil || p.snap.System == nil {
		return nil
	}
	return p.snap.System.PSUs
}

func (p *powerTab) fans() []model.Sensor {
	if p.snap == nil || p.snap.System == nil {
		return nil
	}
	return p.snap.System.Fans
}

// psuStatus reads SMBIOS type 39 status text ("Present, OK").
func psuStatus(x model.PSUFRU) model.Status {
	low := strings.ToLower(x.StatusText)
	switch {
	case !x.Present:
		return model.StatusUnknown
	case strings.Contains(low, "critical") || strings.Contains(low, "fail") || strings.Contains(low, "lost"):
		return model.StatusCrit
	case strings.Contains(low, "non-critical") || strings.Contains(low, "degraded"):
		return model.StatusWarn
	case strings.Contains(low, "ok"):
		return model.StatusOK
	}
	return model.StatusUnknown
}

func (p *powerTab) arrange() {
	var items []listItem
	for _, x := range p.psus() {
		items = append(items, listItem{key: x.Location, alt: []string{x.Name, x.Serial}, status: psuStatus(x)})
	}
	p.list.arrange(items)
}

func (p *powerTab) psu(loc string) (model.PSUFRU, bool) {
	for _, x := range p.psus() {
		if x.Location == loc {
			return x, true
		}
	}
	return model.PSUFRU{}, false
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
	if sec := p.snap.Power; sec != nil { // once the collector exists
		h := sec.Head()
		return statusStrip(t, h.Status, h.Summary, "", "", w)
	}
	var facts []string
	if n := len(p.psus()); n > 0 {
		total := 0.0
		for _, x := range p.psus() {
			if x.MaxWatts != nil {
				total += *x.MaxWatts
			}
		}
		f := countLabel(n, "PSU", "PSUs")
		if total > 0 {
			f += fmt.Sprintf(" · %.0f W rated", total)
		}
		facts = append(facts, f)
	}
	if n := len(p.fans()); n > 0 {
		facts = append(facts, countLabel(n, "fan", "fans"))
	}
	s := t.Unavail("Power", powerMissing)
	if len(facts) > 0 {
		s += t.Dim.Render(" · inventory only: " + strings.Join(facts, " · "))
	}
	return truncate(s, w)
}

func (p *powerTab) View(t *Theme) string {
	if p.snap == nil {
		return ""
	}
	w := p.w
	var out []string
	// power supplies
	out = append(out, t.HeadingRight("Power supplies", "", "SMBIOS type 39 · no live readings", w))
	if len(p.psus()) == 0 {
		out = append(out, t.Unavail("PSU inventory", "no SMBIOS type 39 records (needs root, or the board has none)"))
	} else {
		cols := []column{{"PSU", -1}, {"Model", -1}, {"Serial", -1}, {"State", -1}, {">Rating", 8}, {"Input · output · °C", 0}}
		if w < 100 {
			cols = []column{{"PSU", -1}, {"Model", 0}, {"Serial", -1}, {">Rating", 8}}
		}
		for i := range p.list.shown {
			x, _ := p.psu(p.list.shown[i].key)
			rating := "–"
			if x.MaxWatts != nil {
				rating = fmt.Sprintf("%.0f W", *x.MaxWatts)
			}
			cells := map[string]string{"PSU": x.Location, "Model": strings.TrimSpace(x.Manufacturer + " " + x.Model),
				"Serial": orDash(x.Serial), "State": t.StatusText(psuStatus(x), orDash(x.StatusText)), ">Rating": rating,
				"Input · output · °C": t.Sub.Render("○ needs BMC PSU sensors"), "Readings": t.Sub.Render("○ needs BMC")}
			var r row
			for _, c := range cols {
				r.cells = append(r.cells, cells[c.title])
			}
			p.list.shown[i].row = r
		}
		out = append(out, strings.Split(p.list.render(t, cols, w, len(p.list.shown)+1), "\n")...)
	}

	// totals and rails
	totals := kvBlock(t, []kvPair{
		{"Redundancy", t.Unavail("", "needs PSU status sensors")},
		{"Total draw", t.Unavail("", "no BMC power reading")},
		{"CPU RAPL", t.Unavail("", "no RAPL collector yet")},
		{"DRAM RAPL", t.Unavail("", "no RAPL collector yet")},
	}, "")
	rails := t.Dim.Render("PSU rails") + "\n" + t.Unavail("", "needs BMC voltage sensors")
	out = append(out, "")
	if w >= 100 {
		out = append(out, strings.Split(kvColumns(w, totals, rails), "\n")...)
	} else {
		out = append(out, strings.Split(totals, "\n")...)
	}
	out = append(out, "", t.Bold.Render("Total power draw · W")+"  "+t.Unavail("", "no power readings to graph"))

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

	// findings
	if sec := p.snap.Power; sec != nil {
		out = append(out, "", findingsSection(t, "Power", sec.Head().Findings, sec.Head().Sources, max(3, p.h-len(out)-1), w))
	} else {
		out = append(out, "", t.Heading("Findings · Power", "", w), t.Unavail("no findings", "Power "+powerMissing))
	}
	return strings.Join(out, "\n")
}

// Detail: the selected PSU's page.
func (p *powerTab) Detail(t *Theme, w int) (string, string) {
	it, ok := p.list.current()
	if !ok {
		return "", ""
	}
	x, _ := p.psu(it.key)
	doc := newDoc(t, w)
	doc.section("Identity", t.Badge(psuStatus(x)))
	rating := ""
	if x.MaxWatts != nil {
		rating = fmt.Sprintf("%.0f W", *x.MaxWatts)
	}
	doc.kv([2]string{"Location", x.Location}, [2]string{"Name", x.Name}, [2]string{"Manufacturer", x.Manufacturer},
		[2]string{"Model", x.Model}, [2]string{"Serial", x.Serial}, [2]string{"Revision", x.Revision},
		[2]string{"Rating", rating}, [2]string{"State", x.StatusText})
	doc.section("Readings", "")
	doc.add(t.Unavail("input, output, rails, temperature", "Power "+powerMissing+" (BMC PSU sensors)"))
	return "PSU " + x.Location, doc.String()
}

func (p *powerTab) SelectedSerial() (string, string) {
	it, ok := p.list.current()
	if !ok {
		return "", ""
	}
	x, _ := p.psu(it.key)
	return x.Location, x.Serial
}

func (p *powerTab) PageInfo() (pageInfo, bool) {
	it, ok := p.list.current()
	if !ok {
		return pageInfo{}, false
	}
	x, _ := p.psu(it.key)
	return pageInfo{path: []string{x.Location}, status: psuStatus(x), facts: nonEmpty(x.Model, sn(x.Serial))}, true
}
