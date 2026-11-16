package stress

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/cpuset"
)

// Isolation of one offline CPU for the offline-core test: an isolated
// cgroup v2 cpuset partition keeps every other task off it, interrupt
// affinities are moved away, and the CPU goes online only for the test.
// Every change is written to a state file first, so a restore (also after
// Tyr was killed) puts everything back and takes the CPU offline again.

// Paths, variables for tests.
var (
	sysCPU      = "/sys/devices/system/cpu"
	cgroupRoot  = "/sys/fs/cgroup"
	procIRQ     = "/proc/irq"
	isoState    = "/run/tyr-offline-core.json"
	isoCgroup   = "tyr-offline-core"
	writeSysfs  = func(p, v string) error { return os.WriteFile(p, []byte(v), 0o644) }
	removeCgDir = os.Remove
)

type isolation struct {
	CPU           int               `json:"cpu"`
	Started       time.Time         `json:"started"`
	EnabledCpuset bool              `json:"enabled_cpuset,omitempty"`
	Cgroup        string            `json:"cgroup,omitempty"`
	DefaultIRQ    string            `json:"default_smp_affinity,omitempty"`
	IRQs          map[string]string `json:"irqs,omitempty"` // irq -> original smp_affinity_list
	Online        bool              `json:"online,omitempty"`
	Partition     string            `json:"partition,omitempty"`
	Managed       int               `json:"-"` // interrupts the kernel keeps per CPU
}

func (s *isolation) save() error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(isoState, b, 0o600)
}

