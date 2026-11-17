package cpu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/execx"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/ipmi"
	"github.com/noahzmr/tyr/internal/source/kernellog"
)

// Machine check history: where errors were reported and by which unit, from
// this boot's kernel log, earlier boots (journal), crash records (pstore)
// and rasdaemon. A CPU number alone does not blame a core: L3, memory
// controller and fabric errors are reported by whichever CPU noticed them,
// so every record carries the unit and whether it sits inside the core.

const maxMCEHistory = 300

var (
	mceLine   = regexp.MustCompile(`\[Hardware Error\]: CPU (\d+): Machine Check( Exception)?: (\S+) Bank (\d+): ([0-9a-fA-F]+)`)
	amdStatus = regexp.MustCompile(`\[Hardware Error\]: CPU:(\d+) \([0-9a-f]+:[0-9a-f]+:[0-9a-f]+\) MC(\d+)_STATUS\[([^\]]*)\]`)
	amdUnit   = regexp.MustCompile(`\[Hardware Error\]: (.+?) (?:Ext\. )?Error Code:`)
	ghesSev   = regexp.MustCompile(`\[Hardware Error\]: event severity: (\w+)`)
	ghesSect  = regexp.MustCompile(`\[Hardware Error\]:\s+section_type: (general processor error|memory error|processor error)`)
	panicLine = regexp.MustCompile(`Kernel panic - not syncing: (.+)`)
	panicCPU  = regexp.MustCompile(`\bCPU: (\d+) (?:UID: \d+ )?PID: `)
	pstoreTS  = regexp.MustCompile(`^(?:<\d+>)?\[\s*\d+\.\d+\]\s?`)
	mceTime   = regexp.MustCompile(`\[Hardware Error\]: PROCESSOR \S+ TIME (\d{9,})`) // Intel: when it happened
)

// intelBank names the MCA banks that are the same on Intel server CPUs
// since Nehalem: 0-3 sit inside the core; from 4 on they are the PCU and
// uncore (interconnect, memory controller, L3), which differ per model.
func intelBank(b int) (unit, scope string) {
	switch b {
	case 0:
		return "instruction fetch (IFU)", "core"
	case 1:
		return "L1 data cache (DCU)", "core"
	case 2:
		return "data TLB (DTLB)", "core"
	case 3:
		return "L2 cache (MLC)", "core"
	case 4:
		return "power control unit (PCU)", "shared"
	}
	return fmt.Sprintf("uncore bank %d: interconnect, memory controller or L3", b), "shared"
}

// amdScope sorts AMD SMCA unit names (from the kernel's decoder) into the
// core's own units and shared ones.
func amdScope(unit string) string {
	low := strings.ToLower(unit)
	for _, c := range []string{"load store", "load-store", "instruction fetch", "l2 cache", "decode unit", "execution unit",
		"floating point", "l1", "data cache", "instruction cache", "bus unit"} {
		if strings.Contains(low, c) {
			return "core"
		}
	}
	return "shared"
}

// mceParser turns log lines into records; decoder lines that follow a
// record (AMD unit names, GHES sections) complete the record before them.
type mceParser struct {
	amd     bool
	source  string
	recs    []model.MCERecord
	ghesSev model.Status
	ghesAt  time.Time
}

