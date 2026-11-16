package pci

import (
	"strings"

	"github.com/noahzmr/tyr/internal/hostfs"
)

// VPD holds the read-only Vital Product Data fields of a PCI device.
type VPD struct {
	Name       string // identifier string
	PartNumber string // [PN]
	Serial     string // [SN]
	EC         string // engineering change level [EC]
	MfrID      string // [MN]
}

// ReadVPD reads and parses <dev>/vpd (needs root; empty if unsupported).
func ReadVPD(f hostfs.FS, dev string) (VPD, bool) {
	b, err := f.ReadFile(dev + "/vpd")
	if err != nil || len(b) < 3 {
		return VPD{}, false
	}
	v := ParseVPD(b)
	return v, v.Serial != "" || v.PartNumber != "" || v.Name != ""
}

// ParseVPD decodes PCI VPD resources (PCI Local Bus Spec 6.4 / PCIe 6.28):
// large resource 0x82 identifier string, 0x90 VPD-R keywords, 0x78 end.
func ParseVPD(b []byte) VPD {
	var v VPD
	clean := func(x []byte) string {
		// strip NUL/0xFF padding byte-wise (0xFF is not valid UTF-8)
		for len(x) > 0 && (x[0] == 0 || x[0] == 0xff) {
			x = x[1:]
		}
		for len(x) > 0 && (x[len(x)-1] == 0 || x[len(x)-1] == 0xff) {
			x = x[:len(x)-1]
		}
		return strings.TrimSpace(string(x))
	}
	for i := 0; i < len(b); {
		tag := b[i]
		if tag&0x80 == 0 { // small resource
			name := (tag >> 3) & 0x0f
			l := int(tag & 0x07)
			if name == 0x0f { // end tag
				return v
			}
			i += 1 + l
			continue
		}
		if i+3 > len(b) {
			return v
		}
		l := int(b[i+1]) | int(b[i+2])<<8
		data := b[i+3 : min(len(b), i+3+l)]
		switch tag {
		case 0x82:
			v.Name = clean(data)
		case 0x90, 0x91:
			for j := 0; j+3 <= len(data); {
				kw := string(data[j : j+2])
				kl := int(data[j+2])
				val := data[j+3 : min(len(data), j+3+kl)]
				switch kw {
				case "PN":
					v.PartNumber = clean(val)
				case "SN":
					v.Serial = clean(val)
				case "EC":
					v.EC = clean(val)
				case "MN":
					v.MfrID = clean(val)
				}
				j += 3 + kl
			}
		}
		i += 3 + l
	}
	return v
}
