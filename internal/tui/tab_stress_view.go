package tui

import (
	"fmt"
	"image/color"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/stress"
)

// Views of the stress tab (canvas Stress-Form, Stress-Confirm,
// Stress-Live, Stress-Result).

// OwnsStrip: the live and result views draw their own title, verdict
// banner and progress where the status strip normally sits.
func (s *stressTab) OwnsStrip() bool {
	return s.state == stRunning || s.state == stResult || s.state == stPreparing
}

// bodyH is the height the view may use (two more rows when it owns the
// strip row and the blank row below it).
func (s *stressTab) bodyH() int {
	if s.OwnsStrip() {
		return s.h + 2
	}
	return s.h
}

// Strip (form): the machine's state and what it means for a run.
func (s *stressTab) Strip(t *Theme, w int) string {
	if s.snap == nil {
		return ""
	}
	crit, warn := 0, 0
	for _, f := range s.snap.Findings() {
		switch f.Status {
		case model.StatusCrit:
			crit++
		case model.StatusWarn:
			warn++
		}
	}
	var rest []string
	switch {
	case crit > 0:
		rest = append(rest, fmt.Sprintf("machine has %d critical %s, results may be affected", crit, plural(crit, "finding", "findings")))
	case warn > 0:
		rest = append(rest, fmt.Sprintf("machine has %d %s", warn, plural(warn, "warning", "warnings")))
	default:
		rest = append(rest, "machine healthy")
	}
	if s.env.Simulated() {
		rest = append(rest, "fixtures mode: tests are simulated")
	}
	return statusStrip(t, s.snap.Status, "Stress test", strings.Join(rest, " · "), "", w)
}

// windowTitle overrides the terminal title while a run fails.
func (s *stressTab) windowTitle() string {
	if s.state != stRunning || s.run == nil || (len(s.verify) == 0 && s.guard == "") {
		return ""
	}
	return fmt.Sprintf("FAIL %s stress – Tyr", s.run.Cfg.Component)
}

func (s *stressTab) View(t *Theme) string {
	switch s.state {
	case stPreparing:
		return t.Bold.Render("Stress test") + "\n\n  " + t.Message.Render("collecting the baseline snapshot and checking the plan …")
	case stConfirm:
		return s.confirmView(t)
	case stInstall:
		return s.installView(t)
	case stRunning:
		return s.liveView(t)
	case stResult:
		return s.resultView(t)
	}
	return s.formView(t)
}

// --- form ---------------------------------------------------------------------

const stressLabelW = 12

// engineFix is the install hint of an external engine.
var engineFix = map[string]string{"stress-ng": "apt install stress-ng", "memtester": "apt install memtester",
	"iperf3": "apt install iperf3", "mprime": "download from mersenne.org"}

// engineReason shortens "X is not installed" to "not installed" with the
// install hint; other reasons (fixtures mode) have no hint.
func engineReason(e stress.EngineInfo) (string, string) {
	if strings.HasSuffix(e.Reason, " is not installed") {
		if e.Name == stress.EngMprime {
			return "not installed", engineFix[e.Name]
		}
		return "not installed", "i installs " + engineFix[e.Name][len("apt install "):]
	}
	return e.Reason, ""
}

// installView: the install confirmation over the faint form — packages,
// the exact command, what happens to the terminal, or why nothing can be
// installed.
func (s *stressTab) installView(t *Theme) string {
	p := s.inst
	var lines []string
	if len(p.Packages) > 0 {
		lines = append(lines, t.Dim.Render(pad("Packages", 10))+t.Bold.Render(strings.Join(p.Packages, " ")))
	}
	if len(p.Argv) > 0 {
		lines = append(lines, t.Dim.Render(pad("Command", 10))+t.Bold.Render(p.Command()))
	}
	for _, x := range p.Skipped {
		lines = append(lines, t.Sub.Render("· "+x))
	}
	lines = append(lines, "")
	kind := t.Accent
	if p.Blocked != "" {
		kind = t.Warn
		lines = append(lines, t.StatusText(model.StatusWarn, "▲ "+p.Blocked), "", t.Sub.Render("esc back"))
	} else {
		who := "the package manager"
		if p.Sudo {
			who += " through sudo, which may ask for your password"
		}
		lines = append(lines, t.Dim.Render("Tyr hands the terminal to "+who+". "+p.Manager+" shows what it installs and may ask to confirm. "+
			"The TUI comes back when it finishes and detects the engines again."), "",
			button(t, "Install", true)+"   "+t.Sub.Render("enter install · esc cancel"))
	}
	return overlay(t, s.formView(t), kind, "Install engines", strings.Join(lines, "\n"), s.w, s.h)
}

// compTitle names a component as the form shows it.
func compTitle(c stress.Component) string {
	for i, x := range stress.Components {
		if x == c && i < len(stressComps) {
			return stressComps[i]
		}
	}
	return string(c)
}

