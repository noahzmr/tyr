package stress

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/noahzmr/tyr/internal/affinity"
	"github.com/noahzmr/tyr/internal/cpuset"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/workload"
)

// EngOfflineCore tests a CPU that was taken offline: online only for the
// test, isolated from all other work, offline again afterwards.
const EngOfflineCore = "offline-core"

// WorkerArg is the hidden command that runs the kernels on the CPU under
// test (a child process, born inside the isolated cgroup).
const WorkerArg = "__core-worker"

// offlineCPUs lists the present but offline CPUs.
func offlineCPUs(env *Env) []int {
	if env.Baseline != nil && env.Baseline.CPU != nil {
		var out []int
		for _, l := range env.Baseline.CPU.Logical {
			if l.Present && !l.Online {
				out = append(out, l.ID)
			}
		}
		return out
	}
	if env.Collector == nil || env.Collector.FS == nil {
		return nil
	}
	set, _ := cpuset.Parse(hostfs.StringOr(env.Collector.FS, "/sys/devices/system/cpu/offline", ""))
	return set
}

// offlineTarget is the CPU to test: cfg.CPUs names one offline CPU.
func offlineTarget(cfg Config, env *Env) (int, string) {
	off := offlineCPUs(env)
	if len(off) == 0 {
		return -1, "no CPU is offline"
	}
	if cfg.CPUs == "" {
		if len(off) == 1 {
			return off[0], ""
		}
		return -1, "choose the offline CPU to test with --cpus (offline: " + cpuset.Set(off).String() + ")"
	}
	set, err := cpuset.Parse(cfg.CPUs)
	if err != nil || len(set) != 1 {
		return -1, "--cpus must name exactly one offline CPU (offline: " + cpuset.Set(off).String() + ")"
	}
	if !cpuset.Set(off).Contains(set[0]) {
		return -1, fmt.Sprintf("cpu%d is online; this test is for offline CPUs (offline: %s)", set[0], cpuset.Set(off).String())
	}
	return set[0], ""
}

type offlineCore struct{}

func (offlineCore) Plan(cfg Config, env *Env) *Plan { return offlineCorePlan(cfg, env, false) }

func offlineCorePlan(cfg Config, env *Env, sim bool) *Plan {
	p := &Plan{}
	cpu, why := offlineTarget(cfg, env)
	if why != "" {
		p.Blocked = why
		return p
	}
	name := "cpu" + strconv.Itoa(cpu)
	var l *model.LogicalCPU
	var c *model.CPU
	if env.Baseline != nil && env.Baseline.CPU != nil {
		c = env.Baseline.CPU
		for i := range c.Logical {
			if c.Logical[i].ID == cpu {
				l = &c.Logical[i]
			}
		}
	}
	scope := name + " (offline)"
	switch {
	case l != nil && l.SocketID >= 0 && l.CoreID >= 0:
		scope = fmt.Sprintf("%s (offline) · socket %d, core %d", name, l.SocketID, l.CoreID)
	case l != nil && l.SocketID >= 0:
		scope = fmt.Sprintf("%s (offline) · socket %d", name, l.SocketID)
	}
	p.Scope = []string{scope}
	if l != nil && l.Offline != nil && l.Offline.Sibling != nil {
		state := "stays offline"
		if l.Offline.SiblingOnline {
			state = "keeps running other work (the core is not fully out of service)"
		}
		p.Scope = append(p.Scope, fmt.Sprintf("its other thread cpu%d %s", *l.Offline.Sibling, state))
	}
	p.Impact = []string{
		fmt.Sprintf("%s goes online only for the test, in an exclusive cpuset partition (no other task runs there) with interrupts moved away; Tyr's kernels run on it for %s, every result checked.", name, fmtDur(cfg.Duration)),
		fmt.Sprintf("At the first wrong result or machine check the test stops; %s always goes offline again at the end, also on abort.", name),
	}
	p.Warnings = []string{
		"RISK: a defective core can raise a fatal machine check. The kernel then panics and the server reboots - isolation cannot prevent that. Run it in a maintenance window with workloads moved off this host.",
	}
	if l != nil && l.Offline != nil && l.Offline.Persistent == "" {
		p.Warnings = append(p.Warnings, fmt.Sprintf("Nothing takes %s offline at boot: after such a reboot it runs work again. Add a boot-time unit first (see docs/stress-tests.md).", name))
	}
	if c != nil {
		for _, cr := range c.CrashRecords {
			if cr.CPU == cpu {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s already crashed the kernel once (%s).", name, strings.TrimPrefix(cr.Reason, "Kernel panic - not syncing: ")))
				break
			}
		}
	}
	p.Info = []string{
		"Per-CPU kernel threads (kworker, ksoftirqd, migration) and managed interrupts (NVMe queues) also run on the CPU while it is online.",
		"No error during the test does not prove the core healthy: faults can be intermittent, or depend on temperature and voltage. The vendor's offline diagnostics test outside the OS.",
	}
	p.ConfirmToken = name
	if sim {
		p.Scope = append(p.Scope, "simulated: fixtures mode changes nothing")
		return p
	}
	switch {
	case os.Geteuid() != 0:
		p.Blocked = "needs root: run tyr with sudo"
	default:
		if why := isolationCheck(cpu); why != "" {
			p.Blocked = why
		}
	}
	return p
}

