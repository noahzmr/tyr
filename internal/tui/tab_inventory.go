package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
)

// inventoryTab lists every component with vendor, model, part number,
// serial and firmware ("/" searches all fields, "f" filters a category),
// or shows the hardware topology as a tree with status rolled up to each
// branch; enter on a tree node opens that component in its module tab.
type inventoryTab struct {
	snap    *model.Snapshot
	w, h    int
	view    int // 0 list, 1 tree
	list    *listView
	tlist   *listView
	cat     string // "" = all
	search  lineInput
	keys    keyMap
	find    key.Binding
	filter  key.Binding
	switchV key.Binding
	tree    *hwNode
	opened  map[string]bool
	rows    []treeRow
}

func newInventoryTab(p *prefs) *inventoryTab {
	tl := newListView(p)
	tl.noSort = true
	return &inventoryTab{search: lineInput{placeholder: "search serial, model, slot…", limit: 64}, keys: newKeyMap(),
		list: newListView(p), tlist: tl, opened: map[string]bool{},
		find:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
		filter:  key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "filter")),
		switchV: key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "view"))}
}

func (v *inventoryTab) Title() string          { return "Inventory" }
func (v *inventoryTab) Module() model.ModuleID { return "" }
func (v *inventoryTab) SetData(s *model.Snapshot) {
	v.snap = s
	v.tree = buildTree(s)
	v.arrange()
}
func (v *inventoryTab) SetSize(w, h int) { v.w, v.h = w, h }
func (v *inventoryTab) Help() []key.Binding {
	if v.view == 1 {
		return []key.Binding{v.keys.Up,
			key.NewBinding(key.WithKeys("right"), key.WithHelp("→ space", "open")),
			key.NewBinding(key.WithKeys("left"), key.WithHelp("←", "close/parent")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open in tab")),
			keyCopy, key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "list"))}
	}
	return []key.Binding{v.find,
		key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "category")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "page")),
		keyCopy, key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "tree"))}
}

// Capturing reports whether the tab wants all keys (search input active).
func (v *inventoryTab) Capturing() bool { return v.search.Focused() }

func compKey(c model.Component) string { return c.Category + "|" + c.Location + "|" + c.Serial }

func (v *inventoryTab) categories() []string {
	if v.snap == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, c := range v.snap.Inventory {
		seen[c.Category] = true
	}
	var out []string
	for _, c := range model.InventoryCategories {
		if seen[c] {
			out = append(out, c)
		}
	}
	return out
}

func (v *inventoryTab) component(k string) (model.Component, bool) {
	for _, c := range v.snap.Inventory {
		if compKey(c) == k {
			return c, true
		}
	}
	return model.Component{}, false
}

func (v *inventoryTab) arrange() {
	if v.snap == nil {
		return
	}
	q := strings.ToLower(strings.TrimSpace(v.search.Value()))
	var items []listItem
	for _, c := range v.snap.Inventory {
		if v.cat != "" && c.Category != v.cat {
			continue
		}
		if q != "" {
			hay := strings.ToLower(strings.Join([]string{c.Category, c.Location, c.Vendor, c.Model, c.PartNumber, c.Serial, c.Firmware, c.Detail}, " "))
			if !strings.Contains(hay, q) {
				continue
			}
		}
		items = append(items, listItem{key: compKey(c), status: c.Status})
	}
	v.list.arrange(items)
	if v.tree != nil {
		v.rows = flatten(v.tree, v.opened)
		var t []listItem
		for _, r := range v.rows {
			t = append(t, listItem{key: r.node.path, status: r.node.status})
		}
		v.tlist.arrange(t)
	}
}

func (v *inventoryTab) treeRow() (treeRow, bool) {
	it, ok := v.tlist.current()
	if !ok {
		return treeRow{}, false
	}
	for _, r := range v.rows {
		if r.node.path == it.key {
			return r, true
		}
	}
	return treeRow{}, false
}

