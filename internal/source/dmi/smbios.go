package dmi

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
)

// ParseTable decodes the raw SMBIOS structure table the kernel exports
// (/sys/firmware/dmi/tables/DMI) into the same records Parse builds from
// dmidecode's text: the same field names and value formats, for the types
// Tyr reads (0-4, 16, 17, 39, 42). Other types keep their handle, type and
// strings only. entry is /sys/firmware/dmi/tables/smbios_entry_point (for
// the version; may be nil).
func ParseTable(table, entry []byte) (*Tables, error) {
	t := &Tables{Version: entryVersion(entry)}
	for off := 0; off+4 <= len(table); {
		typ, n := int(table[off]), int(table[off+1])
		if n < 4 || off+n > len(table) {
			return nil, fmt.Errorf("SMBIOS: structure at %d: length %d", off, n)
		}
		f := table[off : off+n]
		// strings: NUL-terminated, the set ends with a double NUL
		end := off + n
		var strs []string
		for end < len(table) {
			i := end
			for i < len(table) && table[i] != 0 {
				i++
			}
			if i == end { // empty string: end of the set
				end++
				if len(strs) == 0 && end < len(table) && table[end] == 0 {
					end++
				}
				break
			}
			strs = append(strs, string(table[end:i]))
			end = i + 1
		}
		r := Record{Handle: fmt.Sprintf("0x%04X", binary.LittleEndian.Uint16(f[2:4])), Type: typ, Fields: map[string]string{}, Lists: map[string][]string{}}
		decode(&r, structure{f: f, s: strs})
		t.Records = append(t.Records, r)
		off = end
		if typ == 127 {
			break
		}
	}
	if len(t.Records) == 0 {
		return nil, fmt.Errorf("SMBIOS: no structures")
	}
	return t, nil
}

func entryVersion(e []byte) string {
	switch {
	case len(e) >= 10 && string(e[:5]) == "_SM3_":
		return fmt.Sprintf("%d.%d.%d", e[7], e[8], e[9])
	case len(e) >= 8 && string(e[:4]) == "_SM_":
		return fmt.Sprintf("%d.%d", e[6], e[7])
	}
	return ""
}

type structure struct {
	f []byte
	s []string
}

func (s structure) has(off, n int) bool { return off+n <= len(s.f) }

func (s structure) byte(off int) (int, bool) {
	if !s.has(off, 1) {
		return 0, false
	}
	return int(s.f[off]), true
}

func (s structure) word(off int) (int, bool) {
	if !s.has(off, 2) {
		return 0, false
	}
	return int(binary.LittleEndian.Uint16(s.f[off:])), true
}

func (s structure) dword(off int) (uint32, bool) {
	if !s.has(off, 4) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(s.f[off:]), true
}

// str is the string a byte at off points to ("Not Specified" for 0, as
// dmidecode prints it).
func (s structure) str(off int) (string, bool) {
	i, ok := s.byte(off)
	if !ok {
		return "", false
	}
	if i == 0 {
		return "Not Specified", true
	}
	if i > len(s.s) {
		return "<BAD INDEX>", true
	}
	return strings.TrimSpace(s.s[i-1]), true
}

func set(r *Record, k string, v string, ok bool) {
	if ok {
		r.Fields[k] = v
	}
}

