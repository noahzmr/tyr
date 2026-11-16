package tui

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/stress"
)

// stressTab runs hardware stress tests: a form, a confirmation screen, a
// live view with graphs while the test runs, and the result. "Full test"
// runs CPU, memory, storage (and network when a peer is set) in sequence
// after a single confirmation.
type stressTab struct {
	env  *stress.Env
	ctx  context.Context
	snap *model.Snapshot
	w, h int
	keys keyMap

	state   int // form, preparing, confirm, running, result
	field   int
	editing bool
	input   lineInput

	comp     int // index into stressComps
	engine   map[stress.Component]int
	duration int
	cpus     string
	memPct   int
	stMode   int // 0 read, 1 file
	stTarget int
	peer     string
	token    string
	streams  int
	reverse  bool

	// confirm / run
	queue    []stress.Config
	plans    []*stress.Plan
	tests    []stress.Test
	confirm  lineInput
	prepErr  string
	run      *stress.Run
	runIdx   int
	results  []*stress.Result
	series   map[string][]float64
	units    map[string]string
	verify   []stress.VerifyError
	logs     []string
	guard    string
	cores    []float64
	coreSock []int
	coreOn   []bool
	thr0     uint64 // throttle count at the first sample
	thrSeen  bool
	critTemp *float64
	history  []stress.Result
	scroll   int // result view
	inst     stress.InstallPlan
}

const (
	stForm = iota
	stPreparing
	stConfirm
	stRunning
	stResult
	stInstall // install confirmation for missing engines
)

var stressComps = []string{"CPU", "Memory", "Storage", "Network", "Full test"}
var durations = []time.Duration{30 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 4 * time.Hour, 8 * time.Hour, 24 * time.Hour}

func newStressTab(ctx context.Context, env *stress.Env) *stressTab {
	return &stressTab{env: env, ctx: ctx, keys: newKeyMap(), engine: map[stress.Component]int{}, duration: 2, memPct: 70, streams: 4,
		input: lineInput{limit: 128}, confirm: lineInput{placeholder: "type yes", limit: 64}}
}

type (
	stressPlanMsg struct {
		plans []*stress.Plan
		tests []stress.Test
		cfgs  []stress.Config
		err   string
	}
	stressStartedMsg struct {
		run *stress.Run
		err string
	}
	stressEventMsg struct {
		ev stress.Event
		ok bool
	}
	installDoneMsg struct {
		plan stress.InstallPlan
		err  error
	}
)

var keyInstall = key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "install engines"))

func (s *stressTab) Title() string          { return "Stress" }
func (s *stressTab) Module() model.ModuleID { return "" }
func (s *stressTab) SetSize(w, h int)       { s.w, s.h = w, h }
func (s *stressTab) SetData(x *model.Snapshot) {
	s.snap = x
	if s.history == nil {
		s.history = stress.History(s.env, 8)
	}
}

// Capturing: text fields and the confirmation input take all keys; a
// running test takes esc (abort).
func (s *stressTab) Capturing() bool {
	return s.editing || s.state == stConfirm || s.state == stInstall
}

// Running reports whether a test is in progress.
func (s *stressTab) Running() bool { return s.state == stRunning || s.state == stPreparing }

func (s *stressTab) Help() []key.Binding {
	switch s.state {
	case stConfirm:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp(s.confirmWord()+"+enter", "start")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))}
	case stRunning, stPreparing:
		return []key.Binding{key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "abort cleanly")),
			key.NewBinding(key.WithKeys("1"), key.WithHelp("1–0", "other tabs stay live"))}
	case stInstall:
		if s.inst.Blocked != "" {
			return []key.Binding{key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))}
		}
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "install")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))}
	case stResult:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter esc", "back to form")),
			key.NewBinding(key.WithKeys("up"), key.WithHelp("↑↓", "scroll")), key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "export"))}
	}
	if s.field >= len(s.fields()) {
		return []key.Binding{key.NewBinding(key.WithKeys("up"), key.WithHelp("↑↓", "field / run")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open result"))}
	}
	b := []key.Binding{key.NewBinding(key.WithKeys("up"), key.WithHelp("↑↓", "field")), key.NewBinding(key.WithKeys("left", "right"), key.WithHelp("←→", "change")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "edit / start"))}
	if len(s.missing()) > 0 {
		b = append(b, keyInstall)
	}
	return b
}

