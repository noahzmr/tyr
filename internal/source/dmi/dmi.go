// Package dmi parses `dmidecode` text output (SMBIOS tables).
package dmi

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/execx"
)

// Record is one SMBIOS structure.
type Record struct {
	Handle string
	Type   int
	Title  string
	Fields map[string]string
	Lists  map[string][]string
}

// Get returns a field value with dmidecode's placeholder values normalized to "".
func (r Record) Get(key string) string { return clean(r.Fields[key]) }

// Tables is a parsed dmidecode dump.
type Tables struct {
	Version string // SMBIOS version
	Records []Record
}

// ByType returns all records of the given type in table order.
func (t *Tables) ByType(typ int) []Record {
	var out []Record
	for _, r := range t.Records {
		if r.Type == typ {
			out = append(out, r)
		}
	}
	return out
}

var handleRe = regexp.MustCompile(`^Handle (0x[0-9A-Fa-f]+), DMI type (\d+), \d+ bytes`)

// Parse parses the full output of `dmidecode` (any -t selection).
func Parse(data []byte) (*Tables, error) {
	t := &Tables{}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var cur *Record
	var listKey string
	flush := func() {
		if cur != nil {
			t.Records = append(t.Records, *cur)
			cur = nil
		}
	}
	for sc.Scan() {
		line := sc.Text()
		if m := handleRe.FindStringSubmatch(line); m != nil {
			flush()
			typ, _ := strconv.Atoi(m[2])
			cur = &Record{Handle: m[1], Type: typ, Fields: map[string]string{}, Lists: map[string][]string{}}
			listKey = ""
			continue
		}
		if strings.HasPrefix(line, "SMBIOS ") && strings.HasSuffix(line, " present.") {
			t.Version = strings.TrimSuffix(strings.TrimPrefix(line, "SMBIOS "), " present.")
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.TrimSpace(line) == "":
			flush()
		case strings.HasPrefix(line, "\t\t"):
			if listKey != "" {
				cur.Lists[listKey] = append(cur.Lists[listKey], strings.TrimSpace(line))
			}
		case strings.HasPrefix(line, "\t"):
			k, v, ok := strings.Cut(strings.TrimPrefix(line, "\t"), ":")
			if !ok {
				continue
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			if v == "" {
				listKey = k
			} else {
				listKey = ""
				cur.Fields[k] = v
			}
		default:
			if cur.Title == "" {
				cur.Title = strings.TrimSpace(line)
			}
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(t.Records) == 0 {
		return nil, fmt.Errorf("dmidecode: no SMBIOS records found")
	}
	return t, nil
}

// Read runs dmidecode and parses its output.
func Read(ctx context.Context, r execx.Runner) (*Tables, error) {
	out, err := r.Run(ctx, "dmidecode")
	if err != nil {
		return nil, err
	}
	return Parse(out)
}

var placeholders = map[string]bool{
	"not specified": true, "to be filled by o.e.m.": true, "not provided": true, "unknown": true,
	"none": true, "default string": true, "system serial number": true, "0123456789": true,
	"not available": true, "no module installed": true, "n/a": true, "": true, "oem": true,
	"system product name": true, "system manufacturer": true, "o.e.m.": true,
	"chassis serial number": true, "base board serial number": true, "base board asset tag": true, "chassis asset tag": true,
	"asset tag": true, "asset-1234567890": true, "no asset tag": true, "no dimm": true, "unknown serial": true,
	"serial number": true, "part number": true, "array1_assettag": true, "00000000": true, "ffffffff": true,
}

func clean(s string) string {
	s = strings.TrimSpace(s)
	if placeholders[strings.ToLower(s)] {
		return ""
	}
	return s
}

// ParseSpeedMHz parses "3200 MHz" / "3200 MT/s" / "Unknown".
func ParseSpeedMHz(s string) *float64 {
	f := strings.Fields(s)
	if len(f) < 1 {
		return nil
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v <= 0 {
		return nil
	}
	if len(f) > 1 && strings.EqualFold(f[1], "GHz") {
		v *= 1000
	}
	return &v
}

// ParseInt parses a plain integer field ("16"); nil for unknown.
func ParseInt(s string) *int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	return &v
}

// Processor is SMBIOS type 4.
type Processor struct {
	Socket       string
	Manufacturer string
	Version      string
	Status       string // raw: "Populated, Enabled"
	Populated    bool
	Enabled      bool
	Cores        *int
	CoresEnabled *int
	Threads      *int
	MaxSpeedMHz  *float64
	CurSpeedMHz  *float64
	Serial       string
	PartNumber   string
	AssetTag     string
}

// Processors returns all type 4 records that describe CPU sockets.
func (t *Tables) Processors() []Processor {
	var out []Processor
	for _, r := range t.ByType(4) {
		if typ := r.Fields["Type"]; typ != "" && typ != "Central Processor" {
			continue
		}
		st := r.Fields["Status"]
		p := Processor{
			Socket:       r.Get("Socket Designation"),
			Manufacturer: r.Get("Manufacturer"),
			Version:      r.Get("Version"),
			Status:       st,
			Populated:    strings.HasPrefix(st, "Populated"),
			Enabled:      strings.HasSuffix(st, "Enabled"),
			Cores:        ParseInt(r.Fields["Core Count"]),
			CoresEnabled: ParseInt(r.Fields["Core Enabled"]),
			Threads:      ParseInt(r.Fields["Thread Count"]),
			MaxSpeedMHz:  ParseSpeedMHz(r.Fields["Max Speed"]),
			CurSpeedMHz:  ParseSpeedMHz(r.Fields["Current Speed"]),
			Serial:       r.Get("Serial Number"),
			PartNumber:   r.Get("Part Number"),
			AssetTag:     r.Get("Asset Tag"),
		}
		out = append(out, p)
	}
	return out
}

// PowerSupply is SMBIOS type 39 (System Power Supply).
type PowerSupply struct {
	Location, Name, Manufacturer, Serial, Model, Revision, Status string
	MaxWatts                                                      *float64
	Present                                                       bool
}

// PowerSupplies returns all type 39 records.
func (t *Tables) PowerSupplies() []PowerSupply {
	var out []PowerSupply
	for _, r := range t.ByType(39) {
		p := PowerSupply{Location: r.Get("Location"), Name: r.Get("Name"), Manufacturer: r.Get("Manufacturer"),
			Serial: r.Get("Serial Number"), Model: r.Get("Model Part Number"), Revision: r.Get("Revision"), Status: r.Fields["Status"]}
		p.Present = strings.HasPrefix(p.Status, "Present")
		if f := strings.Fields(r.Fields["Max Power Capacity"]); len(f) == 2 && f[1] == "W" {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil && v > 0 && v < 100000 {
				p.MaxWatts = &v
			}
		}
		out = append(out, p)
	}
	return out
}
