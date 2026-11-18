package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/noahzmr/tyr/internal/accept"
	"github.com/noahzmr/tyr/internal/acceptui"
	"github.com/noahzmr/tyr/internal/buildinfo"
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/collector/all"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/notify"
	"github.com/noahzmr/tyr/internal/report"
	"github.com/noahzmr/tyr/internal/stress"
)

// runAccept is the acceptance test: health check, the stress tests one
// after another, a second health check, judged against a limits profile.
func runAccept(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tyr accept", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.register(fs)
	profile := fs.String("profile", "burnin", "quick (~30 min), burnin (~4 h) or extended (~24 h)")
	limits := fs.String("limits", "", "JSON file with limits and durations, merged onto the profile (see --print-profile)")
	out := fs.String("out", "", "results directory; a subdirectory per run is created (default /var/lib/tyr/accept, else ./tyr-accept)")
	peer := fs.String("peer", "", "network test: host[:port] of a `tyr net-peer` (without, the network test is skipped)")
	token := fs.String("token", "", "network test: the token `tyr net-peer` printed")
	printProfile := fs.Bool("print-profile", false, "print the effective profile as JSON (a template for --limits) and exit")
	yes := fs.Bool("yes", false, "start without the confirmation prompt")
	var nbFiles netboxFiles
	nbFiles.register(fs)
	toNetBox := fs.Bool("netbox", false, "write the result to NetBox at the end (default: when after_accept is set in the NetBox settings)")
	noNetBox := fs.Bool("no-netbox", false, "do not write to NetBox")
	webhooks := fs.String("webhooks", notify.Path(), "webhook settings (JSON; TYR_WEBHOOK_URL and friends add one more; edit with W in the TUI)")
	noWebhook := fs.Bool("no-webhook", false, "send no webhook notifications")
	plain := fs.Bool("plain", false, "plain lines instead of the live view (what it prints without a terminal)")
	if err := fs.Parse(args); err != nil {
		return 3
	}
	p, err := accept.Builtin(*profile)
	if err == nil && *limits != "" {
		p, err = accept.Load(p, *limits)
	}
	if err != nil {
		fmt.Fprintln(stderr, "tyr accept:", err)
		return 3
	}
	if *printProfile {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(p)
		return 0
	}
	c.opts.CoreTest = true // the quick per-core test finds dead cores before the long load
	if p.RequireEncryption {
		c.opts.RequireEncryption = true
	}
	if c.opts.RequireEncryption {
		p.FailOnWarn = append(p.FailOnWarn, "not_encrypted", "swap_not_encrypted")
	}
	cenv, _, err := c.env()
	if err != nil {
		fmt.Fprintln(stderr, "tyr:", err)
		return 3
	}
	if os.Geteuid() != 0 && c.fixtures == "" {
		fmt.Fprintln(stderr, "tyr accept: run as root: the disk scan, SMART, the BMC and the memory test need it")
		return 3
	}
	// console.log: everything the plain output shows, from the first line;
	// kept in memory until the results directory exists
	clog := &consoleLog{}
	defer clog.Close()
	hw := io.MultiWriter(stdout, clog)
	fmt.Fprintf(hw, "Tyr %s acceptance test, profile %s: %s\n", buildinfo.Version, p.Name, p.Description)
	fmt.Fprintf(hw, "load: %s", fmtDur(p.Total(*peer != "")))
	planned := []string{"health check"}
	for _, k := range accept.Components {
		if d := time.Duration(p.Durations[k]); d > 0 && (k != "network" || *peer != "") {
			fmt.Fprintf(hw, ", %s %s", k, fmtDur(d))
			planned = append(planned, k)
		}
	}
	planned = append(planned, "final check")
	fmt.Fprintln(hw)
	if !*yes {
		if !isTTY(os.Stdin) {
			fmt.Fprintln(stderr, "tyr accept: not a terminal; pass --yes to start without confirmation")
			return 3
		}
		fmt.Fprint(stdout, "The machine is under full load for that long. Type \"yes\" to start: ")
		var line string
		fmt.Fscanln(os.Stdin, &line)
		if strings.TrimSpace(line) != "yes" {
			fmt.Fprintln(hw, "not started")
			return 3
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	run := acceptRun{p: p, c: &c, cenv: cenv, peer: *peer, token: *token, out: *out, webhooks: *webhooks, noWebhook: *noWebhook, clog: clog}
	if !isTTY(os.Stdout) || *plain || os.Getenv("TERM") == "dumb" {
		// plain lines, as before (no terminal: a log, a pipe, a service)
		run.w = hw
		if code := run.run(ctx); code >= 0 {
			return code
		}
	} else {
		ui := acceptui.New(acceptui.Info{Version: buildinfo.Version, Profile: p.Name, Expected: p.Total(*peer != ""), Steps: planned}, cancel)
		prog := tea.NewProgram(ui, tea.WithOutput(os.Stdout), tea.WithInput(os.Stdin))
		run.ui = prog.Send
		run.w = io.MultiWriter(&lineSender{send: prog.Send}, clog)
		code, done := -1, make(chan struct{})
		go func() {
			defer close(done)
			code = run.run(ctx)
			prog.Send(acceptui.DoneMsg{})
		}()
		if _, err := prog.Run(); err != nil {
			fmt.Fprintln(stderr, "tyr accept: screen:", err)
		}
		<-done
		if code >= 0 {
			return code
		}
	}
	return run.finish(hw, stdout, *toNetBox, *noNetBox, nbFiles)
}

// acceptRun is one acceptance test: run does the steps and writes the
// results, finish prints the verdict and reports it (NetBox, webhooks).
type acceptRun struct {
	p                accept.Profile
	c                *common
	cenv             *collector.Env
	peer, token, out string
	webhooks         string
	noWebhook        bool
	clog             *consoleLog
	w                io.Writer     // the plain lines (the live view's log area)
	ui               func(tea.Msg) // the live view, nil in plain mode
	r                *accept.Result
	after            *model.Snapshot
	dir              string
	nt               *notify.Notifier
	event            func(kind string) notify.Event
}

func (a *acceptRun) send(m tea.Msg) {
	if a.ui != nil {
		a.ui(m)
	}
}

// run does the test; it returns an exit code when it stops early, else -1.
func (a *acceptRun) run(ctx context.Context) int {
	p, stdout := a.p, a.w
	r := &accept.Result{SchemaVersion: "1.0", Kind: "acceptance", TyrVersion: buildinfo.Version, Profile: p, StartedAt: time.Now()}
	a.r = r
	// One set of collectors for the whole run: the storage collector counts
	// the I/O errors Tyr's own SMART queries cause (some drives reject a few
	// of them), across the health checks and every stress snapshot; with
	// a new set per check the final check would count them as the disk's.
	cols := all.Collectors()
	var incomplete []string
	check := func(name string) *model.Snapshot {
		a.send(acceptui.StepStartMsg{Name: name})
		fmt.Fprintf(stdout, "\n== %s\n", name)
		t0 := time.Now()
		s := collector.Collect(ctx, a.cenv, cols, nil)
		n := 0
		var details []string
		for _, f := range s.Findings() {
			if f.Status >= model.StatusWarn {
				n++
				mark := map[model.Status]string{model.StatusWarn: "▲", model.StatusCrit: "✖"}[f.Status]
				fmt.Fprintf(stdout, "  %s %s\n", mark, f.Message)
				details = append(details, mark+" "+f.Message)
			}
		}
		fmt.Fprintf(stdout, "%s: %s, %d findings\n", name, s.Status, n)
		st := accept.Step{Name: name, Verdict: s.Status.String(), Summary: fmt.Sprintf("%d findings", n), Took: time.Since(t0)}
		r.Steps = append(r.Steps, st)
		a.send(acceptui.StepDoneMsg{Name: name, Verdict: st.Verdict, Summary: st.Summary, Took: st.Took, Details: details})
		return s
	}

	before := check("health check")
	r.Hostname, r.Machine = before.Hostname, accept.Identify(before)
	dir, err := resultsDir(a.out, r)
	if err != nil {
		fmt.Fprintln(stdout, "tyr accept:", err)
		return 3
	}
	a.dir = dir
	a.clog.Open(filepath.Join(dir, "console.log"))
	fmt.Fprintln(stdout, "results: "+dir)
	saveJSON(filepath.Join(dir, "before.json"), before)

	// webhooks: started now, a failed or warning step at once, the verdict at the end
	nt := loadNotifier(a.webhooks, a.noWebhook, stdout)
	a.nt = nt
	machine := notify.MachineOf(before)
	a.event = func(kind string) notify.Event {
		ev := notify.New(kind, "acceptance", machine)
		ev.Profile, ev.ResultsDir = p.Name, dir
		return ev
	}
	stepEvent := func(st accept.Step) {
		kind := ""
		switch st.Verdict {
		case "FAIL", "ABORTED", "NOT RUN", "CRIT":
			kind = notify.EvStepFailed
		case "WARN":
			kind = notify.EvStepWarning
		}
		if kind != "" {
			ev := a.event(kind)
			ev.Step, ev.Status, ev.Summary = st.Name, st.Verdict, st.Summary
			nt.Send(ev)
		}
	}
	ev := a.event(notify.EvStarted)
	ev.Duration = fmtDur(p.Total(a.peer != ""))
	ev.Summary = fmt.Sprintf("health check: %s, %s", before.Status, r.Steps[0].Summary)
	nt.Send(ev)
	stepEvent(r.Steps[0])

	// the live view's values come from the stress monitor's events
	var obs func(stress.Event, *stress.Run)
	if a.ui != nil {
		obs = func(e stress.Event, run *stress.Run) {
			switch e.Kind {
			case stress.EvProgress:
				a.send(acceptui.ProgressMsg{Fraction: e.Fraction, Stage: e.Text})
			case stress.EvMetric:
				a.send(acceptui.MetricMsg{Name: e.Name, Unit: e.Unit, Value: e.Value})
			case stress.EvSample:
				f, stage := run.Status()
				a.send(acceptui.ProgressMsg{Fraction: f, Stage: stage})
				sm := acceptui.SampleMsg{Counters: e.Sample.Deltas}
				if s := e.Sample.Snap; s != nil && s.Power != nil {
					sm.Watts = s.Power.ConsumedWatts
				}
				a.send(sm)
			}
		}
	}

	env := &stress.Env{Collector: a.cenv, Cols: cols, RunDir: filepath.Join(dir, "runs")}
	var runs []*stress.Result
	for _, comp := range accept.Components {
		d := time.Duration(p.Durations[comp])
		if d <= 0 {
			continue
		}
		if comp == "network" && a.peer == "" {
			st := accept.Step{Name: comp, Verdict: "SKIPPED", Summary: "no peer (tyr.peer= on the kernel command line, or --peer)"}
			r.Steps = append(r.Steps, st)
			a.send(acceptui.StepDoneMsg{Name: st.Name, Verdict: st.Verdict, Summary: st.Summary})
			continue
		}
		if ctx.Err() != nil {
			incomplete = append(incomplete, comp+" stress: not run (interrupted)")
			continue
		}
		cfg := stress.Config{Component: stress.Component(comp), Engine: stress.EngBuiltin, Duration: d}
		switch comp {
		case "cpu":
			if p.CPUEngine != "" {
				cfg.Engine = p.CPUEngine
			}
			cfg.FFT = p.FFT
		case "memory":
			cfg.MemoryPercent = p.MemoryPercent
		case "storage":
			cfg.StorageMode, cfg.Target = "read", "all"
			if !p.StorageUSB {
				cfg.Exclude = accept.USBDisks(before)
			}
		case "network":
			cfg.Target, cfg.Token, cfg.Streams = a.peer, a.token, 4
		}
		a.send(acceptui.StepStartMsg{Name: comp, Duration: d})
		t0 := time.Now()
		_, line, res := stressRun(ctx, env, cfg, nil, stdout, stdout, true, obs)
		env.Baseline = nil
		step := accept.Step{Name: comp, Summary: line, Took: time.Since(t0)}
		if res == nil {
			step.Verdict = "NOT RUN"
			incomplete = append(incomplete, line)
		} else {
			runs = append(runs, res)
			step.Verdict, step.Summary = string(res.Verdict), res.Summary
			if res.File != "" {
				if rel, err := filepath.Rel(dir, res.File); err == nil {
					step.File = rel
				}
				os.WriteFile(strings.TrimSuffix(res.File, ".json")+".md", []byte(report.Run(res)), 0o644)
			}
		}
		r.Steps = append(r.Steps, step)
		a.send(acceptui.StepDoneMsg{Name: step.Name, Verdict: step.Verdict, Summary: step.Summary, Took: step.Took})
		stepEvent(step)
	}
	if ctx.Err() != nil {
		incomplete = append(incomplete, "interrupted")
	}

	after := check("final check")
	a.after = after
	saveJSON(filepath.Join(dir, "after.json"), after)
	r.EndedAt = time.Now()
	accept.Judge(p, r, before, after, runs, incomplete)
	saveJSON(filepath.Join(dir, "accept.json"), r)
	os.WriteFile(filepath.Join(dir, "accept.md"), []byte(accept.Markdown(r, after)), 0o644)
	os.WriteFile(filepath.Join(dir, "VERDICT"), []byte(string(r.Verdict)+"\n"), 0o644)
	return -1
}

// finish prints the verdict to the terminal (and plain into console.log),
// writes to NetBox and sends the last webhook.
func (a *acceptRun) finish(w, screen io.Writer, toNetBox, noNetBox bool, nbFiles netboxFiles) int {
	r := a.r
	banner(screen, r, isTTY(os.Stdout))
	banner(a.clog, r, false)
	fmt.Fprintln(w, "report: "+filepath.Join(a.dir, "accept.md"))
	if a.c.fixtures == "" || toNetBox {
		acceptNetBox(nbFiles, toNetBox, noNetBox, r, a.after, a.dir, w)
	}
	ev := a.event(notify.FinishedEvent(string(r.Verdict)))
	ev.Status, ev.Failures, ev.Remarks = string(r.Verdict), r.Failures, r.Remarks
	ev.Duration = fmtDur(r.EndedAt.Sub(r.StartedAt))
	a.nt.Send(ev)
	a.nt.Wait(2 * time.Minute)
	return r.Verdict.ExitCode()
}

// consoleLog is console.log in the results directory: lines written before
// the directory exists are kept and written when it is opened.
type consoleLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
	f   *os.File
}

func (c *consoleLog) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f != nil {
		return c.f.Write(p)
	}
	return c.buf.Write(p)
}

