// Package network collects NIC inventory, link state, counters and rates,
// bonds, VLANs, bridges, transceiver DOM, NIC temperatures, LLDP neighbors
// and the BMC LAN configuration.
package network

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/netinfo"
	"github.com/noahzmr/tyr/internal/source/pci"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

// Collector is the network collector.
type Collector struct {
	mu   sync.Mutex
	ring map[string]ringInfo // ethtool -g / -l, read once per interface
}

// New returns a network collector.
func New() *Collector { return &Collector{ring: map[string]ringInfo{}} }

func (*Collector) ID() model.ModuleID { return model.ModNetwork }

func (c *Collector) Collect(ctx context.Context, env *collector.Env, prevSec model.Section) (model.Section, error) {
	f := env.FS
	prev, _ := prevSec.(*model.Network)
	prevBy := map[string]*model.NIC{}
	var dt float64
	if prev != nil {
		for i := range prev.Interfaces {
			prevBy[prev.Interfaces[i].Name] = &prev.Interfaces[i]
		}
		dt = env.Now().Sub(prev.CollectedAt).Seconds()
	}
	out := &model.Network{Interfaces: []model.NIC{}}
	h := &out.Header
	if !hostfs.Exists(f, "/sys/class/net") {
		return nil, fmt.Errorf("/sys/class/net not available")
	}
	h.SetSource("sysfs", model.AvailOK, "")

	ifs := netinfo.ReadIfaces(f)
	byName := map[string]*netinfo.Iface{}
	for i := range ifs {
		byName[ifs[i].Name] = &ifs[i]
	}
	addrs := c.addresses(ctx, env, h)
	vlans := netinfo.VLANIDs(f)
	lldp := c.lldp(ctx, env, h)
	hwmon := sysfs.ReadHwmon(f)
	_, haveEthtool := env.Exec.LookPath("ethtool")
	var et *netinfo.Ethtool // native requests on a live host
	if env.FS.IsHost() {
		if e, err := netinfo.OpenEthtool(); err == nil {
			et = e
			defer e.Close()
		}
	}

	// Which interfaces to show: physical, wireless, bonds, VLANs on shown
	// interfaces, bridges with a physical/bond port. Everything else only
	// with --nic-all.
	show := func(x *netinfo.Iface) bool {
		switch x.Kind {
		case "physical", "wireless", "bond":
			return true
		case "vlan":
			for _, l := range x.Lowers {
				if p := byName[l]; p != nil && (p.Kind == "physical" || p.Kind == "bond") {
					return true
				}
			}
		case "bridge":
			for _, p := range x.BridgePorts {
				if q := byName[p]; q != nil && (q.Kind == "physical" || q.Kind == "bond" || q.Kind == "vlan") {
					return true
				}
			}
		}
		return false
	}

	for i := range ifs {
		x := &ifs[i]
		if !show(x) && !env.Opts.NICAll {
			out.Hidden++
			continue
		}
		n := model.NIC{Name: x.Name, Kind: x.Kind, MAC: x.MAC, AdminUp: x.AdminUp, OperState: x.OperState, Carrier: x.Carrier,
			SpeedMbps: x.SpeedMbps, Duplex: x.Duplex, MTU: x.MTU, CarrierChanges: x.CarrierChanges, Master: x.Master,
			Counters: x.Stats, Addresses: addrs[x.Name], NUMANode: -1, Driver: x.Driver}
		if n.Addresses == nil {
			n.Addresses = []string{}
		}
		switch x.Kind {
		case "bond":
			n.Members = x.Lowers
			if b, err := f.ReadFile("/proc/net/bonding/" + x.Name); err == nil {
				n.Bond = netinfo.ParseBonding(b)
				if len(n.Members) == 0 {
					for _, s := range n.Bond.Slaves {
						n.Members = append(n.Members, s.Name)
					}
				}
			}
		case "bridge":
			n.Members = x.BridgePorts
		case "vlan":
			n.Members = x.Lowers
			if id, ok := vlans[x.Name]; ok {
				n.VLANID = &id
			}
		}
		if x.DevicePath != "" {
			if d, ok := pci.Read(f, x.DevicePath); ok {
				n.PCIAddress = d.Address
				n.Vendor, n.Model = pci.Names(f, d.Vendor, d.Device)
				n.NUMANode = d.NUMANode
				n.Serial, n.PartNumber = vpdIDs(f, d)
			}
			c.ethtool(ctx, env, &n, haveEthtool == nil, et)
			if n.Kind == "physical" && (et != nil || haveEthtool == nil) {
				c.rings(ctx, env, &n, et, haveEthtool == nil)
			}
			for _, chip := range hwmon {
				if chip.Device == x.DevicePath {
					for _, s := range chip.Sensors {
						if s.Kind == model.KindTemperature && s.Input != nil {
							n.TempCelsius = s.Input
							if th := s.Thresholds(); th != nil && check.Eval(s.Input, th) >= model.StatusWarn {
								h.Find(check.Eval(s.Input, th), "nic_temp", n.Name, "hwmon", fmt.Sprintf("%s NIC at %s", n.Name, check.FmtTemp(*s.Input)))
							}
							break
						}
					}
				}
			}
		}
		if nb, ok := lldp[x.Name]; ok {
			nb := nb
			n.LLDP = &nb
		}
		if p := prevBy[x.Name]; p != nil && dt > 0.5 {
			if n.Counters.RxBytes >= p.Counters.RxBytes && n.Counters.TxBytes >= p.Counters.TxBytes {
				n.RxBps = model.F(float64(n.Counters.RxBytes-p.Counters.RxBytes) * 8 / dt)
				n.TxBps = model.F(float64(n.Counters.TxBytes-p.Counters.TxBytes) * 8 / dt)
			}
		}
		out.Interfaces = append(out.Interfaces, n)
	}
	c.bmc(ctx, env, out)
	softnet(env, out)

	for i := range out.Interfaces {
		n := &out.Interfaces[i]
		n.Status = nicFindings(h, n, prevBy[n.Name])
	}
	losses(h, out, prev, prevBy, dt)
	h.Summary = summary(out)
	h.Rollup()
	return out, nil
}

