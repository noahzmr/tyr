package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
)

const histLen = 60

type networkTab struct {
	showVirtual bool
	net         *model.Network
	w, h        int
	list        *listView
	keys        keyMap
	order       []int                // display order (indices into net.Interfaces)
	depth       map[string]int       // indentation for members
	hist        map[string][]float64 // rx+tx bits/s history for sparklines
}

func newNetworkTab(p *prefs) *networkTab {
	l := newListView(p)
	l.noSort = true // bonds, members and VLANs stay grouped
	return &networkTab{list: l, keys: newKeyMap(), hist: map[string][]float64{}}
}

func (n *networkTab) Title() string          { return "Network" }
func (n *networkTab) Module() model.ModuleID { return model.ModNetwork }
func (n *networkTab) SetSize(w, h int)       { n.w, n.h = w, h }
func (n *networkTab) Help() []key.Binding {
	return []key.Binding{n.keys.Up, n.keys.Enter, key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "virtual")), keyNext}
}

func (n *networkTab) items() {
	if n.net == nil {
		return
	}
	var out []listItem
	for _, i := range n.order {
		x := n.net.Interfaces[i]
		out = append(out, listItem{key: x.Name, alt: []string{x.PCIAddress, x.MAC}, status: x.Status})
	}
	n.list.arrange(out)
}

func (n *networkTab) nic(name string) (model.NIC, bool) {
	for _, x := range n.net.Interfaces {
		if x.Name == name {
			return x, true
		}
	}
	return model.NIC{}, false
}

func (n *networkTab) Focus(subject string) bool {
	n.items()
	return n.list.focus(subject)
}

func (n *networkTab) SetData(s *model.Snapshot) {
	n.net = s.Network
	if n.net == nil {
		return
	}
	for _, x := range n.net.Interfaces {
		if x.RxBps != nil && x.TxBps != nil {
			h := append(n.hist[x.Name], *x.RxBps+*x.TxBps)
			if len(h) > histLen {
				h = h[len(h)-histLen:]
			}
			n.hist[x.Name] = h
		}
	}
	n.arrange()
	n.items()
}

// arrange orders interfaces: bonds/bridges followed by their members
// (indented), VLANs below their parent, then the remaining ports.
func (n *networkTab) arrange() {
	byName := map[string]int{}
	for i, x := range n.net.Interfaces {
		byName[x.Name] = i
	}
	placed := map[int]bool{}
	n.order, n.depth = nil, map[string]int{}
	var place func(i, depth int)
	place = func(i, depth int) {
		if placed[i] {
			return
		}
		placed[i] = true
		x := n.net.Interfaces[i]
		n.order = append(n.order, i)
		n.depth[x.Name] = depth
		if x.Kind == "bond" || x.Kind == "bridge" {
			for _, m := range x.Members {
				if j, ok := byName[m]; ok {
					place(j, depth+1)
				}
			}
		}
		// VLANs on top of this interface
		var vl []int
		for j, y := range n.net.Interfaces {
			if y.Kind == "vlan" && len(y.Members) > 0 && y.Members[0] == x.Name {
				vl = append(vl, j)
			}
		}
		sort.Ints(vl)
		for _, j := range vl {
			place(j, depth+1)
		}
	}
	for i, x := range n.net.Interfaces {
		if x.Kind == "bond" || x.Kind == "bridge" {
			place(i, 0)
		}
	}
	for i, x := range n.net.Interfaces {
		if x.Master == "" && x.Kind != "vlan" {
			place(i, 0)
		}
	}
	for i := range n.net.Interfaces {
		place(i, 0)
	}
}

func (n *networkTab) Update(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "h" {
		n.showVirtual = !n.showVirtual
		show := n.showVirtual
		return func() tea.Msg { return virtualNICsMsg{show: show} }
	}
	n.list.handle(msg, n.keys, max(1, n.h/2))
	return nil
}

