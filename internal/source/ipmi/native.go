package ipmi

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// native is the /dev/ipmi0 backend. The sensor data repository is read
// once: it describes the hardware and does not change while Tyr runs.
type native struct {
	t      transport
	once   sync.Once
	recs   []sdrRecord
	sdrErr error
}

func (n *native) sdr() ([]sdrRecord, error) {
	n.once.Do(func() { n.recs, n.sdrErr = readSDR(n.t) })
	return n.recs, n.sdrErr
}

// OpenNative opens /dev/ipmi0 and asks the BMC who it is; an error means
// there is no usable BMC device (fall back to the tools).
func OpenNative() (*Client, error) {
	d, err := openDevice()
	if err != nil {
		return nil, err
	}
	c := &Client{Tool: Native, nat: &native{t: guarded{d}}}
	if _, _, err := c.MCInfo(context.Background()); err != nil {
		d.close()
		return nil, err
	}
	return c, nil
}

// --- controller ---------------------------------------------------------------------

// iana names the BMC vendors Tyr meets, by IANA enterprise number.
var iana = map[uint32]string{2: "IBM", 11: "Hewlett-Packard", 343: "Intel Corporation", 674: "Dell Inc.", 2011: "Huawei",
	4769: "ASUSTeK", 6569: "Inventec", 7154: "IBM", 10437: "Peppercon", 10876: "Super Micro Computer Inc.", 15370: "Giga-Byte",
	19046: "Lenovo", 20301: "IBM", 37945: "Inspur", 47196: "Hewlett Packard Enterprise", 49622: "Ampere", 51974: "OpenBMC", 57170: "Lenovo"}

func (n *native) mcInfo() (vendor, firmware string, err error) {
	r, err := n.t.send(netFnApp, 0x01, nil)
	if err != nil {
		return "", "", err
	}
	if len(r) < 9 {
		return "", "", fmt.Errorf("get device ID: short answer")
	}
	firmware = fmt.Sprintf("%d.%02x", r[2]&0x7F, r[3])
	id := uint32(r[6]) | uint32(r[7])<<8 | uint32(r[8]&0x0F)<<16
	vendor = iana[id]
	if vendor == "" {
		vendor = fmt.Sprintf("IANA %d", id)
	}
	return vendor, firmware, nil
}

// --- event log ---------------------------------------------------------------------------

func (n *native) sel() (*SEL, error) {
	info, err := n.t.send(netFnStorage, 0x40, nil)
	if err != nil {
		return nil, err
	}
	sel := &SEL{}
	if len(info) >= 5 {
		sel.Used = int(binary.LittleEndian.Uint16(info[1:]))
		sel.Capacity = sel.Used + int(binary.LittleEndian.Uint16(info[3:]))/16
	}
	names := map[byte]string{}
	if recs, err := n.sdr(); err == nil {
		for _, r := range recs {
			if r.owner == 0x20 && (r.typ == 0x01 || r.typ == 0x02) {
				names[r.number] = r.name
			}
		}
	}
	id := uint16(0)
	for i := 0; i < 10000 && id != 0xFFFF; i++ {
		r, err := n.t.send(netFnStorage, 0x43, []byte{0, 0, byte(id), byte(id >> 8), 0, 0xFF})
		if err != nil {
			if i == 0 {
				var ce *CompletionError
				if errors.As(err, &ce) && ce.Code == 0xCB { // empty log
					return sel, nil
				}
			}
			return sel, err
		}
		if len(r) < 18 {
			break
		}
		next := binary.LittleEndian.Uint16(r)
		sel.Entries = append(sel.Entries, selEntry(r[2:18], names))
		id = next
	}
	return sel, nil
}

// selEntry decodes one 16-byte record the way ipmitool's elist prints it.
func selEntry(rec []byte, names map[byte]string) SELEntry {
	e := SELEntry{ID: fmt.Sprintf("%x", binary.LittleEndian.Uint16(rec)), Asserted: true}
	typ := rec[2]
	if typ < 0xE0 {
		if ts := binary.LittleEndian.Uint32(rec[3:]); ts > 0x20000000 { // below: pre-init, relative time
			e.Time = time.Unix(int64(ts), 0).UTC()
		}
	}
	if typ != 0x02 {
		e.Event = fmt.Sprintf("OEM record 0x%02x", typ)
		classify(&e)
		return e
	}
	st, num, dir := rec[10], rec[11], rec[12]
	e.SensorType = sensorTypeNames[st]
	if name, ok := names[num]; ok && name != "" {
		e.Sensor = name
	} else {
		e.Sensor = fmt.Sprintf("#0x%02x", num)
	}
	e.Asserted = dir&0x80 == 0
	e.Event = eventText(st, dir&0x7F, rec[13]&0x0F)
	classify(&e)
	return e
}

