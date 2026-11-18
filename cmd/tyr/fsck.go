package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/noahzmr/tyr/internal/fsck"
	"github.com/noahzmr/tyr/internal/hostfs"
)

// runFsck lists, checks or repairs filesystems. Exit status: 0 clean or
// repaired, 1 errors remain or the run failed, 2 blocked, 3 usage.
func runFsck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fsck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repair := fs.Bool("repair", false, "repair instead of check: unmounts (when nothing uses it), repairs, mounts again")
	confirm := fs.String("confirm", "", "the device path, to repair without the prompt (--repair --confirm /dev/sdb1)")
	yes := fs.Bool("yes", false, "unmount for a check without asking")
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: tyr fsck [--repair] [DEVICE | MOUNTPOINT]

Without an argument it lists the filesystems Tyr can check. A check only
reads; a mounted ext4 / vfat / exfat is unmounted for it (when nothing uses
it) and mounted again, a mounted btrfs or xfs is checked online. A repair
writes and asks for the device path first; it never runs from the live ISO.
The output is also written to /var/lib/tyr/fsck/.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 3
	}
	f := hostfs.Host()
	targets := fsck.Targets(f)
	if fs.NArg() == 0 {
		for _, t := range targets {
			where := "not mounted"
			if t.Mounted() {
				var ps []string
				for _, m := range t.Mounts {
					ps = append(ps, m.Point)
				}
				where = strings.Join(ps, ", ")
			}
			extra := ""
			if t.Longhorn != "" {
				extra = "  Longhorn " + t.Longhorn
			}
			fmt.Fprintf(stdout, "%-28s %-6s %10s  %-14s %s%s\n", t.Path, t.FSType, sizeStr(t.SizeBytes), t.Label, where, extra)
		}
		return 0
	}
	arg := fs.Arg(0)
	var tg *fsck.Target
	for i, t := range targets {
		if t.Path == arg || "/dev/"+t.Dev == arg || t.Dev == arg {
			tg = &targets[i]
		}
		for _, m := range t.Mounts {
			if m.Point == arg {
				tg = &targets[i]
			}
		}
	}
	if tg == nil {
		fmt.Fprintf(stderr, "tyr fsck: %s: no filesystem Tyr can check (tyr fsck lists them)\n", arg)
		return 3
	}
	if os.Geteuid() == 0 && strings.HasPrefix(tg.FSType, "ext") {
		tg.Ext, _ = fsck.ReadExtSuper(f, tg.Path)
	}
	mode := fsck.Check
	if *repair {
		mode = fsck.Repair
	}
	p := fsck.MakePlan(*tg, mode, fsck.HostSystem(f))
	fmt.Fprintf(stdout, "%s %s (%s)\n", strings.ToUpper(mode.String()[:1])+mode.String()[1:], tg.Path, tg.FSType)
	if e := tg.Ext; e != nil {
		fmt.Fprintf(stdout, "  superblock: %s, %d errors recorded, last checked %s\n", e.State, e.Errors, e.LastCheck.Format("2006-01-02"))
	}
	for _, s := range p.Steps {
		fmt.Fprintln(stdout, "  $", s)
	}
	for _, n := range p.Notes {
		fmt.Fprintln(stdout, "  note:", n)
	}
	if !p.OK() {
		for _, b := range p.Blockers {
			fmt.Fprintln(stderr, "  cannot run:", b)
		}
		return 2
	}
	switch p.Confirm {
	case "y":
		if !*yes && !ask(stdout, "Unmount it for the check and mount it again afterwards? [y/N] ", "y") {
			fmt.Fprintln(stdout, "not started")
			return 2
		}
	case "":
	default:
		if *confirm != p.Confirm && !ask(stdout, fmt.Sprintf("A repair writes to the filesystem. Type %s to start: ", p.Confirm), p.Confirm) {
			fmt.Fprintln(stdout, "not started")
			return 2
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	res := fsck.Run(ctx, p, func(e fsck.Event) {
		if e.Line != "" {
			fmt.Fprintln(stdout, e.Line)
		}
	})
	fmt.Fprintf(stdout, "\n%s: %s (%s)\n", strings.ToUpper(res.State), res.Text, res.Took.Round(1e9))
	if res.Log != "" {
		fmt.Fprintln(stdout, "log:", res.Log)
	}
	switch res.State {
	case "clean", "fixed":
		return 0
	}
	return 1
}

func ask(w io.Writer, prompt, want string) bool {
	if !isTTY(os.Stdin) {
		return false
	}
	fmt.Fprint(w, prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), want)
}

func sizeStr(b uint64) string {
	switch {
	case b >= 1e12:
		return fmt.Sprintf("%.1f TB", float64(b)/1e12)
	case b >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(b)/1e9)
	}
	return fmt.Sprintf("%.0f MB", float64(b)/1e6)
}