// virtualNICsMsg asks the root model to include (or hide) virtual
// interfaces in the next collection.
type virtualNICsMsg struct{ show bool }

func linkText(x model.NIC) string {
	switch {
	case !x.AdminUp:
		return "admin down"
	case x.Carrier == nil || !*x.Carrier:
		return "no link"
	case x.SpeedMbps != nil:
		s := mbpsText(*x.SpeedMbps)
		if x.Duplex == "half" {
			s += " half"
		}
		return s
	}
	return "up"
}

func mbpsText(v int) string {
	if v >= 1000 && v%1000 == 0 {
		return fmt.Sprintf("%dG", v/1000)
	}
	return fmt.Sprintf("%dM", v)
}

// linkCell is the Link column: speed and duplex, "10G of 25G" when below
// what the port supports (warn), "no link" (crit when the port is up),
// "admin down" (dimmed, not a failure).
func linkCell(t *Theme, x model.NIC) string {
	switch {
	case !x.AdminUp:
		return "admin down"
	case x.Carrier == nil || !*x.Carrier:
		return t.StatusBold(model.StatusCrit, "no link")
	case x.SpeedMbps != nil:
		sp := mbpsText(*x.SpeedMbps)
		if x.MaxSpeedMbps != nil && *x.MaxSpeedMbps > *x.SpeedMbps {
			return t.StatusBold(model.StatusWarn, sp+" of "+mbpsText(*x.MaxSpeedMbps))
		}
		if x.Duplex == "half" {
			return t.StatusBold(model.StatusWarn, sp+" half")
		}
		if x.Duplex == "full" {
			sp += " full"
		}
		if b := x.Bond; b != nil {
			on := 0
			for _, sl := range b.Slaves {
				if sl.MIIStatus == "up" {
					on++
				}
			}
			sp = fmt.Sprintf("%s %d/%d ports", mbpsText(*x.SpeedMbps), on, len(b.Slaves))
			if on < len(b.Slaves) {
				sp = t.StatusBold(model.StatusWarn, sp)
			}
		}
		return sp
	}
	return "up"
}

func kindLabel(k string) string {
	if k == "physical" {
		return "port"
	}
	return k
}

func rate(v *float64) string {
	if v == nil {
		return "–"
	}
	return humanRate(*v, "b/s")
}

// guides returns the tree prefix ("├─ ", "│  └─ ") per interface in the
// display order: bonds/bridges → members → VLANs.
func (n *networkTab) guides() map[string]string {
	out := map[string]string{}
	open := []bool{} // per depth: is there a later sibling?
	for k, i := range n.order {
		name := n.net.Interfaces[i].Name
		d := n.depth[name]
		if d == 0 {
			out[name] = ""
			open = open[:0]
			continue
		}
		last := true
		for _, j := range n.order[k+1:] {
			dj := n.depth[n.net.Interfaces[j].Name]
			if dj < d {
				break
			}
			if dj == d {
				last = false
				break
			}
		}
		for len(open) < d {
			open = append(open, false)
		}
		open = open[:d]
		var b strings.Builder
		for lvl := 1; lvl < d; lvl++ {
			if open[lvl] {
				b.WriteString("│  ")
			} else {
				b.WriteString("   ")
			}
		}
		if last {
			b.WriteString("└─ ")
		} else {
			b.WriteString("├─ ")
		}
		open[d-1] = !last
		if d < len(open) {
			open[d] = !last
		}
		out[name] = b.String()
	}
	return out
}

