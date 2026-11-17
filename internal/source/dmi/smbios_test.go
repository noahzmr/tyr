package dmi

import (
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

// rec builds one SMBIOS structure: the formatted area (after the 4-byte
// header) and its strings.
func rec(typ byte, handle uint16, body []byte, strs ...string) []byte {
	b := []byte{typ, byte(4 + len(body)), 0, 0}
	binary.LittleEndian.PutUint16(b[2:], handle)
	b = append(b, body...)
	if len(strs) == 0 {
		return append(b, 0, 0)
	}
	for _, s := range strs {
		b = append(append(b, s...), 0)
	}
	return append(b, 0)
}

func le16(v int) []byte { return []byte{byte(v), byte(v >> 8)} }
func le32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// an HPE-like table: BIOS, system, board, chassis, two CPUs, a memory
// array with a filled and an empty slot, two PSUs (one critical), the
// Redfish host interface.
func hpeTable() []byte {
	uuidBytes := []byte{0x38, 0x36, 0x37, 0x39, 0x35, 0x39, 0x43, 0x5A, 0x4A, 0x38, 0x34, 0x30, 0x30, 0x31, 0x58, 0x51}
	t := cat(
		rec(0, 0x0000, cat([]byte{1, 2}, le16(0xF000), []byte{3}, []byte{0xFF}), "HPE", "U32", "04/01/2026"),
		rec(1, 0x0001, cat([]byte{1, 2, 0, 3}, uuidBytes, []byte{6, 4, 5}), "HPE", "ProLiant DL360 Gen10", "CZJ84001XQ", "867959-B21", "ProLiant"),
		rec(2, 0x0002, []byte{1, 2, 0, 3, 0}, "HPE", "ProLiant DL360 Gen10", "PVZEK0ARHB96DS"),
		rec(3, 0x0003, []byte{1, 23, 0, 2, 0}, "HPE", "CZJ84001XQ"),
	)
	cpu := func(h uint16, sock string, status byte, cores, threads byte) []byte {
		body := cat([]byte{1, 3, 0xB3, 2}, make([]byte, 8), []byte{3}, []byte{0x8C}, le16(100), le16(4000), le16(3000), []byte{status, 0x06},
			le16(0xFFFF), le16(0xFFFF), le16(0xFFFF), []byte{0, 0, 0, cores, cores, threads}, le16(0x00FC), le16(0xB3))
		return rec(4, h, body, sock, "Intel(R) Corporation", "Intel(R) Xeon(R) Gold 6136 CPU @ 3.00GHz")
	}
	t = cat(t, cpu(0x0400, "Proc 1", 0x41, 12, 24), cpu(0x0401, "Proc 2", 0x41, 12, 24))
	t = cat(t, rec(16, 0x1000, cat([]byte{3, 3, 6}, le32(0x80000000), le16(0xFFFE), le16(24), le64(6<<40))))
	dimm := func(h uint16, loc string, sizeMB int, filled bool) []byte {
		size, speed, conf, typ, detail := le16(0), le16(0), le16(0), byte(2), 0
		strs := []string{loc, "PROC 1 DIMM"}
		ids := []byte{0, 0, 0, 0, 0}
		if filled {
			size, speed, conf, typ, detail = le16(sizeMB), le16(2666), le16(2666), 26, 0x2080
			strs = append(strs, "HPE", "840759-091")
			ids = []byte{3, 0, 0, 4, 2}
		}
		body := cat(le16(0x1000), le16(0xFFFE), le16(72), le16(64), size, []byte{9, 0, 1, 2, typ}, le16(detail), speed, ids, le32(0), conf)
		return rec(17, h, body, strs...)
	}
	t = cat(t, dimm(0x1100, "PROC 1 DIMM 8", 0x7FFF, true), dimm(0x1101, "PROC 1 DIMM 1", 0, false))
	// the 64 GB DIMM has size 0x7FFF: its extended size is set by patchExtSize
	psu := func(h uint16, name, serial string, chars int) []byte {
		body := cat([]byte{1, 0, 1, 2, 3, 0, 4, 0}, le16(800), le16(chars), le16(0xFFFF), le16(0xFFFF), le16(0xFFFF))
		return rec(39, h, body, name, "HPE", serial, "865414-B21")
	}
	t = cat(t, psu(0x2700, "Power Supply 1", "5WBXU0DLLCQ744", 0x01A2), psu(0x2701, "Power Supply 2", "5WBXT0D4DBV14E", 0x02A2))
	// type 42: network interface, one Redfish-over-IP protocol record
	proto := make([]byte, 91)
	proto[16], proto[17] = 1, 1 // host IP static, IPv4
	copy(proto[18:], []byte{16, 1, 15, 2})
	proto[50], proto[51] = 1, 1 // service static, IPv4
	copy(proto[52:], []byte{16, 1, 15, 1})
	copy(proto[84:], le16(443))
	t = cat(t, rec(42, 0x2A00, cat([]byte{0x40, 0, 1, 4, byte(len(proto))}, proto)))
	return cat(t, rec(127, 0xFFFF, nil))
}

func le64(v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return b
}

// the same machine as dmidecode prints it
const hpeText = `# dmidecode 3.5
SMBIOS 3.2.0 present.

Handle 0x0000, DMI type 0, 26 bytes
BIOS Information
	Vendor: HPE
	Version: U32
	Release Date: 04/01/2026

Handle 0x0001, DMI type 1, 27 bytes
System Information
	Manufacturer: HPE
	Product Name: ProLiant DL360 Gen10
	Version: Not Specified
	Serial Number: CZJ84001XQ
	UUID: 39373638-3935-5A43-4A38-343030315851
	SKU Number: 867959-B21
	Family: ProLiant

Handle 0x0002, DMI type 2, 17 bytes
Base Board Information
	Manufacturer: HPE
	Product Name: ProLiant DL360 Gen10
	Version: Not Specified
	Serial Number: PVZEK0ARHB96DS
	Asset Tag: Not Specified

Handle 0x0003, DMI type 3, 22 bytes
Chassis Information
	Manufacturer: HPE
	Type: Rack Mount Chassis
	Serial Number: CZJ84001XQ

Handle 0x0400, DMI type 4, 48 bytes
Processor Information
	Socket Designation: Proc 1
	Type: Central Processor
	Manufacturer: Intel(R) Corporation
	Version: Intel(R) Xeon(R) Gold 6136 CPU @ 3.00GHz
	External Clock: 100 MHz
	Max Speed: 4000 MHz
	Current Speed: 3000 MHz
	Status: Populated, Enabled
	Core Count: 12
	Core Enabled: 12
	Thread Count: 24

Handle 0x0401, DMI type 4, 48 bytes
Processor Information
	Socket Designation: Proc 2
	Type: Central Processor
	Manufacturer: Intel(R) Corporation
	Version: Intel(R) Xeon(R) Gold 6136 CPU @ 3.00GHz
	Max Speed: 4000 MHz
	Current Speed: 3000 MHz
	Status: Populated, Enabled
	Core Count: 12
	Core Enabled: 12
	Thread Count: 24

Handle 0x1000, DMI type 16, 23 bytes
Physical Memory Array
	Location: System Board Or Motherboard
	Use: System Memory
	Error Correction Type: Multi-bit ECC
	Maximum Capacity: 6 TB
	Number Of Devices: 24

Handle 0x1100, DMI type 17, 84 bytes
Memory Device
	Array Handle: 0x1000
	Total Width: 72 bits
	Data Width: 64 bits
	Size: 64 GB
	Form Factor: DIMM
	Locator: PROC 1 DIMM 8
	Bank Locator: PROC 1 DIMM
	Type: DDR4
	Type Detail: Synchronous Registered (Buffered)
	Speed: 2666 MT/s
	Manufacturer: HPE
	Part Number: 840759-091
	Rank: 2
	Configured Memory Speed: 2666 MT/s

Handle 0x1101, DMI type 17, 84 bytes
Memory Device
	Array Handle: 0x1000
	Total Width: 72 bits
	Data Width: 64 bits
	Size: No Module Installed
	Form Factor: DIMM
	Locator: PROC 1 DIMM 1
	Bank Locator: PROC 1 DIMM
	Type: Unknown
	Type Detail: None
	Speed: Unknown

Handle 0x2700, DMI type 39, 22 bytes
System Power Supply
	Power Unit Group: 1
	Name: Power Supply 1
	Manufacturer: HPE
	Serial Number: 5WBXU0DLLCQ744
	Model Part Number: 865414-B21
	Max Power Capacity: 800 W
	Status: Present, OK

Handle 0x2701, DMI type 39, 22 bytes
System Power Supply
	Power Unit Group: 1
	Name: Power Supply 2
	Manufacturer: HPE
	Serial Number: 5WBXT0D4DBV14E
	Model Part Number: 865414-B21
	Max Power Capacity: 800 W
	Status: Present, Critical

Handle 0x2A00, DMI type 42, 100 bytes
Management Controller Host Interface
	Host Interface Type: Network
	Protocol ID: 04 (Redfish over IP)
		IPv4 Redfish Service Address: 16.1.15.1
		Redfish Service Port: 443
`

// The native decoder and dmidecode's text give Tyr the same data.
func TestTableMatchesDmidecode(t *testing.T) {
	tab := hpeTable()
	// the 64 GB module: extended size 65536 MB at 0x1C
	native, err := ParseTable(patchExtSize(tab), []byte("_SM3_\x00\x18\x03\x02\x00"))
	if err != nil {
		t.Fatal(err)
	}
	text, err := Parse([]byte(hpeText))
	if err != nil {
		t.Fatal(err)
	}
	if native.Version != "3.2.0" {
		t.Errorf("version %q", native.Version)
	}
	for _, typ := range []int{0, 1, 2, 3} {
		for _, k := range []string{"Vendor", "Version", "Release Date", "Manufacturer", "Product Name", "Serial Number", "UUID", "SKU Number", "Asset Tag", "Type"} {
			if a, b := native.ByType(typ)[0].Get(k), text.ByType(typ)[0].Get(k); a != b {
				t.Errorf("type %d %s: native %q, dmidecode %q", typ, k, a, b)
			}
		}
	}
	if a, b := native.Processors(), text.Processors(); !reflect.DeepEqual(a, b) {
		t.Errorf("processors:\nnative    %+v\ndmidecode %+v", a, b)
	}
	if a, b := native.MemoryArrays(), text.MemoryArrays(); !reflect.DeepEqual(a, b) {
		t.Errorf("arrays:\nnative    %+v\ndmidecode %+v", a, b)
	}
	if a, b := native.MemoryDevices(), text.MemoryDevices(); !reflect.DeepEqual(a, b) {
		t.Errorf("DIMMs:\nnative    %+v\ndmidecode %+v", a, b)
	}
	if a, b := native.PowerSupplies(), text.PowerSupplies(); !reflect.DeepEqual(a, b) {
		t.Errorf("PSUs:\nnative    %+v\ndmidecode %+v", a, b)
	}
	var lines []string
	for _, r := range native.ByType(42) {
		for _, l := range r.Lists {
			lines = append(lines, l...)
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), "IPv4 Redfish Service Address: 16.1.15.1") || !strings.Contains(strings.Join(lines, "\n"), "Redfish Service Port: 443") {
		t.Errorf("host interface lines %v", lines)
	}
}

// patchExtSize sets the extended size (0x1C) of the first type 17 record.
func patchExtSize(tab []byte) []byte {
	out := append([]byte(nil), tab...)
	for off := 0; off+4 <= len(out); {
		typ, n := out[off], int(out[off+1])
		if typ == 17 {
			binary.LittleEndian.PutUint32(out[off+0x1C:], 65536)
			break
		}
		end := off + n
		for end+1 < len(out) && !(out[end] == 0 && out[end+1] == 0) {
			end++
		}
		off = end + 2
	}
	return out
}

func TestTableErrors(t *testing.T) {
	if _, err := ParseTable(nil, nil); err == nil {
		t.Error("empty table parsed")
	}
	if _, err := ParseTable([]byte{4, 2, 0, 0}, nil); err == nil {
		t.Error("bad length parsed")
	}
}
