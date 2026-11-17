package model

import (
	"strings"
	"time"
)

// Sections that are filled in by later collectors. Their typed fields are
// added together with the collector that produces them.

// System holds host identity and chassis-level data.
type System struct {
	Header
	Vendor        string   `json:"vendor,omitempty"`
	Product       string   `json:"product,omitempty"`
	Serial        string   `json:"serial,omitempty"`
	BIOSVendor    string   `json:"bios_vendor,omitempty"`
	BIOSVersion   string   `json:"bios_version,omitempty"`
	BIOSDate      string   `json:"bios_date,omitempty"`
	BoardVendor   string   `json:"board_vendor,omitempty"`
	BoardProduct  string   `json:"board_product,omitempty"`
	BoardSerial   string   `json:"board_serial,omitempty"`
	BMCFirmware   string   `json:"bmc_firmware,omitempty"`
	BMCVendor     string   `json:"bmc_vendor,omitempty"`
	UUID          string   `json:"uuid,omitempty"`
	SKU           string   `json:"sku,omitempty"`
	AssetTag      string   `json:"asset_tag,omitempty"`
	ChassisVendor string   `json:"chassis_vendor,omitempty"`
	ChassisType   string   `json:"chassis_type,omitempty"`
	ChassisSerial string   `json:"chassis_serial,omitempty"`
	ChassisAsset  string   `json:"chassis_asset_tag,omitempty"`
	PSUs          []PSUFRU `json:"psus"` // SMBIOS type 39 inventory
	FRUs          []FRU    `json:"frus"` // BMC FRU devices
	Kernel        string   `json:"kernel,omitempty"`
	UptimeSeconds *float64 `json:"uptime_seconds"`
	Fans          []Sensor `json:"fans"`
	Temps         []Sensor `json:"temps"` // inlet/exhaust/ambient
	// Boots are the boots the journal knows, oldest first.
	Boots []Boot `json:"boots,omitempty"`
}

// Power is the power module section.
type Power struct {
	Header
	Source        string        `json:"source,omitempty"` // "redfish (host interface)", "smbios"
	ConsumedWatts *float64      `json:"consumed_watts,omitempty"`
	CapacityWatts *float64      `json:"capacity_watts,omitempty"`
	LimitWatts    *float64      `json:"limit_watts,omitempty"` // power cap
	AvgWatts      *float64      `json:"avg_watts,omitempty"`
	MinWatts      *float64      `json:"min_watts,omitempty"`
	MaxWatts      *float64      `json:"max_watts,omitempty"`
	IntervalMin   int           `json:"interval_min,omitempty"` // of avg/min/max
	Redundancy    []Redundancy  `json:"redundancy,omitempty"`
	Supplies      []PowerSupply `json:"supplies"`
}

// PowerSupply is one supply with its live readings (Redfish), or its
// SMBIOS record when the BMC cannot be read.
type PowerSupply struct {
	Name          string   `json:"name"`
	Bay           string   `json:"bay,omitempty"`
	Present       bool     `json:"present"`
	State         string   `json:"state,omitempty"`  // Redfish Status.State: Enabled, Absent, UnavailableOffline
	Health        string   `json:"health,omitempty"` // OK, Warning, Critical; SMBIOS "Present, Critical"
	Detail        string   `json:"detail,omitempty"` // vendor state (HPE PowerSupplyStatus)
	Model         string   `json:"model,omitempty"`
	Manufacturer  string   `json:"manufacturer,omitempty"`
	Serial        string   `json:"serial,omitempty"`
	PartNumber    string   `json:"part_number,omitempty"`
	SparePart     string   `json:"spare_part_number,omitempty"`
	Firmware      string   `json:"firmware,omitempty"`
	Type          string   `json:"type,omitempty"` // AC, DC
	CapacityWatts *float64 `json:"capacity_watts,omitempty"`
	OutputWatts   *float64 `json:"output_watts,omitempty"`
	InputVolts    *float64 `json:"input_volts,omitempty"`
	InputType     string   `json:"input_type,omitempty"` // ACHighLine …
	Status        Status   `json:"status"`
}

