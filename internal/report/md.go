package report

import (
	"fmt"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/model"
)

// md builds a Markdown document.
type md struct{ b strings.Builder }

func (m *md) String() string { return strings.TrimRight(m.b.String(), "\n") + "\n" }

func (m *md) h(level int, s string) {
	if cur := m.b.String(); cur != "" && !strings.HasSuffix(cur, "\n\n") {
		m.b.WriteString("\n")
	}
	m.b.WriteString(strings.Repeat("#", level) + " " + s + "\n\n")
}

func (m *md) p(format string, args ...any) {
	m.b.WriteString(fmt.Sprintf(format, args...) + "\n\n")
}

func (m *md) line(format string, args ...any) {
	m.b.WriteString(fmt.Sprintf(format, args...) + "\n")
}

// kv writes "**Key:** value" lines (a hard break after each) for the
// non-empty values.
func (m *md) kv(pairs ...[2]string) {
	var out []string
	for _, p := range pairs {
		if v := strings.TrimSpace(p[1]); v != "" && v != "–" {
			out = append(out, "**"+p[0]+":** "+esc(v))
		}
	}
	if len(out) > 0 {
		m.b.WriteString(strings.Join(out, "  \n") + "\n\n")
	}
}

// table writes a pipe table; a header starting with ">" right-aligns.
func (m *md) table(header []string, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	var h, sep []string
	for _, c := range header {
		if strings.HasPrefix(c, ">") {
			h, sep = append(h, strings.TrimPrefix(c, ">")), append(sep, "--:")
		} else {
			h, sep = append(h, c), append(sep, "---")
		}
	}
	m.b.WriteString("| " + strings.Join(h, " | ") + " |\n| " + strings.Join(sep, " | ") + " |\n")
	for _, r := range rows {
		cells := make([]string, len(header))
		for i := range cells {
			if i < len(r) {
				cells[i] = esc(r[i])
			}
			if cells[i] == "" {
				cells[i] = "–"
			}
		}
		m.b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	m.b.WriteString("\n")
}

// esc keeps a value from breaking the Markdown structure.
func esc(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
}

// status is the status as glyph + word: readable without colour.
func status(s model.Status) string {
	return map[model.Status]string{model.StatusOK: "● OK", model.StatusWarn: "▲ WARN",
		model.StatusCrit: "✖ CRIT", model.StatusUnknown: "? UNKNOWN"}[s]
}

func verdict(v string) string {
	switch v {
	case "PASS":
		return "● PASS"
	case "WARN":
		return "▲ WARN"
	case "FAIL":
		return "✖ FAIL"
	}
	return "○ " + v
}

func when(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func bytesSI(b uint64) string {
	switch {
	case b >= 1e12:
		return fmt.Sprintf("%.1f TB", float64(b)/1e12)
	case b >= 1e9:
		return fmt.Sprintf("%.0f GB", float64(b)/1e9)
	case b >= 1e6:
		return fmt.Sprintf("%.0f MB", float64(b)/1e6)
	}
	return fmt.Sprintf("%d B", b)
}

func bytesGiB(b uint64) string {
	if b == 0 {
		return ""
	}
	if b >= 1<<40 {
		return fmt.Sprintf("%.1f TiB", float64(b)/(1<<40))
	}
	return fmt.Sprintf("%.0f GiB", float64(b)/(1<<30))
}

func f0(v *float64, unit string) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%.0f%s", *v, unit)
}

func u(v *uint64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(*v)
}

func i0(v *int, unit string) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%d%s", *v, unit)
}

func dur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

var moduleNames = map[model.ModuleID]string{model.ModSystem: "System", model.ModCPU: "CPU", model.ModMemory: "Memory",
	model.ModStorage: "Storage", model.ModPower: "Power", model.ModNetwork: "Network", model.ModAccel: "GPU/NPU", model.ModEvents: "Events"}

// moduleFindings pairs every finding with its module.
type moduleFinding struct {
	Module model.ModuleID
	model.Finding
}

func findings(s *model.Snapshot) []moduleFinding {
	var out []moduleFinding
	for _, id := range append([]model.ModuleID{model.ModSystem}, model.Modules...) {
		if sec := s.Section(id); sec != nil {
			for _, f := range sec.Head().Findings {
				out = append(out, moduleFinding{id, f})
			}
		}
	}
	return out
}