func (p *mceParser) line(at time.Time, msg string) {
	if !strings.Contains(msg, "Hardware Error") {
		return
	}
	if m := mceLine.FindStringSubmatch(msg); m != nil {
		cpu, _ := strconv.Atoi(m[1])
		bank, _ := strconv.Atoi(m[4])
		sev := model.StatusWarn
		if st, err := strconv.ParseUint(m[5], 16, 64); err == nil && st&(1<<61) != 0 {
			sev = model.StatusCrit
		}
		if m[2] != "" {
			sev = model.StatusCrit
		}
		r := model.MCERecord{Time: at, Source: p.source, CPU: cpu, Bank: bank, Severity: sev, Text: strings.TrimSpace(msg)}
		if !p.amd {
			r.Unit, r.Scope = intelBank(bank)
		}
		p.recs = append(p.recs, r)
		return
	}
	if m := amdStatus.FindStringSubmatch(msg); m != nil {
		cpu, _ := strconv.Atoi(m[1])
		bank, _ := strconv.Atoi(m[2])
		sev := model.StatusWarn
		if strings.Contains("|"+m[3]+"|", "|UE|") || strings.Contains(m[3], "PCC") {
			sev = model.StatusCrit
		}
		if r := p.lastFor(cpu, bank, at); r != nil {
			r.Severity = model.Worst(r.Severity, sev)
			return
		}
		p.recs = append(p.recs, model.MCERecord{Time: at, Source: p.source, CPU: cpu, Bank: bank, Severity: sev, Text: strings.TrimSpace(msg)})
		return
	}
	if m := amdUnit.FindStringSubmatch(msg); m != nil && len(p.recs) > 0 {
		r := &p.recs[len(p.recs)-1]
		if u := strings.TrimSpace(m[1]); r.Unit == "" && !strings.Contains(u, ":") && at.Sub(r.Time) < 2*time.Second {
			r.Unit, r.Scope = u, amdScope(u)
		}
		return
	}
	if m := ghesSev.FindStringSubmatch(msg); m != nil {
		p.ghesSev, p.ghesAt = model.StatusWarn, at
		if m[1] != "corrected" {
			p.ghesSev = model.StatusCrit
		}
		return
	}
	if m := ghesSect.FindStringSubmatch(msg); m != nil && p.ghesSev != model.StatusOK && at.Sub(p.ghesAt) < 2*time.Second {
		r := model.MCERecord{Time: at, Source: p.source, CPU: -1, Bank: -1, Severity: p.ghesSev, Unit: m[1] + " (firmware report)", Text: strings.TrimSpace(msg)}
		if m[1] == "memory error" {
			r.Scope = "shared"
		}
		p.recs = append(p.recs, r)
		p.ghesSev = model.StatusOK
	}
}

func (p *mceParser) lastFor(cpu, bank int, at time.Time) *model.MCERecord {
	for i := len(p.recs) - 1; i >= 0 && i >= len(p.recs)-4; i-- {
		r := &p.recs[i]
		if r.CPU == cpu && r.Bank == bank && at.Sub(r.Time) < 2*time.Second {
			return r
		}
	}
	return nil
}

// staticHistory is what does not change while Tyr runs: earlier boots,
// crash records, rasdaemon. Read once per collector.
type staticHistory struct {
	recs    []model.MCERecord
	faults  []model.ProcessFault
	crashes []model.CrashRecord
	sources []model.SourceState
}

func (c *Collector) history(ctx context.Context, env *collector.Env, amd bool) *staticHistory {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hist != nil {
		return c.hist
	}
	h := &staticHistory{}
	h.journal(ctx, env, amd)
	h.pstore(env, amd)
	h.rasdaemon(ctx, env)
	c.hist = h
	return h
}

