package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/noahzmr/tyr/internal/model"
)

func f64(v float64) *float64 { return &v }

func TestGauge(t *testing.T) {
	th := NewTheme(true)
	g := ansi.Strip(gauge(th, 44, 20, 70, f64(55), f64(65), 16))
	if ansi.StringWidth(g) != 16 || !strings.Contains(g, "━") || !strings.Contains(g, "╎") || !strings.Contains(g, "┃") || !strings.Contains(g, "─") {
		t.Errorf("gauge %q", g)
	}
	if !strings.HasPrefix(g, "━━━━━━━━") {
		t.Errorf("44 of 20–70 should fill about half: %q", g)
	}
	// Past the crit limit the bar is full, ticks remain.
	if g := ansi.Strip(gaugeVal(th, 96, 20, 105, f64(85), f64(95), 14, "96 °C")); !strings.HasSuffix(g, " 96 °C") || strings.ContainsAny(g, "╎┃") {
		t.Errorf("full gauge %q", g)
	}
	// Lower is worse: rx power below warn.
	if g := ansi.Strip(gaugeInv(th, -11.8, -16, -2, f64(-10), f64(-12), 14, "−11.8 dBm")); !strings.Contains(g, "╎") || !strings.HasSuffix(g, "dBm") {
		t.Errorf("inverted gauge %q", g)
	}
}

func TestKVBlockAndColumns(t *testing.T) {
	th := NewTheme(true)
	b := ansi.Strip(kvBlock(th, []kvPair{{"Model", "ST4000"}, {"Asset tag", ""}, {"Serial", "ZC1A9K3R"}}, "not set"))
	lines := strings.Split(b, "\n")
	if lines[0] != "Model      ST4000" || lines[1] != "Asset tag  not set" {
		t.Errorf("kv block:\n%s", b)
	}
	cols := ansi.Strip(kvColumns(90, "a\nb", "c", "d"))
	if w := lipgloss.Width(strings.Split(cols, "\n")[0]); w != 90 {
		t.Errorf("columns width %d", w)
	}
}

func TestBrailleChart(t *testing.T) {
	th := NewTheme(true)
	var s []float64
	for i := range 80 {
		s = append(s, 60+float64(i%20))
	}
	c := ansi.Strip(brailleChart(th, chartSpec{Title: "Hottest CPU °C", Value: "79 °C", Series: s, Min: 20, Max: 110,
		Limits: []chartLimit{{V: 100, Label: "guard 100", St: model.StatusCrit}}, Color: th.Series[0], Window: 4 * time.Minute}, 60, 10))
	lines := strings.Split(c, "\n")
	if len(lines) != 10 {
		t.Fatalf("height %d:\n%s", len(lines), c)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 60 {
			t.Errorf("line %d is %d wide: %q", i, w, l)
		}
	}
	if !strings.HasPrefix(lines[0], "Hottest CPU °C") || !strings.HasSuffix(lines[0], "79 °C") {
		t.Errorf("title row %q", lines[0])
	}
	if !strings.Contains(c, "110┤") || !strings.Contains(c, " 20┤") || !strings.Contains(c, "┄ guard 100") || !strings.Contains(c, "└──") ||
		!strings.Contains(lines[9], "-4m") || !strings.Contains(lines[9], "-2m") || !strings.HasSuffix(lines[9], "now") {
		t.Errorf("axes/limit:\n%s", c)
	}
	// Too short for a plot: sparkline in one line.
	if c := ansi.Strip(brailleChart(th, chartSpec{Title: "Load", Value: "50%", Series: s, Color: th.Series[0]}, 40, 5)); strings.Contains(c, "\n") || !strings.ContainsAny(c, "▁▂▃▄▅▆▇█") {
		t.Errorf("sparkline fallback %q", c)
	}
}

func TestTreeFolding(t *testing.T) {
	root := &hwNode{label: "host"}
	cpu := root.add(&hwNode{label: "CPU0"})
	for i := range 6 {
		cpu.add(&hwNode{label: "Core " + string(rune('0'+i))})
	}
	cpu.children[3].status = model.StatusCrit
	root.rollup()
	root.setPaths("")
	rows := flatten(root, map[string]bool{})
	var labels []string
	for _, r := range rows {
		labels = append(labels, r.node.label)
	}
	got := strings.Join(labels, "|")
	if got != "host|CPU0|Core 0–2|Core 3|Core 4|Core 5" {
		t.Errorf("folding: %s", got)
	}
	th := NewTheme(true)
	for _, r := range rows {
		if r.fold && !strings.Contains(ansi.Strip(treeInfo(th, r)), "3 healthy") {
			t.Errorf("fold info %q", ansi.Strip(treeInfo(th, r)))
		}
	}
	// opening the fold shows its children
	rows = flatten(root, map[string]bool{rows[2].node.path: true})
	if len(rows) != 9 {
		t.Errorf("opened fold: %d rows", len(rows))
	}
}

