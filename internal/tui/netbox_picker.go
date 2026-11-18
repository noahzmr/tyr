package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/netbox"
)

// nbPicker chooses a value from what NetBox has (sites, roles, tenants,
// tags, device custom fields) or from a fixed list (inventory
// categories), filtered as you type.
type nbPicker struct {
	field    string // site, role, tenant, custom_fields, tag, categories
	title    string
	items    []netbox.Choice
	filter   lineInput
	sel      int
	multi    bool
	chosen   map[string]bool
	allowNew bool   // a typed name that NetBox does not have yet
	newNote  string // what happens to a new name
	loading  bool
	err      string
}

type netboxChoicesMsg struct {
	field string
	items []netbox.Choice
	err   error
}

// pickable: fields chosen from a list once NetBox is reachable
// (categories always).
func (d *netboxDialog) pickable(f string) bool {
	switch f {
	case "categories":
		return true
	case "site", "role", "tenant", "custom_fields":
		return d.cfg.URL != "" && d.cfg.Token != ""
	}
	return false
}

// openPicker opens the list for a field and loads it from NetBox.
func (m *Model) openPicker(f string) tea.Cmd {
	d := m.nb
	p := &nbPicker{field: f, filter: lineInput{limit: 200}}
	p.filter.Focus()
	kind := f
	switch f {
	case "site":
		p.title, p.allowNew, p.newNote = "Site", true, "new site: created with the first new device"
	case "role":
		p.title, p.allowNew, p.newNote = "Device role", true, "new role: created with the first new device"
	case "tenant":
		p.title = "Tenant"
	case "custom_fields":
		p.title, kind = "Device custom field", "custom_field"
	case "tag":
		p.title, p.allowNew, p.newNote = "Tag", true, "new tag: created when a device first gets it"
	case "categories":
		p.title, p.multi, p.chosen = "Inventory categories", true, map[string]bool{}
		for _, c := range netbox.DefaultCategories {
			p.items = append(p.items, netbox.Choice{Name: c, Detail: netbox.RoleName(c)})
			if len(d.cfg.Categories) == 0 || slices.Contains(d.cfg.Categories, c) {
				p.chosen[c] = true
			}
		}
		d.picker = p
		return nil
	}
	p.loading = true
	d.picker = p
	cfg := d.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		items, err := netbox.NewClient(cfg, nil).Choices(ctx, kind)
		return netboxChoicesMsg{field: f, items: items, err: err}
	}
}

func (m *Model) netboxChoices(msg netboxChoicesMsg) {
	if m.nb == nil || m.nb.picker == nil || m.nb.picker.field != msg.field {
		return
	}
	p := m.nb.picker
	p.loading = false
	if msg.err != nil {
		p.err = msg.err.Error()
		return
	}
	if p.field == "tenant" {
		p.items = append([]netbox.Choice{{Name: "", Detail: "no tenant"}}, msg.items...)
	} else {
		p.items = msg.items
	}
	if cur := m.nb.fieldText(p.field); cur != "" && p.field != "custom_fields" {
		for i, it := range p.visible() {
			if it.Name == cur {
				p.sel = i
			}
		}
	}
}

// pickRow is one row of the list: an item, or the typed new name.
type pickRow struct {
	netbox.Choice
	isNew bool
}

func (p *nbPicker) visible() []pickRow {
	q := strings.ToLower(strings.TrimSpace(p.filter.Value()))
	var out []pickRow
	exact := false
	for _, it := range p.items {
		if q == "" || strings.Contains(strings.ToLower(it.Name), q) || strings.Contains(strings.ToLower(it.Detail), q) {
			out = append(out, pickRow{Choice: it})
		}
		if strings.EqualFold(it.Name, strings.TrimSpace(p.filter.Value())) {
			exact = true
		}
	}
	if p.allowNew && q != "" && !exact {
		out = append(out, pickRow{Choice: netbox.Choice{Name: strings.TrimSpace(p.filter.Value()), Detail: p.newNote}, isNew: true})
	}
	return out
}

func (m *Model) updatePicker(msg tea.KeyPressMsg) tea.Cmd {
	d, p := m.nb, m.nb.picker
	rows := p.visible()
	switch msg.String() {
	case "esc":
		d.picker = nil
	case "up":
		p.sel = max(0, p.sel-1)
	case "down":
		p.sel = min(max(0, len(rows)-1), p.sel+1)
	case "pgup":
		p.sel = max(0, p.sel-10)
	case "pgdown":
		p.sel = min(max(0, len(rows)-1), p.sel+10)
	case "tab":
		if !p.multi && p.field != "tag" { // type it yourself
			d.picker = nil
			d.editing = true
			d.input.SetValue(d.fieldText(p.field))
			d.input.Focus()
		}
	case "space", " ":
		if p.multi {
			if p.sel < len(rows) {
				p.chosen[rows[p.sel].Name] = !p.chosen[rows[p.sel].Name]
			}
			return nil
		}
		p.filter.Insert(" ")
		p.sel = 0
	case "enter":
		if p.multi {
			var cats []string
			for _, it := range p.items {
				if p.chosen[it.Name] {
					cats = append(cats, it.Name)
				}
			}
			if len(cats) == len(p.items) {
				cats = nil // all: stays all when Tyr gains a category
			}
			d.cfg.Categories, d.dirty, d.picker = cats, true, nil
			return nil
		}
		if p.loading || p.sel >= len(rows) {
			return nil
		}
		m.pick(rows[p.sel].Name)
	default:
		p.filter.Update(msg)
		p.sel = 0
	}
	return nil
}

