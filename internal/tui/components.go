package tui

import (
	"fmt"
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
)

// Shared components of the redesign (canvas board "Components"). Every
// glyph is one cell wide; widths are measured with lipgloss.Width
// (x/ansi underneath).

// verdictBadge is a badge with a custom word (PASS, FAIL, ABORTED,
// SKIPPED) in the colour of st.
func (t *Theme) verdictBadge(st model.Status, word string) string {
	label := " " + statusSym(st) + " " + word + " "
	if t.NoColor {
		return lipgloss.NewStyle().Bold(true).Reverse(true).Render(label)
	}
	bg, fg := t.StatusColor(st), t.Ink
	if st == model.StatusUnknown {
		bg, fg = t.UnkBg, t.Fg
	}
	return lipgloss.NewStyle().Bold(true).Foreground(fg).Background(bg).Render(label)
}

// inlineStatus is the table variant of a badge: glyph and word in the
// status colour, no background ("✖ FAILED").
func (t *Theme) inlineStatus(st model.Status, word string) string {
	return t.StatusBold(st, statusSym(st)+" "+word)
}

// kv is one key/value pair of a block. Empty values render as missing.
type kvPair struct {
	k, v string
}

// kvBlock renders aligned key/value lines: key muted, padded to the
// longest key + 2; value fg. Missing values (empty or "–") show a subtle
// word instead of a blank ("n/a" unless miss is given).
func kvBlock(t *Theme, rows []kvPair, miss string) string {
	if miss == "" {
		miss = "n/a"
	}
	kw := 0
	for _, r := range rows {
		kw = max(kw, lipgloss.Width(r.k))
	}
	var b []string
	for _, r := range rows {
		v := r.v
		if s := strings.TrimSpace(stripStyle(v)); s == "" || s == "–" {
			v = t.Sub.Render(miss)
		}
		b = append(b, t.Dim.Render(pad(r.k, kw+2))+v)
	}
	return strings.Join(b, "\n")
}

// kvColumns lays out up to three blocks side by side across w (never
// "·"-joined on one line). Each block gets an equal share; lines are
// truncated to fit.
func kvColumns(w int, blocks ...string) string {
	n := len(blocks)
	if n == 0 {
		return ""
	}
	colW := w / n
	var cols []string
	for i, b := range blocks {
		cw := colW
		if i == n-1 {
			cw = w - colW*(n-1)
		}
		var lines []string
		for _, l := range strings.Split(b, "\n") {
			lines = append(lines, padCell(l, cw-1, false)+" ")
		}
		cols = append(cols, strings.Join(lines, "\n"))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cols...)
}

// gaugeVal is a threshold gauge followed by the value, coloured by its
// status: "━━━━━━━━──╎──┃ 44 °C".
func gaugeVal(t *Theme, v, lo, hi float64, warn, crit *float64, w int, label string) string {
	st := model.StatusOK
	if crit != nil && v >= *crit {
		st = model.StatusCrit
	} else if warn != nil && v >= *warn {
		st = model.StatusWarn
	}
	return gauge(t, v, lo, hi, warn, crit, w) + " " + t.StatusText(st, label)
}

// gaugeInv is a gauge where lower is worse (optical rx power, voltage):
// the same marks, ticks at the lower limits.
func gaugeInv(t *Theme, v, lo, hi float64, lwarn, lcrit *float64, w int, label string) string {
	st := model.StatusOK
	if lcrit != nil && v <= *lcrit {
		st = model.StatusCrit
	} else if lwarn != nil && v <= *lwarn {
		st = model.StatusWarn
	}
	if hi <= lo || w < 3 {
		return t.StatusText(st, label)
	}
	pos := func(x float64) int { return int(math.Round((x - lo) / (hi - lo) * float64(w-1))) }
	fill := min(w, max(0, pos(v)+1))
	wi, ci := -1, -1
	if lwarn != nil {
		wi = min(w-1, max(0, pos(*lwarn)))
	}
	if lcrit != nil {
		ci = min(w-1, max(0, pos(*lcrit)))
	}
	track := lipgloss.NewStyle().Foreground(t.Track)
	var b strings.Builder
	for i := 0; i < w; i++ {
		switch {
		case i == ci && i >= fill:
			b.WriteString(t.StatusText(model.StatusCrit, "┃"))
		case i == wi && i >= fill:
			b.WriteString(t.StatusText(model.StatusWarn, "╎"))
		case i < fill:
			b.WriteString(t.StatusText(st, "━"))
		default:
			b.WriteString(track.Render("─"))
		}
	}
	return b.String() + " " + t.StatusText(st, label)
}

