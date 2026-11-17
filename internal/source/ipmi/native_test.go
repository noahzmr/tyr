package ipmi

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/netinfo"
)

// fakeBMC answers like an HPE iLO 5 behind /dev/ipmi0.
type fakeBMC struct {
	sdr  map[uint16][]byte // id -> record incl. 5-byte header
	next map[uint16]uint16
	sel  map[uint16][]byte
	seln map[uint16]uint16
	fru  []byte
	sent [][2]byte
}

func full(id uint16, num, sensorType, units1, baseUnit, m byte, rbExp byte, readable byte, thr [6]byte, name string) []byte {
	body := make([]byte, 43) // offsets 5..47
	set := func(off int, v byte) { body[off-5] = v }
	set(5, 0x20)
	set(7, num)
	set(12, sensorType)
	set(13, 0x01)
	set(18, readable)
	set(20, units1)
	set(21, baseUnit)
	set(24, m)
	set(29, rbExp)
	for i, off := range []int{36, 37, 38, 39, 40, 41} { // unr uc unc lnr lc lnc
		set(off, thr[i])
	}
	set(47, 0xC0|byte(len(name)))
	body = append(body, name...)
	return append([]byte{byte(id), byte(id >> 8), 0x51, 0x01, byte(len(body))}, body...)
}

func compact(id uint16, num, sensorType, eventType byte, name string) []byte {
	body := make([]byte, 27) // offsets 5..31
	body[0], body[2], body[7], body[8] = 0x20, num, sensorType, eventType
	body[15] = 0xC0 // no analog reading
	body[26] = 0xC0 | byte(len(name))
	body = append(body, name...)
	return append([]byte{byte(id), byte(id >> 8), 0x51, 0x02, byte(len(body))}, body...)
}

func fruLocator(id uint16, fru byte, name string) []byte {
	body := make([]byte, 11) // offsets 5..15
	body[0], body[1], body[2] = 0x20, fru, 0x80
	body[10] = 0xC0 | byte(len(name))
	body = append(body, name...)
	return append([]byte{byte(id), byte(id >> 8), 0x51, 0x11, byte(len(body))}, body...)
}

func selRec(id uint16, ts uint32, sensorType, num, dirType, data1 byte) []byte {
	r := make([]byte, 16)
	binary.LittleEndian.PutUint16(r, id)
	r[2] = 0x02
	binary.LittleEndian.PutUint32(r[3:], ts)
	r[7], r[9], r[10], r[11], r[12], r[13] = 0x20, 0x04, sensorType, num, dirType, data1
	return r
}

func tl(s string) []byte { return append([]byte{0xC0 | byte(len(s))}, s...) }

func fruImage(mfg time.Time) []byte {
	board := []byte{1, 0, 0}
	mins := int(mfg.Sub(time.Date(1996, 1, 1, 0, 0, 0, 0, time.UTC)).Minutes())
	board = append(board, byte(mins), byte(mins>>8), byte(mins>>16))
	board = append(append(append(append(append(board, tl("HPE")...), tl("ProLiant DL360 Gen10")...), tl("PVZEK0ARHB96DS")...), tl("875073-001")...), 0xC1)
	for len(board)%8 != 7 {
		board = append(board, 0)
	}
	board = append(board, 0)
	board[1] = byte(len(board) / 8)
	product := []byte{1, 0, 0}
	product = append(append(append(append(append(append(append(product, tl("HPE")...), tl("ProLiant DL360 Gen10")...), tl("867959-B21")...), tl("")...), tl("CZJ84001XQ")...), tl("")...), 0xC1)
	for len(product)%8 != 0 {
		product = append(product, 0)
	}
	product[1] = byte(len(product) / 8)
	hdr := []byte{1, 0, 0, 1, byte(1 + len(board)/8), 0, 0, 0}
	return append(append(hdr, board...), product...)
}

