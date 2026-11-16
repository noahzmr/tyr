package cpu

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/ipmi"
)

var lastNumber = regexp.MustCompile(`(\d+)\D*$`)

// cpuSensorName reports whether a BMC temperature sensor measures a CPU die.
// Names seen in the field: "CPU1 Temp" (Supermicro), "CPU 1 Temp" (Lenovo),
// "02-CPU 1" (HPE), "CPU0_TEMP" (Gigabyte), "TEMP_CPU1" (ASRock Rack).
// Voltage-regulator, DIMM and "margin" sensors are excluded.
func cpuSensorName(name string) bool {
	n := strings.ToLower(name)
	if !strings.Contains(n, "cpu") && !strings.Contains(n, "proc") {
		return false
	}
	for _, x := range []string{"vr", "dimm", "mem", "margin", "inlet", "exhaust", "fan"} {
		if strings.Contains(n, x) {
			return false
		}
	}
	return true
}

// ipmiTemps fills temps from BMC sensors. Socket numbering in sensor names is
// 1-based unless any sensor uses 0 ("CPU0_TEMP").
func ipmiTemps(t *temps, sens []ipmi.Sensor, pkgIDs []int, d check.Defaults) {
	type cand struct {
		s   ipmi.Sensor
		num int
	}
	var cands []cand
	zeroBased := false
	for _, s := range sens {
		if s.Kind != model.KindTemperature || s.Value == nil || !cpuSensorName(s.Name) {
			continue
		}
		num := -1
		if m := lastNumber.FindStringSubmatch(s.Name); m != nil {
			num, _ = strconv.Atoi(m[1])
		}
		if num == 0 {
			zeroBased = true
		}
		cands = append(cands, cand{s, num})
	}
	if len(cands) == 0 {
		return
	}
	for i, c := range cands {
		socket := c.num
		if socket >= 0 && !zeroBased {
			socket--
		}
		if socket < 0 || (len(pkgIDs) > 0 && !slices.Contains(pkgIDs, socket)) {
			if len(cands) == 1 && len(pkgIDs) > 0 {
				socket = pkgIDs[0] // single "CPU Temp" sensor
			} else {
				socket = -1 - i
			}
		}
		m := c.s.Model()
		if m.Thresholds == nil {
			// ipmiutil output carries no numbers: keep the BMC's verdict and
			// add the configurable defaults on top.
			m.Thresholds = check.TempThresholds(nil, d)
			m.Status = model.Worst(c.s.Status, check.Eval(m.Value, m.Thresholds))
		}
		if socket >= 0 {
			m.Name = fmt.Sprintf("CPU%d Package (%s)", socket, c.s.Name)
			t.pkg[socket] = &m
		} else {
			t.extra = append(t.extra, m)
		}
	}
	t.source = "ipmi"
}
