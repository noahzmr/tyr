package fsck

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/noahzmr/tyr/internal/execx"
	"github.com/noahzmr/tyr/internal/hostfs"
)

// HostSystem describes the running host for MakePlan.
func HostSystem(f hostfs.FS) System {
	r := execx.Host()
	sys := System{Root: os.Geteuid() == 0, Live: IsLive(f), LookPath: r.LookPath, Mounts: ReadMounts(f), Users: users}
	if b, err := f.ReadFile("/proc/swaps"); err == nil {
		for _, l := range strings.Split(string(b), "\n")[1:] {
			if x := strings.Fields(l); len(x) > 1 && x[1] == "file" {
				sys.Swaps = append(sys.Swaps, unescape(x[0]))
			}
		}
	}
	bf, _ := f.Glob("/sys/block/loop*/loop/backing_file")
	for _, p := range bf {
		if s := hostfs.StringOr(f, p, ""); s != "" {
			sys.Loops = append(sys.Loops, strings.TrimSuffix(s, " (deleted)"))
		}
	}
	return sys
}

// users finds processes with their working directory, root, executable,
// an open file or a mapped file below one of the paths (like fuser -m).
func users(paths []string) []User {
	self := os.Getpid()
	procs, _ := filepath.Glob("/proc/[0-9]*")
	seen := map[int]bool{}
	var out []User
	for _, pd := range procs {
		pid, err := strconv.Atoi(filepath.Base(pd))
		if err != nil || pid == self {
			continue
		}
		hit := func(p string) bool {
			p = strings.TrimSuffix(p, " (deleted)")
			if !strings.HasPrefix(p, "/") || !under(p, paths) || seen[pid] {
				return false
			}
			comm, _ := os.ReadFile(pd + "/comm")
			out = append(out, User{PID: pid, Comm: strings.TrimSpace(string(comm)), Path: p})
			seen[pid] = true
			return true
		}
		for _, l := range []string{"cwd", "root", "exe"} {
			if p, err := os.Readlink(pd + "/" + l); err == nil && l != "root" || err == nil && p != "/" {
				if hit(p) {
					break
				}
			}
		}
		if seen[pid] {
			continue
		}
		fds, _ := os.ReadDir(pd + "/fd")
		for _, fd := range fds {
			if p, err := os.Readlink(pd + "/fd/" + fd.Name()); err == nil && hit(p) {
				break
			}
		}
		if seen[pid] {
			continue
		}
		if b, err := os.ReadFile(pd + "/maps"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if i := strings.Index(line, " /"); i >= 0 && hit(strings.TrimSpace(line[i:])) {
					break
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// Event is one line of output or the end of a step.
type Event struct {
	Step int    // index into Plan.Steps
	Line string // a line of the tool's output ("" at the end of a step)
	Done bool
	Code int
	Err  error
}

// Result is how a plan ended.
type Result struct {
	State     string // clean, fixed, errors, failed, cancelled, blocked
	Text      string
	Unmounted bool   // a repair failed: the filesystem was left unmounted
	Log       string // the log file
	Started   time.Time
	Took      time.Duration
}

// Verdict reads the exit code of a check or repair tool.
func Verdict(fstype string, s Step, code int) (state, text string) {
	tool := filepath.Base(s.Argv[0])
	check := s.Kind == "check"
	switch {
	case tool == "e2fsck" || tool == "fsck.exfat":
		switch {
		case code == 0:
			return "clean", "no errors"
		case code&8 != 0 || code&16 != 0 || code&128 != 0:
			return "failed", fmt.Sprintf("%s could not run (exit %d): see the output", tool, code)
		case code&32 != 0:
			return "cancelled", "cancelled"
		case code&4 != 0 && check:
			return "errors", "errors found: the filesystem needs a repair"
		case code&4 != 0:
			return "errors", "errors left uncorrected: see the output; restore from backup if a second repair does not fix them"
		case code&3 != 0:
			return "fixed", "errors corrected"
		}
	case tool == "xfs_repair":
		switch code {
		case 0:
			if check {
				return "clean", "no errors"
			}
			return "fixed", "repair finished, no errors left"
		case 1:
			if check {
				return "errors", "errors found: the filesystem needs a repair"
			}
			return "errors", "xfs_repair could not fix everything: see the output"
		case 2:
			return "errors", "the log is dirty: mount and unmount the filesystem once to replay it, then run again (Tyr never zeroes the log)"
		}
		return "failed", fmt.Sprintf("xfs_repair failed (exit %d)", code)
	case tool == "xfs_scrub":
		switch {
		case code == 0:
			return "clean", "no errors"
		case code&4 != 0 || code&8 != 0:
			return "failed", fmt.Sprintf("xfs_scrub could not run (exit %d): the kernel may lack online scrub", code)
		case code&1 != 0:
			return "errors", "corruption found: unmount and repair it (xfs_repair)"
		}
		return "clean", "no errors; xfs_scrub suggests optimizations"
	case tool == "btrfs" && len(s.Argv) > 1 && s.Argv[1] == "scrub":
		switch code {
		case 0:
			if check {
				return "clean", "no errors"
			}
			return "fixed", "scrub finished: errors it found are corrected (see the summary)"
		case 3:
			return "errors", "uncorrectable errors: there was no good copy (see the summary); restore the affected files from backup"
		}
		return "failed", fmt.Sprintf("btrfs scrub failed (exit %d)", code)
	case tool == "btrfs":
		if code == 0 {
			return "clean", "no errors"
		}
		return "errors", "errors found: see the output; mount it and run a scrub, or restore from backup"
	case tool == "fsck.vfat":
		switch code {
		case 0:
			return "clean", "no errors"
		case 1:
			if check {
				return "errors", "errors found: the filesystem needs a repair"
			}
			return "fixed", "errors corrected"
		}
		return "failed", fmt.Sprintf("fsck.vfat failed (exit %d)", code)
	}
	if code == 0 {
		return "clean", "finished"
	}
	return "failed", fmt.Sprintf("exit status %d", code)
}

// OpenLog creates the log file of a run: /var/lib/tyr/fsck, else ./tyr-fsck.
func OpenLog(dev string, at time.Time) (*os.File, error) {
	name := at.UTC().Format("20060102T150405Z") + "-" + strings.ReplaceAll(dev, "/", "_") + ".log"
	var lastErr error
	for _, dir := range []string{"/var/lib/tyr/fsck", "tyr-fsck"} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			lastErr = err
			continue
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
		if err == nil {
			return f, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// Run executes a plan. A check stops when ctx is cancelled (the filesystem
// is mounted again); a repair, once started, always runs to its end.
// emit receives every output line and the end of every step.
func Run(ctx context.Context, p Plan, emit func(Event)) Result {
	res := Result{Started: time.Now()}
	defer func() { res.Took = time.Since(res.Started) }()
	if !p.OK() {
		res.State, res.Text = "blocked", strings.Join(p.Blockers, "; ")
		return res
	}
	var log io.Writer = io.Discard
	if f, err := OpenLog(p.Target.Dev, res.Started); err == nil {
		defer f.Close()
		log, res.Log = f, f.Name()
	}
	fmt.Fprintf(log, "tyr %s of %s (%s) at %s\n", p.Mode, p.Target.Path, p.Target.FSType, res.Started.UTC().Format(time.RFC3339))
	for _, s := range p.Steps {
		fmt.Fprintf(log, "  $ %s\n", s)
	}
	out := func(e Event) {
		if e.Line != "" {
			fmt.Fprintln(log, e.Line)
		}
		if emit != nil {
			emit(e)
		}
	}
	unmounted := map[string]bool{}
	remountAll := func() {
		for i, s := range p.Steps {
			if s.Kind != "mount" || !unmounted[s.Argv[len(s.Argv)-1]] {
				continue
			}
			code, err := runStep(context.WithoutCancel(ctx), s, i, out)
			if err != nil || code != 0 {
				res.Text += fmt.Sprintf("; mounting %s again failed - mount it by hand: %s", s.Argv[len(s.Argv)-1], s)
			}
		}
	}
	for i, s := range p.Steps {
		switch s.Kind {
		case "umount":
			if ctx.Err() != nil {
				res.State, res.Text = "cancelled", "cancelled before the check"
				remountAll()
				return res
			}
			code, err := runStep(ctx, s, i, out)
			if err != nil || code != 0 {
				res.State, res.Text = "failed", fmt.Sprintf("could not unmount %s (busy?): nothing was checked", s.Argv[len(s.Argv)-1])
				remountAll()
				fmt.Fprintln(log, "result:", res.State, "-", res.Text)
				return res
			}
			unmounted[s.Argv[len(s.Argv)-1]] = true
		case "check", "repair":
			sctx := ctx
			if s.Kind == "repair" {
				sctx = context.WithoutCancel(ctx) // interrupting a repair can leave the filesystem worse
			}
			code, err := runStep(sctx, s, i, out)
			switch {
			case err != nil && ctx.Err() != nil:
				res.State, res.Text = "cancelled", "cancelled"
			case err != nil:
				res.State, res.Text = "failed", err.Error()
			default:
				res.State, res.Text = Verdict(p.Target.FSType, s, code)
			}
			if s.Kind == "repair" && (res.State == "failed" || res.State == "errors") && len(unmounted) > 0 && !p.Online {
				res.Unmounted = true
				res.Text += "; left unmounted so nothing writes to it before it is repaired: mount it again with " + remountHint(p)
				fmt.Fprintln(log, "result:", res.State, "-", res.Text)
				return res
			}
		case "mount":
			// after the loop
		}
	}
	remountAll()
	fmt.Fprintln(log, "result:", res.State, "-", res.Text)
	return res
}

func remountHint(p Plan) string {
	for _, s := range p.Steps {
		if s.Kind == "mount" {
			return s.String()
		}
	}
	return "mount"
}

// runStep runs one command with its output streamed line by line.
func runStep(ctx context.Context, s Step, i int, emit func(Event)) (int, error) {
	emit(Event{Step: i, Line: "$ " + s.String()})
	path, err := execx.Host().LookPath(s.Argv[0])
	if err != nil {
		emit(Event{Step: i, Done: true, Err: err})
		return -1, err
	}
	cmd := exec.CommandContext(ctx, path, s.Argv[1:]...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		emit(Event{Step: i, Done: true, Err: err})
		return -1, err
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		sc.Split(scanLinesCR)
		for sc.Scan() {
			if l := strings.TrimRight(sc.Text(), " "); l != "" {
				emit(Event{Step: i, Line: l})
			}
		}
		io.Copy(io.Discard, pr)
		close(done)
	}()
	err = cmd.Wait()
	pw.Close()
	<-done
	code := 0
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee) && ctx.Err() == nil:
		code, err = ee.ExitCode(), nil
	case err != nil:
		code = -1
	}
	emit(Event{Step: i, Done: true, Code: code, Err: err})
	return code, err
}

// scanLinesCR splits on \n and \r (progress lines rewrite themselves).
func scanLinesCR(data []byte, atEOF bool) (int, []byte, error) {
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
