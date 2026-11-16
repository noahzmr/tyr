package report

import (
	"fmt"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/model"
)

// serviceTime renders power-on hours as "5.7 y · 49 716 h" (days below a year).
func serviceTime(h uint64) string {
	switch {
	case h >= 8766:
		return fmt.Sprintf("%.1f y · %s h", float64(h)/8766, groupThousands(h))
	case h >= 48:
		return fmt.Sprintf("%d d · %s h", h/24, groupThousands(h))
	}
	return fmt.Sprintf("%d h", h)
}

func groupThousands(v uint64) string {
	s := fmt.Sprint(v)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + " " + s[i:]
	}
	return s
}

// ageSection is the age and power state of the components: how long they
// have been in service, how often they were power-cycled, when they were
// made, and their power state now - as far as the hardware reports it.
func ageSection(m *md, s *model.Snapshot) {
	var rows [][]string
	add := func(component, kind, service, cycles, made, state, notes string) {
		rows = append(rows, []string{component, kind, service, cycles, made, state, notes})
	}
	if y := s.System; y != nil {
		up, made := "", ""
		if y.UptimeSeconds != nil {
			up = "up " + dur(time.Duration(*y.UptimeSeconds)*time.Second)
		}
		for _, f := range y.FRUs {
			if f.MfgDate != "" {
				made = f.MfgDate + " (board)"
				break
			}
		}
		add(s.Hostname, "system", up, "", made, "running", strings.TrimSpace("BIOS "+y.BIOSVersion+" "+y.BIOSDate))
		for _, f := range y.FRUs {
			if f.MfgDate != "" && !strings.Contains(strings.ToLower(f.Name), "builtin") {
				add(f.Name, "FRU", "", "", f.MfgDate, "", strings.TrimSpace(f.Manufacturer+" "+f.Product))
			}
		}
		for _, p := range y.PSUs {
			state := p.StatusText
			if !p.Present {
				state = "not present"
			}
			add(p.Location, "PSU", "", "", "", state, strings.TrimSpace(p.Manufacturer+" "+p.Model))
		}
	}
	if c := s.CPU; c != nil {
		online := 0
		for _, l := range c.Logical {
			if l.Online {
				online++
			}
		}
		add("CPUs", "cpu", "", "", "", fmt.Sprintf("%d of %d online", online, len(c.Logical)), c.Model)
	}
	if st := s.Storage; st != nil {
		for _, d := range st.Disks {
			if !d.Local() {
				continue
			}
			service, cycles := "", ""
			var notes []string
			if d.SMART == nil {
				notes = append(notes, "no SMART: needs root, or the disk is behind a RAID controller")
			}
			if sm := d.SMART; sm != nil {
				if sm.PowerOnHours != nil {
					service = serviceTime(*sm.PowerOnHours)
				}
				if sm.PowerCycles != nil {
					cycles = groupThousands(*sm.PowerCycles)
				}
				if sm.UnsafeShutdowns != nil && *sm.UnsafeShutdowns > 0 {
					notes = append(notes, fmt.Sprintf("%d unsafe shutdowns", *sm.UnsafeShutdowns))
				}
				if sm.WearPercentUsed != nil {
					notes = append(notes, fmt.Sprintf("wear %.0f %%", *sm.WearPercentUsed))
				}
				if sm.DataWrittenBytes != nil {
					notes = append(notes, bytesSI(*sm.DataWrittenBytes)+" written")
				}
			}
			add(d.Name, d.Kind, service, cycles, "", "", strings.Join(notes, " · "))
		}
	}
	if n := s.Network; n != nil {
		for _, x := range n.Interfaces {
			t := x.Module
			if t == nil {
				continue
			}
			state := "no link"
			if x.Carrier != nil && *x.Carrier {
				state = "link up"
			}
			add(x.Name+" transceiver", "optics", "", "", t.DateCode, state, strings.TrimSpace(t.Vendor+" "+t.PartNumber))
		}
	}
	if a := s.Accel; a != nil {
		for _, list := range [][]model.Accelerator{a.GPUs, a.NPUs} {
			for _, x := range list {
				state := strings.TrimSpace(x.PowerState + " " + x.RuntimeStatus)
				notes := x.Model
				if x.AwakePercent != nil {
					notes += fmt.Sprintf(" · awake %.0f %%", *x.AwakePercent)
				}
				add(x.PCIAddress, x.Kind, "", "", "", state, notes)
			}
		}
	}
	if len(rows) == 0 {
		return
	}
	m.h(2, "Age and power")
	m.table([]string{"Component", "Kind", ">In service", ">Power cycles", "Manufactured", "State", "Notes"}, rows)
	m.p("_In service: power-on time the device counts itself (SMART), or the uptime of the system. Manufactured: board FRU and transceiver " +
		"date codes. Not reported by Linux: when CPUs and DIMMs were made (DIMM SPD data is not read) and the power-on time of CPUs, DIMMs, " +
		"PSUs and fans._")
}
