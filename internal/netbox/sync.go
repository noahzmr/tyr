package netbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/noahzmr/tyr/internal/accept"
	"github.com/noahzmr/tyr/internal/model"
)

// Input is what gets written.
type Input struct {
	Snap      *model.Snapshot
	Result    *accept.Result // the acceptance test, nil without one
	ResultDir string         // where its results are (named in the journal)
}

// Report says what a sync did.
type Report struct {
	Version   string   `json:"netbox_version,omitempty"`
	DeviceID  int      `json:"device_id,omitempty"`
	DeviceURL string   `json:"device_url,omitempty"`
	Device    string   `json:"device,omitempty"`
	Created   bool     `json:"created,omitempty"`
	Tags      []string `json:"tags,omitempty"` // the device's tags afterwards
	Journal   bool     `json:"journal,omitempty"`
	DryRun    bool     `json:"dry_run,omitempty"`
	Problems  []string `json:"problems,omitempty"` // parts that failed; the rest was written
	Changes   []Change `json:"changes,omitempty"`  // what was (dry run: would be) written, and what was already right
}

// Summary counts the changes: "create 31, update 2, 12 unchanged".
func (r *Report) Summary() string {
	n := map[string]int{}
	for _, c := range r.Changes {
		n[c.Action]++
	}
	var parts []string
	for _, a := range []string{"create", "update", "set", "remove", "delete"} {
		if n[a] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", a, n[a]))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "change nothing")
	}
	if n["unchanged"] > 0 {
		parts = append(parts, fmt.Sprintf("%d unchanged", n["unchanged"]))
	}
	return strings.Join(parts, ", ")
}

// Change is one object written or checked.
type Change struct {
	Object string `json:"object"` // device, interface, inventory item, IP address, tag, custom field, journal
	Name   string `json:"name"`
	Action string `json:"action"` // create, update, unchanged, set, remove, delete
	Detail string `json:"detail,omitempty"`
}

type syncer struct {
	c       *Client
	cfg     Config
	tags    Tags
	in      Input
	facts   Facts
	log     func(string, ...any)
	rep     *Report
	dev     obj
	devID   int
	manuf   map[string]int
	roles   map[string]int
	ctx     context.Context
	devCrit bool
}

// Sync writes the input to NetBox. The error is set when nothing could be
// written (no connection, no device); problems with single parts are in
// Report.Problems.
func Sync(ctx context.Context, cfg Config, tags Tags, in Input, dryRun bool, log func(string, ...any)) (*Report, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	if in.Snap == nil {
		return nil, errors.New("no snapshot")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	c := NewClient(cfg, log)
	c.DryRun = dryRun
	s := &syncer{c: c, cfg: cfg, tags: tags, in: in, log: log, ctx: ctx,
		rep: &Report{DryRun: dryRun}, manuf: map[string]int{}, roles: map[string]int{}}
	s.facts = ComputeFacts(in.Snap, in.Result)
	ver, err := c.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("NetBox %s: %w", cfg.URL, err)
	}
	s.rep.Version = ver
	log("NetBox %s (version %s)%s", cfg.URL, ver, map[bool]string{true: " - dry run, nothing is written"}[dryRun])
	if err := s.device(); err != nil {
		return s.rep, err
	}
	if cfg.Interfaces {
		s.step("interfaces", s.interfaces)
	}
	if cfg.Inventory {
		s.step("inventory items", s.inventory)
	}
	s.step("device status, tags and custom fields", s.deviceFields)
	if cfg.Journal {
		s.step("journal", s.journal)
	}
	where := firstNonEmpty(s.rep.DeviceURL, "new device "+s.rep.Device)
	sum := s.rep.Summary()
	if dryRun {
		sum = "would " + sum
	}
	if n := len(s.rep.Problems); n > 0 {
		log("done with %d problem(s), %s: %s", n, sum, where)
	} else {
		log("done, %s: %s", sum, where)
	}
	return s.rep, nil
}

func (s *syncer) step(name string, f func() error) {
	if err := f(); err != nil {
		s.problem("%s: %v", name, err)
	}
}

// change records what is written; created and changed objects go to the
// log with their values.
func (s *syncer) change(object, name, action, detail string) {
	s.rep.Changes = append(s.rep.Changes, Change{Object: object, Name: name, Action: action, Detail: detail})
	if action == "unchanged" {
		return
	}
	verb := action
	if s.c.DryRun {
		verb = "would " + action
	}
	line := fmt.Sprintf("  %s %s %s", verb, object, name)
	if detail != "" {
		line += ": " + detail
	}
	s.log("%s", line)
}

// newDevice: a dry run against a device that does not exist yet - it has
// no interfaces or items to look up.
func (s *syncer) newDevice() bool { return s.devID < 0 }

