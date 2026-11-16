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
	SwapOutPages   int64 // other processes paged out above the background rate (reclaim, not a trip)
	SwapInPages    int64 // paged back in above the background rate (thrashing without PSI)
	OOMKills       int64
	MemAvailable   uint64
	ReserveBytes   uint64  // abort when MemAvailable drops below this (memory test)
	TestSwapBytes  uint64  // Tyr's own memory in swap (VmSwap): the test no longer tests RAM
	PSISomeAvg10   float64 // some task stalled on memory
	PSIFullAvg10   float64 // all non-idle tasks stalled on memory: thrashing
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

// RuleTempCritical trips when a temperature goes past its limit: drives,
// DIMMs and accelerators at CRIT (their own critical threshold, else
// --crit-temp); CPU packages only CPUGuardMarginC above their limit. A CPU
// throttles itself at its maximum (Tjmax / PROCHOT), so running there is
// what a stress test is for; going clearly beyond it means the CPU's own
// protection is not holding the temperature. The plan refuses to start
// when the host is already past these points.
func RuleTempCritical() Rule {
	return ruleFunc{"temp_critical", func(base *model.Snapshot, s Sample) (bool, string) {
		for _, t := range critTemps(s.Snap) {
			return true, "temperature critical: " + t
		}
		return false, ""
	}}
}

// CPUGuardMarginC is how far above its own limit a CPU package may go
// before the guard stops the test.
const CPUGuardMarginC = 5.0

func cpuGuardTemp(p model.Sensor) *float64 {
	if p.Thresholds == nil || p.Thresholds.UpperCrit == nil {
		return nil
	}
	return model.F(*p.Thresholds.UpperCrit + CPUGuardMarginC)
}

// CPUGuardTempC is the lowest CPU package guard point of a snapshot (its
// limit plus the margin), for display; nil without CPU limits.
func CPUGuardTempC(s *model.Snapshot) *float64 {
	var g *float64
	if s != nil && s.CPU != nil {
		for _, p := range s.CPU.PackageTemps {
			if v := cpuGuardTemp(p); v != nil && (g == nil || *v < *g) {
				g = v
			}
		}
	}
	return g
}

// cpuLimitC is the lowest CPU package limit (its critical threshold).
func cpuLimitC(s *model.Snapshot) *float64 {
	if g := CPUGuardTempC(s); g != nil {
		return model.F(*g - CPUGuardMarginC)
	}
	return nil
}

// critTemps lists the temperatures past the guard points of a snapshot.
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
			g := cpuGuardTemp(p)
			switch {
			case g == nil: // no limit known: the sensor's own CRIT (alarm)
				add(p.Name, p.Value, p.Status)
			case p.Value != nil && *p.Value >= *g:
				out = append(out, fmt.Sprintf("%s %.0f°C (limit %.0f°C + %.0f)", p.Name, *p.Value, *g-CPUGuardMarginC, CPUGuardMarginC))
			}
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
// Memory-pressure limits. Swapping out idle pages of other processes is
// the kernel making room for the test and does not trip; the test's own
// pages are locked in RAM.
const (
	testSwapLimit    = 64 << 20
	swapInLimitPages = 1 << 18 // 1 GiB of 4 KiB pages
)

func RuleMemoryPressure() Rule {
	return ruleFunc{"memory_pressure", func(_ *model.Snapshot, s Sample) (bool, string) {
		v := s.VM
		switch {
		case !v.Valid:
			return false, ""
		case v.OOMKills > 0:
			return true, fmt.Sprintf("the kernel OOM-killed %d processes", v.OOMKills)
		case v.ReserveBytes > 0 && v.MemAvailable < v.ReserveBytes:
			return true, fmt.Sprintf("available memory below the reserve (%d MiB < %d MiB)", v.MemAvailable>>20, v.ReserveBytes>>20)
		case v.TestSwapBytes > testSwapLimit:
			return true, fmt.Sprintf("the test's own memory is going to swap (%d MiB): RAM cannot hold it, lower --mem-percent", v.TestSwapBytes>>20)
		case v.HavePSI && v.PSIFullAvg10 > 25:
			return true, fmt.Sprintf("the host is thrashing: all tasks stalled on memory %.0f%% of the time (PSI full avg10)", v.PSIFullAvg10)
		case !v.HavePSI && v.SwapInPages > swapInLimitPages:
			return true, fmt.Sprintf("the host is thrashing: %d MiB paged back in from swap", v.SwapInPages*4/1024)
		}
		return false, ""
	}}
}