func (v *inventoryTab) Update(msg tea.KeyPressMsg) tea.Cmd {
	if v.search.Focused() {
		switch msg.String() {
		case "esc":
			if v.search.Value() != "" {
				v.search.SetValue("")
			} else {
				v.search.Blur()
			}
		case "enter", "down", "up":
			v.search.Blur()
		default:
			v.search.Update(msg)
			v.list.setSel(0)
		}
		v.arrange()
		return nil
	}
	if key.Matches(msg, v.switchV) {
		v.view = (v.view + 1) % 2
		return nil
	}
	if v.view == 1 {
		r, ok := v.treeRow()
		switch {
		case !ok:
		case key.Matches(msg, v.keys.Right) || msg.String() == "space" || msg.String() == "+":
			if r.hasKid {
				v.opened[r.node.path] = true
			}
		case key.Matches(msg, v.keys.Left) || msg.String() == "-":
			if r.hasKid && r.open {
				v.opened[r.node.path] = false
			} else if i := strings.LastIndex(r.node.path, "/"); i > 0 {
				v.tlist.selKey = r.node.path[:i] // go to parent
			}
		case key.Matches(msg, v.keys.Enter):
			if r.node.module != "" && (r.node.subject != "" || !r.hasKid) {
				mod, sub := r.node.module, r.node.subject
				return func() tea.Msg { return switchTabMsg{module: mod, subject: sub} }
			}
			v.opened[r.node.path] = !r.open
		default:
			v.tlist.handle(msg, v.keys, max(1, v.h/2))
		}
		v.arrange()
		return nil
	}
	switch {
	case key.Matches(msg, v.find):
		v.search.Focus()
	case key.Matches(msg, v.filter):
		cats := append([]string{""}, v.categories()...)
		i := slices.Index(cats, v.cat)
		v.cat = cats[(i+1)%len(cats)]
		v.list.setSel(0)
	case key.Matches(msg, v.keys.Back):
		if v.search.Value() != "" {
			v.search.SetValue("")
		} else {
			v.cat = ""
		}
	default:
		v.list.handle(msg, v.keys, max(1, v.h/2))
	}
	v.arrange()
	return nil
}

func (v *inventoryTab) View(t *Theme) string {
	if v.snap == nil {
		return ""
	}
	v.arrange()
	var hint string
	if !v.snap.Privileged {
		hint = t.StatusText(model.StatusWarn, "▲ not root: SMBIOS, PCI VPD, PPIN and BMC serial numbers need root · rerun with sudo tyr")
	}
	if v.view == 1 {
		return v.treeView(t, hint)
	}
	q := strings.TrimSpace(v.search.Value())
	top := []string{v.searchLine(t), hint}
	cols := []column{{"Type", -1}, {"Location", -1}, {"Model", 0}, {"Serial", -1}}
	if v.w >= 110 {
		cols = []column{{"Type", -1}, {"Location", -1}, {"Vendor", -1}, {"Model", 0}, {"Part number", -1}, {"Serial", -1}}
	}
	if v.w >= 150 {
		cols = append(cols, column{"Firmware", -1})
	}
	for i := range v.list.shown {
		c, _ := v.component(v.list.shown[i].key)
		serial := highlight(t, c.Serial, q)
		if c.Serial == "" {
			serial = t.Sub.Render("–")
			if !v.snap.Privileged {
				serial = t.Sub.Render("○ needs root")
			}
		}
		cells := map[string]string{"Type": highlight(t, catLabel(c.Category), q), "Location": highlight(t, c.Location, q),
			"Vendor": highlight(t, orDash(c.Vendor), q), "Model": highlight(t, orDash(c.Model), q),
			"Part number": highlight(t, orDash(c.PartNumber), q), "Serial": serial, "Firmware": highlight(t, orDash(c.Firmware), q)}
		var r row
		for _, col := range cols {
			r.cells = append(r.cells, cells[col.title])
		}
		v.list.shown[i].row = r
	}
	foot := ""
	if q != "" || v.cat != "" {
		foot = t.Dim.Render(fmt.Sprintf("   %d of %d match", len(v.list.shown), len(v.snap.Inventory)))
		if q != "" {
			foot += t.Sub.Render(" · esc clears search")
		} else {
			foot += t.Sub.Render(" · esc shows all categories")
		}
	}
	rows := max(3, v.h-len(top)-1)
	out := strings.Join(top, "\n") + "\n" + v.list.render(t, cols, v.w, rows)
	if foot != "" {
		out += "\n" + foot
	}
	return out
}

