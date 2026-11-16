package sysfs

import (
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
)

// HwmonChip is one /sys/class/hwmon/hwmonN device.
type HwmonChip struct {
	Name    string // driver name: coretemp, k10temp, nvme, jc42, ...
	Path    string // /sys/class/hwmon/hwmonN
	Device  string // canonical device path, e.g. /sys/devices/platform/coretemp.0
	Sensors []HwmonSensor
}

// HwmonSensor is one channel of a chip with values in base units
// (°C, V, A, W, RPM).
type HwmonSensor struct {
	Kind                             model.SensorKind
	Index                            int
	Label                            string
	Input                            *float64
	Min, Max, Crit, LCrit, Emergency *float64
	Alarm, CritAlarm, Fault          bool
}

var hwmonAttr = regexp.MustCompile(`^(temp|in|fan|power|curr)(\d+)_([a-z_]+)$`)

// ReadHwmon enumerates all hwmon chips. Unreadable attributes are skipped.
func ReadHwmon(f hostfs.FS) []HwmonChip {
	dirs, _ := f.Glob("/sys/class/hwmon/hwmon*")
	sort.Slice(dirs, func(i, j int) bool { return natLess(dirs[i], dirs[j]) })
	var chips []HwmonChip
	for _, d := range dirs {
		c := HwmonChip{Path: d, Name: hostfs.StringOr(f, d+"/name", path.Base(d))}
		if dev, err := f.EvalSymlinks(d + "/device"); err == nil {
			c.Device = dev
		}
		c.Sensors = readHwmonSensors(f, d)
		if len(c.Sensors) == 0 && hostfs.Exists(f, d+"/device") {
			// Older kernels put attributes under device/.
			c.Sensors = readHwmonSensors(f, d+"/device")
		}
		chips = append(chips, c)
	}
	return chips
}

func readHwmonSensors(f hostfs.FS, dir string) []HwmonSensor {
	ents, err := f.ReadDir(dir)
	if err != nil {
		return nil
	}
	type key struct {
		kind string
		idx  int
	}
	byKey := map[key]*HwmonSensor{}
	var order []key
	for _, e := range ents {
		m := hwmonAttr.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		idx, _ := strconv.Atoi(m[2])
		k := key{m[1], idx}
		s := byKey[k]
		if s == nil {
			s = &HwmonSensor{Kind: hwmonKind(m[1]), Index: idx}
			byKey[k] = s
			order = append(order, k)
		}
		p := dir + "/" + e.Name()
		switch m[3] {
		case "label":
			s.Label = hostfs.StringOr(f, p, "")
		case "input":
			s.Input = hwmonValue(f, p, m[1])
		case "min":
			s.Min = hwmonValue(f, p, m[1])
		case "max":
			s.Max = hwmonValue(f, p, m[1])
		case "crit":
			s.Crit = hwmonValue(f, p, m[1])
		case "lcrit":
			s.LCrit = hwmonValue(f, p, m[1])
		case "emergency":
			s.Emergency = hwmonValue(f, p, m[1])
		case "alarm":
			s.Alarm = hostfs.StringOr(f, p, "0") == "1"
		case "crit_alarm":
			s.CritAlarm = hostfs.StringOr(f, p, "0") == "1"
		case "fault":
			s.Fault = hostfs.StringOr(f, p, "0") == "1"
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].kind != order[j].kind {
			return order[i].kind < order[j].kind
		}
		return order[i].idx < order[j].idx
	})
	out := make([]HwmonSensor, 0, len(order))
	for _, k := range order {
		s := byKey[k]
		if s.Label == "" {
			s.Label = k.kind + strconv.Itoa(k.idx)
		}
		out = append(out, *s)
	}
	return out
}

func hwmonKind(prefix string) model.SensorKind {
	switch prefix {
	case "temp":
		return model.KindTemperature
	case "in":
		return model.KindVoltage
	case "fan":
		return model.KindFan
	case "power":
		return model.KindPower
	case "curr":
		return model.KindCurrent
	}
	return model.KindOther
}

// hwmonValue reads a value and converts it from hwmon units
// (m°C, mV, mA, µW, RPM) to base units.
func hwmonValue(f hostfs.FS, p, prefix string) *float64 {
	s, err := hostfs.String(f, p)
	if err != nil {
		return nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	switch prefix {
	case "temp", "in", "curr":
		n /= 1000
	case "power":
		n /= 1e6
	}
	return &n
}

// Thresholds converts the chip limits to model thresholds.
func (s HwmonSensor) Thresholds() *model.Thresholds {
	t := &model.Thresholds{Origin: "hwmon"}
	switch s.Kind {
	case model.KindTemperature:
		t.UpperWarn, t.UpperCrit = s.Max, s.Crit
		if t.UpperCrit == nil {
			t.UpperCrit = s.Emergency
		}
	default:
		t.LowerWarn, t.UpperWarn = s.Min, s.Max
		t.LowerCrit, t.UpperCrit = s.LCrit, s.Crit
	}
	// Some drivers report 0 or absurd limits; treat them as absent.
	for _, p := range []**float64{&t.UpperWarn, &t.UpperCrit} {
		if *p != nil && (**p <= 0 || (s.Kind == model.KindTemperature && **p >= 200)) {
			*p = nil
		}
	}
	if t.LowerCrit == nil && t.LowerWarn == nil && t.UpperWarn == nil && t.UpperCrit == nil {
		return nil
	}
	return t
}

// natLess orders "hwmon2" before "hwmon10".
func natLess(a, b string) bool {
	ta, tb := strings.TrimRight(a, "0123456789"), strings.TrimRight(b, "0123456789")
	if ta != tb {
		return a < b
	}
	na, _ := strconv.Atoi(a[len(ta):])
	nb, _ := strconv.Atoi(b[len(tb):])
	return na < nb
}

// ThermalZone is one /sys/class/thermal/thermal_zoneN.
type ThermalZone struct {
	Name     string // "type" attribute, e.g. x86_pkg_temp, cpu-thermal
	Path     string
	TempC    *float64
	CritC    *float64
	HotC     *float64
	PassiveC *float64
}

// ReadThermalZones enumerates thermal zones and their trip points.
func ReadThermalZones(f hostfs.FS) []ThermalZone {
	dirs, _ := f.Glob("/sys/class/thermal/thermal_zone*")
	sort.Slice(dirs, func(i, j int) bool { return natLess(dirs[i], dirs[j]) })
	var out []ThermalZone
	for _, d := range dirs {
		z := ThermalZone{Path: d, Name: hostfs.StringOr(f, d+"/type", path.Base(d))}
		z.TempC = milli(f, d+"/temp")
		trips, _ := f.Glob(d + "/trip_point_*_type")
		for _, tp := range trips {
			typ := hostfs.StringOr(f, tp, "")
			v := milli(f, strings.TrimSuffix(tp, "_type")+"_temp")
			switch typ {
			case "critical":
				z.CritC = v
			case "hot":
				z.HotC = v
			case "passive":
				if z.PassiveC == nil {
					z.PassiveC = v
				}
			}
		}
		out = append(out, z)
	}
	return out
}

func milli(f hostfs.FS, p string) *float64 {
	n, err := hostfs.Int(f, p)
	if err != nil {
		return nil
	}
	v := float64(n) / 1000
	return &v
}
