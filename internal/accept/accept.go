package accept

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/stress"
)

// Verdict of the acceptance test.
type Verdict string

const (
	Pass       Verdict = "PASS"       // fit for production
	Remarks    Verdict = "REMARKS"    // passed; read the remarks
	Fail       Verdict = "FAIL"       // not fit: see the failures
	Incomplete Verdict = "INCOMPLETE" // a test could not run or was stopped
)

// ExitCode: 0 pass, 1 remarks, 2 fail, 3 incomplete (like tyr stress).
func (v Verdict) ExitCode() int {
	return map[Verdict]int{Pass: 0, Remarks: 1, Fail: 2, Incomplete: 3}[v]
}

// Step is one stage of the run.
type Step struct {
	Name    string        `json:"name"` // health check, cpu, memory, storage, network, final check
	Verdict string        `json:"verdict"`
	Summary string        `json:"summary"`
	File    string        `json:"file,omitempty"` // result file, relative to the results directory
	Took    time.Duration `json:"took_ns"`
}

// Machine identifies the tested machine.
type Machine struct {
	Vendor, Product, Serial string
	BIOS                    string
	CPU                     string
	MemoryBytes             uint64
	Disks                   int
	GPUs                    int
}

// Result is the acceptance result (accept.json).
type Result struct {
	SchemaVersion string           `json:"schema_version"`
	Kind          string           `json:"kind"` // "acceptance"
	TyrVersion    string           `json:"tyr_version"`
	Hostname      string           `json:"hostname"`
	Machine       Machine          `json:"machine"`
	Profile       Profile          `json:"profile"`
	StartedAt     time.Time        `json:"started_at"`
	EndedAt       time.Time        `json:"ended_at"`
	Verdict       Verdict          `json:"verdict"`
	Failures      []string         `json:"failures"`
	Remarks       []string         `json:"remarks"`
	Steps         []Step           `json:"steps"`
	CounterDeltas map[string]int64 `json:"counter_deltas"`
}

// Identify fills Machine from a snapshot.
func Identify(s *model.Snapshot) Machine {
	var m Machine
	if y := s.System; y != nil {
		m.Vendor, m.Product, m.Serial = y.Vendor, y.Product, y.Serial
		m.BIOS = strings.TrimSpace(y.BIOSVendor + " " + y.BIOSVersion)
	}
	if c := s.CPU; c != nil {
		m.CPU = c.Model
		if n := len(c.Sockets); n > 1 {
			m.CPU = fmt.Sprintf("%d × %s", n, c.Model)
		}
	}
	if x := s.Memory; x != nil {
		m.MemoryBytes = x.TotalBytes
		if x.InstalledBytes != nil {
			m.MemoryBytes = *x.InstalledBytes
		}
	}
	m.Disks = len(physicalDisks(s))
	if a := s.Accel; a != nil {
		m.GPUs = len(a.GPUs)
	}
	return m
}

func physicalDisks(s *model.Snapshot) []model.Disk {
	var out []model.Disk
	if s.Storage == nil {
		return nil
	}
	for _, d := range s.Storage.Disks {
		if d.Kind != "virtual" && d.Kind != "usb" && d.Interface != "usb" {
			out = append(out, d)
		}
	}
	return out
}

// USBDisks are the disks the storage scan leaves out unless the profile
// includes USB.
func USBDisks(s *model.Snapshot) []string {
	var out []string
	if s.Storage == nil {
		return nil
	}
	for _, d := range s.Storage.Disks {
		if d.Kind == "usb" || d.Interface == "usb" {
			out = append(out, d.Name)
		}
	}
	return out
}