// View (canvas Network): interfaces as a tree table, a hint line for
// hidden virtual interfaces, the selected interface as three columns
// (adapter · transceiver gauges · counters), findings.
func (n *networkTab) View(t *Theme) string {
	x := n.net
	if x == nil {
		return t.Dim.Render("network data not collected")
	}
	n.items()
	detail := n.detail(t)
	hint := ""
	if x.Hidden > 0 {
		hint = t.Sub.Render(fmt.Sprintf("   + %s hidden · h show", countLabel(x.Hidden, "virtual interface", "virtual interfaces")))
	} else if n.showVirtual {
		hint = t.Sub.Render("   virtual interfaces shown · h hide")
	}
	if b := x.BMC; b != nil && b.IP != "" {
		if hint != "" {
			hint += t.Sub.Render(" · ")
		} else {
			hint = "   "
		}
		hint += t.Sub.Render(fmt.Sprintf("BMC %s (%s) %s", b.IP, b.IPSource, b.MAC))
	}
	avail := n.h - lipgloss.Height(detail) - 2
	if hint != "" {
		avail--
	}
	listH := listHeight(avail, len(n.list.shown), countNonOK(x.Findings))
	sparkW := 14
	g := n.guides()
	cols := []column{{"Interface", -1}, {"Kind", 6}, {"Link", 14}, {"Address / LLDP neighbor", 0}, {">rx", 9}, {">tx", 9}, {"traffic", sparkW}}
	if n.w < 100 {
		cols = []column{{"Interface", -1}, {"Link", 14}, {"Address / LLDP neighbor", 0}, {">rx", 9}}
	}
	for li := range n.list.shown {
		nic, _ := n.nic(n.list.shown[li].key)
		name := t.Sub.Render(g[nic.Name]) + nic.Name
		if nic.Status >= model.StatusWarn {
			name = t.Sub.Render(g[nic.Name]) + t.Bold.Render(nic.Name)
		}
		addr := strings.Join(nic.Addresses, " ")
		if nic.LLDP != nil {
			nb := nic.LLDP.System + " " + nic.LLDP.Port
			if addr != "" {
				addr += " "
			}
			addr += nb
		}
		cells := map[string]string{"Interface": name, "Kind": kindLabel(nic.Kind), "Link": linkCell(t, nic),
			"Address / LLDP neighbor": orDash(addr), ">rx": rate(nic.RxBps), ">tx": rate(nic.TxBps),
			"traffic": spark(n.hist[nic.Name], sparkW, 0, t.Series[0])}
		var r row
		for _, c := range cols {
			r.cells = append(r.cells, cells[c.title])
		}
		r.dim = !nic.AdminUp
		n.list.shown[li].row = r
	}
	parts := []string{n.list.render(t, cols, n.w, listH)}
	if hint != "" {
		parts = append(parts, hint)
	}
	if detail != "" {
		parts = append(parts, "", detail)
	}
	top := strings.Join(parts, "\n")
	return top + "\n\n" + findingsSection(t, "Network", x.Findings, x.Sources, n.h-lipgloss.Height(top)-1, n.w)
}

