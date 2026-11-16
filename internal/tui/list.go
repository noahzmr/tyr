package tui

import (
	"fmt"
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
)

// column of a list; w == 0 means "flexible": it takes the remaining width;
// w < 0 means "auto": the widest value seen this session (at least the
// title), so columns never shift when values change. A title starting with
// ">" right-aligns the column (numbers).
type column struct {
	title string
	w     int
}

func (c column) right() bool  { return strings.HasPrefix(c.title, ">") }
func (c column) name() string { return strings.TrimPrefix(c.title, ">") }

// row of a list. Cells may be styled; status drives the leading glyph.
type row struct {
	cells  []string
	status model.Status
	dim    bool
	noDot  bool
}

// withBg paints bg behind an already styled string: every reset inside it
// re-opens the background, so cell colors survive on the selection bar.
func withBg(s string, bg color.Color) string {
	open := lipgloss.NewStyle().Background(bg).Render("\x00")
	open = open[:strings.Index(open, "\x00")]
	s = strings.ReplaceAll(s, "\x1b[m", "\x1b[m"+open)
	s = strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+open)
	return open + s + "\x1b[m"
}

// padCell pads (or truncates) a styled cell to w, right-aligned if asked.
func padCell(s string, w int, right bool) string {
	if lipgloss.Width(s) > w {
		return truncate(s, w)
	}
	if right {
		return strings.Repeat(" ", w-lipgloss.Width(s)) + s
	}
	return s + strings.Repeat(" ", w-lipgloss.Width(s))
}

// renderList draws a table: muted bold header; rows as "▌● cells", where ▌
// marks the selection (with the selection background across the row),
// the glyph is the row status, names of problem rows are bold, and cells
// keep their own colors. A scrollbar (┃ thumb, │ track) appears on the
// right edge when the rows do not fit.
func renderList(t *Theme, cols []column, rows []row, sel, width, maxRows int) string {
	body := max(1, maxRows-1)
	scroll := len(rows) > body
	avail := width - 3 // "▌● "
	if scroll {
		avail -= 2
	}
	widths := make([]int, len(cols))
	sig := ""
	for _, c := range cols {
		sig += c.title + "|"
	}
	for i, c := range cols {
		widths[i] = c.w
		if c.w < 0 { // auto: widest value seen this session
			wmax := lipgloss.Width(c.name())
			for _, r := range rows {
				if i < len(r.cells) {
					wmax = max(wmax, lipgloss.Width(r.cells[i]))
				}
			}
			if t.sess != nil {
				k := sig + "#" + c.title
				wmax = max(wmax, t.sess.colWidth[k])
				t.sess.colWidth[k] = wmax
			}
			widths[i] = min(wmax, max(4, avail/2))
		}
	}
	fixed, flex := 0, 0
	for i, c := range cols {
		fixed += max(0, widths[i]) + 1
		if c.w == 0 {
			flex++
		}
	}
	for i, c := range cols {
		if c.w == 0 {
			widths[i] = max(6, (avail-fixed)/max(1, flex))
		}
	}
	var hdr strings.Builder
	hdr.WriteString("   ")
	for i, c := range cols {
		hdr.WriteString(padCell(c.name(), widths[i], c.right()) + " ")
	}
	lines := []string{t.TableHead.Render(truncate(strings.TrimRight(hdr.String(), " "), width))}
	start := 0
	if scroll {
		start = min(max(0, sel-body/2), len(rows)-body)
	}
	thumbA, thumbB := 0, 0
	if scroll {
		size := max(1, body*body/len(rows))
		thumbA = start * body / len(rows)
		thumbB = thumbA + size
	}
	lineW := 3
	for _, w := range widths {
		lineW += w + 1
	}
	lineW = min(lineW, width-map[bool]int{true: 2, false: 0}[scroll])
	for i := start; i < min(len(rows), start+body); i++ {
		r := rows[i]
		var b strings.Builder
		for j, c := range cols {
			cell := ""
			if j < len(r.cells) {
				cell = r.cells[j]
			}
			if j == 0 && r.status >= model.StatusWarn && !r.dim && cell == stripStyle(cell) {
				cell = t.Bold.Render(cell)
			}
			b.WriteString(padCell(cell, widths[j], c.right()) + " ")
		}
		cells := b.String()
		glyph := t.Dot(r.status)
		if r.dim { // dimmed row: subtle text, ○ glyph
			cells = t.Sub.Render(stripStyle(cells))
			glyph = lipgloss.NewStyle().Foreground(t.NA).Render("○")
		}
		if r.noDot {
			glyph = " "
		}
		var line string
		if i == sel {
			if t.Basic { // 16 colours / no colour: reverse bar, bold row
				bar := lipgloss.NewStyle().Reverse(true).Bold(true).Render("▌")
				line = bar + glyph + " " + t.Bold.Render(stripStyle(padCell(cells, lineW-3, false)))
			} else {
				bar := lipgloss.NewStyle().Foreground(t.Accent).Background(t.SelBg).Bold(true).Render("▌")
				line = bar + withBg(glyph+" "+padCell(cells, lineW-3, false), t.SelBg)
			}
		} else {
			line = " " + glyph + " " + padCell(cells, lineW-3, false)
		}
		if scroll {
			k := i - start
			sb := t.Sub.Render("│")
			if k >= thumbA && k < thumbB {
				sb = lipgloss.NewStyle().Foreground(t.Accent).Render("┃")
			}
			line = padCell(line, width-1, false) + sb
		}
		lines = append(lines, line)
	}
	if len(rows) == 0 {
		lines = append(lines, t.Sub.Render("   (none)"))
	}
	return strings.Join(lines, "\n")
}

