package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
)

// hwNode is one node of the hardware topology tree. module+subject say
// where enter jumps to (the module tab focusing that component).
type hwNode struct {
	label    string
	info     string
	status   model.Status
	module   model.ModuleID
	subject  string
	serial   string // the fixed Serial column; "c" copies it
	children []*hwNode
	path     string
}

func (n *hwNode) add(c *hwNode) *hwNode {
	n.children = append(n.children, c)
	return c
}

// rollup sets every node's status to the worst of itself and its subtree.
func (n *hwNode) rollup() model.Status {
	for _, c := range n.children {
		n.status = model.Worst(n.status, c.rollup())
	}
	return n.status
}

func (n *hwNode) setPaths(prefix string) {
	n.path = prefix + "/" + n.label
	for _, c := range n.children {
		c.setPaths(n.path)
	}
}

// buildTree arranges the snapshot as system → board/BMC/PSUs, CPU sockets
// → cores, memory sockets → channels → DIMMs, storage controllers →
// arrays/drives, network ports, accelerators.
func buildTree(s *model.Snapshot) *hwNode {
	root := &hwNode{label: s.Hostname, module: model.ModSystem}
	if x := s.System; x != nil {
		root.info, root.serial = productName(x), x.Serial
		root.status = x.Status
		plat := root.add(&hwNode{label: "Platform", status: model.StatusOK})
		if x.BoardProduct != "" || x.BoardVendor != "" {
			plat.add(&hwNode{label: "Mainboard", info: strings.TrimSpace(x.BoardVendor + " " + x.BoardProduct), serial: x.BoardSerial})
		}
		if x.BIOSVersion != "" {
			plat.add(&hwNode{label: "BIOS", info: strings.TrimSpace(x.BIOSVendor + " " + x.BIOSVersion + " " + x.BIOSDate)})
		}
		if x.BMCVendor != "" || x.BMCFirmware != "" {
			plat.add(&hwNode{label: "BMC", info: strings.TrimSpace(x.BMCVendor + " fw " + x.BMCFirmware)})
		}
		for _, p := range x.PSUs {
			st := model.StatusOK
			if !p.Present {
				st = model.StatusUnknown
			}
			plat.add(&hwNode{label: orDash(p.Location), info: strings.TrimSpace(p.Manufacturer + " " + p.Model), serial: p.Serial, status: st})
		}
		if len(x.Fans) > 0 {
			fans := plat.add(&hwNode{label: fmt.Sprintf("Fans (%d)", len(x.Fans)), module: model.ModSystem})
			for _, f := range x.Fans {
				fans.add(&hwNode{label: f.Name, info: optF(f.Value, "%.0f rpm"), status: f.Status, module: model.ModSystem})
			}
		}
	}
	if c := s.CPU; c != nil {
		cpu := root.add(&hwNode{label: "Processors", info: countLabel(c.SocketCount, "socket", "sockets") + fmt.Sprintf(" · %d cores · %d threads", c.CoreCount, c.ThreadCount), status: c.Status, module: model.ModCPU})
		bySock := map[int][]model.LogicalCPU{}
		var ids []int
		for _, l := range c.Logical {
			if _, ok := bySock[l.SocketID]; !ok {
				ids = append(ids, l.SocketID)
			}
			bySock[l.SocketID] = append(bySock[l.SocketID], l)
		}
		sort.Ints(ids)
		for _, id := range ids {
			label := fmt.Sprintf("Socket %d", id)
			info, sn := c.Model, ""
			for _, s := range c.Sockets {
				if s.ID == id {
					if s.Designation != "" {
						label = s.Designation
					}
					if s.PPIN != "" {
						info += " · PPIN " + s.PPIN
					}
					sn = s.Serial
				}
			}
			if id < 0 {
				label = "offline CPUs"
			}
			sock := cpu.add(&hwNode{label: label, info: info, serial: sn, module: model.ModCPU})
			for _, p := range c.PackageTemps {
				if strings.HasPrefix(p.Name, fmt.Sprintf("CPU%d ", id)) && p.Value != nil {
					sock.add(&hwNode{label: p.Name, info: optF(p.Value, "%.0f°C"), status: p.Status, module: model.ModCPU, subject: p.Name})
				}
			}
			cores := map[int][]model.LogicalCPU{}
			var coreIDs []int
			for _, l := range bySock[id] {
				if _, ok := cores[l.CoreID]; !ok {
					coreIDs = append(coreIDs, l.CoreID)
				}
				cores[l.CoreID] = append(cores[l.CoreID], l)
			}
			sort.Ints(coreIDs)
			for _, cid := range coreIDs {
				ths := cores[cid]
				var names []string
				st := model.StatusOK
				for _, l := range ths {
					names = append(names, cpuKey(l.ID))
					st = model.Worst(st, l.Status)
				}
				label := fmt.Sprintf("Core %d", cid)
				if cid < 0 {
					label = "unknown core"
				}
				info := strings.Join(names, " ")
				if t := ths[0].CoreType; t != "" {
					info += " · " + coreTypeName(t)
				}
				core := sock.add(&hwNode{label: label, info: info, status: st, module: model.ModCPU, subject: names[0]})
				for _, l := range ths {
					state := "online"
					if !l.Online {
						state = "OFFLINE"
					}
					core.add(&hwNode{label: cpuKey(l.ID), info: fmt.Sprintf("%s · %s · %s", state, optF(l.FreqMHz, "%.0f MHz"), optF(l.TempCelsius, "%.0f°C")),
						status: l.Status, module: model.ModCPU, subject: cpuKey(l.ID)})
				}
			}
		}
	}
	if m := s.Memory; m != nil {
		info := bytesGB(m.TotalBytes) + " usable"
		if m.InstalledBytes != nil {
			info = fmt.Sprintf("%s installed in %d/%d slots", bytesGB(*m.InstalledBytes), m.PopulatedSlots, m.Slots)
		}
		mem := root.add(&hwNode{label: "Memory", info: info, status: m.Status, module: model.ModMemory})
		groups := map[string]*hwNode{}
		chans := map[string]*hwNode{}
		for _, d := range m.DIMMs {
			sock, ch := slotGroup(d)
			parent := mem
			if sock != "" {
				if groups[sock] == nil {
					groups[sock] = mem.add(&hwNode{label: sock, module: model.ModMemory})
				}
				parent = groups[sock]
			}
			if ch != "" {
				k := sock + "/" + ch
				if chans[k] == nil {
					chans[k] = parent.add(&hwNode{label: "Channel " + ch, module: model.ModMemory})
				}
				parent = chans[k]
			}
			info := "empty"
			if d.Populated {
				info = strings.TrimSpace(fmt.Sprintf("%s %s %s %s", bytesGB(d.SizeBytes), d.Type, d.Manufacturer, d.PartNumber))
			}
			parent.add(&hwNode{label: d.Locator, info: info, serial: d.Serial, status: d.Status, module: model.ModMemory, subject: d.Locator})
		}
	}
	if st := s.Storage; st != nil {
		sto := root.add(&hwNode{label: "Storage", info: fmt.Sprintf("%d disks", len(st.Disks)), status: st.Status, module: model.ModStorage})
		for _, c := range st.Controllers {
			cn := sto.add(&hwNode{label: fmt.Sprintf("RAID controller %d", c.Index), info: c.Model, serial: c.Serial, status: c.Status, module: model.ModStorage})
			for _, v := range c.VDs {
				vd := cn.add(&hwNode{label: "VD " + v.ID, info: v.Level + " " + v.State + " " + v.SizeText, status: v.Status, module: model.ModStorage, subject: "VD " + v.ID})
				for _, p := range c.PDs {
					if strings.HasPrefix(v.ID, p.Group+"/") {
						vd.add(&hwNode{label: "PD " + p.Slot, info: strings.TrimSpace(p.State + " " + p.Model), serial: p.Serial, status: p.Status, module: model.ModStorage, subject: "PD " + p.Slot})
					}
				}
			}
			for _, p := range c.PDs {
				inVD := false
				for _, v := range c.VDs {
					inVD = inVD || strings.HasPrefix(v.ID, p.Group+"/")
				}
				if !inVD {
					cn.add(&hwNode{label: "PD " + p.Slot, info: strings.TrimSpace(p.State + " " + p.Model), serial: p.Serial, status: p.Status, module: model.ModStorage, subject: "PD " + p.Slot})
				}
			}
			if b := c.Battery; b != nil {
				cn.add(&hwNode{label: b.Type, info: b.State, status: b.Status, module: model.ModStorage})
			}
		}
		for _, a := range st.MDArrays {
			md := sto.add(&hwNode{label: a.Name, info: fmt.Sprintf("%s %s %d/%d", a.Level, a.State, a.ActiveDevices, a.Devices), status: a.Status, module: model.ModStorage, subject: a.Name})
			for _, mb := range a.Members {
				ms := model.StatusOK
				if mb.Failed {
					ms = model.StatusCrit
				}
				md.add(&hwNode{label: mb.Device, info: map[bool]string{true: "FAILED", false: "member"}[mb.Failed], status: ms, module: model.ModStorage, subject: diskOf(mb.Device, st)})
			}
		}
		for _, d := range st.Disks {
			sto.add(&hwNode{label: d.Name, info: strings.TrimSpace(fmt.Sprintf("%s %s %s", strings.ToUpper(d.Kind), d.Model, bytesSI(d.SizeBytes))), serial: d.Serial,
				status: d.Status, module: model.ModStorage, subject: d.Name})
		}
	}
	if n := s.Network; n != nil {
		net := root.add(&hwNode{label: "Network", info: fmt.Sprintf("%d interfaces", len(n.Interfaces)), status: n.Status, module: model.ModNetwork})
		nodes := map[string]*hwNode{}
		for _, x := range n.Interfaces {
			info := x.Kind
			if x.Model != "" {
				info += " · " + x.Model
			}
			info += " · " + linkText(x)
			nodes[x.Name] = &hwNode{label: x.Name, info: info, serial: x.Serial, status: x.Status, module: model.ModNetwork, subject: x.Name}
			if m := x.Module; m != nil {
				nodes[x.Name].add(&hwNode{label: "transceiver", info: strings.TrimSpace(m.Identifier + " " + m.Vendor + " " + m.PartNumber), serial: m.Serial, status: m.Status, module: model.ModNetwork, subject: x.Name})
			}
		}
		for _, x := range n.Interfaces {
			parent := net
			if x.Master != "" && nodes[x.Master] != nil {
				parent = nodes[x.Master]
			} else if x.Kind == "vlan" && len(x.Members) > 0 && nodes[x.Members[0]] != nil {
				parent = nodes[x.Members[0]]
			}
			parent.add(nodes[x.Name])
		}
	}
	if a := s.Accel; a != nil && len(a.GPUs)+len(a.NPUs) > 0 {
		acc := root.add(&hwNode{label: "GPU/NPU", status: a.Status, module: model.ModAccel})
		for _, list := range [][]model.Accelerator{a.GPUs, a.NPUs} {
			for _, x := range list {
				acc.add(&hwNode{label: accelName(x), info: strings.TrimSpace(x.Model + " " + shortPCI(x.PCIAddress)), serial: x.Serial, status: x.Status, module: model.ModAccel, subject: x.PCIAddress})
			}
		}
	}
	root.rollup()
	root.setPaths("")
	return root
}

