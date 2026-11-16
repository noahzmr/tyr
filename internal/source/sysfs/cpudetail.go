package sysfs

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/hostfs"
)

// CPUCache is one cache level/type with the number of instances (distinct
// shared_cpu_list values across CPUs).
type CPUCache struct {
	Level     int
	Type      string // Data, Instruction, Unified
	SizeBytes uint64 // per instance
	Ways      int
	LineBytes int
	Instances int
	SharedBy  string // shared_cpu_list of the first instance, e.g. "0,8"
}

// ReadCaches reads cpuN/cache/index*/ for every CPU and aggregates by
// level and type.
func ReadCaches(f hostfs.FS) []CPUCache {
	dirs, _ := f.Glob(cpuRoot + "/cpu[0-9]*/cache/index[0-9]*")
	type key struct {
		level int
		typ   string
	}
	agg := map[key]*CPUCache{}
	seen := map[key]map[string]bool{}
	for _, d := range dirs {
		lvl, err := hostfs.Int(f, d+"/level")
		if err != nil {
			continue
		}
		k := key{int(lvl), hostfs.StringOr(f, d+"/type", "")}
		shared := hostfs.StringOr(f, d+"/shared_cpu_list", path.Dir(path.Dir(d)))
		c := agg[k]
		if c == nil {
			c = &CPUCache{Level: k.level, Type: k.typ, SharedBy: shared}
			c.SizeBytes = parseCacheSize(hostfs.StringOr(f, d+"/size", ""))
			if v, err := hostfs.Int(f, d+"/ways_of_associativity"); err == nil {
				c.Ways = int(v)
			}
			if v, err := hostfs.Int(f, d+"/coherency_line_size"); err == nil {
				c.LineBytes = int(v)
			}
			agg[k], seen[k] = c, map[string]bool{}
		}
		if !seen[k][shared] {
			seen[k][shared] = true
			c.Instances++
		}
	}
	out := make([]CPUCache, 0, len(agg))
	for _, c := range agg {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level < out[j].Level
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// parseCacheSize parses "48K", "1024K", "32M".
func parseCacheSize(s string) uint64 {
	s = strings.TrimSpace(s)
	mult := uint64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return v * mult
}

// Vulnerability is one entry of /sys/devices/system/cpu/vulnerabilities.
type Vulnerability struct{ Name, State string }

// ReadVulnerabilities returns the kernel's view of CPU security issues.
func ReadVulnerabilities(f hostfs.FS) []Vulnerability {
	files, _ := f.Glob(cpuRoot + "/vulnerabilities/*")
	sort.Strings(files)
	var out []Vulnerability
	for _, p := range files {
		if s, err := hostfs.String(f, p); err == nil {
			out = append(out, Vulnerability{Name: path.Base(p), State: s})
		}
	}
	return out
}

// IdleState is one cpuidle state of a CPU (C-state).
type IdleState struct {
	Name       string
	Desc       string
	LatencyUs  uint64
	Usage      uint64 // entries since boot
	TimeUs     uint64 // residency since boot
	Disabled   bool
	HasCounter bool
}

// ReadIdleStates reads cpuN/cpuidle/state*.
func ReadIdleStates(f hostfs.FS, cpu int) []IdleState {
	dirs, _ := f.Glob(fmt.Sprintf("%s/cpu%d/cpuidle/state[0-9]*", cpuRoot, cpu))
	sort.Slice(dirs, func(i, j int) bool { return natLess(dirs[i], dirs[j]) })
	var out []IdleState
	for _, d := range dirs {
		s := IdleState{Name: hostfs.StringOr(f, d+"/name", path.Base(d)), Desc: hostfs.StringOr(f, d+"/desc", "")}
		s.LatencyUs, _ = hostfs.Uint(f, d+"/latency")
		u, e1 := hostfs.Uint(f, d+"/usage")
		t, e2 := hostfs.Uint(f, d+"/time")
		s.Usage, s.TimeUs, s.HasCounter = u, t, e1 == nil && e2 == nil
		if v, err := hostfs.Uint(f, d+"/disable"); err == nil && v != 0 {
			s.Disabled = true
		}
		out = append(out, s)
	}
	return out
}

// CPUExtra are per-CPU details beyond topology and current frequency.
type CPUExtra struct {
	MinMHz, MaxMHz *float64 // hardware limits (cpuinfo_min/max_freq)
	Siblings       string   // SMT siblings (core_cpus_list / thread_siblings_list)
	PrefRank       *int     // AMD preferred-core ranking (higher = faster core)
	Capacity       *int     // arm64 cpu_capacity (big.LITTLE)
}

// ReadCPUExtra reads per-CPU limits, siblings and core ranking.
func ReadCPUExtra(f hostfs.FS, cpu int) CPUExtra {
	base := fmt.Sprintf("%s/cpu%d", cpuRoot, cpu)
	var x CPUExtra
	x.MinMHz = khzToMHz(f, base+"/cpufreq/cpuinfo_min_freq")
	x.MaxMHz = khzToMHz(f, base+"/cpufreq/cpuinfo_max_freq")
	x.Siblings = hostfs.StringOr(f, base+"/topology/core_cpus_list", hostfs.StringOr(f, base+"/topology/thread_siblings_list", ""))
	if v, err := hostfs.Int(f, base+"/cpufreq/amd_pstate_prefcore_ranking"); err == nil {
		n := int(v)
		x.PrefRank = &n
	}
	if v, err := hostfs.Int(f, base+"/cpu_capacity"); err == nil {
		n := int(v)
		x.Capacity = &n
	}
	return x
}

// CPUPolicy is system-wide CPU configuration.
type CPUPolicy struct {
	SMTControl         string // on, off, forceoff, notsupported, notimplemented
	SMTActive          *bool
	Isolated, NoHzFull string // CPU lists
	Boost              *bool
	EPP                string // energy_performance_preference of cpu0
	HybridP, HybridE   string // Intel hybrid: P-core and E-core CPU lists
}

// ReadCPUPolicy reads SMT, isolation, boost, EPP and hybrid core lists.
func ReadCPUPolicy(f hostfs.FS) CPUPolicy {
	p := CPUPolicy{
		SMTControl: hostfs.StringOr(f, cpuRoot+"/smt/control", ""),
		Isolated:   hostfs.StringOr(f, cpuRoot+"/isolated", ""),
		NoHzFull:   hostfs.StringOr(f, cpuRoot+"/nohz_full", ""),
		EPP:        hostfs.StringOr(f, cpuRoot+"/cpu0/cpufreq/energy_performance_preference", ""),
		HybridP:    hostfs.StringOr(f, "/sys/devices/cpu_core/cpus", ""),
		HybridE:    hostfs.StringOr(f, "/sys/devices/cpu_atom/cpus", ""),
	}
	if p.NoHzFull == "(null)" {
		p.NoHzFull = ""
	}
	if v, err := hostfs.Uint(f, cpuRoot+"/smt/active"); err == nil {
		b := v == 1
		p.SMTActive = &b
	}
	for _, b := range []string{cpuRoot + "/cpufreq/boost", cpuRoot + "/cpu0/cpufreq/boost"} {
		if v, err := hostfs.Uint(f, b); err == nil {
			on := v == 1
			p.Boost = &on
			break
		}
	}
	if p.Boost == nil {
		if v, err := hostfs.Uint(f, cpuRoot+"/intel_pstate/no_turbo"); err == nil {
			on := v == 0
			p.Boost = &on
		}
	}
	return p
}
