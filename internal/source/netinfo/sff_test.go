package netinfo

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/noahzmr/tyr/internal/model"
)

func sensor(t *model.Transceiver, name string) *model.Sensor {
	for i := range t.Sensors {
		if t.Sensors[i].Name == name {
			return &t.Sensors[i]
		}
	}
	return nil
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// A 25G SR SFP28 at 36.5 °C, 3.3 V, tx −2.0 dBm, rx −13.9 dBm (weak), with
// the module's own limits.
func TestSFPEEPROM(t *testing.T) {
	b := make([]byte, 512)
	b[0] = 0x03
	b[36] = 0x03 // 25GBASE-SR
	copy(b[20:], "FINISAR CORP.   ")
	copy(b[40:], "FTLF8536P4BCL   ")
	copy(b[68:], "XYZ12345        ")
	copy(b[84:], "21031500")
	binary.BigEndian.PutUint16(b[60:], 850)
	b[92] = 0x68 // DDM implemented, internally calibrated
	a2 := b[256:]
	be16 := func(o int, v uint16) { binary.BigEndian.PutUint16(a2[o:], v) }
	s16 := func(v int16) uint16 { return uint16(v) }
	be16(0, s16(75*256)) // temp high alarm
	be16(2, s16(-5*256)) // temp low alarm
	be16(4, s16(70*256)) // temp high warning
	be16(6, 0)           // temp low warning
	be16(32, 10000)      // rx high alarm 1 mW = 0 dBm
	be16(34, 500)        // rx low alarm 0.05 mW = −13.01 dBm
	be16(36, 7943)       // rx high warning −1.0 dBm
	be16(38, 794)        // rx low warning −11.0 dBm
	be16(96, s16(36*256+128))
	be16(98, 33000)
	be16(102, 6310) // 0.631 mW = −2.0 dBm
	be16(104, 407)  // 0.0407 mW = −13.9 dBm
	m := ParseModuleEEPROM(sff8472, b)
	if m == nil || m.Identifier != "SFP" || m.Type != "25G Base-SR" || m.Vendor != "FINISAR CORP." || m.PartNumber != "FTLF8536P4BCL" ||
		m.Serial != "XYZ12345" || m.Wavelength != "850nm" || m.DateCode != "2021-03-15" {
		t.Fatalf("identity %+v", m)
	}
	if s := sensor(m, "Module temperature"); s == nil || *s.Value != 36.5 || *s.Thresholds.UpperCrit != 75 || *s.Thresholds.LowerCrit != -5 {
		t.Errorf("temperature %+v", s)
	}
	if s := sensor(m, "Module voltage"); s == nil || !near(*s.Value, 3.3) {
		t.Errorf("voltage %+v", s)
	}
	if s := sensor(m, "Tx power"); s == nil || !near(*s.Value, -2.0) {
		t.Errorf("tx %+v", s)
	}
	rx := sensor(m, "Rx power")
	if rx == nil || !near(*rx.Value, -13.9) || !near(*rx.Thresholds.LowerCrit, -13.01) || !near(*rx.Thresholds.LowerWarn, -11.0) {
		t.Errorf("rx %+v %+v", rx, rx.Thresholds)
	}
	// no light at all reads as −40 dBm, like ethtool's "-inf"
	binary.BigEndian.PutUint16(a2[104:], 0)
	if s := sensor(ParseModuleEEPROM(sff8472, b), "Rx power"); *s.Value != -40 {
		t.Errorf("dark rx %v", *s.Value)
	}
	// externally calibrated: no diagnostics rather than wrong ones
	b[92] = 0x50
	if m := ParseModuleEEPROM(sff8472, b); len(m.Sensors) != 0 {
		t.Errorf("external calibration decoded: %+v", m.Sensors)
	}
}

func TestQSFPEEPROM(t *testing.T) {
	b := make([]byte, 640)
	b[0] = 0x11
	b[192] = 0x03 // 100GBASE-SR4
	copy(b[148:], "Mellanox        ")
	copy(b[168:], "MMA1B00-C100D   ")
	copy(b[196:], "MT2010FT01234   ")
	copy(b[212:], "20030100")
	binary.BigEndian.PutUint16(b[186:], 850*20)
	binary.BigEndian.PutUint16(b[22:], uint16(int16(40*256)))
	binary.BigEndian.PutUint16(b[26:], 32900)
	for lane := 0; lane < 4; lane++ {
		binary.BigEndian.PutUint16(b[34+2*lane:], 5012) // rx −3.0 dBm
		binary.BigEndian.PutUint16(b[50+2*lane:], 7943) // tx −1.0 dBm
	}
	binary.BigEndian.PutUint16(b[36:], 0) // lane 2 dark
	m := ParseModuleEEPROM(sff8636, b)
	if m == nil || m.Identifier != "QSFP28" || m.Type != "100G Base-SR4" || m.Vendor != "Mellanox" || m.Wavelength != "850nm" || m.DateCode != "2020-03-01" {
		t.Fatalf("identity %+v", m)
	}
	if s := sensor(m, "Module temperature"); s == nil || *s.Value != 40 {
		t.Errorf("temperature %+v", s)
	}
	if s := sensor(m, "Rx power lane 1"); s == nil || !near(*s.Value, -3.0) {
		t.Errorf("rx lane 1 %+v", s)
	}
	if s := sensor(m, "Rx power lane 2"); s == nil || *s.Value != -40 {
		t.Errorf("rx lane 2 %+v", s)
	}
	if s := sensor(m, "Tx power lane 4"); s == nil || !near(*s.Value, -1.0) {
		t.Errorf("tx lane 4 %+v", s)
	}
}
