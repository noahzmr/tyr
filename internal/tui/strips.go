package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/model"
)

// stripper is implemented by tabs: the status line on the first content
// row (canvas: "Verdict strip on every screen"): badge, the key facts in
// bold, details muted, the view switch on the right.
type stripper interface {
	Strip(t *Theme, w int) string
}

// stripOwner is a tab that, in some states, draws its own title and
// banner on the strip row (stress live view and result).
type stripOwner interface {
	OwnsStrip() bool
}

// rightPart joins the filter note and the view switch for the strip.
func rightPart(t *Theme, p *prefs, shown, total int, views []string, active int) string {
	var parts []string
	if note := filterNote(t, p, shown, total); note != "" {
		parts = append(parts, note)
	}
	if len(views) > 0 {
		parts = append(parts, viewSwitch(t, views, active))
	}
	return strings.Join(parts, "  ")
}

// unavailableStrip is the strip of a module without data.
func unavailableStrip(t *Theme, name, reason string, w int) string {
	return truncate(t.Unavail(name, reason), w)
}

func (c *cpuTab) Strip(t *Theme, w int) string {
	x := c.cpu
	if x == nil {
		return unavailableStrip(t, "CPU", "not collected", w)
	}
	online := 0
	for _, l := range x.Logical {
		if l.Online {
			online++
		}
	}
	bold := fmt.Sprintf("%d logical CPUs · %d/%d online", len(x.Logical), online, len(x.Logical))
	if x.SocketCount > 0 && x.Model != "" {
		bold = fmt.Sprintf("%d× %s · %d/%d online", x.SocketCount, shortCPUModel(x.Model), online, len(x.Logical))
	}
	rest := ""
	if x.MaxTempC != nil {
		rest = "hottest " + check.FmtTemp(*x.MaxTempC)
		for _, p := range x.PackageTemps {
			if p.Value != nil && *p.Value == *x.MaxTempC && p.Thresholds != nil && p.Thresholds.UpperCrit != nil {
				rest += fmt.Sprintf(" (crit %.0f)", *p.Thresholds.UpperCrit)
			}
		}
	}
	right := rightPart(t, c.list.prefs, len(c.list.shown), len(c.list.all), []string{"Grid", "Cores", "Details"}, c.view)
	if c.view != 1 {
		right = viewSwitch(t, []string{"Grid", "Cores", "Details"}, c.view)
	}
	return statusStrip(t, x.Status, bold, rest, right, w)
}

// shortCPUModel trims vendor noise: "Intel(R) Xeon(R) Silver 4314 CPU @
// 2.40GHz" → "Intel Xeon Silver 4314".
func shortCPUModel(s string) string {
	s = strings.NewReplacer("(R)", "", "(TM)", "", "(tm)", "").Replace(s)
	if i := strings.Index(s, " @ "); i > 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), " CPU")
	return strings.Join(strings.Fields(s), " ")
}

func (m *memoryTab) Strip(t *Theme, w int) string {
	x := m.mem
	if x == nil {
		return unavailableStrip(t, "Memory", "not collected", w)
	}
	var parts []string
	if x.InstalledBytes != nil {
		parts = append(parts, bytesGB(*x.InstalledBytes)+" installed")
	}
	if x.OnlineBytes != nil {
		parts = append(parts, bytesGB(*x.OnlineBytes)+" visible")
	} else {
		parts = append(parts, bytesGB(x.TotalBytes)+" usable")
	}
	if x.Slots > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d slots", x.PopulatedSlots, x.Slots))
	}
	rest := ""
	if x.ECCType != "" {
		rest = x.ECCType
		if !strings.Contains(strings.ToUpper(rest), "ECC") {
			rest = "ECC " + rest
		}
	}
	right := rightPart(t, m.list.prefs, len(m.list.shown), len(m.list.all), []string{"Table", "Slot map"}, m.view)
	return statusStrip(t, x.Status, strings.Join(parts, " · "), rest, right, w)
}

