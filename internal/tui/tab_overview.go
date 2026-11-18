package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/report"
)

var cardModules = []model.ModuleID{model.ModCPU, model.ModMemory, model.ModStorage, model.ModPower,
	model.ModNetwork, model.ModAccel, model.ModEvents, model.ModSystem}

var moduleTitle = map[model.ModuleID]string{
	model.ModSystem: "System", model.ModCPU: "CPU", model.ModMemory: "Memory", model.ModStorage: "Storage",
	model.ModPower: "Power", model.ModNetwork: "Network", model.ModAccel: "GPU/NPU", model.ModEvents: "Events",
}

var moduleShort = map[model.ModuleID]string{
	model.ModSystem: "Sys", model.ModCPU: "CPU", model.ModMemory: "Mem", model.ModStorage: "Stor",
	model.ModPower: "Power", model.ModNetwork: "Net", model.ModAccel: "GPU", model.ModEvents: "Evt",
}

// overviewTab (canvas: Overview, Overview-80, Overview-160): one card per
// module (4×2 grid; one line per module below 100 columns or 30 rows),
// then all findings (selectable; enter opens the component in its tab)
// and, on the right, rising counters, findings resolved this session and
// missing data sources.
type overviewTab struct {
	snap        *model.Snapshot
	implemented func(model.ModuleID) bool
	w, h        int
	sel         int
	inFindings  bool // focus: false = cards, true = findings
	finds       *listView
	findMod     map[string]model.ModuleID
	keys        keyMap
}

func newOverviewTab(impl func(model.ModuleID) bool, p *prefs) *overviewTab {
	f := newListView(p)
	f.noSort = true // ordered by arrangeFindings
	return &overviewTab{implemented: impl, keys: newKeyMap(), finds: f}
}

func (o *overviewTab) Title() string             { return "Overview" }
func (o *overviewTab) Module() model.ModuleID    { return model.ModSystem }
func (o *overviewTab) SetData(s *model.Snapshot) { o.snap = s; o.arrangeFindings() }
func (o *overviewTab) SetSize(w, h int)          { o.w, o.h = w, h }
func (o *overviewTab) Help() []key.Binding {
	return []key.Binding{key.NewBinding(key.WithKeys("up"), key.WithHelp("←→↑↓", "move")), o.keys.Enter,
		key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next problem")), keyExplain, keyOrder,
		key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "export"))}
}

// compact: one line per module instead of cards.
func (o *overviewTab) compact() bool { return o.w < 100 || o.h < 27 }

func (o *overviewTab) cols() int {
	if o.compact() {
		return 1
	}
	return 4
}

// arrangeFindings lists all non-OK findings: worst first (default) or in
// module order ("o").
func (o *overviewTab) arrangeFindings() {
	if o.snap == nil {
		return
	}
	o.findMod = map[string]model.ModuleID{}
	var items []listItem
	order := map[model.ModuleID]int{}
	for i, id := range cardModules {
		order[id] = i
	}
	for _, id := range append([]model.ModuleID{model.ModSystem}, model.Modules...) {
		if sec := o.snap.Section(id); sec != nil {
			for _, f := range sec.Head().Findings {
				if f.Status == model.StatusOK {
					continue
				}
				k := string(id) + "|" + f.Key()
				o.findMod[k] = id
				items = append(items, listItem{key: k, status: f.Status})
			}
		}
	}
	if o.finds.prefs != nil && o.finds.prefs.naturalOrder {
		sort.SliceStable(items, func(i, j int) bool {
			a, b := order[o.findMod[items[i].key]], order[o.findMod[items[j].key]]
			if a != b {
				return a < b
			}
			return items[i].status > items[j].status
		})
	} else {
		sort.SliceStable(items, func(i, j int) bool { return items[i].status > items[j].status })
	}
	o.finds.arrange(items)
}

func (o *overviewTab) finding(k string) (model.Finding, model.ModuleID) {
	id := o.findMod[k]
	if sec := o.snap.Section(id); sec != nil {
		for _, f := range sec.Head().Findings {
			if string(id)+"|"+f.Key() == k {
				return f, id
			}
		}
	}
	return model.Finding{}, id
}

