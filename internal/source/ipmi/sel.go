package ipmi

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/model"
)

// SELEntry is one System Event Log record.
type SELEntry struct {
	ID         string
	Time       time.Time // zero for "Pre-Init" records
	SensorType string    // "Memory", "Power Supply", ...
	Sensor     string    // "PS2 Status", "#0x87"
	Event      string    // "Correctable ECC", "Power Supply AC lost"
	Asserted   bool
	Severity   model.Status
	Category   string // memory, psu, temperature, fan, processor, voltage, drive, pci, system, other
}

// SEL is the parsed event log.
type SEL struct {
	Entries  []SELEntry
	Used     int // records used (0 if unknown)
	Capacity int // total records (0 if unknown)
}

// FillPercent returns the SEL fill level, or nil if unknown.
func (s *SEL) FillPercent() *float64 {
	if s.Capacity <= 0 {
		return nil
	}
	v := float64(s.Used) / float64(s.Capacity) * 100
	return &v
}

// IPMI sensor type names (IPMI 2.0 table 42-3) as printed by ipmitool,
// longest first so prefixes match correctly.
var sensorTypes = []string{
	"Management Subsystem Health", "System ACPI Power State", "System Firmware Progress", "Event Logging Disabled",
	"Cable / Interconnect", "POST Memory Resize", "System Boot Initiated", "Platform Security", "Physical Security",
	"Critical Interrupt", "Drive Slot / Bay", "Slot / Connector", "Entity Presence", "OS Critical Stop",
	"System Firmwares", "Microcontroller", "Cooling Device", "Platform Alert", "Version Change", "Session Audit",
	"Power Supply", "System Event", "Module / Board", "Add-in Card", "Monitor ASIC", "Boot Error", "Power Unit",
	"Terminator", "Other FRU", "Watchdog1", "Watchdog2", "FRU State", "Temperature", "Processor", "Chip Set",
	"Voltage", "Current", "Battery", "OS Boot", "Chassis", "Memory", "Button", "Other", "LAN", "Fan",
}

// ParseIPMIToolSEL parses `ipmitool sel elist`:
//
//	1 | 06/14/2024 | 10:22:31 | Power Supply PS2 Status | Power Supply AC lost | Asserted
func ParseIPMIToolSEL(data []byte) (*SEL, error) {
	sel := &SEL{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(line, "|")
		if len(f) < 5 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		e := SELEntry{ID: f[0], Event: f[4], Asserted: true}
		if len(f) >= 6 {
			e.Asserted = !strings.EqualFold(f[5], "Deasserted")
		}
		if t, err := time.Parse("01/02/2006 15:04:05", f[1]+" "+f[2]); err == nil {
			e.Time = t.UTC()
		}
		e.SensorType, e.Sensor = splitSensor(f[3])
		classify(&e)
		sel.Entries = append(sel.Entries, e)
	}
	return sel, nil
}

func splitSensor(s string) (typ, name string) {
	for _, t := range sensorTypes {
		if strings.HasPrefix(s, t) {
			return t, strings.TrimSpace(strings.TrimPrefix(s, t))
		}
	}
	return "", s
}

// parseIPMIToolSELInfo extracts "Entries : 13" and the free space; capacity
// is derived from "Free Space : N bytes" (16 bytes per record).
func parseIPMIToolSELInfo(data []byte) (used, capacity int) {
	free := -1
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "Entries":
			used, _ = strconv.Atoi(v)
		case "Free Space":
			if n, err := strconv.Atoi(strings.Fields(v + " x")[0]); err == nil {
				free = n / 16
			}
		}
	}
	if free >= 0 {
		capacity = used + free
	}
	return used, capacity
}

var (
	ipmiutilSELSize = regexp.MustCompile(`Size = (\d+) records \(Used=(\d+), Free=(\d+)\)`)
	// 0002 12/03/21 14:01:52 MAJ BMC  Power Supply #51 AC Lost asserted 6f [03 ff ff]
	ipmiutilSELLine = regexp.MustCompile(`^([0-9a-fA-F]{4}) (\d\d/\d\d/\d\d) (\d\d:\d\d:\d\d) (\w{3}) (\S+)\s+(.+?) #([0-9a-fA-F]{2}) (.*?)(?: [0-9a-fA-F]{2} \[[0-9a-fA-F ]*\])?$`)
)

