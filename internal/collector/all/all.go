// Package all lists the implemented collectors.
package all

import (
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/collector/accel"
	"github.com/noahzmr/tyr/internal/collector/cpu"
	"github.com/noahzmr/tyr/internal/collector/memory"
	"github.com/noahzmr/tyr/internal/collector/network"
	"github.com/noahzmr/tyr/internal/collector/storage"
	"github.com/noahzmr/tyr/internal/collector/system"
	"github.com/noahzmr/tyr/internal/model"
)

// Collectors returns every implemented collector.
func Collectors() []collector.Collector {
	return []collector.Collector{system.New(), cpu.New(), memory.New(), storage.New(), network.New(), accel.New()}
}

// Implemented reports whether a module has a collector yet.
func Implemented(id model.ModuleID) bool {
	for _, c := range Collectors() {
		if c.ID() == id {
			return true
		}
	}
	return false
}

// Select returns the collectors for the given modules (System is always
// included because the header and summary need it).
func Select(ids []model.ModuleID) []collector.Collector {
	want := map[model.ModuleID]bool{model.ModSystem: true}
	for _, id := range ids {
		want[id] = true
	}
	var out []collector.Collector
	for _, c := range Collectors() {
		if want[c.ID()] {
			out = append(out, c)
		}
	}
	return out
}