var sensorTypeNames = map[byte]string{0x01: "Temperature", 0x02: "Voltage", 0x03: "Current", 0x04: "Fan", 0x05: "Physical Security",
	0x06: "Platform Security", 0x07: "Processor", 0x08: "Power Supply", 0x09: "Power Unit", 0x0A: "Cooling Device", 0x0B: "Other",
	0x0C: "Memory", 0x0D: "Drive Slot / Bay", 0x0E: "POST Memory Resize", 0x0F: "System Firmwares", 0x10: "Event Logging Disabled",
	0x11: "Watchdog1", 0x12: "System Event", 0x13: "Critical Interrupt", 0x14: "Button", 0x15: "Module / Board", 0x16: "Microcontroller",
	0x17: "Add-in Card", 0x18: "Chassis", 0x19: "Chip Set", 0x1A: "Other FRU", 0x1B: "Cable / Interconnect", 0x1C: "Terminator",
	0x1D: "System Boot Initiated", 0x1E: "Boot Error", 0x1F: "OS Boot", 0x20: "OS Critical Stop", 0x21: "Slot / Connector",
	0x22: "System ACPI Power State", 0x23: "Watchdog2", 0x24: "Platform Alert", 0x25: "Entity Presence", 0x26: "Monitor ASIC",
	0x27: "LAN", 0x28: "Management Subsystem Health", 0x29: "Battery", 0x2A: "Session Audit", 0x2B: "Version Change", 0x2C: "FRU State"}

var threshold = []string{"Lower Non-critical going low", "Lower Non-critical going high", "Lower Critical going low", "Lower Critical going high",
	"Lower Non-recoverable going low", "Lower Non-recoverable going high", "Upper Non-critical going low", "Upper Non-critical going high",
	"Upper Critical going low", "Upper Critical going high", "Upper Non-recoverable going low", "Upper Non-recoverable going high"}

var generic = map[byte][]string{
	0x02: {"Transition to Idle", "Transition to Active", "Transition to Busy"},
	0x03: {"State Deasserted", "State Asserted"},
	0x04: {"Predictive Failure deasserted", "Predictive Failure asserted"},
	0x05: {"Limit Not Exceeded", "Limit Exceeded"},
	0x06: {"Performance Met", "Performance Lags"},
	0x07: {"Transition to OK", "Transition to Non-critical from OK", "Transition to Critical from less severe", "Transition to Non-recoverable from less severe",
		"Transition to Non-critical from more severe", "Transition to Critical from Non-recoverable", "Transition to Non-recoverable", "Monitor", "Informational"},
	0x08: {"Device Absent", "Device Present"},
	0x09: {"Device Disabled", "Device Enabled"},
	0x0A: {"Transition to Running", "Transition to In Test", "Transition to Power Off", "Transition to On Line", "Transition to Off Line",
		"Transition to Off Duty", "Transition to Degraded", "Transition to Power Save", "Install Error"},
	0x0B: {"Fully Redundant", "Redundancy Lost", "Redundancy Degraded", "Non-Redundant: Sufficient from Redundant", "Non-Redundant: Sufficient from Insufficient",
		"Non-Redundant: Insufficient Resources", "Redundancy Degraded from Fully Redundant", "Redundancy Degraded from Non-redundant"},
	0x0C: {"D0 Power State", "D1 Power State", "D2 Power State", "D3 Power State"},
}

