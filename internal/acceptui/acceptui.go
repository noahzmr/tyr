// Package acceptui is the live view of `tyr accept` on a terminal: the
// finished steps printed above for good (✓ ▲ ✖ ○), and below them the
// running step with a spinner, a progress bar, elapsed and remaining time,
// the live values of the stress monitor, error counters that grew and the
// last log lines. The test runs in another goroutine and feeds the view
// with Program.Send; without a terminal `tyr accept` prints plain lines.
package acceptui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/stress"
)

// Messages the test sends to the view.
type (
	// StepStartMsg: a step begins; Duration is 0 for the health checks.
	StepStartMsg struct {
		Name     string
		Duration time.Duration
	}
	// ProgressMsg: how far the running step is (0..1, -1 unknown) and what
	// it is doing.
	ProgressMsg struct {
		Fraction float64
		Stage    string
	}
	// MetricMsg: one live value of the stress monitor.
	MetricMsg struct {
		Name, Unit string
		Value      float64
	}
	// SampleMsg: counters that grew since the step began, and the power draw.
	SampleMsg struct {
		Counters map[string]int64
		Watts    *float64
	}
	// StepDoneMsg: a step ended; Details are printed below its line.
	StepDoneMsg struct {
		Name, Verdict, Summary string
		Took                   time.Duration
		Details                []string
	}
	// LogMsg: one line of the plain output.
	LogMsg string
	// DoneMsg: the test is over, the view quits.
	DoneMsg struct{}
)

// Info describes the run for the header.
type Info struct {
	Version, Profile string
	Expected         time.Duration
	Steps            []string // planned steps, in order
}

type symbols struct {
	pass, warn, fail, skip, run string
	bar                         [2]rune
	spin                        spinner.Spinner
}

// The Linux console font has no check marks; ASCII there.
var (
	uniSyms   = symbols{pass: "✓", warn: "▲", fail: "✖", skip: "○", run: "▸", bar: [2]rune{'█', '░'}, spin: spinner.Dot}
	asciiSyms = symbols{pass: "+", warn: "!", fail: "x", skip: "-", run: ">", bar: [2]rune{'#', '-'}, spin: spinner.Line}
)

// ANSI colours: legible on dark and light backgrounds and on the Linux
// console's 16 colours.
var (
	cOK     = lipgloss.Color("2")
	cWarn   = lipgloss.Color("3")
	cCrit   = lipgloss.Color("1")
	cAccent = lipgloss.Color("6")
	cDim    = lipgloss.Color("8")
)

// Model is the view.
type Model struct {
	info     Info
	start    time.Time
	step     int // index of the running step in info.Steps (best effort)
	cur      string
	curDur   time.Duration
	curStart time.Time
	frac     float64
	stage    string
	metrics  map[string]MetricMsg
	counters map[string]int64
	watts    *float64
	logs     []string
	width    int
	sym      symbols
	spin     spinner.Model
	confirm  bool
	stopping bool
	cancel   func()
	done     bool
	now      func() time.Time
}

// New makes the view; cancel stops the test (after q and a confirmation).
func New(info Info, cancel func()) *Model {
	sym := uniSyms
	if t := os.Getenv("TERM"); t == "linux" || t == "vt100" || t == "dumb" {
		sym = asciiSyms
	}
	m := &Model{info: info, start: time.Now(), metrics: map[string]MetricMsg{}, counters: map[string]int64{}, width: 80,
		sym: sym, cancel: cancel, now: time.Now}
	m.spin = spinner.New(spinner.WithSpinner(sym.spin), spinner.WithStyle(lipgloss.NewStyle().Foreground(cAccent)))
	return m
}

func (m *Model) Init() tea.Cmd { return m.spin.Tick }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(40, msg.Width)
	case tea.KeyPressMsg:
		switch k := msg.String(); {
		case m.confirm && (k == "y" || k == "Y"):
			m.confirm, m.stopping = false, true
			m.logs = appendLog(m.logs, "stopping: the test ends INCOMPLETE")
			if m.cancel != nil {
				m.cancel()
			}
		case m.confirm:
			m.confirm = false
		case k == "q" || k == "ctrl+c" || k == "esc":
			if !m.stopping {
				m.confirm = true
			}
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case StepStartMsg:
		m.cur, m.curDur, m.curStart, m.frac, m.stage = msg.Name, msg.Duration, m.now(), 0, ""
		m.metrics, m.counters, m.watts = map[string]MetricMsg{}, map[string]int64{}, nil
		for i, s := range m.info.Steps {
			if s == msg.Name {
				m.step = i
			}
		}
	case ProgressMsg:
		if msg.Fraction >= 0 {
			m.frac = max(m.frac, min(1, msg.Fraction))
		}
		if msg.Stage != "" {
			m.stage = msg.Stage
		}
	case MetricMsg:
		m.metrics[msg.Name] = msg
	case SampleMsg:
		m.counters, m.watts = msg.Counters, msg.Watts
	case LogMsg:
		m.logs = appendLog(m.logs, string(msg))
	case StepDoneMsg:
		m.cur = ""
		return m, tea.Println(m.StepLine(msg))
	case DoneMsg:
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func appendLog(logs []string, s string) []string {
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		logs = append(logs, l)
	}
	if len(logs) > 6 {
		logs = logs[len(logs)-6:]
	}
	return logs
}

