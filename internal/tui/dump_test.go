package tui

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// TestDumpTabs prints selected tabs: TYR_SHOW=fixture:tab,tab:WxH (tab "2v1"
// = tab 2 after pressing v once).
// With TYR_SHOW_ANSI=<dir> the raw (colored) screens are written there.
func TestDumpTabs(t *testing.T) {
	spec := os.Getenv("TYR_SHOW")
	if spec == "" {
		t.Skip("set TYR_SHOW=server-degraded:4,6,7:100x30")
	}
	var fx, tabs, size string
	parts := splitN(spec, ':')
	fx, tabs, size = parts[0], parts[1], parts[2]
	wh := splitN(size, 'x')
	w, _ := strconv.Atoi(wh[0])
	h, _ := strconv.Atoi(wh[1])
	m := newLoaded(t, fx, w, h)
	for _, tb := range splitN(tabs, ',') {
		// "2v2" = tab 2, then press v twice (view switch); "+key" suffixes
		// press more keys, e.g. "4+enter", "1+down+down+down".
		var extra []string
		if parts := splitN(tb, '+'); len(parts) > 1 {
			tb, extra = parts[0], parts[1:]
		}
		num, presses := tb, 0
		for i := 0; i < len(tb); i++ {
			if tb[i] == 'v' {
				num = tb[:i]
				presses, _ = strconv.Atoi(tb[i+1:])
				break
			}
		}
		i, _ := strconv.Atoi(num)
		m.active = (i + 9) % 10 // key "0" is the tenth tab
		for range presses {
			m.Update(press("v"))
		}
		run(m, extra...)
		if dir := os.Getenv("TYR_SHOW_ANSI"); dir != "" {
			if err := os.WriteFile(dir+"/"+fx+"-"+tb+".ansi", []byte(screen(m)), 0o644); err != nil {
				t.Fatal(err)
			}
			m = newLoaded(t, fx, w, h) // fresh view state for the next token
			continue
		}
		t.Logf("\n=== %s / %s (%dx%d)\n%s", fx, m.tabs[m.active].Title(), w, h, ansi.Strip(screen(m)))
	}
}

func splitN(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// TestDumpStress prints the stress confirm, live and result screens:
// TYR_STRESS=fixture:WxH[:component index].
func TestDumpStress(t *testing.T) {
	spec := os.Getenv("TYR_STRESS")
	if spec == "" {
		t.Skip("set TYR_STRESS=server-degraded:120x40")
	}
	parts := splitN(spec, ':')
	wh := splitN(parts[1], 'x')
	w, _ := strconv.Atoi(wh[0])
	h, _ := strconv.Atoi(wh[1])
	saved := durations[0]
	durations[0] = 6 * time.Second
	defer func() { durations[0] = saved }()
	m := newLoaded(t, parts[0], w, h)
	run(m, "9")
	st := m.stressTab()
	st.env.RunDir = t.TempDir()
	if len(parts) > 2 {
		st.comp, _ = strconv.Atoi(parts[2])
	}
	st.duration = 0
	m.Update(st.prepare()())
	t.Logf("\n=== confirm\n%s", ansi.Strip(screen(m)))
	st.confirm.SetValue("yes")
	_, cmd := m.Update(press("enter"))
	m.Update(cmd())
	drive(t, m, st, 0.8, func() { t.Logf("\n=== live\n%s", ansi.Strip(screen(m))) })
	for st.state == stRunning && st.run == nil { // full test: start the next component
		if cmd := st.startCurrent(); cmd != nil {
			m.Update(cmd())
		}
		drive(t, m, st, 2, nil)
	}
	t.Logf("\n=== result\n%s", ansi.Strip(screen(m)))
}