func decode(r *Record, s structure) {
	str := func(k string, off int) { v, ok := s.str(off); set(r, k, v, ok) }
	switch r.Type {
	case 0:
		r.Title = "BIOS Information"
		str("Vendor", 0x04)
		str("Version", 0x05)
		str("Release Date", 0x08)
	case 1:
		r.Title = "System Information"
		str("Manufacturer", 0x04)
		str("Product Name", 0x05)
		str("Version", 0x06)
		str("Serial Number", 0x07)
		if s.has(0x08, 16) {
			r.Fields["UUID"] = uuid(s.f[0x08:0x18])
		}
		str("SKU Number", 0x19)
		str("Family", 0x1A)
	case 2:
		r.Title = "Base Board Information"
		str("Manufacturer", 0x04)
		str("Product Name", 0x05)
		str("Version", 0x06)
		str("Serial Number", 0x07)
		str("Asset Tag", 0x08)
	case 3:
		r.Title = "Chassis Information"
		str("Manufacturer", 0x04)
		if v, ok := s.byte(0x05); ok {
			r.Fields["Type"] = lookup(chassisTypes, v&0x7F)
		}
		str("Version", 0x06)
		str("Serial Number", 0x07)
		str("Asset Tag", 0x08)
	case 4:
		r.Title = "Processor Information"
		str("Socket Designation", 0x04)
		if v, ok := s.byte(0x05); ok {
			r.Fields["Type"] = lookup(map[int]string{1: "Other", 2: "Unknown", 3: "Central Processor", 4: "Math Processor", 5: "DSP Processor", 6: "Video Processor"}, v)
		}
		str("Manufacturer", 0x07)
		str("Version", 0x10)
		if v, ok := s.word(0x12); ok {
			r.Fields["External Clock"] = mhz(v)
		}
		if v, ok := s.word(0x14); ok {
			r.Fields["Max Speed"] = mhz(v)
		}
		if v, ok := s.word(0x16); ok {
			r.Fields["Current Speed"] = mhz(v)
		}
		if v, ok := s.byte(0x18); ok {
			st := "Unpopulated"
			if v&0x40 != 0 {
				st = "Populated, " + lookup(map[int]string{0: "Unknown", 1: "Enabled", 2: "Disabled By User", 3: "Disabled By BIOS", 4: "Idle", 7: "Other"}, v&7)
			}
			r.Fields["Status"] = st
		}
		str("Serial Number", 0x20)
		str("Asset Tag", 0x21)
		str("Part Number", 0x22)
		count := func(k string, b, w int) {
			v, ok := s.byte(b)
			if !ok {
				return
			}
			if v == 0xFF {
				if x, ok := s.word(w); ok {
					v = x
				}
			}
			if v != 0 {
				r.Fields[k] = fmt.Sprint(v)
			}
		}
		count("Core Count", 0x23, 0x2A)
		count("Core Enabled", 0x24, 0x2C)
		count("Thread Count", 0x25, 0x2E)
	case 16:
		r.Title = "Physical Memory Array"
		if v, ok := s.byte(0x04); ok {
			r.Fields["Location"] = lookup(map[int]string{1: "Other", 2: "Unknown", 3: "System Board Or Motherboard", 4: "ISA Add-on Card", 5: "EISA Add-on Card",
				6: "PCI Add-on Card", 7: "MCA Add-on Card", 8: "PCMCIA Add-on Card", 9: "Proprietary Add-on Card", 10: "NuBus"}, v)
		}
		if v, ok := s.byte(0x05); ok {
			r.Fields["Use"] = lookup(map[int]string{1: "Other", 2: "Unknown", 3: "System Memory", 4: "Video Memory", 5: "Flash Memory", 6: "Non-volatile RAM", 7: "Cache Memory"}, v)
		}
		if v, ok := s.byte(0x06); ok {
			r.Fields["Error Correction Type"] = lookup(map[int]string{1: "Other", 2: "Unknown", 3: "None", 4: "Parity", 5: "Single-bit ECC", 6: "Multi-bit ECC", 7: "CRC"}, v)
		}
		if v, ok := s.dword(0x07); ok {
			switch {
			case v == 0x80000000 && s.has(0x0F, 8):
				r.Fields["Maximum Capacity"] = size(binary.LittleEndian.Uint64(s.f[0x0F:]))
			case v != 0x80000000:
				r.Fields["Maximum Capacity"] = size(uint64(v) << 10)
			}
		}
		if v, ok := s.word(0x0D); ok {
			r.Fields["Number Of Devices"] = fmt.Sprint(v)
		}
	case 17:
		r.Title = "Memory Device"
		if v, ok := s.word(0x04); ok {
			r.Fields["Array Handle"] = fmt.Sprintf("0x%04X", v)
		}
		width := func(k string, off int) {
			if v, ok := s.word(off); ok {
				if v == 0xFFFF || v == 0 {
					r.Fields[k] = "Unknown"
				} else {
					r.Fields[k] = fmt.Sprintf("%d bits", v)
				}
			}
		}
		width("Total Width", 0x08)
		width("Data Width", 0x0A)
		if v, ok := s.word(0x0C); ok {
			switch {
			case v == 0:
				r.Fields["Size"] = "No Module Installed"
			case v == 0xFFFF:
				r.Fields["Size"] = "Unknown"
			case v == 0x7FFF:
				if x, ok := s.dword(0x1C); ok {
					r.Fields["Size"] = size(uint64(x&0x7FFFFFFF) << 20)
				}
			case v&0x8000 != 0:
				r.Fields["Size"] = size(uint64(v&0x7FFF) << 10)
			default:
				r.Fields["Size"] = size(uint64(v) << 20)
			}
		}
		if v, ok := s.byte(0x0E); ok {
			r.Fields["Form Factor"] = lookup(formFactors, v)
		}
		str("Locator", 0x10)
		str("Bank Locator", 0x11)
		if v, ok := s.byte(0x12); ok {
			r.Fields["Type"] = lookup(memTypes, v)
		}
		if v, ok := s.word(0x13); ok {
			r.Fields["Type Detail"] = typeDetail(v)
		}
		speed := func(k string, off, ext int) {
			v, ok := s.word(off)
			if !ok {
				return
			}
			if v == 0xFFFF {
				if x, ok := s.dword(ext); ok {
					v = int(x)
				}
			}
			if v == 0 {
				r.Fields[k] = "Unknown"
			} else {
				r.Fields[k] = fmt.Sprintf("%d MT/s", v)
			}
		}
		speed("Speed", 0x15, 0x54)
		str("Manufacturer", 0x17)
		str("Serial Number", 0x18)
		str("Asset Tag", 0x19)
		str("Part Number", 0x1A)
		if v, ok := s.byte(0x1B); ok {
			if v&0x0F == 0 {
				r.Fields["Rank"] = "Unknown"
			} else {
				r.Fields["Rank"] = fmt.Sprint(v & 0x0F)
			}
		}
		speed("Configured Memory Speed", 0x20, 0x58)
	case 39:
		r.Title = "System Power Supply"
		if v, ok := s.byte(0x04); ok {
			r.Fields["Power Unit Group"] = fmt.Sprint(v)
		}
		str("Location", 0x05)
		str("Name", 0x06)
		str("Manufacturer", 0x07)
		str("Serial Number", 0x08)
		str("Asset Tag", 0x09)
		str("Model Part Number", 0x0A)
		str("Revision", 0x0B)
		if v, ok := s.word(0x0C); ok {
			if v == 0x8000 {
				r.Fields["Max Power Capacity"] = "Unknown"
			} else {
				r.Fields["Max Power Capacity"] = fmt.Sprintf("%d W", v)
			}
		}
		if v, ok := s.word(0x0E); ok {
			if v&0x0002 == 0 {
				r.Fields["Status"] = "Not Present"
			} else {
				r.Fields["Status"] = "Present, " + lookup(map[int]string{1: "Other", 2: "Unknown", 3: "OK", 4: "Non-critical", 5: "Critical"}, (v>>7)&7)
			}
			r.Fields["Plugged"] = map[bool]string{true: "No", false: "Yes"}[v&0x0004 != 0]
			r.Fields["Hot Replaceable"] = map[bool]string{true: "Yes", false: "No"}[v&0x0001 != 0]
		}
	case 42:
		r.Title = "Management Controller Host Interface"
		hostInterface(r, s)
	default:
		r.Title = fmt.Sprintf("SMBIOS type %d", r.Type)
	}
}

