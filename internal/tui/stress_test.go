package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/stress"
)

// drive feeds the run's events into the model until it finishes; it calls
// mid once when the run's progress reaches at.
func drive(t *testing.T, m *Model, st *stressTab, at float64, mid func()) {
	t.Helper()
	called := false
	deadline := time.After(30 * time.Second)
	for st.run != nil {
		select {
		case e, ok := <-st.run.Events():
			m.Update(stressEventMsg{ev: e, ok: ok})
			if !ok {
				return
			}
			if st.run != nil && !called && mid != nil {
				if p, _ := st.run.Status(); p >= at {
					called = true
					mid()
				}
			}
		case <-deadline:
			t.Fatal("stress run did not finish")
		}
	}
}

func TestStressTabFlow(t *testing.T) {
	saved := durations[0]
	durations[0] = 4 * time.Second
	defer func() { durations[0] = saved }()

	m := newLoaded(t, "server-healthy", 140, 45)
	run(m, "9")
	st := m.stressTab()
	st.env.RunDir = t.TempDir()
	if s := text(m); !strings.Contains(s, "Stress test") || !strings.Contains(s, "fixtures mode") || !strings.Contains(s, "Start CPU test") {
		t.Fatalf("form\n%s", s)
	}
	// form: duration ← to the shortest, then start
	run(m, "down", "down", "left", "left", "left")
	st.duration = 0
	for st.fields()[st.field] != "start" {
		run(m, "down")
	}
	_, cmd := m.Update(press("enter"))
	if st.state != stPreparing || cmd == nil {
		t.Fatalf("start -> state %d", st.state)
	}
	m.Update(cmd())
	s := text(m)
	for _, want := range []string{"Confirm stress test", "CPU · builtin · 4 s", "simulated cpu test", "Type yes"} {
		if !strings.Contains(s, want) {
			t.Errorf("confirm missing %q\n%s", want, s)
		}
	}
	// a wrong word does not start it
	run(m, "n", "o")
	_, cmd = m.Update(press("enter"))
	if cmd != nil || st.state != stConfirm {
		t.Fatal("started without yes")
	}
	st.confirm.SetValue("")
	for _, k := range []string{"y", "e", "s"} {
		m.Update(press(k))
	}
	_, cmd = m.Update(press("enter"))
	if cmd == nil {
		t.Fatal("yes did not start")
	}
	m.Update(cmd()) // stressStartedMsg
	if st.state != stRunning || st.run == nil {
		t.Fatalf("not running: %d", st.state)
	}
	var live, liveRaw string
	drive(t, m, st, 0.7, func() { live, liveRaw = text(m), screen(m) })
	if dir := os.Getenv("TYR_SHOW_ANSI"); dir != "" {
		_ = os.WriteFile(dir+"/1-live.ansi", []byte(liveRaw), 0o644)
	}
	for _, want := range []string{"CPU stress · builtin", "PASSING", "elapsed", "progress", "CPU load %", "Hottest CPU °C", "Verified kernels/s", "Per-core load", "Wrong results", "Log"} {
		if !strings.Contains(live, want) {
			t.Errorf("live view missing %q\n%s", want, live)
		}
	}
	checkFits(t, "live", live, 140, 45)
	if dir := os.Getenv("TYR_SHOW_ANSI"); dir != "" {
		_ = os.WriteFile(dir+"/2-result.ansi", []byte(screen(m)), 0o644)
	}
	s = text(m)
	if st.state != stResult || !strings.Contains(s, "PASS") || !strings.Contains(s, "result file:") {
		t.Fatalf("result\n%s", s)
	}
	run(m, "enter")
	if s := text(m); !strings.Contains(s, "Recent runs") || !strings.Contains(s, "CPU · 4 s · builtin") {
		t.Errorf("history after run\n%s", s)
	}
	// ↓ past Start reaches the recent runs; enter opens the stored result
	for st.field < len(st.fields()) {
		run(m, "down")
	}
	run(m, "enter")
	if s := text(m); st.state != stResult || !strings.Contains(s, "CPU stress result") || !strings.Contains(s, "overall verdict") {
		t.Errorf("open recent run\n%s", s)
	}
	run(m, "esc")

	// esc aborts a running test
	st.duration = 2
	m.Update(st.prepare()())
	for _, k := range []string{"y", "e", "s"} {
		m.Update(press(k))
	}
	_, cmd = m.Update(press("enter"))
	m.Update(cmd())
	drive(t, m, st, 0, func() { m.Update(press("esc")) })
	if len(st.results) != 1 || st.results[0].Verdict != stress.VerdictAborted {
		t.Fatalf("abort: %+v", st.results)
	}
}

func TestStressFormFieldsAndBlockedPlans(t *testing.T) {
	m := newLoaded(t, "server-healthy", 120, 40)
	run(m, "9")
	st := m.stressTab()
	for c, want := range map[int]string{1: "Memory", 2: "Storage", 3: "Network", 4: "Full test"} {
		st.comp = c
		if s := text(m); !strings.Contains(s, want) {
			t.Errorf("component %d: missing %q\n%s", c, want, s)
		}
		checkFits(t, "form "+want, screen(m), 120, 40)
	}
	// network without a peer cannot run; the confirm screen says why
	st.comp, st.field = 3, 0
	m.Update(st.prepare()())
	if s := text(m); !strings.Contains(s, "cannot run") && !strings.Contains(s, "simulated network test") {
		t.Errorf("network confirm\n%s", s)
	}
	run(m, "esc")
	if st.state != stForm {
		t.Error("esc did not leave the confirmation")
	}
	// engines: external ones are listed as unavailable in fixtures mode
	st.comp, st.field = 0, 1
	run(m, "right")
	if s := text(m); !strings.Contains(s, "○ stress-ng") {
		t.Errorf("unavailable engine not shown\n%s", s)
	}
}