func (o *overviewTab) Update(msg tea.KeyPressMsg) tea.Cmd {
	n, c := len(cardModules), o.cols()
	if o.inFindings {
		switch {
		case key.Matches(msg, o.keys.Up) && o.finds.sel == 0:
			o.inFindings = false
		case key.Matches(msg, o.keys.Back):
			o.inFindings = false
		case key.Matches(msg, o.keys.Enter):
			it, ok := o.finds.current()
			if !ok {
				return nil
			}
			f, mod := o.finding(it.key)
			return func() tea.Msg { return switchTabMsg{module: mod, subject: f.Subject} }
		case key.Matches(msg, keyExplain):
			it, ok := o.finds.current()
			if !ok {
				return nil
			}
			f, mod := o.finding(it.key)
			return func() tea.Msg { return explainMsg{report.Alert{Module: mod, Finding: f}} }
		case msg.String() == "n":
			o.finds.setSel(o.finds.sel + 1)
		default:
			o.finds.handle(msg, o.keys, max(1, o.h/2))
		}
		return nil
	}
	switch {
	case msg.String() == "n" && len(o.finds.shown) > 0:
		o.inFindings = true
		o.finds.setSel(0)
	case key.Matches(msg, o.keys.Down) && o.sel+c >= n:
		if len(o.finds.shown) > 0 {
			o.inFindings = true
			o.finds.setSel(0)
		}
	case key.Matches(msg, o.keys.Left):
		o.sel = max(0, o.sel-1)
	case key.Matches(msg, o.keys.Right):
		o.sel = min(n-1, o.sel+1)
	case key.Matches(msg, o.keys.Up):
		if o.sel-c >= 0 {
			o.sel -= c
		}
	case key.Matches(msg, o.keys.Down):
		o.sel += c
	case key.Matches(msg, o.keys.Enter):
		mod := cardModules[o.sel]
		if mod == model.ModSystem {
			return nil
		}
		return func() tea.Msg { return switchTabMsg{module: mod} }
	}
	return nil
}

// Strip: "✖ CRIT  Needs attention  Memory · Storage  Network · Events"
// (failing modules by severity) and "N new since HH:MM".
func (o *overviewTab) Strip(t *Theme, w int) string {
	if o.snap == nil {
		return ""
	}
	names := moduleTitle
	if w < 100 {
		names = moduleShort
	}
	var crit, warn []string
	for _, id := range cardModules {
		if sec := o.snap.Section(id); sec != nil {
			switch sec.Head().Status {
			case model.StatusCrit:
				crit = append(crit, names[id])
			case model.StatusWarn:
				warn = append(warn, names[id])
			}
		}
	}
	sep := " · "
	if w < 100 {
		sep = " "
	}
	var groups []string
	if len(crit) > 0 {
		groups = append(groups, t.StatusText(model.StatusCrit, strings.Join(crit, sep)))
	}
	if len(warn) > 0 {
		groups = append(groups, t.StatusText(model.StatusWarn, strings.Join(warn, sep)))
	}
	left := t.Badge(o.snap.Status) + " "
	if len(groups) == 0 {
		left += t.Bold.Render("All checks passed")
	} else {
		left += t.Bold.Render("Needs attention") + "  " + strings.Join(groups, "  ")
	}
	right := ""
	if s := t.sess; s != nil && s.baseFindings != nil {
		newN := 0
		for _, f := range o.snap.Findings() {
			if f.Status != model.StatusOK && s.isNew(f) {
				newN++
			}
		}
		if newN > 0 {
			right = t.Bold.Render(fmt.Sprint(newN)) + t.Dim.Render(" new since "+s.start.Local().Format("15:04"))
		}
	}
	gap := max(2, w-lipgloss.Width(left)-lipgloss.Width(right))
	return truncate(left+strings.Repeat(" ", gap)+right, w)
}

func (o *overviewTab) View(t *Theme) string {
	if o.snap == nil {
		return ""
	}
	o.arrangeFindings()
	o.sel = min(o.sel, len(cardModules)-1)
	var top string
	if o.compact() {
		top = o.moduleRows(t)
	} else {
		cw := (o.w - 3) / 4
		var rows []string
		for i := 0; i < len(cardModules); i += 4 {
			var cards []string
			for j := i; j < min(i+4, len(cardModules)); j++ {
				if j > i {
					cards = append(cards, " ")
				}
				cards = append(cards, o.card(t, cardModules[j], cw, j == o.sel && !o.inFindings))
			}
			rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cards...))
		}
		top = strings.Join(rows, "\n") + "\n"
	}
	room := max(4, o.h-lipgloss.Height(top))
	if o.compact() || o.w < 110 {
		return top + "\n" + o.findingsList(t, o.w, room)
	}
	rw := max(40, o.w*37/100)
	if o.w >= 150 {
		rw = o.w * 38 / 100
	}
	right := o.rightColumn(t, rw, room)
	if right == "" { // nothing rising, resolved or missing: full width
		return top + "\n" + o.findingsList(t, o.w, room)
	}
	lw := o.w - rw - 2
	left := o.findingsList(t, lw, room)
	return top + "\n" + besideInspector(left, lw+2, right, room)
}