// sensorGaugeLine renders a model.Sensor as gauge + value using its
// thresholds (upper limits; lower-only limits use the inverted gauge).
func sensorGaugeLine(t *Theme, s model.Sensor, w int) string {
	if s.Value == nil {
		return t.Sub.Render("no reading")
	}
	val := fmtSensor(s)
	th := s.Thresholds
	if th == nil {
		return t.StatusText(s.Status, val)
	}
	if th.UpperWarn == nil && th.UpperCrit == nil && (th.LowerWarn != nil || th.LowerCrit != nil) {
		lo, hi := *s.Value, *s.Value
		for _, x := range []*float64{th.LowerCrit, th.LowerWarn} {
			if x != nil {
				lo, hi = math.Min(lo, *x), math.Max(hi, *x)
			}
		}
		span := math.Max(1, hi-lo)
		return gaugeInv(t, *s.Value, lo-span*0.3, hi+span*0.6, th.LowerWarn, th.LowerCrit, w, val)
	}
	lo := 0.0
	if s.Unit == model.UnitCelsius {
		lo = 20
	}
	hi := *s.Value
	for _, x := range []*float64{th.UpperWarn, th.UpperCrit} {
		if x != nil {
			hi = math.Max(hi, *x)
		}
	}
	hi += (hi - lo) * 0.08
	return gaugeVal(t, *s.Value, lo, hi, th.UpperWarn, th.UpperCrit, w, val)
}

// spark renders a sparkline (▁▂▃▄▅▆▇█, newest right) in colour c; maxV > 0
// fixes the scale (percentages), else the visible window's maximum.
func spark(vals []float64, w int, maxV float64, c color.Color) string {
	if len(vals) > w {
		vals = vals[len(vals)-w:]
	}
	if maxV <= 0 {
		for _, v := range vals {
			maxV = math.Max(maxV, v)
		}
	}
	runes := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for _, v := range vals {
		i := 0
		if maxV > 0 {
			i = int(math.Min(1, math.Max(0, v/maxV)) * float64(len(runes)-1))
		}
		b.WriteRune(runes[i])
	}
	return lipgloss.NewStyle().Foreground(c).Render(b.String())
}

// inspector draws a right-hand pane: a vertical │ rule, the selected
// item's glyph and name as an accent heading, then the body. Lines are cut
// to w and the pane is padded to h lines.
func inspector(t *Theme, st model.Status, title, body string, w, h int) string {
	rule := t.Sub.Render("│") + " "
	cw := max(4, w-2)
	lines := []string{t.Dot(st) + " " + t.Bold.Render(title), lipgloss.NewStyle().Foreground(t.Accent).Render(strings.Repeat("─", cw))}
	lines = append(lines, strings.Split(body, "\n")...)
	var out []string
	for i := 0; i < h; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out = append(out, rule+padCell(l, cw, false))
	}
	return strings.Join(out, "\n")
}

// besideInspector joins a main area (left) and an inspector (right) of
// width iw, both h lines tall.
func besideInspector(left string, lw int, right string, h int) string {
	ll := strings.Split(left, "\n")
	rl := strings.Split(right, "\n")
	var out []string
	for i := 0; i < h; i++ {
		a, b := "", ""
		if i < len(ll) {
			a = ll[i]
		}
		if i < len(rl) {
			b = rl[i]
		}
		out = append(out, padCell(a, lw, false)+b)
	}
	return strings.Join(out, "\n")
}

// overlay draws box over base (rendered faint) with lipgloss Layers:
// rounded border in the kind's colour, title in the top border, centred,
// at most 96 cells wide.
func overlay(t *Theme, base string, kind color.Color, title, body string, w, h int) string {
	return overlayR(t, base, kind, title, "", body, w, h)
}

