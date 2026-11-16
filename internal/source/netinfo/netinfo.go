// Package netinfo reads network interface state from sysfs/procfs and parses
// ethtool, ip, lldpctl and ipmitool output.
package netinfo

import (
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
)

// Iface is one entry of /sys/class/net.
type Iface struct {
	Name           string
	Kind           string // physical, wireless, bond, bridge, vlan, virtual, loopback
	MAC            string
	AdminUp        bool
	OperState      string
	Carrier        *bool
	SpeedMbps      *int
	Duplex         string
	MTU            int
	CarrierChanges *uint64
	Master         string
	Lowers         []string // lower_* links (vlan parent, bond slaves)
	BridgePorts    []string
	DevicePath     string // canonical device path for physical NICs
	Driver         string
	Stats          model.NICCounters
}

const iffUp = 0x1

// ReadIfaces reads all interfaces except loopback.
func ReadIfaces(f hostfs.FS) []Iface {
	dirs, _ := f.Glob("/sys/class/net/*")
	sort.Strings(dirs)
	var out []Iface
	for _, d := range dirs {
		n := path.Base(d)
		if n == "lo" || n == "bonding_masters" {
			continue
		}
		x := Iface{Name: n, MAC: hostfs.StringOr(f, d+"/address", ""), OperState: hostfs.StringOr(f, d+"/operstate", "")}
		if s := hostfs.StringOr(f, d+"/flags", ""); s != "" {
			v, _ := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 32)
			x.AdminUp = v&iffUp != 0
		}
		if c, err := hostfs.String(f, d+"/carrier"); err == nil {
			b := c == "1"
			x.Carrier = &b
		}
		if s, err := hostfs.Int(f, d+"/speed"); err == nil && s > 0 && s < 10_000_000 {
			v := int(s)
			x.SpeedMbps = &v
		}
		if dup := hostfs.StringOr(f, d+"/duplex", ""); dup != "unknown" {
			x.Duplex = dup
		}
		if m, err := hostfs.Int(f, d+"/mtu"); err == nil {
			x.MTU = int(m)
		}
		if v, err := hostfs.Uint(f, d+"/carrier_changes"); err == nil {
			x.CarrierChanges = &v
		}
		if m, err := f.Readlink(d + "/master"); err == nil {
			x.Master = path.Base(m)
		}
		for _, l := range globBase(f, d+"/lower_*") {
			x.Lowers = append(x.Lowers, strings.TrimPrefix(l, "lower_"))
		}
		x.BridgePorts = globBase(f, d+"/brif/*")
		if p, err := f.EvalSymlinks(d + "/device"); err == nil {
			x.DevicePath = p
			if drv, err := f.Readlink(d + "/device/driver"); err == nil {
				x.Driver = path.Base(drv)
			}
		}
		devtype := ""
		for _, l := range strings.Split(hostfs.StringOr(f, d+"/uevent", ""), "\n") {
			if v, ok := strings.CutPrefix(l, "DEVTYPE="); ok {
				devtype = v
			}
		}
		switch {
		case devtype == "bond" || hostfs.Exists(f, d+"/bonding"):
			x.Kind = "bond"
		case devtype == "bridge" || hostfs.Exists(f, d+"/bridge"):
			x.Kind = "bridge"
		case devtype == "vlan" || hostfs.Exists(f, "/proc/net/vlan/"+n):
			x.Kind = "vlan"
		case devtype == "wlan" || hostfs.Exists(f, d+"/wireless"):
			x.Kind = "wireless"
		case x.DevicePath != "":
			x.Kind = "physical"
		default:
			x.Kind = "virtual"
		}
		st := d + "/statistics/"
		r := func(k string) uint64 { v, _ := hostfs.Uint(f, st+k); return v }
		x.Stats = model.NICCounters{
			RxBytes: r("rx_bytes"), TxBytes: r("tx_bytes"), RxPackets: r("rx_packets"), TxPackets: r("tx_packets"),
			RxErrors: r("rx_errors"), TxErrors: r("tx_errors"), RxDropped: r("rx_dropped"), TxDropped: r("tx_dropped"),
			RxCRC: r("rx_crc_errors"), RxFrame: r("rx_frame_errors"), RxMissed: r("rx_missed_errors"), Collisions: r("collisions"),
		}
		out = append(out, x)
	}
	return out
}

func globBase(f hostfs.FS, pattern string) []string {
	m, _ := f.Glob(pattern)
	out := make([]string, 0, len(m))
	for _, p := range m {
		out = append(out, path.Base(p))
	}
	sort.Strings(out)
	return out
}