// diskOf maps an md member partition ("sdd1") to its disk ("sdd").
func diskOf(dev string, st *model.Storage) string {
	best := dev
	for _, d := range st.Disks {
		if strings.HasPrefix(dev, d.Name) && len(d.Name) <= len(dev) && (best == dev || len(d.Name) > len(best)) {
			best = d.Name
		}
	}
	return best
}

// treeRow is one visible line of the flattened tree.
type treeRow struct {
	node   *hwNode
	prefix string // "│  ├─ "
	hasKid bool
	open   bool
	fold   bool // a folded run of healthy siblings
}

// foldMin is the shortest run of healthy siblings folded into one row.
const foldMin = 3

// flatten lists visible nodes. A node is open if the user opened it, or
// by default when it is the root, a top-level group, or has a problem.
// Runs of at least foldMin consecutive healthy siblings without problems
// fold into one range row ("Core 0 – Core 11  12 healthy") unless the user
// opened that fold.
func flatten(root *hwNode, opened map[string]bool) []treeRow {
	var out []treeRow
	isOpen := func(n *hwNode, depth int) bool {
		if v, ok := opened[n.path]; ok {
			return v
		}
		return depth == 0 || n.status >= model.StatusWarn
	}
	var walk func(n *hwNode, prefix, childPrefix string, depth int)
	walk = func(n *hwNode, prefix, childPrefix string, depth int) {
		open := isOpen(n, depth)
		out = append(out, treeRow{node: n, prefix: prefix, hasKid: len(n.children) > 0, open: open})
		if !open {
			return
		}
		kids := n.children
		for i := 0; i < len(kids); {
			j := i
			for j < len(kids) && kids[j].status == model.StatusOK && countProblems(kids[j]) == 0 {
				j++
			}
			if j-i >= foldMin && depth > 0 {
				fold := &hwNode{label: foldLabel(kids[i].label, kids[j-1].label), status: model.StatusOK,
					info: fmt.Sprintf("%d healthy", j-i), children: kids[i:j], path: n.path + "/~" + kids[i].label}
				last := j == len(kids)
				p, cp := "├─ ", "│  "
				if last {
					p, cp = "└─ ", "   "
				}
				if opened[fold.path] {
					out = append(out, treeRow{node: fold, prefix: childPrefix + p, hasKid: true, open: true, fold: true})
					for k := i; k < j; k++ {
						kl := k == j-1
						kp, kcp := "├─ ", "│  "
						if kl {
							kp, kcp = "└─ ", "   "
						}
						walk(kids[k], childPrefix+cp+kp, childPrefix+cp+kcp, depth+2)
					}
				} else {
					out = append(out, treeRow{node: fold, prefix: childPrefix + p, hasKid: true, open: false, fold: true})
				}
				i = j
				continue
			}
			last := i == len(kids)-1
			p, cp := "├─ ", "│  "
			if last {
				p, cp = "└─ ", "   "
			}
			walk(kids[i], childPrefix+p, childPrefix+cp, depth+1)
			i++
		}
	}
	walk(root, "", "", 0)
	return out
}

