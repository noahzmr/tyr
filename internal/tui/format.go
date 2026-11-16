package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/noahzmr/tyr/internal/model"
)

func bytesGB(b uint64) string {
	g := float64(b) / (1 << 30)
	switch {
	case b == 0:
		return "0"
	case g >= 1024:
		return fmt.Sprintf("%.1f TB", g/1024)
	case g >= 10:
		return fmt.Sprintf("%.0f GB", g)
	case g >= 1:
		return fmt.Sprintf("%.1f GB", g)
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
	}
	return fmt.Sprintf("%d kB", b>>10)
}

func optF(v *float64, format string) string {
	if v == nil {
		return "–"
	}
	return fmt.Sprintf(format, *v)
}

func optU(v *uint64) string {
	if v == nil {
		return "–"
	}
	return fmt.Sprintf("%d", *v)
}

func optI(v *int, format string) string {
	if v == nil {
		return "–"
	}
	return fmt.Sprintf(format, *v)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "–"
	}
	return s
}

func duration(sec float64) string {
	d := time.Duration(sec) * time.Second
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// truncate cuts a (possibly styled) string to w cells.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// fit truncates every line to w cells and keeps at most h lines.
func fit(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if h >= 0 && len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			lines[i] = truncate(l, w)
		}
	}
	return strings.Join(lines, "\n")
}

// pad right-pads a styled string to w cells (truncating if longer).
func pad(s string, w int) string {
	if lipgloss.Width(s) > w {
		return truncate(s, w)
	}
	return s + strings.Repeat(" ", w-lipgloss.Width(s))
}

// hang wraps body to w cells after prefix; continuation lines are indented
// to the width of prefix (a hanging indent), so long entries stay readable.
// style, if given, is applied to each wrapped line of a plain body.
func hang(prefix, body string, w int, style ...func(...string) string) string {
	pw := lipgloss.Width(prefix)
	bw := max(20, w-pw)
	lines := []string{body}
	if lipgloss.Width(body) > bw {
		lines = strings.Split(ansi.Wrap(body, bw, ""), "\n")
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
		if i > 0 {
			lines[i] = strings.TrimLeft(lines[i], " ")
		}
		if len(style) > 0 {
			lines[i] = style[0](lines[i])
		}
		if i == 0 {
			lines[i] = prefix + lines[i]
		} else {
			lines[i] = strings.Repeat(" ", pw) + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

// kvWrap is kv with values wrapped to w cells under their column.
func kvWrap(t *Theme, rows [][2]string, w int) string {
	lw := 0
	for _, r := range rows {
		lw = max(lw, lipgloss.Width(r[0]))
	}
	var b []string
	for _, r := range rows {
		b = append(b, hang(t.Dim.Render(pad(r[0], lw))+"  ", r[1], w))
	}
	return strings.Join(b, "\n")
}

// kv renders "label  value" lines with aligned labels.
func kv(t *Theme, rows [][2]string) string {
	lw := 0
	for _, r := range rows {
		lw = max(lw, lipgloss.Width(r[0]))
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(t.Dim.Render(pad(r[0], lw)) + "  " + r[1])
	}
	return b.String()
}

// findingsSection closes every module tab (canvas: same format
// everywhere): "Findings · Module ── n next · enter open", then one row per
// finding (glyph, component, message, NEW right-aligned), most severe
// first, as many as fit in h lines; then unavailable sources (○ muted).
func findingsSection(t *Theme, module string, fs []model.Finding, ss []model.SourceState, h, w int) string {
	fs = sortFindings(fs)
	head := t.HeadingRight("Findings · "+module, t.Counts(fs), "n next · enter open", w)
	src := sourcesView(t, ss, w)
	srcN := 0
	if src != "" {
		srcN = strings.Count(src, "\n") + 1
	}
	room := max(1, h-1-srcN)
	var lines []string
	n := countNonOK(fs)
	shown := 0
	for _, f := range fs {
		if f.Status == model.StatusOK {
			continue
		}
		row := strings.Split(findingRow(t, f, w), "\n")
		if len(lines)+len(row) > room || (len(lines)+len(row) == room && shown < n-1) {
			if len(lines) == 0 { // one tall row: show what fits
				lines = append(lines, row[:max(1, min(len(row), room-1))]...)
				if shown++; shown == n {
					break
				}
			}
			lines = append(lines, t.Sub.Render(fmt.Sprintf("   … %d more · enter on a row opens its page", n-shown)))
			break
		}
		lines = append(lines, row...)
		shown++
	}
	if n == 0 {
		lines = append(lines, " "+t.Dot(model.StatusOK)+" "+t.Dim.Render("No problems found."))
	}
	out := head + "\n" + strings.Join(lines, "\n")
	if src != "" {
		out += "\n" + src
	}
	return out
}

// findingRow: " ✖ component   message                       NEW".
func findingRow(t *Theme, f model.Finding, w int) string {
	tag := ""
	if t.sess.isNew(f) {
		tag = t.NewTag()
	}
	prefix := " " + t.Dot(f.Status) + " " + t.Bold.Render(pad(f.Subject, 12)) + " "
	if tag != "" {
		prefix += tag + " "
	}
	return hang(prefix, f.Message, w-1)
}

// sortFindings returns a copy, most severe first (stable).
func sortFindings(fs []model.Finding) []model.Finding {
	fs = slices.Clone(fs)
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Status > fs[j].Status })
	return fs
}

// findingLine renders "✖ NEW Module  message"; module may be empty.
func findingLine(t *Theme, f model.Finding, module string, w int) string {
	s := " " + t.Dot(f.Status) + " "
	if module != "" {
		s += t.Dim.Render(pad(module, 8)) + " "
	}
	if t.sess.isNew(f) {
		s += t.NewTag() + " "
	}
	return hang(s, f.Message, w-1)
}

func countNonOK(fs []model.Finding) int {
	n := 0
	for _, f := range fs {
		if f.Status != model.StatusOK {
			n++
		}
	}
	return n
}

// sourcesView shows unavailable sources with their reason.
func sourcesView(t *Theme, ss []model.SourceState, width int) string {
	var lines []string
	for _, s := range ss {
		if s.State == model.AvailOK {
			continue
		}
		if s.State == model.AvailNotApplicable {
			lines = append(lines, truncate(" "+t.NotApplicable(pad(s.Name, 12), s.Reason), width))
			continue
		}
		lines = append(lines, truncate(" "+t.Unavail(pad(s.Name, 12), s.Reason), width))
	}
	return strings.Join(lines, "\n")
}

// stripStyle removes ANSI styling (used before re-styling a whole line).
func stripStyle(s string) string { return ansi.Strip(s) }