// ParseIPMIUtilSEL parses the default `ipmiutil sel` listing.
func ParseIPMIUtilSEL(data []byte) (*SEL, error) {
	sel := &SEL{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, " \r")
		if m := ipmiutilSELSize.FindStringSubmatch(line); m != nil {
			sel.Capacity, _ = strconv.Atoi(m[1])
			sel.Used, _ = strconv.Atoi(m[2])
			continue
		}
		m := ipmiutilSELLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		e := SELEntry{ID: m[1], SensorType: m[6], Sensor: "#" + m[7], Asserted: true}
		if t, err := time.Parse("01/02/06 15:04:05", m[2]+" "+m[3]); err == nil {
			e.Time = t.UTC()
		}
		ev := m[8]
		low := strings.ToLower(ev)
		switch {
		case strings.HasSuffix(low, " deasserted"):
			e.Asserted = false
			ev = ev[:len(ev)-len(" deasserted")]
		case strings.HasSuffix(low, " asserted"):
			ev = ev[:len(ev)-len(" asserted")]
		}
		e.Event = ev
		classify(&e)
		if e.Severity == model.StatusOK && e.Asserted {
			// fall back to ipmiutil's own severity column
			switch m[4] {
			case "MIN":
				e.Severity = model.StatusWarn
			case "MAJ", "CRT":
				e.Severity = model.StatusCrit
			}
		}
		sel.Entries = append(sel.Entries, e)
	}
	if len(sel.Entries) == 0 && sel.Capacity == 0 {
		return nil, fmt.Errorf("ipmiutil sel: unrecognized output")
	}
	return sel, nil
}

var (
	selCrit = []string{"uncorrectable", "parity", "failure detected", "ac lost", "power supply failure", "ierr",
		"thermal trip", "lower critical", "upper critical", "non-recoverable", "machine check", "bus fatal",
		"pci serr", "drive fault", "redundancy lost", "memory device disabled", "fully redundant lost",
		"configuration error", "catastrophic", "frb"}
	selWarn = []string{"correctable ecc", "non-critical", "predictive failure", "redundancy degraded",
		"logging limit reached", "throttled", "pci perr", "correctable machine check", "transition to degraded",
		"bus correctable", "rebuild", "spare"}
	selIgnore = []string{"log area reset", "timestamp clock sync", "oem", "presence detected", "fully redundant",
		"device present", "device inserted", "transition to running", "s0/g0", "initiated by power up"}
)

// classify sets Category and Severity from the record text. Deasserted
// events (a condition going away) are OK.
func classify(e *SELEntry) {
	text := strings.ToLower(e.SensorType + " " + e.Sensor + " " + e.Event)
	typ := strings.ToLower(e.SensorType)
	switch {
	case typ == "memory" || strings.Contains(text, "ecc") || strings.Contains(text, "dimm"):
		e.Category = "memory"
	case typ == "power supply" || typ == "power unit":
		e.Category = "psu"
	case typ == "temperature":
		e.Category = "temperature"
	case typ == "fan" || typ == "cooling device":
		e.Category = "fan"
	case typ == "processor" || strings.Contains(text, "machine check"):
		e.Category = "processor"
	case typ == "voltage" || typ == "current":
		e.Category = "voltage"
	case typ == "drive slot / bay":
		e.Category = "drive"
	case typ == "critical interrupt" || strings.Contains(text, "pci"):
		e.Category = "pci"
	case typ == "":
		e.Category = "other"
	default:
		e.Category = "system"
	}
	ev := strings.ToLower(e.Event)
	e.Severity = model.StatusOK
	if !e.Asserted {
		return
	}
	for _, p := range selIgnore {
		if strings.Contains(ev, p) && !strings.Contains(ev, "lost") {
			return
		}
	}
	for _, p := range selCrit {
		if strings.Contains(ev, p) {
			e.Severity = model.StatusCrit
			return
		}
	}
	for _, p := range selWarn {
		if strings.Contains(ev, p) {
			e.Severity = model.StatusWarn
			return
		}
	}
}