// Redundancy is a power supply redundancy group.
type Redundancy struct {
	Name   string `json:"name"`
	Mode   string `json:"mode,omitempty"`
	Health string `json:"health,omitempty"`
	Status Status `json:"status"`
}

// Events is the events module section.
type Events struct {
	Header
	Logs    []EventLog `json:"logs"`
	Entries []Event    `json:"entries"` // oldest first
}

// EventLog is one log Tyr read: the HPE IML, the iLO event log, the IPMI
// SEL, the kernel log.
type EventLog struct {
	Name    string `json:"name"`
	Source  string `json:"source"` // redfish, ipmi, kernel
	Entries int    `json:"entries"`
}

// Event is one entry of a BMC or kernel log.
type Event struct {
	Time      time.Time `json:"time,omitzero"`
	Updated   time.Time `json:"updated,omitzero"` // last occurrence (HPE IML)
	Log       string    `json:"log"`
	Source    string    `json:"source"`
	ID        string    `json:"id,omitempty"`
	Severity  Status    `json:"severity"`
	Class     string    `json:"class,omitempty"`     // HPE class ("System Error"), SEL sensor type
	Component string    `json:"component,omitempty"` // sensor, device
	Message   string    `json:"message"`
	Count     int       `json:"count,omitempty"`
	Repaired  *bool     `json:"repaired,omitempty"`
}

// PSUFRU is a power supply as described by SMBIOS type 39.
type PSUFRU struct {
	Location     string   `json:"location"`
	Name         string   `json:"name,omitempty"`
	Manufacturer string   `json:"manufacturer,omitempty"`
	Model        string   `json:"model,omitempty"`
	Serial       string   `json:"serial,omitempty"`
	Revision     string   `json:"revision,omitempty"`
	MaxWatts     *float64 `json:"max_watts"`
	StatusText   string   `json:"status_text,omitempty"` // "Present, OK"
	Present      bool     `json:"present"`
}

// Boot is one boot from the journal. Clean: journald logged "Journal
// stopped" (an orderly shutdown); nil when that cannot be told.
type Boot struct {
	ID      string    `json:"id"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Current bool      `json:"current,omitempty"`
	Clean   *bool     `json:"clean,omitempty"`
}

// Label names the PSU: its location, else its name (HPE leaves the
// location empty: "Power Supply 2").
func (p PSUFRU) Label() string {
	if p.Location != "" {
		return p.Location
	}
	if p.Name != "" {
		return p.Name
	}
	return "PSU"
}

// Health reads the SMBIOS type 39 status text ("Present, OK",
// "Present, Critical", "Present, Non-critical").
func (p PSUFRU) Health() Status {
	low := strings.ToLower(p.StatusText)
	switch {
	case !p.Present:
		return StatusUnknown
	case strings.Contains(low, "non-critical") || strings.Contains(low, "degraded"):
		return StatusWarn
	case strings.Contains(low, "critical") || strings.Contains(low, "fail") || strings.Contains(low, "lost"):
		return StatusCrit
	case strings.Contains(low, "ok"):
		return StatusOK
	}
	return StatusUnknown
}

// FRU is one BMC FRU device (chassis/board/product/PSU records).
type FRU struct {
	Name          string `json:"name"`
	Manufacturer  string `json:"manufacturer,omitempty"`
	Product       string `json:"product,omitempty"`
	PartNumber    string `json:"part_number,omitempty"`
	Serial        string `json:"serial,omitempty"`
	ChassisSerial string `json:"chassis_serial,omitempty"`
	ChassisPart   string `json:"chassis_part_number,omitempty"`
	MfgDate       string `json:"mfg_date,omitempty"` // YYYY-MM-DD (board manufacturing date)
}