func (s *syncer) problem(f string, a ...any) {
	msg := fmt.Sprintf(f, a...)
	s.rep.Problems = append(s.rep.Problems, msg)
	s.log("  ✖ %s", msg)
}

// --- device ------------------------------------------------------------------

// genericHost reports host names many machines share: the live ISO's and
// installer defaults. They never identify a device.
func genericHost(h string) bool {
	switch strings.ToLower(strings.TrimSpace(h)) {
	case "", "tyr-live", "localhost", "localhost.localdomain", "debian", "ubuntu", "live", "archiso", "(none)":
		return true
	}
	return false
}

// junkSerial matches serial numbers that firmware fills in by default.
var junkSerial = regexp.MustCompile(`(?i)^(|0+|1234567890?|0123456789|none|n/?a|not (specified|available|applicable)|to be filled.*|system serial number|default string|serial number|chassis serial number|\.+)$`)

func (s *syncer) device() error {
	y := s.in.Snap.System
	if y == nil {
		y = &model.System{}
	}
	serial := strings.TrimSpace(y.Serial)
	if junkSerial.MatchString(serial) {
		serial = ""
	}
	name := s.in.Snap.Hostname
	if genericHost(name) {
		name = "" // the live ISO is "tyr-live" on every machine
	}
	if (s.cfg.Name == "serial" || name == "") && serial != "" {
		name = serial
	}
	if serial == "" && name == "" {
		return fmt.Errorf("no usable serial number (%q) and no host name of its own (%q): cannot tell this machine apart in NetBox", y.Serial, s.in.Snap.Hostname)
	}
	var dev obj
	var err error
	if serial != "" {
		page, e := s.c.Get(s.ctx, "/api/dcim/devices/", q("serial", serial, "limit", "2"))
		if e != nil {
			return fmt.Errorf("find device by serial %s: %w", serial, e)
		}
		res := list(page["results"])
		if len(res) > 1 {
			return fmt.Errorf("%d devices in NetBox have serial number %s: fix that first", int(page["count"].(float64)), serial)
		}
		if len(res) == 1 {
			dev = res[0]
		}
	} else {
		s.log("  no usable serial number (%q): looking the device up by name %q", y.Serial, name)
		if dev, err = s.c.First(s.ctx, "/api/dcim/devices/", q("name", name)); err != nil {
			return fmt.Errorf("find device %s: %w", name, err)
		}
	}
	if dev != nil {
		s.dev, s.devID = dev, id(dev["id"])
		s.log("device %s (id %d)", str(dev["name"]), s.devID)
		s.change("device", str(dev["name"]), "unchanged", "found by "+map[bool]string{true: "serial " + serial, false: "name"}[serial != ""])
	} else {
		if s.cfg.Site == "" || s.cfg.Role == "" {
			return fmt.Errorf("serial %s is not in NetBox, and site and role for a new device are not set", serial)
		}
		if err := s.createDevice(name, serial, y); err != nil {
			return err
		}
	}
	s.rep.DeviceID = s.devID
	s.rep.Device = str(s.dev["name"])
	if !s.newDevice() {
		s.rep.DeviceURL = fmt.Sprintf("%s/dcim/devices/%d/", s.cfg.URL, s.devID)
	}
	return nil
}

