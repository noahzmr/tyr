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
type Power struct{ Header }

// Events is the events module section.
type Events struct{ Header }

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