// detail: the selected interface as adapter · transceiver · counters.
func (n *networkTab) detail(t *Theme) string {
	it, ok := n.list.current()
	if !ok {
		return ""
	}
	x, _ := n.nic(it.key)
	head := t.HeadingFocus(x.Name, "", "enter full page: transceiver, counters, graphs", n.w)
	drv := x.Driver
	if x.DriverVersion != "" {
		drv += " " + x.DriverVersion
	}
	if x.Firmware != "" {
		drv += " · fw " + x.Firmware
	}
	pci := ""
	if x.PCIAddress != "" {
		pci = shortPCI(x.PCIAddress) + fmt.Sprintf(" · NUMA %d", x.NUMANode)
	}
	c1 := kvBlock(t, []kvPair{{"Adapter", strings.TrimSpace(x.Vendor + " " + x.Model)}, {"Serial", t.Bold.Render(x.Serial)},
		{"Driver", drv}, {"PCI", pci}, {"MAC / MTU", fmt.Sprintf("%s · %d", orDash(x.MAC), x.MTU)}}, "")
	var c2 string
	if m := x.Module; m != nil {
		rows := []kvPair{{"Module", strings.TrimSpace(m.Vendor + " " + m.PartNumber + " · S/N " + orDash(m.Serial))}}
		for _, sen := range m.Sensors {
			if len(rows) >= 5 {
				break
			}
			rows = append(rows, kvPair{pad(sen.Name, 9), sensorGaugeLine(t, sen, 12)})
		}
		c2 = kvBlock(t, rows, "")
	} else {
		c2 = kvBlock(t, []kvPair{{"Port", x.PortType}, {"Autoneg", x.Autoneg}, {"Duplex", x.Duplex}, {"Max speed", optI(x.MaxSpeedMbps, "%d Mb/s")}}, "")
	}
	cn := func(v uint64, key string) string {
		s := fmt.Sprint(v)
		if v > 0 {
			s = t.StatusBold(model.StatusWarn, s)
		}
		for _, c := range t.sess.counters(func(k string) bool { return k == "net."+x.Name+"."+key }) {
			if c.Last > 0 {
				s += " " + t.StatusText(model.StatusWarn, fmt.Sprintf("↑ +%d", c.Last))
			} else if c.SinceStart > 0 {
				s += " " + t.StatusText(model.StatusWarn, fmt.Sprintf("+%d", c.SinceStart))
			}
		}
		return s
	}
	c := x.Counters
	c3 := kvBlock(t, []kvPair{{"rx CRC", cn(c.RxCRC, "rx_crc_errors")}, {"rx errors", cn(c.RxErrors, "rx_errors")},
		{"rx dropped", cn(c.RxDropped, "rx_dropped")}, {"carrier chg", optU(x.CarrierChanges)},
		{"rx", spark(t.sess.history("network/"+x.Name+"/rx"), 16, 0, t.Series[0])}}, "")
	if n.w < 100 {
		return head + "\n" + c1
	}
	return head + "\n" + kvColumns(n.w, c1, c2, c3)
}

