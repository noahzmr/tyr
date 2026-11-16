package cpu

import (
	"strings"
	"testing"

	"github.com/noahzmr/tyr/internal/check"
)

func TestAMDLimits(t *testing.T) {
	def := check.DefaultDefaults()
	// Ryzen AI 7 PRO 350 (family 26 / 0x1a, model 96 / 0x60): 100 °C
	d, origin, ok := amdLimits("26", "96", def)
	if !ok || d.CritTempC != 100 || d.WarnTempC != 90 || !strings.Contains(origin, "Krackan") {
		t.Errorf("krackan: %+v %q %v", d, origin, ok)
	}
	// Ryzen 9 7950X (0x19, 0x61): 95 °C
	if d, _, ok := amdLimits("25", "97", def); !ok || d.CritTempC != 95 {
		t.Errorf("raphael: %+v", d)
	}
	// unknown models (e.g. EPYC) keep the configurable default
	if d, _, ok := amdLimits("25", "17", def); ok || d != def {
		t.Errorf("epyc: %+v %v", d, ok)
	}
	// an explicit --crit-temp wins
	ex := check.Defaults{WarnTempC: 85, CritTempC: 97, Explicit: true}
	if d, _, ok := amdLimits("26", "96", ex); ok || d != ex {
		t.Errorf("explicit: %+v %v", d, ok)
	}
}
