package netbox

import (
	"regexp"
	"strings"
	"unicode"
)

// Manufacturer names as NetBox should keep them: one entry per company,
// whatever SMBIOS, PCI, SMART or a transceiver's EEPROM call it.

// vendorAlias maps lower-case names (after legal suffixes are dropped) to
// the name used in NetBox.
var vendorAlias = map[string]string{
	"genuineintel": "Intel", "intel": "Intel",
	"authenticamd": "AMD", "advanced micro devices": "AMD", "amd": "AMD", "amd/ati": "AMD",
	"hygongenuine":               "Hygon",
	"hewlett packard enterprise": "HPE", "hpe": "HPE",
	"hewlett-packard": "HP", "hp": "HP",
	"dell": "Dell", "dell emc": "Dell",
	"supermicro": "Supermicro", "super micro computer": "Supermicro",
	"lenovo": "Lenovo", "ibm": "IBM",
	"fujitsu": "Fujitsu", "cisco systems": "Cisco", "cisco": "Cisco",
	"asustek computer": "ASUS", "asus": "ASUS",
	"micro-star international": "MSI", "gigabyte technology": "Gigabyte", "giga-byte technology": "Gigabyte",
	"asrockrack": "ASRock Rack", "asrock": "ASRock", "asrock rack": "ASRock Rack",
	"micro-star": "MSI", "msi": "MSI", "gigabyte": "Gigabyte", "giga-byte": "Gigabyte", "asustek": "ASUS",
	"super micro": "Supermicro", "american megatrends": "AMI", "lite-on": "Lite-On",
	"smart modular": "SMART Modular", "western digital": "Western Digital", "matrox": "Matrox", "aspeed": "ASPEED",
	"nvidia": "NVIDIA", "mellanox technologies": "Mellanox", "mellanox": "Mellanox",
	"broadcom": "Broadcom", "broadcom / lsi": "Broadcom", "lsi logic / symbios logic": "Broadcom", "lsi": "Broadcom", "avago": "Broadcom",
	"qlogic": "QLogic", "marvell": "Marvell", "emulex": "Emulex", "chelsio communications": "Chelsio",
	"samsung": "Samsung", "samsung electronics": "Samsung",
	"sk hynix": "SK hynix", "hynix": "SK hynix", "hynix semiconductor": "SK hynix",
	"micron technology": "Micron", "micron": "Micron", "crucial": "Crucial",
	"seagate": "Seagate", "seagate technology": "Seagate",
	"wdc": "Western Digital", "wd": "Western Digital",
	"hgst": "HGST", "toshiba": "Toshiba", "kioxia": "Kioxia", "kingston": "Kingston", "kingston technology": "Kingston",
	"finisar": "Finisar", "fs": "FS", "fs.com": "FS",
	"aspeed technology": "ASPEED", "matrox electronics systems": "Matrox",
	"american megatrends international": "AMI",
	"delta":                             "Delta", "delta electronics": "Delta", "liteon": "Lite-On",
}

// legal suffixes, longest first
var legalSuffix = regexp.MustCompile(`(?i)([,.]?\s+(corporation|corp\.?|incorporated|inc\.?|co\.?,?\s*ltd\.?|ltd\.?|limited|llc|l\.l\.c\.|gmbh|ag|s\.a\.|b\.v\.|technologies|technology|computer|electronics|international|systems))+\.?$`)

// diskModel infers the maker of a drive that reports only its model.
var diskModel = []struct {
	re     *regexp.Regexp
	vendor string
}{
	{regexp.MustCompile(`(?i)^samsung\b|^MZ[0-9A-Z]`), "Samsung"},
	{regexp.MustCompile(`^ST\d`), "Seagate"},
	{regexp.MustCompile(`(?i)^(wdc\b|wd[ \-]?\w*\d)`), "Western Digital"},
	{regexp.MustCompile(`(?i)^(hgst\b|HU[SHC]\d)`), "HGST"},
	{regexp.MustCompile(`(?i)^toshiba\b|^(MG|AL|PX|KPM)\d`), "Toshiba"},
	{regexp.MustCompile(`(?i)^kioxia\b|^K[CX]D\d`), "Kioxia"},
	{regexp.MustCompile(`(?i)^micron\b|^MTFD`), "Micron"},
	{regexp.MustCompile(`(?i)^intel\b|^SSD(PE|SC)`), "Intel"},
	{regexp.MustCompile(`(?i)^solidigm\b|^SB[FMN]`), "Solidigm"},
	{regexp.MustCompile(`(?i)^kingston\b|^SA400|^SEDC|^DC[56]00`), "Kingston"},
	{regexp.MustCompile(`^CT\d`), "Crucial"},
	{regexp.MustCompile(`(?i)^(SK ?hynix|HFS\d)`), "SK hynix"},
}

// Manufacturer is the NetBox manufacturer for a vendor name; for drives
// without one, inferred from the model. Empty when unknown.
func Manufacturer(vendor, model string) string {
	v := strings.TrimSpace(vendor)
	if v == "" || strings.EqualFold(v, "ATA") || strings.EqualFold(v, "NVMe") || strings.EqualFold(v, "unknown") {
		for _, d := range diskModel {
			if d.re.MatchString(strings.TrimSpace(model)) {
				return d.vendor
			}
		}
		return ""
	}
	if i := strings.Index(v, " ["); i > 0 { // "Advanced Micro Devices, Inc. [AMD/ATI]"
		if j := strings.IndexByte(v[i:], ']'); j > 0 {
			v = v[i+2 : i+j]
		}
	}
	v = strings.TrimSpace(legalSuffix.ReplaceAllString(v, ""))
	v = strings.TrimRight(v, ",. ")
	if a, ok := vendorAlias[strings.ToLower(v)]; ok {
		return a
	}
	if len(v) > 4 && v == strings.ToUpper(v) && strings.IndexFunc(v, unicode.IsLetter) >= 0 {
		// "FINISAR" -> "Finisar"; short names (QEMU, HGST, IBM) stay
		words := strings.Fields(strings.ToLower(v))
		for i, w := range words {
			r := []rune(w)
			r[0] = unicode.ToUpper(r[0])
			words[i] = string(r)
		}
		v = strings.Join(words, " ")
	}
	return v
}