func (s *stressTab) component() stress.Component {
	if s.comp >= len(stress.Components) {
		return ""
	}
	return stress.Components[s.comp]
}

// fields lists the form rows for the current component.
func (s *stressTab) fields() []string {
	f := []string{"test", "engine", "duration"}
	switch s.component() {
	case stress.CompCPU:
		f = append(f, "cpus")
	case stress.CompMemory:
		f = append(f, "mempct")
	case stress.CompStorage:
		f = append(f, "stmode", "sttarget")
	case stress.CompNetwork:
		f = append(f, "peer", "token", "streams", "direction")
	default:
		f = []string{"test", "duration", "peer", "token"}
	}
	return append(f, "start")
}

func (s *stressTab) engines() []stress.EngineInfo {
	c := s.component()
	if c == "" {
		return nil
	}
	return stress.Engines(c, s.env)
}

func (s *stressTab) targets() []string {
	disks, mounts := stress.StorageTargets(s.storageEnv())
	if s.stMode == 0 {
		return append([]string{"all"}, disks...)
	}
	return mounts
}

// storageEnv gives StorageTargets a baseline before any test ran.
func (s *stressTab) storageEnv() *stress.Env {
	if s.env.Baseline == nil && s.snap != nil {
		e := *s.env
		e.Baseline = s.snap
		return &e
	}
	return s.env
}

func (s *stressTab) config(c stress.Component) stress.Config {
	cfg := stress.Config{Component: c, Engine: stress.EngBuiltin, Duration: durations[s.duration], CPUs: s.cpus,
		MemoryPercent: s.memPct, Target: s.peer, Token: s.token, Streams: s.streams, Reverse: s.reverse}
	if s.component() != "" {
		if es := s.engines(); s.engine[c] < len(es) {
			cfg.Engine = es[s.engine[c]].Name
		}
	}
	if c == stress.CompStorage {
		cfg.StorageMode = []string{"read", "file"}[s.stMode]
		cfg.Target = ""
		if ts := s.targets(); s.stTarget < len(ts) {
			cfg.Target = ts[s.stTarget]
		}
		if s.component() == "" { // full test: read-scan every disk
			cfg.StorageMode, cfg.Target = "read", "all"
		}
	}
	return cfg
}