// VLANIDs parses /proc/net/vlan/config ("bond0.100 | 100 | bond0").
func VLANIDs(f hostfs.FS) map[string]int {
	out := map[string]int{}
	b, err := f.ReadFile("/proc/net/vlan/config")
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		p := strings.Split(l, "|")
		if len(p) < 3 {
			continue
		}
		if id, err := strconv.Atoi(strings.TrimSpace(p[1])); err == nil {
			out[strings.TrimSpace(p[0])] = id
		}
	}
	return out
}

// --- bonding ------------------------------------------------------------------

// ParseBonding parses /proc/net/bonding/<bond>.
func ParseBonding(data []byte) *model.Bond {
	b := &model.Bond{Slaves: []model.BondSlave{}}
	var cur *model.BondSlave
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "Bonding Mode":
			b.Mode = v
		case "Currently Active Slave":
			b.ActiveSlave = v
		case "Slave Interface":
			b.Slaves = append(b.Slaves, model.BondSlave{Name: v})
			cur = &b.Slaves[len(b.Slaves)-1]
		case "MII Status":
			if cur == nil {
				b.MIIStatus = v
			} else {
				cur.MIIStatus = v
			}
		case "Speed":
			if cur != nil {
				cur.Speed = v
			}
		case "Duplex":
			if cur != nil {
				cur.Duplex = v
			}
		case "Link Failure Count":
			if cur != nil {
				cur.LinkFailures, _ = strconv.ParseUint(v, 10, 64)
			}
		case "Aggregator ID":
			if cur != nil {
				cur.AggregatorID = v
			}
		}
		// "details partner lacp pdu" etc. sections are ignored.
	}
	return b
}

// --- ethtool --------------------------------------------------------------------

// DrvInfo is ethtool -i data.
type DrvInfo struct{ Driver, Version, Firmware, BusInfo string }

// IoctlDrvInfo reads driver info with the ETHTOOL_GDRVINFO ioctl (no tool needed).
func IoctlDrvInfo(ifname string) (DrvInfo, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return DrvInfo{}, err
	}
	defer unix.Close(fd)
	d, err := unix.IoctlGetEthtoolDrvinfo(fd, ifname)
	if err != nil {
		return DrvInfo{}, err
	}
	s := func(b []byte) string { return strings.TrimRight(string(b), "\x00") }
	return DrvInfo{Driver: s(d.Driver[:]), Version: s(d.Version[:]), Firmware: s(d.Fw_version[:]), BusInfo: s(d.Bus_info[:])}, nil
}

// ParseDrvInfo parses `ethtool -i <if>`.
func ParseDrvInfo(data []byte) DrvInfo {
	kv := keyValues(data)
	return DrvInfo{Driver: kv["driver"], Version: kv["version"], Firmware: kv["firmware-version"], BusInfo: kv["bus-info"]}
}

func keyValues(data []byte) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(l, ":")
		if ok {
			out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return out
}

// LinkInfo is parsed `ethtool <if>` output.
type LinkInfo struct {
	MaxSpeedMbps *int
	Port         string
	Autoneg      string
	Duplex       string
}

var linkMode = regexp.MustCompile(`(\d+)base`)

// ParseLinkInfo parses `ethtool <if>`. The maximum speed is taken from the
// advertised modes when available, otherwise from the supported modes.
func ParseLinkInfo(data []byte) LinkInfo {
	var li LinkInfo
	section := ""
	maxOf := map[string]int{}
	for _, l := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(l)
		if k, v, ok := strings.Cut(t, ":"); ok && !strings.HasPrefix(t, "0") {
			k = strings.TrimSpace(k)
			switch k {
			case "Supported link modes", "Advertised link modes":
				section = k
				t = v
			case "Port":
				li.Port = strings.TrimSpace(v)
				section = ""
			case "Auto-negotiation":
				li.Autoneg = strings.TrimSpace(v)
				section = ""
			case "Duplex":
				li.Duplex = strings.TrimSpace(v)
				section = ""
			default:
				section = ""
			}
		}
		if section != "" {
			for _, m := range linkMode.FindAllStringSubmatch(t, -1) {
				if s, err := strconv.Atoi(m[1]); err == nil && s > maxOf[section] {
					maxOf[section] = s
				}
			}
		}
	}
	if v := maxOf["Advertised link modes"]; v > 0 {
		li.MaxSpeedMbps = &v
	} else if v := maxOf["Supported link modes"]; v > 0 {
		li.MaxSpeedMbps = &v
	}
	return li
}

