package tui

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
)

// chartLimit is a horizontal limit line (crit 95, guard 100).
type chartLimit struct {
	V     float64
	Label string
	St    model.Status
}

// chartSpec describes a braille line graph (canvas: Components › Braille
// graph): title row with the current value at the right, y labels (top,
// middle, bottom) with ┤ ticks on a │ axis, braille plot (2×4 dots per
// cell, newest point at the right edge), limits as ┄ across empty cells
// with their label at the right end, x axis └──── with -window, mid, now.
type chartSpec struct {
	Title   string
	Value   string       // current value text
	ValueSt model.Status // its colour (by limits)
	Series  []float64    // oldest first
	Min     float64      // y axis minimum
	Max     float64      // y axis maximum; 0 = auto (largest value or limit * 1.1)
	Limits  []chartLimit
	Color   color.Color
	Window  time.Duration // time span of the visible points (x labels)
	YFmt    func(float64) string
}

// brailleChart renders spec in w × h cells. Below 4 plot rows it falls back
// to a one-line sparkline.
func brailleChart(t *Theme, s chartSpec, w, h int) string {
	yf := s.YFmt
	if yf == nil {
		yf = trimFloat
	}
	val := ""
	if s.Value != "" {
		val = t.StatusBold(s.ValueSt, s.Value)
	}
	title := t.Bold.Render(s.Title)
	ph := h - 3 // title, x axis, x labels
	if ph < 4 {
		line := title + " " + spark(s.Series, max(4, w-lipgloss.Width(title)-lipgloss.Width(val)-3), s.Max, s.Color)
		return padCell(line, w-lipgloss.Width(val)-1, false) + " " + val
	}
	const lw = 5 // y label width, then the ┤/│ column
	pw := max(4, w-lw-1)
	maxV := s.Max
	if maxV == 0 {
		for _, v := range s.Series {
			maxV = math.Max(maxV, v)
		}
		for _, l := range s.Limits {
			maxV = math.Max(maxV, l.V)
		}
		maxV = math.Max(maxV*1.1, s.Min+1e-9)
	}
	span := maxV - s.Min
	dotsX, dotsY := pw*2, ph*4
	grid := make([][]rune, ph)
	for i := range grid {
		grid[i] = []rune(strings.Repeat(string(rune(0x2800)), pw))
	}
	set := func(x, y int) {
		if x < 0 || x >= dotsX || y < 0 || y >= dotsY {
			return
		}
		bit := [2][4]rune{{0x40, 0x04, 0x02, 0x01}, {0x80, 0x20, 0x10, 0x08}}[x%2][y%4]
		grid[ph-1-y/4][x/2] |= bit
	}
	toY := func(v float64) int {
		return int(math.Round((math.Min(math.Max(v, s.Min), maxV) - s.Min) / span * float64(dotsY-1)))
	}
	pts := s.Series
	if len(pts) > dotsX {
		pts = pts[len(pts)-dotsX:]
	}
	off := dotsX - len(pts)
	prevY := -1
	for i, v := range pts {
		y := toY(v)
		if prevY >= 0 {
			for yy := min(prevY, y); yy <= max(prevY, y); yy++ {
				set(off+i, yy)
			}
		} else {
			set(off+i, y)
		}
		prevY = y
	}
	limitRow := map[int]chartLimit{}
	for _, l := range s.Limits {
		if l.V >= s.Min && l.V <= maxV {
			limitRow[ph-1-toY(l.V)/4] = l
		}
	}
	line := lipgloss.NewStyle().Foreground(s.Color)
	var rows []string
	for r := 0; r < ph; r++ {
		label, tick := "", "│"
		switch r {
		case 0:
			label, tick = yf(maxV), "┤"
		case ph / 2:
			label, tick = yf(s.Min+span*float64(ph-1-r)/float64(ph-1)), "┤"
		case ph - 1:
			label, tick = yf(s.Min), "┤"
		}
		var b strings.Builder
		cells := grid[r]
		lim, isLim := limitRow[r]
		tail := ""
		if isLim {
			tail = " " + lim.Label
		}
		plotW := pw - len([]rune(tail))
		for c := 0; c < pw; c++ {
			ch := cells[c]
			switch {
			case c >= plotW && isLim:
				// label area
			case ch != 0x2800:
				b.WriteString(line.Render(string(ch)))
			case isLim:
				b.WriteString(t.StatusText(lim.St, "┄"))
			default:
				b.WriteRune(' ')
			}
		}
		if isLim {
			b.WriteString(t.StatusText(lim.St, tail))
		}
		rows = append(rows, t.Sub.Render(fmt.Sprintf("%*s", lw, label)+tick)+b.String())
	}
	head := padCell(title, w-lipgloss.Width(val), false) + val
	axis := t.Sub.Render(strings.Repeat(" ", lw+1) + "└" + strings.Repeat("─", max(0, pw-1)))
	xl := ""
	if s.Window > 0 {
		left := "-" + fmtWindow(s.Window)
		mid := "-" + fmtWindow(s.Window/2)
		gap1 := max(1, pw/2-len(left)-len(mid)/2)
		gap2 := max(1, w-(lw+2)-len(left)-gap1-len(mid)-3)
		xl = t.Sub.Render(strings.Repeat(" ", lw+2) + left + strings.Repeat(" ", gap1) + mid + strings.Repeat(" ", gap2) + "now")
	}
	return strings.Join(append(append([]string{head}, rows...), axis, xl), "\n")
}

func fmtWindow(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02d", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// trimFloat formats an axis label compactly: 1.2G, 340M, 52k, 98, 2.5.
func trimFloat(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.1fG", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.0fM", v/1e6)
	case v >= 1e4:
		return fmt.Sprintf("%.0fk", v/1e3)
	case v >= 100 || v == math.Trunc(v):
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}