func readTrim(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

// isolationCheck says why the CPU cannot be isolated here ("" = it can).
func isolationCheck(cpu int) string {
	if _, err := os.Stat(isoState); err == nil {
		return "a previous offline-core test did not clean up (" + isoState + "): run `tyr core-test --restore` first"
	}
	if _, err := os.Stat(fmt.Sprintf("%s/cpu%d/online", sysCPU, cpu)); err != nil {
		return fmt.Sprintf("cpu%d cannot be brought online by the kernel (no %s/cpu%d/online)", cpu, sysCPU, cpu)
	}
	if !strings.Contains(" "+readTrim(cgroupRoot+"/cgroup.controllers")+" ", " cpuset ") {
		return "no cgroup v2 cpuset controller: the CPU cannot be isolated from other work, so Tyr does not bring it online"
	}
	return ""
}

// isolate prepares the partition and interrupts, then brings cpu online.
func isolate(cpu int, logf func(string, ...any)) (*isolation, error) {
	s := &isolation{CPU: cpu, Started: time.Now(), IRQs: map[string]string{}}
	if why := isolationCheck(cpu); why != "" {
		return nil, errors.New(why)
	}
	if err := s.save(); err != nil {
		return nil, fmt.Errorf("state file %s: %w", isoState, err)
	}
	fail := func(err error) (*isolation, error) {
		if rerr := s.restore(logf); rerr != nil {
			err = fmt.Errorf("%w; restore: %v", err, rerr)
		}
		return nil, err
	}
	if !strings.Contains(" "+readTrim(cgroupRoot+"/cgroup.subtree_control")+" ", " cpuset ") {
		if err := writeSysfs(cgroupRoot+"/cgroup.subtree_control", "+cpuset"); err != nil {
			return fail(fmt.Errorf("enabling the cpuset controller: %w", err))
		}
		s.EnabledCpuset = true
		_ = s.save()
	}
	dir := filepath.Join(cgroupRoot, isoCgroup)
	_ = removeCgDir(dir) // left over and empty
	if err := os.Mkdir(dir, 0o755); err != nil {
		return fail(fmt.Errorf("creating cgroup %s: %w", dir, err))
	}
	s.Cgroup = dir
	_ = s.save()
	if err := writeSysfs(dir+"/cpuset.cpus", strconv.Itoa(cpu)); err != nil {
		return fail(fmt.Errorf("cpuset.cpus: %w", err))
	}

	// interrupts: new ones and every movable one away from cpu
	if def := readTrim(procIRQ + "/default_smp_affinity"); def != "" {
		if m, ok := maskWithout(def, cpu); ok && m != def {
			if err := writeSysfs(procIRQ+"/default_smp_affinity", m); err == nil {
				s.DefaultIRQ = def
			}
		}
	}
	irqs, _ := filepath.Glob(procIRQ + "/*/smp_affinity_list")
	for _, p := range irqs {
		cur := readTrim(p)
		set, err := cpuset.Parse(cur)
		if err != nil || !set.Contains(cpu) || len(set) < 2 {
			continue
		}
		if err := writeSysfs(p, set.Minus(cpuset.Set{cpu}).String()); err != nil {
			s.Managed++ // managed (per-CPU queue) interrupts cannot be moved
			continue
		}
		s.IRQs[filepath.Base(filepath.Dir(p))] = cur
	}
	_ = s.save()
	logf("interrupts: %d moved off cpu%d, %d managed by the kernel stay per CPU", len(s.IRQs), cpu, s.Managed)

	// online, then make the partition exclusive: other tasks leave cpu
	s.Online = true
	_ = s.save()
	if err := writeSysfs(fmt.Sprintf("%s/cpu%d/online", sysCPU, cpu), "1"); err != nil {
		return fail(fmt.Errorf("bringing cpu%d online: %w", cpu, err))
	}
	logf("cpu%d is online", cpu)
	for _, mode := range []string{"isolated", "root"} {
		if err := writeSysfs(dir+"/cpuset.cpus.partition", mode); err != nil {
			continue
		}
		if got := readTrim(dir + "/cpuset.cpus.partition"); strings.HasPrefix(got, mode) && !strings.Contains(got, "invalid") {
			s.Partition = mode
			break
		}
	}
	_ = s.save()
	if s.Partition == "" {
		return fail(fmt.Errorf("the kernel refused an exclusive cpuset partition for cpu%d (%s)", cpu, readTrim(dir+"/cpuset.cpus.partition")))
	}
	logf("cpu%d is an %s cpuset partition: no other task runs there", cpu, s.Partition)
	return s, nil
}

// restore takes the CPU offline first, then undoes the rest in reverse.
func (s *isolation) restore(logf func(string, ...any)) error {
	var errs []error
	if s.Online {
		p := fmt.Sprintf("%s/cpu%d/online", sysCPU, s.CPU)
		var err error
		for range 5 {
			if err = writeSysfs(p, "0"); err == nil || readTrim(p) == "0" {
				err = nil
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("taking cpu%d offline again FAILED: %w - run: echo 0 > %s", s.CPU, err, p))
		} else {
			logf("cpu%d is offline again", s.CPU)
		}
	}
	if s.Cgroup != "" {
		_ = writeSysfs(s.Cgroup+"/cpuset.cpus.partition", "member")
		if err := removeCgDir(s.Cgroup); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("removing cgroup %s: %w", s.Cgroup, err))
		}
	}
	for irq, list := range s.IRQs {
		_ = writeSysfs(filepath.Join(procIRQ, irq, "smp_affinity_list"), list)
	}
	if s.DefaultIRQ != "" {
		_ = writeSysfs(procIRQ+"/default_smp_affinity", s.DefaultIRQ)
	}
	if s.EnabledCpuset {
		_ = writeSysfs(cgroupRoot+"/cgroup.subtree_control", "-cpuset") // fails harmlessly if others use it
	}
	if len(errs) == 0 {
		_ = os.Remove(isoState)
	}
	return errors.Join(errs...)
}

// OfflineCorePending reports a test that did not clean up, and its CPU.
func OfflineCorePending() (int, bool) {
	b, err := os.ReadFile(isoState)
	if err != nil {
		return -1, false
	}
	var s isolation
	if json.Unmarshal(b, &s) != nil {
		return -1, true
	}
	return s.CPU, true
}

// RestoreOfflineCore undoes an offline-core test that did not finish (Tyr
// was killed): the CPU goes offline again, the rest is put back.
func RestoreOfflineCore(logf func(string, ...any)) (bool, error) {
	b, err := os.ReadFile(isoState)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var s isolation
	if err := json.Unmarshal(b, &s); err != nil {
		return false, fmt.Errorf("%s: %w", isoState, err)
	}
	return true, s.restore(logf)
}

// maskWithout clears cpu in a kernel CPU mask ("ffffffff,ffffffff"),
// keeping the number of 32-bit groups.
func maskWithout(mask string, cpu int) (string, bool) {
	groups := strings.Split(mask, ",")
	g := len(groups) - 1 - cpu/32
	if g < 0 {
		return mask, true // not in the mask at all
	}
	v, err := strconv.ParseUint(groups[g], 16, 32)
	if err != nil {
		return mask, false
	}
	v &^= 1 << (cpu % 32)
	groups[g] = fmt.Sprintf("%0*x", len(groups[g]), v)
	return strings.Join(groups, ","), true
}