// overlayR is overlay with a muted note at the right end of the top
// border ("tyr 0.9 · ? to close").
func overlayR(t *Theme, base string, kind color.Color, title, right, body string, w, h int) string {
	bw := min(96, w-4)
	inner := lipgloss.NewStyle().Width(bw - 4).Render(body)
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(kind).Padding(0, 1).Render(inner)
	lines := strings.Split(box, "\n")
	if title != "" && len(lines) > 0 {
		top := []rune(stripStyle(lines[0]))
		label := []rune("─ " + title + " ")
		if len(label)+2 < len(top) {
			copy(top[1:], label)
		}
		bs := lipgloss.NewStyle().Foreground(kind)
		rest := string(top[len(label)+1:])
		tail := ""
		if r := []rune(" " + right + " ─╮"); right != "" && len([]rune(rest)) > len(r)+2 {
			rr := []rune(rest)
			rest = string(rr[:len(rr)-len(r)])
			tail = t.Dim.Render(" "+right+" ") + bs.Render("─╮")
		}
		lines[0] = bs.Render(string(top[:1])) + bs.Render("─ ") + lipgloss.NewStyle().Bold(true).Foreground(kind).Render(title) + bs.Render(" "+rest) + tail
	}
	box = strings.Join(lines, "\n")
	faint := lipgloss.NewStyle().Faint(true).Render(stripStyle(base))
	x := max(0, (w-lipgloss.Width(box))/2)
	y := max(0, (h-lipgloss.Height(box))/2)
	c := lipgloss.NewCompositor(lipgloss.NewLayer(faint), lipgloss.NewLayer(box).X(x).Y(y).Z(1))
	return fit(c.Render(), w, h)
}

// pill renders a choice option: selected = accent pill, else muted.
func pill(t *Theme, s string, selected bool) string {
	if selected {
		return t.ActiveTab.Render(s)
	}
	return t.Dim.Render(" " + s + " ")
}

// stepper renders "◂ value ▸".
func stepper(t *Theme, v string, focused bool) string {
	a := t.Sub
	if focused {
		a = lipgloss.NewStyle().Foreground(t.Accent)
	}
	return a.Render("◂") + " " + t.Bold.Render(v) + " " + a.Render("▸")
}

// button renders the primary action: accent pill with ▸; disabled shows
// the reason instead.
func button(t *Theme, label string, focused bool) string {
	if focused {
		return t.ActiveTab.Render(label + " ▸")
	}
	return lipgloss.NewStyle().Bold(true).Foreground(t.Accent).Render(label + " ▸")
}

// fieldLabel renders a form label: focused = ▌ + accent label.
func fieldLabel(t *Theme, label string, focused bool, w int) string {
	if focused {
		return lipgloss.NewStyle().Foreground(t.Accent).Bold(true).Render("▌" + pad(label, w-1))
	}
	return " " + t.Dim.Render(pad(label, w-1))
}

// disabledOption renders "○ name not installed · fix".
func disabledOption(t *Theme, name, reason, fix string) string {
	s := lipgloss.NewStyle().Foreground(t.NA).Render("○") + " " + t.Dim.Render(name) + " " + t.Sub.Render(reason)
	if fix != "" {
		s += t.Sub.Render(" · " + fix)
	}
	return s
}

// toast renders a footer message with a glyph by kind.
func toast(t *Theme, kind model.Status, msg string) string {
	switch kind {
	case model.StatusOK:
		return t.Dot(model.StatusOK) + " " + t.Bold.Render(msg)
	case model.StatusWarn, model.StatusCrit:
		return t.Dot(kind) + " " + t.Bold.Render(msg)
	}
	return lipgloss.NewStyle().Foreground(t.Accent).Render("ℹ") + " " + t.Bold.Render(msg)
}

// footerHints joins key hints ("key desc", 2 spaces apart), dropping hints
// from the right so that the toast (right-aligned) and "? help" (always
// last) fit in w.
func footerHints(t *Theme, hints [][2]string, toastStr string, w int) string {
	help := t.Key.Render("?") + " " + t.Dim.Render("help")
	used := lipgloss.Width(help)
	if toastStr != "" {
		used += lipgloss.Width(toastStr) + 2
	}
	var parts []string
	for _, h := range hints {
		p := t.Key.Render(h[0]) + " " + t.Dim.Render(h[1])
		if used+lipgloss.Width(p)+2 > w {
			break
		}
		parts = append(parts, p)
		used += lipgloss.Width(p) + 2
	}
	left := strings.Join(append(parts, help), "  ")
	if toastStr == "" {
		return left
	}
	gap := max(2, w-lipgloss.Width(left)-lipgloss.Width(toastStr))
	return left + strings.Repeat(" ", gap) + toastStr
}

// countLabel formats "N thing(s)".
func countLabel(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
