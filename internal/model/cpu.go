package model

import (
	"fmt"
	"time"
)

// CPU is the CPU module section.
type CPU struct {
	Header
	Vendor       string        `json:"vendor"`
	Model        string        `json:"model"`
	Microcode    string        `json:"microcode,omitempty"`
	Arch         string        `json:"arch"`
	SocketCount  int           `json:"socket_count"`
	CoreCount    int           `json:"core_count"`
	ThreadCount  int           `json:"thread_count"`
	Governor     string        `json:"governor,omitempty"`
	FreqDriver   string        `json:"freq_driver,omitempty"`
	BaseFreqMHz  *float64      `json:"base_freq_mhz"`
	MaxFreqMHz   *float64      `json:"max_freq_mhz"`
	VirtFlags    []string      `json:"virt_flags"`
	Sockets      []CPUSocket   `json:"sockets"`
	Topology     CPUTopology   `json:"topology"`
	Logical      []LogicalCPU  `json:"logical"`
	PackageTemps []Sensor      `json:"package_temps"`
	MaxTempC     *float64      `json:"max_temp_celsius"`
	CoreTest     *CoreTestInfo `json:"core_test,omitempty"`

	// Detailed identification and configuration.
	Family          string             `json:"family,omitempty"`
	ModelNumber     string             `json:"model_number,omitempty"`
	Stepping        string             `json:"stepping,omitempty"`
	Flags           []string           `json:"flags"`
	Caches          []CPUCache         `json:"caches"`
	Vulnerabilities []CPUVulnerability `json:"vulnerabilities"`
	SMTControl      string             `json:"smt_control,omitempty"`
	SMTActive       *bool              `json:"smt_active"`
	Isolated        string             `json:"isolated_cpus,omitempty"`
	NoHzFull        string             `json:"nohz_full_cpus,omitempty"`
	Boost           *bool              `json:"boost"`
	EPP             string             `json:"energy_performance_preference,omitempty"`

	// MCEHistory are machine check records from this boot (kernel log),
	// earlier boots (journal), crash records (pstore) and rasdaemon, oldest
	// first; CrashRecords are kernel panics saved in pstore.
	MCEHistory   []MCERecord   `json:"mce_history,omitempty"`
	CrashRecords []CrashRecord `json:"crash_records,omitempty"`
}

// OfflineInfo describes a logical CPU the kernel does not run.
type OfflineInfo struct {
	Since time.Time `json:"since,omitzero"` // "smpboot: CPU N is now offline" this boot
	// How: "runtime" (taken offline after boot: echo 0 > …/online or a RAS
	// daemon), "boot" (never brought up: maxcpus=/nr_cpus=), "" unknown.
	How        string `json:"how,omitempty"`
	AfterMCE   bool   `json:"after_mce,omitempty"`  // a machine check on it shortly before
	Persistent string `json:"persistent,omitempty"` // file that offlines it at boot; "" = back online after a reboot
	// Sibling is the other hardware thread of the same physical core
	// (inferred from the numbering of the complete cores when sysfs no
	// longer lists it); SiblingOnline means the core still runs work.
	Sibling         *int   `json:"smt_sibling,omitempty"`
	SiblingOnline   bool   `json:"smt_sibling_online,omitempty"`
	SiblingInferred bool   `json:"smt_sibling_inferred,omitempty"`
	L3SharedWith    string `json:"l3_shared_with,omitempty"` // CPUs sharing its L3 (from the sibling)
}

// MCERecord is one machine check: where it was reported and which unit.
type MCERecord struct {
	Time   time.Time `json:"time,omitzero"`
	Source string    `json:"source"` // "kernel log", "journal (boot -1)", "pstore", "rasdaemon"
	CPU    int       `json:"cpu"`    // -1 unknown
	Bank   int       `json:"bank"`   // -1 unknown
	Unit   string    `json:"unit,omitempty"`
	// Scope: "core" (a unit inside the core: caches L1/L2, load-store,
	// fetch, execution), "shared" (L3, memory controller, fabric, I/O: the
	// CPU number only says who reported it), "" unknown.
	Scope    string `json:"scope,omitempty"`
	Severity Status `json:"severity"`
	Text     string `json:"text"`
}

// CrashRecord is a kernel panic saved in pstore before a reboot.
type CrashRecord struct {
	Time   time.Time `json:"time,omitzero"`
	File   string    `json:"file"`
	Reason string    `json:"reason"` // "Kernel panic - not syncing: Fatal machine check"
	CPU    int       `json:"cpu"`    // CPU of the machine check, -1 unknown
	Lines  []string  `json:"lines,omitempty"`
}

// Suspects maps every online CPU whose SMT sibling is offline to that
// sibling: the physical core is suspect (someone took a thread offline),
// so stress and core tests leave it alone.
func (c *CPU) Suspects() map[int]int {
	out := map[int]int{}
	if c == nil {
		return out
	}
	for _, l := range c.Logical {
		if o := l.Offline; o != nil && o.Sibling != nil && o.SiblingOnline {
			out[*o.Sibling] = l.ID
		}
	}
	return out
}

// CPUCache is one cache level/type. SizeBytes is per instance.
type CPUCache struct {
	Level     int    `json:"level"`
	Type      string `json:"type"`
	SizeBytes uint64 `json:"size_bytes"`
	Ways      int    `json:"ways,omitempty"`
	LineBytes int    `json:"line_bytes,omitempty"`
	Instances int    `json:"instances"`
	SharedBy  string `json:"shared_cpu_list,omitempty"` // of the first instance
}