// Detail is the full page of the selected interface.
func (n *networkTab) Detail(t *Theme, w int) (string, string) {
	it, ok := n.list.current()
	if !ok || n.net == nil {
		return "", ""
	}
	x, _ := n.nic(it.key)
	doc := newDoc(t, w)
	doc.section("Identity", t.Badge(x.Status))
	vlan := ""
	if x.VLANID != nil {
		vlan = fmt.Sprint(*x.VLANID)
	}
	drv := x.Driver
	if x.DriverVersion != "" {
		drv += " " + x.DriverVersion
	}
	pci := ""
	if x.PCIAddress != "" {
		pci = fmt.Sprintf("%s · NUMA node %d", x.PCIAddress, x.NUMANode)
	}
	doc.kv([2]string{"Kind", x.Kind}, [2]string{"Adapter", strings.TrimSpace(x.Vendor + " " + x.Model)}, [2]string{"Serial", x.Serial},
		[2]string{"Part number", x.PartNumber}, [2]string{"Driver", drv}, [2]string{"Firmware", x.Firmware}, [2]string{"PCI", pci},
		[2]string{"MAC", x.MAC}, [2]string{"MTU", fmt.Sprint(x.MTU)}, [2]string{"Master", x.Master},
		[2]string{"Members", strings.Join(x.Members, ", ")}, [2]string{"VLAN ID", vlan})

	doc.section("Link", "")
	carrier := ""
	if x.Carrier != nil {
		carrier = map[bool]string{true: t.StatusText(model.StatusOK, "yes"), false: t.StatusText(model.StatusCrit, "no")}[*x.Carrier]
	}
	speed := ""
	if x.SpeedMbps != nil {
		speed = mbpsText(*x.SpeedMbps)
		if x.MaxSpeedMbps != nil && *x.MaxSpeedMbps > *x.SpeedMbps {
			speed += t.StatusText(model.StatusWarn, " (port supports "+mbpsText(*x.MaxSpeedMbps)+")")
		}
	}
	admin := map[bool]string{true: "up", false: "down"}[x.AdminUp]
	doc.kv([2]string{"Admin / oper", admin + " / " + x.OperState}, [2]string{"Carrier", carrier}, [2]string{"Speed", speed},
		[2]string{"Duplex", x.Duplex}, [2]string{"Autoneg", x.Autoneg}, [2]string{"Port", x.PortType},
		[2]string{"Carrier changes", optU(x.CarrierChanges)}, [2]string{"Addresses", strings.Join(x.Addresses, ", ")})
	if l := x.LLDP; l != nil {
		doc.kv([2]string{"LLDP neighbor", l.System + " port " + l.Port}, [2]string{"Port description", l.PortDescr}, [2]string{"Neighbor VLAN", l.VLAN})
	}
	if x.RxBps != nil || x.TxBps != nil {
		doc.section("Traffic", t.Dim.Render("bits/s"))
		doc.add(pad("rx "+bps(x.RxBps), 14) + t.Message.Render(sparkline(t.sess.history("network/"+x.Name+"/rx"), 40)))
		doc.add(pad("tx "+bps(x.TxBps), 14) + t.Message.Render(sparkline(t.sess.history("network/"+x.Name+"/tx"), 40)))
	}
	if b := x.Bond; b != nil {
		doc.section("Bond", t.Dim.Render(b.Mode))
		doc.kv([2]string{"MII status", b.MIIStatus}, [2]string{"Active slave", b.ActiveSlave})
		for _, sl := range b.Slaves {
			st := model.StatusOK
			if sl.MIIStatus != "up" {
				st = model.StatusCrit
			}
			doc.add(t.Dot(st) + " " + pad(sl.Name, 14) + t.StatusText(st, pad(sl.MIIStatus, 6)) +
				t.Dim.Render(fmt.Sprintf("  %s %s · %d link failures · aggregator %s", sl.Speed, sl.Duplex, sl.LinkFailures, orDash(sl.AggregatorID))))
		}
	}
	if m := x.Module; m != nil {
		doc.section("Transceiver", t.Badge(m.Status))
		doc.kv([2]string{"Type", strings.TrimSpace(m.Identifier + " " + m.Type)}, [2]string{"Vendor", m.Vendor},
			[2]string{"Part number", m.PartNumber}, [2]string{"Serial", m.Serial}, [2]string{"Wavelength", m.Wavelength})
		for _, s := range m.Sensors {
			doc.sensor(s, nil)
		}
	}
	if x.TempCelsius != nil {
		doc.section("Sensors", "")
		doc.sensor(model.Sensor{Name: "Adapter temperature", Value: x.TempCelsius, Unit: model.UnitCelsius, Status: model.StatusOK}, nil)
	}
	c := x.Counters
	doc.section("Statistics", t.Dim.Render("since boot"))
	doc.kv([2]string{"Rx", fmt.Sprintf("%s · %d packets · %d errors · %d dropped · %d CRC", bytesGB(c.RxBytes), c.RxPackets, c.RxErrors, c.RxDropped, c.RxCRC)},
		[2]string{"Tx", fmt.Sprintf("%s · %d packets · %d errors · %d dropped", bytesGB(c.TxBytes), c.TxPackets, c.TxErrors, c.TxDropped)})
	if len(x.ErrorCounters) > 0 {
		doc.section("Driver error counters", t.Dim.Render("non-zero ethtool -S counters"))
		names := make([]string, 0, len(x.ErrorCounters))
		for k := range x.ErrorCounters {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			doc.add(t.StatusText(model.StatusWarn, pad(k, 34)) + fmt.Sprint(x.ErrorCounters[k]))
		}
	}
	lossSection(doc, n.net, &x)
	doc.counters(t.sess.counters(func(k string) bool { return strings.HasPrefix(k, "net."+x.Name+".") }), "net."+x.Name+".")
	doc.findings(model.ModNetwork, relatedFindings(n.net.Findings, x.Name))
	return "Interface " + x.Name, doc.String()
}