// searchLine: "Search ╭─ query█ ──╮  Category  all  system chassis …";
// the chips give way to the active one when the line is too wide.
func (v *inventoryTab) searchLine(t *Theme) string {
	box := v.searchBox(t, 24)
	head := t.Dim.Render("Search ") + box + "  " + t.Dim.Render("Category ")
	cats := append([]string{""}, v.categories()...)
	var chips []string
	for _, c := range cats {
		name := "all"
		if c != "" {
			name = catLabel(c)
		}
		if c == v.cat {
			chips = append(chips, pill(t, name, true))
		} else {
			chips = append(chips, t.Dim.Render(name))
		}
	}
	line := head + strings.Join(chips, " ")
	if lipgloss.Width(line) > v.w {
		name := "all"
		if v.cat != "" {
			name = catLabel(v.cat)
		}
		line = head + pill(t, name, true) + t.Sub.Render(" f cycles")
	}
	return truncate(line, v.w)
}

// searchBox is the inline field: rounded ends, accent while typing, the
// query in bold with a block cursor, "/ to search" when idle.
func (v *inventoryTab) searchBox(t *Theme, w int) string {
	br := lipgloss.NewStyle().Foreground(t.Border)
	if v.search.Focused() {
		br = lipgloss.NewStyle().Foreground(t.Accent)
	}
	inner := w - 4 // "╭─ " … "╮"
	val := []rune(v.search.Value())
	var body string
	switch {
	case len(val) == 0 && !v.search.Focused():
		body = t.Sub.Render("/ to search") + " "
	default:
		if len(val) > inner-2 {
			val = val[len(val)-(inner-2):]
		}
		body = t.Bold.Render(string(val))
		if v.search.Focused() {
			body += lipgloss.NewStyle().Foreground(t.Accent).Render("█")
		}
	}
	fill := max(0, inner-lipgloss.Width(body))
	return br.Render("╭─ ") + body + br.Render(strings.Repeat("─", fill)+"╮")
}

// highlight marks case-insensitive matches of q in s with the search
// match token (reverse without colour).
func highlight(t *Theme, s, q string) string {
	if q == "" || s == "" {
		return s
	}
	low, lq := strings.ToLower(s), strings.ToLower(q)
	if len(low) != len(s) {
		return s
	}
	st := lipgloss.NewStyle().Background(t.Match).Foreground(t.Ink)
	if t.NoColor {
		st = lipgloss.NewStyle().Reverse(true)
	}
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(low[i:], lq)
		if j < 0 {
			break
		}
		b.WriteString(s[i : i+j])
		b.WriteString(st.Render(s[i+j : i+j+len(lq)]))
		i += j + len(lq)
	}
	b.WriteString(s[i:])
	return b.String()
}

var catLabels = map[string]string{"bmc": "BMC", "psu": "PSU", "cpu": "CPU", "dimm": "DIMM", "raid_controller": "RAID",
	"raid_drive": "RAID PD", "volume": "volume", "nic": "NIC", "transceiver": "xcvr", "gpu": "GPU", "npu": "NPU", "fru": "FRU", "device": "device"}

// catLabel is the short display name of an inventory category.
func catLabel(c string) string {
	if l, ok := catLabels[c]; ok {
		return l
	}
	return c
}