func (s *stressTab) Update(msg tea.KeyPressMsg) tea.Cmd {
	switch s.state {
	case stInstall:
		switch msg.String() {
		case "esc":
			s.state = stForm
		case "enter":
			if s.inst.Blocked == "" {
				s.state = stForm
				plan := s.inst
				c := exec.Command(plan.Argv[0], plan.Argv[1:]...)
				return tea.ExecProcess(c, func(err error) tea.Msg { return installDoneMsg{plan: plan, err: err} })
			}
		}
		return nil
	case stConfirm:
		switch msg.String() {
		case "esc":
			s.state = stForm
			s.confirm.SetValue("")
		case "enter":
			if strings.TrimSpace(strings.ToLower(s.confirm.Value())) == s.confirmWord() && s.startable() {
				s.confirm.SetValue("")
				s.results, s.runIdx = nil, 0
				return s.startCurrent()
			}
		default:
			s.confirm.Update(msg)
		}
		return nil
	case stRunning:
		if msg.String() == "esc" && s.run != nil {
			s.run.Abort("aborted from the TUI (esc)")
			s.queue = s.queue[:s.runIdx+1] // do not start the remaining components
		}
		return nil
	case stPreparing:
		return nil
	case stResult:
		switch msg.String() {
		case "enter", "esc":
			s.state, s.scroll = stForm, 0
			s.history = stress.History(s.env, 8)
		case "up", "k":
			s.scroll = max(0, s.scroll-1)
		case "down", "j":
			s.scroll++
		case "pgup":
			s.scroll = max(0, s.scroll-s.h/2)
		case "pgdown", "space", " ":
			s.scroll += s.h / 2
		case "g", "home":
			s.scroll = 0
		}
		return nil
	}
	fields := s.fields()
	total := len(fields) + len(s.history)
	s.field = min(s.field, total-1)
	if s.field >= len(fields) { // a recent run
		switch {
		case key.Matches(msg, s.keys.Up):
			s.field--
		case key.Matches(msg, s.keys.Down):
			s.field = min(total-1, s.field+1)
		case key.Matches(msg, s.keys.Enter):
			h := s.history[s.field-len(fields)]
			s.results, s.state, s.scroll = []*stress.Result{&h}, stResult, 0
		}
		return nil
	}
	f := fields[s.field]
	if s.editing {
		switch msg.String() {
		case "enter", "esc":
			if msg.String() == "enter" {
				s.setText(f, s.input.Value())
			}
			s.editing = false
			s.input.Blur()
		default:
			s.input.Update(msg)
		}
		return nil
	}
	switch {
	case key.Matches(msg, keyInstall):
		miss := s.missing()
		if len(miss) == 0 {
			return func() tea.Msg { return statusMsg("all engines for this test are installed") }
		}
		s.inst = stress.PlanInstall(s.env, miss)
		s.state = stInstall
	case key.Matches(msg, s.keys.Up):
		s.field = max(0, s.field-1)
	case key.Matches(msg, s.keys.Down):
		s.field = min(total-1, s.field+1)
	case key.Matches(msg, s.keys.Left):
		s.change(f, -1)
	case key.Matches(msg, s.keys.Right):
		s.change(f, +1)
	case key.Matches(msg, s.keys.Enter):
		switch f {
		case "cpus", "peer", "token":
			s.editing = true
			s.input.SetValue(s.text(f))
			s.input.Focus()
		case "start":
			return s.prepare()
		default:
			s.change(f, +1)
		}
	}
	return nil
}

func (s *stressTab) text(f string) string {
	return map[string]string{"cpus": s.cpus, "peer": s.peer, "token": s.token}[f]
}

func (s *stressTab) setText(f, v string) {
	v = strings.TrimSpace(v)
	switch f {
	case "cpus":
		s.cpus = v
	case "peer":
		s.peer = v
	case "token":
		s.token = v
	}
}

func cycle(i, d, n int) int {
	if n == 0 {
		return 0
	}
	return ((i+d)%n + n) % n
}

func (s *stressTab) change(f string, d int) {
	switch f {
	case "test":
		s.comp = cycle(s.comp, d, len(stressComps))
	case "engine": // only available engines; unavailable ones are listed below
		c := s.component()
		es := s.engines()
		for range es {
			s.engine[c] = cycle(s.engine[c], d, len(es))
			if es[s.engine[c]].Available {
				break
			}
		}
	case "duration":
		s.duration = cycle(s.duration, d, len(durations))
	case "mempct":
		s.memPct = min(100, max(10, s.memPct+10*d))
	case "stmode":
		s.stMode, s.stTarget = cycle(s.stMode, d, 2), 0
	case "sttarget":
		s.stTarget = cycle(s.stTarget, d, len(s.targets()))
	case "streams":
		opts := []int{1, 2, 4, 8, 16}
		i := 0
		for j, o := range opts {
			if o == s.streams {
				i = j
			}
		}
		s.streams = opts[cycle(i, d, len(opts))]
	case "direction":
		s.reverse = !s.reverse
	}
}