// sparkline renders values as block characters scaled to the window maximum.
func sparkline(vals []float64, width int) string {
	const blocks = "▁▂▃▄▅▆▇█"
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	maxV := 0.0
	for _, v := range vals {
		maxV = math.Max(maxV, v)
	}
	var b strings.Builder
	runes := []rune(blocks)
	for _, v := range vals {
		i := 0
		if maxV > 0 {
			i = int(v / maxV * float64(len(runes)-1))
		}
		b.WriteRune(runes[i])
	}
	return strings.Repeat(" ", max(0, width-len(vals))) + b.String()
}

func bps(v *float64) string {
	if v == nil {
		return "–"
	}
	x := *v
	switch {
	case x >= 1e9:
		return fmt.Sprintf("%.1fG", x/1e9)
	case x >= 1e6:
		return fmt.Sprintf("%.1fM", x/1e6)
	case x >= 1e3:
		return fmt.Sprintf("%.0fk", x/1e3)
	}
	return fmt.Sprintf("%.0f", x)
}

// usageBar renders "[████░░░░] 61%" in the status color.
func usageBar(t *Theme, pct *float64, st model.Status, w int) string {
	if pct == nil {
		return "–"
	}
	n := int(*pct / 100 * float64(w))
	n = min(max(n, 0), w)
	return t.StatusText(st, strings.Repeat("█", n)) + t.Dim.Render(strings.Repeat("░", w-n)) + fmt.Sprintf(" %3.0f%%", *pct)
}

// viewSwitch renders "v Disks · RAID · Filesystems": the active view in
// bold underlined accent, the others muted.
func viewSwitch(t *Theme, names []string, active int) string {
	var parts []string
	for i, n := range names {
		if i == active {
			parts = append(parts, lipgloss.NewStyle().Bold(true).Underline(true).Foreground(t.Accent).Render(n))
		} else {
			parts = append(parts, t.Dim.Render(n))
		}
	}
	return t.Sub.Render("v ") + strings.Join(parts, t.Sub.Render(" · "))
}

// statusStrip is the first line of every tab: badge, the key facts in bold,
// details muted, and a right-aligned part (usually the view switch).
func statusStrip(t *Theme, st model.Status, bold, rest, right string, w int) string {
	left := t.Badge(st) + " " + t.Bold.Render(bold)
	if rest != "" {
		left += t.Dim.Render(" · " + rest)
	}
	room := w - lipgloss.Width(right) - 2
	if lipgloss.Width(left) > room {
		left = truncate(left, max(10, room))
	}
	gap := max(2, w-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + right
}

// gauge draws "━━━━━━──╎──┃" on a scale from lo to hi: the filled part in
// the value's status color, the track in the track color, warn ╎ and crit
// ┃ ticks. warn/crit may be nil. Upper limits only (lower limits: mirror).
func gauge(t *Theme, v, lo, hi float64, warn, crit *float64, w int) string {
	if hi <= lo || w < 3 {
		return ""
	}
	pos := func(x float64) int { return int(math.Round((x - lo) / (hi - lo) * float64(w-1))) }
	fill := min(w, max(0, pos(v)+1))
	st := model.StatusOK
	if crit != nil && v >= *crit {
		st = model.StatusCrit
	} else if warn != nil && v >= *warn {
		st = model.StatusWarn
	}
	wi, ci := -1, -1
	if warn != nil {
		wi = min(w-1, max(0, pos(*warn)))
	}
	if crit != nil {
		ci = min(w-1, max(0, pos(*crit)))
	}
	track := lipgloss.NewStyle().Foreground(t.Track)
	var b strings.Builder
	for i := 0; i < w; i++ {
		switch {
		case i == ci && i >= fill: // ticks only where the track is not filled
			b.WriteString(t.StatusText(model.StatusCrit, "┃"))
		case i == wi && i >= fill:
			b.WriteString(t.StatusText(model.StatusWarn, "╎"))
		case i < fill:
			b.WriteString(t.StatusText(st, "━"))
		default:
			b.WriteString(track.Render("─"))
		}
	}
	return b.String()
}

// listHeight returns how many lines a list (header + listRows) may use when
// avail lines are shared with a findings block of findWant entries. The list
// gets what it needs; findings keep at least two lines plus their heading.
// The findings block itself then fills whatever the list leaves free.
func listHeight(avail, listRows, findWant int) int {
	reserve := 1 + max(1, min(findWant, max(2, avail-(listRows+1)-1)))
	return max(3, min(listRows+1, avail-reserve))
}

// bytesSI formats disk capacities in decimal units like drive vendors do.
func bytesSI(b uint64) string {
	switch {
	case b >= 1e15:
		return fmt.Sprintf("%.1f PB", float64(b)/1e15)
	case b >= 100e12:
		return fmt.Sprintf("%.0f TB", float64(b)/1e12)
	case b >= 1e12:
		return fmt.Sprintf("%.2g TB", float64(b)/1e12)
	case b >= 1e9:
		return fmt.Sprintf("%.0f GB", float64(b)/1e9)
	}
	return fmt.Sprintf("%.0f MB", float64(b)/1e6)
}
