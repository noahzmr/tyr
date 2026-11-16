package dmi

import (
	"strconv"
	"strings"
)

// MemoryArray is SMBIOS type 16 (Physical Memory Array).
type MemoryArray struct {
	Handle          string
	Location        string // "System Board Or Motherboard"
	Use             string // "System Memory"
	ErrorCorrection string // "Multi-bit ECC", "Single-bit ECC", "None"
	MaxCapacity     uint64 // bytes, 0 if unknown
	Devices         int
}

// MemoryArrays returns the system memory arrays (type 16, Use: System Memory).
func (t *Tables) MemoryArrays() []MemoryArray {
	var out []MemoryArray
	for _, r := range t.ByType(16) {
		if u := r.Fields["Use"]; u != "" && u != "System Memory" {
			continue
		}
		a := MemoryArray{
			Handle: r.Handle, Location: r.Get("Location"), Use: r.Get("Use"),
			ErrorCorrection: r.Fields["Error Correction Type"],
			MaxCapacity:     ParseSize(r.Fields["Maximum Capacity"]),
		}
		if n := ParseInt(r.Fields["Number Of Devices"]); n != nil {
			a.Devices = *n
		}
		out = append(out, a)
	}
	return out
}

// MemoryDevice is SMBIOS type 17. Empty slots have Populated=false.
type MemoryDevice struct {
	Handle        string
	ArrayHandle   string
	Locator       string // "P1-DIMMA1", "DIMM_B1", "A1"
	BankLocator   string // "P0_Node0_Channel0_Dimm0", "BANK 0"
	Populated     bool
	SizeBytes     uint64
	FormFactor    string
	Type          string // DDR4, DDR5
	TypeDetail    string
	SpeedMTs      *int // rated
	ConfiguredMTs *int // running
	Rank          *int
	TotalWidth    *int // bits, incl. ECC
	DataWidth     *int
	Manufacturer  string
	PartNumber    string
	Serial        string
	AssetTag      string
}

// ECC reports whether the module carries ECC bits (total width > data width).
func (d MemoryDevice) ECC() bool {
	return d.TotalWidth != nil && d.DataWidth != nil && *d.TotalWidth > *d.DataWidth
}

// MemoryDevices returns all type 17 records belonging to system memory
// arrays (flash/cache arrays are skipped when the array handle is known).
func (t *Tables) MemoryDevices() []MemoryDevice {
	system := map[string]bool{}
	for _, a := range t.MemoryArrays() {
		system[a.Handle] = true
	}
	var out []MemoryDevice
	for _, r := range t.ByType(17) {
		ah := r.Fields["Array Handle"]
		if len(system) > 0 && ah != "" && !system[ah] {
			continue
		}
		size := ParseSize(r.Fields["Size"])
		d := MemoryDevice{
			Handle: r.Handle, ArrayHandle: ah,
			Locator: r.Get("Locator"), BankLocator: r.Get("Bank Locator"),
			Populated: size > 0, SizeBytes: size,
			FormFactor: r.Get("Form Factor"), Type: r.Get("Type"), TypeDetail: r.Get("Type Detail"),
			SpeedMTs:      speedMTs(r.Fields["Speed"]),
			ConfiguredMTs: speedMTs(firstNonEmpty(r.Fields["Configured Memory Speed"], r.Fields["Configured Clock Speed"])),
			Rank:          ParseInt(r.Fields["Rank"]),
			TotalWidth:    bits(r.Fields["Total Width"]),
			DataWidth:     bits(r.Fields["Data Width"]),
			Manufacturer:  jedecName(r.Get("Manufacturer")), PartNumber: r.Get("Part Number"), Serial: r.Get("Serial Number"),
			AssetTag: r.Get("Asset Tag"),
		}
		if !d.Populated {
			d.Type, d.Manufacturer, d.PartNumber, d.Serial, d.AssetTag = "", "", "", "", ""
		}
		out = append(out, d)
	}
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// ParseSize parses "32 GB", "16384 MB", "2 TB", "512 kB"; 0 for
// "No Module Installed", "Unknown" or anything else unparseable.
func ParseSize(s string) uint64 {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0
	}
	n, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		return 0
	}
	switch strings.ToUpper(f[1]) {
	case "B", "BYTES":
		return n
	case "KB":
		return n << 10
	case "MB":
		return n << 20
	case "GB":
		return n << 30
	case "TB":
		return n << 40
	}
	return 0
}

func speedMTs(s string) *int {
	v := ParseSpeedMHz(s) // "3200 MT/s" or legacy "3200 MHz"
	if v == nil {
		return nil
	}
	i := int(*v)
	return &i
}

func bits(s string) *int {
	f := strings.Fields(s)
	if len(f) < 1 {
		return nil
	}
	return ParseInt(f[0])
}

// jedecName maps JEDEC manufacturer IDs that some BIOSes print in hex
// ("00AD00B300AD", "80CE000080CE", "Unknown (802C)") to vendor names.
func jedecName(s string) string {
	u := strings.ToUpper(s)
	if i := strings.Index(u, "("); i >= 0 {
		u = strings.Trim(u[i:], "() ")
	}
	if len(u) < 4 || strings.Trim(u, "0123456789ABCDEF") != "" {
		return s
	}
	ids := map[string]string{"CE": "Samsung", "AD": "SK Hynix", "2C": "Micron", "98": "Kingston", "94": "Smart Modular", "FE": "Elpida"}
	if name, ok := ids[u[2:4]]; ok && (u[:2] == "00" || u[:2] == "80" || u[:2] == "01" || u[:2] == "81") {
		return name
	}
	return s
}
