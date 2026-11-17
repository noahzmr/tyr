package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/noahzmr/tyr/internal/collector/all"
	"github.com/noahzmr/tyr/internal/netpeer"
	"github.com/noahzmr/tyr/internal/netproto"
	"github.com/noahzmr/tyr/internal/stress"
)

// stressFlags are the options of `tyr stress`.
type stressFlags struct {
	component, engine, cpus, mode, target, size, token, runDir, fft string
	duration                                                        time.Duration
	memPct, streams                                                 int
	reverse, yes, simError, suspect                                 bool
}

func runStress(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tyr stress", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.register(fs)
	var f stressFlags
	fs.StringVar(&f.component, "component", "", "cpu, memory, storage, network, or full (all of them, one after another)")
	fs.StringVar(&f.engine, "engine", stress.EngBuiltin, "builtin, torture (Prime95-style), stress-ng, mprime (cpu); memtester (memory); iperf3 (network)")
	fs.StringVar(&f.fft, "fft", "blend", "torture engine: small (in cache, most heat), large (memory), blend")
	fs.DurationVar(&f.duration, "duration", 5*time.Minute, "load duration per component")
	fs.StringVar(&f.cpus, "cpus", "", "CPU list to load, e.g. 0-7 (default: all online)")
	fs.IntVar(&f.memPct, "mem-percent", 70, "memory test size in percent of available memory, up to 100 (a reserve of max(1 GiB, 5 % of RAM) always stays free)")
	fs.StringVar(&f.mode, "mode", "read", "storage: read (read-only surface scan of a device) or file (write+verify a temporary file on a filesystem)")
	fs.StringVar(&f.target, "target", "", "storage: /dev/sdX or all (read), a mountpoint (file); network: peer host[:port]")
	fs.StringVar(&f.size, "size", "", "storage file test size, e.g. 4G (default min(4G, 10% of free space))")
	fs.StringVar(&f.token, "token", "", "network: token printed by `tyr net-peer`")
	fs.IntVar(&f.streams, "streams", 4, "network: parallel TCP streams")
	fs.BoolVar(&f.reverse, "reverse", false, "network: the peer sends, this host receives")
	fs.BoolVar(&f.yes, "yes", false, "start without the confirmation prompt")
	fs.BoolVar(&f.suspect, "include-suspect-cores", false, "cpu: also load online CPUs whose other thread is offline (left out by default)")
	fs.StringVar(&f.runDir, "run-dir", "", "where to write result files (default /var/lib/tyr/runs, else ./tyr-runs)")
	fs.BoolVar(&f.simError, "sim-error", false, "fixtures mode: inject one verification error")
	if err := fs.Parse(args); err != nil {
		return 3
	}
	cenv, _, err := c.env()
	if err != nil {
		fmt.Fprintln(stderr, "tyr:", err)
		return 3
	}
	var comps []stress.Component
	switch f.component {
	case "full":
		comps = stress.Components
	case "":
		fmt.Fprintln(stderr, "tyr stress: choose --component cpu, memory, storage, network or full")
		return 3
	default:
		comps = []stress.Component{stress.Component(f.component)}
	}
	size, err := parseSize(f.size)
	if err != nil {
		fmt.Fprintln(stderr, "tyr stress: --size:", err)
		return 3
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	env := &stress.Env{Collector: cenv, Cols: all.Collectors(), RunDir: f.runDir}
	in := bufio.NewReader(stdin)
	worst := -1
	var lines []string
	for _, comp := range comps {
		cfg := stress.Config{Component: comp, Engine: f.engine, Duration: f.duration, CPUs: f.cpus, MemoryPercent: f.memPct,
			StorageMode: f.mode, Target: f.target, SizeBytes: size, Token: f.token, Streams: f.streams, Reverse: f.reverse, SimError: f.simError,
			IncludeSuspect: f.suspect, FFT: f.fft}
		if len(comps) > 1 {
			cfg.Engine = stress.EngBuiltin
			if comp == stress.CompStorage && f.target == "" {
				cfg.Target, cfg.StorageMode = "all", "read"
			}
			if comp == stress.CompNetwork && f.target == "" {
				fmt.Fprintln(stdout, "\n== network: skipped (no --target peer; run `tyr net-peer` on another host)")
				lines = append(lines, "network: SKIPPED (no peer)")
				continue
			}
		}
		if ctx.Err() != nil {
			break
		}
		code, line := stressOne(ctx, env, cfg, in, stdout, stderr, f.yes)
		lines = append(lines, line)
		worst = worseExit(worst, code)
		env.Baseline = nil // fresh baseline per component
	}
	if len(lines) > 1 {
		fmt.Fprintln(stdout, "\n== full test")
		for _, l := range lines {
			fmt.Fprintln(stdout, "  "+l)
		}
	}
	if worst < 0 {
		worst = 3
	}
	switch {
	case len(comps) > 1:
		verdict := map[int]string{0: "PASS", 1: "WARN", 2: "FAIL", 3: "INCOMPLETE"}[worst]
		fmt.Fprintf(stdout, "%s: full hardware test (%s)\n", verdict, strings.Join(lines, "; "))
	case len(lines) > 0:
		fmt.Fprintln(stdout, lines[len(lines)-1])
	}
	return worst
}

// worseExit orders stress exit codes FAIL(2) > ABORTED(3) > WARN(1) > PASS(0).
func worseExit(a, b int) int {
	rank := map[int]int{-1: -1, 0: 0, 1: 1, 3: 2, 2: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func stressOne(ctx context.Context, env *stress.Env, cfg stress.Config, in *bufio.Reader, stdout, stderr io.Writer, yes bool) (int, string) {
	code, line, _ := stressRun(ctx, env, cfg, in, stdout, stderr, yes)
	return code, line
}

// stressRun runs one stress test with progress on stdout; the result is nil
// when the test did not start.
func stressRun(ctx context.Context, env *stress.Env, cfg stress.Config, in *bufio.Reader, stdout, stderr io.Writer, yes bool) (int, string, *stress.Result) {
	fmt.Fprintf(stdout, "\n== %s stress (%s), %s\n", cfg.Component, cfg.Engine, cfg.Duration)
	fmt.Fprintln(stdout, "collecting baseline …")
	plan, test, err := stress.Prepare(ctx, env, cfg)
	if err != nil {
		fmt.Fprintln(stderr, "tyr stress:", err)
		return 3, fmt.Sprintf("%s: could not run (%v)", cfg.Component, err), nil
	}
	printPlan(stdout, plan)
	if plan.Blocked != "" {
		fmt.Fprintln(stdout, "cannot run: "+plan.Blocked)
		return 3, fmt.Sprintf("%s: could not run (%s)", cfg.Component, plan.Blocked), nil
	}
	if !yes {
		want := "yes"
		if plan.ConfirmToken != "" {
			want = plan.ConfirmToken
		}
		if !isTTY(os.Stdin) {
			fmt.Fprintln(stderr, "tyr stress: not a terminal; pass --yes to start without confirmation")
			return 3, fmt.Sprintf("%s: not confirmed", cfg.Component), nil
		}
		fmt.Fprintf(stdout, "Type %q to start: ", want)
		line, _ := in.ReadString('\n')
		if strings.TrimSpace(line) != want {
			fmt.Fprintln(stdout, "not started")
			return 3, fmt.Sprintf("%s: not started", cfg.Component), nil
		}
	}
	r, err := stress.Start(ctx, env, cfg, plan, test)
	if err != nil {
		fmt.Fprintln(stderr, "tyr stress:", err)
		return 3, fmt.Sprintf("%s: could not run (%v)", cfg.Component, err), nil
	}
	go func() {
		<-ctx.Done()
		r.Abort("interrupted (Ctrl+C)")
	}()
	lastLine := time.Now()
	for e := range r.Events() {
		switch e.Kind {
		case stress.EvPhase:
			fmt.Fprintf(stdout, "phase: %s\n", e.Phase)
		case stress.EvVerify:
			v := e.Verify
			fmt.Fprintf(stdout, "VERIFY ERROR %s %s: %s %s\n", v.Subject, v.Workload, strings.TrimSpace(v.Expected+" → "+v.Got), v.Detail)
		case stress.EvGuard:
			fmt.Fprintf(stdout, "GUARD: %s — stopping\n", e.Text)
		case stress.EvSample:
			if time.Since(lastLine) >= 10*time.Second {
				lastLine = time.Now()
				p, stage := r.Status()
				fmt.Fprintf(stdout, "%5.1f%%  %s  %s\n", p*100, time.Since(r.Start).Round(time.Second), stage)
			}
		}
	}
	res := r.Wait()
	fmt.Fprintf(stdout, "verdict: %s\n", res.Verdict)
	for _, why := range res.Reasons {
		fmt.Fprintln(stdout, "  - "+why)
	}
	for _, m := range res.Metrics {
		fmt.Fprintf(stdout, "  %-28s min %-10s avg %-10s max %s\n", m.Name, fmtMetric(m.Min, m.Unit), fmtMetric(m.Avg, m.Unit), fmtMetric(m.Max, m.Unit))
	}
	if res.File != "" {
		fmt.Fprintln(stdout, "result: "+res.File)
	}
	return res.Verdict.ExitCode(), res.SummaryLine(), res
}

func printPlan(w io.Writer, p *stress.Plan) {
	for _, s := range p.Scope {
		fmt.Fprintln(w, "scope:   "+s)
	}
	for _, s := range p.Impact {
		fmt.Fprintln(w, "impact:  "+s)
	}
	for _, s := range p.Warnings {
		fmt.Fprintln(w, "WARNING: "+s)
	}
	for _, s := range p.Info {
		fmt.Fprintln(w, "note:    "+s)
	}
}

func fmtMetric(v float64, unit string) string {
	switch unit {
	case "bytes_per_second":
		return fmt.Sprintf("%.1fMB/s", v/1e6)
	case "bits_per_second":
		return fmt.Sprintf("%.2fGb/s", v/1e9)
	case "percent":
		return fmt.Sprintf("%.0f%%", v)
	case "celsius":
		return fmt.Sprintf("%.0f°C", v)
	case "mhz":
		return fmt.Sprintf("%.0fMHz", v)
	case "ms":
		return fmt.Sprintf("%.1fms", v)
	}
	return strconv.FormatFloat(v, 'f', 0, 64)
}

func parseSize(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	mult := uint64(1)
	switch strings.ToUpper(s[len(s)-1:]) {
	case "K":
		mult = 1 << 10
	case "M":
		mult = 1 << 20
	case "G":
		mult = 1 << 30
	case "T":
		mult = 1 << 40
	}
	if mult > 1 {
		s = s[:len(s)-1]
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return v * mult, nil
}

func runNetPeer(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tyr net-peer", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", fmt.Sprintf(":%d", netproto.DefaultPort), "address to listen on")
	token := fs.String("token", "", "token clients must send (default: a random one, printed at start)")
	idle := fs.Duration("idle", 10*time.Minute, "exit after this long without a test session")
	if err := fs.Parse(args); err != nil {
		return 3
	}
	if *token == "" {
		*token = netpeer.NewToken()
	}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, "tyr net-peer:", err)
		return 3
	}
	fmt.Fprintf(stdout, "tyr net-peer listening on %s (one session at a time, exits after %v idle)\n", l.Addr(), *idle)
	fmt.Fprintf(stdout, "token: %s\n", *token)
	fmt.Fprintf(stdout, "on the host to test: tyr stress --component network --target <this-host>:%d --token %s\n", l.Addr().(*net.TCPAddr).Port, *token)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv := &netpeer.Server{Token: *token, IdleTimeout: *idle, Logf: func(format string, a ...any) {
		fmt.Fprintf(stderr, time.Now().Format("15:04:05 ")+format+"\n", a...)
	}}
	if err := srv.Serve(ctx, l); err != nil {
		fmt.Fprintln(stderr, "tyr net-peer:", err)
		return 3
	}
	return 0
}

// runCoreTest is `tyr core-test cpu7`: the offline-core stress engine for
// one CPU, or --restore after a test that was killed.
func runCoreTest(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tyr core-test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: tyr core-test [flags] CPU     e.g. tyr core-test cpu7\n       tyr core-test --restore\n\nTests an offline CPU: online only for the test, in an exclusive cpuset partition,\nevery result checked, offline again afterwards. A defective core can still crash\nthe kernel: use a maintenance window.")
		fs.PrintDefaults()
	}
	var c common
	c.register(fs)
	duration := fs.Duration("duration", 2*time.Minute, "how long the CPU runs the kernels")
	yes := fs.Bool("yes", false, "start without typing the CPU name")
	restore := fs.Bool("restore", false, "take the CPU offline again and undo the isolation of a test that did not finish")
	runDir := fs.String("run-dir", "", "where to write the result file (default /var/lib/tyr/runs, else ./tyr-runs)")
	// flags may follow the CPU
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return 3
		}
		if fs.NArg() == 0 {
			break
		}
		pos, args = append(pos, fs.Arg(0)), fs.Args()[1:]
	}
	if *restore {
		did, err := stress.RestoreOfflineCore(func(f string, a ...any) { fmt.Fprintf(stdout, f+"\n", a...) })
		switch {
		case err != nil:
			fmt.Fprintln(stderr, "tyr core-test --restore:", err)
			return 2
		case !did:
			fmt.Fprintln(stdout, "nothing to restore")
		default:
			fmt.Fprintln(stdout, "restored")
		}
		return 0
	}
	if len(pos) != 1 {
		fs.Usage()
		return 3
	}
	cpu := strings.TrimPrefix(strings.ToLower(pos[0]), "cpu")
	if _, err := strconv.Atoi(cpu); err != nil {
		fmt.Fprintf(stderr, "tyr core-test: %q is not a CPU (e.g. cpu7)\n", pos[0])
		return 3
	}
	cenv, _, err := c.env()
	if err != nil {
		fmt.Fprintln(stderr, "tyr:", err)
		return 3
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	env := &stress.Env{Collector: cenv, Cols: all.Collectors(), RunDir: *runDir}
	cfg := stress.Config{Component: stress.CompCPU, Engine: stress.EngOfflineCore, Duration: *duration, CPUs: cpu}
	code, line := stressOne(ctx, env, cfg, bufio.NewReader(stdin), stdout, stderr, *yes)
	fmt.Fprintln(stdout, line)
	return code
}