func (s *syncer) createDevice(name, serial string, y *model.System) error {
	vendor := firstNonEmpty(Manufacturer(y.Vendor, ""), "Unknown")
	product := firstNonEmpty(strings.TrimSpace(y.Product), "Unknown")
	mid, err := s.manufacturer(vendor)
	if err != nil {
		return err
	}
	tid, err := s.getOrCreate("/api/dcim/device-types/", q("model", product, "manufacturer_id", fmt.Sprint(mid)),
		obj{"manufacturer": mid, "model": product, "slug": slugify(vendor + "-" + product), "u_height": 1})
	if err != nil {
		return fmt.Errorf("device type %s: %w", product, err)
	}
	rid, err := s.getOrCreate("/api/dcim/device-roles/", q("name", s.cfg.Role),
		obj{"name": s.cfg.Role, "slug": slugify(s.cfg.Role), "color": "607d8b"})
	if err != nil {
		return fmt.Errorf("role %s: %w", s.cfg.Role, err)
	}
	sid, err := s.getOrCreate("/api/dcim/sites/", q("name", s.cfg.Site),
		obj{"name": s.cfg.Site, "slug": slugify(s.cfg.Site), "status": "active"})
	if err != nil {
		return fmt.Errorf("site %s: %w", s.cfg.Site, err)
	}
	body := obj{"name": name, "device_type": tid, "site": sid, "serial": serial,
		"status": firstNonEmpty(s.cfg.Status, "active")}
	if s.c.AtLeast(3, 6) {
		body["role"] = rid
	} else {
		body["device_role"] = rid
	}
	if s.cfg.Tenant != "" {
		t, err := s.c.First(s.ctx, "/api/tenancy/tenants/", q("name", s.cfg.Tenant))
		if err != nil || t == nil {
			return fmt.Errorf("tenant %q not found in NetBox", s.cfg.Tenant)
		}
		body["tenant"] = id(t["id"])
	}
	dev, err := s.c.Post(s.ctx, "/api/dcim/devices/", body)
	var ae *APIError
	if errors.As(err, &ae) && ae.Code == 400 && strings.Contains(ae.Body, "name") && serial != "" && name != serial {
		s.log("  name %s is taken: using the serial number", name)
		body["name"] = serial
		dev, err = s.c.Post(s.ctx, "/api/dcim/devices/", body)
	}
	if err != nil {
		return fmt.Errorf("create device: %w", err)
	}
	if str(dev["name"]) == "" {
		dev["name"] = body["name"]
	}
	s.dev, s.devID, s.rep.Created = dev, id(dev["id"]), true
	detail := fmt.Sprintf("%s %s, serial %s, site %s, role %s, status %s", vendor, product, firstNonEmpty(serial, "-"), s.cfg.Site, s.cfg.Role, body["status"])
	if s.cfg.Tenant != "" {
		detail += ", tenant " + s.cfg.Tenant
	}
	s.change("device", fmt.Sprint(body["name"]), "create", detail)
	return nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// getOrCreate finds an object by query, else creates it; returns its id.
func (s *syncer) getOrCreate(path string, find map[string][]string, create obj) (int, error) {
	o, err := s.c.First(s.ctx, path, find)
	if err != nil {
		return 0, err
	}
	if o != nil {
		return id(o["id"]), nil
	}
	o, err = s.c.Post(s.ctx, path, create)
	if err != nil {
		var ae *APIError // a slug clash: the object exists under another name
		if slug, ok := create["slug"].(string); ok && errors.As(err, &ae) && ae.Code == 400 {
			if o2, e2 := s.c.First(s.ctx, path, q("slug", slug)); e2 == nil && o2 != nil {
				return id(o2["id"]), nil
			}
		}
		return 0, err
	}
	kind := strings.TrimSuffix(strings.TrimPrefix(path, "/api/"), "/")
	kind = strings.TrimSuffix(kind[strings.LastIndex(kind, "/")+1:], "s")
	s.change(strings.ReplaceAll(kind, "-", " "), firstNonEmpty(str(create["name"]), str(create["model"])), "create", "")
	return id(o["id"]), nil
}

// manufacturer finds a manufacturer by slug or by name (any case) before
// it creates one, so "Supermicro" and "SUPERMICRO" stay one entry.
func (s *syncer) manufacturer(name string) (int, error) {
	if v, ok := s.manuf[strings.ToLower(name)]; ok {
		return v, nil
	}
	o, err := s.c.First(s.ctx, "/api/dcim/manufacturers/", q("slug", slugify(name)))
	if err == nil && o == nil {
		o, err = s.c.First(s.ctx, "/api/dcim/manufacturers/", q("name__ie", name))
	}
	if err != nil {
		return 0, err
	}
	v := 0
	if o != nil {
		v = id(o["id"])
	} else if v, err = s.getOrCreate("/api/dcim/manufacturers/", q("slug", slugify(name)), obj{"name": name, "slug": slugify(name)}); err != nil {
		return 0, err
	}
	s.manuf[strings.ToLower(name)] = v
	return v, nil
}

// --- interfaces --------------------------------------------------------------

// ifType maps a port to a NetBox interface type.
func ifType(n model.NIC) string {
	sp := nicSpeed(n)
	fibre := strings.Contains(strings.ToLower(n.PortType), "fibre") || strings.Contains(strings.ToLower(n.PortType), "direct attach") || n.Module != nil
	switch {
	case sp >= 400000:
		return "400gbase-x-qsfpdd"
	case sp >= 200000:
		return "200gbase-x-qsfp56"
	case sp >= 100000:
		return "100gbase-x-qsfp28"
	case sp >= 50000:
		return "50gbase-x-sfp56"
	case sp >= 40000:
		return "40gbase-x-qsfpp"
	case sp >= 25000:
		return "25gbase-x-sfp28"
	case sp >= 10000 && fibre:
		return "10gbase-x-sfpp"
	case sp >= 10000:
		return "10gbase-t"
	case sp >= 5000:
		return "5gbase-t"
	case sp >= 2500:
		return "2.5gbase-t"
	case sp >= 1000 && fibre:
		return "1000base-x-sfp"
	case sp >= 1000:
		return "1000base-t"
	case sp >= 100:
		return "100base-tx"
	}
	return "other"
}

func (s *syncer) interfaces() error {
	nw := s.in.Snap.Network
	if nw == nil {
		return nil
	}
	var existing []obj
	if !s.newDevice() {
		var err error
		if existing, err = s.c.List(s.ctx, "/api/dcim/interfaces/", q("device_id", fmt.Sprint(s.devID))); err != nil {
			return err
		}
	}
	byName := map[string]obj{}
	for _, e := range existing {
		byName[str(e["name"])] = e
	}
	n := 0
	for _, x := range nw.Interfaces {
		if x.Kind != "physical" {
			continue
		}
		desc := strings.TrimSpace(x.Vendor + " " + x.Model)
		body := obj{"description": trunc(desc, 200)}
		if sp := nicSpeed(x); sp > 0 && x.SpeedMbps != nil && *x.SpeedMbps > 0 {
			body["speed"] = *x.SpeedMbps * 1000 // kbit/s
		}
		info := []string{ifType(x)}
		if x.MAC != "" {
			info = append(info, "MAC "+strings.ToUpper(x.MAC))
		}
		if x.SpeedMbps != nil && *x.SpeedMbps > 0 {
			info = append(info, fmt.Sprintf("%d Mbit/s", *x.SpeedMbps))
		}
		if desc != "" {
			info = append(info, desc)
		}
		iid, err := s.upsertInterface(byName[x.Name], x.Name, ifType(x), false, body, strings.Join(info, ", "))
		if err != nil {
			s.problem("interface %s: %v", x.Name, err)
			continue
		}
		n++
		if x.MAC != "" {
			s.mac(iid, byName[x.Name], x.MAC)
		}
	}
	s.log("interfaces: %d ports", n)
	if b := nw.BMC; b != nil && (b.MAC != "" || validIP(b.IP)) {
		name := "bmc"
		for _, cand := range []string{"iLO", "iDRAC", "BMC", "bmc", "IPMI", "XCC"} {
			if byName[cand] != nil {
				name = cand
				break
			}
		}
		info := "management only"
		if b.MAC != "" {
			info += ", MAC " + strings.ToUpper(b.MAC)
		}
		iid, err := s.upsertInterface(byName[name], name, "1000base-t", true, obj{"description": "management controller"}, info)
		if err != nil {
			return fmt.Errorf("BMC interface: %w", err)
		}
		if b.MAC != "" {
			s.mac(iid, byName[name], b.MAC)
		}
		if validIP(b.IP) {
			s.oobIP(iid, b)
		}
	}
	return nil
}

func validIP(ip string) bool {
	p := net.ParseIP(ip)
	return p != nil && !p.IsUnspecified()
}

func (s *syncer) upsertInterface(cur obj, name, typ string, mgmt bool, body obj, info string) (int, error) {
	if cur == nil {
		body["device"], body["name"], body["type"], body["mgmt_only"], body["enabled"] = s.devID, name, typ, mgmt, true
		o, err := s.c.Post(s.ctx, "/api/dcim/interfaces/", body)
		if err != nil {
			return 0, err
		}
		s.change("interface", name, "create", info)
		return id(o["id"]), nil
	}
	iid := id(cur["id"])
	if ch := changes(cur, body); len(ch) > 0 {
		if _, err := s.c.Patch(s.ctx, "/api/dcim/interfaces/", iid, ch); err != nil {
			return iid, err
		}
		s.change("interface", name, "update", describeChanges(ch))
		return iid, nil
	}
	s.change("interface", name, "unchanged", info)
	return iid, nil
}

// describeChanges lists changed fields with their new values.
func describeChanges(ch obj) string {
	keys := make([]string, 0, len(ch))
	for k := range ch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		switch v := ch[k].(type) {
		case string:
			parts = append(parts, fmt.Sprintf("%s %q", k, v))
		case int, bool:
			parts = append(parts, fmt.Sprintf("%s %v", k, v))
		default:
			parts = append(parts, k)
		}
	}
	return strings.Join(parts, ", ")
}

