package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/report"
	"github.com/noahzmr/tyr/internal/stress"
)

// reportDialog generates reports from the TUI (R): the current host's
// hardware and alerts, a stress result, or the history of saved JSON;
// as Markdown or JSON; shown as a page, saved to a file, or both.
type reportDialog struct {
	kind    int // 0 host, 1 stress result, 2 history
	run     int // index into runs
	runs    []*stress.Result
	format  int // 0 Markdown, 1 JSON
	output  int // 0 view, 1 save, 2 view + save
	field   int
	editing bool
	input   lineInput
	paths   string // history sources: comma-separated files/directories
	saveTo  string // "" = generated name in the export directory
	err     string
}

var (
	reportKinds   = []string{"Host report", "Stress result", "History"}
	reportFormats = []string{"Markdown", "JSON"}
	reportOutputs = []string{"View", "Save", "View + save"}
)

// openReports opens the dialog; on a stress result it starts there.
func (m *Model) openReports() {
	d := &reportDialog{input: lineInput{limit: 512}}
	seen := map[string]bool{}
	st := m.stressTab()
	if st != nil {
		for _, r := range st.results {
			if r.RunID != "" && !seen[r.RunID] {
				seen[r.RunID] = true
				d.runs = append(d.runs, r)
			}
		}
		for _, h := range stress.History(st.env, 30) {
			if !seen[h.RunID] {
				seen[h.RunID] = true
				r := h
				d.runs = append(d.runs, &r)
			}
		}
		if t, ok := m.tabs[m.active].(*stressTab); ok && t.state == stResult && len(d.runs) > 0 {
			d.kind = 1
		}
	}
	// history sources: where Tyr writes JSON, if those exist
	var dirs []string
	cands := []string{m.opts.ExportDir}
	if st != nil {
		cands = append(cands, stress.RunDirs(st.env)...)
	}
	for _, c := range cands {
		if c == "" {
			c = "."
		}
		if fi, err := os.Stat(c); err == nil && fi.IsDir() && !containsStr(dirs, c) {
			dirs = append(dirs, c)
		}
	}
	d.paths = strings.Join(dirs, ", ")
	m.reports = d
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// fields lists the dialog rows for the chosen report.
func (d *reportDialog) fields() []string {
	f := []string{"kind"}
	switch d.kind {
	case 1:
		f = append(f, "run")
	case 2:
		f = append(f, "paths")
	}
	f = append(f, "format", "output")
	if d.output > 0 {
		f = append(f, "saveto")
	}
	return append(f, "generate")
}

func (d *reportDialog) text(f string) string {
	if f == "paths" {
		return d.paths
	}
	return d.saveTo
}

// update handles a key; it returns a command and whether the dialog
// stays open.
func (m *Model) updateReports(msg tea.KeyPressMsg) tea.Cmd {
	d := m.reports
	fields := d.fields()
	d.field = min(d.field, len(fields)-1)
	f := fields[d.field]
	if d.editing {
		switch msg.String() {
		case "enter", "esc":
			if msg.String() == "enter" {
				v := strings.TrimSpace(d.input.Value())
				if f == "paths" {
					d.paths = v
				} else {
					d.saveTo = v
				}
			}
			d.editing = false
			d.input.Blur()
		default:
			d.input.Update(msg)
		}
		return nil
	}
	step := func(i, delta, n int) int { return ((i+delta)%n + n) % n }
	change := func(delta int) {
		d.err = ""
		switch f {
		case "kind":
			d.kind = step(d.kind, delta, len(reportKinds))
		case "run":
			if len(d.runs) > 0 {
				d.run = step(d.run, delta, len(d.runs))
			}
		case "format":
			d.format = step(d.format, delta, len(reportFormats))
		case "output":
			d.output = step(d.output, delta, len(reportOutputs))
		}
	}
	switch msg.String() {
	case "esc", "q":
		m.reports = nil
	case "up", "k":
		d.field = max(0, d.field-1)
	case "down", "j", "tab":
		d.field = min(len(fields)-1, d.field+1)
	case "left", "h":
		change(-1)
	case "right", "l", "space", " ":
		change(+1)
	case "enter":
		switch f {
		case "paths", "saveto":
			d.editing = true
			v := d.text(f)
			if f == "saveto" && v == "" {
				v = m.reportFile(d)
			}
			d.input.SetValue(v)
			d.input.Focus()
		case "generate":
			m.generateReport()
		default:
			change(+1)
		}
	}
	return nil
}

// reportFile is the default file name: tyr-<kind>-<host>-<time>.<ext>.
func (m *Model) reportFile(d *reportDialog) string {
	host := "host"
	if m.snap != nil {
		host = m.snap.Hostname
	}
	kind := []string{"report", "stress", "history"}[d.kind]
	ext := []string{".md", ".json"}[d.format]
	dir := m.opts.ExportDir
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, fmt.Sprintf("tyr-%s-%s-%s%s", kind, host, time.Now().UTC().Format("20060102T150405Z"), ext))
}

// generateReport builds the report, then views and/or saves it.
func (m *Model) generateReport() {
	d := m.reports
	d.err = ""
	// saved Markdown carries Mermaid charts; the page shows sparklines
	title, md, js, err := m.buildReport(d, report.Options{})
	if err != nil {
		d.err = err.Error()
		return
	}
	_, termMD, _, _ := m.buildReport(d, report.Options{Terminal: true})
	content := md
	if d.format == 1 {
		content = js
	}
	saved := ""
	if d.output > 0 {
		p := d.saveTo
		if p == "" {
			p = m.reportFile(d)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			d.err = "save failed: " + err.Error()
			return
		}
		saved = p
	}
	m.reports = nil
	if saved != "" {
		m.flash("saved " + saved)
	}
	if d.output != 1 {
		view := termMD
		if d.format == 1 { // JSON shown as a highlighted code block
			view = "```json\n" + js + "\n```\n"
		}
		m.page = markdownPage(m.theme, "R reports", title, view, m.width, m.contentHeight()+3)
	}
}