// card: rounded border (status colour when the module has problems, border
// token when OK, thick accent when focused); glyph + bold name with status
// word and count; summary; top finding wrapped to two lines; at ≥150
// columns a trend line.
func (o *overviewTab) card(t *Theme, mod model.ModuleID, w int, focused bool) string {
	inner := max(10, w-4)
	sec := o.snap.Section(mod)
	var glyph, right, summary string
	var lines []string
	border := t.Border
	st := model.StatusOK
	switch {
	case sec != nil:
		h := sec.Head()
		st = h.Status
		glyph = t.Dot(st)
		n := countNonOK(h.Findings)
		right = t.StatusBold(st, st.String())
		if n > 0 {
			right += t.Dim.Render(" · ") + t.StatusText(st, fmt.Sprint(n))
		}
		if st >= model.StatusWarn {
			border = t.StatusColor(st)
		}
		summary = o.cardSummary(mod)
		if f, ok := topFindingOf(h); ok {
			lines = wrapTwo("▸ "+f.Message, inner)
		} else if mod != model.ModSystem {
			lines = []string{t.StatusText(model.StatusOK, "✓ no findings")}
		} else {
			lines = systemLines(t, o.snap.System)
		}
	default:
		glyph = lipgloss.NewStyle().Foreground(t.NA).Render("○")
		right = t.Sub.Render("unavailable")
		reason := "collector not implemented yet"
		if o.implemented(mod) {
			reason = "not collected"
		}
		summary = t.Sub.Render(reason)
	}
	title := glyph + " " + t.Bold.Render(moduleTitle[mod])
	head := title + strings.Repeat(" ", max(1, inner-lipgloss.Width(title)-lipgloss.Width(right))) + right
	body := []string{head, t.Dim.Render(truncate(summary, inner))}
	for i := 0; i < 2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
			if sec != nil && st >= model.StatusWarn {
				l = t.Bold.Render(l)
			}
		}
		body = append(body, l)
	}
	if o.w >= 150 {
		body = append(body, o.cardTrend(t, mod, inner))
	}
	for i := range body {
		body[i] = padCell(body[i], inner, false)
	}
	stl := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1)
	if focused {
		stl = stl.Border(lipgloss.ThickBorder()).BorderForeground(t.Accent)
	}
	return stl.Render(strings.Join(body, "\n"))
}

// wrapTwo wraps s to two lines of w cells; the ellipsis only on the last.
func wrapTwo(s string, w int) []string {
	if lipgloss.Width(s) <= w {
		return []string{s}
	}
	words := strings.Fields(s)
	first := ""
	i := 0
	for ; i < len(words); i++ {
		next := strings.TrimSpace(first + " " + words[i])
		if lipgloss.Width(next) > w {
			break
		}
		first = next
	}
	if first == "" {
		return []string{truncate(s, w)}
	}
	rest := "  " + strings.Join(words[i:], " ")
	return []string{first, truncate(rest, w)}
}

func topFindingOf(h *model.Header) (model.Finding, bool) {
	fs := sortFindings(h.Findings)
	for _, f := range fs {
		if f.Status != model.StatusOK {
			return f, true
		}
	}
	return model.Finding{}, false
}

