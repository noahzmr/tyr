package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/cpuset"
	"github.com/noahzmr/tyr/internal/model"
)

// cpuGridRow is one row of the socket × thread × core grid.
type cpuGridRow struct {
	label string
	cells []int // index into CPU.Logical, -1 = none
}

// cpuLayout places logical CPUs as socket/thread rows and core columns
// (canvas CPU-Grid), so "cpu37" reads as "socket 0, thread 1, core 5".
type cpuLayout struct {
	cores []int // core ids of the header (first socket)
	rows  []cpuGridRow
	pos   map[int][2]int // logical index → row, column
	cellW int
}

func (c *cpuTab) layout() (cpuLayout, bool) {
	x := c.cpu
	type key struct{ s, core int }
	threads := map[key][]int{}
	socks := map[int]map[int]bool{}
	var unplaced []int
	for i, l := range x.Logical {
		if l.SocketID < 0 || l.CoreID < 0 {
			unplaced = append(unplaced, i)
			continue
		}
		k := key{l.SocketID, l.CoreID}
		threads[k] = append(threads[k], i)
		if socks[l.SocketID] == nil {
			socks[l.SocketID] = map[int]bool{}
		}
		socks[l.SocketID][l.CoreID] = true
	}
	if len(socks) == 0 {
		return cpuLayout{}, false
	}
	var sids []int
	for s := range socks {
		sids = append(sids, s)
	}
	sort.Ints(sids)
	lay := cpuLayout{pos: map[int][2]int{}}
	maxCores := 0
	for si, s := range sids {
		var cores []int
		for co := range socks[s] {
			cores = append(cores, co)
		}
		sort.Ints(cores)
		if si == 0 {
			lay.cores = cores
		}
		maxCores = max(maxCores, len(cores))
		maxT := 0
		for _, co := range cores {
			th := threads[key{s, co}]
			sort.Slice(th, func(a, b int) bool { return x.Logical[th[a]].ID < x.Logical[th[b]].ID })
			threads[key{s, co}] = th
			maxT = max(maxT, len(th))
		}
		for tI := 0; tI < maxT; tI++ {
			r := cpuGridRow{label: fmt.Sprintf("socket %d t%d", s, tI)}
			for _, co := range cores {
				idx := -1
				if th := threads[key{s, co}]; tI < len(th) {
					idx = th[tI]
				}
				if idx >= 0 {
					lay.pos[idx] = [2]int{len(lay.rows), len(r.cells)}
				}
				r.cells = append(r.cells, idx)
			}
			lay.rows = append(lay.rows, r)
		}
	}
	if len(unplaced) > 0 { // offline CPUs whose topology the kernel removed
		r := cpuGridRow{label: "no topology"}
		for _, i := range unplaced {
			lay.pos[i] = [2]int{len(lay.rows), len(r.cells)}
			r.cells = append(r.cells, i)
		}
		lay.rows = append(lay.rows, r)
	}
	maxCols := maxCores
	for _, r := range lay.rows {
		maxCols = max(maxCols, len(r.cells))
	}
	switch {
	case 12+maxCols*5+10 <= c.w:
		lay.cellW = 5
	case 12+maxCols*2 <= c.w:
		lay.cellW = 2 // block glyph only (80×24)
	default:
		return lay, false
	}
	return lay, true
}

// gridMove moves the selection in the grid: dc columns, dr rows.
func (c *cpuTab) gridMove(lay cpuLayout, dr, dc int) {
	p, ok := lay.pos[c.sel]
	if !ok {
		return
	}
	r, col := p[0], p[1]
	for step := 0; step < 64; step++ {
		r, col = r+dr, col+dc
		if r < 0 || r >= len(lay.rows) || col < 0 {
			return
		}
		cells := lay.rows[r].cells
		if col >= len(cells) {
			if dc != 0 {
				return
			}
			col = len(cells) - 1
		}
		if idx := cells[col]; idx >= 0 {
			c.sel = idx
			return
		}
	}
}