// Judge sets r's verdict, failures and remarks from the snapshots before
// and after the load and the stress results. incomplete names the parts
// that could not run.
func Judge(p Profile, r *Result, before, after *model.Snapshot, runs []*stress.Result, incomplete []string) {
	var fail, remark []string
	seen := map[string]bool{}
	add := func(list *[]string, s string) {
		if !seen[s] {
			seen[s] = true
			*list = append(*list, s)
		}
	}

	// findings: the worst state of each finding before or after the load
	type fk struct {
		f   model.Finding
		mod model.ModuleID
	}
	worst := map[string]fk{}
	var order []string
	for i, s := range []*model.Snapshot{before, after} {
		if s == nil {
			continue
		}
		for _, id := range append([]model.ModuleID{model.ModSystem}, model.Modules...) {
			sec := s.Section(id)
			if sec == nil {
				continue
			}
			for _, f := range sec.Head().Findings {
				if i == 1 && (f.Code == "cpu_temp" || f.Code == "core_temp") {
					continue // still hot from the load; the stress run judged that
				}
				k := string(id) + "|" + f.Key()
				if w, ok := worst[k]; !ok || f.Status > w.f.Status {
					if !ok {
						order = append(order, k)
					}
					worst[k] = fk{f, id}
				}
			}
		}
	}
	for _, k := range order {
		w := worst[k]
		f, code := w.f, w.f.Code
		if listed(p.Ignore, w.mod, code) {
			continue
		}
		msg := fmt.Sprintf("%s: %s", w.mod, f.Message)
		switch {
		case f.Status == model.StatusCrit:
			add(&fail, msg)
		case f.Status == model.StatusWarn && listed(p.FailOnWarn, w.mod, code):
			add(&fail, msg)
		case f.Status == model.StatusWarn:
			add(&remark, msg)
		}
	}

	// stress runs
	for _, res := range runs {
		comp := string(res.Component)
		switch res.Verdict {
		case stress.VerdictFail:
			for _, why := range res.Reasons {
				if strings.HasPrefix(why, "new finding: ") {
					continue // judged above with the profile's lists
				}
				add(&fail, comp+" stress: "+why)
			}
		case stress.VerdictAborted:
			incomplete = append(incomplete, comp+" stress: "+strings.Join(res.Reasons, "; "))
		case stress.VerdictWarn:
			for _, why := range res.Reasons {
				if !strings.HasPrefix(why, "new finding: ") && !counterReason(why) {
					add(&remark, comp+" stress: "+why)
				}
			}
		}
		for _, m := range res.Metrics {
			kind := strings.TrimSuffix(m.Name, "_temp")
			if lim, ok := p.MaxTempC[kind]; ok && lim > 0 && m.Name == kind+"_temp" && m.Samples > 0 && m.Max > lim {
				add(&fail, fmt.Sprintf("%s stress: %s temperature reached %.0f°C (limit %.0f°C)", comp, kind, m.Max, lim))
			}
		}
	}

	// counters over the whole run, including the time between the tests
	if before != nil && after != nil {
		r.CounterDeltas = map[string]int64{}
		b, a := before.Counters(), after.Counters()
		keys := make([]string, 0, len(a))
		for k := range a {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v, ok := b[k]
			if !ok || a[k] <= v {
				continue
			}
			d := int64(a[k] - v)
			r.CounterDeltas[k] = d
			switch cls := stress.CounterClass(k); {
			case cls == model.StatusCrit || matchesAny(k, p.FailCounters):
				add(&fail, fmt.Sprintf("counter %s +%d during the test", k, d))
			case cls == model.StatusWarn:
				add(&remark, fmt.Sprintf("counter %s +%d during the test", k, d))
			}
		}
	}

	// minimums
	if s := after; s != nil {
		if p.Min.MemoryGB > 0 && s.Memory != nil {
			have := s.Memory.TotalBytes
			if s.Memory.InstalledBytes != nil {
				have = *s.Memory.InstalledBytes
			}
			if gb := float64(have) / (1 << 30); gb < p.Min.MemoryGB {
				add(&fail, fmt.Sprintf("memory: %.0f GB installed, at least %.0f GB required", gb, p.Min.MemoryGB))
			}
		}
		if p.Min.LogicalCPUs > 0 && s.CPU != nil {
			n := 0
			for _, l := range s.CPU.Logical {
				if l.Online {
					n++
				}
			}
			if n < p.Min.LogicalCPUs {
				add(&fail, fmt.Sprintf("cpu: %d logical CPUs online, at least %d required", n, p.Min.LogicalCPUs))
			}
		}
		if n := len(physicalDisks(s)); p.Min.Disks > 0 && n < p.Min.Disks {
			add(&fail, fmt.Sprintf("storage: %d disks, at least %d required", n, p.Min.Disks))
		}
	}

	r.Failures, r.Remarks = nonNil(fail), nonNil(remark)
	switch {
	case len(fail) > 0:
		r.Verdict = Fail
	case len(incomplete) > 0:
		r.Verdict = Incomplete
		r.Failures = incomplete
	case len(remark) > 0:
		r.Verdict = Remarks
	default:
		r.Verdict = Pass
	}
	if len(fail) > 0 && len(incomplete) > 0 {
		r.Remarks = append(r.Remarks, incomplete...)
	}
}

// counterReason: a stress reason about counter growth ("mem.edac.mc0.ce +3"),
// judged over the whole run instead.
func counterReason(s string) bool {
	f := strings.Fields(s)
	return len(f) == 2 && strings.HasPrefix(f[1], "+") && strings.Contains(f[0], ".")
}

func listed(list []string, mod model.ModuleID, code string) bool {
	for _, e := range list {
		if e == code || e == string(mod)+"/"+code {
			return true
		}
	}
	return false
}

func matchesAny(k string, subs []string) bool {
	for _, s := range subs {
		if s != "" && strings.Contains(k, s) {
			return true
		}
	}
	return false
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
