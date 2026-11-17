package netinfo

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/noahzmr/tyr/internal/model"
)

// Module EEPROM types (ETHTOOL_GMODULEINFO).
const (
	sff8079 = 1 // SFP, 256 bytes (no diagnostics page)
	sff8472 = 2 // SFP with diagnostics: A0h + A2h, 512 bytes
	sff8636 = 3 // QSFP+/QSFP28: page 0 (and page 3 at 384)
	sff8436 = 4 // QSFP+
)

var be = binary.BigEndian // SFF fields are big-endian

// ParseModuleEEPROM decodes a transceiver's EEPROM into the data ParseModule
// takes from `ethtool -m`: identity, wavelength, date code, temperature,
// voltage, optical power (per lane on QSFP) with the module's own alarm and
// warning thresholds. Diagnostics are read only when the module implements
// them and calibrates internally; nil when the bytes are not a module.
func ParseModuleEEPROM(typ int, b []byte) *model.Transceiver {
	switch typ {
	case sff8079, sff8472:
		if len(b) < 128 {
			return nil
		}
		return sfp(b)
	case sff8636, sff8436:
		if len(b) < 256 {
			return nil
		}
		return qsfp(b)
	}
	return nil
}

func ascii(b []byte) string { return strings.TrimSpace(strings.TrimRight(string(b), "\x00")) }

var identifiers = map[byte]string{0x03: "SFP", 0x0C: "QSFP", 0x0D: "QSFP+", 0x11: "QSFP28", 0x18: "QSFP-DD", 0x19: "OSFP", 0x1E: "QSFP+ or later with CMIS"}

// powerDBm converts a 0.1 µW reading to dBm; no light is −40 dBm, as
// ParseModule reads ethtool's "-inf".
func powerDBm(raw uint16) float64 {
	if raw == 0 {
		return -40
	}
	mw := float64(raw) / 10000
	return math.Round(10*math.Log10(mw)*100) / 100
}

func th(hiAlarm, loAlarm, hiWarn, loWarn float64) *model.Thresholds {
	return &model.Thresholds{Origin: "module", UpperCrit: &hiAlarm, LowerCrit: &loAlarm, UpperWarn: &hiWarn, LowerWarn: &loWarn}
}

func add(t *model.Transceiver, name string, kind model.SensorKind, unit string, v float64, thr *model.Thresholds) {
	t.Sensors = append(t.Sensors, model.Sensor{Name: name, Kind: kind, Value: &v, Unit: unit, Thresholds: thr, Source: "ethtool"})
}

func dateCode(b []byte) string { return moduleDate(ascii(b)) }

// sfp: SFF-8472. A0h at 0, A2h (diagnostics) at 256.
func sfp(b []byte) *model.Transceiver {
	t := &model.Transceiver{Sensors: []model.Sensor{}, Identifier: identifiers[b[0]]}
	if t.Identifier == "" {
		t.Identifier = fmt.Sprintf("0x%02x", b[0])
	}
	t.Type = sfpType(b)
	t.Vendor, t.PartNumber, t.Serial = ascii(b[20:36]), ascii(b[40:56]), ascii(b[68:84])
	t.DateCode = dateCode(b[84:92])
	if wl := be.Uint16(b[60:62]); wl > 0 && b[8]&0x0C == 0 { // copper cables carry a compliance code there
		t.Wavelength = fmt.Sprintf("%dnm", wl)
	}
	ddm, internal := b[92]&0x40 != 0, b[92]&0x20 != 0
	if len(b) < 512 || !ddm || !internal {
		return t
	}
	a2 := b[256:]
	s16 := func(o int) float64 { return float64(int16(be.Uint16(a2[o:]))) / 256 }
	u16 := func(o int) uint16 { return be.Uint16(a2[o:]) }
	volt := func(o int) float64 { return float64(u16(o)) / 10000 }
	dbm := func(o int) float64 { return powerDBm(u16(o)) }
	add(t, "Module temperature", model.KindTemperature, model.UnitCelsius, s16(96), th(s16(0), s16(2), s16(4), s16(6)))
	add(t, "Module voltage", model.KindVoltage, model.UnitVolts, volt(98), th(volt(8), volt(10), volt(12), volt(14)))
	add(t, "Tx power", model.KindOptical, model.UnitDBm, dbm(102), th(dbm(24), dbm(26), dbm(28), dbm(30)))
	add(t, "Rx power", model.KindOptical, model.UnitDBm, dbm(104), th(dbm(32), dbm(34), dbm(36), dbm(38)))
	return t
}