// mac records a MAC address on an interface: NetBox 4.2 and later keep
// them as objects (the interface's primary MAC), older ones as a field.
func (s *syncer) mac(iid int, cur obj, mac string) {
	mac = strings.ToUpper(mac)
	if !s.c.AtLeast(4, 2) {
		if cur == nil || !strings.EqualFold(str(cur["mac_address"]), mac) {
			if _, err := s.c.Patch(s.ctx, "/api/dcim/interfaces/", iid, obj{"mac_address": mac}); err != nil {
				s.problem("MAC %s: %v", mac, err)
			}
		}
		return
	}
	if cur != nil && strings.EqualFold(str(nested(cur, "primary_mac_address", "mac_address")), mac) {
		return
	}
	found, err := s.c.List(s.ctx, "/api/dcim/mac-addresses/", q("mac_address", mac))
	if err != nil {
		s.problem("MAC %s: %v", mac, err)
		return
	}
	mid := 0
	for _, m := range found {
		if str(m["assigned_object_type"]) == "dcim.interface" && id(m["assigned_object_id"]) == iid {
			mid = id(m["id"])
		}
	}
	if mid == 0 {
		for _, m := range found {
			if m["assigned_object_id"] != nil && id(m["assigned_object_id"]) != iid {
				s.log("  MAC %s is already on another interface in NetBox: left there", mac)
				return
			}
		}
		o, err := s.c.Post(s.ctx, "/api/dcim/mac-addresses/", obj{"mac_address": mac,
			"assigned_object_type": "dcim.interface", "assigned_object_id": iid})
		if err != nil {
			s.problem("MAC %s: %v", mac, err)
			return
		}
		mid = id(o["id"])
	}
	if _, err := s.c.Patch(s.ctx, "/api/dcim/interfaces/", iid, obj{"primary_mac_address": mid}); err != nil {
		s.problem("MAC %s as primary: %v", mac, err)
	}
}