func (s *stressTab) formView(t *Theme) string {
	lw := s.w
	if s.w >= 100 {
		lw = s.w - 50
	}
	left := s.formLeft(t, lw)
	right := s.formRight(t, min(48, s.w))
	if s.w < 100 {
		return left + "\n\n" + right
	}
	return besideInspector(left, lw+2, right, max(lipgloss.Height(left), lipgloss.Height(right)))
}

func (s *stressTab) formLeft(t *Theme, w int) string {
	lines := []string{t.Heading("New run", "", w), ""}
	fields := s.fields()
	hintCol := stressLabelW + 34
	row := func(i int, label, value, hint string) {
		l := fieldLabel(t, label, i == s.field, stressLabelW) + value
		if hint != "" {
			l = padCell(l, max(lipgloss.Width(l)+2, hintCol), false) + t.Sub.Render(hint)
		}
		lines = append(lines, truncate(l, w))
	}
	indent := strings.Repeat(" ", stressLabelW)
	for i, f := range fields {
		focused := i == s.field
		switch f {
		case "test":
			var ps []string
			for j, name := range stressComps {
				ps = append(ps, pill(t, name, j == s.comp))
			}
			row(i, "Test", strings.Join(ps, ""), "")
			if s.comp == 4 {
				lines = append(lines, indent+t.Sub.Render(" CPU → Memory → Storage → Network (with a peer), one confirmation"))
			}
			lines = append(lines, "")
		case "engine":
			es := s.engines()
			cur := min(s.engine[s.component()], len(es)-1)
			var ps []string
			var off []string
			for j, e := range es {
				if e.Available {
					ps = append(ps, pill(t, e.Name, j == cur))
				} else {
					reason, fix := engineReason(e)
					off = append(off, indent+disabledOption(t, e.Name, reason, fix))
				}
			}
			if len(ps) == 0 {
				ps = append(ps, t.Sub.Render("none available"))
			}
			row(i, "Engine", strings.Join(ps, ""), "")
			if cur >= 0 && cur < len(es) && es[cur].Available {
				lines = append(lines, truncate(indent+" "+t.Sub.Render(es[cur].Desc), w))
			}
			for _, o := range off {
				lines = append(lines, truncate(o, w))
			}
			if other := s.otherEngines(es); other != "" {
				lines = append(lines, truncate(indent+t.Sub.Render("· "+other+" not for "+stressComps[s.comp]+" tests"), w))
			}
			lines = append(lines, "")
		case "duration":
			row(i, "Duration", stepper(t, fmtDuration(durations[s.duration]), focused), "30 s … 24 h · per component")
		case "cpus":
			v := s.cpus
			if v == "" && !(s.editing && focused) {
				v = s.allCPUsText()
			}
			row(i, "CPUs", s.textField(t, f, v, 24), "enter to edit · e.g. 0-7,16")
		case "mempct":
			row(i, "Memory", stepper(t, fmt.Sprintf("%d %% of available", s.memPct), focused), "locked in RAM · max(1 GiB, 5 %) stays free")
		case "stmode":
			row(i, "Mode", pill(t, "read-only scan", s.stMode == 0)+pill(t, "file write + verify", s.stMode == 1),
				[]string{"never writes", "temporary file, removed after"}[s.stMode])
		case "sttarget":
			ts := s.targets()
			v := "–"
			if s.stTarget < len(ts) {
				v = ts[s.stTarget]
			}
			row(i, "Target", stepper(t, v, focused), []string{"a disk, or all in parallel", "a writable filesystem"}[s.stMode])
		case "peer":
			row(i, "Peer", s.textField(t, f, s.peer, 24), "host[:port] of tyr net-peer")
		case "token":
			row(i, "Token", s.textField(t, f, strings.Repeat("•", len([]rune(s.token))), 24), "printed by tyr net-peer")
		case "streams":
			row(i, "Streams", stepper(t, strconv.Itoa(s.streams), focused), "parallel TCP streams")
		case "direction":
			row(i, "Direction", pill(t, "this host sends", !s.reverse)+pill(t, "peer sends", s.reverse), "")
		case "start":
			lines = append(lines, " "+t.Dim.Render(pad("Guard", stressLabelW-1))+padCell(s.guardText(), hintCol-stressLabelW, false)+t.Sub.Render("fixed safety guard"), "")
			lines = append(lines, strings.Repeat(" ", stressLabelW+2)+button(t, "Start "+s.startLabel(), focused)+"   "+t.Sub.Render("asks for confirmation"))
		}
	}
	if s.prepErr != "" {
		lines = append(lines, "", truncate(t.StatusText(model.StatusCrit, "✖ "+s.prepErr), w))
	}
	if ws := s.formWarnings(); len(ws) > 0 {
		lines = append(lines, "")
		for _, x := range ws {
			lines = append(lines, truncate(t.StatusText(model.StatusWarn, "▲ "+x), w))
		}
	}
	if s.component() == stress.CompNetwork || s.comp == 4 {
		lines = append(lines, "", truncate(t.Sub.Render("Network needs a second host: run tyr net-peer there and enter its address and token."), w))
	}
	return strings.Join(lines, "\n")
}