// ParseStats parses `ethtool -S <if>` and returns non-zero counters whose
// names indicate errors, drops or discards.
func ParseStats(data []byte) map[string]uint64 {
	out := map[string]uint64{}
	re := regexp.MustCompile(`err|drop|crc|miss|discard|fifo|timeout|bad|illegal|symbol|fcs|lost`)
	for _, l := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil || n == 0 || !re.MatchString(strings.ToLower(k)) || strings.Contains(k, "pkts_ok") {
			continue
		}
		out[k] = n
		if len(out) >= 50 {
			break
		}
	}
	return out
}

// --- transceivers (ethtool -m) ------------------------------------------------------

var (
	dbmRe  = regexp.MustCompile(`(-?[\d.]+|-inf)\s*dBm`)
	numRe  = regexp.MustCompile(`^(-?[\d.]+)`)
	laneRe = regexp.MustCompile(`\(?[Cc]hannel\s*(\d+)\)?`)
)

func dBm(v string) *float64 {
	m := dbmRe.FindStringSubmatch(v)
	if m == nil {
		return nil
	}
	if m[1] == "-inf" {
		x := -40.0
		return &x
	}
	x, err := strconv.ParseFloat(m[1], 64)
	if err != nil || math.IsInf(x, 0) {
		return nil
	}
	return &x
}

func num(v string) *float64 {
	m := numRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return nil
	}
	x, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil
	}
	return &x
}

// ParseModule parses `ethtool -m <if>` for SFP (SFF-8472) and QSFP
// (SFF-8636) modules. Thresholds come from the module itself.
func ParseModule(data []byte) *model.Transceiver {
	kv := map[string]string{}
	var order []string
	for _, l := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(l, " : ")
		if !ok {
			k, v, ok = strings.Cut(l, ":")
		}
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if _, dup := kv[k]; !dup {
			order = append(order, k)
		}
		kv[k] = strings.TrimSpace(v)
	}
	if kv["identifier"] == "" {
		return nil
	}
	t := &model.Transceiver{Sensors: []model.Sensor{}}
	if m := regexp.MustCompile(`\(([^)]+)\)`).FindStringSubmatch(kv["identifier"]); m != nil {
		t.Identifier = m[1]
	}
	t.Type = kv["transceiver type"]
	t.Vendor, t.PartNumber, t.Serial, t.Wavelength = kv["vendor name"], kv["vendor pn"], kv["vendor sn"], kv["laser wavelength"]
	t.DateCode = moduleDate(kv["date code"])

	thr := func(prefix string, conv func(string) *float64) *model.Thresholds {
		th := &model.Thresholds{Origin: "module",
			UpperCrit: conv(kv[prefix+" high alarm threshold"]), LowerCrit: conv(kv[prefix+" low alarm threshold"]),
			UpperWarn: conv(kv[prefix+" high warning threshold"]), LowerWarn: conv(kv[prefix+" low warning threshold"])}
		if th.UpperCrit == nil && th.LowerCrit == nil && th.UpperWarn == nil && th.LowerWarn == nil {
			return nil
		}
		return th
	}
	add := func(name string, kind model.SensorKind, unit string, v *float64, th *model.Thresholds) {
		if v == nil {
			return
		}
		s := model.Sensor{Name: name, Kind: kind, Value: v, Unit: unit, Thresholds: th, Source: "ethtool"}
		t.Sensors = append(t.Sensors, s)
	}
	add("Module temperature", model.KindTemperature, model.UnitCelsius, num(kv["module temperature"]), thr("module temperature", num))
	add("Module voltage", model.KindVoltage, model.UnitVolts, num(kv["module voltage"]), thr("module voltage", num))
	txTh, rxTh := thr("laser output power", dBm), thr("laser rx power", dBm)
	if v := dBm(kv["laser output power"]); v != nil {
		add("Tx power", model.KindOptical, model.UnitDBm, v, txTh)
	}
	if v := dBm(kv["receiver signal average optical power"]); v != nil {
		add("Rx power", model.KindOptical, model.UnitDBm, v, rxTh)
	}
	for _, k := range order {
		lane := laneRe.FindStringSubmatch(k)
		if lane == nil {
			continue
		}
		switch {
		case strings.HasPrefix(k, "transmit avg optical power"):
			add("Tx power lane "+lane[1], model.KindOptical, model.UnitDBm, dBm(kv[k]), txTh)
		case strings.HasPrefix(k, "rcvr signal avg optical power"), strings.HasPrefix(k, "receiver signal average optical power"):
			add("Rx power lane "+lane[1], model.KindOptical, model.UnitDBm, dBm(kv[k]), rxTh)
		}
	}
	return t
}