// journal reads machine checks of earlier boots from the systemd journal
// (this boot comes from the kernel log).
func (h *staticHistory) journal(ctx context.Context, env *collector.Env, amd bool) {
	if _, err := env.Exec.LookPath("journalctl"); err != nil {
		h.sources = append(h.sources, model.SourceState{Name: "journal", State: model.AvailUnavailable, Reason: "journalctl not installed: no machine checks from earlier boots"})
		return
	}
	cur := strings.ReplaceAll(strings.TrimSpace(hostfs.StringOr(env.FS, "/proc/sys/kernel/random/boot_id", "")), "-", "")
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := env.Exec.Run(ctx, "journalctl", "_TRANSPORT=kernel", "-o", "json", "--no-pager", "-q",
		"--output-fields=MESSAGE", "--since=-365d", "-g", "Hardware Error|Kernel panic|likely on CPU")
	var xe *execx.ExitError
	if err != nil && len(out) == 0 && !(errors.As(err, &xe) && xe.Code == 1 && len(bytes.TrimSpace(xe.Stderr)) == 0) {
		// exit 1 without a message: no matching entries
		h.sources = append(h.sources, model.SourceState{Name: "journal", State: model.AvailUnavailable, Reason: firstLine(err.Error())})
		return
	}
	type boot struct {
		p *mceParser
	}
	boots := map[string]*boot{}
	for _, ln := range bytes.Split(out, []byte("\n")) {
		var e struct {
			Boot string          `json:"_BOOT_ID"`
			TS   string          `json:"__REALTIME_TIMESTAMP"`
			Msg  json.RawMessage `json:"MESSAGE"`
		}
		if len(ln) == 0 || json.Unmarshal(ln, &e) != nil || e.Boot == cur {
			continue
		}
		var msg string
		if json.Unmarshal(e.Msg, &msg) != nil {
			var raw []byte
			if json.Unmarshal(e.Msg, &raw) != nil {
				continue
			}
			msg = string(raw)
		}
		us, _ := strconv.ParseInt(e.TS, 10, 64)
		at := time.UnixMicro(us).UTC()
		b := boots[e.Boot]
		if b == nil {
			b = &boot{p: &mceParser{amd: amd}}
			boots[e.Boot] = b
		}
		b.p.line(at, msg)
		if f, ok := parseFault(at, "earlier boot "+e.Boot[:min(8, len(e.Boot))], msg); ok {
			h.faults = append(h.faults, f)
		}
	}
	for id, b := range boots {
		for _, r := range b.p.recs {
			r.Source = "earlier boot " + id[:min(8, len(id))] // journalctl -b <id>
			h.recs = append(h.recs, r)
		}
	}
	mces := 0
	for _, b := range boots {
		mces += len(b.p.recs)
	}
	reason := fmt.Sprintf("earlier boots: %d machine checks, %d program crashes", mces, len(h.faults))
	if !hostfs.Exists(env.FS, "/var/log/journal") {
		reason += "; the journal is not persistent (no /var/log/journal): earlier boots are not kept"
	}
	h.sources = append(h.sources, model.SourceState{Name: "journal", State: model.AvailOK, Reason: reason})
}

// pstore reads crash records the kernel saved before a panic, in place or
// archived by systemd-pstore.
func (h *staticHistory) pstore(env *collector.Env, amd bool) {
	var files []string
	for _, g := range []string{"/sys/fs/pstore/dmesg-*", "/var/lib/systemd/pstore/*/dmesg*", "/var/lib/systemd/pstore/*/*/dmesg*"} {
		m, _ := env.FS.Glob(g)
		files = append(files, m...)
	}
	sort.Strings(files)
	seen := map[string]bool{}
	for _, f := range files {
		b, err := env.FS.ReadFile(f)
		if err != nil {
			continue
		}
		at := time.Time{}
		if fi, err := env.FS.Stat(f); err == nil && env.FS.IsHost() { // fixtures: checkout time, not the crash
			at = fi.ModTime().UTC()
		}
		if m := mceTime.FindSubmatch(b); m != nil {
			if sec, err := strconv.ParseInt(string(m[1]), 10, 64); err == nil {
				at = time.Unix(sec, 0).UTC()
			}
		}
		p := &mceParser{amd: amd, source: "crash record"}
		cr := model.CrashRecord{Time: at, File: f, CPU: -1}
		var lines []string
		for _, ln := range strings.Split(string(b), "\n") {
			ln = strings.TrimRight(pstoreTS.ReplaceAllString(ln, ""), " \r")
			if ln == "" {
				continue
			}
			lines = append(lines, ln)
			p.line(at, ln)
			if m := panicLine.FindStringSubmatch(ln); m != nil && cr.Reason == "" {
				cr.Reason = "Kernel panic - not syncing: " + strings.TrimSpace(m[1])
			}
			if m := panicCPU.FindStringSubmatch(ln); m != nil && cr.CPU < 0 {
				cr.CPU, _ = strconv.Atoi(m[1])
			}
		}
		for _, r := range p.recs {
			if cr.CPU < 0 && r.Severity == model.StatusCrit {
				cr.CPU = r.CPU
			}
			h.recs = append(h.recs, r)
		}
		if cr.Reason == "" {
			continue
		}
		key := cr.Reason + at.Truncate(time.Minute).String()
		if seen[key] {
			continue
		}
		seen[key] = true
		cr.Lines = lines[max(0, len(lines)-25):]
		h.crashes = append(h.crashes, cr)
	}
}