func newFake() *fakeBMC {
	f := &fakeBMC{sdr: map[uint16][]byte{}, next: map[uint16]uint16{}, sel: map[uint16][]byte{}, seln: map[uint16]uint16{}}
	f.sdr[0] = full(0, 0x02, 0x01, 0x00, 1, 1, 0x00, 0x18, [6]byte{0, 100, 90, 0, 0, 0}, "02-CPU 1")
	f.sdr[1] = full(1, 0x30, 0x04, 0x01, 0, 39, 0xE0, 0, [6]byte{}, "Fan 1 DutyCycle")
	f.sdr[2] = compact(2, 0x40, 0x08, 0x6F, "Power Supply 2")
	f.sdr[3] = fruLocator(3, 2, "PSU 2")
	f.next = map[uint16]uint16{0: 1, 1: 2, 2: 3, 3: 0xFFFF}
	f.sel[0] = selRec(0, 1794000000, 0x0C, 0x87, 0x6F, 0xA1)
	f.sel[2] = selRec(2, 1794003600, 0x08, 0x40, 0x6F, 0x03)
	f.seln = map[uint16]uint16{0: 2, 2: 0xFFFF}
	f.fru = fruImage(time.Date(2018, 10, 2, 0, 0, 0, 0, time.UTC))
	return f
}

func (f *fakeBMC) send(netfn, cmd byte, d []byte) ([]byte, error) {
	f.sent = append(f.sent, [2]byte{netfn, cmd})
	switch [2]byte{netfn, cmd} {
	case [2]byte{netFnApp, 0x01}:
		return []byte{0x20, 0x01, 0x03, 0x20, 0x02, 0xBF, 0x5C, 0xB8, 0x00, 0x00, 0x00}, nil
	case [2]byte{netFnStorage, 0x22}:
		return []byte{0x34, 0x12}, nil
	case [2]byte{netFnStorage, 0x23}:
		id, off, n := binary.LittleEndian.Uint16(d[2:]), int(d[4]), int(d[5])
		rec := f.sdr[id]
		nx := f.next[id]
		return append([]byte{byte(nx), byte(nx >> 8)}, rec[off:min(off+n, len(rec))]...), nil
	case [2]byte{netFnSensor, 0x2D}:
		switch d[0] {
		case 0x02:
			return []byte{92, 0xC0, 0x08}, nil // above upper non-critical
		case 0x30:
			return []byte{60, 0xC0, 0x00}, nil
		case 0x40:
			return []byte{0, 0xC0, 0x02, 0x00}, nil
		}
	case [2]byte{netFnStorage, 0x40}:
		return []byte{0x51, 2, 0, 0xE0, 0x0F, 0, 0, 0, 0, 0, 0, 0, 0, 0x0F}, nil
	case [2]byte{netFnStorage, 0x43}:
		id := binary.LittleEndian.Uint16(d[2:])
		nx := f.seln[id]
		return append([]byte{byte(nx), byte(nx >> 8)}, f.sel[id]...), nil
	case [2]byte{netFnStorage, 0x10}:
		if d[0] != 0 {
			return nil, &CompletionError{Cmd: "Get FRU Inventory Area Info", Code: 0xCB}
		}
		return []byte{byte(len(f.fru)), byte(len(f.fru) >> 8), 0}, nil
	case [2]byte{netFnStorage, 0x11}:
		off, n := int(binary.LittleEndian.Uint16(d[1:])), int(d[3])
		part := f.fru[off:min(off+n, len(f.fru))]
		return append([]byte{byte(len(part))}, part...), nil
	case [2]byte{netFnTransport, 0x02}:
		if d[0] != 1 {
			return nil, &CompletionError{Code: 0xCC}
		}
		v := map[byte][]byte{3: {10, 16, 25, 99}, 4: {0x01}, 5: {0x94, 0x40, 0xC9, 0x01, 0x02, 0x03}, 6: {255, 255, 255, 0}, 12: {10, 16, 25, 1}, 20: {0, 0}}[d[1]]
		return append([]byte{0x11}, v...), nil
	}
	return nil, fmt.Errorf("fake: no answer for %02x/%02x", netfn, cmd)
}

