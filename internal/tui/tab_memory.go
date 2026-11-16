package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
)

// memoryTab shows usage, ECC state and every DIMM slot as a table or as a
// physical slot map (sockets × channels). Enter opens a DIMM's page.
type memoryTab struct {
	mem       *model.Memory
	w, h      int
	list      *listView
	view      int // 0 table, 1 slot map
	showEmpty bool
	keys      keyMap
	empty     key.Binding
	switchV   key.Binding
}

func newMemoryTab(p *prefs) *memoryTab {
	return &memoryTab{list: newListView(p), keys: newKeyMap(),
		empty:   key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "empty slots")),
		switchV: key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "view"))}
}

func (m *memoryTab) Title() string          { return "Memory" }
func (m *memoryTab) Module() model.ModuleID { return model.ModMemory }
func (m *memoryTab) SetData(s *model.Snapshot) {
	m.mem = s.Memory
	m.items()
}
func (m *memoryTab) SetSize(w, h int) { m.w, m.h = w, h }
func (m *memoryTab) Help() []key.Binding {
	return []key.Binding{m.keys.Up, m.keys.Enter, m.switchV, m.empty, keyNext}
}

// items arranges DIMMs (empty slots only when requested or in the slot map).
func (m *memoryTab) items() []listItem {
	if m.mem == nil {
		return nil
	}
	var out []listItem
	for _, d := range m.mem.DIMMs {
		if !d.Populated && !m.showEmpty && m.view == 0 {
			continue
		}
		out = append(out, listItem{key: d.Locator, alt: []string{d.EDACLabel}, status: d.Status})
	}
	if m.view == 1 { // slot map keeps physical order
		m.list.noSort = true
	} else {
		m.list.noSort = false
	}
	return m.list.arrange(out)
}

func (m *memoryTab) dimm(locator string) (model.DIMM, bool) {
	for _, d := range m.mem.DIMMs {
		if d.Locator == locator {
			return d, true
		}
	}
	return model.DIMM{}, false
}

func (m *memoryTab) Focus(subject string) bool {
	m.items()
	return m.list.focus(subject)
}

func (m *memoryTab) Update(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.empty):
		m.showEmpty = !m.showEmpty
	case key.Matches(msg, m.switchV):
		m.view = (m.view + 1) % 2
	case m.view == 1 && key.Matches(msg, m.keys.Left):
		m.list.setSel(m.list.sel - 1)
	case m.view == 1 && key.Matches(msg, m.keys.Right):
		m.list.setSel(m.list.sel + 1)
	default:
		m.list.handle(msg, m.keys, max(1, m.h/2))
	}
	m.items()
	return nil
}

