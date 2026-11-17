package tui

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/collector/all"
	"github.com/noahzmr/tyr/internal/fixture"
)

const fixtures = "../../testdata/fixtures/"

var allFixtures = []string{"server-healthy", "server-degraded", "server-bmc-only", "laptop-amd-nonroot"}

// newLoaded returns a model with a collected snapshot and the given size.
func newLoaded(t *testing.T, name string, w, h int) *Model {
	t.Helper()
	env, _, err := fixture.Load(fixtures+name, collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	m := New(env, all.Collectors(), all.Implemented, Options{FixtureName: name, ExportDir: t.TempDir()})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	snap := collector.Collect(context.Background(), env, all.Collectors(), nil)
	m.Update(snapshotMsg{snap: snap})
	return m
}

func press(s string) tea.KeyPressMsg {
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func screen(m *Model) string { return m.View().Content }

func checkFits(t *testing.T, label, s string, w, h int) {
	t.Helper()
	lines := strings.Split(s, "\n")
	if len(lines) != h {
		t.Errorf("%s: %d lines, want %d", label, len(lines), h)
	}
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw > w {
			t.Errorf("%s: line %d is %d cells wide (max %d): %q", label, i, lw, w, ansi.Strip(l))
		}
	}
}

func TestEveryTabFits(t *testing.T) {
	for _, name := range allFixtures {
		for _, size := range [][2]int{{80, 24}, {160, 50}, {100, 30}} {
			m := newLoaded(t, name, size[0], size[1])
			for i := range m.tabs {
				m.active = i
				s := screen(m)
				checkFits(t, name+"/"+m.tabs[i].Title(), s, size[0], size[1])
				if os.Getenv("TYR_DUMP") != "" && size[0] == 80 {
					t.Logf("\n=== %s / %s (80x24) ===\n%s", name, m.tabs[i].Title(), ansi.Strip(s))
				}
			}
		}
	}
}

func TestHeaderAndOverview(t *testing.T) {
	m := newLoaded(t, "server-degraded", 120, 40)
	s := ansi.Strip(screen(m))
	for _, want := range []string{"TYR", "srv-degraded-01", "Supermicro SYS-120U-TNR", "S/N S412345X3A01234", "✖ CRIT", "fixture", "25 crit 27 warn",
		"63/64 online · max 96°C", "512 GB · 16/32 slots", "CRIT · 8", "Needs attention",
		"Findings 25 ✖ · 27 ▲", "worst first", "▸ 1 core offline (cpu7)", "✖ CPU      cpu7", "/data 96% full", "▸ 1 of 2 boots in the"} {
		if !strings.Contains(s, want) {
			t.Errorf("overview missing %q\n%s", want, s)
		}
	}
	// Findings use the free space instead of stopping after a fixed few.
	if strings.Count(s, " ✖ ")+strings.Count(s, " ▲ ") < 15 {
		t.Errorf("overview should fill the free space with findings\n%s", s)
	}
	nonroot := ansi.Strip(screen(newLoaded(t, "laptop-amd-nonroot", 120, 40)))
	if !strings.Contains(nonroot, "not root") {
		t.Errorf("non-root hint missing\n%s", nonroot)
	}
}

func TestNavigation(t *testing.T) {
	m := newLoaded(t, "server-degraded", 120, 40)
	m.Update(press("tab"))
	if m.active != 1 {
		t.Fatalf("tab -> %d", m.active)
	}
	m.Update(press("shift+tab"))
	m.Update(press("shift+tab"))
	if m.active != len(m.tabs)-1 {
		t.Fatalf("shift+tab wrap -> %d", m.active)
	}
	m.Update(press("3"))
	if m.tabs[m.active].Title() != "Memory" {
		t.Fatalf("3 -> %s", m.tabs[m.active].Title())
	}
	// Overview: select the Memory card and press enter -> Memory tab
	m.Update(press("1"))
	m.Update(press("right"))
	_, cmd := m.Update(press("enter"))
	if cmd == nil {
		t.Fatal("enter on a card must switch tabs")
	}
	m.Update(cmd())
	if m.tabs[m.active].Title() != "Memory" {
		t.Fatalf("enter -> %s", m.tabs[m.active].Title())
	}
}

func TestCPUCoresAndDetails(t *testing.T) {
	m := newLoaded(t, "server-degraded", 120, 40)
	m.Update(press("2"))
	m.Update(press("v"))
	if s := ansi.Strip(screen(m)); !strings.Contains(s, "cpu0 · idle states") || !strings.Contains(s, "C6    ") || !strings.Contains(s, "60 %") {
		t.Errorf("cpu0 detail\n%s", s)
	}
	m.Update(press("home")) // problems first: the offline cpu7 is the top row
	s := ansi.Strip(screen(m))
	for _, want := range []string{"Grid · Cores · Details", "SMT", "C-idle", "C6 60%", "cpu7 · idle states", "✖ offline"} {
		if !strings.Contains(s, want) {
			t.Errorf("cores view missing %q\n%s", want, s)
		}
	}
	m.Update(press("v"))
	s = ansi.Strip(screen(m))
	for _, want := range []string{"Identification", "family 6 · model 106 · stepping 6", "(06h/6Ah)", "PPIN 0x2f1a9c4e5b6d7000", "lines 1–"} {
		if !strings.Contains(s, want) {
			t.Errorf("details view missing %q\n%s", want, s)
		}
	}
	for range 80 {
		m.Update(press("down"))
	}
	s = ansi.Strip(screen(m))
	for _, want := range []string{"gather_data_sampling", "Vulnerable: No microcode", "avx512f"} {
		if !strings.Contains(s, want) {
			t.Errorf("details view (scrolled) missing %q\n%s", want, s)
		}
	}
	checkFits(t, "cpu details", screen(m), 120, 40)
	lap := newLoaded(t, "laptop-amd-nonroot", 100, 30)
	lap.Update(press("2"))
	lap.Update(press("v"))
	if s := ansi.Strip(screen(lap)); !strings.Contains(s, "Rank") || !strings.Contains(s, "Type") || !strings.Contains(s, " P ") {
		t.Errorf("laptop cores view\n%s", s)
	}
}

func TestCPUTab(t *testing.T) {
	m := newLoaded(t, "server-degraded", 120, 40)
	m.Update(press("2"))
	m.tabs[1].(*cpuTab).Focus("cpu7")
	m.tabs[1].(*cpuTab).view = 0
	s := ansi.Strip(screen(m))
	if !strings.Contains(s, "cpu7 ───") || !strings.Contains(s, "✖ offline") || !strings.Contains(s, "Silver 4314") || !strings.Contains(s, "socket 1 t1") || !strings.Contains(s, "✖ off") {
		t.Errorf("cpu tab\n%s", s)
	}
	for range 3 { // spatial moves stay on placed CPUs
		m.Update(press("up"))
	}
	m.Update(press("t"))
	if s := ansi.Strip(screen(m)); !strings.Contains(s, "°C · t load") || !strings.Contains(s, "96") {
		t.Errorf("temp mode\n%s", s)
	}
}

func TestMemoryTab(t *testing.T) {
	m := newLoaded(t, "server-degraded", 120, 40)
	m.Update(press("3"))
	s := ansi.Strip(screen(m))
	for _, want := range []string{"P1-DIMMA1", "M393A4K40EB3-CWE", "2933/3200", "14 corrected", "1 uncorrected", "poisoned 4 kB", "16 of 32 slots", "+ 16 empty slots hidden"} {
		if !strings.Contains(s, want) {
			t.Errorf("memory tab missing %q\n%s", want, s)
		}
	}
	if strings.Contains(s, "P1-DIMMA2") {
		t.Error("empty slots must be hidden by default")
	}
	m.Update(press("a"))
	if !strings.Contains(ansi.Strip(screen(m)), "P1-DIMMA2") {
		t.Error("a must show empty slots")
	}
	if s := ansi.Strip(screen(newLoaded(t, "laptop-amd-nonroot", 100, 30).withTab(2))); !strings.Contains(s, "needs root") {
		t.Errorf("non-root memory tab\n%s", s)
	}
}

func (m *Model) withTab(i int) *Model { m.active = i; return m }

func TestExportPauseRefresh(t *testing.T) {
	m := newLoaded(t, "server-healthy", 100, 30)
	m.Update(press("e"))
	if !strings.HasPrefix(m.message, "exported ") {
		t.Fatalf("export: %q", m.message)
	}
	p := strings.TrimSuffix(strings.TrimPrefix(m.message, "exported "), " and .md")
	if b, err := os.ReadFile(p); err != nil || !strings.Contains(string(b), `"schema_version": "1.0"`) {
		t.Errorf("export file: %v", err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("export mode %v", fi.Mode().Perm())
	}
	m.Update(press("p"))
	if !m.paused || !strings.Contains(ansi.Strip(screen(m)), "paused") {
		t.Error("pause")
	}
	// a tick while paused must not start a collection
	if _, cmd := m.Update(tickMsg{seq: m.tickSeq}); cmd != nil || m.collecting {
		t.Error("tick while paused collected")
	}
	_, cmd := m.Update(press("p"))
	if cmd == nil || !m.collecting {
		t.Fatal("resume must collect")
	}
	// second refresh while collecting is refused
	m.Update(press("r"))
	if m.message != "collection already running" {
		t.Errorf("refresh while collecting: %q", m.message)
	}
	msg := cmd()
	if _, ok := msg.(snapshotMsg); !ok {
		t.Fatalf("collect returned %T", msg)
	}
	_, next := m.Update(msg)
	if next == nil || m.collecting {
		t.Error("snapshot must schedule the next tick")
	}
	// a stale tick (older sequence) is ignored
	if _, cmd := m.Update(tickMsg{seq: m.tickSeq - 1}); cmd != nil {
		t.Error("stale tick collected")
	}
}

func TestTooSmallAndLoading(t *testing.T) {
	env, _, _ := fixture.Load(fixtures+"server-healthy", collector.DefaultOptions())
	m := New(env, all.Collectors(), all.Implemented, Options{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if s := ansi.Strip(screen(m)); !strings.Contains(s, "collecting hardware data") {
		t.Errorf("loading screen\n%s", s)
	}
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 8})
	if s := screen(m); !strings.Contains(s, "too small") {
		t.Errorf("small screen: %q", s)
	}
	if _, cmd := m.Update(press("q")); cmd == nil {
		t.Error("q must quit")
	}
}

func TestInventoryTabSearchAndFilter(t *testing.T) {
	m := newLoaded(t, "server-degraded", 160, 50)
	m.Update(press("0"))
	if m.tabs[m.active].Title() != "Inventory" {
		t.Fatalf("0 -> %s", m.tabs[m.active].Title())
	}
	s := ansi.Strip(screen(m))
	for _, want := range []string{"S412345X3A01234", "P1K22CL21123452", "ZFL4A002", "B49691A00010", "PPIN 0x2f1a9c4e5b6d7000", "Part number"} {
		if !strings.Contains(s, want) {
			t.Errorf("inventory missing %q", want)
		}
	}
	// search: keys go to the input, so "q" and "1" must not quit or switch tabs
	m.Update(press("/"))
	for _, r := range "zfl4a002" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if m.tabs[m.active].Title() != "Inventory" {
		t.Fatal("typing must not switch tabs")
	}
	m.Update(press("enter"))
	s = ansi.Strip(screen(m))
	if !strings.Contains(s, "1 of 48 match") || !strings.Contains(s, "c0 252:2") {
		t.Errorf("search result\n%s", s)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.Update(press("/"))
	for _, r := range "1324420012346" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.Update(press("enter"))
	if s := ansi.Strip(screen(m)); !strings.Contains(s, "GPU1 ca:00.0") || !strings.Contains(s, "900-21001-0020-100") {
		t.Errorf("gpu search\n%s", s)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.Update(press("f")) // first category: system
	if s := ansi.Strip(screen(m)); !strings.Contains(s, "esc shows all categories") || !strings.Contains(s, "1 of 48 match") {
		t.Errorf("filter\n%s", s)
	}
	// narrow terminal still shows serials
	n := newLoaded(t, "server-degraded", 80, 24)
	n.Update(press("0"))
	if s := ansi.Strip(screen(n)); !strings.Contains(s, "Serial") || !strings.Contains(s, "S412345X3A01234") {
		t.Errorf("80 cols\n%s", s)
	}
	if s := ansi.Strip(screen(newLoaded(t, "laptop-amd-nonroot", 120, 40).withTab(9))); !strings.Contains(s, "need root") {
		t.Errorf("non-root hint\n%s", s)
	}
}