// cardSummary is the short line under a card title (≤ card width).
func (o *overviewTab) cardSummary(mod model.ModuleID) string {
	s := o.snap
	switch mod {
	case model.ModCPU:
		if c := s.CPU; c != nil {
			on := 0
			for _, l := range c.Logical {
				if l.Online {
					on++
				}
			}
			out := fmt.Sprintf("%d/%d online", on, len(c.Logical))
			if c.MaxTempC != nil {
				out += " · max " + check.FmtTemp(*c.MaxTempC)
			}
			return out
		}
	case model.ModMemory:
		if m := s.Memory; m != nil {
			out := bytesGB(m.TotalBytes)
			if m.InstalledBytes != nil {
				out = bytesGB(*m.InstalledBytes)
			}
			if m.Slots > 0 {
				out += fmt.Sprintf(" · %d/%d slots", m.PopulatedSlots, m.Slots)
			}
			return out
		}
	case model.ModStorage:
		if st := s.Storage; st != nil {
			out := countLabel(len(st.Disks), "disk", "disks")
			for _, a := range st.MDArrays {
				if a.Degraded {
					out += " · " + a.Name + " degraded"
				}
			}
			return out
		}
	case model.ModNetwork:
		if n := s.Network; n != nil {
			ports, up := 0, 0
			out := ""
			for _, x := range n.Interfaces {
				if x.Kind == "physical" {
					ports++
					if x.Carrier != nil && *x.Carrier {
						up++
					}
				}
				if b := x.Bond; b != nil {
					on := 0
					for _, sl := range b.Slaves {
						if sl.MIIStatus == "up" {
							on++
						}
					}
					out += fmt.Sprintf(" · %s %d/%d", x.Name, on, len(b.Slaves))
				}
			}
			return fmt.Sprintf("%d/%d up", up, ports) + out
		}
	case model.ModAccel:
		if a := s.Accel; a != nil {
			return a.Head().Summary
		}
	case model.ModSystem:
		if sys := s.System; sys != nil {
			if sys.OS != "" { // the OS here, kernel and BIOS on the lines below
				if sys.LiveSystem {
					return sys.OS + " (live)"
				}
				return sys.OS
			}
			var p []string
			if sys.BIOSVersion != "" {
				p = append(p, "BIOS "+sys.BIOSVersion)
			}
			if sys.BMCFirmware != "" {
				p = append(p, "BMC "+sys.BMCFirmware)
			}
			if len(p) == 0 {
				p = append(p, productName(sys))
			}
			return strings.Join(p, " · ")
		}
	}
	if sec := s.Section(mod); sec != nil {
		return sec.Head().Summary
	}
	return ""
}

// cardTrend is the ≥150-column fourth card line: a sparkline of the
// module's key trend this session.
func (o *overviewTab) cardTrend(t *Theme, mod model.ModuleID, w int) string {
	h := func(k string) []float64 {
		v := t.sess.history(k)
		if len(v) < 2 {
			return nil
		}
		return v
	}
	if len(h("cpu/load"))+len(h("cpu/maxtemp"))+len(h("network/total"))+len(h("memory/used"))+len(h("power/draw")) == 0 &&
		mod != model.ModSystem && mod != model.ModAccel {
		return ""
	}
	switch mod {
	case model.ModCPU:
		return t.Dim.Render("load ") + spark(h("cpu/load"), w/2-6, 100, t.Series[0]) + t.Dim.Render(" temp ") + spark(h("cpu/maxtemp"), w/2-6, 0, t.Series[1])
	case model.ModNetwork:
		return t.Dim.Render("rx+tx ") + spark(h("network/total"), w-7, 0, t.Series[0])
	case model.ModMemory:
		if m := o.snap.Memory; m != nil && m.TotalBytes > 0 {
			pct := fmt.Sprintf(" %.0f%%", float64(m.UsedBytes)/float64(m.TotalBytes)*100)
			return t.Dim.Render("used  ") + spark(h("memory/used"), w-7-len(pct), float64(m.TotalBytes), t.Series[0]) + t.Dim.Render(pct)
		}
	case model.ModPower:
		if p := o.snap.Power; p != nil && p.ConsumedWatts != nil {
			now := fmt.Sprintf(" %.0f W", *p.ConsumedWatts)
			return t.Dim.Render("draw  ") + spark(h("power/draw"), w-7-len(now), 0, t.Series[0]) + t.Dim.Render(now)
		}
	case model.ModAccel:
		if a := o.snap.Accel; a != nil && len(a.GPUs) > 0 {
			return t.Dim.Render("util  ") + spark(h("accel/"+a.GPUs[0].PCIAddress+"/load"), w-7, 100, t.Series[0])
		}
	case model.ModSystem:
		if sys := o.snap.System; sys != nil {
			return t.Dim.Render(truncate(strings.TrimSpace(sys.BoardVendor+" "+sys.BoardProduct), w))
		}
	}
	return ""
}