func (m *memoryTab) View(t *Theme) string {
	x := m.mem
	if x == nil {
		return t.Dim.Render("memory data not collected")
	}
	m.items()
	// summary: usage bar, then ECC totals, NUMA nodes and poisoned memory
	used := 0.0
	if x.TotalBytes > 0 {
		used = float64(x.UsedBytes) / float64(x.TotalBytes) * 100
	}
	ust := model.StatusOK
	if used >= 90 {
		ust = model.StatusWarn
	}
	barW := 30
	if m.w < 100 {
		barW = 16
	}
	usage := t.Dim.Render("Used ") + memBar(t, used, ust, barW) + " " + t.Bold.Render(bytesGB(x.UsedBytes)) + t.Dim.Render(" of "+bytesGB(x.TotalBytes)+" · ") +
		bytesGB(x.AvailableBytes) + t.Dim.Render(" available · swap "+bytesGB(x.SwapUsedBytes)+" / "+bytesGB(x.SwapTotalBytes))
	ecc := t.Dim.Render("ECC  ")
	if x.CorrectableErrors != nil && x.UncorrectableErrors != nil {
		ce, ue := *x.CorrectableErrors, *x.UncorrectableErrors
		ces, ues := t.Dot(model.StatusOK)+" "+fmt.Sprintf("%d corrected", ce), t.Dot(model.StatusOK)+" "+fmt.Sprintf("%d uncorrected", ue)
		if ce > 0 {
			ces = t.Dot(model.StatusWarn) + " " + t.StatusText(model.StatusWarn, fmt.Sprintf("%d corrected", ce))
		}
		if ue > 0 {
			ues = t.Dot(model.StatusCrit) + " " + t.StatusBold(model.StatusCrit, fmt.Sprintf("%d uncorrected", ue))
		}
		ecc += ces + "  " + ues
	} else {
		ecc += t.Sub.Render("n/a (no EDAC)")
	}
	for _, n := range x.NUMANodes {
		ecc += t.Dim.Render(fmt.Sprintf("  node %d ", n.ID)) + bytesGB(n.TotalBytes)
	}
	if x.HardwareCorruptedBytes > 0 {
		ecc += t.Dim.Render("  poisoned ") + t.StatusBold(model.StatusCrit, bytesGB(x.HardwareCorruptedBytes))
	} else {
		ecc += t.Dim.Render("  poisoned ") + "0"
	}
	info := truncate(usage, m.w) + "\n" + truncate(ecc, m.w)
	if len(x.DIMMs) == 0 {
		body := t.Unavail("DIMM inventory", "needs root · rerun with sudo tyr")
		top := info + "\n\n" + body
		return top + "\n\n" + findingsSection(t, "Memory", x.Findings, x.Sources, m.h-lipgloss.Height(top)-1, m.w)
	}
	side := m.w >= 100
	iw := 0
	if side {
		iw = 36
		if m.w >= 150 {
			iw = 48
		}
	}
	mainW := m.w - iw
	findN := min(countNonOK(x.Findings), 6) + 2
	avail := max(5, m.h-lipgloss.Height(info)-1-findN)
	var main string
	if m.view == 1 {
		main = m.slotMap(t, mainW)
	} else {
		main = m.table(t, mainW, avail)
	}
	mainH := lipgloss.Height(main)
	var block string
	if side {
		block = besideInspector(main, mainW, m.inspector(t, iw, max(mainH, min(avail, 20))), max(mainH, min(avail, 20)))
	} else {
		block = main + "\n\n" + m.detail(t)
	}
	top := info + "\n\n" + block
	return top + "\n\n" + findingsSection(t, "Memory", x.Findings, x.Sources, m.h-lipgloss.Height(top)-1, m.w)
}

// memBar is the █/░ usage bar (the canvas uses blocks for usage, gauges
// for values against limits).
func memBar(t *Theme, pct float64, st model.Status, w int) string {
	n := min(w, max(0, int(pct/100*float64(w)+0.5)))
	return t.StatusText(st, strings.Repeat("█", n)) + lipgloss.NewStyle().Foreground(t.Track).Render(strings.Repeat("░", w-n))
}

// inspector is the right-hand pane for the selected DIMM.
func (m *memoryTab) inspector(t *Theme, w, h int) string {
	it, ok := m.list.current()
	if !ok {
		return inspector(t, model.StatusUnknown, "no DIMM", "", w, h)
	}
	d, _ := m.dimm(it.key)
	if !d.Populated {
		return inspector(t, model.StatusOK, d.Locator, t.Sub.Render("empty slot · "+orDash(d.BankLocator)), w, h)
	}
	loc := d.Locator
	if sock, ch := slotGroup(d); sock != "" {
		loc = sock + " · channel " + ch
	}
	speed := optI(d.ConfiguredSpeedMTs, "%d MT/s")
	if d.SpeedMTs != nil {
		speed += t.Sub.Render(fmt.Sprintf(" (rated %d)", *d.SpeedMTs))
	}
	lines := []string{kvBlock(t, []kvPair{{"Location", loc}, {"Size", strings.TrimSpace(bytesGB(d.SizeBytes) + " " + d.Type + " " + d.FormFactor)},
		{"Speed", speed}, {"Vendor", d.Manufacturer}, {"Part", d.PartNumber}, {"Serial", t.Bold.Render(d.Serial)}, {"Asset tag", d.AssetTag}}, "not set")}
	if d.TempCelsius != nil {
		lines = append(lines, "", t.Dim.Render("Temp      ")+sensorGaugeLine(t, model.Sensor{Value: d.TempCelsius, Unit: model.UnitCelsius,
			Thresholds: &model.Thresholds{UpperWarn: f64p(80), UpperCrit: f64p(90), Origin: "default"}}, 12))
	}
	for _, mc := range m.mem.EDAC {
		for _, e := range mc.DIMMs {
			if e.Locator == d.Locator || (d.EDACLabel != "" && e.Label == d.EDACLabel) {
				lines = append(lines, "", t.Dim.Render("EDAC "+mc.Name+" "+e.Name))
				ce := fmt.Sprint(e.CorrectableErrors)
				if e.CorrectableErrors > 0 {
					ce = t.StatusText(model.StatusWarn, ce)
				}
				ue := fmt.Sprint(e.UncorrectableErrors)
				if e.UncorrectableErrors > 0 {
					ue = t.StatusBold(model.StatusCrit, ue)
				}
				lines = append(lines, t.Dim.Render("corrected   ")+ce, t.Dim.Render("uncorrected ")+ue)
			}
		}
	}
	if fs := relatedFindings(m.mem.Findings, d.Locator, d.EDACLabel); len(fs) > 0 {
		lines = append(lines, "")
		for _, f := range sortFindings(fs) {
			if f.Status != model.StatusOK {
				lines = append(lines, t.Dot(f.Status)+" "+f.Message)
			}
		}
	}
	lines = append(lines, "", t.Sub.Render("enter full page"))
	return inspector(t, d.Status, d.Locator, strings.Join(lines, "\n"), w, h)
}

