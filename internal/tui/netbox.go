package tui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/netbox"
)

// netboxDialog (N) edits the NetBox settings and the tag rules, tests
// the connection and writes this machine to NetBox.
type netboxDialog struct {
	page    int // 0 connection, 1 tags
	cfg     netbox.Config
	tags    netbox.Tags
	field   int
	action  int // the button on the actions row
	editing bool
	input   lineInput
	// tags page
	sel       int
	editRule  int // index being edited, len(rules) = a new rule
	editPart  int // 0 tag, 1 condition
	ruleTag   lineInput
	ruleWhen  lineInput
	showFacts bool
	factsTop  int

	dirty   bool
	confirm bool // esc once with unsaved changes
	busy    string
	msg     string
	err     string
}

var nbPages = []string{"Connection", "Tag rules"}

type nbField struct {
	id, label, help string
}

var nbFields = []nbField{
	{"url", "URL", "https://netbox.example.com (without /api)"},
	{"token", "Token", "API token with write access to devices, interfaces, IPs, inventory, tags, journal"},
	{"insecure", "TLS", "skip certificate checks (self-signed NetBox)"},
	{"site", "Site", "for devices not yet in NetBox (found by serial number); created if missing"},
	{"role", "Role", "for new devices; created if missing"},
	{"tenant", "Tenant", "for new devices; optional, must exist"},
	{"name", "New name", "name of a new device"},
	{"status", "New status", "status of a new device"},
	{"interfaces", "Interfaces", "NICs with MAC addresses, the BMC interface and its IP (out-of-band)"},
	{"inventory", "Inventory", "an inventory item per component: part number, serial, firmware"},
	{"mark_failed", "Mark failed", "failed parts and a FAIL verdict set status failed (back to active when healthy)"},
	{"journal", "Journal", "a journal entry with the test result or the health check"},
	{"prune", "Prune", "remove inventory items Tyr wrote that are no longer there"},
	{"after_accept", "After test", "write automatically after every tyr accept (the ISO)"},
	{"categories", "Categories", "inventory categories written as items, comma-separated; empty = all"},
	{"custom_fields", "Custom fields", "name=template, …; {fact} is replaced: memory={memory_gb} GB"},
	{"actions", "", ""},
}

var nbActions = []string{"Save", "Test", "Dry run", "Write to NetBox"}
var nbNames = []string{"hostname", "serial"}
var nbStatuses = []string{"active", "planned", "staged", "offline", "inventory"}

func (m *Model) openNetBox() {
	d := &netboxDialog{input: lineInput{limit: 1024}, ruleTag: lineInput{limit: 100}, ruleWhen: lineInput{limit: 1024}}
	np, tp := netbox.Paths("")
	if m.opts.NetBoxFile != "" {
		np = m.opts.NetBoxFile
	}
	if m.opts.TagsFile != "" {
		tp = m.opts.TagsFile
	}
	var errs []string
	cfg, err := netbox.LoadConfig(np)
	if err != nil {
		errs = append(errs, err.Error())
	}
	tags, err := netbox.LoadTags(tp)
	if err != nil {
		errs = append(errs, err.Error())
	}
	d.cfg, d.tags = cfg, tags
	d.cfg.Path, d.tags.Path = np, tp
	d.err = strings.Join(errs, "; ")
	m.nb = d
}

func boolOf(c *netbox.Config, id string) *bool {
	switch id {
	case "insecure":
		return &c.Insecure
	case "interfaces":
		return &c.Interfaces
	case "inventory":
		return &c.Inventory
	case "mark_failed":
		return &c.MarkFailed
	case "journal":
		return &c.Journal
	case "prune":
		return &c.Prune
	case "after_accept":
		return &c.AfterAccept
	}
	return nil
}

func textOf(c *netbox.Config, id string) *string {
	switch id {
	case "url":
		return &c.URL
	case "token":
		return &c.Token
	case "site":
		return &c.Site
	case "role":
		return &c.Role
	case "tenant":
		return &c.Tenant
	}
	return nil
}

func customFieldsText(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ", ")
}

