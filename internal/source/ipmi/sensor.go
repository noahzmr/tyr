package ipmi

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/model"
)

// Sensor is one BMC sensor. Status is the BMC's own evaluation against its
// thresholds, which takes precedence over Tyr defaults.
type Sensor struct {
	Name       string
	Kind       model.SensorKind
	Value      *float64
	Unit       string // model unit constant, "" for discrete
	Status     model.Status
	StatusText string // raw status ("ok", "cr", "BelowCrit", ...)
	Discrete   string // discrete reading/state text
	Thresholds *model.Thresholds
}

// Model converts to the generic model sensor.
func (s Sensor) Model() model.Sensor {
	return model.Sensor{Name: s.Name, Kind: s.Kind, Value: s.Value, Unit: s.Unit, Status: s.Status, Thresholds: s.Thresholds, Source: "ipmi"}
}

func unitKind(u string) (string, model.SensorKind) {
	switch strings.ToLower(strings.TrimSpace(u)) {
	case "degrees c", "c":
		return model.UnitCelsius, model.KindTemperature
	case "volts", "v":
		return model.UnitVolts, model.KindVoltage
	case "amps", "a":
		return model.UnitAmps, model.KindCurrent
	case "watts", "w":
		return model.UnitWatts, model.KindPower
	case "rpm":
		return model.UnitRPM, model.KindFan
	case "", "discrete":
		return "", model.KindOther
	}
	return strings.ToLower(strings.TrimSpace(u)), model.KindOther
}

// fanDuty: HPE iLO and some others report fans as a duty cycle in percent
// ("Fan 1 DutyCycle"), not in RPM.
func fanDuty(name, unit string, kind model.SensorKind) model.SensorKind {
	if kind == model.KindOther && unit == "percent" && strings.Contains(strings.ToLower(name), "fan") {
		return model.KindFan
	}
	return kind
}

func parseNum(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "na" || s == "N/A" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

// ParseIPMIToolSensors parses `ipmitool sensor`:
//
//	CPU1 Temp | 45.000 | degrees C | ok | lnr | lcr | lnc | unc | ucr | unr
func ParseIPMIToolSensors(data []byte) ([]Sensor, error) {
	var out []Sensor
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(line, "|")
		if len(f) < 4 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		unit, kind := unitKind(f[2])
		kind = fanDuty(f[0], unit, kind)
		s := Sensor{Name: f[0], Kind: kind, Unit: unit, StatusText: f[3]}
		if f[2] == "discrete" {
			s.Discrete = f[1]
			s.Status = model.StatusOK
			if f[3] == "na" {
				s.Status = model.StatusUnknown
			}
			out = append(out, s)
			continue
		}
		s.Value = parseNum(f[1])
		if len(f) >= 10 {
			t := &model.Thresholds{Origin: "ipmi",
				LowerCrit: parseNum(f[5]), LowerWarn: parseNum(f[6]),
				UpperWarn: parseNum(f[7]), UpperCrit: parseNum(f[8])}
			if t.LowerCrit == nil {
				t.LowerCrit = parseNum(f[4]) // non-recoverable
			}
			if t.UpperCrit == nil {
				t.UpperCrit = parseNum(f[9])
			}
			// BMCs often report 0.000 for unused lower thresholds of temps/fans.
			if kind == model.KindTemperature {
				for _, p := range []**float64{&t.LowerCrit, &t.LowerWarn} {
					if *p != nil && **p <= 0 {
						*p = nil
					}
				}
			}
			if t.LowerCrit != nil || t.LowerWarn != nil || t.UpperWarn != nil || t.UpperCrit != nil {
				s.Thresholds = t
			}
		}
		switch strings.ToLower(f[3]) {
		case "ok":
			s.Status = model.StatusOK
		case "nc":
			s.Status = model.StatusWarn
		case "cr", "nr":
			s.Status = model.StatusCrit
		default: // na, ns: no reading (absent PSU, unused fan header)
			s.Status = model.StatusUnknown
		}
		if s.Value == nil {
			s.Status = model.StatusUnknown
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ipmitool sensor: no sensors in output")
	}
	return out, nil
}

// ParseIPMIUtilSensors parses `ipmiutil sensor -c` (simple '|' output):
//
//	ID   | SDRType | Type            |SNum| Name             |Status| Reading
//	0004 | Full    | Temperature     | 01 | CPU1 Temp        | OK   | 45.00 C
//	0040 | Compact | Power Supply    | 51 | PS2 Status       | Crit | Failure detected
//
// ipmiutil's simple output carries the BMC status but not the numeric
// thresholds; severity therefore comes from the BMC status column.
func ParseIPMIUtilSensors(data []byte) ([]Sensor, error) {
	var out []Sensor
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(line, "|")
		if len(f) < 7 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		if _, err := strconv.ParseUint(f[0], 16, 32); err != nil {
			continue // header
		}
		sdrType, sensType, name, status, reading := f[1], f[2], f[4], f[5], strings.Join(f[6:], "|")
		s := Sensor{Name: name, StatusText: status}
		if fields := strings.Fields(reading); len(fields) == 2 && parseNum(fields[0]) != nil && sdrType == "Full" {
			s.Value = parseNum(fields[0])
			s.Unit, s.Kind = unitKind(fields[1])
			s.Kind = fanDuty(name, s.Unit, s.Kind)
		} else {
			s.Discrete = reading
			s.Kind = typeKind(sensType)
		}
		if s.Kind == model.KindOther {
			s.Kind = typeKind(sensType)
		}
		s.Status = ipmiutilStatus(status)
		if s.Value == nil && s.Discrete == "" {
			s.Status = model.StatusUnknown
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ipmiutil sensor: no sensors in output")
	}
	return out, nil
}

func typeKind(t string) model.SensorKind {
	switch strings.ToLower(t) {
	case "temperature":
		return model.KindTemperature
	case "voltage":
		return model.KindVoltage
	case "current":
		return model.KindCurrent
	case "fan":
		return model.KindFan
	}
	return model.KindOther
}

func ipmiutilStatus(s string) model.Status {
	l := strings.ToLower(s)
	switch {
	case l == "ok":
		return model.StatusOK
	case strings.Contains(l, "noncrit"), strings.Contains(l, "warn"), l == "nc":
		return model.StatusWarn
	case strings.Contains(l, "crit"), strings.Contains(l, "nonrec"), strings.Contains(l, "fail"):
		return model.StatusCrit
	}
	return model.StatusUnknown // Absent, NotAvailable, Init, ...
}

// FindTemps returns temperature sensors whose lowercase name contains all
// of the given substrings.
func FindTemps(ss []Sensor, contains ...string) []Sensor {
	var out []Sensor
	for _, s := range ss {
		if s.Kind != model.KindTemperature {
			continue
		}
		n := strings.ToLower(s.Name)
		ok := true
		for _, c := range contains {
			if !strings.Contains(n, c) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, s)
		}
	}
	return out
}