func f64p(v float64) *float64 { return &v }

func (m *memoryTab) table(t *Theme, w, maxRows int) string {
	cols := []column{{"Slot", -1}, {">Size", 6}, {"Type", 4}, {">Speed", 9}, {"Part number", -1}, {">CE", 4}, {">UE", 3}, {">°C", 3}, {"Serial", -1}}
	switch {
	case m.w < 100:
		cols = []column{{"Slot", -1}, {">Size", 6}, {">Speed", 9}, {">CE", 4}, {">UE", 3}, {">°C", 3}, {"Serial", -1}}
	case m.w >= 150:
		cols = append(cols, column{">Rank", 4}, column{"Vendor", -1}, column{"Asset tag", -1})
	}
	for i := range m.list.shown {
		it := &m.list.shown[i]
		d, _ := m.dimm(it.key)
		speed := "–"
		if d.ConfiguredSpeedMTs != nil {
			speed = fmt.Sprintf("%d", *d.ConfiguredSpeedMTs)
			if d.SpeedMTs != nil {
				speed += fmt.Sprintf("/%d", *d.SpeedMTs)
				if *d.SpeedMTs != *d.ConfiguredSpeedMTs {
					speed = t.StatusBold(model.StatusWarn, speed) // configured below rated
				}
			}
		}
		if !d.Populated {
			it.row = row{cells: []string{d.Locator, "empty"}, dim: true}
			continue
		}
		num := func(v *uint64, bad model.Status) string {
			if v == nil {
				return t.Sub.Render("–")
			}
			if *v > 0 {
				return t.StatusBold(bad, fmt.Sprint(*v))
			}
			return fmt.Sprint(*v)
		}
		temp := t.Sub.Render("–")
		if d.TempCelsius != nil {
			temp = fmt.Sprintf("%.0f", *d.TempCelsius)
			if *d.TempCelsius >= 80 {
				temp = t.StatusText(model.StatusWarn, temp)
			}
		}
		cells := map[string]string{"Slot": d.Locator, ">Size": bytesGB(d.SizeBytes), "Type": orDash(d.Type), ">Speed": speed,
			"Part number": orDash(d.PartNumber), ">CE": num(d.CorrectableErrors, model.StatusWarn), ">UE": num(d.UncorrectableErrors, model.StatusCrit),
			">°C": temp, "Serial": orDash(d.Serial), ">Rank": optI(d.Rank, "%d"), "Vendor": orDash(d.Manufacturer), "Asset tag": orDash(d.AssetTag)}
		var r row
		for _, c := range cols {
			r.cells = append(r.cells, cells[c.title])
		}
		it.row = r
	}
	out := m.list.render(t, cols, w, maxRows)
	if hidden := m.hiddenEmpty(); hidden > 0 {
		out += "\n" + t.Sub.Render(fmt.Sprintf("   + %d empty slots hidden · a show", hidden))
	}
	return out
}