// sfpType names the module from its compliance codes, the way people say
// it ("25G Base-SR", "10G Base-LR", "1000BASE-SX", "Copper DAC").
func sfpType(b []byte) string {
	switch b[36] { // extended compliance (SFF-8024)
	case 0x02:
		return "25G AOC"
	case 0x03:
		return "25G Base-SR"
	case 0x04:
		return "25G Base-LR"
	case 0x0B, 0x0C, 0x0D:
		return "25G Base-CR (DAC)"
	}
	switch {
	case b[3]&0x10 != 0:
		return "10G Base-SR"
	case b[3]&0x20 != 0:
		return "10G Base-LR"
	case b[3]&0x40 != 0:
		return "10G Base-LRM"
	case b[3]&0x80 != 0:
		return "10G Base-ER"
	case b[8]&0x04 != 0:
		return "Copper DAC (passive)"
	case b[8]&0x08 != 0:
		return "Copper DAC (active)"
	case b[6]&0x01 != 0:
		return "1000BASE-SX"
	case b[6]&0x02 != 0:
		return "1000BASE-LX"
	case b[6]&0x08 != 0:
		return "1000BASE-T"
	}
	return ""
}

// qsfp: SFF-8636. Lower page 0 at 0, upper page 0 at 128, page 3 at 384.
func qsfp(b []byte) *model.Transceiver {
	t := &model.Transceiver{Sensors: []model.Sensor{}, Identifier: identifiers[b[0]]}
	if t.Identifier == "" {
		t.Identifier = fmt.Sprintf("0x%02x", b[0])
	}
	t.Type = qsfpType(b)
	t.Vendor, t.PartNumber, t.Serial = ascii(b[148:164]), ascii(b[168:184]), ascii(b[196:212])
	t.DateCode = dateCode(b[212:220])
	if wl := be.Uint16(b[186:188]); wl > 0 && b[147]>>4 < 0x0A { // optical: wavelength in 0.05 nm
		t.Wavelength = fmt.Sprintf("%.0fnm", float64(wl)/20)
	}
	s16 := func(o int) float64 { return float64(int16(be.Uint16(b[o:]))) / 256 }
	u16 := func(o int) uint16 { return be.Uint16(b[o:]) }
	volt := func(o int) float64 { return float64(u16(o)) / 10000 }
	dbm := func(o int) float64 { return powerDBm(u16(o)) }
	var tTh, vTh, rxTh, txTh *model.Thresholds
	if len(b) >= 640 { // page 3: thresholds
		p3 := 384 - 128
		tTh = th(s16(p3+128), s16(p3+130), s16(p3+132), s16(p3+134))
		vTh = th(volt(p3+144), volt(p3+146), volt(p3+148), volt(p3+150))
		rxTh = th(dbm(p3+176), dbm(p3+178), dbm(p3+180), dbm(p3+182))
		txTh = th(dbm(p3+192), dbm(p3+194), dbm(p3+196), dbm(p3+198))
	}
	add(t, "Module temperature", model.KindTemperature, model.UnitCelsius, s16(22), tTh)
	add(t, "Module voltage", model.KindVoltage, model.UnitVolts, volt(26), vTh)
	for lane := 0; lane < 4; lane++ {
		add(t, fmt.Sprintf("Tx power lane %d", lane+1), model.KindOptical, model.UnitDBm, dbm(50+2*lane), txTh)
	}
	for lane := 0; lane < 4; lane++ {
		add(t, fmt.Sprintf("Rx power lane %d", lane+1), model.KindOptical, model.UnitDBm, dbm(34+2*lane), rxTh)
	}
	return t
}

func qsfpType(b []byte) string {
	switch b[192] { // extended compliance
	case 0x02:
		return "100G AOC"
	case 0x03:
		return "100G Base-SR4"
	case 0x04:
		return "100G Base-LR4"
	case 0x0B:
		return "100G Base-CR4 (DAC)"
	}
	switch {
	case b[131]&0x04 != 0:
		return "40G Base-SR4"
	case b[131]&0x02 != 0:
		return "40G Base-LR4"
	case b[131]&0x08 != 0:
		return "40G Base-CR4 (DAC)"
	}
	return ""
}