// moduleRows is the compact form: one selectable line per module.
func (o *overviewTab) moduleRows(t *Theme) string {
	var rows []row
	for _, mod := range cardModules {
		sec := o.snap.Section(mod)
		if sec == nil {
			reason := "collector not implemented yet"
			if o.implemented(mod) {
				reason = "not collected"
			}
			rows = append(rows, row{cells: []string{moduleTitle[mod], "n/a", "", reason}, dim: true})
			continue
		}
		h := sec.Head()
		n := countNonOK(h.Findings)
		cnt := "–"
		if n > 0 {
			cnt = fmt.Sprint(n)
		}
		msg := o.cardSummary(mod)
		if f, ok := topFindingOf(h); ok {
			msg = f.Message
		}
		rows = append(rows, row{cells: []string{moduleTitle[mod], t.StatusText(h.Status, h.Status.String()), cnt, msg}, status: h.Status})
	}
	sel := o.sel
	if o.inFindings {
		sel = -1
	}
	head := t.HeadingRight("Modules", "", "enter open tab", o.w)
	if !o.inFindings {
		head = t.HeadingFocus("Modules", "", "enter open tab", o.w)
	}
	list := renderList(t, []column{{"Module", 8}, {"State", 7}, {">#", 3}, {"Top finding", 0}}, rows, sel, o.w, len(rows)+1)
	// the module list needs no column header in compact form
	ls := strings.Split(list, "\n")
	return head + "\n" + strings.Join(ls[1:], "\n")
}

// findingsList renders the selectable findings (worst first) in w × h.
func (o *overviewTab) findingsList(t *Theme, w, h int) string {
	wide := w >= 90
	total := len(o.finds.shown)
	order := "worst first"
	if o.finds.prefs != nil && o.finds.prefs.naturalOrder {
		order = "module order"
	}
	body := max(1, h-2)
	note := t.Counts(o.snap.Findings())
	right := order
	if total > body {
		first := max(0, min(o.finds.sel-body/2, total-body))
		if !o.inFindings {
			first = 0
		}
		right += fmt.Sprintf(" · %d–%d of %d", first+1, min(total, first+body), total)
	}
	head := t.HeadingRight("Findings", note, right, w)
	if o.inFindings {
		head = t.HeadingFocus("Findings", note, "i explain · "+right, w)
	}
	if total == 0 {
		return head + "\n" + t.StatusText(model.StatusOK, " ● no problems found")
	}
	cols := []column{{"Module", 8}, {"Finding", 0}, {"", 5}}
	if wide {
		cols = []column{{"Module", 8}, {"Component", 13}, {"Finding", 0}, {">Seen", 5}, {"", 5}}
	}
	for i := range o.finds.shown {
		f, mod := o.finding(o.finds.shown[i].key)
		newTag := ""
		if t.sess.isNew(f) {
			newTag = t.NewTag()
		}
		cells := []string{moduleTitle[mod], f.Message, newTag}
		if wide {
			seen := ""
			if t.sess != nil {
				if ts, ok := t.sess.firstSeen[f.Key()]; ok {
					seen = t.Sub.Render(ts.Local().Format("15:04"))
				}
			}
			cells = []string{moduleTitle[mod], t.Dim.Render(f.Subject), f.Message, seen, newTag}
		}
		o.finds.shown[i].row = row{cells: cells}
	}
	sel := o.finds.sel
	if !o.inFindings {
		sel = -1
	}
	save := o.finds.sel
	o.finds.sel = sel
	list := o.finds.render(t, cols, w, body+1)
	o.finds.sel = save
	ls := strings.Split(list, "\n") // drop the column header: the heading is enough
	return head + "\n" + strings.Join(ls[1:], "\n")
}