// hostInterface reads the protocol records of type 42 (Redfish over IP)
// into dmidecode's lines ("IPv4 Redfish Service Address: 16.1.15.1").
func hostInterface(r *Record, s structure) {
	it, ok := s.byte(0x04)
	if !ok {
		return
	}
	if it == 0x40 {
		r.Fields["Host Interface Type"] = "Network"
	}
	dl, ok := s.byte(0x05)
	if !ok {
		return
	}
	off := 0x06 + dl
	n, ok := s.byte(off)
	if !ok {
		return
	}
	off++
	for range n {
		pt, ok1 := s.byte(off)
		pl, ok2 := s.byte(off + 1)
		if !ok1 || !ok2 || !s.has(off+2, pl) {
			return
		}
		d := s.f[off+2 : off+2+pl]
		off += 2 + pl
		if pt != 4 || len(d) < 91 { // Redfish over IP
			continue
		}
		var lines []string
		lines = append(lines, "Protocol ID: 04 (Redfish over IP)")
		ip := func(format byte, b []byte) string {
			if format == 2 {
				return net.IP(b[:16]).String()
			}
			return net.IP(b[:4]).String()
		}
		hostFmt, svcFmt := d[17], d[51]
		fam := map[byte]string{1: "IPv4", 2: "IPv6"}
		lines = append(lines, fmt.Sprintf("%s Address: %s", fam[hostFmt], ip(hostFmt, d[18:34])))
		lines = append(lines, fmt.Sprintf("%s Redfish Service Address: %s", fam[svcFmt], ip(svcFmt, d[52:68])))
		lines = append(lines, fmt.Sprintf("Redfish Service Port: %d", binary.LittleEndian.Uint16(d[84:86])))
		if hl := int(d[90]); len(d) >= 91+hl && hl > 0 {
			lines = append(lines, "Redfish Service Hostname: "+strings.TrimRight(string(d[91:91+hl]), "\x00"))
		}
		r.Lists["Protocol"] = append(r.Lists["Protocol"], lines...)
	}
}