// oobIP records the BMC's address and makes it the device's out-of-band IP.
func (s *syncer) oobIP(iid int, b *model.BMCLan) {
	plen := 32
	if m := net.ParseIP(b.Netmask).To4(); m != nil {
		plen, _ = net.IPv4Mask(m[0], m[1], m[2], m[3]).Size()
	}
	if strings.Contains(b.IP, ":") {
		plen = 64
	}
	addr := fmt.Sprintf("%s/%d", b.IP, plen)
	found, err := s.c.List(s.ctx, "/api/ipam/ip-addresses/", q("address", b.IP))
	if err != nil {
		s.problem("BMC IP %s: %v", addr, err)
		return
	}
	ipID := 0
	for _, a := range found {
		switch {
		case str(a["assigned_object_type"]) == "dcim.interface" && id(a["assigned_object_id"]) == iid:
			ipID = id(a["id"])
		case a["assigned_object_id"] == nil && ipID == 0:
			if _, err := s.c.Patch(s.ctx, "/api/ipam/ip-addresses/", id(a["id"]), obj{"assigned_object_type": "dcim.interface", "assigned_object_id": iid}); err != nil {
				s.problem("BMC IP %s: %v", addr, err)
				return
			}
			ipID = id(a["id"])
		}
	}
	if ipID == 0 {
		if len(found) > 0 {
			s.log("  BMC IP %s is assigned to another object in NetBox: left there", addr)
			return
		}
		o, err := s.c.Post(s.ctx, "/api/ipam/ip-addresses/", obj{"address": addr, "status": "active",
			"assigned_object_type": "dcim.interface", "assigned_object_id": iid, "description": "BMC"})
		if err != nil {
			s.problem("BMC IP %s: %v", addr, err)
			return
		}
		ipID = id(o["id"])
		s.change("IP address", addr, "create", "on the BMC interface, out-of-band IP of the device")
	}
	if s.c.AtLeast(4, 0) && id(s.dev["oob_ip"]) != ipID {
		if _, err := s.c.Patch(s.ctx, "/api/dcim/devices/", s.devID, obj{"oob_ip": ipID}); err != nil {
			s.problem("out-of-band IP: %v", err)
		}
	}
}

// --- inventory items ---------------------------------------------------------

// roleNames are the inventory item roles, per inventory category.
var roleNames = map[string]string{
	"board": "Mainboard", "bmc": "BMC", "cpu": "CPU", "dimm": "Memory", "disk": "Disk",
	"raid_controller": "RAID controller", "raid_drive": "RAID drive", "nic": "NIC",
	"transceiver": "Transceiver", "gpu": "GPU", "npu": "NPU", "psu": "Power supply",
	"fru": "FRU", "device": "Device",
}

var roleColors = map[string]string{"cpu": "2196f3", "dimm": "4caf50", "disk": "ff9800", "raid_drive": "ff9800",
	"nic": "9c27b0", "transceiver": "9c27b0", "gpu": "f44336", "npu": "f44336", "psu": "795548"}

// RoleName is the inventory item role of an inventory category.
func RoleName(cat string) string { return roleName(cat) }

func roleName(cat string) string {
	if n, ok := roleNames[cat]; ok {
		return n
	}
	return strings.ToUpper(cat[:1]) + strings.ReplaceAll(cat[1:], "_", " ")
}

func (s *syncer) role(cat string) int {
	if v, ok := s.roles[cat]; ok {
		return v
	}
	name := roleName(cat)
	v, err := s.getOrCreate("/api/dcim/inventory-item-roles/", q("name", name),
		obj{"name": name, "slug": slugify(name), "color": firstNonEmpty(roleColors[cat], "9e9e9e")})
	if err != nil {
		s.problem("inventory role %s: %v", name, err)
	}
	s.roles[cat] = v
	return v
}

