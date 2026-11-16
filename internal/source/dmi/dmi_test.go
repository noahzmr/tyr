package dmi

import (
	"os"
	"testing"
)

func TestParseServer(t *testing.T) {
	b, err := os.ReadFile("../../../testdata/fixtures/server-healthy/cmd/dmidecode.txt")
	if err != nil {
		t.Fatal(err)
	}
	tb, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if tb.Version != "3.3.0" {
		t.Errorf("version %q", tb.Version)
	}
	sys := tb.ByType(1)
	if len(sys) != 1 || sys[0].Get("Product Name") != "SYS-120U-TNR" || sys[0].Get("Version") != "" {
		t.Errorf("system %+v", sys)
	}
	if feats := tb.ByType(2)[0].Lists["Features"]; len(feats) != 2 || feats[1] != "Board is replaceable" {
		t.Errorf("board features %v", feats)
	}
	ps := tb.Processors()
	if len(ps) != 2 || ps[1].Socket != "CPU2" || !ps[1].Populated || !ps[1].Enabled || *ps[1].Threads != 32 || *ps[1].MaxSpeedMHz != 4000 {
		t.Errorf("processors %+v", ps)
	}
}

// Socket states seen on real hardware (Dell R740 with one empty socket,
// HPE with a CPU disabled after a POST error).
const sockets = `# dmidecode 3.3
SMBIOS 3.2 present.

Handle 0x0400, DMI type 4, 48 bytes
Processor Information
	Socket Designation: CPU1
	Type: Central Processor
	Version: Intel(R) Xeon(R) Gold 6230 CPU @ 2.10GHz
	Max Speed: 4000 MHz
	Status: Populated, Enabled
	Core Count: 20
	Thread Count: 40

Handle 0x0401, DMI type 4, 48 bytes
Processor Information
	Socket Designation: CPU2
	Type: Central Processor
	Version: Not Specified
	Max Speed: Unknown
	Status: Unpopulated
	Core Count: Unknown

Handle 0x0402, DMI type 4, 48 bytes
Processor Information
	Socket Designation: Proc 3
	Type: Central Processor
	Status: Populated, Disabled By BIOS (POST Error)
	Core Count: 20
	Thread Count: 40

Handle 0x0403, DMI type 4, 48 bytes
Processor Information
	Socket Designation: Proc 4
	Type: Math Processor
	Status: Populated, Enabled
`

func TestProcessorStates(t *testing.T) {
	tb, err := Parse([]byte(sockets))
	if err != nil {
		t.Fatal(err)
	}
	ps := tb.Processors()
	if len(ps) != 3 {
		t.Fatalf("want 3 CPU sockets (math processor skipped), got %d", len(ps))
	}
	if ps[1].Populated || ps[1].Version != "" || ps[1].Cores != nil || ps[1].MaxSpeedMHz != nil {
		t.Errorf("unpopulated %+v", ps[1])
	}
	if !ps[2].Populated || ps[2].Enabled || ps[2].Status != "Populated, Disabled By BIOS (POST Error)" {
		t.Errorf("disabled %+v", ps[2])
	}
}

func TestParseEmpty(t *testing.T) {
	if _, err := Parse([]byte("# dmidecode 3.5\n/sys/firmware/dmi/tables/smbios_entry_point: Permission denied\n")); err == nil {
		t.Error("expected error for output without records")
	}
}

// Older dmidecode (2.12, RHEL 7) prints "Configured Clock Speed" and MB sizes.
const oldMemory = `SMBIOS 2.8 present.

Handle 0x1000, DMI type 16, 23 bytes
Physical Memory Array
	Location: System Board Or Motherboard
	Use: System Memory
	Error Correction Type: Multi-bit ECC
	Maximum Capacity: 768 GB
	Number Of Devices: 24

Handle 0x1001, DMI type 16, 23 bytes
Physical Memory Array
	Location: System Board Or Motherboard
	Use: Flash Memory
	Error Correction Type: None
	Maximum Capacity: 16 MB
	Number Of Devices: 1

Handle 0x1100, DMI type 17, 40 bytes
Memory Device
	Array Handle: 0x1000
	Total Width: 72 bits
	Data Width: 64 bits
	Size: 16384 MB
	Form Factor: DIMM
	Locator: A1
	Bank Locator: Not Specified
	Type: DDR4
	Speed: 2666 MHz
	Manufacturer: 00AD00B300AD
	Serial Number: 2A7F3C11
	Part Number: HMA82GR7AFR8N-VK
	Rank: 2
	Configured Clock Speed: 2400 MHz

Handle 0x1101, DMI type 17, 40 bytes
Memory Device
	Array Handle: 0x1000
	Total Width: 72 bits
	Data Width: 64 bits
	Size: No Module Installed
	Locator: A2
	Speed: Unknown
	Manufacturer: Not Specified
	Part Number: Not Specified

Handle 0x1102, DMI type 17, 40 bytes
Memory Device
	Array Handle: 0x1001
	Size: 16 MB
	Locator: BIOS FLASH
`

func TestMemoryDevices(t *testing.T) {
	tb, err := Parse([]byte(oldMemory))
	if err != nil {
		t.Fatal(err)
	}
	arr := tb.MemoryArrays()
	if len(arr) != 1 || arr[0].MaxCapacity != 768<<30 || arr[0].Devices != 24 || arr[0].ErrorCorrection != "Multi-bit ECC" {
		t.Errorf("arrays %+v", arr)
	}
	ds := tb.MemoryDevices()
	if len(ds) != 2 {
		t.Fatalf("flash device must be skipped, got %d", len(ds))
	}
	d := ds[0]
	if !d.Populated || d.SizeBytes != 16<<30 || *d.SpeedMTs != 2666 || *d.ConfiguredMTs != 2400 || !d.ECC() || *d.Rank != 2 {
		t.Errorf("A1 %+v", d)
	}
	if d.Manufacturer != "SK Hynix" {
		t.Errorf("JEDEC id not mapped: %q", d.Manufacturer)
	}
	for in, want := range map[string]string{"80CE000080CE": "Samsung", "Unknown (802C)": "Micron", "Samsung": "Samsung", "ABCD": "ABCD"} {
		if got := jedecName(in); got != want {
			t.Errorf("jedecName(%q) = %q, want %q", in, got, want)
		}
	}
	if ds[1].Populated || ds[1].SizeBytes != 0 || ds[1].SpeedMTs != nil || ds[1].PartNumber != "" {
		t.Errorf("A2 %+v", ds[1])
	}
	for in, want := range map[string]uint64{"32 GB": 32 << 30, "2 TB": 2 << 40, "512 kB": 512 << 10, "Unknown": 0, "No Module Installed": 0} {
		if got := ParseSize(in); got != want {
			t.Errorf("ParseSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestPowerSuppliesAndCPUIDs(t *testing.T) {
	b, _ := os.ReadFile("../../../testdata/fixtures/server-degraded/cmd/dmidecode.txt")
	tb, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	ps := tb.PowerSupplies()
	if len(ps) != 2 || ps[1].Location != "PSU2" || ps[1].Serial != "P1K22CL21123452" || *ps[1].MaxWatts != 1200 || !ps[1].Present {
		t.Errorf("%+v", ps)
	}
	if p := tb.Processors()[0]; p.Serial != "" || p.AssetTag != "CPU1" {
		t.Errorf("cpu ids %+v", p) // Intel prints "Serial Number: Not Specified"
	}
	if ch := tb.ByType(3); len(ch) != 1 || ch[0].Get("Serial Number") != "C1160LL12AB0123" {
		t.Errorf("chassis %+v", ch)
	}
}