func lookup(m map[int]string, v int) string {
	if s, ok := m[v]; ok {
		return s
	}
	return fmt.Sprintf("<OUT OF SPEC> (%d)", v)
}

func mhz(v int) string {
	if v == 0 {
		return "Unknown"
	}
	return fmt.Sprintf("%d MHz", v)
}

// size prints bytes the way dmidecode does: the largest unit that divides.
func size(b uint64) string {
	if b == 0 {
		return "No Module Installed"
	}
	for _, u := range []struct {
		shift uint
		name  string
	}{{40, "TB"}, {30, "GB"}, {20, "MB"}, {10, "kB"}} {
		if b%(1<<u.shift) == 0 {
			return fmt.Sprintf("%d %s", b>>u.shift, u.name)
		}
	}
	return fmt.Sprintf("%d bytes", b)
}

// uuid formats SMBIOS 2.6+ UUIDs: the first three fields are little-endian.
func uuid(b []byte) string {
	return fmt.Sprintf("%02X%02X%02X%02X-%02X%02X-%02X%02X-%02X%02X-%02X%02X%02X%02X%02X%02X",
		b[3], b[2], b[1], b[0], b[5], b[4], b[7], b[6], b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func typeDetail(v int) string {
	names := []string{"Other", "Unknown", "Fast-paged", "Static Column", "Pseudo-static", "RAMBus", "Synchronous", "CMOS", "EDO",
		"Window DRAM", "Cache DRAM", "Non-Volatile", "Registered (Buffered)", "Unbuffered (Unregistered)", "LRDIMM"}
	var out []string
	for i, n := range names {
		if v&(1<<(i+1)) != 0 {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return "None"
	}
	return strings.Join(out, " ")
}

var chassisTypes = map[int]string{1: "Other", 2: "Unknown", 3: "Desktop", 4: "Low Profile Desktop", 5: "Pizza Box", 6: "Mini Tower", 7: "Tower",
	8: "Portable", 9: "Laptop", 10: "Notebook", 11: "Hand Held", 12: "Docking Station", 13: "All In One", 14: "Sub Notebook",
	15: "Space-saving", 16: "Lunch Box", 17: "Main Server Chassis", 18: "Expansion Chassis", 19: "Sub Chassis", 20: "Bus Expansion Chassis",
	21: "Peripheral Chassis", 22: "RAID Chassis", 23: "Rack Mount Chassis", 24: "Sealed-case PC", 25: "Multi-system", 26: "CompactPCI",
	27: "AdvancedTCA", 28: "Blade", 29: "Blade Enclosing", 30: "Tablet", 31: "Convertible", 32: "Detachable", 33: "IoT Gateway",
	34: "Embedded PC", 35: "Mini PC", 36: "Stick PC"}

var formFactors = map[int]string{1: "Other", 2: "Unknown", 3: "SIMM", 4: "SIP", 5: "Chip", 6: "DIP", 7: "ZIP", 8: "Proprietary Card",
	9: "DIMM", 10: "TSOP", 11: "Row Of Chips", 12: "RIMM", 13: "SODIMM", 14: "SRIMM", 15: "FB-DIMM", 16: "Die", 17: "CAMM"}

var memTypes = map[int]string{1: "Other", 2: "Unknown", 3: "DRAM", 4: "EDRAM", 5: "VRAM", 6: "SRAM", 7: "RAM", 8: "ROM", 9: "Flash",
	10: "EEPROM", 11: "FEPROM", 12: "EPROM", 13: "CDRAM", 14: "3DRAM", 15: "SDRAM", 16: "SGRAM", 17: "RDRAM", 18: "DDR", 19: "DDR2",
	20: "DDR2 FB-DIMM", 24: "DDR3", 25: "FBD2", 26: "DDR4", 27: "LPDDR", 28: "LPDDR2", 29: "LPDDR3", 30: "LPDDR4",
	31: "Logical non-volatile device", 32: "HBM", 33: "HBM2", 34: "DDR5", 35: "LPDDR5", 36: "HBM3"}