var loadBlocks = []rune("▁▂▃▄▅▆▇█")

func loadBlock(v float64) string {
	return string(loadBlocks[min(len(loadBlocks)-1, max(0, int(v/100*float64(len(loadBlocks)-1)+0.5)))])
}

// gridCell renders one CPU: block glyph + number (load % or °C).
func (c *cpuTab) gridCell(t *Theme, i int, w int, selected bool) string {
	if i < 0 {
		return strings.Repeat(" ", w)
	}
	l := &c.cpu.Logical[i]
	var out string
	switch {
	case l.Present && !l.Online:
		out = t.StatusBold(model.StatusCrit, "✖")
		if w == 5 {
			out += t.StatusText(model.StatusCrit, " off")
		}
	case c.tempMode:
		st := tempStatus(c.cpu, l)
		if l.TempCelsius == nil {
			out = t.Sub.Render("·")
			if w == 5 {
				out += t.Sub.Render("   –")
			}
			break
		}
		crit := c.critTemp(l)
		out = t.StatusText(st, loadBlock((*l.TempCelsius-30)/(crit-30)*100))
		if w == 5 {
			out += t.StatusText(st, fmt.Sprintf("%4.0f", *l.TempCelsius))
		}
	default:
		if l.UtilizationPercent == nil {
			out = t.Sub.Render("·")
			if w == 5 {
				out += t.Sub.Render("   –")
			}
			break
		}
		u := *l.UtilizationPercent
		out = lipgloss.NewStyle().Foreground(t.Series[0]).Render(loadBlock(u))
		if w == 5 {
			n := fmt.Sprintf("%4.0f", u)
			if l.Status >= model.StatusWarn {
				n = t.StatusBold(l.Status, n)
			}
			out += n
		}
	}
	out = padCell(out, w, false)
	if selected {
		out = withBg(lipgloss.NewStyle().Bold(true).Render(out), t.SelBg)
	}
	return out
}

// gridView (canvas CPU-Grid): two-column summary, the socket/thread/core
// grid, the selected CPU as three key/value columns, findings, and the
// package temperature graphs in the remaining rows.
func (c *cpuTab) gridView(t *Theme) string {
	x := c.cpu
	lay, ok := c.layout()
	if !ok {
		return c.flatGridView(t)
	}
	summary := c.summaryBlock(t)
	// grid
	var g []string
	legend := "load % · t temperature · ←→↑↓ move"
	if c.tempMode {
		legend = "°C · t load · ←→↑↓ move"
	}
	g = append(g, t.HeadingRight("Logical CPUs", "", legend, c.w))
	if lay.cellW == 5 {
		hdr := pad("", 11) + "core"
		for i, co := range lay.cores {
			if i == 0 {
				hdr += fmt.Sprintf("%2d", co)
				continue
			}
			hdr += fmt.Sprintf("%5d", co)
		}
		g = append(g, t.Sub.Render(hdr))
	}
	for _, r := range lay.rows {
		line := t.Dim.Render(pad(r.label, 12))
		var ids []int
		for _, i := range r.cells {
			line += c.gridCell(t, i, lay.cellW, i == c.sel)
			if i >= 0 {
				ids = append(ids, c.cpu.Logical[i].ID)
			}
		}
		if lay.cellW == 5 && len(ids) > 0 {
			line += " " + t.Sub.Render("cpu"+cpuset.Set(ids).String())
		}
		g = append(g, truncate(line, c.w))
	}
	sel := c.selectedBlock(t)
	top := strings.Join([]string{summary, "", strings.Join(g, "\n"), "", sel}, "\n")
	rest := c.h - lipgloss.Height(top) - 1
	findN := min(countNonOK(x.Findings), 4) + 1
	graphs := ""
	if gh := rest - findN - 1; gh >= 7 && len(x.PackageTemps) > 0 {
		graphs = c.packageGraphs(t, gh)
		rest -= gh + 1
	}
	out := top + "\n\n" + findingsSection(t, "CPU", x.Findings, x.Sources, max(2, rest), c.w)
	if graphs != "" {
		out += "\n\n" + graphs
	}
	return out
}