// --- ip -j addr -----------------------------------------------------------------

// ParseIPAddr parses `ip -j addr show` into interface -> "addr/prefix" (global scope).
func ParseIPAddr(data []byte) (map[string][]string, error) {
	var ifs []struct {
		Name  string `json:"ifname"`
		Addrs []struct {
			Family string `json:"family"`
			Local  string `json:"local"`
			Prefix int    `json:"prefixlen"`
			Scope  string `json:"scope"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(data, &ifs); err != nil {
		return nil, fmt.Errorf("ip -j addr: %w", err)
	}
	out := map[string][]string{}
	for _, i := range ifs {
		for _, a := range i.Addrs {
			if a.Scope == "global" || a.Scope == "site" {
				out[i.Name] = append(out[i.Name], fmt.Sprintf("%s/%d", a.Local, a.Prefix))
			}
		}
	}
	return out, nil
}

// --- lldpctl -f json ---------------------------------------------------------------

// ParseLLDP parses `lldpctl -f json`. lldpd emits "interface" as an object
// for one neighbor and as a list for several.
func ParseLLDP(data []byte) (map[string]model.LLDPNeighbor, error) {
	var root struct {
		LLDP struct {
			Interface json.RawMessage `json:"interface"`
		} `json:"lldp"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("lldpctl json: %w", err)
	}
	var entries []map[string]json.RawMessage
	var one map[string]json.RawMessage
	raw := root.LLDP.Interface
	if len(raw) == 0 {
		return map[string]model.LLDPNeighbor{}, nil
	}
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, err
		}
	} else if err := json.Unmarshal(raw, &one); err == nil {
		for k, v := range one {
			entries = append(entries, map[string]json.RawMessage{k: v})
		}
	}
	out := map[string]model.LLDPNeighbor{}
	for _, e := range entries {
		for ifname, body := range e {
			var x struct {
				Chassis map[string]json.RawMessage `json:"chassis"`
				Port    struct {
					ID struct {
						Value string `json:"value"`
					} `json:"id"`
					Descr string `json:"descr"`
				} `json:"port"`
				VLAN json.RawMessage `json:"vlan"`
			}
			if json.Unmarshal(body, &x) != nil {
				continue
			}
			n := model.LLDPNeighbor{Port: x.Port.ID.Value, PortDescr: x.Port.Descr}
			for name, c := range x.Chassis {
				if name == "id" || name == "descr" {
					continue
				}
				n.System = name
				if name == "name" {
					var v struct {
						Value string `json:"value"`
					}
					if json.Unmarshal(c, &v) == nil {
						n.System = v.Value
					}
				}
			}
			var vl struct {
				ID string `json:"vlan-id"`
			}
			if json.Unmarshal(x.VLAN, &vl) == nil {
				n.VLAN = vl.ID
			}
			out[ifname] = n
		}
	}
	return out, nil
}

// --- ipmitool lan print ---------------------------------------------------------------

// ParseIPMILan parses `ipmitool lan print`.
func ParseIPMILan(data []byte) *model.BMCLan {
	kv := map[string]string{}
	for _, l := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(l, ":")
		if ok && strings.TrimSpace(k) != "" {
			key := strings.TrimSpace(k)
			if _, dup := kv[key]; !dup {
				kv[key] = strings.TrimSpace(v)
			}
		}
	}
	if kv["IP Address"] == "" && kv["MAC Address"] == "" {
		return nil
	}
	return &model.BMCLan{IPSource: kv["IP Address Source"], IP: kv["IP Address"], Netmask: kv["Subnet Mask"],
		Gateway: kv["Default Gateway IP"], MAC: kv["MAC Address"], VLAN: kv["802.1q VLAN ID"]}
}

// moduleDate turns an SFF date code ("210314", optionally followed by a lot
// code) into "2021-03-14".
func moduleDate(v string) string {
	v = strings.TrimSpace(v)
	if len(v) < 6 {
		return ""
	}
	for _, c := range v[:6] {
		if c < '0' || c > '9' {
			return ""
		}
	}
	y, m, d := v[0:2], v[2:4], v[4:6]
	if m < "01" || m > "12" || d < "01" || d > "31" {
		return ""
	}
	return "20" + y + "-" + m + "-" + d
}