// CPUVulnerability is the kernel's state for one CPU security issue.
// Status: OK (not affected / mitigated), WARN (vulnerable), UNKNOWN.
type CPUVulnerability struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Status Status `json:"status"`
}

// IdleState is one cpuidle state (C-state) of a logical CPU.
type IdleState struct {
	Name      string  `json:"name"`
	Desc      string  `json:"desc,omitempty"`
	LatencyUs uint64  `json:"latency_us"`
	Usage     *uint64 `json:"usage"`
	TimeUs    *uint64 `json:"time_us"`
	Disabled  bool    `json:"disabled,omitempty"`
}

// CPUSocket is a physical socket as described by SMBIOS type 4 and sysfs.
type CPUSocket struct {
	ID          int      `json:"id"` // physical_package_id, -1 if unknown
	Designation string   `json:"designation,omitempty"`
	Populated   *bool    `json:"populated"`
	Enabled     *bool    `json:"enabled"`
	StatusText  string   `json:"status_text,omitempty"` // raw SMBIOS status
	Version     string   `json:"version,omitempty"`
	Cores       *int     `json:"cores"`
	Threads     *int     `json:"threads"`
	MaxSpeedMHz *float64 `json:"max_speed_mhz"`
	Serial      string   `json:"serial,omitempty"`
	PartNumber  string   `json:"part_number,omitempty"`
	AssetTag    string   `json:"asset_tag,omitempty"`
	PPIN        string   `json:"ppin,omitempty"` // Intel Protected Processor Inventory Number
	Online      int      `json:"online_threads"`
	Status      Status   `json:"status"`
}

// CPUTopology contains the kernel's CPU masks as list strings ("0-63").
type CPUTopology struct {
	Present  string `json:"present"`
	Possible string `json:"possible"`
	Online   string `json:"online"`
	Offline  string `json:"offline"`
}

// CoreTestState is the result of the quick per-core check.
type CoreTestState string

const (
	CoreTestNotRun  CoreTestState = ""
	CoreTestPass    CoreTestState = "PASS"
	CoreTestFail    CoreTestState = "FAIL"
	CoreTestTimeout CoreTestState = "TIMEOUT"
	CoreTestSkip    CoreTestState = "SKIP"
)

// CoreTestInfo summarizes the quick core test.
type CoreTestInfo struct {
	DurationMS int64 `json:"duration_ms"`
	TimeoutMS  int64 `json:"timeout_ms"`
	Passed     int   `json:"passed"`
	Failed     int   `json:"failed"`
	TimedOut   int   `json:"timed_out"`
	Skipped    int   `json:"skipped"`
}

// LogicalCPU is one hardware thread.
type LogicalCPU struct {
	ID                 int           `json:"id"`
	CoreID             int           `json:"core_id"`   // -1 if unknown (e.g. offline)
	SocketID           int           `json:"socket_id"` // -1 if unknown
	NUMANode           int           `json:"numa_node"` // -1 if unknown
	Present            bool          `json:"present"`
	Online             bool          `json:"online"`
	UtilizationPercent *float64      `json:"utilization_percent"`
	FreqMHz            *float64      `json:"freq_mhz"`
	TempCelsius        *float64      `json:"temp_celsius"`
	ThrottleCount      *uint64       `json:"throttle_count"`    // core_throttle_count or TRM interrupts
	MCEExceptions      *uint64       `json:"mce_exceptions"`    // /proc/interrupts MCE
	MCELogged          *uint64       `json:"mce_logged_events"` // kernel log machine check records
	CoreTest           CoreTestState `json:"core_test,omitempty"`
	CoreTestDetail     string        `json:"core_test_detail,omitempty"`
	Offline            *OfflineInfo  `json:"offline,omitempty"`
	Status             Status        `json:"status"`

	MinFreqMHz *float64    `json:"min_freq_mhz"` // hardware limits of this CPU
	MaxFreqMHz *float64    `json:"max_freq_mhz"`
	Siblings   string      `json:"smt_siblings,omitempty"`
	CoreType   string      `json:"core_type,omitempty"`      // Intel hybrid "P"/"E", arm64 "big"/"little"
	PrefRank   *int        `json:"preferred_rank,omitempty"` // AMD preferred-core ranking (higher = faster)
	Microcode  string      `json:"microcode,omitempty"`
	IdleStates []IdleState `json:"idle_states,omitempty"`

	// Raw /proc/stat jiffies kept for utilization deltas between refreshes.
	StatBusy  uint64 `json:"-"`
	StatTotal uint64 `json:"-"`
}

func (c *CPU) counters(m map[string]uint64) {
	for _, l := range c.Logical {
		if l.ThrottleCount != nil {
			m[fmt.Sprintf("throttle.cpu%d", l.ID)] = *l.ThrottleCount
		}
		if l.MCEExceptions != nil {
			m[fmt.Sprintf("mce.exceptions.cpu%d", l.ID)] = *l.MCEExceptions
		}
		if l.MCELogged != nil {
			m[fmt.Sprintf("mce.logged.cpu%d", l.ID)] = *l.MCELogged
		}
	}
}