// sensorSpecific are the event texts of sensor-specific events (type 0x6F).
var sensorSpecific = map[byte][]string{
	0x05: {"General Chassis intrusion", "Drive Bay intrusion", "I/O Card area intrusion", "Processor area intrusion", "System unplugged from LAN", "Unauthorized dock", "FAN area intrusion"},
	0x07: {"IERR", "Thermal Trip", "FRB1/BIST failure", "FRB2/Hang in POST failure", "FRB3/Processor startup/init failure", "Configuration Error",
		"SM BIOS Uncorrectable CPU-complex Error", "Presence detected", "Disabled", "Terminator presence detected", "Throttled",
		"Uncorrectable machine check exception", "Correctable machine check error"},
	0x08: {"Presence detected", "Failure detected", "Predictive failure", "Power Supply AC lost", "AC lost or out-of-range", "AC out-of-range, but present", "Config Error"},
	0x09: {"Power off/down", "Power cycle", "240VA power down", "Interlock power down", "AC lost", "Soft-power control failure", "Failure detected", "Predictive failure"},
	0x0C: {"Correctable ECC", "Uncorrectable ECC", "Parity", "Memory Scrub Failed", "Memory Device Disabled", "Correctable ECC logging limit reached",
		"Presence Detected", "Configuration Error", "Spare", "Throttled", "Critical Overtemperature"},
	0x0D: {"Drive Present", "Drive Fault", "Predictive Failure", "Hot Spare", "Parity Check In Progress", "In Critical Array", "In Failed Array", "Rebuild in progress", "Rebuild aborted"},
	0x0F: {"System Firmware Error", "System Firmware Hang", "System Firmware Progress"},
	0x10: {"Correctable memory error logging disabled", "Event logging disabled", "Log area reset/cleared", "All event logging disabled", "Log full", "Log almost full"},
	0x12: {"System Reconfigured", "OEM System boot event", "Undetermined system hardware failure", "Entry added to auxiliary log", "PEF Action", "Timestamp Clock Sync"},
	0x13: {"Front Panel NMI", "Bus Timeout", "I/O channel check NMI", "Software NMI", "PCI PERR", "PCI SERR", "EISA failsafe timeout",
		"Bus Correctable error", "Bus Uncorrectable error", "Fatal NMI", "Bus Fatal Error", "Bus Degraded"},
	0x14: {"Power Button pressed", "Sleep Button pressed", "Reset Button pressed", "FRU latch open", "FRU service request button"},
	0x1D: {"Initiated by power up", "Initiated by hard reset", "Initiated by warm reset", "User requested PXE boot", "Automatic boot to diagnostic",
		"OS initiated hard reset", "OS initiated warm reset", "System Restart"},
	0x1E: {"No bootable media", "Non-bootable disk in drive", "PXE server not found", "Invalid boot sector", "Timeout waiting for selection"},
	0x1F: {"A: boot completed", "C: boot completed", "PXE boot completed", "Diagnostic boot completed", "CD-ROM boot completed", "ROM boot completed", "boot completed - device not specified"},
	0x20: {"Stop during OS load/init", "Run-time stop", "OS graceful stop", "OS graceful shutdown", "PEF initiated soft shutdown", "Agent not responding"},
	0x21: {"Fault Status asserted", "Identify Status asserted", "Slot/Connector Device installed/attached", "Slot/Connector ready for device installation",
		"Slot/Connector ready for device removal", "Slot Power is Off", "Slot/Connector device removal request", "Interlock asserted", "Slot is Disabled", "Slot holds spare device"},
	0x22: {"S0/G0: working", "S1: sleeping with system hw & processor context maintained", "S2: sleeping, processor context lost",
		"S3: sleeping, processor & hw context lost, memory retained", "S4: non-volatile sleep/suspend-to-disk", "S5/G2: soft-off", "S4/S5: soft-off",
		"G3: mechanical off", "Sleeping in S1/S2/S3 state", "G1: sleeping", "S5: entered by override", "Legacy ON state", "Legacy OFF state"},
	0x23: {"Timer expired, status only", "Hard reset", "Power down", "Power cycle", "reserved", "reserved", "reserved", "reserved", "Timer interrupt"},
	0x25: {"Present", "Absent", "Disabled"},
	0x28: {"Sensor access degraded or unavailable", "Controller access degraded or unavailable", "Management controller off-line",
		"Management controller unavailable", "Sensor failure", "FRU failure"},
	0x29: {"Low", "Failed", "Presence Detected"},
	0x2B: {"Hardware change detected", "Firmware or software change detected", "Hardware incompatibility detected",
		"Firmware or software incompatibility detected", "Invalid or unsupported hardware version", "Invalid or unsupported firmware or software version",
		"Hardware change success", "Firmware or software change success"},
	0x2C: {"Not Installed", "Inactive", "Activation Requested", "Activation in Progress", "Active", "Deactivation Requested", "Deactivation in Progress", "Communication lost"},
}