var (
	rasLine = regexp.MustCompile(`^\s*\d+ (\d{4}-\d\d-\d\d \d\d:\d\d:\d\d [+-]\d{4}) (?:error: )?(.*)$`)
	rasCPU  = regexp.MustCompile(`(?i)\bcpu[ =:]+(\d+)`)
	rasBank = regexp.MustCompile(`bank[ =:]+(\d+)`)
	rasUnit = regexp.MustCompile(`bank ([A-Za-z][^,(]*?) \(bank=`)
)

// rasdaemon lists the MCE events rasdaemon stored (all boots).
func (h *staticHistory) rasdaemon(ctx context.Context, env *collector.Env) {
	if _, err := env.Exec.LookPath("ras-mc-ctl"); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := env.Exec.Run(ctx, "ras-mc-ctl", "--errors")
	if err != nil && len(out) == 0 {
		h.sources = append(h.sources, model.SourceState{Name: "rasdaemon", State: model.AvailUnavailable, Reason: "ras-mc-ctl: " + firstLine(err.Error())})
		return
	}
	in := false
	n := 0
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.HasSuffix(strings.TrimSpace(ln), "events:") {
			in = strings.HasPrefix(strings.TrimSpace(ln), "MCE")
			continue
		}
		m := rasLine.FindStringSubmatch(ln)
		if !in || m == nil {
			continue
		}
		at, _ := time.Parse("2006-01-02 15:04:05 -0700", m[1])
		r := model.MCERecord{Time: at.UTC(), Source: "rasdaemon", CPU: -1, Bank: -1, Severity: model.StatusWarn, Text: strings.TrimSpace(m[2])}
		if c := rasCPU.FindStringSubmatch(m[2]); c != nil {
			r.CPU, _ = strconv.Atoi(c[1])
		}
		if b := rasBank.FindStringSubmatch(m[2]); b != nil {
			r.Bank, _ = strconv.Atoi(b[1])
		}
		if u := rasUnit.FindStringSubmatch(m[2]); u != nil {
			r.Unit, r.Scope = u[1], amdScope(u[1])
		}
		if low := strings.ToLower(m[2]); strings.Contains(low, "uncorrected") || strings.Contains(low, "fatal") {
			r.Severity = model.StatusCrit
		}
		h.recs = append(h.recs, r)
		n++
	}
	h.sources = append(h.sources, model.SourceState{Name: "rasdaemon", State: model.AvailOK, Reason: fmt.Sprintf("%d MCE events", n)})
}

// mceHistory merges this boot's kernel log with the static history, oldest
// first. rasdaemon records that repeat a kernel log record are dropped.
func mceHistory(klog *kernellog.Log, st *staticHistory, amd bool) []model.MCERecord {
	p := &mceParser{amd: amd, source: "kernel log (this boot)"}
	if klog != nil {
		for _, e := range klog.Recent {
			p.line(e.Time, e.Message)
		}
	}
	recs := append([]model.MCERecord(nil), p.recs...)
	for _, r := range st.recs {
		if r.Source == "rasdaemon" && duplicate(p.recs, r) {
			continue
		}
		recs = append(recs, r)
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Time.Before(recs[j].Time) })
	if len(recs) > maxMCEHistory {
		recs = recs[len(recs)-maxMCEHistory:]
	}
	return recs
}