// buildReport returns the title, Markdown and JSON of the chosen report.
func (m *Model) buildReport(d *reportDialog, o report.Options) (string, string, string, error) {
	enc := func(v any) (string, error) {
		b, err := json.MarshalIndent(v, "", "  ")
		return string(b) + "\n", err
	}
	switch d.kind {
	case 1:
		if len(d.runs) == 0 {
			return "", "", "", fmt.Errorf("no stress results yet: run a test on the Stress tab, or choose History")
		}
		r := d.runs[d.run]
		if r.Baseline == nil && r.File != "" { // history entries come without snapshots
			if doc, err := report.LoadOne(r.File); err == nil && doc.Run != nil {
				r = doc.Run
			}
		}
		js, err := enc(r)
		return "stress result " + string(r.Component), report.RunWith(r, o), js, err
	case 2:
		var paths []string
		for _, p := range strings.Split(d.paths, ",") {
			if p = strings.TrimSpace(p); p != "" {
				paths = append(paths, p)
			}
		}
		if len(paths) == 0 {
			return "", "", "", fmt.Errorf("enter files or directories with saved JSON")
		}
		docs, skipped, err := report.Load(paths)
		if err != nil {
			return "", "", "", err
		}
		if len(docs) == 0 {
			return "", "", "", fmt.Errorf("no Tyr snapshots or stress results in %s", strings.Join(paths, ", "))
		}
		hs := report.BuildHistory(docs)
		js, err := enc(hs)
		return "history", report.HistoryMarkdownWith(hs, skipped, o), js, err
	}
	if m.snap == nil {
		return "", "", "", fmt.Errorf("no snapshot yet")
	}
	js, err := enc(m.snap)
	return m.snap.Hostname + " report", report.Snapshot(m.snap), js, err
}

// runLabel names a stress result in the dialog.
func runLabel(r *stress.Result) string {
	day := r.StartedAt.Local().Format("Jan 2 15:04")
	if y, mo, dd := time.Now().Date(); r.StartedAt.Local().Year() == y && r.StartedAt.Local().Month() == mo && r.StartedAt.Local().Day() == dd {
		day = "today " + r.StartedAt.Local().Format("15:04")
	}
	return fmt.Sprintf("%s · %s · %s", compTitle(r.Component), r.Verdict, day)
}

// reportsView draws the dialog over the dimmed screen.
func (m *Model) reportsView(base string) string {
	t, d := m.theme, m.reports
	const lw = 11
	var lines []string
	row := func(i int, label, value, hint string) {
		l := fieldLabel(t, label, i == d.field, lw) + value
		if hint != "" {
			l += "  " + t.Sub.Render(hint)
		}
		lines = append(lines, l)
	}
	pills := func(names []string, sel int) string {
		var ps []string
		for i, n := range names {
			ps = append(ps, pill(t, n, i == sel))
		}
		return strings.Join(ps, "")
	}
	field := func(f, v string, w int) string {
		if d.editing && d.fields()[d.field] == f {
			return d.input.Field(t, w)
		}
		in := lineInput{value: []rune(v)}
		return in.Field(t, w)
	}
	inner := min(96, m.width-4) - 4
	for i, f := range d.fields() {
		switch f {
		case "kind":
			row(i, "Report", pills(reportKinds, d.kind), "")
			lines = append(lines, strings.Repeat(" ", lw)+t.Sub.Render([]string{
				"hardware, alerts and inventory of this host, as collected now",
				"verdict, metrics, wrong results and log of one run",
				"status, alert timeline, trends, hardware changes and runs from saved JSON",
			}[d.kind]), "")
		case "run":
			v := t.Sub.Render("no stress results yet")
			if len(d.runs) > 0 {
				v = stepper(t, runLabel(d.runs[d.run]), i == d.field) + t.Sub.Render(fmt.Sprintf("  %d of %d", d.run+1, len(d.runs)))
			}
			row(i, "Run", v, "")
		case "paths":
			row(i, "Files", field(f, d.paths, max(20, inner-lw-2)), "")
			lines = append(lines, strings.Repeat(" ", lw)+t.Sub.Render("files or directories, comma-separated; *.json inside, other JSON is skipped"))
		case "format":
			row(i, "Format", pills(reportFormats, d.format), "")
		case "output":
			row(i, "Output", pills(reportOutputs, d.output), "")
		case "saveto":
			v := d.saveTo
			hint := ""
			if v == "" && !d.editing {
				v, hint = m.reportFile(d), "enter to change"
			}
			row(i, "Save to", field(f, v, max(20, inner-lw-18)), hint)
		case "generate":
			lines = append(lines, "", strings.Repeat(" ", lw+1)+button(t, "Generate", i == d.field))
		}
	}
	if d.err != "" {
		lines = append(lines, "", t.StatusText(model.StatusCrit, "✖ "+d.err))
	}
	lines = append(lines, "", t.Sub.Render("↑↓ field · ←→ change · enter edit / generate · esc close"))
	return overlay(t, base, t.Accent, "Reports", strings.Join(lines, "\n"), m.width, m.height)
}