// prepare collects baselines and plans for the selected test(s).
func (s *stressTab) prepare() tea.Cmd {
	var cfgs []stress.Config
	if c := s.component(); c != "" {
		cfgs = []stress.Config{s.config(c)}
	} else {
		// network without a peer stays in the plan: the confirmation shows
		// why it cannot run and the result lists it as SKIPPED
		for _, c := range stress.Components {
			cfgs = append(cfgs, s.config(c))
		}
	}
	s.state, s.prepErr = stPreparing, ""
	env, ctx := s.env, s.ctx
	return func() tea.Msg {
		var m stressPlanMsg
		for _, cfg := range cfgs {
			env.Baseline = nil
			p, t, err := stress.Prepare(ctx, env, cfg)
			if err != nil {
				m.err = err.Error()
				return m
			}
			m.plans, m.tests, m.cfgs = append(m.plans, p), append(m.tests, t), append(m.cfgs, cfg)
		}
		return m
	}
}

func (s *stressTab) startable() bool {
	for _, p := range s.plans {
		if p.Blocked == "" {
			return true
		}
	}
	return false
}

// startCurrent starts queue[runIdx], skipping blocked components.
func (s *stressTab) startCurrent() tea.Cmd {
	for s.runIdx < len(s.queue) && s.plans[s.runIdx].Blocked != "" {
		s.results = append(s.results, &stress.Result{Component: s.queue[s.runIdx].Component, Engine: s.queue[s.runIdx].Engine,
			Verdict: stress.VerdictAborted, Summary: "not run: " + s.plans[s.runIdx].Blocked, Reasons: []string{s.plans[s.runIdx].Blocked}})
		s.runIdx++
	}
	if s.runIdx >= len(s.queue) {
		s.state = stResult
		return nil
	}
	s.state = stRunning
	s.series, s.units, s.verify, s.logs, s.guard, s.cores = map[string][]float64{}, map[string]string{}, nil, nil, "", nil
	s.thrSeen = false
	cfg, plan, t := s.queue[s.runIdx], s.plans[s.runIdx], s.tests[s.runIdx]
	env, ctx := s.env, s.ctx
	first := s.runIdx == 0
	return func() tea.Msg {
		if !first { // fresh baseline for every component after the first
			env.Baseline = nil
			p, t2, err := stress.Prepare(ctx, env, cfg)
			if err != nil {
				return stressStartedMsg{err: err.Error()}
			}
			plan, t = p, t2
		}
		r, err := stress.Start(ctx, env, cfg, plan, t)
		if err != nil {
			return stressStartedMsg{err: err.Error()}
		}
		return stressStartedMsg{run: r}
	}
}

func waitEvent(r *stress.Run) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-r.Events()
		return stressEventMsg{ev: e, ok: ok}
	}
}

// missing lists the unavailable engines of the selected test (all
// components for the full test).
func (s *stressTab) missing() []string {
	comps := stress.Components
	if c := s.component(); c != "" {
		comps = []stress.Component{c}
	}
	return stress.MissingEngines(s.env, comps...)
}

// installResult re-detects the engines after the package manager ran.
func (s *stressTab) installResult(m installDoneMsg) string {
	var now, still []string
	for _, p := range m.plan.Packages {
		if _, err := s.env.Collector.Exec.LookPath(p); err == nil {
			now = append(now, p)
		} else {
			still = append(still, p)
		}
	}
	switch {
	case len(still) == 0:
		return "installed " + strings.Join(now, ", ") + " · engines re-detected"
	case m.err != nil && m.plan.Manager == "apt-get":
		return "install failed (" + m.err.Error() + "): " + strings.Join(still, ", ") + " still missing · apt-get update may help"
	case m.err != nil:
		return "install failed (" + m.err.Error() + "): " + strings.Join(still, ", ") + " still missing"
	}
	return strings.Join(still, ", ") + " still not found after the install"
}

