package tui

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/execx"
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

func TestStressFromModuleTabs(t *testing.T) {
	m := newLoaded(t, "server-degraded", 120, 40)
	st := m.stressTab()
	confirm := func(label string, cmd tea.Cmd) {
		t.Helper()
		if cmd == nil {
			t.Fatalf("%s: no prepare command (%q)", label, m.message)
		}
		m.Update(cmd())
		if m.tabs[m.active] != tab(st) || st.state != stConfirm || !strings.Contains(text(m), "Confirm stress test") {
			t.Fatalf("%s: not on the confirmation\n%s", label, text(m))
		}
		run(m, "esc")
	}
	// CPU: the selected CPU and its SMT sibling
	focus(t, m, "2", "cpu12")
	_, cmd := m.Update(press("S"))
	confirm("cpu", cmd)
	if st.comp != 0 || st.cpus != "12,44" {
		t.Errorf("cpu request: comp %d cpus %q", st.comp, st.cpus)
	}
	// an offline CPU: the guarded offline-core test, confirmed by its name
	focus(t, m, "2", "cpu7")
	_, cmd = m.Update(press("S"))
	if cmd == nil {
		t.Fatalf("offline cpu: no prepare (%q)", m.message)
	}
	m.Update(cmd())
	if st.state != stConfirm || !strings.Contains(text(m), "Type cpu7 and press enter") {
		t.Fatalf("offline cpu: not asking for the CPU name\n%s", text(m))
	}
	if cfg := st.config(stress.CompCPU); cfg.Engine != stress.EngOfflineCore || cfg.CPUs != "7" || st.plans[0].ConfirmToken != "cpu7" {
		t.Errorf("offline cpu: engine %q cpus %q token %q", cfg.Engine, cfg.CPUs, st.plans[0].ConfirmToken)
	}
	if w := strings.Join(st.plans[0].Warnings, " "); !strings.Contains(w, "RISK") || !strings.Contains(w, "already crashed the kernel") {
		t.Errorf("offline-core plan lacks the risk: %q", w)
	}
	// "yes" does not start it; the CPU name does (simulated in fixtures)
	run(m, "y", "e", "s", "enter")
	if st.state != stConfirm {
		t.Fatalf("yes started the offline-core test")
	}
	run(m, "esc")
	// its other thread: says why instead of loading a suspect core
	focus(t, m, "2", "cpu39")
	if _, cmd = m.Update(press("S")); cmd != nil || !strings.Contains(m.message, "offline cpu7") {
		t.Errorf("suspect cpu39: %q", m.message)
	}
	// back on an online CPU the engine is the built-in one again
	focus(t, m, "2", "cpu12")
	_, cmd = m.Update(press("S"))
	confirm("cpu again", cmd)
	if cfg := st.config(stress.CompCPU); cfg.Engine != stress.EngBuiltin {
		t.Errorf("engine stayed %q", cfg.Engine)
	}
	// Storage: a read scan of the selected disk
	focus(t, m, "4", "sdd")
	_, cmd = m.Update(press("S"))
	confirm("disk", cmd)
	if st.comp != 2 || st.stMode != 0 || st.targets()[st.stTarget] != "/dev/sdd" {
		t.Errorf("disk request: comp %d mode %d target %q", st.comp, st.stMode, st.targets()[st.stTarget])
	}
	// GPU: no engine, says why and stays
	run(m, "7")
	if _, cmd = m.Update(press("S")); cmd != nil || !strings.Contains(m.message, "no GPU/NPU stress engine") || m.tabs[m.active].Title() != "GPU/NPU" {
		t.Errorf("gpu: %q", m.message)
	}
	// F from anywhere: the default full test (CPU list reset)
	run(m, "0")
	_, cmd = m.Update(press("F"))
	confirm("full", cmd)
	if st.comp != 4 || st.cpus != "" || len(st.plans) != 4 {
		t.Errorf("full test: comp %d cpus %q plans %d", st.comp, st.cpus, len(st.plans))
	}
	// Overview: the focused card (CPU)
	run(m, "1")
	_, cmd = m.Update(press("S"))
	confirm("overview", cmd)
	if st.comp != 0 {
		t.Errorf("overview card: comp %d", st.comp)
	}
}

func TestStressInstallEngines(t *testing.T) {
	m := newLoaded(t, "server-degraded", 120, 40)
	run(m, "9")
	st := m.stressTab()
	// fixtures mode: nothing is installed
	run(m, "i")
	if s := text(m); st.state != stInstall || !strings.Contains(s, "Install engines") || !strings.Contains(s, "fixtures mode: nothing is installed") {
		t.Fatalf("fixtures install\n%s", s)
	}
	if _, cmd := m.Update(press("enter")); cmd != nil {
		t.Fatal("blocked install ran something")
	}
	run(m, "esc")
	// a host with apt-get and sudo, not root, stress-ng missing
	rp := &execx.Replay{Tools: map[string]string{"apt-get": "/usr/bin/apt-get", "sudo": "/usr/bin/sudo"}}
	st.env = &stress.Env{Collector: &collector.Env{Exec: rp}}
	run(m, "i")
	s := text(m)
	for _, want := range []string{"Packages  stress-ng", "Command   sudo apt-get install stress-ng", "mprime has no distribution package", "sudo, which may ask for your password"} {
		if !strings.Contains(s, want) {
			t.Errorf("install overlay missing %q\n%s", want, s)
		}
	}
	if _, cmd := m.Update(press("enter")); cmd == nil || st.state != stForm {
		t.Fatalf("enter did not hand over to the package manager (state %d)", st.state)
	}
	plan := stress.PlanInstall(st.env, []string{"stress-ng"})
	rp.Tools["stress-ng"] = "/usr/bin/stress-ng"
	_, cmd := m.Update(installDoneMsg{plan: plan})
	m.Update(cmd())
	if !strings.Contains(m.message, "installed stress-ng") {
		t.Errorf("after install: %q", m.message)
	}
	delete(rp.Tools, "stress-ng")
	_, cmd = m.Update(installDoneMsg{plan: plan, err: errors.New("exit status 100")})
	m.Update(cmd())
	if !strings.Contains(m.message, "install failed (exit status 100)") || !strings.Contains(m.message, "apt-get update may help") {
		t.Errorf("failed install: %q", m.message)
	}
}