func TestTableConventions(t *testing.T) {
	th := NewTheme(true)
	var rows []row
	for i := range 30 {
		st := model.StatusOK
		if i == 1 {
			st = model.StatusCrit
		}
		rows = append(rows, row{cells: []string{"disk" + string(rune('a'+i)), "4.0 TB"}, status: st})
	}
	rows[2].dim = true
	out := renderList(th, []column{{"Disk", 8}, {">Size", 8}}, rows, 1, 60, 10)
	lines := strings.Split(ansi.Strip(out), "\n")
	if !strings.HasPrefix(lines[0], "   Disk") || !strings.HasSuffix(strings.TrimRight(lines[0], " "), "Size") {
		t.Errorf("header %q", lines[0])
	}
	if !strings.HasPrefix(lines[2], "▌✖ diskb") {
		t.Errorf("selected problem row %q", lines[2])
	}
	if !strings.HasPrefix(lines[3], " ○ diskc") {
		t.Errorf("dimmed row %q", lines[3])
	}
	if !strings.Contains(lines[1], "  4.0 TB") {
		t.Errorf("numbers right-aligned: %q", lines[1])
	}
	last := func(l string) string { r := []rune(l); return string(r[len(r)-1]) }
	if last(lines[1]) != "┃" || last(lines[9]) != "│" {
		t.Errorf("scrollbar: %q %q", lines[1], lines[9])
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > 60 {
			t.Errorf("row too wide: %q", l)
		}
	}
}

func TestOverlayFooterBadges(t *testing.T) {
	th := NewTheme(true)
	base := strings.Repeat(strings.Repeat("x", 120)+"\n", 39) + strings.Repeat("x", 120)
	o := ansi.Strip(overlay(th, base, th.Warn, "▲ Confirm", "Abort the running CPU test?", 120, 40))
	ls := strings.Split(o, "\n")
	if len(ls) != 40 || !strings.Contains(o, "╭─ ▲ Confirm") || !strings.Contains(o, "Abort the running CPU test?") {
		t.Errorf("overlay:\n%s", o)
	}
	f := ansi.Strip(footerHints(th, [][2]string{{"↑↓", "move"}, {"enter", "open"}, {"v", "view"}, {"!", "problems"}, {"n", "next problem"}},
		toast(th, model.StatusOK, "exported /root/x.json"), 60))
	if !strings.Contains(f, "? help") || !strings.HasSuffix(f, "● exported /root/x.json") || ansi.StringWidth(f) > 60 {
		t.Errorf("footer %q", f)
	}
	if b := ansi.Strip(th.verdictBadge(model.StatusOK, "PASS")); b != " ● PASS " {
		t.Errorf("verdict badge %q", b)
	}
	if b := ansi.Strip(th.Badge(model.StatusWarn)); b != " ▲ WARN " {
		t.Errorf("badge %q", b)
	}
}

func TestHangWrapsWithIndent(t *testing.T) {
	got := hang("Last  ", strings.Repeat("word ", 20), 40)
	lines := strings.Split(got, "\n")
	if len(lines) < 3 {
		t.Fatalf("not wrapped: %q", got)
	}
	for i, l := range lines {
		if lipgloss.Width(l) > 40 {
			t.Errorf("line %d too wide: %q", i, l)
		}
		if i > 0 && !strings.HasPrefix(l, "      word") {
			t.Errorf("line %d not indented under the value: %q", i, l)
		}
	}
	if hang("a ", "short", 40) != "a short" {
		t.Error("short text changed")
	}
}

func TestAlertKeys(t *testing.T) {
	m := newLoaded(t, "server-degraded", 100, 40)
	run(m, "A")
	if m.page == nil || m.page.title != "Alert reference" || !strings.Contains(m.page.copyText, "storage/crc_errors") {
		t.Fatalf("A did not open the alert reference")
	}
	run(m, "esc")
	run(m, "1", "n", "i")
	if m.page == nil || !strings.Contains(m.page.copyText, "## What it means") {
		t.Fatalf("i on a finding did not open its explanation")
	}
	// A also works from a component page (its "About these alerts" hint)
	run(m, "esc", "4", "enter")
	if m.page == nil || !strings.Contains(ansi.Strip(m.page.vp.GetContent()), "About these alerts") {
		t.Fatalf("disk page lacks About these alerts")
	}
	run(m, "A")
	if m.page == nil || m.page.title != "Alert reference" {
		t.Fatalf("A from a page did not open the reference")
	}
}
