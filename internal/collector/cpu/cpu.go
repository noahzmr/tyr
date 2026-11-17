// Package cpu collects CPU inventory and health: sockets, core liveness,
// utilization, frequency, temperatures, throttling, machine checks and the
// optional quick per-core test.
package cpu

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/noahzmr/tyr/internal/affinity"
	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/cpuset"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/dmi"
	"github.com/noahzmr/tyr/internal/source/kernellog"
	"github.com/noahzmr/tyr/internal/source/sysfs"
	"github.com/noahzmr/tyr/internal/workload"
)

// Collector is the CPU collector.
type Collector struct {
	// Corrupt is a test hook for the core test; nil in production.
	Corrupt Corrupt

	mu   sync.Mutex
	hist *staticHistory // machine checks of earlier boots, crash records
}

// New returns a CPU collector.
func New() *Collector { return &Collector{} }

func (*Collector) ID() model.ModuleID { return model.ModCPU }

// minUtilWindow is the minimum sampling window for utilization on a full
// collection (no previous sample available).
const minUtilWindow = 250 * time.Millisecond

func (c *Collector) Collect(ctx context.Context, env *collector.Env, prevSec model.Section) (model.Section, error) {
	var prev *model.CPU
	if p, ok := prevSec.(*model.CPU); ok && p != nil {
		prev = p
	}
	f := env.FS
	out := &model.CPU{Arch: env.Arch, VirtFlags: []string{}, Sockets: []model.CPUSocket{}, PackageTemps: []model.Sensor{}}
	h := &out.Header

	masks, err := sysfs.ReadCPUMasks(f)
	if err != nil {
		return nil, fmt.Errorf("reading CPU masks: %w", err)
	}
	h.SetSource("sysfs", model.AvailOK, "")
	out.Topology = model.CPUTopology{Present: masks.Present.String(), Possible: masks.Possible.String(), Online: masks.Online.String(), Offline: masks.Offline.String()}

	start := env.Now()
	stat1, statErr := readStat(f)

	// --- identity -----------------------------------------------------
	var ci sysfs.CPUInfo
	if b, err := f.ReadFile("/proc/cpuinfo"); err == nil {
		ci = sysfs.ParseCPUInfo(b)
	}
	out.Vendor, out.Model, out.Microcode = ci.Vendor, ci.Model, ci.Microcode
	if out.Microcode == "" {
		out.Microcode = hostfs.StringOr(f, "/sys/devices/system/cpu/cpu0/microcode/version", "")
	}
	out.VirtFlags = sysfs.VirtFlags(ci.Flags)

	// --- logical CPUs ---------------------------------------------------
	ids := union(masks.Present, masks.Online)
	interrupts := readInterrupts(f)
	logical := make([]model.LogicalCPU, 0, len(ids))
	var prevByID map[int]*model.LogicalCPU
	if prev != nil {
		prevByID = map[int]*model.LogicalCPU{}
		for i := range prev.Logical {
			prevByID[prev.Logical[i].ID] = &prev.Logical[i]
		}
	}
	policy := sysfs.ReadCPUPolicy(f)
	extras := map[int]sysfs.CPUExtra{}
	for _, id := range ids {
		extras[id] = sysfs.ReadCPUExtra(f, id)
	}
	tiers := coreTiers(extras)
	firstFreq := true
	for _, id := range ids {
		l := model.LogicalCPU{ID: id, Present: masks.Present.Contains(id), Online: masks.Online.Contains(id), CoreID: -1, SocketID: -1, NUMANode: -1}
		t := sysfs.ReadCPUTopo(f, id)
		l.CoreID, l.SocketID, l.NUMANode = t.CoreID, t.PackageID, t.Node
		if l.Online {
			if fr, ok := sysfs.ReadCPUFreq(f, id); ok {
				l.FreqMHz = fr.CurMHz
				if firstFreq {
					out.Governor, out.FreqDriver, out.BaseFreqMHz, out.MaxFreqMHz = fr.Governor, fr.Driver, fr.BaseMHz, fr.MaxMHz
					firstFreq = false
				}
			} else if mhz, ok := ci.MHz[id]; ok && mhz > 0 {
				l.FreqMHz = model.F(mhz)
			}
			if core, _, ok := sysfs.ReadThrottle(f, id); ok {
				l.ThrottleCount = model.U(core)
			} else if v, ok := interrupts["TRM"][id]; ok {
				l.ThrottleCount = model.U(v)
			}
			if v, ok := interrupts["MCE"][id]; ok {
				l.MCEExceptions = model.U(v)
			}
			l.IdleStates = idleStates(sysfs.ReadIdleStates(f, id))
		}
		x := extras[id]
		l.MinFreqMHz, l.MaxFreqMHz, l.Siblings, l.PrefRank = x.MinMHz, x.MaxMHz, x.Siblings, x.PrefRank
		l.CoreType = coreType(id, policy, tiers)
		l.Microcode = ci.MicrocodeByCPU[id]
		if prev != nil {
			if pl := prevByID[id]; pl != nil {
				l.CoreTest, l.CoreTestDetail = pl.CoreTest, pl.CoreTestDetail
				if l.CoreID < 0 { // offline now: keep last known topology
					l.CoreID, l.SocketID, l.NUMANode = pl.CoreID, pl.SocketID, pl.NUMANode
				}
			}
		}
		logical = append(logical, l)
	}
	out.Logical = logical
	c.details(out, f, ci, policy, prev)
	if statErr != nil {
		h.SetSource("proc_stat", model.AvailUnavailable, statErr.Error())
	}

	// --- sockets (sysfs + SMBIOS type 4) --------------------------------
	pkgIDs := packageIDs(logical)
	var procs []dmi.Processor
	if prev == nil {
		tables, st := env.DMI(ctx)
		h.Sources = append(h.Sources, st)
		if tables != nil {
			procs = tables.Processors()
		}
		out.Sockets = buildSockets(procs, pkgIDs, logical)
	} else {
		out.Sockets = refreshSockets(prev.Sockets, logical)
		if st, ok := prev.Source("dmidecode"); ok {
			h.Sources = append(h.Sources, st)
		}
	}
	if out.Model == "" {
		for _, p := range procs {
			if p.Version != "" {
				out.Model = p.Version
				break
			}
		}
		if out.Model == "" && prev != nil {
			out.Model = prev.Model
		}
	}
	// PPIN: one per package, read from its first online CPU (root only).
	for i := range out.Sockets {
		s := &out.Sockets[i]
		if s.ID < 0 || s.PPIN != "" {
			continue
		}
		for _, l := range logical {
			if l.Online && l.SocketID == s.ID {
				s.PPIN = sysfs.ReadPPIN(f, l.ID)
				break
			}
		}
	}
	out.SocketCount = len(pkgIDs)
	for _, s := range out.Sockets {
		if s.Populated != nil && *s.Populated && s.ID < 0 {
			out.SocketCount++ // populated but no online CPU: still a socket
		}
	}
	out.ThreadCount = len(masks.Present)
	out.CoreCount = countCores(logical)

	// --- temperatures -----------------------------------------------------
	temps := readTemps(f, pkgIDs, env.Opts.Thresholds, ci)
	if temps.source == "" && !env.IsVM() {
		// Linux has no CPU sensor: ask the BMC.
		sens, st := env.IPMISensors(ctx)
		if sens != nil {
			ipmiTemps(&temps, sens, pkgIDs, env.Opts.Thresholds)
		} else {
			h.Sources = append(h.Sources, st)
		}
	}
	if temps.source != "" {
		h.SetSource("temperature", model.AvailOK, "via "+temps.source)
	} else if env.IsVM() {
		h.SetSource("temperature", model.AvailNotApplicable, "virtual machine ("+env.Virt+")")
	} else {
		h.SetSource("temperature", model.AvailUnavailable, "no coretemp/k10temp hwmon, CPU thermal zone or BMC CPU sensor")
	}
	out.PackageTemps = temps.packages()
	for i := range out.Logical {
		l := &out.Logical[i]
		if s := temps.core[[2]int{l.SocketID, l.CoreID}]; s != nil {
			l.TempCelsius = s.Value
		} else if s := temps.pkg[l.SocketID]; s != nil {
			l.TempCelsius = s.Value
		}
	}
	out.MaxTempC = temps.max()

	// --- kernel log: machine checks ---------------------------------------
	klog, kst := env.KernelLog(ctx)
	h.Sources = append(h.Sources, kst)
	var mceGlobal []kernellog.Entry
	if klog != nil {
		mceBy := map[string][]kernellog.Entry{}
		for _, e := range klog.ByCategory(kernellog.CatMCE) {
			if e.Subject == "" {
				mceGlobal = append(mceGlobal, e)
				continue
			}
			mceBy[e.Subject] = append(mceBy[e.Subject], e)
		}
		for i := range out.Logical {
			l := &out.Logical[i]
			l.MCELogged = model.U(uint64(len(mceBy["cpu"+strconv.Itoa(l.ID)])))
		}
		c.mceFindings(h, mceBy, mceGlobal)
	}

	// --- machine check history, offline CPUs ---------------------------------
	amd := strings.Contains(strings.ToLower(out.Vendor), "amd")
	hist := c.history(ctx, env, amd)
	for _, s := range hist.sources {
		h.SetSource(s.Name, s.State, s.Reason)
	}
	out.MCEHistory = mceHistory(klog, hist, amd)
	if !env.IsVM() {
		// firmware-first servers (HPE, Dell) report CPU errors to the BMC,
		// not to the kernel
		if _, evs, _ := env.RedfishLogs(ctx); len(evs) > 0 {
			out.MCEHistory = append(out.MCEHistory, redfishProcessorRecords(evs)...)
		} else if sel, _ := env.SEL(ctx); sel != nil {
			out.MCEHistory = append(out.MCEHistory, selProcessorRecords(sel)...)
		}
		sort.SliceStable(out.MCEHistory, func(i, j int) bool { return out.MCEHistory[i].Time.Before(out.MCEHistory[j].Time) })
	}
	out.CrashRecords = hist.crashes
	out.ProcessFaults = processFaults(klog, hist)
	c.offlineInfo(env, out, klog)

	// --- core test ----------------------------------------------------------
	if env.Opts.CoreTest && prev == nil {
		c.coreTest(ctx, env, out)
	}

	// --- utilization ----------------------------------------------------------
	if statErr == nil {
		var base map[int]sysfs.CPUStat
		if prev != nil {
			base = map[int]sysfs.CPUStat{}
			for _, pl := range prev.Logical {
				if pl.StatTotal > 0 {
					base[pl.ID] = sysfs.CPUStat{Busy: pl.StatBusy, Total: pl.StatTotal}
				}
			}
		} else {
			if wait := minUtilWindow - env.Now().Sub(start); wait > 0 && (env.FS.IsHost() || env.LiveSim) {
				select {
				case <-time.After(wait):
				case <-ctx.Done():
				}
			}
			base = stat1.CPUs
		}
		cur := stat1
		if prev == nil {
			if s2, err := readStat(f); err == nil {
				cur = s2
			}
		}
		for i := range out.Logical {
			l := &out.Logical[i]
			st, ok := cur.CPUs[l.ID]
			if !ok {
				continue
			}
			l.StatBusy, l.StatTotal = st.Busy, st.Total
			if b, ok := base[l.ID]; ok {
				l.UtilizationPercent = sysfs.Utilization(b, st)
			}
		}
	}

	// --- findings ---------------------------------------------------------------
	c.livenessFindings(env, h, masks, procs, out)
	offlineFindings(h, out)
	faultFindings(h, out)
	historyFindings(h, out)
	tempFindings(h, out, temps)
	throttleFindings(h, out, prevByID)
	microcodeFindings(h, out.Logical)
	suspects := out.Suspects()
	for i := range out.Logical {
		out.Logical[i].Status = logicalStatus(&out.Logical[i], temps, suspects)
	}

	online := 0
	for _, l := range out.Logical {
		if l.Online {
			online++
		}
	}
	h.Summary = fmt.Sprintf("%d/%d cores online", online, len(masks.Present))
	if out.MaxTempC != nil {
		h.Summary += ", max " + check.FmtTemp(*out.MaxTempC)
	}
	h.Rollup()
	return out, nil
}

