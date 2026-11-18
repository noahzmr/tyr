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
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/noahzmr/tyr/internal/accept"
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/collector/all"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/netbox"
)

// netboxFiles registers --netbox-config and --tags.
type netboxFiles struct{ config, tags string }

func (n *netboxFiles) register(fs *flag.FlagSet) {
	nb, tg := netbox.Paths("")
	fs.StringVar(&n.config, "netbox-config", nb, "NetBox settings (JSON; edit with N in the TUI)")
	fs.StringVar(&n.tags, "tags", tg, "tag rules for NetBox (JSON; edit with N in the TUI)")
}

func (n *netboxFiles) load() (netbox.Config, netbox.Tags, error) {
	cfg, err := netbox.LoadConfig(n.config)
	if err != nil {
		return cfg, netbox.Tags{}, err
	}
	tags, err := netbox.LoadTags(n.tags)
	return cfg, tags, err
}

func runNetBox(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tyr netbox", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.register(fs)
	var files netboxFiles
	files.register(fs)
	dry := fs.Bool("dry-run", false, "show what would be written, write nothing")
	facts := fs.Bool("facts", false, "print the facts and which tag rules match this machine, then exit (no NetBox)")
	test := fs.Bool("test", false, "check the URL and token, write nothing (--dry-run also looks the device up)")
	from := fs.String("from", "", "write a saved snapshot (JSON) or an acceptance results directory instead of collecting now")
	validate := fs.Bool("validate", false, "check the two files (settings complete, rules valid) and exit")
	if err := fs.Parse(args); err != nil {
		return 3
	}
	cfg, tags, err := files.load()
	if err == nil && *validate {
		err = cfg.Validate()
	}
	if err != nil {
		fmt.Fprintln(stderr, "tyr netbox:", err)
		return 3
	}
	if *validate {
		fmt.Fprintf(stdout, "%s: %s, site %q, role %q, after_accept %v\n%s: %d tag rule(s)\n",
			cfg.Path, cfg.URL, cfg.Site, cfg.Role, cfg.AfterAccept, tags.Path, len(tags.Rules))
		return 0
	}
	if !*facts {
		if err := cfg.Validate(); err != nil {
			fmt.Fprintf(stderr, "tyr netbox: %v\nset it with N in the TUI or in %s\n", err, cfg.Path)
			return 3
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *test {
		cl := netbox.NewClient(cfg, nil)
		v, err := cl.Status(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "tyr netbox:", err)
			return 2
		}
		fmt.Fprintf(stdout, "NetBox %s: version %s, token accepted\n", cfg.URL, v)
		return 0
	}

	var in netbox.Input
	if *from != "" {
		in, err = loadInput(*from)
	} else {
		var env *collector.Env
		if env, _, err = c.env(); err == nil {
			if os.Geteuid() != 0 && c.fixtures == "" {
				fmt.Fprintln(stderr, "tyr netbox: not root: serial numbers, DIMMs, disks and the BMC may be missing")
			}
			in.Snap = collector.Collect(ctx, env, all.Collectors(), nil)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "tyr netbox:", err)
		return 3
	}
	if *facts {
		printFacts(stdout, in, tags)
		return 0
	}
	logf := func(f string, a ...any) { fmt.Fprintf(stdout, f+"\n", a...) }
	rep, err := netbox.Sync(ctx, cfg, tags, in, *dry, logf)
	if err != nil {
		fmt.Fprintln(stderr, "tyr netbox:", err)
		return 2
	}
	if len(rep.Problems) > 0 {
		return 1
	}
	return 0
}

// loadInput reads a snapshot file or an acceptance results directory
// (after.json and accept.json).
func loadInput(path string) (netbox.Input, error) {
	var in netbox.Input
	fi, err := os.Stat(path)
	if err != nil {
		return in, err
	}
	snapFile := path
	if fi.IsDir() {
		snapFile = filepath.Join(path, "after.json")
		var r accept.Result
		if err := readJSONFile(filepath.Join(path, "accept.json"), &r); err != nil {
			return in, fmt.Errorf("%s: not an acceptance results directory: %w", path, err)
		}
		in.Result, in.ResultDir = &r, path
	}
	var s model.Snapshot
	if err := readJSONFile(snapFile, &s); err != nil {
		return in, err
	}
	if s.SchemaVersion == "" || s.System == nil && s.CPU == nil {
		return in, fmt.Errorf("%s: not a Tyr snapshot", snapFile)
	}
	if len(s.Inventory) == 0 {
		s.Inventory = model.BuildInventory(&s)
	}
	in.Snap = &s
	return in, nil
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func printFacts(w io.Writer, in netbox.Input, tags netbox.Tags) {
	f := netbox.ComputeFacts(in.Snap, in.Result)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "FACT\tVALUE\tMEANS")
	for _, d := range netbox.FactDefs {
		v := []rune(f[d.Name].String())
		if len(v) > 60 {
			v = append(v[:59], '…')
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", d.Name, string(v), d.Help)
	}
	tw.Flush()
	fmt.Fprintln(w)
	if len(tags.Rules) == 0 {
		fmt.Fprintf(w, "no tag rules (%s): Tyr sets no tags\n", orNA(tags.Path))
		return
	}
	fmt.Fprintf(w, "tag rules (%s):\n", tags.Path)
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range tags.Evaluate(f) {
		m := "no"
		switch {
		case r.Err != nil:
			m = "error: " + r.Err.Error()
		case r.Match:
			m = "SET"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", r.Rule.Tag, m, r.Rule.When)
	}
	tw.Flush()
}

// acceptNetBox writes an acceptance result to NetBox when configured; it
// logs to netbox.log in the results directory and never changes the
// verdict.
func acceptNetBox(files netboxFiles, force, off bool, r *accept.Result, after *model.Snapshot, dir string, stdout io.Writer) {
	if off {
		return
	}
	cfg, tags, err := files.load()
	if err == nil && !cfg.Enabled() {
		if force {
			fmt.Fprintf(stdout, "\nNetBox: not configured (%s)\n", cfg.Path)
		}
		return
	}
	if err == nil && !cfg.AfterAccept && !force {
		return
	}
	fmt.Fprintln(stdout, "\n== NetBox")
	logPath := filepath.Join(dir, "netbox.log")
	lf, ferr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	logf := func(f string, a ...any) {
		line := fmt.Sprintf(f, a...)
		fmt.Fprintln(stdout, line)
		if ferr == nil {
			fmt.Fprintf(lf, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
		}
	}
	if ferr == nil {
		defer lf.Close()
	}
	if err != nil {
		logf("NetBox: %v", err)
		return
	}
	var rep *netbox.Report
	for attempt := 1; attempt <= 3; attempt++ { // the network may come up late on the ISO
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		rep, err = netbox.Sync(ctx, cfg, tags, netbox.Input{Snap: after, Result: r, ResultDir: dir}, false, logf)
		cancel()
		if err == nil || !netbox.Unreachable(err) || attempt == 3 {
			break // only a NetBox that cannot be reached is worth trying again
		}
		logf("NetBox: %v - again in 30 s", err)
		time.Sleep(30 * time.Second)
	}
	if err != nil {
		logf("NetBox: not written: %v", err)
		return
	}
	saveJSON(filepath.Join(dir, "netbox.json"), rep)
	if len(rep.Tags) > 0 {
		logf("NetBox tags: %s", strings.Join(rep.Tags, ", "))
	}
}