func TestNativeIPMI(t *testing.T) {
	f := newFake()
	c := &Client{Tool: Native, nat: &native{t: guarded{f}}}
	ctx := context.Background()
	if v, fw, err := c.MCInfo(ctx); err != nil || v != "Hewlett Packard Enterprise" || fw != "3.20" {
		t.Errorf("mc info %q %q %v", v, fw, err)
	}
	ss, err := c.Sensors(ctx)
	if err != nil || len(ss) != 3 {
		t.Fatalf("sensors %+v %v", ss, err)
	}
	cpu, fan, psu := ss[0], ss[1], ss[2]
	if cpu.Name != "02-CPU 1" || cpu.Kind != model.KindTemperature || *cpu.Value != 92 || cpu.Status != model.StatusWarn ||
		cpu.Thresholds == nil || *cpu.Thresholds.UpperWarn != 90 || *cpu.Thresholds.UpperCrit != 100 {
		t.Errorf("cpu %+v %+v", cpu, cpu.Thresholds)
	}
	if fan.Kind != model.KindFan || fan.Unit != "percent" || *fan.Value != 23.4 {
		t.Errorf("fan %+v value %v", fan, *fan.Value)
	}
	if psu.Value != nil || psu.Discrete != "0x0002" || psu.Status != model.StatusOK {
		t.Errorf("psu %+v", psu)
	}
	sel, err := c.SEL(ctx)
	if err != nil || len(sel.Entries) != 2 || sel.Used != 2 || sel.Capacity != 256 {
		t.Fatalf("sel %+v %v", sel, err)
	}
	ue, ac := sel.Entries[0], sel.Entries[1]
	if ue.SensorType != "Memory" || ue.Sensor != "#0x87" || ue.Event != "Uncorrectable ECC" || ue.Category != "memory" || ue.Severity != model.StatusCrit ||
		!ue.Time.Equal(time.Unix(1794000000, 0).UTC()) {
		t.Errorf("UE %+v", ue)
	}
	if ac.Sensor != "Power Supply 2" || ac.Event != "Power Supply AC lost" || ac.Category != "psu" || ac.Severity != model.StatusCrit {
		t.Errorf("AC lost %+v", ac)
	}
	frus, err := c.FRUs(ctx)
	if err != nil || len(frus) != 1 {
		t.Fatalf("frus %+v %v", frus, err)
	}
	if fr := frus[0]; fr.Name != "Builtin FRU Device" || fr.Manufacturer != "HPE" || fr.Product != "ProLiant DL360 Gen10" || fr.Serial != "CZJ84001XQ" ||
		fr.PartNumber != "867959-B21" || fr.MfgDate != "2018-10-02" {
		t.Errorf("fru %+v", fr)
	}
	lan, err := c.LanPrint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if l := netinfo.ParseIPMILan(lan); l == nil || l.IP != "10.16.25.99" || l.IPSource != "Static Address" || l.MAC != "94:40:c9:01:02:03" || l.VLAN != "Disabled" {
		t.Errorf("lan %+v\n%s", l, lan)
	}
	// only read-only requests ever reach the BMC
	for _, s := range f.sent {
		if _, ok := readOnly[s]; !ok {
			t.Errorf("sent %02x/%02x", s[0], s[1])
		}
	}
	if _, err := (guarded{f}).send(0x00, 0x02, []byte{0}); !errors.Is(err, ErrNotAllowed) { // chassis control
		t.Errorf("chassis control passed: %v", err)
	}
	if !strings.Contains(fmt.Sprint(readOnly[[2]byte{netFnSensor, 0x2D}]), "Reading") {
		t.Error("allowlist")
	}
}