// treeView: the topology with a fixed Serial column; at 100 columns and
// more the selected node's inspector sits on the right.
func (v *inventoryTab) treeView(t *Theme, hint string) string {
	var top []string
	if hint != "" {
		top = append(top, hint)
	}
	for i := range v.tlist.shown {
		var r treeRow
		for _, x := range v.rows {
			if x.node.path == v.tlist.shown[i].key {
				r = x
			}
		}
		label := treeLabel(t, r)
		if info := treeInfo(t, r); info != "" {
			label += "  " + info
		}
		sn := r.node.serial
		if sn == "" && !v.snap.Privileged && !r.fold && (r.node == v.tree || (r.node.module == model.ModMemory || r.node.module == model.ModSystem) && !r.hasKid) {
			sn = t.Sub.Render("○ needs root")
		}
		v.tlist.shown[i].row = row{cells: []string{label, sn}, noDot: true}
	}
	cols := []column{{"Component", 0}, {"Serial", -1}}
	h := max(3, v.h-len(top))
	if v.w < 100 {
		return strings.Join(append(top, v.tlist.render(t, cols, v.w, h)), "\n")
	}
	iw := 38
	if v.w >= 150 {
		iw = 48
	}
	lw := v.w - iw - 1
	left := v.tlist.render(t, cols, lw, h)
	return strings.Join(append(top, besideInspector(left, lw+1, v.treeInspector(t, iw, h), h)), "\n")
}

// nodeComponent finds the inventory record behind a tree node: by serial,
// else by location.
func (v *inventoryTab) nodeComponent(n *hwNode) (model.Component, bool) {
	for _, c := range v.snap.Inventory {
		if n.serial != "" && c.Serial == n.serial {
			return c, true
		}
	}
	for _, c := range v.snap.Inventory {
		if c.Location == n.label || (n.subject != "" && c.Location == n.subject) || strings.HasSuffix(c.Location, "/"+n.label) {
			return c, true
		}
	}
	return model.Component{}, false
}

// treeInspector: the selected node's identity, its findings, the keys
// that act on it and its path.
func (v *inventoryTab) treeInspector(t *Theme, w, h int) string {
	r, ok := v.treeRow()
	if !ok {
		return inspector(t, model.StatusUnknown, "nothing selected", "", w, h)
	}
	n := r.node
	var lines []string
	if c, ok := v.nodeComponent(n); ok && !r.fold {
		sn := c.Serial
		if sn != "" {
			sn = t.Bold.Render(sn)
		} else if !v.snap.Privileged {
			sn = t.Sub.Render("○ needs root")
		}
		lines = append(lines, kvBlock(t, []kvPair{{"Type", catLabel(c.Category)}, {"Location", c.Location}, {"Vendor", c.Vendor},
			{"Model", c.Model}, {"Part no.", c.PartNumber}, {"Serial", sn}, {"Firmware", c.Firmware}}, ""))
		if c.Detail != "" {
			lines = append(lines, t.Dim.Render(c.Detail))
		}
	} else {
		lines = append(lines, t.Dim.Render(n.info))
		if n.serial != "" {
			lines = append(lines, kvBlock(t, []kvPair{{"Serial", t.Bold.Render(n.serial)}}, ""))
		}
		if r.hasKid {
			lines = append(lines, t.Dim.Render(countLabel(len(n.children), "item", "items")+" below"))
		}
	}
	if n.module != "" && !r.fold {
		var fs []model.Finding
		if sec := v.snap.Section(n.module); sec != nil {
			keys := []string{n.label}
			if n.subject != "" {
				keys = append(keys, n.subject)
			}
			if !r.hasKid || n.subject != "" {
				fs = relatedFindings(sec.Head().Findings, keys...)
			}
		}
		if len(fs) > 0 {
			lines = append(lines, "")
			for i, f := range sortFindings(fs) {
				if i == 4 {
					lines = append(lines, t.Sub.Render(fmt.Sprintf("… %d more", len(fs)-4)))
					break
				}
				if f.Status != model.StatusOK {
					lines = append(lines, truncate(t.Dot(f.Status)+" "+f.Message, w-2))
				}
			}
		}
	}
	lines = append(lines, "")
	if n.module != "" && !r.fold {
		lines = append(lines, t.Key.Render(pad("enter", 7))+t.Dim.Render("open in "+moduleTitle[n.module]+" tab"))
	} else if r.hasKid {
		lines = append(lines, t.Key.Render(pad("enter", 7))+t.Dim.Render(map[bool]string{true: "close", false: "open"}[r.open]))
	}
	if n.serial != "" {
		lines = append(lines, t.Key.Render(pad("c", 7))+t.Dim.Render("copy serial"))
	}
	var crumbs []string
	for _, p := range strings.Split(strings.TrimPrefix(n.path, "/"), "/") {
		if !strings.HasPrefix(p, "~") {
			crumbs = append(crumbs, p)
		}
	}
	if r.fold {
		crumbs = append(crumbs, n.label)
	}
	lines = append(lines, "", t.Dim.Render("Path"), lipgloss.NewStyle().Width(w-2).Render(strings.Join(crumbs, t.Sub.Render(" › "))))
	return inspector(t, n.status, n.label, strings.Join(lines, "\n"), w, h)
}