// pick applies the chosen value.
func (m *Model) pick(v string) {
	d, p := m.nb, m.nb.picker
	d.picker = nil
	switch p.field {
	case "site":
		d.cfg.Site = v
	case "role":
		d.cfg.Role = v
	case "tenant":
		d.cfg.Tenant = v
	case "tag":
		d.ruleTag.SetValue(v)
		d.editPart = 1
		return
	case "custom_fields": // the name; the template is typed next
		cur := customFieldsText(d.cfg.CustomFields)
		if _, ok := d.cfg.CustomFields[v]; !ok {
			if cur != "" {
				cur += ", "
			}
			cur += v + "="
		}
		d.editing = true
		d.input.SetValue(cur)
		d.input.Focus()
		d.msg = "type the value after " + v + "= ; {fact} is replaced, e.g. {memory_gb} GB"
		return
	}
	d.dirty = true
}

func (m *Model) pickerLines(inner int) []string {
	t, p := m.theme, m.nb.picker
	src := "from NetBox"
	if p.multi {
		src = "space toggles, enter takes"
	}
	lines := []string{t.Bold.Render(p.title) + t.Sub.Render("  "+src), ""}
	lines = append(lines, " "+p.filter.Field(t, max(20, min(50, inner-20)))+t.Sub.Render("  type to search"), "")
	switch {
	case p.loading:
		lines = append(lines, " "+m.spin.View()+" loading from "+m.nb.cfg.URL+" …")
	case p.err != "":
		lines = append(lines, " "+t.StatusText(model.StatusCrit, "✖ "+p.err))
		if p.field != "tag" {
			lines = append(lines, " "+t.Sub.Render("tab: type the value yourself"))
		}
	}
	rows := p.visible()
	const show = 14
	top := max(0, min(p.sel-show/2, len(rows)-show))
	nw := 8
	for _, r := range rows {
		nw = max(nw, min(32, len([]rune(r.Name))+2))
	}
	for i := top; i < min(len(rows), top+show); i++ {
		r := rows[i]
		name := r.Name
		if name == "" {
			name = "(none)"
		}
		mark := ""
		if p.multi {
			mark = "[ ] "
			if p.chosen[r.Name] {
				mark = "[x] "
			}
		}
		if r.isNew {
			name = "+ " + name
		}
		row := mark + pad(truncate(name, nw), nw) + " " + t.Sub.Render(truncate(r.Detail, max(10, inner-nw-8)))
		if i == p.sel {
			row = lipgloss.NewStyle().Foreground(t.Accent).Render("▌") + row
		} else {
			row = " " + row
		}
		lines = append(lines, row)
	}
	if !p.loading && len(rows) == 0 && p.err == "" {
		lines = append(lines, " "+t.Sub.Render("nothing matches"))
	}
	if len(rows) > show {
		lines = append(lines, " "+t.Sub.Render(fmt.Sprintf("%d of %d", p.sel+1, len(rows))))
	}
	help := "↑↓ choose · enter take · tab type it yourself · esc back"
	switch {
	case p.multi:
		help = "↑↓ move · space toggle · enter take · esc back"
	case p.field == "tag":
		help = "↑↓ choose · enter take (a new name creates the tag) · esc back"
	}
	lines = append(lines, "", t.Sub.Render(help))
	return lines
}

// --- paste and copy ------------------------------------------------------------

// oneLine makes pasted text fit a single-line field.
func oneLine(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(s)
	return strings.TrimSpace(s)
}

// paste puts pasted text (bracketed paste, or the clipboard read with
// ctrl+v) into the input that has the focus.
func (m *Model) paste(text string) tea.Cmd {
	text = oneLine(text)
	if text == "" {
		return nil
	}
	switch {
	case m.nb != nil:
		m.nb.paste(text)
	case m.wh != nil:
		m.wh.paste(text)
	case m.reports != nil:
		if m.reports.editing {
			m.reports.input.Insert(text)
		}
	case m.page != nil:
		if m.page.capturing() {
			m.page.update(tea.KeyPressMsg{Text: text})
		}
	default:
		if c, ok := m.tabs[m.active].(capturer); ok && c.Capturing() {
			return m.tabs[m.active].Update(tea.KeyPressMsg{Text: text})
		}
	}
	return nil
}

func (d *netboxDialog) paste(text string) {
	switch {
	case d.picker != nil:
		d.picker.filter.Insert(text)
		d.picker.sel = 0
	case d.editing && d.page == 1:
		if d.editPart == 0 {
			d.ruleTag.Insert(text)
		} else {
			d.ruleWhen.Insert(text)
		}
		d.err = ""
	case d.editing:
		d.input.Insert(text)
	case d.page == 0:
		// pasted onto a text field: edit it, the pasted text in it
		f := nbFields[d.field].id
		if textOf(&d.cfg, f) == nil && f != "categories" && f != "custom_fields" {
			return
		}
		v := d.fieldText(f)
		if f == "url" || f == "token" {
			v = "" // a pasted URL or token replaces the old one
		}
		d.editing = true
		d.input.SetValue(v)
		d.input.Insert(text)
		d.input.Focus()
	}
}

// editingValue is the text of the input being edited, for ctrl+c.
func (m *Model) editingValue() (string, bool) {
	switch d := m.nb; {
	case d != nil && d.picker != nil:
		return d.picker.filter.Value(), true
	case d != nil && d.editing && d.page == 1:
		if d.editPart == 0 {
			return d.ruleTag.Value(), true
		}
		return d.ruleWhen.Value(), true
	case d != nil && d.editing:
		return d.input.Value(), true
	case m.reports != nil && m.reports.editing:
		return m.reports.input.Value(), true
	case m.wh != nil && m.wh.typing:
		return m.wh.input.Value(), true
	}
	return "", false
}