// itemName is the inventory item's name: drives by serial number (their
// device names change between boots), the rest by location.
func itemName(c model.Component) string {
	r := roleName(c.Category)
	if (c.Category == "disk" || c.Category == "raid_drive") && c.Serial != "" {
		return trunc(r+" "+c.Serial, 64)
	}
	loc := strings.TrimSpace(c.Location)
	if loc == "" {
		return r
	}
	if strings.HasPrefix(strings.ToLower(loc), strings.ToLower(r)) {
		return trunc(loc, 64)
	}
	return trunc(r+" "+loc, 64)
}

func describe(c model.Component) string {
	var parts []string
	for _, p := range []string{c.Model, c.Detail} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	if c.Firmware != "" {
		parts = append(parts, "firmware "+c.Firmware)
	}
	if c.Status != model.StatusOK && c.Status != model.StatusUnknown {
		parts = append(parts, "Tyr: "+c.Status.String())
	}
	return trunc(strings.Join(parts, " · "), 200)
}

// mergeCards makes one item of the ports of one network card: NICs with
// the same model and serial number (a dual-port card reports its serial on
// every port). The item is named after the card's PCI slot, the ports go
// into its label.
func mergeCards(comps []model.Component) []model.Component {
	out := make([]model.Component, 0, len(comps))
	at := map[string]int{}
	ports := map[int][]string{}
	for _, c := range comps {
		if c.Category != "nic" || c.Serial == "" {
			out = append(out, c)
			continue
		}
		port, pci, _ := strings.Cut(c.Location, " ")
		key := c.Model + "|" + c.Serial
		if i, ok := at[key]; ok {
			ports[i] = append(ports[i], port)
			out[i].Status = model.Worst(out[i].Status, c.Status)
			continue
		}
		if i := strings.LastIndexByte(pci, '.'); i > 0 {
			pci = pci[:i] // 03:00.1 -> 03:00: the card, not the function
		}
		at[key] = len(out)
		ports[len(out)] = []string{port}
		c.Location = pci
		out = append(out, c)
	}
	for i, ps := range ports {
		d := "ports " + strings.Join(ps, ", ")
		if len(ps) == 1 {
			d = "port " + ps[0]
		}
		out[i].Detail = strings.TrimSpace(strings.TrimSpace(out[i].Detail+" · ") + " " + d)
		out[i].Detail = strings.TrimPrefix(out[i].Detail, "· ")
	}
	return out
}

// itemInfo is what Tyr knows about a part, for the log and the plan.
func itemInfo(c model.Component) string {
	var parts []string
	add := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			parts = append(parts, k+v)
		}
	}
	add("", Manufacturer(c.Vendor, c.Model))
	add("", c.Model)
	add("part ", c.PartNumber)
	add("S/N ", c.Serial)
	add("firmware ", c.Firmware)
	add("", c.Detail)
	if c.Status == model.StatusCrit {
		parts = append(parts, "FAILED")
	}
	return strings.Join(parts, " · ")
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func (s *syncer) inventory() error {
	cats := s.cfg.Categories
	if len(cats) == 0 {
		cats = DefaultCategories
	}
	var comps []model.Component
	for _, c := range inventory(s.in.Snap) {
		if slices.Contains(cats, c.Category) && c.Detail != "empty socket" {
			comps = append(comps, c)
		}
	}
	comps = mergeCards(comps)
	var existing []obj
	if !s.newDevice() {
		var err error
		if existing, err = s.c.List(s.ctx, "/api/dcim/inventory-items/", q("device_id", fmt.Sprint(s.devID))); err != nil {
			return err
		}
	}
	byName, bySerial := map[string]obj{}, map[string]obj{}
	for _, e := range existing {
		byName[str(e["name"])] = e
		if sn := str(e["serial"]); sn != "" {
			bySerial[str(nested(e, "role", "name"))+"|"+sn] = e
		}
	}
	seen := map[int]bool{}
	used := map[string]bool{}
	var created, updated, failed int
	for _, c := range comps {
		name := itemName(c)
		for i := 2; used[name]; i++ {
			name = trunc(fmt.Sprintf("%s #%d", itemName(c), i), 64)
		}
		used[name] = true
		body := obj{"label": trunc(c.Location, 64), "part_id": trunc(firstNonEmpty(c.PartNumber, c.Model), 50),
			"serial": trunc(c.Serial, 50), "description": describe(c), "discovered": true}
		if v := Manufacturer(c.Vendor, c.Model); v != "" {
			if mid, err := s.manufacturer(v); err == nil {
				body["manufacturer"] = mid
			}
		}
		if rid := s.role(c.Category); rid != 0 {
			body["role"] = rid
		}
		cur := byName[name]
		if c.Serial != "" && (cur == nil || str(cur["serial"]) != c.Serial) {
			if x := bySerial[roleName(c.Category)+"|"+c.Serial]; x != nil && !seen[id(x["id"])] && !used[str(x["name"])] {
				cur = x // same part under another name (renamed or moved)
			}
		}
		if cur != nil && seen[id(cur["id"])] {
			cur = nil
		}
		if s.cfg.MarkFailed { // NetBox versions without the field ignore it
			switch {
			case c.Status == model.StatusCrit:
				body["status"] = "failed"
				failed++
			case cur != nil && str(nested(cur, "status", "value")) == "failed":
				body["status"] = "active"
			}
		}
		info := itemInfo(c)
		if cur == nil {
			body["device"], body["name"] = s.devID, name
			o, err := s.c.Post(s.ctx, "/api/dcim/inventory-items/", body)
			if err != nil {
				s.problem("inventory item %s: %v", name, err)
				continue
			}
			seen[id(o["id"])] = true
			created++
			s.change("inventory item", name, "create", info)
			continue
		}
		seen[id(cur["id"])] = true
		body["name"] = name
		if ch := changes(cur, body); len(ch) > 0 {
			if _, err := s.c.Patch(s.ctx, "/api/dcim/inventory-items/", id(cur["id"]), ch); err != nil {
				s.problem("inventory item %s: %v", name, err)
				continue
			}
			updated++
			s.change("inventory item", name, "update", describeChanges(ch))
			continue
		}
		s.change("inventory item", name, "unchanged", info)
	}
	s.log("inventory items: %d parts, %d new, %d changed%s", len(comps), created, updated,
		map[bool]string{true: fmt.Sprintf(", %d marked failed", failed)}[failed > 0])
	if !s.cfg.Prune {
		return nil
	}
	// Prune: discovered items of Tyr's roles that this run did not report,
	// only in categories this run could see at all.
	present := map[string]bool{}
	for _, c := range comps {
		present[roleName(c.Category)] = true
	}
	removed := 0
	for _, e := range existing {
		r := str(nested(e, "role", "name"))
		if seen[id(e["id"])] || e["discovered"] != true || !present[r] {
			continue
		}
		if err := s.c.Delete(s.ctx, "/api/dcim/inventory-items/", id(e["id"])); err != nil {
			s.problem("remove %s: %v", str(e["name"]), err)
			continue
		}
		removed++
		s.change("inventory item", str(e["name"]), "delete", "no longer reported")
	}
	if removed > 0 {
		s.log("inventory items: %d removed", removed)
	}
	return nil
}

