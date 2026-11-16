package ipmi

import (
	"strings"
	"time"
)

// FRU is one FRU device from `ipmitool fru print`.
type FRU struct {
	Name          string
	Manufacturer  string
	Product       string
	PartNumber    string
	Serial        string
	ChassisSerial string
	ChassisPart   string
	MfgDate       string // YYYY-MM-DD, from "Board Mfg Date"
}

// ParseFRU parses `ipmitool fru print`. Product fields win over board fields.
func ParseFRU(data []byte) []FRU {
	var out []FRU
	var kv map[string]string
	var name string
	flush := func() {
		if kv == nil {
			return
		}
		first := func(keys ...string) string {
			for _, k := range keys {
				if v := strings.TrimSpace(kv[k]); v != "" && !placeholder(v) {
					return v
				}
			}
			return ""
		}
		f := FRU{Name: name,
			Manufacturer:  first("Product Manufacturer", "Board Mfg"),
			Product:       first("Product Name", "Board Product"),
			PartNumber:    first("Product Part Number", "Board Part Number"),
			Serial:        first("Product Serial", "Board Serial"),
			ChassisSerial: first("Chassis Serial"), ChassisPart: first("Chassis Part Number"),
			MfgDate: mfgDate(first("Board Mfg Date", "Product Mfg Date", "Board Mfg DateTime"))}
		if f.Manufacturer+f.Product+f.Serial+f.ChassisSerial != "" {
			out = append(out, f)
		}
		kv = nil
	}
	for _, l := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "FRU Device Description" {
			flush()
			kv = map[string]string{}
			name = strings.TrimSpace(v)
			if i := strings.Index(name, " (ID"); i > 0 {
				name = name[:i]
			}
			continue
		}
		if kv != nil {
			if _, dup := kv[k]; !dup {
				kv[k] = strings.TrimSpace(v)
			}
		}
	}
	flush()
	return out
}

func placeholder(v string) bool {
	switch strings.ToLower(v) {
	case "", "n/a", "na", "not specified", "to be filled by o.e.m.", "unknown", "none", "0123456789":
		return true
	}
	return false
}

// mfgDate turns ipmitool's "Mon Mar  1 00:00:00 2021 UTC" (or ipmiutil's
// "03/01/2021 00:00:00") into "2021-03-01"; unknown formats stay as they are.
func mfgDate(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	if v == "" {
		return ""
	}
	for _, layout := range []string{"Mon Jan 2 15:04:05 2006 MST", "Mon Jan 2 15:04:05 2006", "01/02/2006 15:04:05", "01/02/2006", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			if t.Year() <= 1996 { // the FRU epoch: "not set"
				return ""
			}
			return t.Format("2006-01-02")
		}
	}
	return v
}
