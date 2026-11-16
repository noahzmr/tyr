// Package sysfs reads hardware state from /sys and /proc.
package sysfs

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/cpuset"
	"github.com/noahzmr/tyr/internal/hostfs"
)

const cpuRoot = "/sys/devices/system/cpu"

// CPUMasks are the kernel's CPU state masks.
type CPUMasks struct {
	Present, Possible, Online, Offline cpuset.Set
}

// ReadCPUMasks reads present/possible/online/offline. Missing files yield
// empty sets; only a missing "online" mask is an error.
func ReadCPUMasks(f hostfs.FS) (CPUMasks, error) {
	var m CPUMasks
	var err error
	read := func(name string) cpuset.Set {
		s, e := hostfs.String(f, cpuRoot+"/"+name)
		if e != nil {
			if name == "online" {
				err = e
			}
			return cpuset.Set{}
		}
		set, e := cpuset.Parse(s)
		if e != nil {
			err = fmt.Errorf("%s: %w", name, e)
		}
		return set
	}
	m.Present, m.Possible, m.Online, m.Offline = read("present"), read("possible"), read("online"), read("offline")
	return m, err
}

// CPUTopo is the topology of one logical CPU. Fields are -1 when unknown;
// the kernel removes topology/ for offline CPUs.
type CPUTopo struct {
	CoreID, PackageID, DieID, Node int
}

// ReadCPUTopo reads cpuN/topology and the NUMA node link.
func ReadCPUTopo(f hostfs.FS, id int) CPUTopo {
	base := fmt.Sprintf("%s/cpu%d", cpuRoot, id)
	t := CPUTopo{CoreID: -1, PackageID: -1, DieID: -1, Node: -1}
	if v, err := hostfs.Int(f, base+"/topology/core_id"); err == nil {
		t.CoreID = int(v)
	}
	if v, err := hostfs.Int(f, base+"/topology/physical_package_id"); err == nil {
		t.PackageID = int(v)
	}
	if v, err := hostfs.Int(f, base+"/topology/die_id"); err == nil {
		t.DieID = int(v)
	}
	if m, _ := f.Glob(base + "/node[0-9]*"); len(m) > 0 {
		if n, err := strconv.Atoi(strings.TrimPrefix(path.Base(m[0]), "node")); err == nil {
			t.Node = n
		}
	}
	return t
}

// NodeCPUs maps NUMA node -> CPU list from /sys/devices/system/node.
func NodeCPUs(f hostfs.FS) map[int]cpuset.Set {
	out := map[int]cpuset.Set{}
	dirs, _ := f.Glob("/sys/devices/system/node/node[0-9]*")
	for _, d := range dirs {
		n, err := strconv.Atoi(strings.TrimPrefix(path.Base(d), "node"))
		if err != nil {
			continue
		}
		if s, err := hostfs.String(f, d+"/cpulist"); err == nil {
			if set, err := cpuset.Parse(s); err == nil {
				out[n] = set
			}
		}
	}
	return out
}

// CPUFreq holds cpufreq data for one CPU. Frequencies are in MHz.
type CPUFreq struct {
	CurMHz, MaxMHz, BaseMHz *float64
	Governor, Driver        string
}

func khzToMHz(f hostfs.FS, p string) *float64 {
	v, err := hostfs.Uint(f, p)
	if err != nil || v == 0 {
		return nil
	}
	mhz := float64(v) / 1000
	return &mhz
}

// ReadCPUFreq reads cpuN/cpufreq. Absent in many VMs.
func ReadCPUFreq(f hostfs.FS, id int) (CPUFreq, bool) {
	base := fmt.Sprintf("%s/cpu%d", cpuRoot, id)
	var c CPUFreq
	if !hostfs.Exists(f, base+"/cpufreq") {
		return c, false
	}
	c.CurMHz = khzToMHz(f, base+"/cpufreq/scaling_cur_freq")
	c.MaxMHz = khzToMHz(f, base+"/cpufreq/cpuinfo_max_freq")
	c.BaseMHz = khzToMHz(f, base+"/cpufreq/base_frequency") // intel_pstate
	if c.BaseMHz == nil {
		// ACPI CPPC (amd_pstate, arm64): nominal_freq is already in MHz.
		if v, err := hostfs.Uint(f, base+"/acpi_cppc/nominal_freq"); err == nil && v > 0 {
			mhz := float64(v)
			c.BaseMHz = &mhz
		}
	}
	c.Governor = hostfs.StringOr(f, base+"/cpufreq/scaling_governor", "")
	c.Driver = hostfs.StringOr(f, base+"/cpufreq/scaling_driver", "")
	return c, true
}

// ReadThrottle returns core_throttle_count and package_throttle_count
// (Intel only; ok=false if the thermal_throttle directory is missing).
func ReadThrottle(f hostfs.FS, id int) (core, pkg uint64, ok bool) {
	base := fmt.Sprintf("%s/cpu%d/thermal_throttle", cpuRoot, id)
	c, err := hostfs.Uint(f, base+"/core_throttle_count")
	if err != nil {
		return 0, 0, false
	}
	p, _ := hostfs.Uint(f, base+"/package_throttle_count")
	return c, p, true
}

// ReadPPIN returns the Intel Protected Processor Inventory Number of the
// package cpu belongs to (topology/ppin, kernel >= 6.5, root only).
func ReadPPIN(f hostfs.FS, cpu int) string {
	return hostfs.StringOr(f, fmt.Sprintf("%s/cpu%d/topology/ppin", cpuRoot, cpu), "")
}