// workerMsg is one line from the worker.
type workerMsg struct {
	Checks   uint64 `json:"checks,omitempty"`
	CPU      int    `json:"cpu"`
	Error    string `json:"error,omitempty"`
	Kernel   string `json:"kernel,omitempty"`
	Expected string `json:"expected,omitempty"`
	Got      string `json:"got,omitempty"`
	Seed     uint64 `json:"seed,omitempty"`
}

func (offlineCore) Run(ctx context.Context, cfg Config, _ *Plan, rt Runtime) error {
	env := runEnv(ctx)
	cpu, why := offlineTarget(cfg, env)
	if why != "" {
		return fmt.Errorf("%s", why)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding the tyr binary for the worker: %w", err)
	}
	rt.Progress(0, fmt.Sprintf("isolating cpu%d", cpu))
	iso, err := isolate(cpu, rt.Logf)
	if err != nil {
		return err
	}
	defer func() {
		if err := iso.restore(rt.Logf); err != nil {
			rt.Logf("RESTORE FAILED: %v", err)
		}
	}()
	cg, err := os.Open(iso.Cgroup)
	if err != nil {
		return fmt.Errorf("opening cgroup: %w", err)
	}
	defer cg.Close()
	cmd := exec.Command(exe, WorkerArg, strconv.Itoa(cpu))
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(cg.Fd()), Pdeathsig: syscall.SIGKILL}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the worker in the isolated cgroup (needs Linux 5.7+, and a filesystem that allows exec for %s): %w", exe, err)
	}
	stop := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	msgs := make(chan workerMsg, 16)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			var m workerMsg
			if json.Unmarshal(sc.Bytes(), &m) != nil {
				m = workerMsg{Error: strings.TrimSpace(sc.Text())}
			}
			msgs <- m
		}
		close(msgs)
	}()
	mce0, mceOK := mceCount(cpu)
	start := time.Now()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var last uint64
	lastAt := start
	for {
		select {
		case <-ctx.Done():
			stop()
			return nil
		case m, ok := <-msgs:
			if !ok {
				stop()
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("the worker on cpu%d ended unexpectedly", cpu)
			}
			switch {
			case m.Kernel != "":
				rt.VerifyError(VerifyError{Subject: fmt.Sprintf("cpu%d", cpu), Workload: m.Kernel, Expected: m.Expected, Got: m.Got,
					Detail: fmt.Sprintf("seed %d · offline core, isolated", m.Seed)})
				stop()
				return nil
			case m.Error != "":
				stop()
				return fmt.Errorf("worker: %s", m.Error)
			default:
				if m.Checks > last {
					now := time.Now()
					rt.Metric("verified_kernels_per_s", "ops_per_second", float64(m.Checks-last)/now.Sub(lastAt).Seconds())
					last, lastAt = m.Checks, now
				}
			}
		case now := <-t.C:
			if n, ok := mceCount(cpu); ok && mceOK && n > mce0 {
				rt.VerifyError(VerifyError{Subject: fmt.Sprintf("cpu%d", cpu), Workload: "machine check",
					Detail: fmt.Sprintf("%d new machine check exceptions on cpu%d (/proc/interrupts MCE)", n-mce0, cpu)})
				stop()
				return nil
			}
			el := now.Sub(start)
			stage := fmt.Sprintf("cpu%d online, isolated · ", cpu)
			switch {
			case el < 3*time.Second:
				stage += "first pass"
			case el < 15*time.Second:
				stage += "short check"
			default:
				stage += "sustained"
			}
			rt.Progress(min(1, el.Seconds()/max(1, cfg.Duration.Seconds())), stage)
		}
	}
}

// mceCount is cpu's machine check exception count from /proc/interrupts.
func mceCount(cpu int) (uint64, bool) {
	b, err := os.ReadFile("/proc/interrupts")
	if err != nil {
		return 0, false
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) == 0 {
		return 0, false
	}
	col := -1
	for i, h := range strings.Fields(lines[0]) {
		if h == "CPU"+strconv.Itoa(cpu) {
			col = i
		}
	}
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		if len(f) > col+1 && col >= 0 && f[0] == "MCE:" {
			n, err := strconv.ParseUint(f[col+1], 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// CoreWorker runs in the child process on the CPU under test: every kernel
// with rotating seeds, each result checked; it reports progress as JSON
// lines and stops at the first wrong result.
func CoreWorker(cpu int, w io.Writer) int {
	enc := json.NewEncoder(w)
	if err := affinity.Pin(cpu); err != nil {
		_ = enc.Encode(workerMsg{CPU: cpu, Error: "pinning: " + err.Error()})
		return 3
	}
	if cur, err := affinity.Current(); err == nil && cur != cpu {
		_ = enc.Encode(workerMsg{CPU: cpu, Error: fmt.Sprintf("runs on cpu%d instead of cpu%d", cur, cpu)})
		return 3
	}
	var checks uint64
	last := time.Now()
	for iter := 0; ; iter++ {
		for _, k := range workload.Kernels {
			seed := uint64((cpu+iter)%workload.Seeds) + 1
			v := k.Run(seed)
			if err := workload.Check(k.Name, seed, v); err != nil {
				want, _ := workload.Expected(k.Name, seed)
				_ = enc.Encode(workerMsg{CPU: cpu, Kernel: k.Name, Seed: seed, Expected: fmt.Sprintf("%#016x", want), Got: fmt.Sprintf("%#016x", v)})
				return 2
			}
			checks++
		}
		if time.Since(last) >= 500*time.Millisecond {
			last = time.Now()
			if enc.Encode(workerMsg{CPU: cpu, Checks: checks}) != nil {
				return 3 // parent gone
			}
		}
	}
}