// Open starts writing to path, beginning with what was kept.
func (c *consoleLog) Open(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return
	}
	f.Write(c.buf.Bytes())
	c.buf.Reset()
	c.f = f
}

func (c *consoleLog) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f != nil {
		c.f.Close()
		c.f = nil
	}
}

// lineSender passes whole lines of the plain output to the live view.
type lineSender struct {
	mu   sync.Mutex
	send func(tea.Msg)
	part []byte
}

func (l *lineSender) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.part = append(l.part, p...)
	for {
		i := bytes.IndexByte(l.part, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimRight(string(l.part[:i]), "\r"); strings.TrimSpace(line) != "" {
			l.send(acceptui.LogMsg(line))
		}
		l.part = l.part[i+1:]
	}
	return len(p), nil
}

// resultsDir creates <out>/<serial or hostname>-<UTC time>.
func resultsDir(out string, r *accept.Result) (string, error) {
	bases := []string{out}
	if out == "" {
		bases = []string{"/var/lib/tyr/accept", "tyr-accept"}
	}
	id := r.Machine.Serial
	if id == "" || strings.EqualFold(id, "to be filled by o.e.m.") || strings.Trim(id, "0 ") == "" {
		id = r.Hostname
	}
	id = regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(id, "_")
	name := id + "-" + r.StartedAt.UTC().Format("20060102T150405Z")
	var last error
	for _, b := range bases {
		d := filepath.Join(b, name)
		if err := os.MkdirAll(filepath.Join(d, "runs"), 0o755); err != nil {
			last = err
			continue
		}
		return d, nil
	}
	return "", last
}

