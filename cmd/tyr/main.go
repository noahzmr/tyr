// Command tyr is a hardware inventory, health check and stress test tool.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/sys/unix"

	"github.com/noahzmr/tyr/internal/buildinfo"
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/collector/all"
	"github.com/noahzmr/tyr/internal/fixture"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/report"
	"github.com/noahzmr/tyr/internal/tui"
)

const usage = `Tyr - hardware inventory, health check and stress test

Usage:
  tyr [flags]            interactive TUI (default when stdout is a terminal)
  tyr report [flags]     non-interactive health report (text or JSON)
  tyr stress [flags]     stress test: --component cpu|memory|storage|network|full
  tyr net-peer [flags]   far end for the network stress test (run on another host)
  tyr history PATH...    history of hosts from saved JSON (snapshots, stress results)
  tyr view PATH...       show a report in the terminal: a .md file, one JSON document,
                         or several (their history), like glow
  tyr version

Run "tyr <command> -h" for the flags of a command.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "":
		if !isTTY(os.Stdout) {
			return runReport(args, stdout, stderr)
		}
		return runTUI(args, stderr)
	case "report":
		return runReport(args, stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "tyr %s (commit %s, built %s)\n", buildinfo.Version, orNA(buildinfo.Commit), orNA(buildinfo.Date))
		return 0
	case "stress":
		return runStress(args, os.Stdin, stdout, stderr)
	case "net-peer":
		return runNetPeer(args, stdout, stderr)
	case "history":
		return runHistory(args, stdout, stderr)
	case "view":
		return runView(args, stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "tyr: unknown command %q\n\n%s", cmd, usage)
		return 3
	}
}

func orNA(s string) string {
	if s == "" {
		return "n/a"
	}
	return s
}

func isTTY(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

// common holds flags shared by the TUI and report.
type common struct {
	fixtures   string
	opts       collector.Options
	version    bool
	warn, crit float64
	fs         *flag.FlagSet
}

func (c *common) register(fs *flag.FlagSet) {
	c.opts = collector.DefaultOptions()
	c.fs = fs
	fs.StringVar(&c.fixtures, "fixtures", "", "run against a fixture directory instead of this host (developer mode)")
	fs.BoolVar(&c.version, "version", false, "print version and exit")
	fs.DurationVar(&c.opts.Timeout, "timeout", c.opts.Timeout, "timeout per collector")
	fs.BoolVar(&c.opts.NoIPMI, "no-ipmi", false, "do not query the BMC")
	fs.BoolVar(&c.opts.NoDMI, "no-dmidecode", false, "do not run dmidecode")
	fs.BoolVar(&c.opts.NoSMART, "no-smart", false, "do not run smartctl/nvme")
	fs.BoolVar(&c.opts.NICAll, "nic-all", false, "include virtual network interfaces")
	fs.BoolVar(&c.opts.SELAll, "sel-all", false, "show all SEL entries, not only warnings and errors")
	fs.Float64Var(&c.warn, "warn-temp", c.opts.Thresholds.WarnTempC, "default warning temperature (°C) when a sensor has no own limit")
	fs.Float64Var(&c.crit, "crit-temp", c.opts.Thresholds.CritTempC, "critical temperature (°C) for sensors without their own limit; given explicitly it also replaces the built-in AMD CPU limits")
}

func (c *common) env() (*collector.Env, string, error) {
	c.opts.Thresholds.WarnTempC, c.opts.Thresholds.CritTempC = c.warn, c.crit
	if c.fs != nil {
		c.fs.Visit(func(f *flag.Flag) {
			if f.Name == "warn-temp" || f.Name == "crit-temp" {
				c.opts.Thresholds.Explicit = true
			}
		})
	}
	if c.warn >= c.crit {
		return nil, "", fmt.Errorf("--warn-temp (%.0f) must be below --crit-temp (%.0f)", c.warn, c.crit)
	}
	if c.fixtures == "" {
		return collector.NewHostEnv(c.opts), "", nil
	}
	env, _, err := fixture.Load(c.fixtures, c.opts)
	if err != nil {
		return nil, "", err
	}
	return env, filepath.Base(filepath.Clean(c.fixtures)), nil
}

func runTUI(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("tyr", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.register(fs)
	refresh := fs.Duration("refresh", 5*time.Second, "live sensor refresh interval")
	mouse := fs.Bool("mouse", false, "enable mouse: click tabs, wheel scrolls (hold Shift to select text)")
	if err := fs.Parse(args); err != nil {
		return 3
	}
	if c.version {
		return run([]string{"version"}, os.Stdout, stderr)
	}
	env, name, err := c.env()
	if err != nil {
		fmt.Fprintln(stderr, "tyr:", err)
		return 3
	}
	if name != "" {
		fixture.EnableLive(env)
	}
	m := tui.New(env, all.Collectors(), all.Implemented, tui.Options{Refresh: *refresh, FixtureName: name, Mouse: *mouse})
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fmt.Fprintln(stderr, "tyr:", err)
		return 3
	}
	return 0
}

func runReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tyr report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.register(fs)
	format := fs.String("format", "text", "output format: text, json or markdown")
	from := fs.String("from", "", "render a saved snapshot or stress result (JSON) instead of collecting from this host")
	section := fs.String("section", "all", "comma-separated modules: all, cpu, memory, storage, power, network, accel, events")
	fs.BoolVar(&c.opts.CoreTest, "core-test", false, "run the quick per-core test")
	fs.DurationVar(&c.opts.CoreTestTimeout, "core-test-duration", 2*time.Second, "core test timeout")
	inventory := fs.Bool("inventory", false, "print the hardware inventory (vendor, model, part number, serial, firmware) in the text report")
	if err := fs.Parse(args); err != nil {
		return 3
	}
	if c.version {
		return run([]string{"version"}, stdout, stderr)
	}
	if *format != "text" && *format != "json" && *format != "markdown" && *format != "md" {
		fmt.Fprintf(stderr, "tyr report: unknown --format %q\n", *format)
		return 3
	}
	if *from != "" {
		return reportFrom(*from, *format, stdout, stderr)
	}
	cols, err := selectSections(*section)
	if err != nil {
		fmt.Fprintln(stderr, "tyr report:", err)
		return 3
	}
	env, _, err := c.env()
	if err != nil {
		fmt.Fprintln(stderr, "tyr:", err)
		return 3
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	snap := collector.Collect(ctx, env, cols, nil)
	if errors.Is(ctx.Err(), context.Canceled) {
		fmt.Fprintln(stderr, "tyr: interrupted")
		return 3
	}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(snap); err != nil {
			fmt.Fprintln(stderr, "tyr:", err)
			return 3
		}
		return snap.Status.ExitCode()
	}
	if *format == "markdown" || *format == "md" {
		fmt.Fprint(stdout, report.Snapshot(snap))
		return snap.Status.ExitCode()
	}
	writeText(stdout, snap)
	if *inventory {
		writeInventory(stdout, snap)
	}
	return snap.Status.ExitCode()
}

func selectSections(s string) ([]collector.Collector, error) {
	if s == "" || s == "all" {
		return all.Collectors(), nil
	}
	var ids []model.ModuleID
	for _, p := range strings.Split(s, ",") {
		id := model.ModuleID(strings.TrimSpace(p))
		if !slices.Contains(model.Modules, id) {
			return nil, fmt.Errorf("unknown section %q", p)
		}
		if !all.Implemented(id) {
			return nil, fmt.Errorf("section %q is not implemented yet", id)
		}
		ids = append(ids, id)
	}
	return all.Select(ids), nil
}

// writeText is a compact interim text report; the full report is step 3.
func writeText(w io.Writer, s *model.Snapshot) {
	fmt.Fprintf(w, "Tyr %s  %s  %s\n", s.TyrVersion, s.Hostname, s.Timestamp.Format(time.RFC3339))
	if sys := s.System; sys != nil {
		fmt.Fprintf(w, "System: %s %s  serial %s  BIOS %s  kernel %s\n", sys.Vendor, sys.Product, orNA(sys.Serial), orNA(sys.BIOSVersion), orNA(sys.Kernel))
	}
	if !s.Privileged {
		fmt.Fprintln(w, "Note: not running as root; some data is unavailable (see 'skipped').")
	}
	fmt.Fprintln(w)
	for _, id := range append([]model.ModuleID{model.ModSystem}, model.Modules...) {
		sec := s.Section(id)
		if sec == nil {
			continue
		}
		h := sec.Head()
		fmt.Fprintf(w, "%-8s %-7s %s\n", strings.ToUpper(string(id)), h.Status, h.Summary)
		for _, f := range h.Findings {
			if f.Status != model.StatusOK {
				fmt.Fprintf(w, "         - %-4s %s\n", f.Status, f.Message)
			}
		}
		for _, src := range h.Sources {
			if src.State != model.AvailOK {
				fmt.Fprintf(w, "         skipped %s (%s): %s\n", src.Name, src.State, src.Reason)
			}
		}
	}
	fmt.Fprintln(w)
	line := s.Summary
	if line == "" {
		line = "all checks passed"
	}
	fmt.Fprintf(w, "%s - %s\n", s.Status.Nagios(), line)
}

// writeInventory prints all components as an aligned table.
func writeInventory(w io.Writer, s *model.Snapshot) {
	rows := [][]string{{"TYPE", "LOCATION", "VENDOR", "MODEL", "PART NUMBER", "SERIAL", "FIRMWARE", "DETAIL"}}
	for _, c := range s.Inventory {
		rows = append(rows, []string{c.Category, c.Location, c.Vendor, c.Model, c.PartNumber, c.Serial, c.Firmware, c.Detail})
	}
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], min(len([]rune(c)), 40))
		}
	}
	fmt.Fprintln(w, "\nInventory")
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			if rs := []rune(c); len(rs) > 40 {
				c = string(rs[:39]) + "…"
			}
			if c == "" {
				c = "-"
			}
			if i < len(r)-1 {
				fmt.Fprintf(&b, "%-*s  ", widths[i], c)
			} else {
				b.WriteString(c)
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	if !s.Privileged {
		fmt.Fprintln(w, "Note: SMBIOS, PCI VPD, PPIN and BMC serial numbers require root.")
	}
}
