package stress

import (
	"fmt"
	"strings"

	"github.com/noahzmr/tyr/internal/model"
)

// Sample is one monitor reading during the load phase.
type Sample struct {
	Snap   *model.Snapshot
	Deltas map[string]int64 // counters vs baseline
	VM     VMStat           // /proc/vmstat and meminfo deltas (memory pressure)
}

// VMStat holds memory-pressure indicators relative to the baseline.
type VMStat struct {
	SwapOutPages   int64
	OOMKills       int64
	MemAvailable   uint64
	ReserveBytes   uint64 // abort when MemAvailable drops below this (memory test)
	PSISomeAvg10   float64
	HavePSI, Valid bool
}

// Rule is one guard condition; trip=true aborts the run.
type Rule interface {
	Name() string
	Eval(base *model.Snapshot, s Sample) (trip bool, reason string)
}

type ruleFunc struct {
	name string
	f    func(base *model.Snapshot, s Sample) (bool, string)
}

func (r ruleFunc) Name() string { return r.name }
func (r ruleFunc) Eval(b *model.Snapshot, s Sample) (bool, string) {
	return r.f(b, s)
}

// DefaultRules apply to every test.
func DefaultRules() []Rule {
	return []Rule{RuleTempCritical(), RuleNewMCE(), RuleNewUncorrectable()}
}

// RuleTempCritical trips when any temperature reaches CRIT (its sensor's
// critical threshold, else --crit-temp) and was not already CRIT before
// the test started (the plan refuses to start in that case).
func RuleTempCritical() Rule {
	return ruleFunc{"temp_critical", func(base *model.Snapshot, s Sample) (bool, string) {
		for _, t := range critTemps(s.Snap) {
			return true, "temperature critical: " + t
		}
		return false, ""
	}}
}

// critTemps lists CRIT temperature readings of a snapshot.
func critTemps(s *model.Snapshot) []string {
	if s == nil {
		return nil
	}
	var out []string
	add := func(name string, v *float64, st model.Status) {
		if st == model.StatusCrit && v != nil {
			out = append(out, fmt.Sprintf("%s %.0f°C", name, *v))
		}
	}
	if c := s.CPU; c != nil {
		for _, p := range c.PackageTemps {
			add(p.Name, p.Value, p.Status)
		}
	}
	if m := s.Memory; m != nil {
		for _, d := range m.DIMMs {
			if d.TempCelsius != nil && d.Status == model.StatusCrit {
				for _, f := range m.Findings {
					if f.Code == "dimm_temp" && f.Status == model.StatusCrit && strings.HasPrefix(f.Subject, d.Locator) {
						add(d.Locator, d.TempCelsius, model.StatusCrit)
					}
				}
			}
		}
	}
	if st := s.Storage; st != nil {
		for _, f := range st.Findings {
			if f.Code == "disk_temp" && f.Status == model.StatusCrit {
				out = append(out, f.Message)
			}
		}
	}
	if a := s.Accel; a != nil {
		for _, list := range [][]model.Accelerator{a.GPUs, a.NPUs} {
			for _, x := range list {
				for _, t := range x.Temps {
					if !t.Shared {
						add(x.Name+" "+t.Name, t.Value, t.Status)
					}
				}
			}
		}
	}
	return out
}

// RuleNewMCE trips on any new machine check (exceptions or logged records).
func RuleNewMCE() Rule {
	return ruleFunc{"new_mce", func(_ *model.Snapshot, s Sample) (bool, string) {
		for k, d := range s.Deltas {
			if d > 0 && strings.HasPrefix(k, "mce.") {
				return true, fmt.Sprintf("new machine check events (%s +%d)", k, d)
			}
		}
		return false, ""
	}}
}

// RuleNewUncorrectable trips on new uncorrectable memory errors or pages
// the kernel poisoned.
func RuleNewUncorrectable() Rule {
	return ruleFunc{"new_uncorrectable_ecc", func(_ *model.Snapshot, s Sample) (bool, string) {
		for k, d := range s.Deltas {
			if d > 0 && (strings.HasSuffix(k, ".ue") || k == "mem.hardware_corrupted_bytes") {
				return true, fmt.Sprintf("new uncorrectable memory error (%s +%d)", k, d)
			}
		}
		return false, ""
	}}
}

// RuleNewSMARTErrors trips when an in-scope disk's SMART error counters
// grow.
func RuleNewSMARTErrors(disks []string) Rule {
	return ruleFunc{"new_smart_errors", func(_ *model.Snapshot, s Sample) (bool, string) {
		for _, d := range disks {
			p := "disk." + d + ".smart."
			for k, v := range s.Deltas {
				if v > 0 && strings.HasPrefix(k, p) && !strings.HasSuffix(k, "power_on_hours") {
					return true, fmt.Sprintf("%s: SMART %s +%d", d, strings.TrimPrefix(k, p), v)
				}
			}
		}
		return false, ""
	}}
}

// RuleMemoryPressure protects the host during the memory test: no
// swapping, no OOM kills, and MemAvailable above the reserve.
func RuleMemoryPressure() Rule {
	return ruleFunc{"memory_pressure", func(_ *model.Snapshot, s Sample) (bool, string) {
		v := s.VM
		switch {
		case !v.Valid:
			return false, ""
		case v.OOMKills > 0:
			return true, fmt.Sprintf("the kernel OOM-killed %d processes", v.OOMKills)
		case v.SwapOutPages > 4096:
			return true, fmt.Sprintf("the host started swapping (%d MiB swapped out above its normal rate)", v.SwapOutPages*4/1024)
		case v.ReserveBytes > 0 && v.MemAvailable < v.ReserveBytes:
			return true, fmt.Sprintf("available memory below the reserve (%d MiB < %d MiB)", v.MemAvailable>>20, v.ReserveBytes>>20)
		case v.HavePSI && v.PSISomeAvg10 > 25:
			return true, fmt.Sprintf("memory pressure stall %.0f%% (PSI some avg10)", v.PSISomeAvg10)
		}
		return false, ""
	}}
}