// rightColumn: rising counters, resolved this session, data sources.
func (o *overviewTab) rightColumn(t *Theme, w, h int) string {
	var out []string
	add := func(s ...string) { out = append(out, s...) }
	s := t.sess
	if rising := s.rising(nil); len(rising) > 0 {
		add(t.HeadingRight("Rising counters", "", "this session", w))
		for _, c := range rising[:min(5, len(rising))] {
			v := fmt.Sprintf("%d", c.Value)
			g := t.StatusText(model.StatusWarn, fmt.Sprintf("↑+%d", c.SinceStart))
			add(padCell(" "+counterLabel(o.snap, c.Key), w-lipgloss.Width(v)-lipgloss.Width(g)-2, false) + " " + v + " " + g)
		}
		add("")
	}
	if res := s.resolved(); len(res) > 0 {
		add(t.Heading("Resolved this session", "", w))
		for _, f := range res[:min(3, len(res))] {
			ts := ""
			if at, ok := s.resolvedAt[f.Key()]; ok {
				ts = at.Local().Format("15:04")
			}
			add(padCell(" "+t.Dot(model.StatusOK)+" "+t.Dim.Render(f.Message), w-6, false) + " " + t.Sub.Render(ts))
		}
		add("")
	}
	var missing []string
	total := 0
	for _, id := range append([]model.ModuleID{model.ModSystem}, model.Modules...) {
		sec := o.snap.Section(id)
		if sec == nil {
			continue
		}
		for _, src := range sec.Head().Sources {
			total++
			name := pad(src.Name, 12)
			switch src.State {
			case model.AvailUnavailable:
				missing = append(missing, truncate(" "+t.Unavail(name, src.Reason), w))
			case model.AvailNotApplicable:
				missing = append(missing, truncate(" "+t.NotApplicable(name, src.Reason), w))
			}
		}
	}
	if len(missing) > 0 {
		add(t.HeadingRight("Data sources", "", fmt.Sprintf("%d of %d missing", len(missing), total), w))
		add(missing...)
	}
	if len(out) > h {
		out = out[:h]
	}
	return strings.Join(out, "\n")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// systemLines are the System card's two lines without findings: the
// kernel (and BIOS), then fans and inlet temperature (or the uptime).
func systemLines(t *Theme, s *model.System) []string {
	if s == nil {
		return nil
	}
	var first []string
	if s.Kernel != "" {
		first = append(first, t.Dim.Render("kernel ")+s.Kernel)
	}
	if s.OS != "" && s.BIOSVersion != "" { // without an OS the BIOS is in the summary line
		first = append(first, t.Dim.Render("BIOS ")+s.BIOSVersion)
	}
	second := systemLine(t, s)
	if len(first) == 0 {
		return []string{second}
	}
	return []string{strings.Join(first, t.Dim.Render(" · ")), second}
}

func systemLine(t *Theme, s *model.System) string {
	if s == nil {
		return ""
	}
	var parts []string
	if n := len(s.Fans); n > 0 {
		ok := 0
		for _, f := range s.Fans {
			if f.Status == model.StatusOK {
				ok++
			}
		}
		parts = append(parts, t.StatusText(worstSensor(s.Fans), fmt.Sprintf("%d/%d fans OK", ok, n)))
	}
	for _, x := range s.Temps {
		if strings.Contains(strings.ToLower(x.Name), "inlet") && x.Value != nil {
			parts = append(parts, t.StatusText(x.Status, "inlet "+check.FmtTemp(*x.Value)))
		}
	}
	if len(parts) == 0 && s.UptimeSeconds != nil {
		parts = append(parts, t.Dim.Render("up "+duration(*s.UptimeSeconds)))
	}
	return strings.Join(parts, " · ")
}

func worstSensor(ss []model.Sensor) model.Status {
	st := model.StatusOK
	for _, s := range ss {
		st = model.Worst(st, s.Status)
	}
	return st
}

// counterLabel names a session counter for people: "cpe rx dropped (bond:
// members' sum)", "eno5np0 lost in NIC", "sda SCSI cmd errors".
func counterLabel(s *model.Snapshot, key string) string {
	parts := strings.SplitN(key, ".", 3)
	if len(parts) < 3 {
		return key
	}
	what := parts[2]
	if n, ok := counterNames[what]; ok {
		what = n
	}
	if parts[0] == "net" {
		what = map[string]string{"rx_missed": "lost in the NIC", "rx_dropped": "rx dropped", "tx_dropped": "tx dropped", "rx_errors": "rx errors",
			"tx_errors": "tx errors", "rx_crc_errors": "CRC errors", "carrier_changes": "link flaps", "dropped": "backlog drops"}[parts[2]]
		if what == "" {
			what = strings.ReplaceAll(parts[2], "_", " ")
		}
		if s != nil && s.Network != nil {
			if x := s.Network.Interface(parts[1]); x != nil {
				switch {
				case x.Kind == "bond":
					what += " (bond: members' sum)"
				case x.Master != "":
					if m := s.Network.Interface(x.Master); m != nil && m.Bond != nil && strings.Contains(m.Bond.Mode, "active-backup") && m.Bond.ActiveSlave != x.Name {
						what += " (bond backup)"
					}
				}
			}
		}
	}
	return parts[1] + " " + what
}