func (c *cpuTab) summaryBlock(t *Theme) string {
	x := c.cpu
	online := 0
	var offline []int
	for _, l := range x.Logical {
		if l.Online {
			online++
		} else {
			offline = append(offline, l.ID)
		}
	}
	mc := orDash(x.Microcode)
	revs := map[string]bool{}
	for _, l := range x.Logical {
		if l.Online && l.Microcode != "" {
			revs[l.Microcode] = true
		}
	}
	if len(revs) == 1 {
		mc += " " + t.Dot(model.StatusOK) + t.Dim.Render(fmt.Sprintf(" same on all %d", online))
	} else if len(revs) > 1 {
		mc += " " + t.StatusBold(model.StatusWarn, "▲ mixed")
	}
	smt := ""
	if x.SMTControl != "" {
		smt = " SMT " + x.SMTControl
	}
	on := x.Topology.Online
	if len(offline) > 0 {
		on += "  " + t.StatusBold(model.StatusCrit, "✖ "+cpuset.Set(offline).String()+" offline")
	}
	ct := ""
	if x.CoreTest != nil {
		st := model.StatusOK
		word := "PASS"
		if x.CoreTest.Failed+x.CoreTest.TimedOut > 0 {
			st, word = model.StatusCrit, "FAIL"
		}
		ct = t.inlineStatus(st, word) + t.Dim.Render(fmt.Sprintf(" · %d/%d verified", x.CoreTest.Passed, x.CoreTest.Passed+x.CoreTest.Failed+x.CoreTest.TimedOut))
	}
	left := kvBlock(t, []kvPair{{"Model", x.Model}, {"Microcode", mc},
		{"Topology", fmt.Sprintf("%d sockets · %d cores · %d threads%s", x.SocketCount, x.CoreCount, x.ThreadCount, smt)},
		{"Online", on}, {"Core test", ct}}, "not run")
	freq := fmt.Sprintf("%s base · %s max GHz", ghz(x.BaseFreqMHz), ghz(x.MaxFreqMHz))
	if x.FreqDriver != "" {
		freq += " " + t.Dim.Render(x.FreqDriver)
	}
	gov := x.Governor
	if x.EPP != "" {
		gov += " · EPP " + x.EPP
	}
	if x.Boost != nil {
		gov += " · boost " + map[bool]string{true: "on", false: "off"}[*x.Boost]
	}
	rows := []kvPair{{"Frequency", freq}, {"Governor", gov}}
	for _, p := range x.PackageTemps {
		if p.Value == nil {
			continue
		}
		rows = append(rows, kvPair{packageLabel(p.Name), sensorGaugeLine(t, p, 16)})
	}
	var ppin []string
	for _, s := range x.Sockets {
		if s.PPIN != "" {
			ppin = append(ppin, fmt.Sprintf("S%d %s", max(0, s.ID), s.PPIN))
		} else if s.Serial != "" {
			ppin = append(ppin, fmt.Sprintf("S%d %s", max(0, s.ID), s.Serial))
		}
	}
	if len(ppin) > 0 {
		rows = append(rows, kvPair{"PPIN/S/N", t.Bold.Render(strings.Join(ppin, "  "))})
	}
	right := kvBlock(t, rows, "")
	if c.w < 100 {
		return left
	}
	return kvColumns(c.w, left, right)
}

// packageLabel turns "CPU0 Package" into "Package 0".
func packageLabel(name string) string {
	var n int
	if _, err := fmt.Sscanf(name, "CPU%d Package", &n); err == nil {
		return fmt.Sprintf("Package %d", n)
	}
	return name
}

