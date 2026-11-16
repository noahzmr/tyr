package model

// Memory is the memory module section.
type Memory struct {
	Header
	TotalBytes             uint64 `json:"total_bytes"` // MemTotal (excludes kernel/firmware reservations)
	AvailableBytes         uint64 `json:"available_bytes"`
	UsedBytes              uint64 `json:"used_bytes"`
	SwapTotalBytes         uint64 `json:"swap_total_bytes"`
	SwapUsedBytes          uint64 `json:"swap_used_bytes"`
	HugePagesTotal         uint64 `json:"hugepages_total"`
	HugePageSizeBytes      uint64 `json:"hugepage_size_bytes"`
	HardwareCorruptedBytes uint64 `json:"hardware_corrupted_bytes"` // poisoned pages

	OnlineBytes    *uint64 `json:"online_bytes"` // memory blocks online (incl. kernel)
	OfflineBlocks  int     `json:"offline_blocks"`
	InstalledBytes *uint64 `json:"installed_bytes"` // sum of DIMM sizes (SMBIOS)

	ECCType          string  `json:"ecc_type,omitempty"` // SMBIOS type 16: "Multi-bit ECC", "None"
	MaxCapacityBytes *uint64 `json:"max_capacity_bytes"`
	Slots            int     `json:"slots"`
	PopulatedSlots   int     `json:"populated_slots"`

	CorrectableErrors   *uint64 `json:"correctable_errors"` // EDAC totals; nil if EDAC unavailable
	UncorrectableErrors *uint64 `json:"uncorrectable_errors"`

	DIMMs     []DIMM           `json:"dimms"`
	EDAC      []EDACController `json:"edac"`
	NUMANodes []NUMANode       `json:"numa_nodes"`
	Temps     []Sensor         `json:"temps"`
}

// DIMM is one memory slot (populated or empty).
type DIMM struct {
	Locator             string   `json:"locator"`
	BankLocator         string   `json:"bank_locator,omitempty"`
	Channel             string   `json:"channel,omitempty"` // derived slot group, e.g. "P1-A"
	Populated           bool     `json:"populated"`
	SizeBytes           uint64   `json:"size_bytes"`
	Type                string   `json:"type,omitempty"`
	FormFactor          string   `json:"form_factor,omitempty"`
	SpeedMTs            *int     `json:"speed_mts"`
	ConfiguredSpeedMTs  *int     `json:"configured_speed_mts"`
	Rank                *int     `json:"rank"`
	ECC                 *bool    `json:"ecc"`
	Manufacturer        string   `json:"manufacturer,omitempty"`
	PartNumber          string   `json:"part_number,omitempty"`
	Serial              string   `json:"serial,omitempty"`
	AssetTag            string   `json:"asset_tag,omitempty"`
	EDACLabel           string   `json:"edac_label,omitempty"`
	CorrectableErrors   *uint64  `json:"correctable_errors"`
	UncorrectableErrors *uint64  `json:"uncorrectable_errors"`
	TempCelsius         *float64 `json:"temp_celsius"`
	Status              Status   `json:"status"`
}

// EDACController is one EDAC memory controller.
type EDACController struct {
	Name                string     `json:"name"`
	Type                string     `json:"type,omitempty"`
	SizeBytes           uint64     `json:"size_bytes"`
	CorrectableErrors   uint64     `json:"correctable_errors"`
	UncorrectableErrors uint64     `json:"uncorrectable_errors"`
	CorrectableNoInfo   uint64     `json:"correctable_noinfo"`
	UncorrectableNoInfo uint64     `json:"uncorrectable_noinfo"`
	DIMMs               []EDACDIMM `json:"dimms"`
}

// EDACDIMM is one DIMM as seen by EDAC.
type EDACDIMM struct {
	Name                string `json:"name"`
	Label               string `json:"label,omitempty"`
	Location            string `json:"location,omitempty"`
	SizeBytes           uint64 `json:"size_bytes"`
	Locator             string `json:"locator,omitempty"` // matched SMBIOS locator
	CorrectableErrors   uint64 `json:"correctable_errors"`
	UncorrectableErrors uint64 `json:"uncorrectable_errors"`
}

// NUMANode is the memory of one NUMA node.
type NUMANode struct {
	ID         int    `json:"id"`
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
}

func (m *Memory) counters(c map[string]uint64) {
	c["mem.hardware_corrupted_bytes"] = m.HardwareCorruptedBytes
	for _, mc := range m.EDAC {
		c["edac."+mc.Name+".ce"] = mc.CorrectableErrors + mc.CorrectableNoInfo
		c["edac."+mc.Name+".ue"] = mc.UncorrectableErrors + mc.UncorrectableNoInfo
		for _, d := range mc.DIMMs {
			c["edac."+mc.Name+"."+d.Name+".ce"] = d.CorrectableErrors
			c["edac."+mc.Name+"."+d.Name+".ue"] = d.UncorrectableErrors
		}
	}
}