func duplicate(in []model.MCERecord, r model.MCERecord) bool {
	for _, x := range in {
		d := x.Time.Sub(r.Time)
		if x.CPU == r.CPU && (r.Bank < 0 || x.Bank == r.Bank) && d < 10*time.Second && d > -10*time.Second {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// historyFindings: machine checks outside this boot, and kernel panics.
func historyFindings(h *model.Header, out *model.CPU) {
	type agg struct {
		n     int
		worst model.Status
		units map[string]bool
		last  time.Time
	}
	by := map[int]*agg{}
	var cpus []int
	for _, r := range out.MCEHistory {
		if strings.HasPrefix(r.Source, "kernel log") {
			continue // this boot: the mce finding
		}
		a := by[r.CPU]
		if a == nil {
			a = &agg{units: map[string]bool{}}
			by[r.CPU] = a
			cpus = append(cpus, r.CPU)
		}
		a.n++
		a.worst = model.Worst(a.worst, r.Severity)
		if r.Unit != "" {
			a.units[r.Scope+": "+r.Unit] = true
		}
		if r.Time.After(a.last) {
			a.last = r.Time
		}
	}
	if len(cpus) > 0 {
		sort.Slice(cpus, func(i, j int) bool { return by[cpus[i]].n > by[cpus[j]].n })
		worst := model.StatusOK
		var parts, subj []string
		var last time.Time
		for i, c := range cpus {
			a := by[c]
			worst = model.Worst(worst, a.worst)
			if a.last.After(last) {
				last = a.last
			}
			if i >= 3 {
				continue
			}
			name := "cpu" + strconv.Itoa(c)
			if c < 0 {
				name = "BMC / firmware"
			} else {
				subj = append(subj, name)
			}
			var units []string
			for u := range a.units {
				units = append(units, u)
			}
			sort.Strings(units)
			s := fmt.Sprintf("%s %d×", name, a.n)
			if len(units) > 0 {
				s += " (" + strings.Join(units, "; ") + ")"
			}
			parts = append(parts, s)
		}
		if len(cpus) > 3 {
			parts = append(parts, fmt.Sprintf("+%d more CPUs", len(cpus)-3))
		}
		subject := strings.Join(subj, ",")
		if subject == "" {
			subject = "cpu"
		}
		msg := "machine checks in earlier boots, crash records or the BMC log: " + strings.Join(parts, ", ")
		if !last.IsZero() {
			msg += " · last " + last.Local().Format("2006-01-02")
		}
		h.Find(worst, "mce_history", subject, "journal", msg)
	}
	for _, cr := range out.CrashRecords {
		subj, on := "cpu", ""
		if cr.CPU >= 0 {
			subj = "cpu" + strconv.Itoa(cr.CPU)
			on = " on " + subj
		}
		when := ""
		if !cr.Time.IsZero() {
			when = " (" + cr.Time.Local().Format("2006-01-02 15:04") + ")"
		}
		h.Find(model.StatusCrit, "crash_record", subj, "pstore", "the kernel crashed"+on+when+": "+strings.TrimPrefix(cr.Reason, "Kernel panic - not syncing: "))
	}
}

// selProcessorRecords turns processor events of the BMC event log (IERR,
// machine check, thermal trip, configuration error) into history records.
// The BMC names the socket, not the logical CPU.
func selProcessorRecords(sel *ipmi.SEL) []model.MCERecord {
	var out []model.MCERecord
	for _, e := range sel.Entries {
		if e.Category != "processor" || !e.Asserted || e.Severity < model.StatusWarn {
			continue
		}
		out = append(out, model.MCERecord{Time: e.Time, Source: "BMC event log", CPU: -1, Bank: -1,
			Unit: strings.TrimSpace(e.Sensor + ": " + e.Event), Severity: e.Severity,
			Text: strings.TrimSpace(e.SensorType + " " + e.Sensor + ": " + e.Event)})
	}
	return out
}

// --- program crashes per core ---------------------------------------------------

const maxFaults = 500

// faultLine: "unpigz[203267]: segfault at … likely on CPU 46 (core 21,
// socket 1)", "traps: x[1]: trap invalid opcode … likely on CPU 3 (…)".
var faultLine = regexp.MustCompile(`(?:traps: )?(\S+?)\[\d+\]:? (.*?)\s*likely on CPU (\d+) \(core (\d+), socket (\d+)\)`)

func parseFault(at time.Time, source, msg string) (model.ProcessFault, bool) {
	m := faultLine.FindStringSubmatch(msg)
	if m == nil {
		return model.ProcessFault{}, false
	}
	f := model.ProcessFault{Time: at, Source: source, Process: m[1], Text: strings.TrimSpace(msg)}
	f.CPU, _ = strconv.Atoi(m[3])
	f.Core, _ = strconv.Atoi(m[4])
	f.Socket, _ = strconv.Atoi(m[5])
	low := strings.ToLower(m[2])
	switch {
	case strings.Contains(low, "segfault"):
		f.Kind = "segfault"
	case strings.Contains(low, "general protection"):
		f.Kind = "general protection"
	case strings.Contains(low, "invalid opcode"):
		f.Kind = "invalid opcode"
	default:
		f.Kind = "trap"
	}
	return f, true
}

// processFaults merges this boot's kernel log with earlier boots.
func processFaults(klog *kernellog.Log, st *staticHistory) []model.ProcessFault {
	out := append([]model.ProcessFault(nil), st.faults...)
	if klog != nil {
		for _, e := range klog.Recent {
			if strings.Contains(e.Message, "likely on CPU") {
				if f, ok := parseFault(e.Time, "kernel log (this boot)", e.Message); ok {
					out = append(out, f)
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	if len(out) > maxFaults {
		out = out[len(out)-maxFaults:]
	}
	return out
}

// FaultGroup is the crashes of one physical core.
type faultGroup struct {
	socket, core int
	n            int
	cpus, procs  map[string]bool
	kinds        map[string]int
}

func faultGroups(fs []model.ProcessFault) []*faultGroup {
	by := map[[2]int]*faultGroup{}
	var out []*faultGroup
	for _, f := range fs {
		k := [2]int{f.Socket, f.Core}
		g := by[k]
		if g == nil {
			g = &faultGroup{socket: f.Socket, core: f.Core, cpus: map[string]bool{}, procs: map[string]bool{}, kinds: map[string]int{}}
			by[k] = g
			out = append(out, g)
		}
		g.n++
		g.cpus["cpu"+strconv.Itoa(f.CPU)] = true
		g.procs[f.Process] = true
		g.kinds[f.Kind]++
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].n > out[j].n })
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return natLess(out[i], out[j]) })
	return out
}

// faultFindings: crashes of different programs piling up on one core. A
// healthy system spreads its (rare) crashes over all cores; a core that
// computes wrong results makes unrelated programs crash on it.
func faultFindings(h *model.Header, out *model.CPU) {
	gs := faultGroups(out.ProcessFaults)
	if len(gs) == 0 {
		return
	}
	total := len(out.ProcessFaults)
	g := gs[0]
	if g.n < 3 || len(g.procs) < 2 || float64(g.n) < 0.4*float64(total) {
		return
	}
	var kinds []string
	for k := range g.kinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	cpus := keys(g.cpus)
	h.Find(model.StatusCrit, "fault_cluster", strings.Join(cpus, ","), "kernel_log",
		fmt.Sprintf("%d of %d program crashes (%s) ran on core %d of socket %d (%s), in %d different programs: the core likely computes wrong results",
			g.n, total, strings.Join(kinds, ", "), g.core, g.socket, strings.Join(cpus, ", "), len(g.procs)))
}