func countProblems(n *hwNode) int {
	c := 0
	for _, ch := range n.children {
		if ch.status >= model.StatusWarn && len(ch.children) == 0 {
			c++
		}
		c += countProblems(ch)
	}
	return c
}

// Detail is the page of the selected component (list view).
func (v *inventoryTab) Detail(t *Theme, w int) (string, string) {
	if v.view != 0 {
		return "", ""
	}
	it, ok := v.list.current()
	if !ok {
		return "", ""
	}
	c, _ := v.component(it.key)
	doc := newDoc(t, w)
	doc.section("Component", t.Badge(c.Status))
	doc.kv([2]string{"Category", c.Category}, [2]string{"Location", c.Location}, [2]string{"Vendor", c.Vendor}, [2]string{"Model", c.Model},
		[2]string{"Part number", c.PartNumber}, [2]string{"Serial", c.Serial}, [2]string{"Firmware", c.Firmware}, [2]string{"Detail", c.Detail})
	return c.Category + " " + c.Location, doc.String()
}

// lineInput is a minimal single-line text input (no clipboard integration,
// so no external helper is ever executed).
type lineInput struct {
	value       []rune
	focused     bool
	placeholder string
	limit       int
}

func (l *lineInput) Focus()            { l.focused = true }
func (l *lineInput) Blur()             { l.focused = false }
func (l *lineInput) Focused() bool     { return l.focused }
func (l *lineInput) Value() string     { return string(l.value) }
func (l *lineInput) SetValue(s string) { l.value = []rune(s) }

func (l *lineInput) Update(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "backspace", "ctrl+h":
		if len(l.value) > 0 {
			l.value = l.value[:len(l.value)-1]
		}
	case "ctrl+u":
		l.value = nil
	default:
		if msg.Text != "" && (l.limit == 0 || len(l.value) < l.limit) {
			for _, r := range msg.Text {
				if r >= 0x20 && r != 0x7f {
					l.value = append(l.value, r)
				}
			}
		}
	}
}

// Insert adds text at the end (paste): control characters are dropped,
// the limit holds.
func (l *lineInput) Insert(s string) {
	for _, r := range s {
		if l.limit > 0 && len(l.value) >= l.limit {
			return
		}
		if r >= 0x20 && r != 0x7f {
			l.value = append(l.value, r)
		}
	}
}

// Field renders the form variant: "[value█   ]" with border-token
// brackets, fixed width w, accent block cursor while editing.
func (l *lineInput) Field(t *Theme, w int) string {
	br := lipgloss.NewStyle().Foreground(t.Border)
	v := string(l.value)
	cur := ""
	if l.focused {
		cur = lipgloss.NewStyle().Foreground(t.Accent).Render("█")
	}
	inner := w - 2
	if len([]rune(v))+1 > inner {
		r := []rune(v)
		v = string(r[len(r)-(inner-1):])
	}
	return br.Render("[") + padCell(t.Bold.Render(v)+cur, inner, false) + br.Render("]")
}

func (l *lineInput) View(t *Theme) string {
	cursor := ""
	if l.focused {
		cursor = t.Selected.Render(" ")
	}
	if len(l.value) == 0 && !l.focused {
		return t.Dim.Render("/ " + l.placeholder)
	}
	if len(l.value) == 0 {
		return "/ " + cursor + t.Dim.Render(l.placeholder)
	}
	return "/ " + string(l.value) + cursor
}
