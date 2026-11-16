package model

import (
	"sort"
	"time"
)

// ModuleID identifies a health module.
type ModuleID string

const (
	ModSystem  ModuleID = "system"
	ModCPU     ModuleID = "cpu"
	ModMemory  ModuleID = "memory"
	ModStorage ModuleID = "storage"
	ModPower   ModuleID = "power"
	ModNetwork ModuleID = "network"
	ModAccel   ModuleID = "accel"
	ModEvents  ModuleID = "events"
)

// Modules lists the health modules in display order (System is implicit).
var Modules = []ModuleID{ModCPU, ModMemory, ModStorage, ModPower, ModNetwork, ModAccel, ModEvents}

// Header is embedded in every module section; its fields are flattened into
// the section's JSON object.
type Header struct {
	Status      Status        `json:"status"`
	Summary     string        `json:"summary"`
	Findings    []Finding     `json:"findings"`
	Sources     []SourceState `json:"sources"`
	CollectedAt time.Time     `json:"collected_at"`
	DurationMS  int64         `json:"duration_ms"`
}

// Head returns the header itself; it lets every section satisfy Section.
func (h *Header) Head() *Header { return h }

// Section is implemented by every module section through the embedded Header.
type Section interface {
	Head() *Header
}

// AddFinding appends a finding.
func (h *Header) AddFinding(f Finding) { h.Findings = append(h.Findings, f) }

// Find is a convenience to append a finding from parts.
func (h *Header) Find(st Status, code, subject, source, msg string) {
	h.Findings = append(h.Findings, Finding{Status: st, Code: code, Subject: subject, Source: source, Message: msg})
}

// SetSource records (or replaces) the availability of a source.
func (h *Header) SetSource(name string, a Availability, reason string) {
	for i := range h.Sources {
		if h.Sources[i].Name == name {
			h.Sources[i].State, h.Sources[i].Reason = a, reason
			return
		}
	}
	h.Sources = append(h.Sources, SourceState{Name: name, State: a, Reason: reason})
}

// Source returns the state of a named source, if recorded.
func (h *Header) Source(name string) (SourceState, bool) {
	for _, s := range h.Sources {
		if s.Name == name {
			return s, true
		}
	}
	return SourceState{}, false
}

// Rollup sets Status to the worst finding status and sorts findings by
// severity (most severe first), keeping the collector's order within a level.
func (h *Header) Rollup() {
	sort.SliceStable(h.Findings, func(i, j int) bool { return h.Findings[i].Status > h.Findings[j].Status })
	st := StatusOK
	for _, f := range h.Findings {
		st = Worst(st, f.Status)
	}
	h.Status = st
	if h.Findings == nil {
		h.Findings = []Finding{}
	}
	if h.Sources == nil {
		h.Sources = []SourceState{}
	}
}

// Finding is a single reason for a non-OK status (or a notable OK fact).
type Finding struct {
	Status  Status `json:"status"`
	Code    string `json:"code"`
	Subject string `json:"subject"`
	Message string `json:"message"`
	Source  string `json:"source"`
}

// Key identifies a finding across snapshots (used for before/after diffs).
func (f Finding) Key() string { return f.Code + "|" + f.Subject }

// SourceState records whether a data source could be used.
type SourceState struct {
	Name   string       `json:"name"`
	State  Availability `json:"state"`
	Reason string       `json:"reason,omitempty"`
}

// SensorKind classifies a generic sensor.
type SensorKind string

const (
	KindTemperature SensorKind = "temperature"
	KindVoltage     SensorKind = "voltage"
	KindCurrent     SensorKind = "current"
	KindPower       SensorKind = "power"
	KindFan         SensorKind = "fan"
	KindOptical     SensorKind = "optical_power"
	KindOther       SensorKind = "other"
)

// Units used by Sensor.Unit.
const (
	UnitCelsius = "celsius"
	UnitVolts   = "volts"
	UnitAmps    = "amps"
	UnitWatts   = "watts"
	UnitRPM     = "rpm"
	UnitMW      = "milliwatts"
	UnitDBm     = "dbm"
)

// Sensor is a generic reading (hwmon, IPMI, transceiver DOM). Because the unit
// varies per row it is carried in a field; typed fields elsewhere carry the
// unit in their name.
type Sensor struct {
	Name       string      `json:"name"`
	Kind       SensorKind  `json:"kind"`
	Value      *float64    `json:"value"`
	Unit       string      `json:"unit"`
	Status     Status      `json:"status"`
	Thresholds *Thresholds `json:"thresholds,omitempty"`
	Source     string      `json:"source"`
	// Shared marks a reading that belongs to another component too (an
	// integrated NPU reports the CPU die temperature). Findings for it are
	// raised by the owning module only.
	Shared bool `json:"shared,omitempty"`
}

// Thresholds are limits for a reading. Origin tells where they came from:
// "hwmon", "ipmi", "smart", "module" (transceiver), or "default".
type Thresholds struct {
	LowerCrit *float64 `json:"lower_crit,omitempty"`
	LowerWarn *float64 `json:"lower_warn,omitempty"`
	UpperWarn *float64 `json:"upper_warn,omitempty"`
	UpperCrit *float64 `json:"upper_crit,omitempty"`
	Origin    string   `json:"origin"`
}

// F returns a pointer to v; handy for optional numeric fields.
func F(v float64) *float64 { return &v }

// U returns a pointer to v.
func U(v uint64) *uint64 { return &v }

// I returns a pointer to v.
func I(v int) *int { return &v }