func (s *storageTab) Strip(t *Theme, w int) string {
	x := s.st
	if x == nil {
		return unavailableStrip(t, "Storage", "not collected", w)
	}
	var total uint64
	local, remote, down := 0, 0, 0
	for _, d := range x.Disks {
		if !d.Local() {
			remote++
			if r := d.Remote; r.State != "" && r.State != "LOGGED_IN" && r.State != "Online" {
				down++
			}
			continue
		}
		local++
		total += d.SizeBytes
	}
	bold := fmt.Sprintf("%s · %s", countLabel(local, "disk", "disks"), bytesSI(total))
	var rest []string
	if remote > 0 {
		v := countLabel(remote, "network volume", "network volumes")
		if down > 0 {
			v += fmt.Sprintf(" (%d session down)", down)
		}
		rest = append(rest, v)
	}
	for _, a := range x.MDArrays {
		if a.Degraded || a.Status >= model.StatusWarn {
			rest = append(rest, a.Name+" "+map[bool]string{true: "degraded", false: strings.ToLower(a.State)}[a.Degraded])
		}
	}
	if n := len(x.Controllers); n > 0 {
		rest = append(rest, countLabel(n, "RAID controller", "RAID controllers"))
	}
	rest = append(rest, countLabel(len(x.Filesystems), "filesystem", "filesystems"))
	l := s.lists[s.view]
	right := rightPart(t, l.prefs, len(l.shown), len(l.all), []string{"Disks", "RAID", "Filesystems"}, s.view)
	return statusStrip(t, x.Status, bold, strings.Join(rest, " · "), right, w)
}

func (n *networkTab) Strip(t *Theme, w int) string {
	x := n.net
	if x == nil {
		return unavailableStrip(t, "Network", "not collected", w)
	}
	ports, up := 0, 0
	var extra []string
	for _, i := range x.Interfaces {
		if i.Kind == "physical" {
			ports++
			if i.Carrier != nil && *i.Carrier {
				up++
			}
		}
		if b := i.Bond; b != nil {
			on := 0
			for _, s := range b.Slaves {
				if s.MIIStatus == "up" {
					on++
				}
			}
			extra = append(extra, fmt.Sprintf("%s %d/%d", i.Name, on, len(b.Slaves)))
		}
	}
	bold := fmt.Sprintf("%s · %d up", countLabel(ports, "physical port", "physical ports"), up)
	if b := x.BMC; b != nil && b.IP != "" {
		extra = append(extra, "BMC "+b.IP)
	}
	if x.Hidden > 0 {
		extra = append(extra, fmt.Sprintf("%d virtual hidden", x.Hidden))
	}
	right := filterNote(t, n.list.prefs, len(n.list.shown), len(n.list.all))
	return statusStrip(t, x.Status, bold, strings.Join(extra, " · "), right, w)
}

func (a *accelTab) Strip(t *Theme, w int) string {
	x := a.acc
	if x == nil {
		return unavailableStrip(t, "GPU/NPU", "not collected", w)
	}
	bold := fmt.Sprintf("%s · %s", countLabel(len(x.GPUs), "GPU", "GPUs"), countLabel(len(x.NPUs), "NPU", "NPUs"))
	var rest []string
	drivers := map[string]bool{}
	for _, list := range [][]model.Accelerator{x.GPUs, x.NPUs} {
		for _, d := range list {
			if d.Driver != "" && !drivers[d.Driver] {
				drivers[d.Driver] = true
				v := d.Driver
				if d.DriverVersion != "" {
					v += " " + d.DriverVersion
				}
				rest = append(rest, v)
			}
		}
	}
	if n := len(x.BMCDisplays); n > 0 {
		rest = append(rest, countLabel(n, "BMC display adapter", "BMC display adapters")+" not counted")
	}
	right := filterNote(t, a.list.prefs, len(a.list.shown), len(a.list.all))
	return statusStrip(t, x.Status, bold, strings.Join(rest, " · "), right, w)
}

func (v *inventoryTab) Strip(t *Theme, w int) string {
	if v.snap == nil {
		return ""
	}
	problems := 0
	for _, c := range v.snap.Inventory {
		if c.Status >= model.StatusWarn {
			problems++
		}
	}
	bold := fmt.Sprintf("%d components · %d with problems", len(v.snap.Inventory), problems)
	rest := "serials visible"
	if !v.snap.Privileged {
		rest = "serials need root (sudo tyr)"
	}
	l := v.list
	if v.view == 1 {
		l = v.tlist
	}
	right := rightPart(t, l.prefs, len(l.shown), len(l.all), []string{"List", "Tree"}, v.view)
	return statusStrip(t, v.snap.Status, bold, rest, right, w)
}

// stripLine pads a strip to the full width.
func stripLine(s string, w int) string {
	if lipgloss.Width(s) > w {
		return truncate(s, w)
	}
	return s
}