// changes returns the fields of want that differ from cur.
func changes(cur, want obj) obj {
	out := obj{}
	for k, v := range want {
		c := cur[k]
		switch w := v.(type) {
		case int:
			if id(c) != w {
				out[k] = v
			}
		case string:
			cs := str(c)
			if m, ok := c.(obj); ok { // choice fields: {"value": "active"}
				cs = str(m["value"])
			}
			if cs != w {
				out[k] = v
			}
		case bool:
			if b, _ := c.(bool); b != w {
				out[k] = v
			}
		default:
			out[k] = v
		}
	}
	return out
}

// --- device fields -----------------------------------------------------------

func (s *syncer) deviceFields() error {
	body := obj{}
	// status: failed hardware (or a failed test) marks the device failed;
	// healthy again, it goes back to active - only from failed, so a
	// deliberate planned/staged/offline stays.
	cur := str(nested(s.dev, "status", "value"))
	s.devCrit = s.facts["failed_parts"].N > 0 || s.facts["verdict"].S == string(accept.Fail)
	if s.cfg.MarkFailed {
		switch {
		case s.devCrit && cur != "failed":
			body["status"] = "failed"
			s.change("device", s.rep.Device, "update", "status failed: "+s.failReason())
		case !s.devCrit && cur == "failed" && s.healthAssessable():
			body["status"] = "active"
			s.change("device", s.rep.Device, "update", "status active again (was failed)")
		}
	}
	// tags: rules own their tags; other tags stay as they are
	var curTags []int
	var curSlugs []string
	for _, t := range list(s.dev["tags"]) {
		curTags = append(curTags, id(t["id"]))
		curSlugs = append(curSlugs, str(t["slug"]))
	}
	keep := map[string]bool{}
	for _, sl := range curSlugs {
		keep[sl] = true
	}
	changed := false
	for _, r := range s.tags.Evaluate(s.facts) {
		sl := slugify(r.Rule.Tag)
		if r.Err != nil {
			s.problem("tag rule %s: %v (tag left as it is)", r.Rule.Tag, r.Err)
			continue
		}
		if r.Match != keep[sl] {
			changed = true
			keep[sl] = r.Match
			s.change("tag", r.Rule.Tag, map[bool]string{true: "set", false: "remove"}[r.Match], r.Rule.When)
		}
	}
	var tagNames []string
	if changed {
		var ids []int
		for i, sl := range curSlugs {
			if keep[sl] {
				ids = append(ids, curTags[i])
				tagNames = append(tagNames, sl)
				delete(keep, sl)
			}
		}
		for _, r := range s.tags.Rules {
			sl := slugify(r.Tag)
			if keep[sl] {
				tid, err := s.getOrCreate("/api/extras/tags/", q("slug", sl), obj{"name": r.Tag, "slug": sl})
				if err != nil {
					s.problem("tag %s: %v", r.Tag, err)
					continue
				}
				ids = append(ids, tid)
				tagNames = append(tagNames, sl)
				delete(keep, sl)
			}
		}
		if ids == nil {
			ids = []int{}
		}
		body["tags"] = ids
	} else {
		tagNames = curSlugs
	}
	sort.Strings(tagNames)
	s.rep.Tags = tagNames
	if len(s.cfg.CustomFields) > 0 {
		cf := obj{}
		curCF, _ := s.dev["custom_fields"].(obj)
		for k, v := range s.cfg.CustomFields {
			val := Expand(v, s.facts)
			if str(curCF[k]) != val {
				cf[k] = val
				s.change("custom field", k, "set", val)
			}
		}
		if len(cf) > 0 {
			body["custom_fields"] = cf
		}
	}
	if len(body) == 0 {
		return nil
	}
	_, err := s.c.Patch(s.ctx, "/api/dcim/devices/", s.devID, body)
	return err
}