// slotGroup splits "P1-A" style channels into socket and channel.
func slotGroup(d model.DIMM) (socket, channel string) {
	c := d.Channel
	if c == "" {
		return "", ""
	}
	if i := strings.LastIndexAny(c, "-_ "); i > 0 {
		return c[:i], c[i+1:]
	}
	return "", c
}

func (m *memoryTab) hiddenEmpty() int {
	if m.showEmpty || m.view == 1 {
		return 0
	}
	n := 0
	for _, d := range m.mem.DIMMs {
		if !d.Populated {
			n++
		}
	}
	return n
}

// slotMap is the physical map: one block per CPU socket, one row per
// channel, slots as boxes ("▕● 64G     ▏" populated, "▕┄ empty ┄ ▏"),
// the reason for a problem to the right of its row.
func (m *memoryTab) slotMap(t *Theme, w int) string {
	type ch struct {
		name  string
		slots []model.DIMM
	}
	sockets := map[string][]*ch{}
	var order []string
	for _, d := range m.mem.DIMMs {
		so, c := slotGroup(d)
		if _, ok := sockets[so]; !ok {
			order = append(order, so)
		}
		var cur *ch
		for _, x := range sockets[so] {
			if x.name == c {
				cur = x
			}
		}
		if cur == nil {
			cur = &ch{name: c}
			sockets[so] = append(sockets[so], cur)
		}
		cur.slots = append(cur.slots, d)
	}
	sel, _ := m.list.current()
	nodeOf := map[string]string{}
	for i, n := range m.mem.NUMANodes {
		if i < len(order) {
			nodeOf[order[i]] = fmt.Sprintf("node %d · %s", n.ID, bytesGB(n.TotalBytes))
		}
	}
	box := func(d model.DIMM, selected bool) string {
		inner := "┄ empty ┄"
		st := t.Sub
		if d.Populated {
			inner = statusSym(d.Status) + " " + strings.TrimSuffix(strings.ReplaceAll(bytesGB(d.SizeBytes), " ", ""), "B")
			st = lipgloss.NewStyle().Foreground(t.StatusColor(d.Status))
			if d.Status >= model.StatusWarn {
				st = st.Bold(true)
			}
		}
		cell := st.Render(pad(inner, 12))
		edge := t.Sub
		if selected {
			edge = lipgloss.NewStyle().Foreground(t.Accent).Bold(true)
			cell = withBg(cell, t.SelBg)
		}
		return edge.Render("▕") + cell + edge.Render("▏")
	}
	var lines []string
	for _, so := range order {
		chs := sockets[so]
		sort.SliceStable(chs, func(i, j int) bool { return chs[i].name < chs[j].name })
		label := so
		if label == "" {
			label = "slots"
		}
		lines = append(lines, t.HeadingRight(label, "", nodeOf[so], w-2))
		maxSlots := 0
		for _, c := range chs {
			maxSlots = max(maxSlots, len(c.slots))
		}
		hdr := strings.Repeat(" ", 7)
		for i := 1; i <= maxSlots; i++ {
			hdr += pad(fmt.Sprintf("   slot %d", i), 14)
		}
		lines = append(lines, t.Sub.Render(hdr))
		for _, c := range chs {
			line := " ch " + pad(c.name, 2) + " "
			mark := " "
			var reason []string
			for _, d := range c.slots {
				isSel := d.Locator == sel.key
				if isSel {
					mark = "▌"
				}
				line += box(d, isSel)
				if d.Populated && d.Status >= model.StatusWarn {
					if r := why(m.mem.Findings, d.Locator); r != "" {
						reason = append(reason, r)
					}
				}
			}
			if mark == "▌" {
				line = lipgloss.NewStyle().Foreground(t.Accent).Render("▌") + line[1:]
			}
			if len(reason) > 0 {
				line += "  " + t.Dim.Render("◂ "+strings.Join(reason, " · "))
			}
			lines = append(lines, truncate(line, w-1))
		}
		lines = append(lines, "")
	}
	lines = append(lines, t.Sub.Render("● ok  ▲ warn  ✖ crit  ┄ empty ┄ empty slot · ←→ select · a hide empty · enter DIMM page"))
	return strings.Join(lines, "\n")
}