func parseCustomFields(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		k, v, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("custom field %q: name=template", p)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// fieldText is the editable text of a field.
func (d *netboxDialog) fieldText(id string) string {
	switch id {
	case "categories":
		return strings.Join(d.cfg.Categories, ", ")
	case "custom_fields":
		return customFieldsText(d.cfg.CustomFields)
	}
	if p := textOf(&d.cfg, id); p != nil {
		return *p
	}
	return ""
}

func (d *netboxDialog) setFieldText(id, v string) error {
	v = strings.TrimSpace(v)
	switch id {
	case "categories":
		var cats []string
		for _, c := range strings.Split(v, ",") {
			if c = strings.TrimSpace(c); c != "" {
				if !slices.Contains(netbox.DefaultCategories, c) && !slices.Contains(model.InventoryCategories, c) {
					return fmt.Errorf("unknown category %q (%s)", c, strings.Join(netbox.DefaultCategories, ", "))
				}
				cats = append(cats, c)
			}
		}
		d.cfg.Categories = cats
	case "custom_fields":
		cf, err := parseCustomFields(v)
		if err != nil {
			return err
		}
		d.cfg.CustomFields = cf
	case "url":
		d.cfg.URL = strings.TrimRight(strings.TrimSuffix(strings.TrimRight(v, "/"), "/api"), "/")
	default:
		if p := textOf(&d.cfg, id); p != nil {
			*p = v
		}
	}
	return nil
}

type netboxDoneMsg struct {
	test    bool
	dry     bool
	version string
	log     []string
	rep     *netbox.Report
	err     error
}

func (m *Model) updateNetBox(msg tea.KeyPressMsg) tea.Cmd {
	d := m.nb
	k := msg.String()
	if d.busy != "" {
		if k == "esc" {
			m.nb = nil // the request finishes in the background
		}
		return nil
	}
	if d.editing {
		return m.updateNetBoxEdit(msg)
	}
	if k != "esc" {
		d.confirm = false
	}
	switch k {
	case "esc", "q":
		if d.dirty && !d.confirm {
			d.confirm = true
			d.msg = "unsaved changes: esc again to discard, or Save"
			return nil
		}
		m.nb = nil
		return nil
	case "tab", "shift+tab":
		d.page = 1 - d.page
		d.msg, d.err = "", ""
		return nil
	}
	if d.page == 1 {
		return m.updateNetBoxTags(msg)
	}
	f := nbFields[d.field].id
	step := func(i, delta, n int) int { return ((i+delta)%n + n) % n }
	change := func(delta int) {
		if b := boolOf(&d.cfg, f); b != nil {
			*b = !*b
			d.dirty = true
			return
		}
		switch f {
		case "name":
			d.cfg.Name = nbNames[step(slices.Index(nbNames, d.cfg.Name), delta, len(nbNames))]
			d.dirty = true
		case "status":
			d.cfg.Status = nbStatuses[step(max(0, slices.Index(nbStatuses, d.cfg.Status)), delta, len(nbStatuses))]
			d.dirty = true
		case "actions":
			d.action = step(d.action, delta, len(nbActions))
		}
	}
	switch k {
	case "up", "k":
		d.field = max(0, d.field-1)
	case "down", "j":
		d.field = min(len(nbFields)-1, d.field+1)
	case "left", "h":
		change(-1)
	case "right", "l", "space", " ":
		change(+1)
	case "ctrl+s":
		m.saveNetBox()
	case "enter":
		switch {
		case f == "actions":
			return m.netboxAction()
		case boolOf(&d.cfg, f) != nil || f == "name" || f == "status":
			change(+1)
		default:
			d.editing = true
			d.input.SetValue(d.fieldText(f))
			d.input.Focus()
		}
	}
	return nil
}

func (m *Model) updateNetBoxEdit(msg tea.KeyPressMsg) tea.Cmd {
	d := m.nb
	if d.page == 1 {
		in := &d.ruleTag
		if d.editPart == 1 {
			in = &d.ruleWhen
		}
		switch msg.String() {
		case "esc":
			d.editing = false
		case "up":
			d.factsTop = max(0, d.factsTop-1)
		case "down":
			d.factsTop = min(len(netbox.FactDefs)-1, d.factsTop+1)
		case "tab", "shift+tab":
			d.editPart = 1 - d.editPart
		case "enter":
			if d.editPart == 0 {
				d.editPart = 1
				return nil
			}
			r := netbox.TagRule{Tag: strings.TrimSpace(d.ruleTag.Value()), When: strings.TrimSpace(d.ruleWhen.Value())}
			rules := slices.Clone(d.tags.Rules)
			if d.editRule >= len(rules) {
				rules = append(rules, r)
			} else {
				rules[d.editRule] = r
			}
			if err := (netbox.Tags{Rules: rules}).Validate(); err != nil {
				d.err = err.Error()
				return nil
			}
			d.tags.Rules, d.sel, d.editing, d.dirty, d.err = rules, min(d.editRule, len(rules)-1), false, true, ""
		default:
			in.Update(msg)
			d.err = ""
		}
		return nil
	}
	switch msg.String() {
	case "esc":
		d.editing = false
		d.input.Blur()
	case "enter":
		if err := d.setFieldText(nbFields[d.field].id, d.input.Value()); err != nil {
			d.err = err.Error()
			return nil
		}
		d.editing, d.dirty, d.err = false, true, ""
		d.input.Blur()
	default:
		d.input.Update(msg)
	}
	return nil
}

func (m *Model) updateNetBoxTags(msg tea.KeyPressMsg) tea.Cmd {
	d := m.nb
	n := len(d.tags.Rules)
	edit := func(i int) {
		d.editRule, d.editPart, d.editing, d.err = i, 0, true, ""
		d.ruleTag.SetValue("")
		d.ruleWhen.SetValue("")
		if i < n {
			d.ruleTag.SetValue(d.tags.Rules[i].Tag)
			d.ruleWhen.SetValue(d.tags.Rules[i].When)
			d.editPart = 1
		}
	}
	switch msg.String() {
	case "up", "k":
		if d.showFacts {
			d.factsTop = max(0, d.factsTop-1)
		} else {
			d.sel = max(0, d.sel-1)
		}
	case "down", "j":
		if d.showFacts {
			d.factsTop = min(len(netbox.FactDefs)-1, d.factsTop+1)
		} else {
			d.sel = min(max(0, n-1), d.sel+1)
		}
	case "a", "+":
		edit(n)
	case "enter", "e":
		if n > 0 {
			edit(d.sel)
		} else {
			edit(0)
		}
	case "d", "delete", "x":
		if n > 0 {
			d.tags.Rules = slices.Delete(slices.Clone(d.tags.Rules), d.sel, d.sel+1)
			d.sel = max(0, min(d.sel, len(d.tags.Rules)-1))
			d.dirty = true
		}
	case "K", "shift+up":
		if d.sel > 0 {
			d.tags.Rules[d.sel-1], d.tags.Rules[d.sel] = d.tags.Rules[d.sel], d.tags.Rules[d.sel-1]
			d.sel--
			d.dirty = true
		}
	case "J", "shift+down":
		if d.sel < n-1 {
			d.tags.Rules[d.sel+1], d.tags.Rules[d.sel] = d.tags.Rules[d.sel], d.tags.Rules[d.sel+1]
			d.sel++
			d.dirty = true
		}
	case "f":
		d.showFacts = !d.showFacts
	case "s", "ctrl+s":
		m.saveNetBox()
	}
	return nil
}

func (m *Model) saveNetBox() {
	d := m.nb
	d.msg, d.err = "", ""
	if err := d.cfg.Save(); err != nil {
		d.err = fmt.Sprintf("%s: %v", d.cfg.Path, err)
		return
	}
	if err := d.tags.Save(); err != nil {
		d.err = fmt.Sprintf("%s: %v", d.tags.Path, err)
		return
	}
	d.dirty = false
	d.msg = "saved " + d.cfg.Path + " and " + d.tags.Path
}

func (m *Model) netboxAction() tea.Cmd {
	d := m.nb
	d.msg, d.err = "", ""
	switch d.action {
	case 0:
		m.saveNetBox()
		return nil
	case 1:
		if err := d.cfg.Validate(); err != nil {
			d.err = err.Error()
			return nil
		}
		cfg := d.cfg
		d.busy = "testing " + cfg.URL + " …"
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			v, err := netbox.NewClient(cfg, nil).Status(ctx)
			return netboxDoneMsg{test: true, version: v, err: err}
		}
	}
	if m.snap == nil {
		d.err = "no snapshot yet"
		return nil
	}
	if err := d.cfg.Validate(); err != nil {
		d.err = err.Error()
		return nil
	}
	if err := d.tags.Validate(); err != nil {
		d.err = err.Error()
		return nil
	}
	cfg, tags, snap, dry := d.cfg, d.tags, m.snap, d.action == 2
	d.busy = map[bool]string{true: "dry run against ", false: "writing to "}[dry] + cfg.URL + " …"
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		var log []string
		rep, err := netbox.Sync(ctx, cfg, tags, netbox.Input{Snap: snap}, dry, func(f string, a ...any) {
			log = append(log, fmt.Sprintf(f, a...))
		})
		return netboxDoneMsg{dry: dry, log: log, rep: rep, err: err}
	}
}

