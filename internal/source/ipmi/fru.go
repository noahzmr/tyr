package ipmi

import "strings"

// FRU is one FRU device from `ipmitool fru print`.
type FRU struct {
	Name          string
	Manufacturer  string
	Product       string
	PartNumber    string
	Serial        string
	ChassisSerial string
	ChassisPart   string
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
			ChassisSerial: first("Chassis Serial"), ChassisPart: first("Chassis Part Number")}
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
