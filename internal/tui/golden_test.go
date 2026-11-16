package tui

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/collector/all"
	"github.com/noahzmr/tyr/internal/fixture"
)

var update = flag.Bool("update", false, "rewrite the golden screens in testdata/golden")

// goldenScreens drives the server-degraded fixture to each screen of the
// redesign (one per canvas board where the data exists).
var goldenScreens = []struct {
	name string
	keys []string
	prep func(m *Model)
}{
	{name: "overview", keys: []string{"1"}},
	{name: "overview-findings", keys: []string{"1", "n", "down"}},
	{name: "cpu-grid", keys: []string{"2"}},
	{name: "cpu-cores", keys: []string{"2", "v"}},
	{name: "cpu-details", keys: []string{"2", "v", "v"}},
	{name: "memory-table", keys: []string{"3"}},
	{name: "memory-slotmap", keys: []string{"3", "v"}},
	{name: "storage-disks", keys: []string{"4"}},
	{name: "storage-raid", keys: []string{"4", "v"}},
	{name: "storage-filesystems", keys: []string{"4", "v", "v"}},
	{name: "power", keys: []string{"5"}},
	{name: "network", keys: []string{"6"}},
	{name: "gpu", keys: []string{"7"}},
	{name: "events", keys: []string{"8"}},
	{name: "stress-form", keys: []string{"9"}},
	{name: "stress-confirm", keys: []string{"9"}, prep: func(m *Model) {
		st := m.stressTab()
		m.Update(st.prepare()())
	}},
	{name: "inventory-list", keys: []string{"0"}},
	{name: "inventory-search", keys: []string{"0", "/", "s", "d", "d", "enter"}},
	{name: "inventory-tree", keys: []string{"0", "v"}},
	{name: "detail-disk", keys: []string{"4", "enter"}},
	{name: "detail-search", keys: []string{"4", "enter", "/", "r", "e", "a", "l", "l", "o", "c", "enter"}},
	{name: "help", keys: []string{"4", "?"}},
}

var goldenThemes = []struct {
	name    string
	dark    bool
	profile colorprofile.Profile
}{
	{"dark", true, colorprofile.TrueColor},
	{"light", false, colorprofile.TrueColor},
	{"nocolor", true, colorprofile.Ascii},
}

// TestGoldenScreens compares every screen at 80×24, 120×40 and 160×50
// with testdata/golden/<screen>-<W>x<H>.txt (ANSI stripped). Dark, light
// and NO_COLOR must render the same text: status never depends on
// colour. go test ./internal/tui -run TestGoldenScreens -update rewrites
// the files.
func TestGoldenScreens(t *testing.T) {
	env, _, err := fixture.Load(fixtures+"server-degraded", collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	snap := collector.Collect(context.Background(), env, all.Collectors(), nil)
	load := func(w, h int) *Model {
		m := New(env, all.Collectors(), all.Implemented, Options{FixtureName: "server-degraded", ExportDir: t.TempDir()})
		m.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.Update(snapshotMsg{snap: snap})
		return m
	}
	for _, size := range [][2]int{{80, 24}, {120, 40}, {160, 50}} {
		for _, sc := range goldenScreens {
			name := fmt.Sprintf("%s-%dx%d", sc.name, size[0], size[1])
			t.Run(name, func(t *testing.T) {
				var first string
				for _, th := range goldenThemes {
					m := load(size[0], size[1])
					m.dark, m.profile = th.dark, th.profile
					m.retheme()
					m.stressTab().env.RunDir = "/nonexistent/tyr-runs"
					m.stressTab().history = nil
					run(m, sc.keys[:1]...)
					if sc.prep != nil {
						sc.prep(m)
					}
					run(m, sc.keys[1:]...)
					raw := screen(m)
					checkFits(t, name+" "+th.name, raw, size[0], size[1])
					got := strings.Join(trimRight(strings.Split(ansi.Strip(raw), "\n")), "\n") + "\n"
					if first == "" {
						first = got
					} else if got != first {
						t.Errorf("%s: %s differs from %s\n%s", name, th.name, goldenThemes[0].name, got)
					}
				}
				path := filepath.Join("testdata", "golden", name+".txt")
				if *update {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v (run with -update to create it)", err)
				}
				if string(want) != first {
					t.Errorf("%s differs from %s (run with -update after checking)\n--- got\n%s", name, path, first)
				}
			})
		}
	}
}

func trimRight(ls []string) []string {
	for i, l := range ls {
		ls[i] = strings.TrimRight(l, " ")
	}
	return ls
}