// treeLabel renders a tree row's label cell: guides subtle, ▾/▸ accent,
// the rolled-up status glyph, the name (bold when it has a problem).
func treeLabel(t *Theme, r treeRow) string {
	toggle := ""
	if r.hasKid {
		toggle = lipgloss.NewStyle().Foreground(t.Accent).Render(map[bool]string{true: "▾", false: "▸"}[r.open]) + " "
	}
	name := r.node.label
	if r.node.status >= model.StatusWarn {
		name = t.Bold.Render(name)
	}
	return t.Sub.Render(r.prefix) + toggle + t.Dot(r.node.status) + " " + name
}

// treeInfo renders the row's details; closed branches add "(N problems
// inside)" in the colour of their worst child.
func treeInfo(t *Theme, r treeRow) string {
	info := t.Dim.Render(r.node.info)
	if r.hasKid && !r.open {
		if n := countProblems(r.node); n > 0 {
			info += "  " + t.StatusText(r.node.status, fmt.Sprintf("(%d %s inside)", n, plural(n, "problem", "problems")))
		}
	}
	return info
}

// foldLabel names a run of folded siblings: "Core 0–11" when both ends
// share the word before the number, else "A – B".
func foldLabel(first, last string) string {
	fi, li := strings.LastIndex(first, " "), strings.LastIndex(last, " ")
	if fi > 0 && li > 0 && first[:fi] == last[:li] {
		return first + "–" + last[li+1:]
	}
	return first + " – " + last
}
