package sysfs

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// CPUStat holds the jiffy counters of one CPU line in /proc/stat.
type CPUStat struct {
	Busy, Total uint64
}

// ProcStat is the parsed subset of /proc/stat.
type ProcStat struct {
	All   CPUStat
	CPUs  map[int]CPUStat // only online CPUs appear
	BTime int64           // boot time, seconds since epoch
}

// ParseProcStat parses /proc/stat. Busy excludes idle and iowait; guest time
// is already included in user/nice and is not added again.
func ParseProcStat(data []byte) (ProcStat, error) {
	ps := ProcStat{CPUs: map[int]CPUStat{}}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	seen := false
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		switch {
		case fields[0] == "btime" && len(fields) >= 2:
			ps.BTime, _ = strconv.ParseInt(fields[1], 10, 64)
		case strings.HasPrefix(fields[0], "cpu"):
			if len(fields) < 5 {
				return ps, fmt.Errorf("/proc/stat: short line %q", sc.Text())
			}
			var v [8]uint64 // user nice system idle iowait irq softirq steal
			for i := 0; i < 8 && i+1 < len(fields); i++ {
				n, err := strconv.ParseUint(fields[i+1], 10, 64)
				if err != nil {
					return ps, fmt.Errorf("/proc/stat: %q: %w", fields[0], err)
				}
				v[i] = n
			}
			idle := v[3] + v[4]
			total := v[0] + v[1] + v[2] + v[3] + v[4] + v[5] + v[6] + v[7]
			st := CPUStat{Busy: total - idle, Total: total}
			if fields[0] == "cpu" {
				ps.All = st
				seen = true
				continue
			}
			id, err := strconv.Atoi(fields[0][3:])
			if err != nil {
				return ps, fmt.Errorf("/proc/stat: bad cpu label %q", fields[0])
			}
			ps.CPUs[id] = st
		}
	}
	if err := sc.Err(); err != nil {
		return ps, err
	}
	if !seen {
		return ps, fmt.Errorf("/proc/stat: no cpu line")
	}
	return ps, nil
}

// Utilization returns busy percent between two samples (nil if no time passed
// or the counters went backwards, e.g. CPU hotplug).
func Utilization(prev, cur CPUStat) *float64 {
	if cur.Total <= prev.Total || cur.Busy < prev.Busy {
		return nil
	}
	u := float64(cur.Busy-prev.Busy) / float64(cur.Total-prev.Total) * 100
	if u > 100 {
		u = 100
	}
	return &u
}

// CPUInfo is the parsed subset of /proc/cpuinfo.
type CPUInfo struct {
	Vendor    string
	Model     string
	Microcode string
	Flags     []string
	MHz       map[int]float64 // per processor "cpu MHz" (x86)
	// Family, model number and stepping (x86 "cpu family"/"model"/
	// "stepping"; arm64 "CPU architecture"/"CPU variant"/"CPU revision").
	Family, ModelNum, Stepping string
	MicrocodeByCPU             map[int]string
	// arm64
	Implementer, Part string
}

// ParseCPUInfo parses /proc/cpuinfo for x86 and arm64.
func ParseCPUInfo(data []byte) CPUInfo {
	ci := CPUInfo{MHz: map[int]float64{}, MicrocodeByCPU: map[int]string{}}
	proc := -1
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "processor":
			proc, _ = strconv.Atoi(v)
		case "vendor_id":
			setOnce(&ci.Vendor, v)
		case "model name":
			setOnce(&ci.Model, v)
		case "microcode":
			setOnce(&ci.Microcode, v)
			if proc >= 0 {
				ci.MicrocodeByCPU[proc] = v
			}
		case "cpu family", "CPU architecture":
			setOnce(&ci.Family, v)
		case "model", "CPU variant":
			setOnce(&ci.ModelNum, v)
		case "stepping", "CPU revision":
			setOnce(&ci.Stepping, v)
		case "flags", "Features":
			if ci.Flags == nil {
				ci.Flags = strings.Fields(v)
			}
		case "cpu MHz":
			if f, err := strconv.ParseFloat(v, 64); err == nil && proc >= 0 {
				ci.MHz[proc] = f
			}
		case "CPU implementer":
			setOnce(&ci.Implementer, v)
		case "CPU part":
			setOnce(&ci.Part, v)
		}
	}
	if ci.Vendor == "" && ci.Implementer != "" {
		ci.Vendor, ci.Model = armName(ci.Implementer, ci.Part)
	}
	return ci
}

func setOnce(dst *string, v string) {
	if *dst == "" {
		*dst = v
	}
}

// armName maps MIDR implementer/part codes to names for common server cores.
func armName(impl, part string) (vendor, model string) {
	impl, part = strings.ToLower(impl), strings.ToLower(part)
	vendors := map[string]string{"0x41": "ARM", "0x42": "Broadcom", "0x43": "Cavium", "0x46": "Fujitsu",
		"0x48": "HiSilicon", "0x4e": "NVIDIA", "0x50": "APM", "0x51": "Qualcomm", "0x61": "Apple", "0xc0": "Ampere"}
	parts := map[string]string{
		"0x41/0xd08": "Cortex-A72", "0x41/0xd0b": "Cortex-A76", "0x41/0xd0c": "Neoverse-N1",
		"0x41/0xd40": "Neoverse-V1", "0x41/0xd49": "Neoverse-N2", "0x41/0xd4f": "Neoverse-V2",
		"0x41/0xd8e": "Neoverse-N3", "0x41/0xd84": "Neoverse-V3", "0x48/0xd01": "Kunpeng-920",
		"0xc0/0xac3": "AmpereOne", "0x43/0x0af": "ThunderX2", "0x46/0x001": "A64FX",
	}
	vendor = vendors[impl]
	if vendor == "" {
		vendor = "implementer " + impl
	}
	model = parts[impl+"/"+part]
	if model == "" {
		model = vendor + " part " + part
	}
	return vendor, model
}

// VirtFlags returns the virtualization-related CPU flags present.
func VirtFlags(flags []string) []string {
	want := map[string]bool{"vmx": true, "svm": true, "ept": true, "npt": true, "vpid": true, "avic": true, "x2avic": true, "hypervisor": true}
	out := []string{}
	for _, f := range flags {
		if want[f] {
			out = append(out, f)
		}
	}
	return out
}

// Interrupts maps an interrupt name (e.g. "MCE", "TRM") to per-CPU counts.
// Only CPUs that were online when /proc/interrupts was read appear.
type Interrupts map[string]map[int]uint64

// ParseInterrupts parses /proc/interrupts.
func ParseInterrupts(data []byte) (Interrupts, error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("/proc/interrupts: empty")
	}
	var cols []int
	for _, h := range strings.Fields(lines[0]) {
		id, err := strconv.Atoi(strings.TrimPrefix(h, "CPU"))
		if err != nil {
			return nil, fmt.Errorf("/proc/interrupts: bad header %q", h)
		}
		cols = append(cols, id)
	}
	out := Interrupts{}
	for _, l := range lines[1:] {
		name, rest, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		fields := strings.Fields(rest)
		m := map[int]uint64{}
		for i := 0; i < len(cols) && i < len(fields); i++ {
			n, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				break
			}
			m[cols[i]] = n
		}
		out[name] = m
	}
	return out, nil
}
