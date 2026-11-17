package ipmi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/noahzmr/tyr/internal/model"
)

// sdrRecord is one sensor (full or compact) or FRU locator from the BMC's
// sensor data repository.
type sdrRecord struct {
	id         uint16
	typ        byte // 0x01 full, 0x02 compact, 0x11 FRU device locator
	owner, lun byte
	number     byte
	sensorType byte
	eventType  byte
	name       string
	units1     byte
	baseUnit   byte
	m, b       int
	rExp, bExp int
	linear     byte
	thresholds [6]*byte // lnr, lc, lnc, unc, uc, unr (raw)
	fruID      byte     // FRU locator: logical device id
	fruLogical bool
}

// readSDR reads every record: a reservation, then each record in pieces
// small enough for any BMC.
func readSDR(t transport) ([]sdrRecord, error) {
	res, err := t.send(netFnStorage, 0x22, nil)
	if err != nil || len(res) < 2 {
		return nil, fmt.Errorf("reserve SDR repository: %w", errOr(err))
	}
	rsv := binary.LittleEndian.Uint16(res)
	var out []sdrRecord
	id := uint16(0)
	for n := 0; n < 4096 && id != 0xFFFF; n++ {
		hdr, next, err := sdrPart(t, rsv, id, 0, 5)
		if err != nil {
			var ce *CompletionError
			if errors.As(err, &ce) && ce.Code == 0xC5 { // reservation lost: once more
				if r2, e2 := t.send(netFnStorage, 0x22, nil); e2 == nil && len(r2) >= 2 {
					rsv = binary.LittleEndian.Uint16(r2)
					n--
					continue
				}
			}
			return out, err
		}
		if len(hdr) < 5 {
			break
		}
		body := make([]byte, 0, int(hdr[4]))
		for off := 0; off < int(hdr[4]); off += 16 {
			part, _, err := sdrPart(t, rsv, id, 5+off, min(16, int(hdr[4])-off))
			if err != nil {
				return out, err
			}
			body = append(body, part...)
		}
		if r, ok := parseSDR(binary.LittleEndian.Uint16(hdr), hdr[3], body); ok {
			out = append(out, r)
		}
		id = next
	}
	return out, nil
}

func errOr(err error) error {
	if err == nil {
		return errors.New("short answer")
	}
	return err
}

func sdrPart(t transport, rsv, id uint16, off, n int) ([]byte, uint16, error) {
	req := []byte{byte(rsv), byte(rsv >> 8), byte(id), byte(id >> 8), byte(off), byte(n)}
	r, err := t.send(netFnStorage, 0x23, req)
	if err != nil {
		return nil, 0, err
	}
	if len(r) < 2 {
		return nil, 0, errors.New("get SDR: short answer")
	}
	return r[2:], binary.LittleEndian.Uint16(r), nil
}

// parseSDR reads a record body (after the 5-byte header).
func parseSDR(id uint16, typ byte, b []byte) (sdrRecord, bool) {
	r := sdrRecord{id: id, typ: typ}
	at := func(i int) byte { // record offsets as in the spec (header included)
		if i-5 < len(b) {
			return b[i-5]
		}
		return 0
	}
	idString := func(lenAt int) string {
		tl := at(lenAt)
		n := int(tl & 0x1F)
		start := lenAt + 1 - 5
		if start+n > len(b) {
			n = max(0, len(b)-start)
		}
		return strings.TrimRight(strings.TrimSpace(string(b[start:start+n])), "\x00")
	}
	switch typ {
	case 0x01, 0x02:
		r.owner, r.lun, r.number = at(5), at(6)&3, at(7)
		r.sensorType, r.eventType = at(12), at(13)&0x7F
		r.units1, r.baseUnit = at(20), at(21)
		if typ == 0x01 {
			r.linear = at(23) & 0x7F
			r.m = tenBit(int(at(24)) | int(at(25)>>6)<<8)
			r.b = tenBit(int(at(26)) | int(at(27)>>6)<<8)
			r.rExp, r.bExp = fourBit(int(at(29)>>4)), fourBit(int(at(29)&0x0F))
			// readable threshold mask (byte 18): bit0 LNC, 1 LC, 2 LNR, 3 UNC,
			// 4 UC, 5 UNR; values at 36 UNR, 37 UC, 38 UNC, 39 LNR, 40 LC, 41 LNC
			readable := at(18)
			for i, x := range []struct{ bit, off int }{{2, 39}, {1, 40}, {0, 41}, {3, 38}, {4, 37}, {5, 36}} { // lnr lc lnc unc uc unr
				if readable&(1<<x.bit) != 0 {
					v := at(x.off)
					r.thresholds[i] = &v
				}
			}
			r.name = idString(47)
		} else {
			r.name = idString(31)
		}
	case 0x11:
		r.fruID, r.fruLogical = at(6), at(7)&0x80 != 0
		r.name = idString(15)
	default:
		return r, false
	}
	return r, true
}