func ghz(v *float64) string {
	if v == nil {
		return "–"
	}
	return fmt.Sprintf("%.1f", *v/1000)
}

// selectedBlock: the selected logical CPU as three key/value columns.
func (c *cpuTab) selectedBlock(t *Theme) string {
	if len(c.cpu.Logical) == 0 {
		return ""
	}
	l := c.cpu.Logical[c.sel]
	id := cpuKey(l.ID)
	head := t.HeadingFocus(id, "", "enter full page", c.w)
	state := t.inlineStatus(model.StatusOK, "online")
	if !l.Online {
		state = t.inlineStatus(model.StatusCrit, "offline")
	}
	loc := fmt.Sprintf("socket %s · core %s", intOrDash(l.SocketID), intOrDash(l.CoreID))
	sib := ""
	if l.Siblings != "" {
		sib = l.Siblings
	}
	mce := uint64(0)
	if l.MCEExceptions != nil {
		mce += *l.MCEExceptions
	}
	if l.MCELogged != nil {
		mce += *l.MCELogged
	}
	mceTxt := fmt.Sprint(mce)
	if mce > 0 {
		mceTxt = t.StatusBold(model.StatusWarn, mceTxt)
	}
	thr := optU(l.ThrottleCount)
	if l.ThrottleCount != nil && *l.ThrottleCount > 0 {
		thr = t.StatusBold(model.StatusWarn, thr)
	}
	test := string(l.CoreTest)
	if l.CoreTest == model.CoreTestFail || l.CoreTest == model.CoreTestTimeout {
		test = t.StatusBold(model.StatusCrit, test)
	}
	c1 := kvBlock(t, []kvPair{{"State", state}, {"Location", loc}, {"Siblings", sib}}, "")
	c2 := kvBlock(t, []kvPair{{"Load", optF(l.UtilizationPercent, "%.0f %%")}, {"MHz", optF(l.FreqMHz, "%.0f")}, {"Temp", optF(l.TempCelsius, "%.0f °C")}}, "")
	c3 := kvBlock(t, []kvPair{{"MCE", mceTxt}, {"Throttle", thr}, {"Core test", test}, {"Deep idle", deepestShare(l.IdleStates)}}, "not run")
	if c.w < 100 {
		return head + "\n" + kvColumns(c.w, c1, c3)
	}
	return head + "\n" + kvColumns(c.w, c1, c2, c3)
}

// packageGraphs: one braille graph per package, side by side, with the
// crit limit drawn as a labelled line.
func (c *cpuTab) packageGraphs(t *Theme, h int) string {
	var graphs []string
	ps := c.cpu.PackageTemps
	n := min(len(ps), 2)
	if n == 0 {
		return ""
	}
	w := c.w / n
	for i := 0; i < n; i++ {
		p := ps[i]
		hist := t.sess.history("cpu/" + p.Name + "/temp")
		var lim []chartLimit
		if p.Thresholds != nil && p.Thresholds.UpperCrit != nil {
			lim = append(lim, chartLimit{V: *p.Thresholds.UpperCrit, Label: fmt.Sprintf("crit %.0f", *p.Thresholds.UpperCrit), St: model.StatusCrit})
		}
		val := ""
		if p.Value != nil {
			val = check.FmtTemp(*p.Value)
		}
		hi := 105.0
		if p.Thresholds != nil && p.Thresholds.UpperCrit != nil {
			hi = *p.Thresholds.UpperCrit + 10
		}
		g := brailleChart(t, chartSpec{Title: packageLabel(p.Name) + " °C", Value: val, ValueSt: p.Status,
			Series: hist, Min: 20, Max: hi, Limits: lim, Color: t.Series[i%4], Window: time.Duration(len(hist)) * 5 * time.Second}, w-1, h)
		graphs = append(graphs, g)
	}
	if len(graphs) == 1 {
		return graphs[0]
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, graphs[0], " ", graphs[1])
}