func eventText(sensorType, eventType, offset byte) string {
	var list []string
	switch {
	case eventType == 0x01:
		list = threshold
	case eventType == 0x6F:
		list = sensorSpecific[sensorType]
	default:
		list = generic[eventType]
	}
	if int(offset) < len(list) {
		return list[offset]
	}
	return fmt.Sprintf("Unknown event (type 0x%02x, offset %d)", eventType, offset)
}

// --- FRU inventory --------------------------------------------------------------------------

func (n *native) frus() ([]FRU, error) {
	devs := []struct {
		id   byte
		name string
	}{{0, "Builtin FRU Device"}}
	if recs, err := n.sdr(); err == nil {
		for _, r := range recs {
			if r.typ == 0x11 && r.fruLogical && r.fruID != 0 && r.owner == 0x20 {
				devs = append(devs, struct {
					id   byte
					name string
				}{r.fruID, r.name})
			}
		}
	}
	var out []FRU
	var first error
	for _, d := range devs {
		b, err := n.readFRU(d.id)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if f, ok := parseFRUData(b); ok {
			f.Name = d.name
			out = append(out, f)
		}
	}
	if len(out) == 0 && first != nil {
		return nil, first
	}
	return out, nil
}

func (n *native) readFRU(id byte) ([]byte, error) {
	info, err := n.t.send(netFnStorage, 0x10, []byte{id})
	if err != nil {
		return nil, err
	}
	if len(info) < 3 {
		return nil, fmt.Errorf("FRU %d: short area info", id)
	}
	size := int(binary.LittleEndian.Uint16(info))
	words := info[2]&1 != 0
	if size == 0 || size > 4096 {
		return nil, fmt.Errorf("FRU %d: size %d", id, size)
	}
	out := make([]byte, 0, size)
	for off := 0; off < size; {
		cnt := min(16, size-off)
		o, c := off, cnt
		if words {
			o, c = off/2, (cnt+1)/2
		}
		r, err := n.t.send(netFnStorage, 0x11, []byte{id, byte(o), byte(o >> 8), byte(c)})
		if err != nil {
			return nil, err
		}
		if len(r) < 2 || r[0] == 0 {
			break
		}
		got := r[1 : 1+min(int(r[0]), len(r)-1)]
		if words {
			got = got[:min(len(got), int(r[0])*2)]
		}
		out = append(out, got...)
		off += len(got)
	}
	return out, nil
}

// parseFRUData reads the chassis, board and product areas (IPMI FRU
// storage definition). Product fields win over board fields, as in ParseFRU.
func parseFRUData(b []byte) (FRU, bool) {
	var f FRU
	if len(b) < 8 || b[0] != 1 {
		return f, false
	}
	area := func(off byte) []byte {
		o := int(off) * 8
		if off == 0 || o+2 > len(b) {
			return nil
		}
		n := int(b[o+1]) * 8
		if o+n > len(b) || n == 0 {
			return b[o:]
		}
		return b[o : o+n]
	}
	fields := func(a []byte, from int) []string {
		var out []string
		for i := from; i < len(a); {
			tl := a[i]
			if tl == 0xC1 {
				break
			}
			n := int(tl & 0x3F)
			if i+1+n > len(a) {
				break
			}
			out = append(out, fruString(tl>>6, a[i+1:i+1+n]))
			i += 1 + n
		}
		return out
	}
	get := func(ss []string, i int) string {
		if i < len(ss) && !placeholder(ss[i]) {
			return ss[i]
		}
		return ""
	}
	if c := area(b[2]); len(c) > 3 {
		ss := fields(c, 3)
		f.ChassisPart, f.ChassisSerial = get(ss, 0), get(ss, 1)
	}
	var bMfg, bProd, bSerial, bPart string
	if a := area(b[3]); len(a) > 6 {
		mins := int(a[3]) | int(a[4])<<8 | int(a[5])<<16
		if mins > 0 {
			f.MfgDate = time.Date(1996, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(mins) * time.Minute).Format("2006-01-02")
		}
		ss := fields(a, 6)
		bMfg, bProd, bSerial, bPart = get(ss, 0), get(ss, 1), get(ss, 2), get(ss, 3)
	}
	var pMfg, pProd, pPart, pSerial string
	if a := area(b[4]); len(a) > 3 {
		ss := fields(a, 3)
		pMfg, pProd, pPart, pSerial = get(ss, 0), get(ss, 1), get(ss, 2), get(ss, 4)
	}
	first := func(a, b string) string {
		if a != "" {
			return a
		}
		return b
	}
	f.Manufacturer, f.Product = first(pMfg, bMfg), first(pProd, bProd)
	f.PartNumber, f.Serial = first(pPart, bPart), first(pSerial, bSerial)
	return f, true
}

