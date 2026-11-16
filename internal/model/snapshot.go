package model

import (
	"fmt"
	"time"
)

// Snapshot is the complete health state of the host at one point in time.
// It is the JSON document written by `tyr report --format json` and the
// baseline/after state of a stress run.
type Snapshot struct {
	SchemaVersion  string    `json:"schema_version"`
	TyrVersion     string    `json:"tyr_version"`
	Hostname       string    `json:"hostname"`
	Timestamp      time.Time `json:"timestamp"`
	Privileged     bool      `json:"privileged"`
	Virtualization string    `json:"virtualization"`
	Arch           string    `json:"arch"`
	Status         Status    `json:"status"`
	Summary        string    `json:"summary"`

	System  *System  `json:"system,omitempty"`
	CPU     *CPU     `json:"cpu,omitempty"`
	Memory  *Memory  `json:"memory,omitempty"`
	Storage *Storage `json:"storage,omitempty"`
	Power   *Power   `json:"power,omitempty"`
	Network *Network `json:"network,omitempty"`
	Accel   *Accel   `json:"accel,omitempty"`
	Events  *Events  `json:"events,omitempty"`

	// Inventory lists every hardware component with its identifiers,
	// derived from the sections by BuildInventory.
	Inventory []Component `json:"inventory"`
}

// Section returns the section of a module (nil if not collected).
func (s *Snapshot) Section(id ModuleID) Section {
	switch id {
	case ModSystem:
		if s.System != nil {
			return s.System
		}
	case ModCPU:
		if s.CPU != nil {
			return s.CPU
		}
	case ModMemory:
		if s.Memory != nil {
			return s.Memory
		}
	case ModStorage:
		if s.Storage != nil {
			return s.Storage
		}
	case ModPower:
		if s.Power != nil {
			return s.Power
		}
	case ModNetwork:
		if s.Network != nil {
			return s.Network
		}
	case ModAccel:
		if s.Accel != nil {
			return s.Accel
		}
	case ModEvents:
		if s.Events != nil {
			return s.Events
		}
	}
	return nil
}

// SetSection stores sec into the matching field. It panics on a type
// mismatch, which is a programming error.
func (s *Snapshot) SetSection(id ModuleID, sec Section) {
	switch id {
	case ModSystem:
		s.System = sec.(*System)
	case ModCPU:
		s.CPU = sec.(*CPU)
	case ModMemory:
		s.Memory = sec.(*Memory)
	case ModStorage:
		s.Storage = sec.(*Storage)
	case ModPower:
		s.Power = sec.(*Power)
	case ModNetwork:
		s.Network = sec.(*Network)
	case ModAccel:
		s.Accel = sec.(*Accel)
	case ModEvents:
		s.Events = sec.(*Events)
	default:
		panic(fmt.Sprintf("model: unknown module %q", id))
	}
}

// NewSection returns an empty section for a module.
func NewSection(id ModuleID) Section {
	switch id {
	case ModSystem:
		return &System{}
	case ModCPU:
		return &CPU{}
	case ModMemory:
		return &Memory{}
	case ModStorage:
		return &Storage{}
	case ModPower:
		return &Power{}
	case ModNetwork:
		return &Network{}
	case ModAccel:
		return &Accel{}
	case ModEvents:
		return &Events{}
	}
	panic(fmt.Sprintf("model: unknown module %q", id))
}

// Findings returns all findings of all sections, most severe first.
func (s *Snapshot) Findings() []Finding {
	var out []Finding
	for _, id := range append([]ModuleID{ModSystem}, Modules...) {
		if sec := s.Section(id); sec != nil {
			out = append(out, sec.Head().Findings...)
		}
	}
	return out
}

// Counters flattens every monotonic error/event counter in the snapshot into
// stable keys. Stress tests and the abort guard diff two snapshots with it.
func (s *Snapshot) Counters() map[string]uint64 {
	c := map[string]uint64{}
	if s.CPU != nil {
		s.CPU.counters(c)
	}
	if s.Memory != nil {
		s.Memory.counters(c)
	}
	if s.Storage != nil {
		s.Storage.counters(c)
	}
	if s.Network != nil {
		s.Network.counters(c)
	}
	if s.Accel != nil {
		s.Accel.counters(c)
	}
	return c
}