// netboxDone shows the result: a test in the dialog, a sync as a page.
func (m *Model) netboxDone(msg netboxDoneMsg) {
	d := m.nb
	if d != nil {
		d.busy = ""
	}
	if msg.test {
		if d == nil {
			return
		}
		if msg.err != nil {
			d.err = msg.err.Error()
		} else {
			d.msg = fmt.Sprintf("NetBox %s, token accepted", msg.version)
		}
		return
	}
	var b strings.Builder
	title := "NetBox"
	if msg.dry {
		title = "NetBox dry run"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	if msg.err != nil {
		fmt.Fprintf(&b, "**Not written:** %s\n\n", msg.err)
	}
	if r := msg.rep; r != nil {
		if r.DeviceURL != "" {
			fmt.Fprintf(&b, "Device **%s**: %s\n\n", r.Device, r.DeviceURL)
		}
		if len(r.Tags) > 0 {
			fmt.Fprintf(&b, "Tags: %s\n\n", strings.Join(r.Tags, ", "))
		}
		if len(r.Problems) > 0 {
			b.WriteString("## Problems\n\n")
			for _, p := range r.Problems {
				fmt.Fprintf(&b, "- %s\n", p)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("## Log\n\n```text\n" + strings.Join(msg.log, "\n") + "\n```\n")
	m.nb = nil
	m.page = markdownPage(m.theme, "N NetBox", title, b.String(), m.width, m.contentHeight()+3)
	switch {
	case msg.err != nil:
		m.flash("NetBox: not written")
	case msg.dry:
		m.flash("NetBox dry run: nothing written")
	default:
		m.flash("written to NetBox")
	}
}

// netboxView draws the dialog over the dimmed screen.
func (m *Model) netboxView(base string) string {
	t, d := m.theme, m.nb
	var pills []string
	for i, p := range nbPages {
		pills = append(pills, pill(t, p, i == d.page))
	}
	lines := []string{strings.Join(pills, "") + "  " + t.Sub.Render("tab to switch"), ""}
	inner := min(96, m.width-4) - 4
	if d.page == 0 {
		lines = append(lines, m.netboxConnLines(inner)...)
	} else {
		lines = append(lines, m.netboxTagLines(inner)...)
	}
	if d.busy != "" {
		lines = append(lines, "", m.spin.View()+" "+d.busy)
	}
	if d.err != "" {
		lines = append(lines, "", t.StatusText(model.StatusCrit, "✖ "+d.err))
	} else if d.msg != "" {
		lines = append(lines, "", t.Sub.Render(d.msg))
	}
	right := "saved"
	if d.dirty {
		right = "unsaved changes"
	}
	return overlayR(t, base, t.Accent, "NetBox", right, strings.Join(lines, "\n"), m.width, m.height)
}

func (m *Model) netboxConnLines(inner int) []string {
	t, d := m.theme, m.nb
	const lw = 15
	onOff := func(b bool) string {
		return pill(t, "off", !b) + pill(t, "on", b)
	}
	var lines []string
	for i, f := range nbFields {
		focused := i == d.field
		var v string
		switch {
		case f.id == "actions":
			var bs []string
			for j, a := range nbActions {
				bs = append(bs, button(t, a, focused && j == d.action))
			}
			lines = append(lines, "", strings.Repeat(" ", lw)+strings.Join(bs, "  "))
			continue
		case boolOf(&d.cfg, f.id) != nil:
			v = onOff(*boolOf(&d.cfg, f.id))
			if f.id == "insecure" {
				v = pill(t, "verify", !d.cfg.Insecure) + pill(t, "insecure", d.cfg.Insecure)
			}
		case f.id == "name":
			for _, n := range nbNames {
				v += pill(t, n, d.cfg.Name == n)
			}
		case f.id == "status":
			for _, s := range nbStatuses {
				v += pill(t, s, d.cfg.Status == s)
			}
		default:
			txt := d.fieldText(f.id)
			if f.id == "token" && txt != "" {
				txt = strings.Repeat("•", min(12, len(txt))) + " (" + fmt.Sprint(len(txt)) + " characters)"
			}
			w := max(20, inner-lw-2)
			if d.editing && focused {
				v = d.input.Field(t, w)
			} else {
				in := lineInput{value: []rune(txt)}
				v = in.Field(t, w)
			}
		}
		lines = append(lines, fieldLabel(t, f.label, focused, lw)+v)
		if focused && f.help != "" {
			lines = append(lines, strings.Repeat(" ", lw)+t.Sub.Render(truncate(f.help, inner-lw)))
		}
	}
	lines = append(lines, "", t.Sub.Render(truncate("files: "+d.cfg.Path+" · "+d.tags.Path, inner)))
	if d.cfg.Path != "" && !strings.HasPrefix(d.cfg.Path, netbox.SystemDir) {
		lines = append(lines, t.Sub.Render(truncate("as root (tyr accept, the ISO) Tyr reads "+netbox.SystemDir+"/netbox.json", inner)))
	}
	lines = append(lines, t.Sub.Render("↑↓ field · ←→ change · enter edit / run · ctrl+s save · esc close"))
	return lines
}

func (m *Model) netboxTagLines(inner int) []string {
	t, d := m.theme, m.nb
	facts := netbox.ComputeFacts(m.snap, nil)
	var lines []string
	if d.editing {
		lw := 11
		tag, when := d.ruleTag, d.ruleWhen
		tag.focused, when.focused = d.editPart == 0, d.editPart == 1
		title := "New rule"
		if d.editRule < len(d.tags.Rules) {
			title = "Edit rule"
		}
		lines = append(lines, t.Bold.Render(title), "",
			fieldLabel(t, "Tag", d.editPart == 0, lw)+tag.Field(t, max(20, inner-lw-2)),
			fieldLabel(t, "When", d.editPart == 1, lw)+when.Field(t, max(20, inner-lw-2)))
		if e, err := netbox.Parse(when.Value()); err == nil {
			ok, err := e.Eval(facts)
			switch {
			case err != nil:
				lines = append(lines, strings.Repeat(" ", lw)+t.StatusText(model.StatusWarn, err.Error()))
			case ok:
				lines = append(lines, strings.Repeat(" ", lw)+t.StatusText(model.StatusOK, "✔ matches this machine now"))
			default:
				lines = append(lines, strings.Repeat(" ", lw)+t.Sub.Render("· does not match this machine now"))
			}
		} else if strings.TrimSpace(when.Value()) != "" {
			lines = append(lines, strings.Repeat(" ", lw)+t.Sub.Render(err.Error()))
		}
		lines = append(lines, "", t.Sub.Render("&& || ! ( )  == != < <= > >=  ~ !~ (regular expression)  \"text\"  numbers  true false"))
		lines = append(lines, m.factLines(facts, inner, 8)...)
		lines = append(lines, "", t.Sub.Render("tab tag ↔ condition · ↑↓ facts · enter next / save · esc cancel"))
		return lines
	}
	if len(d.tags.Rules) == 0 {
		lines = append(lines, t.Sub.Render("No rules: Tyr sets no tags. a adds one, e.g."),
			t.Sub.Render("  hw-memory   memory_gb >= 192"),
			t.Sub.Render("  defect      verdict == \"FAIL\" || failed_parts > 0"))
	}
	tw := 4
	for _, r := range d.tags.Rules {
		tw = max(tw, min(24, len([]rune(r.Tag))))
	}
	res := d.tags.Evaluate(facts)
	for i, r := range res {
		mark := t.Sub.Render("·  -  ")
		switch {
		case r.Err != nil:
			mark = t.StatusText(model.StatusCrit, "✖ err ")
		case r.Match:
			mark = t.StatusText(model.StatusOK, "✔ set ")
		}
		row := mark + pad(truncate(r.Rule.Tag, tw), tw) + "  " + truncate(r.Rule.When, inner-tw-10)
		if i == d.sel && !d.showFacts {
			row = lipgloss.NewStyle().Foreground(t.Accent).Render("▌") + row
		} else {
			row = " " + row
		}
		lines = append(lines, row)
	}
	lines = append(lines, "", t.Sub.Render("✔ set: the rule matches this machine now; the tag comes off when it no longer does. Other tags stay."))
	if d.showFacts {
		lines = append(lines, m.factLines(facts, inner, 14)...)
	}
	lines = append(lines, "", t.Sub.Render(truncate("file: "+d.tags.Path, inner)),
		t.Sub.Render("a add · enter edit · d delete · J K move · f facts · s save · esc close"))
	return lines
}

// factLines lists the facts with their values on this machine.
func (m *Model) factLines(f netbox.Facts, inner, n int) []string {
	t, d := m.theme, m.nb
	lines := []string{"", t.Bold.Render("Facts") + t.Sub.Render(fmt.Sprintf("  this machine now · ↑↓ scroll (%d)", len(netbox.FactDefs)))}
	top := min(d.factsTop, max(0, len(netbox.FactDefs)-n))
	for _, fd := range netbox.FactDefs[top:min(len(netbox.FactDefs), top+n)] {
		v := truncate(f[fd.Name].String(), 26)
		lines = append(lines, " "+t.Key.Render(pad(fd.Name, 17))+pad(v, 27)+t.Sub.Render(truncate(fd.Help, max(10, inner-46))))
	}
	return lines
}