// HandleMsg receives the tab's own messages (routed by the root model).
func (s *stressTab) HandleMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case installDoneMsg:
		text := s.installResult(msg)
		return func() tea.Msg { return statusMsg(text) }, true
	case stressPlanMsg:
		if msg.err != "" {
			s.state, s.prepErr = stForm, msg.err
			return nil, true
		}
		s.plans, s.tests, s.queue = msg.plans, msg.tests, msg.cfgs
		s.state = stConfirm
		s.confirm.Focus()
		return nil, true
	case stressStartedMsg:
		if msg.err != "" {
			s.results = append(s.results, &stress.Result{Component: s.queue[s.runIdx].Component, Verdict: stress.VerdictAborted,
				Summary: "could not start: " + msg.err, Reasons: []string{msg.err}})
			s.runIdx++
			return s.startCurrent(), true
		}
		s.run = msg.run
		return waitEvent(s.run), true
	case stressEventMsg:
		if !msg.ok {
			return nil, true
		}
		var cmd tea.Cmd
		s.apply(msg.ev)
		if msg.ev.Kind == stress.EvFinished {
			s.results = append(s.results, msg.ev.Result)
			s.run = nil
			s.runIdx++
			return s.startCurrent(), true
		}
		if msg.ev.Kind == stress.EvSample && msg.ev.Sample != nil {
			snap := msg.ev.Sample.Snap
			cmd = func() tea.Msg { return snapshotMsg{snap: snap} }
		}
		return tea.Batch(cmd, waitEvent(s.run)), true
	}
	return nil, false
}

const maxSeries = 600

func (s *stressTab) apply(e stress.Event) {
	switch e.Kind {
	case stress.EvMetric:
		v := append(s.series[e.Name], e.Value)
		if len(v) > maxSeries {
			v = v[len(v)-maxSeries:]
		}
		s.series[e.Name], s.units[e.Name] = v, e.Unit
	case stress.EvVerify:
		s.verify = append(s.verify, *e.Verify)
	case stress.EvGuard:
		s.guard = e.Text
	case stress.EvLog:
		s.logs = append(s.logs, e.Time.Format("15:04:05 ")+e.Text)
		if len(s.logs) > 200 {
			s.logs = s.logs[len(s.logs)-200:]
		}
	case stress.EvSample:
		if c := e.Sample.Snap.CPU; c != nil {
			s.cores, s.coreSock, s.coreOn = s.cores[:0], s.coreSock[:0], s.coreOn[:0]
			var thr uint64
			haveThr := false
			for _, l := range c.Logical {
				v := 0.0
				if l.UtilizationPercent != nil {
					v = *l.UtilizationPercent
				}
				s.cores, s.coreSock, s.coreOn = append(s.cores, v), append(s.coreSock, l.SocketID), append(s.coreOn, l.Online)
				if l.ThrottleCount != nil {
					thr += *l.ThrottleCount
					haveThr = true
				}
			}
			if haveThr {
				if !s.thrSeen {
					s.thr0, s.thrSeen = thr, true
				}
				v := append(s.series["throttle"], float64(thr-min(thr, s.thr0)))
				if len(v) > maxSeries {
					v = v[len(v)-maxSeries:]
				}
				s.series["throttle"], s.units["throttle"] = v, "count"
			}
			s.critTemp = nil
			for _, p := range c.PackageTemps {
				if p.Thresholds != nil && p.Thresholds.UpperCrit != nil {
					s.critTemp = p.Thresholds.UpperCrit
				}
			}
		}
	}
}

// confirmWord is what the user types to start: a plan's token (the CPU
// name for the risky offline-core test), else "yes".
func (s *stressTab) confirmWord() string {
	for _, p := range s.plans {
		if p != nil && p.ConfirmToken != "" {
			return strings.ToLower(p.ConfirmToken)
		}
	}
	return "yes"
}
