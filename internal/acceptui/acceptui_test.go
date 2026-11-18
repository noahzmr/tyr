package acceptui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func plain(m *Model) string { return ansi.Strip(m.View().Content) }

func fits(t *testing.T, s string, w int) {
	t.Helper()
	for _, l := range strings.Split(s, "\n") {
		if n := ansi.StringWidth(l); n > w {
			t.Errorf("line %d wide: %q", n, l)
		}
	}
}

func TestView(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	stopped := false
	m := New(Info{Version: "1.4.0", Profile: "burnin", Expected: 4 * time.Hour,
		Steps: []string{"health check", "cpu", "memory", "storage", "final check"}}, func() { stopped = true })
	m.now = func() time.Time { return now }
	m.start = now
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.Update(StepStartMsg{Name: "health check"})
	if s := plain(m); !strings.Contains(s, "step 1 of 5") || !strings.Contains(s, "health check") || !strings.Contains(s, "q stop the test") {
		t.Errorf("health check:\n%s", s)
	}
	// a finished step is printed above for good
	_, cmd := m.Update(StepDoneMsg{Name: "health check", Verdict: "WARN", Summary: "2 findings", Took: 3 * time.Second,
		Details: []string{"▲ eno1 at 100M, port supports 1G", "▲ sdc: 8 pending sectors"}})
	if cmd == nil {
		t.Fatal("no print for a finished step")
	}
	line := ansi.Strip(m.StepLine(StepDoneMsg{Name: "health check", Verdict: "WARN", Summary: "2 findings", Took: 3 * time.Second,
		Details: []string{"▲ eno1 at 100M"}}))
	if !strings.HasPrefix(line, "▲ health check") || !strings.Contains(line, "WARN") || !strings.Contains(line, "3s") || !strings.Contains(line, "    ▲ eno1") {
		t.Errorf("step line %q", line)
	}

	m.Update(StepStartMsg{Name: "cpu", Duration: 90 * time.Minute})
	now = now.Add(30 * time.Minute)
	m.Update(ProgressMsg{Fraction: 1.0 / 3, Stage: "blend FFTs 1K-1M on 64 CPUs"})
	m.Update(MetricMsg{Name: "cpu_temp", Unit: "celsius", Value: 78})
	m.Update(MetricMsg{Name: "cpu_load", Unit: "percent", Value: 100})
	m.Update(MetricMsg{Name: "fft_iterations_per_s", Unit: "ops_per_second", Value: 1234})
	w := 412.0
	m.Update(SampleMsg{Counters: map[string]int64{"disk.sdc.crc": 2, "mce.cpu12.corrected": 1, "net.eno1.rx_bytes": 5}, Watts: &w})
	for i := range 9 {
		m.Update(LogMsg("log line " + string(rune('a'+i))))
	}
	s := plain(m)
	fits(t, s, 80)
	for _, want := range []string{"step 2 of 5", "cpu", " 33%", "30m00s elapsed", "1h00m left", "blend FFTs", "CPU 78°C", "load 100%",
		"412 W", "verified 1.2k/s", "mce.cpu12.corrected +1", "disk.sdc.crc +2", "log line i"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "rx_bytes") || strings.Contains(s, "log line c") {
		t.Errorf("traffic counter or old log line shown:\n%s", s)
	}

	// q asks, n goes on, q+y stops
	m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if !strings.Contains(plain(m), "Stop the test?") {
		t.Error("no confirmation")
	}
	m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if stopped || strings.Contains(plain(m), "Stop the test?") {
		t.Error("n stopped the test")
	}
	m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if !stopped || !strings.Contains(plain(m), "stopping") {
		t.Error("y did not stop")
	}
	_, cmd = m.Update(DoneMsg{})
	if cmd == nil || plain(m) != "" {
		t.Error("done does not quit and clear")
	}
}

func TestLinuxConsole(t *testing.T) {
	t.Setenv("TERM", "linux")
	m := New(Info{Version: "1", Profile: "quick"}, nil)
	m.Update(StepStartMsg{Name: "memory", Duration: time.Minute})
	m.Update(ProgressMsg{Fraction: 0.5})
	s := plain(m)
	for _, r := range s {
		if r > 0x7e && r != '·' && r != '─' && r != '°' {
			t.Errorf("glyph %q on the Linux console:\n%s", r, s)
			break
		}
	}
	l := ansi.Strip(m.StepLine(StepDoneMsg{Name: "health check", Verdict: "CRIT", Details: []string{"✖ cpu7 offline", "▲ eno1 at 100M"}}))
	if !strings.HasPrefix(l, "x health check") || !strings.Contains(l, "    x cpu7 offline") || !strings.Contains(l, "    ! eno1") {
		t.Errorf("ASCII marks: %q", l)
	}
}