// lossSection explains the interface's drop and loss counters: how many,
// how many this session, what share of the traffic, where they are lost,
// the likely cause here, and the commands to look closer.
func lossSection(doc *doc, net *model.Network, x *model.NIC) {
	t := doc.t
	c := x.Counters
	type ctr struct {
		key, label string
		total      uint64
		packets    uint64
	}
	ctrs := []ctr{{"rx_missed", "lost in the NIC (rx_missed)", c.RxMissed, c.RxPackets},
		{"rx_dropped", "dropped on receive (rx_dropped)", c.RxDropped, c.RxPackets},
		{"tx_dropped", "dropped on send (tx_dropped)", c.TxDropped, c.TxPackets}}
	any := false
	for _, k := range ctrs {
		any = any || k.total > 0
	}
	if !any {
		return
	}
	growth := map[string]uint64{}
	for _, ch := range t.sess.counters(func(k string) bool { return strings.HasPrefix(k, "net."+x.Name+".") || k == "net.softnet.dropped" }) {
		growth[strings.TrimPrefix(ch.Key, "net."+x.Name+".")] = ch.SinceStart
	}
	softnetRising := growth["net.softnet.dropped"] > 0
	doc.section("Drops and losses", t.Dim.Render("since boot · this session · where · why"))
	for _, k := range ctrs {
		if k.total == 0 {
			continue
		}
		where, cause, benign := net.LossCause(x, k.key, softnetRising)
		st := model.StatusOK
		if g := growth[k.key]; g > 0 && !benign && k.key != "rx_dropped" {
			st = model.StatusWarn
		}
		share := float64(k.total) / float64(k.packets+k.total) * 100
		head := t.Dot(st) + " " + t.Bold.Render(pad(k.label, 32)) + padCell(grouped(k.total), 12, true) +
			t.Dim.Render(fmt.Sprintf("  %.3f %% of the packets", share))
		if g := growth[k.key]; g > 0 {
			head += "  " + t.StatusText(model.Worst(st, model.StatusWarn), fmt.Sprintf("+%d this session", g))
		}
		doc.add(head)
		doc.add(hang("     "+t.Dim.Render(pad("where", 7)), where, doc.w-1))
		doc.add(hang("     "+t.Dim.Render(pad("why", 7)), cause, doc.w-1))
	}
	if x.RingRX != nil {
		r := fmt.Sprintf("%d entries", *x.RingRX)
		if x.RingRXMax != nil {
			r += fmt.Sprintf(" of %d", *x.RingRXMax)
		}
		if x.RxQueues != nil {
			r += fmt.Sprintf(" · %d receive queues", *x.RxQueues)
		}
		doc.kv([2]string{"Receive ring", r})
	}
	if net.SoftnetDropped != nil {
		doc.kv([2]string{"Kernel backlog", fmt.Sprintf("%s dropped since boot (all CPUs)", grouped(*net.SoftnetDropped))})
	}
	cmds := []string{"ethtool -S " + x.Name + " | grep -iE 'drop|miss|discard|no_buf|fifo'   # which queue or reason"}
	if x.Kind == "physical" {
		cmds = append(cmds, "ethtool -g "+x.Name+"   # ring size; a larger ring: ethtool -G "+x.Name+" rx <max> (may reset the link briefly)",
			"ethtool -l "+x.Name+"   # receive queues (RSS)")
	}
	cmds = append(cmds, "awk '{print NR-1, strtonum(\"0x\"$2)}' /proc/net/softnet_stat   # backlog drops per CPU")
	if x.Kind == "bond" || x.Master != "" {
		b := x.Name
		if x.Master != "" {
			b = x.Master
		}
		cmds = append(cmds, "cat /proc/net/bonding/"+b+"   # mode, active member")
	}
	for i, cmd := range cmds {
		label := ""
		if i == 0 {
			label = "Check"
		}
		doc.add(hang("   "+t.Dim.Render(pad(label, 8)), cmd, doc.w-1, t.Key.Render))
	}
}