func tenBit(v int) int {
	if v&0x200 != 0 {
		return v - 0x400
	}
	return v
}

func fourBit(v int) int {
	if v&0x8 != 0 {
		return v - 0x10
	}
	return v
}

// convert turns a raw reading into units: y = (M·x + B·10^Bexp)·10^Rexp
// for linear sensors (linearization 0); nil for others.
func (r sdrRecord) convert(raw byte) *float64 {
	if r.typ != 0x01 || r.linear != 0 || r.units1>>6 == 3 {
		return nil
	}
	var x float64
	switch r.units1 >> 6 {
	case 0:
		x = float64(raw)
	case 1:
		x = float64(int8(raw))
		if raw&0x80 != 0 {
			x++ // one's complement
		}
	case 2:
		x = float64(int8(raw))
	}
	y := (float64(r.m)*x + float64(r.b)*math.Pow10(r.bExp)) * math.Pow10(r.rExp)
	y = math.Round(y*1000) / 1000
	return &y
}

// unitName is ipmitool's word for the record's unit.
func (r sdrRecord) unitName() string {
	if r.units1&1 != 0 {
		return "percent"
	}
	switch r.baseUnit {
	case 1:
		return "degrees C"
	case 2:
		return "degrees F"
	case 4:
		return "Volts"
	case 5:
		return "Amps"
	case 6:
		return "Watts"
	case 18:
		return "RPM"
	}
	if r.typ == 0x02 || r.eventType != 0x01 {
		return "discrete"
	}
	return "unspecified"
}

// readSensors reads every sensor the BMC owns, with value, unit, status and
// thresholds - what `ipmitool sensor` lists.
func readSensors(t transport, recs []sdrRecord) []Sensor {
	var out []Sensor
	for _, r := range recs {
		if (r.typ != 0x01 && r.typ != 0x02) || r.owner != 0x20 || r.lun != 0 {
			continue // other controllers need bridging; the BMC's own sensors suffice
		}
		unit, kind := unitKind(r.unitName())
		kind = fanDuty(r.name, unit, kind)
		if kind == model.KindOther && r.sensorType == 0x04 && r.units1&1 != 0 {
			kind = model.KindFan
		}
		s := Sensor{Name: r.name, Kind: kind, Unit: unit}
		resp, err := t.send(netFnSensor, 0x2D, []byte{r.number})
		switch {
		case err != nil || len(resp) < 2:
			s.Status, s.StatusText = model.StatusUnknown, "na"
		case resp[1]&0x20 != 0 || resp[1]&0x40 == 0: // reading unavailable / scanning disabled
			s.Status, s.StatusText = model.StatusUnknown, "na"
		case r.typ == 0x01 && r.eventType == 0x01:
			s.Value = r.convert(resp[0])
			s.Status, s.StatusText = model.StatusOK, "ok"
			if len(resp) >= 3 {
				// bit0 ≤ LNC, 1 ≤ LC, 2 ≤ LNR, 3 ≥ UNC, 4 ≥ UC, 5 ≥ UNR
				switch st := resp[2]; {
				case st&0x36 != 0:
					s.Status, s.StatusText = model.StatusCrit, "cr"
				case st&0x09 != 0:
					s.Status, s.StatusText = model.StatusWarn, "nc"
				}
			}
			s.Thresholds = r.thresholdsModel()
		default:
			s.Status, s.StatusText = model.StatusOK, "ok"
			state := uint16(0)
			if len(resp) >= 3 {
				state = uint16(resp[2])
			}
			if len(resp) >= 4 {
				state |= uint16(resp[3]&0x7F) << 8
			}
			s.Discrete = fmt.Sprintf("0x%04x", state)
			if r.typ == 0x01 && r.linear == 0 {
				s.Value = r.convert(resp[0])
			}
		}
		out = append(out, s)
	}
	return out
}

func (r sdrRecord) thresholdsModel() *model.Thresholds {
	conv := func(i int) *float64 {
		if r.thresholds[i] == nil {
			return nil
		}
		return r.convert(*r.thresholds[i])
	}
	th := &model.Thresholds{Origin: "bmc", LowerCrit: conv(1), LowerWarn: conv(2), UpperWarn: conv(3), UpperCrit: conv(4)}
	if th.LowerCrit == nil && th.LowerWarn == nil && th.UpperWarn == nil && th.UpperCrit == nil {
		return nil
	}
	return th
}
