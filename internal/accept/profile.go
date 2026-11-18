// Package accept is Tyr's acceptance test: a health check, the stress tests
// one after another, a second health check, judged against a limits profile
// with one verdict: is this machine fit for production?
package accept

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Duration is a time.Duration written as "90m" in JSON.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration: want a string like \"90m\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Profile is how long each part runs and what the machine must meet.
type Profile struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Durations per stress component (cpu, memory, storage, network); a
	// missing or zero entry skips the component. The storage scan reads
	// all disks for at most this long; network runs only with a peer.
	Durations map[string]Duration `json:"durations"`
	// CPUEngine: torture (Prime95-style, default) or builtin.
	CPUEngine     string `json:"cpu_engine,omitempty"`
	FFT           string `json:"fft,omitempty"`            // torture: blend (default), small, large
	MemoryPercent int    `json:"memory_percent,omitempty"` // of available memory (default 90)
	// StorageUSB also scans USB disks (left out by default: the boot
	// stick, the results stick, BMC virtual media).
	StorageUSB bool `json:"storage_include_usb,omitempty"`
	// FailOnWarn: finding codes ("pending_sectors" or
	// "storage/pending_sectors") whose warnings fail the test. Critical
	// findings always fail unless ignored.
	FailOnWarn []string `json:"fail_on_warn"`
	// Ignore: finding codes that do not count at all.
	Ignore []string `json:"ignore"`
	// FailCounters: counters (substrings of the counter name, e.g. ".ce"
	// for correctable ECC errors) whose growth during the test fails it.
	// Hardware error counters (machine checks, uncorrectable ECC, SMART,
	// I/O errors) always fail.
	FailCounters []string `json:"fail_counters"`
	// MaxTempC: highest temperature allowed under load, per kind (cpu,
	// disk, dimm). Without an entry the sensors' own limits apply.
	MaxTempC map[string]float64 `json:"max_temp_c,omitempty"`
	Min      Minimums           `json:"min"`
	// RequireEncryption: unencrypted filesystems, data disks and swap
	// fail the test (not_encrypted, swap_not_encrypted).
	RequireEncryption bool `json:"require_encryption,omitempty"`
}

// Minimums the machine must have; zero = not checked.
type Minimums struct {
	MemoryGB    float64 `json:"memory_gb,omitempty"`
	LogicalCPUs int     `json:"logical_cpus,omitempty"`
	Disks       int     `json:"disks,omitempty"` // physical disks, USB not counted
}

// Components in the order they run.
var Components = []string{"cpu", "memory", "storage", "network"}

// failOnWarn: warnings that mean worn or failing hardware, not a setting.
var failOnWarn = []string{
	// storage
	"pending_sectors", "reallocated_sectors", "offline_uncorrectable", "reported_uncorrectable", "media_errors",
	"scsi_uncorrected", "grown_defects", "crc_errors", "smart_failed", "self_test_failed", "wear", "spare_low",
	"nvme_critical_warning", "md_degraded", "md_member_failed", "raid_vd", "raid_pd", "raid_battery", "kernel_io_errors",
	// cpu, memory
	"mce", "cpu_missing", "socket_disabled", "ecc_correctable", "ecc_uncorrectable", "hardware_corrupted",
	"capacity_mismatch", "memory_offline",
	// network
	"transceiver", "half_duplex", "link_flap",
	// accelerators
	"accel_ecc_ce", "accel_ecc_ue", "retired_pages", "remap_pending", "remap_failed", "pcie_width", "accel_driver_errors",
	// power, cooling
	"psu_status", "psu_redundancy", "fan_failed", "fan_low",
}

// Profiles are the built-in profiles.
var Profiles = map[string]Profile{
	"quick": {Name: "quick", Description: "health check plus a short load per component: finds dead parts, not marginal ones (about 30 minutes)",
		Durations: map[string]Duration{"cpu": Duration(10 * time.Minute), "memory": Duration(10 * time.Minute), "storage": Duration(5 * time.Minute), "network": Duration(2 * time.Minute)}},
	"burnin": {Name: "burnin", Description: "burn-in: long enough for marginal CPUs, DIMMs and disks to show (about 4 hours)",
		Durations: map[string]Duration{"cpu": Duration(90 * time.Minute), "memory": Duration(90 * time.Minute), "storage": Duration(45 * time.Minute), "network": Duration(10 * time.Minute)}},
	"extended": {Name: "extended", Description: "extended burn-in for new servers before production (about 24 hours)",
		Durations: map[string]Duration{"cpu": Duration(8 * time.Hour), "memory": Duration(8 * time.Hour), "storage": Duration(6 * time.Hour), "network": Duration(30 * time.Minute)}},
}

// Builtin returns a built-in profile with the default limits filled in.
func Builtin(name string) (Profile, error) {
	p, ok := Profiles[name]
	if !ok {
		names := make([]string, 0, len(Profiles))
		for n := range Profiles {
			names = append(names, n)
		}
		sort.Strings(names)
		return Profile{}, fmt.Errorf("unknown profile %q (built in: %s)", name, strings.Join(names, ", "))
	}
	d := map[string]Duration{}
	for k, v := range p.Durations {
		d[k] = v
	}
	p.Durations = d
	p.CPUEngine, p.FFT, p.MemoryPercent = "torture", "blend", 90
	p.FailOnWarn = append([]string(nil), failOnWarn...)
	p.Ignore = []string{"unclean_shutdown"}
	p.FailCounters = []string{".ce", "crc", "link_failures"}
	p.MaxTempC = map[string]float64{}
	return p, nil
}

// Merge applies a limits file on top of p: fields present in the file
// replace p's; lists are replaced, durations and temperatures are merged
// per key (encoding/json keeps the existing map entries).
func Merge(p Profile, data []byte) (Profile, error) {
	if p.Durations == nil {
		p.Durations = map[string]Duration{}
	}
	if p.MaxTempC == nil {
		p.MaxTempC = map[string]float64{}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, err
	}
	return p, p.Validate()
}

// Load reads a limits file and merges it onto p.
func Load(p Profile, path string) (Profile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	out, err := Merge(p, b)
	if err != nil {
		return p, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// Validate checks names and ranges.
func (p Profile) Validate() error {
	for k, v := range p.Durations {
		if !contains(Components, k) {
			return fmt.Errorf("durations: unknown component %q (cpu, memory, storage, network)", k)
		}
		if v < 0 {
			return fmt.Errorf("durations.%s: negative", k)
		}
	}
	for k := range p.MaxTempC {
		if k != "cpu" && k != "disk" && k != "dimm" {
			return fmt.Errorf("max_temp_c: unknown kind %q (cpu, disk, dimm)", k)
		}
	}
	if p.CPUEngine != "" && p.CPUEngine != "torture" && p.CPUEngine != "builtin" {
		return fmt.Errorf("cpu_engine: %q (torture or builtin)", p.CPUEngine)
	}
	if p.MemoryPercent < 0 || p.MemoryPercent > 100 {
		return fmt.Errorf("memory_percent: %d (1-100)", p.MemoryPercent)
	}
	return nil
}

// Total is the planned stress time (network only when it will run).
func (p Profile) Total(network bool) time.Duration {
	var t time.Duration
	for k, v := range p.Durations {
		if k != "network" || network {
			t += time.Duration(v)
		}
	}
	return t
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