// healthAssessable: a snapshot without storage or without inventory
// cannot declare the machine healthy again.
func (s *syncer) healthAssessable() bool {
	return s.in.Snap.Storage != nil && len(inventory(s.in.Snap)) > 0
}

func (s *syncer) failReason() string {
	var r []string
	if n := s.facts["failed_parts"].N; n > 0 {
		var parts []string
		for _, c := range inventory(s.in.Snap) {
			if c.Status == model.StatusCrit {
				parts = append(parts, itemName(c))
			}
		}
		r = append(r, fmt.Sprintf("failed: %s", strings.Join(parts, ", ")))
	}
	if s.facts["verdict"].S == string(accept.Fail) {
		r = append(r, "acceptance test FAIL")
	}
	return strings.Join(r, "; ")
}

// --- journal -----------------------------------------------------------------

var runLink = regexp.MustCompile(`\[([^\]]+)\]\(runs/[^)]+\)`)

func (s *syncer) journal() error {
	kind, text := JournalEntry(s.in)
	_, err := s.c.Post(s.ctx, "/api/extras/journal-entries/", obj{"assigned_object_type": "dcim.device",
		"assigned_object_id": s.devID, "kind": kind, "comments": text})
	if err == nil {
		s.rep.Journal = true
		title, _, _ := strings.Cut(text, "\n")
		s.change("journal entry", kind, "create", strings.TrimLeft(title, "# "))
	}
	return err
}

// JournalEntry is the journal entry's kind and text: the acceptance test's
// summary, or the health check's findings.
func JournalEntry(in Input) (kind, text string) {
	if r := in.Result; r != nil {
		kind = map[accept.Verdict]string{accept.Pass: "success", accept.Remarks: "warning",
			accept.Fail: "danger", accept.Incomplete: "warning"}[r.Verdict]
		md := runLink.ReplaceAllString(accept.Markdown(r, nil), "$1")
		if in.ResultDir != "" {
			md += fmt.Sprintf("\nResults: `%s`\n", in.ResultDir)
		}
		return firstNonEmpty(kind, "info"), md
	}
	sn := in.Snap
	var b strings.Builder
	worst := model.StatusOK
	var lines []string
	for _, id := range model.Modules {
		sec := sn.Section(id)
		if sec == nil {
			continue
		}
		for _, f := range sec.Head().Findings {
			worst = model.Worst(worst, f.Status)
			if f.Status == model.StatusWarn || f.Status == model.StatusCrit {
				lines = append(lines, fmt.Sprintf("- **%s** %s: %s", f.Status, f.Subject, f.Message))
			}
		}
	}
	fmt.Fprintf(&b, "# Tyr health check: %s\n\n", worst)
	fmt.Fprintf(&b, "%s · %s · Tyr %s\n\n", sn.Hostname, sn.Timestamp.UTC().Format("2006-01-02 15:04 UTC"), sn.TyrVersion)
	if len(lines) == 0 {
		b.WriteString("No findings.\n")
	} else {
		if len(lines) > 60 {
			lines = append(lines[:60], fmt.Sprintf("- … and %d more", len(lines)-60))
		}
		b.WriteString(strings.Join(lines, "\n") + "\n")
	}
	kind = map[model.Status]string{model.StatusOK: "success", model.StatusWarn: "warning", model.StatusCrit: "danger"}[worst]
	return firstNonEmpty(kind, "info"), b.String()
}