// textField is the bracketed text input; it edits in place.
func (s *stressTab) textField(t *Theme, f, v string, w int) string {
	if s.editing && s.fields()[s.field] == f {
		return s.input.Field(t, w)
	}
	in := lineInput{value: []rune(v)}
	return in.Field(t, w)
}

// otherEngines lists engines that exist for other components only.
func (s *stressTab) otherEngines(mine []stress.EngineInfo) string {
	if s.component() == "" {
		return ""
	}
	have := map[string]bool{}
	for _, e := range mine {
		have[e.Name] = true
	}
	var out []string
	for _, c := range stress.Components {
		for _, e := range stress.Engines(c, s.env) {
			if !have[e.Name] {
				have[e.Name] = true
				out = append(out, e.Name)
			}
		}
	}
	return strings.Join(out, ", ")
}

func (s *stressTab) allCPUsText() string {
	if s.snap != nil && s.snap.CPU != nil {
		n := 0
		for _, l := range s.snap.CPU.Logical {
			if l.Online {
				n++
			}
		}
		return fmt.Sprintf("all online (%d)", n)
	}
	return "all online"
}

func (s *stressTab) startLabel() string {
	if s.comp == 4 {
		return "full test"
	}
	return stressComps[s.comp] + " test"
}

// guardText: what stops a run on its own (stress.DefaultRules).
func (s *stressTab) guardText() string {
	if c := s.cpuCrit(); c != nil {
		return fmt.Sprintf("stop at %.0f °C (limit+%.0f), MCE, UE", *c+stress.CPUGuardMarginC, stress.CPUGuardMarginC)
	}
	return "stop at CRIT temp, any MCE or UE"
}

// cpuCrit is the lowest CPU package limit of the snapshot (its critical
// threshold: the CPU's Tjmax where known). The guard stops a test
// stress.CPUGuardMarginC above it.
func (s *stressTab) cpuCrit() *float64 {
	if s.critTemp != nil {
		return s.critTemp
	}
	var c *float64
	if s.snap != nil && s.snap.CPU != nil {
		for _, p := range s.snap.CPU.PackageTemps {
			if p.Thresholds != nil && p.Thresholds.UpperCrit != nil && (c == nil || *p.Thresholds.UpperCrit < *c) {
				c = p.Thresholds.UpperCrit
			}
		}
	}
	return c
}

// formWarnings are conditions that affect the run (offline CPUs, hot
// packages).
func (s *stressTab) formWarnings() []string {
	if s.snap == nil || s.snap.CPU == nil {
		return nil
	}
	c := s.component()
	if c != stress.CompCPU && c != "" {
		return nil
	}
	var out []string
	for _, l := range s.snap.CPU.Logical {
		if !l.Online {
			out = append(out, cpuKey(l.ID)+" is offline and will be skipped.")
		}
	}
	for _, p := range s.snap.CPU.PackageTemps {
		if p.Value != nil && p.Status >= model.StatusWarn {
			out = append(out, fmt.Sprintf("%s is already at %.0f °C; the guard may stop the test early.", p.Name, *p.Value))
		}
	}
	return out
}

// formRight: engine availability and recent runs.
func (s *stressTab) formRight(t *Theme, w int) string {
	lines := []string{t.Heading("Engines", "", w)}
	seen := map[string]bool{}
	for _, c := range stress.Components {
		for _, e := range stress.Engines(c, s.env) {
			if seen[e.Name] {
				continue
			}
			seen[e.Name] = true
			switch {
			case e.Name == stress.EngBuiltin:
				lines = append(lines, t.Dot(model.StatusOK)+" "+pad(e.Name, 10)+t.Sub.Render("always · verifies results"))
			case e.Available:
				lines = append(lines, t.Dot(model.StatusOK)+" "+pad(e.Name, 10)+t.Sub.Render("installed"))
			default:
				reason, _ := engineReason(e)
				lines = append(lines, truncate(lipgloss.NewStyle().Foreground(t.NA).Render("○")+" "+t.Dim.Render(pad(e.Name, 10))+t.Sub.Render(reason), w))
			}
		}
	}
	if len(s.missing()) > 0 && !s.env.Simulated() {
		lines = append(lines, t.Sub.Render("i installs the missing engines of this test"))
	}
	lines = append(lines, "", t.HeadingRight("Recent runs", "", "enter open result", w))
	if len(s.history) == 0 {
		lines = append(lines, t.Sub.Render("no runs yet · results go to "+strings.Join(stress.RunDirs(s.env), " or ")))
	}
	nf := len(s.fields())
	for i, r := range s.history {
		st, word := verdictWord(&r)
		mark := " "
		if s.field == nf+i {
			mark = lipgloss.NewStyle().Foreground(t.Accent).Render("▌")
		}
		when := r.StartedAt.Local()
		day := when.Format("Jan 2")
		if y, m, d := time.Now().Date(); when.Year() == y && when.Month() == m && when.Day() == d {
			day = "today"
		}
		head := compTitle(r.Component) + " · " + fmtDuration(r.EndedAt.Sub(r.StartedAt).Round(time.Second)) + " · " + r.Engine + " · " + day + " " + when.Format("15:04")
		lines = append(lines, truncate(mark+t.verdictBadge(st, word)+" "+head, w))
		if len(r.Reasons) > 0 {
			lines = append(lines, truncate("   "+t.StatusText(st, r.Reasons[0]), w))
		}
	}
	return strings.Join(lines, "\n")
}

