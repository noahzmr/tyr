package report

import (
	"fmt"
	"strings"

	"github.com/noahzmr/tyr/internal/model"
)

// healthGroup counts one kind of component by status and names the ones
// with problems.
type healthGroup struct {
	name     string
	total    int
	count    map[model.Status]int
	problems []string
}

func (g *healthGroup) add(name string, st model.Status) {
	g.total++
	g.count[st]++
	if st != model.StatusOK {
		g.problems = append(g.problems, name+" "+statusSym(st))
	}
}

func statusSym(s model.Status) string {
	return map[model.Status]string{model.StatusOK: "●", model.StatusWarn: "▲", model.StatusCrit: "✖", model.StatusUnknown: "?"}[s]
}

// psuStatus reads the SMBIOS type 39 status text ("Present, OK").
func psuStatus(p model.PSUFRU) model.Status {
	low := strings.ToLower(p.StatusText)
	switch {
	case !p.Present:
		return model.StatusUnknown
	case strings.Contains(low, "critical") || strings.Contains(low, "fail") || strings.Contains(low, "lost"):
		return model.StatusCrit
	case strings.Contains(low, "non-critical") || strings.Contains(low, "degraded"):
		return model.StatusWarn
	case strings.Contains(low, "ok"):
		return model.StatusOK
	}
	return model.StatusUnknown
}

// componentHealth groups every component of a snapshot by kind.
func componentHealth(s *model.Snapshot) []*healthGroup {
	var groups []*healthGroup
	group := func(name string) *healthGroup {
		g := &healthGroup{name: name, count: map[model.Status]int{}}
		groups = append(groups, g)
		return g
	}
	if c := s.CPU; c != nil {
		g := group("CPU cores (logical)")
		for _, l := range c.Logical {
			g.add(fmt.Sprintf("cpu%d", l.ID), l.Status)
		}
		g = group("CPU packages")
		for _, p := range c.PackageTemps {
			g.add(p.Name, p.Status)
		}
	}
	if m := s.Memory; m != nil {
		g := group("Memory modules")
		for _, d := range m.DIMMs {
			if d.Populated {
				g.add(d.Locator, d.Status)
			}
		}
	}
	if st := s.Storage; st != nil {
		g := group("Disks")
		for _, d := range st.Disks {
			g.add(d.Name, d.Status)
		}
		if len(st.MDArrays) > 0 {
			g = group("Software RAID arrays")
			for _, a := range st.MDArrays {
				g.add(a.Name, a.Status)
			}
		}
		if len(st.Controllers) > 0 {
			gc, gv, gp := group("RAID controllers"), group("RAID virtual drives"), group("RAID physical drives")
			for _, c := range st.Controllers {
				gc.add(fmt.Sprintf("c%d", c.Index), c.Status)
				for _, v := range c.VDs {
					gv.add("VD "+v.ID, v.Status)
				}
				for _, p := range c.PDs {
					gp.add("PD "+p.Slot, p.Status)
				}
			}
		}
		g = group("Filesystems")
		for _, f := range st.Filesystems {
			g.add(f.Mountpoint, f.Status)
		}
	}
	if n := s.Network; n != nil {
		g := group("Network interfaces")
		var xcvrs []model.NIC
		for _, x := range n.Interfaces {
			g.add(x.Name, x.Status)
			if x.Module != nil {
				xcvrs = append(xcvrs, x)
			}
		}
		if len(xcvrs) > 0 {
			g = group("Transceivers")
			for _, x := range xcvrs {
				g.add(x.Name, x.Module.Status)
			}
		}
	}
	if a := s.Accel; a != nil {
		if len(a.GPUs) > 0 {
			g := group("GPUs")
			for _, x := range a.GPUs {
				g.add(x.Model+" "+x.PCIAddress, x.Status)
			}
		}
		if len(a.NPUs) > 0 {
			g := group("NPUs")
			for _, x := range a.NPUs {
				g.add(x.Model+" "+x.PCIAddress, x.Status)
			}
		}
	}
	if y := s.System; y != nil {
		if len(y.PSUs) > 0 {
			g := group("Power supplies")
			for _, p := range y.PSUs {
				g.add(p.Location, psuStatus(p))
			}
		}
		if len(y.Fans) > 0 {
			g := group("Fans")
			for _, f := range y.Fans {
				g.add(f.Name, f.Status)
			}
		}
		if len(y.Temps) > 0 {
			g := group("Chassis temperatures")
			for _, t := range y.Temps {
				g.add(t.Name, t.Status)
			}
		}
	}
	var out []*healthGroup
	for _, g := range groups {
		if g.total > 0 {
			out = append(out, g)
		}
	}
	return out
}

// healthSection writes the component health table.
func healthSection(m *md, s *model.Snapshot, title string) {
	gs := componentHealth(s)
	if len(gs) == 0 {
		return
	}
	m.h(2, title)
	var rows [][]string
	for _, g := range gs {
		n := func(st model.Status) string {
			if g.count[st] == 0 {
				return ""
			}
			return fmt.Sprint(g.count[st])
		}
		worst := model.StatusOK
		for st := range g.count {
			worst = model.Worst(worst, st)
		}
		probs := g.problems
		if len(probs) > 8 {
			probs = append(probs[:8:8], fmt.Sprintf("+%d more", len(g.problems)-8))
		}
		rows = append(rows, []string{g.name, status(worst), fmt.Sprint(g.total), n(model.StatusOK), n(model.StatusWarn), n(model.StatusCrit),
			n(model.StatusUnknown), strings.Join(probs, ", ")})
	}
	m.table([]string{"Component", "Health", ">Total", ">● OK", ">▲ WARN", ">✖ CRIT", ">? UNKNOWN", "With problems"}, rows)
}

// coresTable writes one row per logical CPU.
func coresTable(m *md, c *model.CPU) {
	if len(c.Logical) == 0 {
		return
	}
	m.h(3, "Cores")
	if ct := c.CoreTest; ct != nil {
		m.p("Core test: %d passed · %d failed · %d timed out · %d skipped (%d ms)", ct.Passed, ct.Failed, ct.TimedOut, ct.Skipped, ct.DurationMS)
	}
	var rows [][]string
	for _, l := range c.Logical {
		state := "online"
		if !l.Online {
			state = "offline"
		}
		mce := ""
		if l.MCEExceptions != nil || l.MCELogged != nil {
			var n uint64
			if l.MCEExceptions != nil {
				n += *l.MCEExceptions
			}
			if l.MCELogged != nil {
				n += *l.MCELogged
			}
			mce = fmt.Sprint(n)
		}
		core := ""
		if l.SocketID >= 0 && l.CoreID >= 0 {
			core = fmt.Sprintf("%d / %d", l.SocketID, l.CoreID)
		}
		test := string(l.CoreTest)
		if l.CoreTestDetail != "" {
			test = strings.TrimSpace(test + " " + l.CoreTestDetail)
		}
		rows = append(rows, []string{fmt.Sprintf("cpu%d", l.ID), core, l.CoreType, state, f0(l.FreqMHz, " MHz"), f0(l.UtilizationPercent, " %"),
			f0(l.TempCelsius, " °C"), mce, u(l.ThrottleCount), test, status(l.Status)})
	}
	m.table([]string{"CPU", "Socket / core", "Type", "State", ">Clock", ">Load", ">Temp", ">MCE", ">Throttled", "Core test", "Status"}, rows)
}