func readStat(f hostfs.FS) (sysfs.ProcStat, error) {
	b, err := f.ReadFile("/proc/stat")
	if err != nil {
		return sysfs.ProcStat{}, err
	}
	return sysfs.ParseProcStat(b)
}

func readInterrupts(f hostfs.FS) sysfs.Interrupts {
	b, err := f.ReadFile("/proc/interrupts")
	if err != nil {
		return nil
	}
	in, _ := sysfs.ParseInterrupts(b)
	return in
}

func union(a, b cpuset.Set) []int {
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}

func packageIDs(ls []model.LogicalCPU) []int {
	var ids []int
	for _, l := range ls {
		if l.Online && l.SocketID >= 0 {
			ids = append(ids, l.SocketID)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

func countCores(ls []model.LogicalCPU) int {
	seen := map[[2]int]bool{}
	for _, l := range ls {
		if l.Online && l.CoreID >= 0 {
			seen[[2]int{l.SocketID, l.CoreID}] = true
		}
	}
	return len(seen)
}

// buildSockets merges SMBIOS processors with kernel packages. The n-th
// populated+enabled SMBIOS socket is matched to the n-th kernel package ID.
func buildSockets(procs []dmi.Processor, pkgIDs []int, ls []model.LogicalCPU) []model.CPUSocket {
	onlineBy := map[int]int{}
	for _, l := range ls {
		if l.Online {
			onlineBy[l.SocketID]++
		}
	}
	var out []model.CPUSocket
	if len(procs) == 0 {
		for _, id := range pkgIDs {
			out = append(out, model.CPUSocket{ID: id, Online: onlineBy[id], Status: model.StatusOK})
		}
		return out
	}
	next := 0
	for _, p := range procs {
		s := model.CPUSocket{
			ID: -1, Designation: p.Socket, Populated: boolp(p.Populated), Enabled: boolp(p.Enabled),
			StatusText: p.Status, Version: p.Version, Cores: p.Cores, Threads: p.Threads, MaxSpeedMHz: p.MaxSpeedMHz,
			Serial: p.Serial, PartNumber: p.PartNumber, AssetTag: p.AssetTag,
			Status: model.StatusOK,
		}
		if p.Populated && p.Enabled && next < len(pkgIDs) {
			s.ID = pkgIDs[next]
			s.Online = onlineBy[s.ID]
			next++
		}
		switch {
		case p.Populated && !p.Enabled && strings.Contains(p.Status, "POST Error"):
			s.Status = model.StatusCrit
		case p.Populated && !p.Enabled:
			s.Status = model.StatusWarn
		case p.Populated && p.Enabled && s.Online == 0:
			s.Status = model.StatusCrit
		}
		out = append(out, s)
	}
	return out
}

func refreshSockets(prev []model.CPUSocket, ls []model.LogicalCPU) []model.CPUSocket {
	onlineBy := map[int]int{}
	for _, l := range ls {
		if l.Online {
			onlineBy[l.SocketID]++
		}
	}
	out := slices.Clone(prev)
	for i := range out {
		if out[i].ID >= 0 {
			out[i].Online = onlineBy[out[i].ID]
			if out[i].Online == 0 {
				out[i].Status = model.StatusCrit
			}
		}
	}
	return out
}

func boolp(b bool) *bool { return &b }

// details fills identification, caches, vulnerabilities and CPU policy.
// Caches, flags and vulnerabilities do not change at runtime: refreshes
// carry them over.
func (c *Collector) details(out *model.CPU, f hostfs.FS, ci sysfs.CPUInfo, p sysfs.CPUPolicy, prev *model.CPU) {
	out.Family, out.ModelNumber, out.Stepping = ci.Family, ci.ModelNum, ci.Stepping
	out.SMTControl, out.SMTActive, out.Isolated, out.NoHzFull, out.Boost, out.EPP = p.SMTControl, p.SMTActive, p.Isolated, p.NoHzFull, p.Boost, p.EPP
	if prev != nil {
		out.Flags, out.Caches, out.Vulnerabilities = prev.Flags, prev.Caches, prev.Vulnerabilities
		return
	}
	out.Flags = append([]string{}, ci.Flags...)
	out.Caches = []model.CPUCache{}
	for _, x := range sysfs.ReadCaches(f) {
		out.Caches = append(out.Caches, model.CPUCache{Level: x.Level, Type: x.Type, SizeBytes: x.SizeBytes, Ways: x.Ways,
			LineBytes: x.LineBytes, Instances: x.Instances, SharedBy: x.SharedBy})
	}
	out.Vulnerabilities = []model.CPUVulnerability{}
	for _, v := range sysfs.ReadVulnerabilities(f) {
		out.Vulnerabilities = append(out.Vulnerabilities, model.CPUVulnerability{Name: v.Name, State: v.State, Status: vulnStatus(v.State)})
	}
}

// vulnStatus: "Not affected" and "Mitigation: ..." are fine, anything
// starting with "Vulnerable" is not. It is shown, not raised as a finding:
// it is a configuration/microcode matter, not a hardware fault.
func vulnStatus(s string) model.Status {
	switch {
	case strings.HasPrefix(s, "Not affected"), strings.HasPrefix(s, "Mitigation"):
		return model.StatusOK
	case strings.HasPrefix(s, "Vulnerable"):
		return model.StatusWarn
	}
	return model.StatusUnknown
}

func idleStates(in []sysfs.IdleState) []model.IdleState {
	var out []model.IdleState
	for _, s := range in {
		st := model.IdleState{Name: s.Name, Desc: s.Desc, LatencyUs: s.LatencyUs, Disabled: s.Disabled}
		if s.HasCounter {
			st.Usage, st.TimeUs = model.U(s.Usage), model.U(s.TimeUs)
		}
		out = append(out, st)
	}
	return out
}

// coreType classifies a CPU on hybrid designs: Intel P/E cores from the
// cpu_core/cpu_atom PMU lists, otherwise the tier from coreTiers.
func coreType(id int, p sysfs.CPUPolicy, tiers map[int]string) string {
	if p.HybridP != "" && p.HybridE != "" {
		if s, err := cpuset.Parse(p.HybridP); err == nil && s.Contains(id) {
			return "P"
		}
		if s, err := cpuset.Parse(p.HybridE); err == nil && s.Contains(id) {
			return "E"
		}
	}
	return tiers[id]
}

// coreTiers detects two core classes without a PMU list: arm64 big.LITTLE
// via differing cpu_capacity, or (AMD Zen 5/5c, others) exactly two
// maximum frequencies far apart. Small differences (favored cores, Turbo
// Boost Max) are not tiers, so the gap must exceed 15%.
func coreTiers(ex map[int]sysfs.CPUExtra) map[int]string {
	out := map[int]string{}
	caps, freqs := map[int]bool{}, map[float64]bool{}
	for _, x := range ex {
		if x.Capacity != nil {
			caps[*x.Capacity] = true
		}
		if x.MaxMHz != nil {
			freqs[*x.MaxMHz] = true
		}
	}
	switch {
	case len(caps) == 2:
		hi := 0
		for c := range caps {
			hi = max(hi, c)
		}
		for id, x := range ex {
			if x.Capacity != nil {
				out[id] = map[bool]string{true: "big", false: "little"}[*x.Capacity == hi]
			}
		}
	case len(freqs) == 2:
		var lo, hi float64
		for f := range freqs {
			if lo == 0 || f < lo {
				lo = f
			}
			hi = max(hi, f)
		}
		if lo/hi >= 0.85 {
			return out
		}
		for id, x := range ex {
			if x.MaxMHz != nil {
				out[id] = map[bool]string{true: "P", false: "E"}[*x.MaxMHz == hi]
			}
		}
	}
	return out
}

// microcodeFindings warns when online CPUs run different microcode
// revisions (a partially applied update).
func microcodeFindings(h *model.Header, ls []model.LogicalCPU) {
	revs := map[string][]string{}
	for _, l := range ls {
		if l.Online && l.Microcode != "" {
			revs[l.Microcode] = append(revs[l.Microcode], fmt.Sprintf("cpu%d", l.ID))
		}
	}
	if len(revs) < 2 {
		return
	}
	var parts []string
	for r, cpus := range revs {
		parts = append(parts, fmt.Sprintf("%s on %s", r, abbreviate(cpus, 4)))
	}
	sort.Strings(parts)
	h.Find(model.StatusWarn, "microcode_mismatch", "cpu", "cpu", "CPUs run different microcode revisions: "+strings.Join(parts, "; "))
}

func (c *Collector) livenessFindings(env *collector.Env, h *model.Header, masks sysfs.CPUMasks, procs []dmi.Processor, out *model.CPU) {
	offline := masks.Present.Minus(masks.Online)
	smt := hostfs.StringOr(env.FS, "/sys/devices/system/cpu/smt/control", "")
	smtOff := smt == "off" || smt == "forceoff"
	if len(offline) > 0 {
		if smtOff {
			h.Find(model.StatusOK, "smt_disabled", "smt", "sysfs",
				fmt.Sprintf("SMT disabled (%s): %d threads offline by configuration", smt, len(offline)))
		} else {
			names := make([]string, len(offline))
			for i, id := range offline {
				names[i] = "cpu" + strconv.Itoa(id)
			}
			noun := "cores"
			if len(offline) == 1 {
				noun = "core"
			}
			h.Find(model.StatusCrit, "core_offline", strings.Join(names, ","), "sysfs",
				fmt.Sprintf("%d %s offline (%s)%s", len(offline), noun, abbreviate(names, 6), offlineNote(out)))
		}
	}
	// Threads SMBIOS expects vs. threads the kernel sees.
	expected, known := 0, len(procs) > 0
	for _, p := range procs {
		if !p.Populated || !p.Enabled {
			continue
		}
		if p.Threads == nil || *p.Threads <= 0 || *p.Threads > 4096 {
			known = false
			break
		}
		t := *p.Threads
		if smtOff && p.Cores != nil && *p.Cores > 0 && t == 2**p.Cores {
			t = *p.Cores
		}
		expected += t
	}
	if known && expected > 0 && len(masks.Present) < expected {
		reason := ""
		if strings.Contains(hostfs.StringOr(env.FS, "/proc/cmdline", ""), "maxcpus=") || strings.Contains(hostfs.StringOr(env.FS, "/proc/cmdline", ""), "nr_cpus=") {
			reason = " (maxcpus=/nr_cpus= on kernel command line)"
		}
		h.Find(model.StatusWarn, "cpu_missing", "cpu", "dmidecode",
			fmt.Sprintf("%d CPUs missing: SMBIOS reports %d threads, kernel sees %d%s", expected-len(masks.Present), expected, len(masks.Present), reason))
	}
	for _, s := range out.Sockets {
		name := s.Designation
		if name == "" {
			name = "socket " + strconv.Itoa(s.ID)
		}
		switch {
		case s.Status == model.StatusOK:
		case s.Populated != nil && *s.Populated && s.Enabled != nil && !*s.Enabled:
			h.Find(s.Status, "socket_disabled", name, "dmidecode", fmt.Sprintf("%s populated but disabled (%s)", name, s.StatusText))
		default:
			h.Find(s.Status, "socket_no_cpus", name, "sysfs", fmt.Sprintf("%s has no online CPUs", name))
		}
	}
	if out.CoreTest != nil {
		var failed []string
		var worst model.Status
		for _, l := range out.Logical {
			switch l.CoreTest {
			case model.CoreTestFail, model.CoreTestTimeout:
				failed = append(failed, fmt.Sprintf("cpu%d %s", l.ID, l.CoreTest))
				worst = model.StatusCrit
			}
		}
		if len(failed) > 0 {
			h.Find(worst, "core_test_failed", strings.Join(failed, ","), "core_test",
				fmt.Sprintf("core test failed on %d CPUs (%s)", len(failed), abbreviate(failed, 4)))
		}
	}
}

func abbreviate(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:n], ", ") + fmt.Sprintf(", +%d more", len(items)-n)
}

func (c *Collector) mceFindings(h *model.Header, by map[string][]kernellog.Entry, global []kernellog.Entry) {
	if len(by) == 0 && len(global) > 0 {
		// Only summary records ("Machine check events logged"); details are in
		// mcelog/rasdaemon. Severity comes from the records themselves.
		worst := model.StatusOK
		for _, e := range global {
			worst = model.Worst(worst, e.Severity())
		}
		h.Find(worst, "mce", "cpu", "kernel_log", fmt.Sprintf("%d machine check records in kernel log", len(global)))
	}
	subjects := make([]string, 0, len(by))
	for s := range by {
		subjects = append(subjects, s)
	}
	sort.Slice(subjects, func(i, j int) bool { return natLess(subjects[i], subjects[j]) })
	for _, s := range subjects {
		es := by[s]
		worst, uc := model.StatusOK, 0
		for _, e := range es {
			worst = model.Worst(worst, e.Severity())
			if e.Severity() == model.StatusCrit {
				uc++
			}
		}
		msg := fmt.Sprintf("%d machine check errors on %s", len(es), s)
		if uc > 0 {
			msg += fmt.Sprintf(" (%d uncorrected)", uc)
		} else {
			msg += " (corrected)"
		}
		h.Find(worst, "mce", s, "kernel_log", msg)
	}
}

func natLess(a, b string) bool {
	ta, tb := strings.TrimRight(a, "0123456789"), strings.TrimRight(b, "0123456789")
	if ta != tb {
		return a < b
	}
	na, _ := strconv.Atoi(a[len(ta):])
	nb, _ := strconv.Atoi(b[len(tb):])
	return na < nb
}

func throttleFindings(h *model.Header, out *model.CPU, prev map[int]*model.LogicalCPU) {
	var total uint64
	var rising []string
	exc := 0
	for _, l := range out.Logical {
		if l.ThrottleCount != nil {
			total += *l.ThrottleCount
			if p := prev[l.ID]; p != nil && p.ThrottleCount != nil && *l.ThrottleCount > *p.ThrottleCount {
				rising = append(rising, "cpu"+strconv.Itoa(l.ID))
			}
		}
		if l.MCEExceptions != nil && *l.MCEExceptions > 0 {
			exc++
		}
	}
	if len(rising) > 0 {
		h.Find(model.StatusWarn, "throttling", strings.Join(rising, ","), "sysfs",
			fmt.Sprintf("thermal throttling on %d CPUs since last refresh (%s)", len(rising), abbreviate(rising, 4)))
	} else if total > 0 {
		h.Find(model.StatusOK, "throttle_history", "cpu", "sysfs", fmt.Sprintf("%d thermal throttling events since boot", total))
	}
	if exc > 0 {
		h.Find(model.StatusWarn, "mce_exceptions", "cpu", "interrupts",
			fmt.Sprintf("machine check exceptions on %d CPUs since boot", exc))
	}
}

func logicalStatus(l *model.LogicalCPU, t temps, suspects map[int]int) model.Status {
	st := model.StatusOK
	if l.Present && !l.Online {
		st = model.StatusCrit
	}
	if s := t.core[[2]int{l.SocketID, l.CoreID}]; s != nil {
		st = model.Worst(st, s.Status)
	} else if s := t.pkg[l.SocketID]; s != nil {
		st = model.Worst(st, s.Status)
	}
	switch l.CoreTest {
	case model.CoreTestFail, model.CoreTestTimeout:
		st = model.StatusCrit
	}
	if l.MCELogged != nil && *l.MCELogged > 0 {
		st = model.Worst(st, model.StatusWarn)
	}
	if _, bad := suspects[l.ID]; bad {
		st = model.Worst(st, model.StatusWarn) // its other thread was taken offline
	}
	return st
}

func (c *Collector) coreTest(ctx context.Context, env *collector.Env, out *model.CPU) {
	start := env.Now()
	timeout := env.Opts.CoreTestTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	var online []int
	suspect := out.Suspects()
	for _, l := range out.Logical {
		if _, bad := suspect[l.ID]; l.Online && !bad {
			online = append(online, l.ID)
		}
	}
	var res map[int]CoreResult
	if env.Simulated {
		res = map[int]CoreResult{}
		for _, id := range online {
			r := CoreResult{State: model.CoreTestPass}
			if slices.Contains(env.Sim.CoreTestFail, id) {
				r = CoreResult{State: model.CoreTestFail, Detail: "fp seed 4: expected 0x5f1c9a7e33b0d2c4, got 0x5f1c9a7e33b0d2c5 (simulated)"}
			} else if slices.Contains(env.Sim.CoreTestTimeout, id) {
				r = CoreResult{State: model.CoreTestTimeout, Detail: fmt.Sprintf("no result within %v (simulated)", timeout)}
			}
			res[id] = r
		}
	} else {
		var allowedFn func(int) bool
		if allowed, err := affinity.Allowed(); err == nil {
			allowedFn = allowed.Contains
		}
		res = RunCoreTest(ctx, online, allowedFn, timeout, c.Corrupt)
	}
	info := &model.CoreTestInfo{TimeoutMS: timeout.Milliseconds()}
	for i := range out.Logical {
		l := &out.Logical[i]
		if !l.Online {
			if l.Present {
				l.CoreTest, l.CoreTestDetail = model.CoreTestSkip, "offline"
				info.Skipped++
			}
			continue
		}
		r, ok := res[l.ID]
		if s, bad := suspect[l.ID]; bad && !ok {
			r = CoreResult{State: model.CoreTestSkip, Detail: fmt.Sprintf("suspect core: its other thread cpu%d is offline", s)}
		}
		l.CoreTest, l.CoreTestDetail = r.State, r.Detail
		switch r.State {
		case model.CoreTestPass:
			info.Passed++
		case model.CoreTestFail:
			info.Failed++
		case model.CoreTestTimeout:
			info.TimedOut++
		default:
			info.Skipped++
		}
	}
	info.DurationMS = env.Now().Sub(start).Milliseconds()
	out.CoreTest = info
	out.Header.SetSource("core_test", model.AvailOK, fmt.Sprintf("%d kernels per CPU", len(workload.Kernels)))
}

// --- temperatures ------------------------------------------------------------

type temps struct {
	source string
	pkg    map[int]*model.Sensor    // socket -> package sensor
	core   map[[2]int]*model.Sensor // (socket, core) -> core sensor
	extra  []model.Sensor           // CCD temps etc.
}

func (t temps) packages() []model.Sensor {
	ids := make([]int, 0, len(t.pkg))
	for id := range t.pkg {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := []model.Sensor{}
	for _, id := range ids {
		out = append(out, *t.pkg[id])
	}
	return append(out, t.extra...)
}

func (t temps) max() *float64 {
	var m *float64
	consider := func(s *model.Sensor) {
		if s.Value != nil && (m == nil || *s.Value > *m) {
			m = model.F(*s.Value)
		}
	}
	for _, s := range t.pkg {
		consider(s)
	}
	for _, s := range t.core {
		consider(s)
	}
	for i := range t.extra {
		consider(&t.extra[i])
	}
	return m
}

func mkTemp(name string, s sysfs.HwmonSensor, d check.Defaults, src string) *model.Sensor {
	sen := &model.Sensor{Name: name, Kind: model.KindTemperature, Value: s.Input, Unit: model.UnitCelsius,
		Thresholds: check.TempThresholds(s.Thresholds(), d), Source: src}
	check.EvalSensor(sen)
	if s.CritAlarm {
		sen.Status = model.StatusCrit
	} else if s.Alarm {
		sen.Status = model.Worst(sen.Status, model.StatusWarn)
	}
	return sen
}

// readTemps finds CPU temperatures. Order: coretemp (Intel, per package and
// per core), k10temp/zenpower (AMD, per socket), CPU thermal zones.
func readTemps(f hostfs.FS, pkgIDs []int, d check.Defaults, ci sysfs.CPUInfo) temps {
	t := temps{pkg: map[int]*model.Sensor{}, core: map[[2]int]*model.Sensor{}}
	chips := sysfs.ReadHwmon(f)
	var amd []sysfs.HwmonChip
	for _, c := range chips {
		switch c.Name {
		case "coretemp":
			pkg := 0
			if i := strings.LastIndexByte(c.Device, '.'); i >= 0 {
				pkg, _ = strconv.Atoi(c.Device[i+1:])
			}
			for _, s := range c.Sensors {
				if s.Kind != model.KindTemperature || s.Input == nil {
					continue
				}
				switch {
				case strings.HasPrefix(s.Label, "Package id "):
					if p, err := strconv.Atoi(strings.TrimPrefix(s.Label, "Package id ")); err == nil {
						pkg = p
					}
					t.pkg[pkg] = mkTemp(fmt.Sprintf("CPU%d Package", pkg), s, d, "coretemp")
				case strings.HasPrefix(s.Label, "Core "):
					core, err := strconv.Atoi(strings.TrimPrefix(s.Label, "Core "))
					if err == nil {
						t.core[[2]int{pkg, core}] = mkTemp(fmt.Sprintf("CPU%d Core %d", pkg, core), s, d, "coretemp")
					}
				}
			}
			t.source = "coretemp"
		case "k10temp", "zenpower", "zenpower3":
			amd = append(amd, c)
		}
	}
	if len(amd) > 0 {
		sort.Slice(amd, func(i, j int) bool { return amd[i].Device < amd[j].Device })
		// k10temp has no limits: use the model's Tjmax where known
		ad, origin, known := amdLimits(ci.Family, ci.ModelNum, d)
		mk := func(name string, s sysfs.HwmonSensor, src string) *model.Sensor {
			sen := mkTemp(name, s, ad, src)
			if known && sen.Thresholds != nil && sen.Thresholds.Origin == "default" {
				sen.Thresholds.Origin = origin
			}
			return sen
		}
		for i, c := range amd {
			socket := i
			if i < len(pkgIDs) {
				socket = pkgIDs[i]
			}
			var tctl, tdie *sysfs.HwmonSensor
			for j := range c.Sensors {
				s := &c.Sensors[j]
				if s.Kind != model.KindTemperature || s.Input == nil {
					continue
				}
				switch {
				case s.Label == "Tdie":
					tdie = s
				case s.Label == "Tctl":
					tctl = s
				case strings.HasPrefix(s.Label, "Tccd"):
					t.extra = append(t.extra, *mk(fmt.Sprintf("CPU%d %s", socket, s.Label), *s, c.Name))
				}
			}
			if tdie == nil {
				tdie = tctl
			}
			if tdie != nil {
				t.pkg[socket] = mk(fmt.Sprintf("CPU%d Package", socket), *tdie, c.Name)
			}
		}
		if t.source == "" {
			t.source = amd[0].Name
		}
	}
	if t.source != "" {
		return t
	}
	// Fallback: thermal zones (x86_pkg_temp, arm64 cpu/soc zones).
	var zones []sysfs.ThermalZone
	for _, z := range sysfs.ReadThermalZones(f) {
		n := strings.ToLower(z.Name)
		if z.TempC != nil && (n == "x86_pkg_temp" || strings.Contains(n, "cpu") || strings.Contains(n, "soc")) {
			zones = append(zones, z)
		}
	}
	for i, z := range zones {
		hs := sysfs.HwmonSensor{Kind: model.KindTemperature, Input: z.TempC, Crit: z.CritC, Max: z.PassiveC}
		if hs.Max == nil {
			hs.Max = z.HotC
		}
		if len(zones) == len(pkgIDs) {
			t.pkg[pkgIDs[i]] = mkTemp(fmt.Sprintf("CPU%d Package", pkgIDs[i]), hs, d, "thermal_zone")
		} else {
			t.extra = append(t.extra, *mkTemp(z.Name+" ("+path.Base(z.Path)+")", hs, d, "thermal_zone"))
		}
	}
	if len(zones) > 0 {
		t.source = "thermal_zone"
	}
	return t
}

func tempFindings(h *model.Header, out *model.CPU, t temps) {
	for _, s := range out.PackageTemps {
		if s.Status == model.StatusWarn || s.Status == model.StatusCrit {
			h.Find(s.Status, "cpu_temp", s.Name, s.Source, tempMsg(s.Name, s))
		}
	}
	var hottest *model.Sensor
	for _, s := range t.core {
		if (s.Status == model.StatusWarn || s.Status == model.StatusCrit) && s.Value != nil {
			if hottest == nil || *s.Value > *hottest.Value {
				hottest = s
			}
		}
	}
	if hottest != nil {
		h.Find(hottest.Status, "core_temp", hottest.Name, hottest.Source, tempMsg(hottest.Name, *hottest))
	}
}

func tempMsg(name string, s model.Sensor) string {
	lim := ""
	if s.Thresholds != nil {
		if s.Status == model.StatusCrit && s.Thresholds.UpperCrit != nil {
			lim = " (crit " + check.FmtTemp(*s.Thresholds.UpperCrit) + ")"
		} else if s.Thresholds.UpperWarn != nil {
			lim = " (warn " + check.FmtTemp(*s.Thresholds.UpperWarn) + ")"
		}
	}
	return fmt.Sprintf("%s at %s%s", name, check.FmtTemp(*s.Value), lim)
}