func (c *Collector) addresses(ctx context.Context, env *collector.Env, h *model.Header) map[string][]string {
	out := map[string][]string{}
	if env.FS.IsHost() {
		ifs, err := net.Interfaces()
		if err != nil {
			return out
		}
		for _, i := range ifs {
			as, _ := i.Addrs()
			for _, a := range as {
				if ipn, ok := a.(*net.IPNet); ok && ipn.IP.IsGlobalUnicast() {
					ones, _ := ipn.Mask.Size()
					out[i.Name] = append(out[i.Name], fmt.Sprintf("%s/%d", ipn.IP, ones))
				}
			}
		}
		return out
	}
	if b, err := env.Exec.Run(ctx, "ip", "-j", "addr", "show"); err == nil {
		if m, err := netinfo.ParseIPAddr(b); err == nil {
			return m
		}
	}
	return out
}

func (c *Collector) lldp(ctx context.Context, env *collector.Env, h *model.Header) map[string]model.LLDPNeighbor {
	if _, err := env.Exec.LookPath("lldpctl"); err != nil {
		h.SetSource("lldp", model.AvailUnavailable, "lldpctl not installed")
		return nil
	}
	b, err := env.Exec.Run(ctx, "lldpctl", "-f", "json")
	if err != nil {
		h.SetSource("lldp", model.AvailUnavailable, err.Error())
		return nil
	}
	m, err := netinfo.ParseLLDP(b)
	if err != nil {
		h.SetSource("lldp", model.AvailUnavailable, err.Error())
		return nil
	}
	h.SetSource("lldp", model.AvailOK, "via lldpctl")
	return m
}

