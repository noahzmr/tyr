package tui

import (
	tea "charm.land/bubbletea/v2"
)

// serialer is implemented by tabs whose selected row can have a serial
// number; "c" copies it (canvas Interactions: "c copy serial (OSC 52,
// falls back to a footer message with the serial)").
type serialer interface {
	SelectedSerial() (name, serial string)
}

// copySerial sets the clipboard through OSC 52 and always shows the serial
// in the footer: terminals that ignore OSC 52 give no answer, so the
// footer is the fallback the user can copy from.
func (m *Model) copySerial() tea.Cmd {
	s, ok := m.tabs[m.active].(serialer)
	if !ok {
		m.flash("no serial on this tab")
		return nil
	}
	name, sn := s.SelectedSerial()
	switch {
	case name == "":
		m.flash("no row selected")
		return nil
	case sn == "":
		m.flash("no serial for " + name + serialHint(m))
		return nil
	}
	m.flash("copied S/N " + sn + " (" + name + ")")
	return tea.SetClipboard(sn)
}

func serialHint(m *Model) string {
	if m.snap != nil && !m.snap.Privileged {
		return " · serials need root (sudo tyr)"
	}
	return ""
}

func (m *memoryTab) SelectedSerial() (string, string) {
	if m.mem == nil {
		return "", ""
	}
	it, ok := m.list.current()
	if !ok {
		return "", ""
	}
	d, _ := m.dimm(it.key)
	return d.Locator, d.Serial
}

func (s *storageTab) SelectedSerial() (string, string) {
	if s.st == nil {
		return "", ""
	}
	it, ok := s.lists[s.view].current()
	if !ok {
		return "", ""
	}
	switch s.view {
	case 0:
		d, _ := s.disk(it.key)
		return d.Name, d.Serial
	case 1:
		ri := s.raidItems()[it.key]
		switch {
		case ri.pd != nil:
			return "PD " + ri.pd.Slot, ri.pd.Serial
		case ri.ctrl != nil:
			return ri.ctrl.Model, ri.ctrl.Serial
		}
		return it.key, ""
	}
	return it.key, ""
}

func (n *networkTab) SelectedSerial() (string, string) {
	if n.net == nil {
		return "", ""
	}
	it, ok := n.list.current()
	if !ok {
		return "", ""
	}
	x, _ := n.nic(it.key)
	if x.Serial == "" && x.Module != nil && x.Module.Serial != "" {
		return x.Name + " transceiver", x.Module.Serial
	}
	return x.Name, x.Serial
}

func (a *accelTab) SelectedSerial() (string, string) {
	if a.acc == nil {
		return "", ""
	}
	it, ok := a.list.current()
	if !ok {
		return "", ""
	}
	x, _ := a.byAddr(it.key)
	return accelName(x), x.Serial
}

func (v *inventoryTab) SelectedSerial() (string, string) {
	if v.snap == nil {
		return "", ""
	}
	if v.view == 1 {
		r, ok := v.treeRow()
		if !ok {
			return "", ""
		}
		return r.node.label, r.node.serial
	}
	it, ok := v.list.current()
	if !ok {
		return "", ""
	}
	c, _ := v.component(it.key)
	return c.Category + " " + c.Location, c.Serial
}