// fruString decodes a type/length field: 8-bit ASCII, 6-bit packed ASCII,
// BCD plus, or binary (as hex).
func fruString(typ byte, d []byte) string {
	switch typ {
	case 3:
		return strings.TrimSpace(strings.TrimRight(string(d), "\x00"))
	case 2:
		var sb strings.Builder
		bits, nbits := 0, 0
		for _, c := range d {
			bits |= int(c) << nbits
			nbits += 8
			for nbits >= 6 {
				sb.WriteByte(byte(bits&0x3F) + 0x20)
				bits >>= 6
				nbits -= 6
			}
		}
		return strings.TrimSpace(sb.String())
	case 1:
		const bcd = "0123456789 -.:,_"
		var sb strings.Builder
		for _, c := range d {
			sb.WriteByte(bcd[c>>4])
			sb.WriteByte(bcd[c&0x0F])
		}
		return strings.TrimSpace(sb.String())
	}
	return fmt.Sprintf("%x", d)
}

// --- LAN --------------------------------------------------------------------------------

// lanText returns the LAN settings of the first LAN channel in `ipmitool
// lan print` form (what ParseIPMILan reads).
func (n *native) lanText() ([]byte, error) {
	param := func(ch, p byte) ([]byte, error) {
		r, err := n.t.send(netFnTransport, 0x02, []byte{ch, p, 0, 0})
		if err != nil {
			return nil, err
		}
		if len(r) < 1 {
			return nil, fmt.Errorf("LAN parameter %d: short answer", p)
		}
		return r[1:], nil // after the parameter revision
	}
	var last error
	for ch := byte(1); ch <= 11; ch++ {
		ip, err := param(ch, 3)
		if err != nil || len(ip) < 4 {
			last = err
			continue
		}
		var sb strings.Builder
		if src, err := param(ch, 4); err == nil && len(src) > 0 {
			name := map[byte]string{1: "Static Address", 2: "DHCP Address", 3: "BIOS Assigned Address", 4: "Other"}[src[0]&0x0F]
			fmt.Fprintf(&sb, "IP Address Source       : %s\n", name)
		}
		fmt.Fprintf(&sb, "IP Address              : %s\n", net.IP(ip[:4]))
		if m, err := param(ch, 6); err == nil && len(m) >= 4 {
			fmt.Fprintf(&sb, "Subnet Mask             : %s\n", net.IP(m[:4]))
		}
		if mac, err := param(ch, 5); err == nil && len(mac) >= 6 {
			fmt.Fprintf(&sb, "MAC Address             : %s\n", net.HardwareAddr(mac[:6]))
		}
		if gw, err := param(ch, 12); err == nil && len(gw) >= 4 {
			fmt.Fprintf(&sb, "Default Gateway IP      : %s\n", net.IP(gw[:4]))
		}
		if v, err := param(ch, 20); err == nil && len(v) >= 2 {
			vlan := binary.LittleEndian.Uint16(v)
			if vlan&0x8000 != 0 {
				fmt.Fprintf(&sb, "802.1q VLAN ID          : %d\n", vlan&0x0FFF)
			} else {
				sb.WriteString("802.1q VLAN ID          : Disabled\n")
			}
		}
		return []byte(sb.String()), nil
	}
	if last == nil {
		last = fmt.Errorf("no LAN channel answered")
	}
	return nil, last
}
