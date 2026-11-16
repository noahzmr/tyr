package cpu

import (
	"strconv"

	"github.com/noahzmr/tyr/internal/check"
)

// k10temp and zenpower report no temperature limit, so without this table
// every AMD CPU would get the generic --crit-temp default (90 °C), well
// below the 95–100 °C many Ryzen parts are specified for. The CPU
// throttles itself at this maximum (AMD: "Max. Operating Temperature
// (Tjmax)"). Only families whose product pages agree on one value are
// listed; other AMD CPUs keep the configurable default.
var amdTjmax = []struct {
	family     int
	modelLo    int
	modelHi    int
	tjmax      float64
	generation string
}{
	{0x19, 0x60, 0x6f, 95, "Ryzen 7000 desktop (Raphael)"},
	{0x19, 0x70, 0x7f, 100, "Ryzen 7040/8040 mobile (Phoenix, Hawk Point)"},
	{0x1a, 0x20, 0x2f, 100, "Ryzen AI 300 (Strix Point)"},
	{0x1a, 0x40, 0x4f, 95, "Ryzen 9000 desktop (Granite Ridge)"},
	{0x1a, 0x60, 0x6f, 100, "Ryzen AI 300 (Krackan Point)"},
	{0x1a, 0x70, 0x7f, 100, "Ryzen AI Max (Strix Halo)"},
}

// amdLimits returns the thresholds for an AMD CPU's temperatures: crit at
// its Tjmax, warn 10 °C below. ok is false for unknown models, and when
// --crit-temp was given explicitly (the user's limit wins).
func amdLimits(family, model string, d check.Defaults) (check.Defaults, string, bool) {
	if d.Explicit {
		return d, "", false
	}
	f, err1 := strconv.Atoi(family)
	m, err2 := strconv.Atoi(model)
	if err1 != nil || err2 != nil {
		return d, "", false
	}
	for _, e := range amdTjmax {
		if f == e.family && m >= e.modelLo && m <= e.modelHi {
			return check.Defaults{WarnTempC: e.tjmax - 10, CritTempC: e.tjmax}, "AMD Tjmax " + e.generation, true
		}
	}
	return d, "", false
}