// ethtool fills driver/firmware, link modes, error counters and the
// transceiver: natively (et, the SIOCETHTOOL requests) where the driver
// answers, else through the ethtool tool when it is installed. Wireless
// NICs only get driver info.
func (c *Collector) ethtool(ctx context.Context, env *collector.Env, n *model.NIC, have bool, et *netinfo.Ethtool) {
	var di netinfo.DrvInfo
	var err error
	if env.FS.IsHost() {
		di, err = netinfo.IoctlDrvInfo(n.Name)
	} else {
		err = fmt.Errorf("fixture")
	}
	if err != nil && have {
		if b, e := env.Exec.Run(ctx, "ethtool", "-i", n.Name); e == nil {
			di = netinfo.ParseDrvInfo(b)
		}
	}
	if di.Driver != "" {
		n.Driver = di.Driver
	}
	n.DriverVersion, n.Firmware = di.Version, di.Firmware
	if strings.EqualFold(n.Firmware, "N/A") {
		n.Firmware = ""
	}
	if n.Kind == "wireless" {
		return
	}
	// link modes
	linked := false
	if et != nil {
		if li, err := et.LinkInfo(n.Name); err == nil {
			n.MaxSpeedMbps, n.PortType, n.Autoneg, linked = li.MaxSpeedMbps, li.Port, li.Autoneg, true
		}
	}
	if !linked && have {
		if b, err := env.Exec.Run(ctx, "ethtool", n.Name); err == nil || len(b) > 0 {
			li := netinfo.ParseLinkInfo(b)
			n.MaxSpeedMbps, n.PortType, n.Autoneg = li.MaxSpeedMbps, li.Port, li.Autoneg
		}
	}
	// driver statistics: the error counters
	stats := false
	if et != nil {
		if all, err := et.Stats(n.Name); err == nil {
			if m := netinfo.FilterStats(all); len(m) > 0 {
				n.ErrorCounters = m
			}
			stats = true
		}
	}
	if !stats && have {
		if b, err := env.Exec.Run(ctx, "ethtool", "-S", n.Name); err == nil {
			if m := netinfo.ParseStats(b); len(m) > 0 {
				n.ErrorCounters = m
			}
		}
	}
	// transceiver
	if n.PortType != "FIBRE" && n.PortType != "Direct Attach Copper" && n.PortType != "Other" && n.PortType != "" {
		return
	}
	var t *model.Transceiver
	if et != nil {
		if typ, b, err := et.ModuleEEPROM(n.Name); err == nil {
			t = netinfo.ParseModuleEEPROM(typ, b)
		}
	}
	if (t == nil || len(t.Sensors) == 0) && have { // externally calibrated or unread: let ethtool decode it
		if b, err := env.Exec.Run(ctx, "ethtool", "-m", n.Name); err == nil {
			if tt := netinfo.ParseModule(b); tt != nil {
				t = tt
			}
		}
	}
	if t != nil {
		st := model.StatusOK
		for i := range t.Sensors {
			check.EvalSensor(&t.Sensors[i])
			st = model.Worst(st, t.Sensors[i].Status)
		}
		t.Status = st
		n.Module = t
	}
}

func (c *Collector) bmc(ctx context.Context, env *collector.Env, out *model.Network) {
	cl, st := env.IPMI()
	if cl == nil {
		if st.State != model.AvailNotApplicable {
			st.Name = "bmc_lan"
			out.Sources = append(out.Sources, st)
		}
		return
	}
	b, err := cl.LanPrint(ctx)
	if err != nil {
		out.SetSource("bmc_lan", model.AvailUnavailable, err.Error())
		return
	}
	out.BMC = netinfo.ParseIPMILan(b)
	out.SetSource("bmc_lan", model.AvailOK, "via "+string(cl.Tool))
}

func mbps(v int) string {
	if v >= 1000 && v%1000 == 0 {
		return fmt.Sprintf("%dG", v/1000)
	}
	return fmt.Sprintf("%dM", v)
}

