package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/noahzmr/tyr/internal/explain"
	"github.com/noahzmr/tyr/internal/model"
)

// Alert is one finding with its module, for explanations.
type Alert struct {
	Module model.ModuleID
	model.Finding
}

// diskOf is the disk a finding is about, if any.
func diskOf(s *model.Snapshot, subject string) *model.Disk {
	if s == nil || s.Storage == nil {
		return nil
	}
	for i := range s.Storage.Disks {
		if s.Storage.Disks[i].Name == subject {
			return &s.Storage.Disks[i]
		}
	}
	return nil
}

// where writes the location and the kernel log lines of a finding's
// component (disks and volumes), newest last.
func where(m *md, s *model.Snapshot, a Alert, maxLines int) {
	d := diskOf(s, a.Subject)
	if d == nil {
		return
	}
	var loc []string
	if d.Location != "" {
		loc = append(loc, "attached at "+d.Location)
	}
	if r := d.Remote; r != nil {
		tr := map[string]string{"iscsi": "iSCSI", "fc": "Fibre Channel"}[r.Transport]
		if tr == "" {
			tr = r.Transport
		}
		v := tr + " " + r.Target
		if r.Portal != "" {
			v += " via " + r.Portal
		}
		if r.Session != "" || r.State != "" {
			v += ", " + strings.TrimSpace(r.Session+" "+r.State)
		}
		loc = append(loc, v)
	}
	if id := strings.TrimSpace(d.Vendor + " " + d.Model); id != "" || d.Serial != "" {
		if d.Serial != "" {
			id = strings.TrimSpace(id + " S/N " + d.Serial)
		}
		loc = append(loc, id)
	}
	if len(loc) > 0 {
		m.line("  %s  ", esc(strings.Join(loc, " · ")))
	}
	if lines := d.KernelLines; len(lines) > 0 {
		if maxLines > 0 && len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
		}
		m.line("")
		m.p("```text\n%s\n```", strings.Join(lines, "\n"))
	}
}

// alertsFrom collects a snapshot's non-OK findings with their modules.
func alertsFrom(s *model.Snapshot) []Alert {
	var out []Alert
	for _, f := range findings(s) {
		if f.Status != model.StatusOK {
			out = append(out, Alert(f))
		}
	}
	return out
}

// explainSection writes "What the alerts mean": per kind of alert its
// meaning, where it happens (component, location, kernel lines), the usual
// causes and the commands to check, filled in for the affected components.
func explainSection(m *md, s *model.Snapshot, alerts []Alert) {
	type group struct {
		mod    model.ModuleID
		code   string
		worst  model.Status
		alerts []Alert
	}
	by := map[string]*group{}
	var order []string
	for _, a := range alerts {
		k := string(a.Module) + "/" + a.Code
		g := by[k]
		if g == nil {
			g = &group{mod: a.Module, code: a.Code}
			by[k] = g
			order = append(order, k)
		}
		g.worst = model.Worst(g.worst, a.Status)
		g.alerts = append(g.alerts, a)
	}
	if len(order) == 0 {
		return
	}
	sort.SliceStable(order, func(i, j int) bool { return by[order[i]].worst > by[order[j]].worst })
	m.h(2, "What the alerts mean")
	for _, k := range order {
		g := by[k]
		e, known := explain.For(g.mod, g.code)
		title := e.Title
		if !known {
			title = strings.ReplaceAll(g.code, "_", " ")
		}
		m.h(3, fmt.Sprintf("%s %s", status(g.worst), title))
		m.line("`%s/%s` · %s", g.mod, g.code, moduleNames[g.mod])
		m.line("")
		if known {
			m.p("%s", e.Meaning)
		}
		m.p("**Where**")
		for i, a := range g.alerts {
			if i == 8 {
				m.line("- … %d more", len(g.alerts)-8)
				break
			}
			m.line("- %s", esc(a.Message))
			where(m, s, a, 6)
		}
		m.line("")
		if known && len(e.Causes) > 0 {
			m.p("**Usual causes**")
			for _, c := range e.Causes {
				m.line("- %s", c)
			}
			m.line("")
		}
		if known && len(e.Check) > 0 {
			m.p("**How to check**")
			var cmds []string
			seen := map[string]bool{}
			for i, a := range g.alerts {
				if i == 3 {
					break
				}
				for _, c := range e.Commands(a.Subject) {
					if !seen[c] {
						seen[c] = true
						cmds = append(cmds, c)
					}
				}
			}
			m.p("```bash\n%s\n```", strings.Join(cmds, "\n"))
		}
	}
}

// ExplainAlert is the page of one finding: what happened, where, what it
// means, the usual causes and the commands to check this component.
func ExplainAlert(s *model.Snapshot, a Alert) string {
	var m md
	e, known := explain.For(a.Module, a.Code)
	title := e.Title
	if !known {
		title = strings.ReplaceAll(a.Code, "_", " ")
	}
	m.line("# %s %s", status(a.Status), title)
	m.line("")
	m.p("`%s/%s` · %s · %s", a.Module, a.Code, moduleNames[a.Module], esc(a.Subject))
	m.p("**%s**", esc(a.Message))
	m.h(2, "Where")
	if diskOf(s, a.Subject) == nil {
		m.p("%s", esc(a.Subject))
	}
	where(&m, s, a, 0)
	var others []string
	for _, o := range alertsFrom(s) {
		if o.Module == a.Module && o.Code == a.Code && o.Subject != a.Subject {
			others = append(others, o.Subject)
		}
	}
	if len(others) > 0 {
		m.p("Also on: %s", esc(strings.Join(others, ", ")))
	}
	if !known {
		m.p("_No explanation for this kind of alert yet; the message above is what the collector reported._")
		return m.String()
	}
	m.h(2, "What it means")
	m.p("%s", e.Meaning)
	if len(e.Causes) > 0 {
		m.h(2, "Usual causes")
		for _, c := range e.Causes {
			m.line("- %s", c)
		}
		m.line("")
	}
	if len(e.Check) > 0 {
		m.h(2, "How to check")
		m.p("```bash\n%s\n```", strings.Join(e.Commands(a.Subject), "\n"))
	}
	return m.String()
}

// AlertReference is the catalog of every alert Tyr knows, by module: what
// it means, the usual causes and how to check ({dev} is the component).
func AlertReference() string {
	var m md
	m.line("# Alert reference")
	m.line("")
	m.p("Every kind of alert Tyr raises: what it means, the usual causes and how to check. `{dev}` stands for the component the alert names (a disk, a port, a CPU, a DIMM, a GPU's PCI address).")
	cur := model.ModuleID("")
	for _, k := range explain.All() {
		if k.Module != cur {
			cur = k.Module
			m.h(2, moduleNames[cur])
		}
		m.h(3, k.Title)
		m.line("`%s/%s`", k.Module, k.Code)
		m.line("")
		m.p("%s", k.Meaning)
		if len(k.Causes) > 0 {
			m.p("**Usual causes:** %s.", strings.Join(k.Causes, "; "))
		}
		if len(k.Check) > 0 {
			m.p("```bash\n%s\n```", strings.Join(k.Check, "\n"))
		}
	}
	return m.String()
}