// StepLine is a finished step as printed above the live area.
func (m *Model) StepLine(s StepDoneMsg) string {
	mark, c := m.sym.pass, cOK
	switch s.Verdict {
	case "WARN", "REMARKS":
		mark, c = m.sym.warn, cWarn
	case "FAIL", "CRIT", "ABORTED", "NOT RUN":
		mark, c = m.sym.fail, cCrit
	case "SKIPPED":
		mark, c = m.sym.skip, cDim
	}
	st := lipgloss.NewStyle().Foreground(c)
	line := st.Bold(true).Render(mark) + " " + lipgloss.NewStyle().Bold(true).Render(pad(s.Name, 15)) + " " +
		st.Render(pad(s.Verdict, 8)) + " " + truncate(s.Summary, max(10, m.width-45))
	if s.Took > 0 {
		line += lipgloss.NewStyle().Foreground(cDim).Render("  " + clock(s.Took))
	}
	for i, d := range s.Details {
		if i == 8 {
			line += "\n" + lipgloss.NewStyle().Foreground(cDim).Render(fmt.Sprintf("    … %d more", len(s.Details)-i))
			break
		}
		if after, ok := strings.CutPrefix(d, "▲ "); ok {
			d = m.sym.warn + " " + after
		} else if after, ok := strings.CutPrefix(d, "✖ "); ok {
			d = m.sym.fail + " " + after
		}
		line += "\n" + lipgloss.NewStyle().Foreground(cDim).Render("    "+truncate(d, m.width-4))
	}
	return line
}

// View is the live area at the bottom.
func (m *Model) View() tea.View {
	if m.done {
		return tea.NewView("")
	}
	dim := lipgloss.NewStyle().Foreground(cDim)
	bold := lipgloss.NewStyle().Bold(true)
	var b strings.Builder
	el := m.now().Sub(m.start)
	head := fmt.Sprintf("Tyr %s · %s", m.info.Version, m.info.Profile)
	if n := len(m.info.Steps); n > 0 {
		head += fmt.Sprintf(" · step %d of %d", m.step+1, n)
	}
	head += " · " + clock(el)
	if m.info.Expected > 0 {
		head += " of ~" + clock(m.info.Expected)
	}
	b.WriteString(dim.Render(truncate(head, m.width)) + "\n")
	if m.cur != "" {
		spin := strings.TrimRight(m.spin.View(), " ")
		left := spin + " " + bold.Render(m.cur)
		if m.curDur > 0 {
			stepEl := m.now().Sub(m.curStart)
			rem := max(0, m.curDur-stepEl)
			if m.frac > 0 && m.frac < 1 && stepEl > 10*time.Second {
				rem = max(rem, time.Duration(float64(stepEl)/m.frac)-stepEl)
			}
			left = pad(left, 16)
			tail := fmt.Sprintf(" %3.0f%%  %s", m.frac*100, dim.Render(clock(stepEl)+" elapsed · "+clock(rem)+" left"))
			barW := max(8, min(40, m.width-lipgloss.Width(left)-lipgloss.Width(tail)-2))
			left = left + " " + m.barView(barW) + tail
		}
		b.WriteString(left + "\n")
		if m.stage != "" {
			b.WriteString("  " + dim.Render(truncate(m.stage, m.width-2)) + "\n")
		}
		if v := m.values(); v != "" {
			b.WriteString("  " + truncate(v, m.width-2) + "\n")
		}
		if c := m.counterLine(); c != "" {
			b.WriteString("  " + truncate(c, m.width-2) + "\n")
		}
	}
	if len(m.logs) > 0 {
		b.WriteString(dim.Render(strings.Repeat("─", min(m.width, 60))) + "\n")
		for _, l := range m.logs {
			b.WriteString(dim.Render(truncate(l, m.width)) + "\n")
		}
	}
	switch {
	case m.confirm:
		b.WriteString(lipgloss.NewStyle().Foreground(cWarn).Bold(true).Render("Stop the test? It ends INCOMPLETE. y stops, any other key goes on") + "\n")
	case m.stopping:
		b.WriteString(lipgloss.NewStyle().Foreground(cWarn).Render("stopping …") + "\n")
	default:
		b.WriteString(dim.Render("q stop the test") + "\n")
	}
	return tea.NewView(strings.TrimRight(b.String(), "\n"))
}