func nicFindings(h *model.Header, n *model.NIC, p *model.NIC) model.Status {
	st := model.StatusOK
	add := func(s model.Status, code, msg string) {
		h.Find(s, code, n.Name, "sysfs", msg)
		st = model.Worst(st, s)
	}
	linked := n.Carrier != nil && *n.Carrier
	configured := len(n.Addresses) > 0 || n.Master != "" || n.Kind == "bond" || n.Kind == "vlan"
	if n.Kind == "bridge" {
		configured = len(n.Addresses) > 0
	}
	switch {
	case n.AdminUp && !linked && configured:
		add(model.StatusCrit, "link_down", fmt.Sprintf("%s link down", n.Name))
	case n.AdminUp && !linked && n.Kind == "physical":
		h.Find(model.StatusOK, "link_unused", n.Name, "sysfs", n.Name+": no link (not configured)")
	}
	if linked && (n.Kind == "physical") {
		if n.Duplex == "half" {
			add(model.StatusWarn, "half_duplex", n.Name+" running half duplex")
		}
		if n.SpeedMbps != nil && n.MaxSpeedMbps != nil && *n.SpeedMbps < *n.MaxSpeedMbps {
			add(model.StatusWarn, "speed_below_max", fmt.Sprintf("%s at %s, port supports %s", n.Name, mbps(*n.SpeedMbps), mbps(*n.MaxSpeedMbps)))
		}
	}
	// Counters: rising since the last refresh is a WARN; history is info.
	errs := n.Counters.RxErrors + n.Counters.TxErrors + n.Counters.RxCRC
	if p != nil {
		perr := p.Counters.RxErrors + p.Counters.TxErrors + p.Counters.RxCRC
		if errs > perr {
			add(model.StatusWarn, "errors_rising", fmt.Sprintf("%s: %d new rx/tx/CRC errors since last refresh", n.Name, errs-perr))
		}
		if n.CarrierChanges != nil && p.CarrierChanges != nil && *n.CarrierChanges > *p.CarrierChanges {
			add(model.StatusWarn, "link_flap", fmt.Sprintf("%s link flapped (%d carrier changes since last refresh)", n.Name, *n.CarrierChanges-*p.CarrierChanges))
		}
	} else if n.Counters.RxCRC > 0 {
		add(model.StatusWarn, "crc_errors", fmt.Sprintf("%s: %d CRC errors since boot (cable/optics)", n.Name, n.Counters.RxCRC))
	} else if errs > 0 {
		h.Find(model.StatusOK, "errors_history", n.Name, "sysfs", fmt.Sprintf("%s: %d rx/tx errors since boot", n.Name, errs))
	}
	if n.CarrierChanges != nil && *n.CarrierChanges > 20 && p == nil && n.Kind == "physical" {
		h.Find(model.StatusOK, "flap_history", n.Name, "sysfs", fmt.Sprintf("%s: %d carrier changes since boot", n.Name, *n.CarrierChanges))
	}
	if b := n.Bond; b != nil {
		var down []string
		aggs := map[string]bool{}
		for _, s := range b.Slaves {
			if s.MIIStatus != "up" {
				down = append(down, s.Name)
			}
			if s.AggregatorID != "" && s.MIIStatus == "up" {
				aggs[s.AggregatorID] = true
			}
		}
		switch {
		case b.MIIStatus != "" && b.MIIStatus != "up":
			add(model.StatusCrit, "bond_down", fmt.Sprintf("%s down (%s)", n.Name, b.Mode))
		case len(down) > 0:
			add(model.StatusCrit, "bond_degraded", fmt.Sprintf("%s degraded: %s down (%d/%d members up)", n.Name, strings.Join(down, ", "), len(b.Slaves)-len(down), len(b.Slaves)))
		case len(aggs) > 1:
			add(model.StatusWarn, "bond_aggregator", fmt.Sprintf("%s: members in %d different LACP aggregators (switch config?)", n.Name, len(aggs)))
		}
	}
	if m := n.Module; m != nil && linked {
		for _, s := range m.Sensors {
			if s.Status >= model.StatusWarn && s.Value != nil {
				unit := map[string]string{model.UnitDBm: " dBm", model.UnitCelsius: "°C", model.UnitVolts: " V"}[s.Unit]
				add(s.Status, "transceiver", fmt.Sprintf("%s transceiver %s %.2f%s out of range", n.Name, strings.ToLower(s.Name), *s.Value, unit))
			}
		}
	}

	return st
}

func summary(n *model.Network) string {
	var phys, up int
	var bonds []string
	for _, x := range n.Interfaces {
		if x.Kind == "physical" {
			phys++
			if x.Carrier != nil && *x.Carrier {
				up++
			}
		}
		if x.Kind == "bond" {
			bonds = append(bonds, fmt.Sprintf("%s %s", x.Name, x.Status))
		}
	}
	sort.Strings(bonds)
	parts := []string{fmt.Sprintf("%d/%d ports up", up, phys)}
	parts = append(parts, bonds...)
	for _, x := range n.Interfaces {
		if x.Kind == "wireless" {
			state := "down"
			if x.Carrier != nil && *x.Carrier {
				state = "up"
			}
			parts = append(parts, x.Name+" "+state)
		}
	}
	return strings.Join(parts, ", ")
}

// vpdIDs reads serial and part number from PCI VPD. Multi-port adapters
// usually expose VPD only on function 0.
func vpdIDs(f hostfs.FS, d pci.Device) (serial, part string) {
	for _, dev := range []string{d.Path, d.Path[:len(d.Path)-1] + "0"} {
		if v, ok := pci.ReadVPD(f, dev); ok {
			return v.Serial, v.PartNumber
		}
	}
	return "", ""
}