func (m *memoryTab) detail(t *Theme) string {
	it, ok := m.list.current()
	if !ok {
		return t.Dim.Render("no populated DIMMs (a: show empty slots)")
	}
	d, _ := m.dimm(it.key)
	if !d.Populated {
		return t.Section.Render(d.Locator) + t.Dim.Render(" empty slot · "+orDash(d.BankLocator))
	}
	ecc := "–"
	if d.ECC != nil {
		ecc = map[bool]string{true: "ECC", false: "no ECC"}[*d.ECC]
	}
	s := fmt.Sprintf("%s %s · %s · rank %s · %s · S/N %s", d.Manufacturer, d.PartNumber, orDash(d.FormFactor), optI(d.Rank, "%d"), ecc, orDash(d.Serial))
	s2 := fmt.Sprintf("bank %s · channel %s · EDAC %s", orDash(d.BankLocator), orDash(d.Channel), orDash(d.EDACLabel))
	if d.AssetTag != "" {
		s2 += " · asset " + d.AssetTag
	}
	return t.Section.Render(d.Locator) + " " + s + "\n" + t.Dim.Render("  "+s2)
}

// Detail is the full page of the selected DIMM.
func (m *memoryTab) Detail(t *Theme, w int) (string, string) {
	it, ok := m.list.current()
	if !ok || m.mem == nil {
		return "", ""
	}
	d, _ := m.dimm(it.key)
	doc := newDoc(t, w)
	doc.section("Identity", t.Badge(d.Status))
	if !d.Populated {
		doc.kv([2]string{"State", "empty slot"}, [2]string{"Bank", d.BankLocator}, [2]string{"Channel", d.Channel})
		return "DIMM " + d.Locator, doc.String()
	}
	ecc := ""
	if d.ECC != nil {
		ecc = map[bool]string{true: "yes", false: "no"}[*d.ECC]
	}
	speed := ""
	if d.ConfiguredSpeedMTs != nil {
		speed = fmt.Sprintf("%d MT/s configured", *d.ConfiguredSpeedMTs)
	}
	if d.SpeedMTs != nil {
		speed += fmt.Sprintf(" · %d MT/s rated", *d.SpeedMTs)
	}
	doc.kv([2]string{"Manufacturer", d.Manufacturer}, [2]string{"Part number", d.PartNumber}, [2]string{"Serial", d.Serial},
		[2]string{"Asset tag", d.AssetTag}, [2]string{"Size", bytesGB(d.SizeBytes)}, [2]string{"Type", d.Type},
		[2]string{"Form factor", d.FormFactor}, [2]string{"Speed", speed}, [2]string{"Rank", optI(d.Rank, "%d")},
		[2]string{"ECC", ecc}, [2]string{"Bank locator", d.BankLocator}, [2]string{"Channel", d.Channel}, [2]string{"EDAC label", d.EDACLabel})
	if d.TempCelsius != nil {
		doc.section("Sensors", "")
		doc.sensor(model.Sensor{Name: "Temperature", Value: d.TempCelsius, Unit: model.UnitCelsius, Status: d.Status}, t.sess.history("memory/"+d.Locator+"/temp"))
	}
	var prefixes []string
	for _, mc := range m.mem.EDAC {
		for _, e := range mc.DIMMs {
			if e.Locator == d.Locator || (d.EDACLabel != "" && e.Label == d.EDACLabel) {
				doc.section("EDAC", t.Dim.Render(mc.Name+" "+e.Name))
				doc.kv([2]string{"Label", e.Label}, [2]string{"Location", e.Location},
					[2]string{"Correctable", fmt.Sprint(e.CorrectableErrors)}, [2]string{"Uncorrectable", fmt.Sprint(e.UncorrectableErrors)})
				prefixes = append(prefixes, "edac."+mc.Name+"."+e.Name+".")
			}
		}
	}
	doc.counters(t.sess.counters(func(k string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				return true
			}
		}
		return false
	}), "edac.")
	doc.findings(model.ModMemory, relatedFindings(m.mem.Findings, d.Locator, d.EDACLabel))
	return "DIMM " + d.Locator, doc.String()
}