// barView draws the progress bar: filled cells in the accent colour.
func (m *Model) barView(w int) string {
	n := int(m.frac*float64(w) + 0.5)
	n = max(0, min(w, n))
	return lipgloss.NewStyle().Foreground(cAccent).Render(strings.Repeat(string(m.sym.bar[0]), n)) +
		lipgloss.NewStyle().Foreground(cDim).Render(strings.Repeat(string(m.sym.bar[1]), w-n))
}

// values: the monitor's live values, most telling first.
func (m *Model) values() string {
	var parts []string
	get := func(name string) (MetricMsg, bool) { v, ok := m.metrics[name]; return v, ok }
	if v, ok := get("cpu_temp"); ok {
		parts = append(parts, fmt.Sprintf("CPU %.0f°C", v.Value))
	}
	if v, ok := get("cpu_load"); ok {
		parts = append(parts, fmt.Sprintf("load %.0f%%", v.Value))
	}
	if v, ok := get("cpu_freq"); ok && v.Value > 0 {
		parts = append(parts, fmt.Sprintf("%.1f GHz", v.Value/1000))
	}
	if v, ok := get("mem_used"); ok {
		parts = append(parts, fmt.Sprintf("mem %.0f%%", v.Value))
	}
	if m.watts != nil && *m.watts > 0 {
		parts = append(parts, fmt.Sprintf("%.0f W", *m.watts))
	}
	// the test's own throughput: verified work, disk reads summed over disks
	var read float64
	names := make([]string, 0, len(m.metrics))
	for k := range m.metrics {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v := m.metrics[k]
		switch {
		case strings.HasPrefix(k, "read_bytes_per_s_"):
			read += v.Value
		case strings.HasPrefix(k, "verified_") || k == "fft_iterations_per_s":
			parts = append(parts, "verified "+rate(v))
		case k == "net_link_ratio":
			parts = append(parts, fmt.Sprintf("link %.0f%%", v.Value))
		}
	}
	if read > 0 {
		parts = append(parts, "read "+rate(MetricMsg{Unit: "bytes_per_second", Value: read}))
	}
	return strings.Join(parts, " · ")
}

func rate(v MetricMsg) string {
	switch v.Unit {
	case "bytes_per_second":
		switch {
		case v.Value >= 1e9:
			return fmt.Sprintf("%.1f GB/s", v.Value/1e9)
		default:
			return fmt.Sprintf("%.0f MB/s", v.Value/1e6)
		}
	case "ops_per_second":
		switch {
		case v.Value >= 1e6:
			return fmt.Sprintf("%.1fM/s", v.Value/1e6)
		case v.Value >= 1e3:
			return fmt.Sprintf("%.1fk/s", v.Value/1e3)
		}
		return fmt.Sprintf("%.0f/s", v.Value)
	}
	return fmt.Sprintf("%.0f", v.Value)
}

// counterLine: error counters that grew during the step, worst first.
func (m *Model) counterLine() string {
	type ent struct {
		k  string
		d  int64
		st model.Status
	}
	var es []ent
	for k, d := range m.counters {
		if d <= 0 {
			continue
		}
		if st := stress.CounterClass(k); st >= model.StatusWarn {
			es = append(es, ent{k, d, st})
		}
	}
	if len(es) == 0 {
		return ""
	}
	sort.Slice(es, func(i, j int) bool {
		if es[i].st != es[j].st {
			return es[i].st > es[j].st
		}
		return es[i].k < es[j].k
	})
	var parts []string
	for i, e := range es {
		if i == 4 {
			parts = append(parts, fmt.Sprintf("+%d more", len(es)-i))
			break
		}
		c := cWarn
		if e.st == model.StatusCrit {
			c = cCrit
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(c).Render(fmt.Sprintf("%s +%d", e.k, e.d)))
	}
	mark := m.sym.warn
	if es[0].st == model.StatusCrit {
		mark = m.sym.fail
	}
	return lipgloss.NewStyle().Foreground(cCrit).Render(mark) + " " + strings.Join(parts, "  ")
}

// clock: 42s, 12m05s, 3h04m.
func clock(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func pad(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func truncate(s string, n int) string {
	if n <= 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