// verdictWord maps a result to the badge word: components that never ran
// are SKIPPED.
func verdictWord(r *stress.Result) (model.Status, string) {
	if r.Verdict == stress.VerdictAborted && strings.HasPrefix(r.Summary, "not run") {
		return model.StatusUnknown, "SKIPPED"
	}
	return r.Verdict.Status(), string(r.Verdict)
}

// --- confirmation -------------------------------------------------------------

func (s *stressTab) confirmView(t *Theme) string {
	base := s.formView(t)
	bw := min(96, s.w-4) - 4
	var lines []string
	var total time.Duration
	var names []string
	kind := t.Accent
	for _, p := range s.plans {
		total += p.Duration
		names = append(names, compTitle(p.Component))
		if len(p.Warnings) > 0 {
			kind = t.Warn
		}
	}
	if len(s.plans) > 1 {
		lines = append(lines, t.Bold.Render("Full test · "+strings.Join(names, " → "))+t.Dim.Render(" · about "+fmtDuration(total)))
	} else if len(s.plans) == 1 {
		p := s.plans[0]
		lines = append(lines, t.Bold.Render(fmt.Sprintf("%s · %s · %s", compTitle(p.Component), p.Engine, fmtDuration(p.Duration))))
	}
	lines = append(lines, "")
	ind := strings.Repeat(" ", 11)
	wrap := func(prefix, s string) {
		for i, l := range strings.Split(lipgloss.NewStyle().Width(bw-11).Render(s), "\n") {
			if i == 0 {
				lines = append(lines, prefix+l)
			} else {
				lines = append(lines, ind+l)
			}
		}
	}
	for _, p := range s.plans {
		head := pad(compTitle(p.Component), 9)
		if p.Blocked != "" {
			wrap(lipgloss.NewStyle().Foreground(t.NA).Render("○")+" "+t.Bold.Render(head), t.StatusText(model.StatusCrit, "cannot run: "+p.Blocked))
			continue
		}
		st := model.StatusOK
		if len(p.Warnings) > 0 {
			st = model.StatusWarn
		}
		first := p.Engine + " · " + fmtDuration(p.Duration)
		scope := p.Scope
		if len(scope) > 0 {
			first += " · " + scope[0]
			scope = scope[1:]
		}
		wrap(t.Dot(st)+" "+t.Bold.Render(head), first)
		for _, x := range scope {
			wrap(ind, t.Dim.Render(x))
		}
		for _, x := range p.Impact {
			wrap(ind, t.Dim.Render(x))
		}
		for _, x := range p.Warnings {
			wrap(ind, t.StatusText(model.StatusWarn, "▲ "+x))
		}
		for _, x := range p.Info {
			wrap(ind, t.Sub.Render("note: "+x))
		}
	}
	lines = append(lines, "", lipgloss.NewStyle().Width(bw).Render(t.Dim.Render("Abort any time with esc; all workers stop cleanly. "+
		"The guard stops the run on a critical temperature, a new machine check or an uncorrectable ECC error.")), "")
	if s.startable() {
		lines = append(lines, t.Bold.Render("Type yes and press enter to start  ")+s.confirm.Field(t, 8), t.Sub.Render("esc cancel"))
	} else {
		lines = append(lines, t.StatusText(model.StatusCrit, "Nothing can run."), t.Sub.Render("esc back to the form"))
	}
	title := "Confirm stress test"
	if kind == t.Warn {
		title = "▲ " + title
	}
	return overlay(t, base, kind, title, strings.Join(lines, "\n"), s.w, s.h)
}

// --- live ---------------------------------------------------------------------

// metricTitle names a metric for graphs and the result table.
func metricTitle(name string) string { return stress.MetricTitle(name) }

