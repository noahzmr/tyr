package tui

import (
	"strings"
)

// PageInfo implementations: the breadcrumb of each tab's detail page
// ("› Disks › sdd  ✖ CRIT   Seagate ST4000NM0035 · S/N ZC1A9K3R").

func sn(s string) string {
	if s == "" {
		return ""
	}
	return "S/N " + s
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (c *cpuTab) PageInfo() (pageInfo, bool) {
	if c.cpu == nil || c.sel >= len(c.cpu.Logical) {
		return pageInfo{}, false
	}
	l := c.cpu.Logical[c.sel]
	state := "online"
	if !l.Online {
		state = "offline"
	}
	return pageInfo{path: []string{[]string{"Grid", "Cores", "Details"}[c.view], cpuKey(l.ID)}, status: l.Status,
		facts: nonEmpty(shortCPUModel(c.cpu.Model), "socket "+intOrDash(l.SocketID)+" · core "+intOrDash(l.CoreID), state)}, true
}

func (m *memoryTab) PageInfo() (pageInfo, bool) {
	name, serial := m.SelectedSerial()
	if name == "" {
		return pageInfo{}, false
	}
	d, _ := m.dimm(name)
	size := ""
	if d.Populated {
		size = strings.TrimSpace(bytesGB(d.SizeBytes) + " " + d.Type)
	}
	return pageInfo{path: []string{[]string{"Table", "Slot map"}[m.view], name}, status: d.Status,
		facts: nonEmpty(d.Manufacturer, size, sn(serial))}, true
}

func (s *storageTab) PageInfo() (pageInfo, bool) {
	if s.st == nil {
		return pageInfo{}, false
	}
	it, ok := s.lists[s.view].current()
	if !ok {
		return pageInfo{}, false
	}
	view := s.storageViews()[s.view]
	if s.view == 3 {
		if x, ok := s.session(it.key); ok {
			return pageInfo{path: []string{view, x.ID}, status: x.Status, facts: nonEmpty(transportLabel(x.Transport), sessionTarget(x))}, true
		}
		return pageInfo{path: []string{view, "node"}, status: it.status}, true
	}
	if s.view == 0 {
		d, _ := s.disk(it.key)
		return pageInfo{path: []string{view, d.Name}, status: d.Status,
			facts: nonEmpty(strings.TrimSpace(d.Vendor+" "+d.Model), bytesSI(d.SizeBytes), sn(d.Serial))}, true
	}
	name, serial := s.SelectedSerial()
	return pageInfo{path: []string{view, name}, status: it.status, facts: nonEmpty(sn(serial))}, true
}

func (n *networkTab) PageInfo() (pageInfo, bool) {
	if n.net == nil {
		return pageInfo{}, false
	}
	it, ok := n.list.current()
	if !ok {
		return pageInfo{}, false
	}
	x, _ := n.nic(it.key)
	return pageInfo{path: []string{x.Name}, status: x.Status,
		facts: nonEmpty(strings.TrimSpace(x.Vendor+" "+x.Model), x.MAC, sn(x.Serial))}, true
}

func (a *accelTab) PageInfo() (pageInfo, bool) {
	if a.acc == nil {
		return pageInfo{}, false
	}
	it, ok := a.list.current()
	if !ok {
		return pageInfo{}, false
	}
	x, _ := a.byAddr(it.key)
	return pageInfo{path: []string{accelName(x)}, status: x.Status,
		facts: nonEmpty(x.Model, x.PCIAddress, sn(x.Serial))}, true
}

func (v *inventoryTab) PageInfo() (pageInfo, bool) {
	if v.snap == nil || v.view != 0 {
		return pageInfo{}, false
	}
	it, ok := v.list.current()
	if !ok {
		return pageInfo{}, false
	}
	c, _ := v.component(it.key)
	return pageInfo{path: []string{"List", catLabel(c.Category) + " " + c.Location}, status: c.Status,
		facts: nonEmpty(strings.TrimSpace(c.Vendor+" "+c.Model), sn(c.Serial))}, true
}
