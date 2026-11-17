package main

import (
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
	"syscall"
	"time"

	"github.com/noahzmr/tyr/internal/accept"
	"github.com/noahzmr/tyr/internal/buildinfo"
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/collector/all"
	"github.com/noahzmr/tyr/internal/model"
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
	cenv, _, err := c.env()
	if err != nil {
		fmt.Fprintln(stderr, "tyr:", err)
		return 3
	}
	if os.Geteuid() != 0 && c.fixtures == "" {
		fmt.Fprintln(stderr, "tyr accept: run as root: the disk scan, SMART, the BMC and the memory test need it")
		return 3
	}
	fmt.Fprintf(stdout, "Tyr %s acceptance test, profile %s: %s\n", buildinfo.Version, p.Name, p.Description)
	fmt.Fprintf(stdout, "load: %s", fmtDur(p.Total(*peer != "")))
	for _, k := range accept.Components {
		if d := time.Duration(p.Durations[k]); d > 0 && (k != "network" || *peer != "") {
			fmt.Fprintf(stdout, ", %s %s", k, fmtDur(d))
		}
	}
	fmt.Fprintln(stdout)
	if !*yes {
		if !isTTY(os.Stdin) {
			fmt.Fprintln(stderr, "tyr accept: not a terminal; pass --yes to start without confirmation")
			return 3
		}
		fmt.Fprint(stdout, "The machine is under full load for that long. Type \"yes\" to start: ")
		var line string
		fmt.Fscanln(os.Stdin, &line)
		if strings.TrimSpace(line) != "yes" {
			fmt.Fprintln(stdout, "not started")
			return 3
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	r := &accept.Result{SchemaVersion: "1.0", Kind: "acceptance", TyrVersion: buildinfo.Version, Profile: p, StartedAt: time.Now()}
	var incomplete []string
	check := func(name string) *model.Snapshot {
		fmt.Fprintf(stdout, "\n== %s\n", name)
		t0 := time.Now()
		s := collector.Collect(ctx, cenv, all.Collectors(), nil)
		fs := s.Findings()
		n := 0
		for _, f := range fs {
			if f.Status >= model.StatusWarn {
				n++
				fmt.Fprintf(stdout, "  %s %s\n", map[model.Status]string{model.StatusWarn: "▲", model.StatusCrit: "✖"}[f.Status], f.Message)
			}
		}
		fmt.Fprintf(stdout, "%s: %s, %d findings\n", name, s.Status, n)
		r.Steps = append(r.Steps, accept.Step{Name: name, Verdict: s.Status.String(), Summary: fmt.Sprintf("%d findings", n), Took: time.Since(t0)})
		return s
	}

	before := check("health check")
	r.Hostname, r.Machine = before.Hostname, accept.Identify(before)
	dir, err := resultsDir(*out, r)
	if err != nil {
		fmt.Fprintln(stderr, "tyr accept:", err)
		return 3
	}
	fmt.Fprintln(stdout, "results: "+dir)
	saveJSON(filepath.Join(dir, "before.json"), before)

	env := &stress.Env{Collector: cenv, Cols: all.Collectors(), RunDir: filepath.Join(dir, "runs")}
	var runs []*stress.Result
	for _, comp := range accept.Components {
		d := time.Duration(p.Durations[comp])
		if d <= 0 {
			continue
		}
		if comp == "network" && *peer == "" {
			r.Steps = append(r.Steps, accept.Step{Name: comp, Verdict: "SKIPPED", Summary: "no peer (tyr.peer= on the kernel command line, or --peer)"})
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
			cfg.Target, cfg.Token, cfg.Streams = *peer, *token, 4
		}
		t0 := time.Now()
		_, line, res := stressRun(ctx, env, cfg, nil, stdout, stderr, true)
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
	}
	if ctx.Err() != nil {
		incomplete = append(incomplete, "interrupted")
	}

	after := check("final check")
	saveJSON(filepath.Join(dir, "after.json"), after)
	r.EndedAt = time.Now()
	accept.Judge(p, r, before, after, runs, incomplete)
	saveJSON(filepath.Join(dir, "accept.json"), r)
	os.WriteFile(filepath.Join(dir, "accept.md"), []byte(accept.Markdown(r, after)), 0o644)
	os.WriteFile(filepath.Join(dir, "VERDICT"), []byte(string(r.Verdict)+"\n"), 0o644)
	banner(stdout, r, isTTY(os.Stdout))
	fmt.Fprintln(stdout, "report: "+filepath.Join(dir, "accept.md"))
	if c.fixtures == "" || *toNetBox {
		acceptNetBox(nbFiles, *toNetBox, *noNetBox, r, after, dir, stdout)
	}
	return r.Verdict.ExitCode()
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
	for _, s := range []string{line, pad(title), pad(strings.TrimSpace(r.Machine.Vendor + " " + r.Machine.Product + "  S/N " + r.Machine.Serial)), line} {
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