// mainMetric picks the test's own throughput metric.
func (s *stressTab) mainMetric() (string, bool) {
	var names []string
	for n := range s.series {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, pref := range []string{"verified_", "read_bytes_per_s_", "file_write", "net_test", "bogo_ops", "memtester", "mprime"} {
		for _, n := range names {
			if strings.HasPrefix(n, pref) {
				return n, true
			}
		}
	}
	return "", false
}

// panels lists the graphs of the running component, most telling first.
func (s *stressTab) panels() []string {
	main, ok := s.mainMetric()
	var out []string
	add := func(names ...string) {
		for _, n := range names {
			if n != "" && !contains(out, n) {
				out = append(out, n)
			}
		}
	}
	if !ok {
		main = ""
	}
	switch s.run.Cfg.Component {
	case stress.CompMemory:
		add("mem_used", main, "cpu_load", "cpu_temp", "cpu_freq", "throttle")
	case stress.CompStorage:
		add(main, "disk_read", "disk_write", "cpu_temp", "cpu_load", "mem_used")
	case stress.CompNetwork:
		add(main, "net_tx", "net_rx", "cpu_load", "tcp_retransmits", "cpu_temp")
	default:
		add("cpu_load", "cpu_temp", main, "cpu_freq", "pkg_power", "throttle")
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// fmtClock: 04:12, or 1:04:12 from an hour.
func fmtClock(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, sec := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}

// runTitle: "CPU stress · builtin · 63 CPUs · started 10:41".
func (s *stressTab) runTitle(t *Theme) string {
	r := s.run
	title := compTitle(r.Cfg.Component) + " stress"
	if len(s.queue) > 1 {
		title = fmt.Sprintf("Full test %d/%d · %s", s.runIdx+1, len(s.queue), compTitle(r.Cfg.Component))
	}
	parts := []string{r.Cfg.Engine}
	switch r.Cfg.Component {
	case stress.CompCPU:
		if r.Cfg.CPUs != "" {
			parts = append(parts, "CPUs "+r.Cfg.CPUs)
		} else if n := len(s.cores); n > 0 {
			on := 0
			for _, o := range s.coreOn {
				if o {
					on++
				}
			}
			parts = append(parts, fmt.Sprintf("%d CPUs", on))
		}
	case stress.CompMemory:
		parts = append(parts, fmt.Sprintf("%d %% of available", r.Cfg.MemoryPercent))
	case stress.CompStorage, stress.CompNetwork:
		if r.Cfg.Target != "" {
			parts = append(parts, r.Cfg.Target)
		}
	}
	parts = append(parts, "started "+r.Start.Local().Format("15:04"))
	return t.Bold.Render(title) + t.Dim.Render(" · "+strings.Join(parts, " · "))
}

// verdict is the live banner state: PASSING, FAILING or GUARD TRIPPED.
func (s *stressTab) verdict() (model.Status, string, string) {
	switch {
	case s.guard != "":
		return model.StatusCrit, "GUARD TRIPPED", s.guard
	case len(s.verify) > 0:
		n := len(s.verify)
		return model.StatusCrit, "FAILING", fmt.Sprintf("%d wrong %s", n, plural(n, "result", "results"))
	}
	return model.StatusOK, "PASSING", "no wrong results"
}

func (s *stressTab) liveView(t *Theme) string {
	w, h := s.w, s.bodyH()
	if s.run == nil {
		return t.Bold.Render("Stress test") + "\n\n  " + t.Message.Render("starting …")
	}
	r := s.run
	p, stage := r.Status()
	el := time.Since(r.Start)
	rem := max(0, r.Cfg.Duration-el)

	st, word, msg := s.verdict()
	right := t.Dim.Render(fmtClock(el) + " elapsed · " + fmtClock(rem) + " left")
	if w < 100 {
		right = t.Dim.Render(fmtClock(rem) + " left")
	}
	banner := "  " + t.verdictBadge(st, word) + "  " + t.StatusBold(st, msg)
	banner = padCell(banner, max(lipgloss.Width(banner)+2, w-lipgloss.Width(right)), false) + right

	suffix := fmt.Sprintf("  %.0f %% · stage %d/%d", p*100, s.runIdx+1, max(1, len(s.queue)))
	if stage != "" && w >= 100 {
		suffix += " · " + stage
	}
	bw := max(10, w-9-lipgloss.Width(suffix))
	fill := int(p * float64(bw))
	prog := t.Dim.Render("progress ") + lipgloss.NewStyle().Foreground(t.Accent).Render(strings.Repeat("█", fill)) +
		lipgloss.NewStyle().Foreground(t.Track).Render(strings.Repeat("░", bw-fill)) + t.Dim.Render(suffix)
	out := []string{truncate(s.runTitle(t), w), truncate(banner, w), truncate(prog, w), ""}

	if w < 100 {
		out = append(out, s.liveSparks(t, w)...)
	} else {
		out = append(out, s.liveGraphs(t, w, h-len(out)-10)...)
	}
	if core := s.perCore(t, w); core != "" {
		out = append(out, "", core)
	}
	out = append(out, "")
	rest := max(2, h-len(out))
	if w < 100 {
		line := t.Bold.Render(fmt.Sprintf("Wrong results %d", len(s.verify)))
		if len(s.logs) > 0 {
			line += t.Dim.Render("  log: " + s.logs[len(s.logs)-1])
		}
		out = append(out, truncate(line, w))
		return strings.Join(out, "\n")
	}
	lw := w/2 - 1
	rw := w - lw - 2
	var left []string
	left = append(left, t.HeadingRight("Wrong results", "", fmt.Sprint(len(s.verify)), lw))
	if len(s.verify) == 0 {
		left = append(left, t.StatusText(model.StatusOK, "● none so far")+t.Sub.Render(" · every result is checked"))
	}
	for i := max(0, len(s.verify)-(rest-1)); i < len(s.verify); i++ {
		v := s.verify[i]
		left = append(left, truncate(t.StatusText(model.StatusCrit, "✖ ")+t.Dim.Render(v.Time.Local().Format("15:04:05")+" ")+t.Bold.Render(v.Subject)+" "+
			t.Dim.Render(v.Workload)+" "+strings.TrimSpace(v.Expected+" → "+v.Got+" "+v.Detail), lw))
	}
	logs := []string{t.HeadingRight("Log", "", "newest last", rw)}
	for i := max(0, len(s.logs)-(rest-1)); i < len(s.logs); i++ {
		logs = append(logs, truncate(t.Dim.Render(s.logs[i]), rw))
	}
	out = append(out, besideInspector(strings.Join(left, "\n"), lw+2, strings.Join(logs, "\n"), min(rest, max(len(left), len(logs)))))
	return strings.Join(out, "\n")
}

// liveGraphs: 2×2 braille graphs, 3×2 from 150 columns.
func (s *stressTab) liveGraphs(t *Theme, w, avail int) []string {
	cols, ph := 2, 6
	if w >= 150 {
		cols, ph = 3, 8
	}
	names := s.panels()
	if len(names) > cols*2 {
		names = names[:cols*2]
	}
	rows := (len(names) + cols - 1) / cols
	ch := min(ph+3, max(5, avail/max(1, rows)))
	gap := 2
	cw := (w - gap*(cols-1)) / cols
	var charts []string
	for i, n := range names {
		charts = append(charts, s.graph(t, n, t.Series[i%len(t.Series)], cw, ch))
	}
	var out []string
	for i := 0; i < len(charts); i += cols {
		var parts []string
		for j, c := range charts[i:min(i+cols, len(charts))] {
			if j > 0 {
				parts = append(parts, strings.Repeat(" ", gap))
			}
			parts = append(parts, lipgloss.NewStyle().Width(cw).Render(c))
		}
		out = append(out, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, parts...), "\n")...)
	}
	return out
}

// graph is one live panel: title, value coloured by its limit, limits as
// labelled lines.
func (s *stressTab) graph(t *Theme, name string, col color.Color, w, h int) string {
	if name == "pkg_power" { // no RAPL collector yet
		lines := []string{t.Bold.Render("Package power W"), "", t.Unavail("package power", "no RAPL collector yet")}
		return strings.Join(lines, "\n")
	}
	v := s.series[name]
	spec := chartSpec{Title: metricTitle(name), Series: v, Color: col, YFmt: trimFloat, ValueSt: model.StatusOK,
		Window: time.Duration(len(v)) * time.Second}
	if len(v) > 0 {
		spec.Value = fmtUnit(last(v), s.units[name])
	}
	switch name {
	case "cpu_load", "mem_used":
		spec.Max = 100
	case "cpu_temp":
		spec.Min = 20
		if c := s.cpuCrit(); c != nil {
			g := *c + stress.CPUGuardMarginC
			spec.Limits = []chartLimit{{V: g, Label: fmt.Sprintf("guard %.0f", g), St: model.StatusCrit},
				{V: *c, Label: fmt.Sprintf("limit %.0f", *c), St: model.StatusWarn}}
			spec.Max = g + 10
			if len(v) > 0 {
				spec.ValueSt = checkEval(&v[len(v)-1], &model.Thresholds{UpperWarn: ptr(*c - 10), UpperCrit: c})
			}
		}
	case "throttle":
		spec.Max = max(5, last(v)*1.2)
		if len(v) > 0 && last(v) > 0 {
			spec.Value, spec.ValueSt = fmt.Sprintf("+%.0f", last(v)), model.StatusWarn
		}
	}
	return brailleChart(t, spec, w, h)
}

// liveSparks: below 100 columns each graph is one sparkline row.
func (s *stressTab) liveSparks(t *Theme, w int) []string {
	short := map[string]string{"cpu_load": "CPU load", "cpu_temp": "Hottest CPU", "cpu_freq": "Freq MHz", "verified_kernels_per_s": "Verified/s",
		"verified_bytes_per_s": "Verified/s", "mem_used": "Memory used"}
	var out []string
	for i, n := range s.panels() {
		if len(out) == 4 || n == "pkg_power" {
			continue
		}
		v := s.series[n]
		val := ""
		if len(v) > 0 {
			val = fmtUnit(last(v), s.units[n])
		}
		note := ""
		if n == "cpu_temp" {
			if c := s.cpuCrit(); c != nil {
				note = t.Sub.Render(fmt.Sprintf(" limit %.0f", *c))
			}
		}
		label := short[n]
		if label == "" {
			label = truncate(metricTitle(n), 15)
		}
		label = pad(label, 16)
		sw := max(8, w-16-12-lipgloss.Width(note))
		mx := 0.0
		if n == "cpu_load" || n == "mem_used" {
			mx = 100
		}
		out = append(out, truncate(t.Dim.Render(label)+padCell(spark(v, sw, mx, t.Series[i%len(t.Series)]), sw, false)+" "+padCell(t.Bold.Render(val), 10, true)+note, w))
	}
	return out
}

// perCore: one block per CPU, height = load; offline CPUs are ✖.
func (s *stressTab) perCore(t *Theme, w int) string {
	if len(s.cores) == 0 || s.run.Cfg.Component != stress.CompCPU {
		return ""
	}
	blocks := []rune(" ▁▂▃▄▅▆▇█")
	bySock := map[int][]string{}
	var socks []int
	off := 0
	for i, v := range s.cores {
		sk := 0
		if i < len(s.coreSock) {
			sk = s.coreSock[i]
		}
		if _, ok := bySock[sk]; !ok {
			socks = append(socks, sk)
		}
		cell := lipgloss.NewStyle().Foreground(t.Series[0]).Render(string(blocks[min(8, max(1, int(v/100*8+0.5)))]))
		if i < len(s.coreOn) && !s.coreOn[i] {
			cell = t.StatusText(model.StatusCrit, "✖")
			off++
		}
		bySock[sk] = append(bySock[sk], cell)
	}
	sort.Slice(socks, func(i, j int) bool { // CPUs without topology last
		if (socks[i] < 0) != (socks[j] < 0) {
			return socks[j] < 0
		}
		return socks[i] < socks[j]
	})
	note := countLabel(len(s.cores), "CPU", "CPUs")
	if off > 0 {
		note += " · ✖ offline"
	}
	note += " · block height = load"
	var parts []string
	for _, sk := range socks {
		label := "socket " + strconv.Itoa(sk)
		if sk < 0 {
			label = "no topology"
		}
		parts = append(parts, t.Dim.Render(label+" ")+strings.Join(bySock[sk], ""))
	}
	lines := []string{t.HeadingRight("Per-core load", "", note, w)}
	lines = append(lines, truncate(strings.Join(parts, "  "), w))
	return strings.Join(lines, "\n")
}

// --- result -------------------------------------------------------------------

// metricRelevant keeps the metrics that say something about the tested
// component (the side readings of other parts stay in the result file).
func metricRelevant(c stress.Component, name string) bool {
	pre := map[stress.Component][]string{
		stress.CompCPU:     {"cpu_", "verified_kernels", "bogo_ops", "mprime"},
		stress.CompMemory:  {"mem_used", "verified_bytes", "memtester", "bogo_ops"},
		stress.CompStorage: {"disk_", "read_", "slow_reads_", "file_"},
		stress.CompNetwork: {"net_", "tcp_"},
	}[c]
	for _, p := range pre {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// overall: FAIL wins, then the worst exit code.
func overall(rs []*stress.Result) stress.Verdict {
	worst := stress.VerdictPass
	for _, r := range rs {
		if r.Verdict == stress.VerdictFail {
			return stress.VerdictFail
		}
		if r.Verdict.ExitCode() > worst.ExitCode() {
			worst = r.Verdict
		}
	}
	return worst
}

func (s *stressTab) resultView(t *Theme) string {
	w, h := s.w, s.bodyH()
	if len(s.results) == 0 {
		return t.Sub.Render("no result")
	}
	first := s.results[0]
	var engines []string
	var end time.Time
	for _, r := range s.results {
		if r.Engine != "" && !contains(engines, r.Engine) {
			engines = append(engines, r.Engine)
		}
		if r.EndedAt.After(end) {
			end = r.EndedAt
		}
	}
	name := compTitle(first.Component) + " stress result"
	if len(s.results) > 1 {
		name = "Full test result"
	}
	head := t.Bold.Render(name) + t.Dim.Render(" · started "+first.StartedAt.Local().Format("15:04")+" · "+fmtDuration(end.Sub(first.StartedAt).Round(time.Second))+" · "+strings.Join(engines, " + "))
	v := overall(s.results)
	reason := "no wrong results, no new errors, no new findings"
	for _, r := range s.results {
		if r.Verdict == v && len(r.Reasons) > 0 {
			reason = r.Reasons[0]
			if len(s.results) > 1 {
				reason = strings.ToLower(compTitle(r.Component)) + ": " + reason
			}
			break
		}
	}
	right := t.Sub.Render("overall verdict")
	banner := "  " + t.verdictBadge(v.Status(), string(v)) + "  " + t.StatusBold(v.Status(), reason)
	banner = padCell(truncate(banner, w-lipgloss.Width(right)-2), w-lipgloss.Width(right), false) + right
	top := []string{truncate(head, w), banner, ""}

	var body []string
	body = append(body, t.Heading("Components", "", w))
	for _, r := range s.results {
		st, word := verdictWord(r)
		facts := []string{}
		if !r.EndedAt.IsZero() && !r.StartedAt.IsZero() {
			facts = append(facts, fmtDuration(r.EndedAt.Sub(r.StartedAt).Round(time.Second)))
		}
		if r.Engine != "" {
			facts = append(facts, r.Engine)
		}
		if n := len(r.VerifyErrors); n > 0 {
			facts = append(facts, fmt.Sprintf("%d wrong %s", n, plural(n, "result", "results")))
		}
		why := ""
		if len(r.Reasons) > 0 {
			why = t.StatusText(st, strings.Join(r.Reasons, " · "))
		} else if st == model.StatusOK {
			why = t.StatusText(model.StatusOK, "no wrong results, no new errors")
		}
		line := padCell(" "+t.verdictBadge(st, word), 11, false) + t.Bold.Render(pad(compTitle(r.Component), 9)) + t.Dim.Render(strings.Join(facts, " · "))
		line = padCell(line, max(lipgloss.Width(line)+2, w*55/100), false) + why
		body = append(body, truncate(line, w))
	}

	var rows []row
	for _, r := range s.results {
		for _, m := range r.Metrics {
			if !metricRelevant(r.Component, m.Name) {
				continue
			}
			st, limit := model.StatusOK, ""
			if m.Name == "cpu_temp" {
				if c := s.cpuCrit(); c != nil {
					limit = fmt.Sprintf("limit %.0f · guard %.0f", *c, *c+stress.CPUGuardMarginC)
					if m.Max >= *c {
						st = model.StatusWarn
					}
				}
			}
			title := metricTitle(m.Name)
			rows = append(rows, row{cells: []string{title, fmtUnit(m.Min, m.Unit), fmtUnit(m.Avg, m.Unit), fmtUnit(m.Max, m.Unit), limit}, status: st})
		}
	}
	if len(rows) > 0 {
		body = append(body, "", t.HeadingRight("Metrics", "", "min · avg · max", w))
		cols := []column{{"Metric", -1}, {">Min", 11}, {">Avg", 11}, {">Max", 11}, {"Limit", 0}}
		body = append(body, strings.Split(renderList(t, cols, rows, -1, w, len(rows)+1), "\n")...)
	}

	var ve []stress.VerifyError
	for _, r := range s.results {
		ve = append(ve, r.VerifyErrors...)
	}
	if len(ve) > 0 {
		body = append(body, "", t.HeadingRight("Wrong results", "", t.StatusText(model.StatusCrit, fmt.Sprint(len(ve))), w))
		for _, v := range ve {
			body = append(body, truncate(t.StatusText(model.StatusCrit, "✖ ")+t.Dim.Render(v.Time.Local().Format("15:04:05")+" "+v.Workload+" ")+
				t.Bold.Render(v.Subject)+"  "+strings.TrimSpace(v.Expected+" → "+v.Got+" "+v.Detail), w))
		}
	}
	var files []string
	for _, r := range s.results {
		if r.File != "" {
			files = append(files, r.File)
		}
	}
	if len(files) > 0 {
		body = append(body, "", t.Heading("Result file", "", w))
		for _, f := range files {
			body = append(body, truncate("result file: "+f+t.Sub.Render(" · e export snapshot"), w))
		}
	}
	room := max(1, h-len(top))
	s.scroll = min(s.scroll, max(0, len(body)-room))
	end2 := min(len(body), s.scroll+room)
	return strings.Join(append(top, body[s.scroll:end2]...), "\n")
}

// --- formatting ---------------------------------------------------------------

func fmtDuration(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%d h", int(d.Hours()))
	case d >= time.Hour:
		return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d >= time.Minute:
		return fmt.Sprintf("%d min %d s", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d s", int(d.Seconds()))
}

func fmtUnit(v float64, unit string) string {
	switch unit {
	case "bytes_per_second":
		return humanRate(v, "B/s")
	case "bits_per_second":
		return humanRate(v, "b/s")
	case "percent":
		return fmt.Sprintf("%.0f %%", v)
	case "celsius":
		return fmt.Sprintf("%.0f °C", v)
	case "mhz":
		return grouped(uint64(max(0, v))) + " MHz"
	case "ms":
		return fmt.Sprintf("%.1f ms", v)
	case "ops_per_second":
		if v < 1e5 {
			return grouped(uint64(max(0, v))) + "/s"
		}
		return humanRate(v, "/s")
	}
	return strconv.FormatFloat(v, 'f', 0, 64)
}

func humanRate(v float64, suffix string) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.2f G%s", v/1e9, suffix)
	case v >= 1e6:
		return fmt.Sprintf("%.1f M%s", v/1e6, suffix)
	case v >= 1e3:
		return fmt.Sprintf("%.1f k%s", v/1e3, suffix)
	}
	return fmt.Sprintf("%.0f %s", v, suffix)
}

func last(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return v[len(v)-1]
}

func ptr(v float64) *float64 { return &v }