func saveJSON(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err == nil {
		err = os.WriteFile(path, append(b, '\n'), 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tyr accept:", err)
	}
}

// banner prints the verdict so it can be read from across the room (or a
// BMC console thumbnail).
func banner(w io.Writer, r *accept.Result, color bool) {
	col := map[accept.Verdict]string{accept.Pass: "42;30", accept.Remarks: "43;30", accept.Fail: "41;97", accept.Incomplete: "45;97"}[r.Verdict]
	title := map[accept.Verdict]string{
		accept.Pass:       "PASS - fit for production",
		accept.Remarks:    "PASS WITH REMARKS - read them before production",
		accept.Fail:       "FAIL - not fit for production",
		accept.Incomplete: "INCOMPLETE - a test could not run or was stopped",
	}[r.Verdict]
	width := 72
	line := strings.Repeat(" ", width)
	pad := func(s string) string {
		n := width - 2 - len([]rune(s))
		if n < 0 {
			return " " + string([]rune(s)[:width-3]) + "… "
		}
		return " " + s + strings.Repeat(" ", n+1)
	}
	paint := func(s string) string {
		if !color {
			return s
		}
		return "\033[" + col + ";1m" + s + "\033[0m"
	}
	fmt.Fprintln(w)
	machine := strings.TrimSpace(r.Machine.Vendor + " " + r.Machine.Product)
	if sn := strings.TrimSpace(r.Machine.Serial); sn != "" {
		machine += "  S/N " + sn
	}
	for _, s := range []string{line, pad(title), pad(machine), line} {
		fmt.Fprintln(w, paint(s))
	}
	show := func(head string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintln(w, head)
		for i, s := range items {
			if i == 15 {
				fmt.Fprintf(w, "  … %d more in the report\n", len(items)-i)
				break
			}
			fmt.Fprintln(w, "  - "+s)
		}
	}
	if r.Verdict == accept.Incomplete {
		show("Did not complete:", r.Failures)
	} else {
		show("Failures:", r.Failures)
	}
	show("Remarks:", r.Remarks)
}

func fmtDur(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%02dm", h, m)
}
