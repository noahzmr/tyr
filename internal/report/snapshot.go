package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/model"
)

// Snapshot is the hardware and alert report of one snapshot.
func Snapshot(s *model.Snapshot) string {
	var m md
	m.line("# %s · hardware report", esc(s.Hostname))
	m.line("")
	crit, warn := 0, 0
	all := findings(s)
	for _, f := range all {
		switch f.Status {
		case model.StatusCrit:
			crit++
		case model.StatusWarn:
			warn++
		}
	}
	m.p("**Status: %s** · %d critical · %d %s", status(s.Status), crit, warn, plural(warn, "warning", "warnings"))
	priv := "root"
	if !s.Privileged {
		priv = "not root (serials, SMART and the BMC may be missing)"
	}
	m.kv([2]string{"Collected", when(s.Timestamp)}, [2]string{"Tyr", s.TyrVersion}, [2]string{"Privileges", priv},
		[2]string{"Virtualization", s.Virtualization}, [2]string{"Architecture", s.Arch})

	// summary per module
	m.h(2, "Summary")
	var rows [][]string
	for _, id := range append([]model.ModuleID{model.ModSystem}, model.Modules...) {
		sec := s.Section(id)
		if sec == nil {
			rows = append(rows, []string{moduleNames[id], "○ not collected", "", ""})
			continue
		}
		h := sec.Head()
		c, w := 0, 0
		for _, f := range h.Findings {
			switch f.Status {
			case model.StatusCrit:
				c++
			case model.StatusWarn:
				w++
			}
		}
		alerts := ""
		if c+w > 0 {
			alerts = fmt.Sprintf("%d ✖ · %d ▲", c, w)
		}
		rows = append(rows, []string{moduleNames[id], status(h.Status), h.Summary, alerts})
	}
	m.table([]string{"Module", "Status", "Summary", "Alerts"}, rows)

	alertsSection(&m, all)
	healthSection(&m, s, "Component health")
	ageSection(&m, s)
	systemSection(&m, s.System)
	cpuSection(&m, s.CPU)
	memorySection(&m, s.Memory)
	storageSection(&m, s.Storage)
	networkSection(&m, s.Network)
	accelSection(&m, s.Accel)
	inventorySection(&m, s.Inventory)
	sourcesSection(&m, s)
	return m.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func alertsSection(m *md, all []moduleFinding) {
	m.h(2, "Alerts")
	sort.SliceStable(all, func(i, j int) bool { return all[i].Status > all[j].Status })
	var rows [][]string
	for _, f := range all {
		if f.Status == model.StatusOK {
			continue
		}
		rows = append(rows, []string{status(f.Status), moduleNames[f.Module], f.Subject, f.Message, f.Source})
	}
	if len(rows) == 0 {
		m.p("● No alerts.")
		return
	}
	m.table([]string{"Severity", "Module", "Component", "Alert", "Source"}, rows)
}

func systemSection(m *md, x *model.System) {
	if x == nil {
		return
	}
	m.h(2, "System")
	up := ""
	if x.UptimeSeconds != nil {
		up = dur(time.Duration(*x.UptimeSeconds) * time.Second)
	}
	m.kv([2]string{"Product", strings.TrimSpace(x.Vendor + " " + x.Product)}, [2]string{"Serial", x.Serial},
		[2]string{"Board", strings.TrimSpace(x.BoardVendor + " " + x.BoardProduct + " " + x.BoardSerial)},
		[2]string{"BIOS", strings.TrimSpace(x.BIOSVendor + " " + x.BIOSVersion + " " + x.BIOSDate)},
		[2]string{"BMC", strings.TrimSpace(x.BMCVendor + " " + x.BMCFirmware)}, [2]string{"Chassis", strings.TrimSpace(x.ChassisType + " " + x.ChassisSerial)},
		[2]string{"Kernel", x.Kernel}, [2]string{"Uptime", up})
	if len(x.PSUs) > 0 {
		var rows [][]string
		for _, p := range x.PSUs {
			rows = append(rows, []string{p.Location, strings.TrimSpace(p.Manufacturer + " " + p.Model), p.Serial, f0(p.MaxWatts, " W"), p.StatusText})
		}
		m.h(3, "Power supplies")
		m.table([]string{"PSU", "Model", "Serial", ">Rating", "State"}, rows)
	}
	sensorTable(m, "Fans and chassis temperatures", append(append([]model.Sensor{}, x.Fans...), x.Temps...))
}

func sensorTable(m *md, title string, ss []model.Sensor) {
	var rows [][]string
	for _, s := range ss {
		lim := ""
		if t := s.Thresholds; t != nil {
			var p []string
			if t.LowerCrit != nil {
				p = append(p, fmt.Sprintf("lcrit %.4g", *t.LowerCrit))
			}
			if t.UpperWarn != nil {
				p = append(p, fmt.Sprintf("warn %.4g", *t.UpperWarn))
			}
			if t.UpperCrit != nil {
				p = append(p, fmt.Sprintf("crit %.4g", *t.UpperCrit))
			}
			lim = strings.Join(p, " · ")
		}
		val := ""
		if s.Value != nil {
			val = fmt.Sprintf("%.4g %s", *s.Value, unitName(s.Unit))
		}
		rows = append(rows, []string{s.Name, val, lim, status(s.Status)})
	}
	if len(rows) > 0 {
		m.h(3, title)
		m.table([]string{"Sensor", ">Value", "Limits", "Status"}, rows)
	}
}

func unitName(u string) string {
	switch u {
	case model.UnitCelsius:
		return "°C"
	case model.UnitRPM:
		return "rpm"
	}
	return u
}

func cpuSection(m *md, c *model.CPU) {
	if c == nil {
		return
	}
	m.h(2, "CPU")
	online := 0
	var offline []string
	for _, l := range c.Logical {
		if l.Online {
			online++
		} else {
			offline = append(offline, fmt.Sprintf("cpu%d", l.ID))
		}
	}
	m.kv([2]string{"Model", c.Model}, [2]string{"Topology", fmt.Sprintf("%d sockets · %d cores · %d threads", c.SocketCount, c.CoreCount, c.ThreadCount)},
		[2]string{"Online", fmt.Sprintf("%d of %d", online, len(c.Logical))}, [2]string{"Offline", strings.Join(offline, ", ")},
		[2]string{"Microcode", c.Microcode}, [2]string{"Hottest", f0(c.MaxTempC, " °C")})
	if len(c.Sockets) > 0 {
		var rows [][]string
		for _, s := range c.Sockets {
			if s.ID < 0 && s.Designation == "" {
				continue
			}
			rows = append(rows, []string{s.Designation, s.Version, s.Serial, s.PPIN})
		}
		m.table([]string{"Socket", "Model", "Serial", "PPIN"}, rows)
	}
	sensorTable(m, "Temperatures", c.PackageTemps)
	coresTable(m, c)
}

func memorySection(m *md, x *model.Memory) {
	if x == nil {
		return
	}
	m.h(2, "Memory")
	inst := ""
	if x.InstalledBytes != nil {
		inst = bytesGiB(*x.InstalledBytes)
	}
	slots := ""
	if x.Slots > 0 {
		slots = fmt.Sprintf("%d of %d populated", x.PopulatedSlots, x.Slots)
	}
	m.kv([2]string{"Installed", inst}, [2]string{"Usable", bytesGiB(x.TotalBytes)}, [2]string{"Slots", slots},
		[2]string{"ECC", x.ECCType}, [2]string{"Corrected errors", u(x.CorrectableErrors)}, [2]string{"Uncorrected errors", u(x.UncorrectableErrors)})
	var rows [][]string
	for _, d := range x.DIMMs {
		if !d.Populated {
			continue
		}
		rows = append(rows, []string{d.Locator, bytesGiB(d.SizeBytes), d.Type, i0(d.ConfiguredSpeedMTs, " MT/s"), d.Manufacturer, d.PartNumber,
			d.Serial, u(d.CorrectableErrors), u(d.UncorrectableErrors), status(d.Status)})
	}
	m.table([]string{"Slot", ">Size", "Type", ">Speed", "Vendor", "Part number", "Serial", ">CE", ">UE", "Status"}, rows)
}

func storageSection(m *md, x *model.Storage) {
	if x == nil {
		return
	}
	m.h(2, "Storage")
	var rows, vols [][]string
	for _, d := range x.Disks {
		if r := d.Remote; r != nil {
			vols = append(vols, []string{d.Name, strings.ToUpper(r.Transport), strings.TrimSpace(d.Vendor + " " + d.Model), bytesSI(d.SizeBytes),
				r.Target, r.Portal, r.State, fmt.Sprint(d.KernelErrors), status(d.Status)})
			continue
		}
		smart, poh, realloc, pending := "", "", "", ""
		if s := d.SMART; s != nil {
			if s.Passed != nil {
				smart = map[bool]string{true: "PASSED", false: "FAILED"}[*s.Passed]
			}
			poh, realloc, pending = u(s.PowerOnHours), u(s.Reallocated), u(s.Pending)
		}
		cmd := ""
		if d.IOErrors != nil {
			cmd = fmt.Sprint(*d.IOErrors)
			if d.IOErrorsNew != nil && *d.IOErrorsNew > 0 {
				cmd += fmt.Sprintf(" (+%d new)", *d.IOErrorsNew)
			}
		}
		rows = append(rows, []string{d.Name, strings.TrimSpace(d.Vendor + " " + d.Model), d.Serial, bytesSI(d.SizeBytes), strings.ToUpper(d.Kind),
			f0(d.TempCelsius, " °C"), smart, poh, realloc, pending, fmt.Sprint(d.KernelErrors), cmd, status(d.Status)})
	}
	m.table([]string{"Disk", "Model", "Serial", ">Size", "Type", ">Temp", "SMART", ">Power-on h", ">Realloc", ">Pending", ">Kernel I/O", ">SCSI cmd errors", "Status"}, rows)
	if len(rows) > 0 {
		m.p("_Kernel I/O: failed reads and writes in the kernel log. SCSI cmd errors (ioerr_cnt) since boot also count commands a drive or " +
			"controller rejects, such as SMART queries it does not pass through; only new ones Tyr did not cause are an alert._")
	}
	if len(vols) > 0 {
		m.h(3, "Network volumes")
		m.table([]string{"Device", "Transport", "Volume", ">Size", "Target", "Portal", "State", ">Kernel I/O", "Status"}, vols)
		m.p("_Volumes of a storage system: their disks are checked there. Kernel I/O errors here mean the path to the storage system failed._")
	}
	if len(x.MDArrays) > 0 {
		m.h(3, "Software RAID")
		var rows [][]string
		for _, a := range x.MDArrays {
			var mem []string
			for _, mb := range a.Members {
				s := mb.Device
				if mb.Failed {
					s += " (failed)"
				}
				mem = append(mem, s)
			}
			rows = append(rows, []string{a.Name, a.Level, a.State, fmt.Sprintf("%d/%d", a.ActiveDevices, a.Devices), strings.Join(mem, ", "), status(a.Status)})
		}
		m.table([]string{"Array", "Level", "State", ">Active", "Members", "Status"}, rows)
	}
	if len(x.Controllers) > 0 {
		m.h(3, "Hardware RAID")
		var rows [][]string
		for _, c := range x.Controllers {
			rows = append(rows, []string{fmt.Sprintf("c%d", c.Index), c.Model, c.Serial, c.Firmware, fmt.Sprintf("%d VD · %d PD", len(c.VDs), len(c.PDs)), status(c.Status)})
		}
		m.table([]string{"Controller", "Model", "Serial", "Firmware", "Drives", "Status"}, rows)
	}
	if len(x.Filesystems) > 0 {
		m.h(3, "Filesystems")
		var rows [][]string
		for _, f := range x.Filesystems {
			mode := "rw"
			if f.ReadOnly {
				mode = "ro"
			}
			rows = append(rows, []string{f.Mountpoint, f.Source, f.FSType, mode, bytesSI(f.SizeBytes), f0(f.UsedPercent, " %"), status(f.Status)})
		}
		m.table([]string{"Mount", "Device", "Type", "Mode", ">Size", ">Used", "Status"}, rows)
	}
}

func networkSection(m *md, x *model.Network) {
	if x == nil {
		return
	}
	m.h(2, "Network")
	var rows [][]string
	for _, n := range x.Interfaces {
		link := n.OperState
		if n.SpeedMbps != nil && *n.SpeedMbps > 0 {
			link = fmt.Sprintf("%s · %d Mb/s", n.OperState, *n.SpeedMbps)
		}
		xcvr := ""
		if t := n.Module; t != nil {
			xcvr = strings.TrimSpace(t.Identifier + " " + t.Vendor + " " + t.PartNumber + " " + t.Serial)
		}
		rows = append(rows, []string{n.Name, n.Kind, strings.TrimSpace(n.Vendor + " " + n.Model), link, n.MAC, n.Serial,
			strings.TrimSpace(n.Driver + " " + n.Firmware), xcvr, status(n.Status)})
	}
	m.table([]string{"Interface", "Kind", "Adapter", "Link", "MAC", "Serial", "Driver · firmware", "Transceiver", "Status"}, rows)
}

func accelSection(m *md, x *model.Accel) {
	if x == nil || len(x.GPUs)+len(x.NPUs) == 0 {
		return
	}
	m.h(2, "GPU/NPU")
	var rows [][]string
	for _, list := range [][]model.Accelerator{x.GPUs, x.NPUs} {
		for _, a := range list {
			temp := ""
			for _, t := range a.Temps {
				if t.Value != nil {
					temp = fmt.Sprintf("%.0f °C", *t.Value)
					break
				}
			}
			mem := ""
			if a.MemTotalBytes != nil {
				mem = bytesGiB(*a.MemTotalBytes)
			}
			rows = append(rows, []string{strings.ToUpper(a.Kind), a.Model, a.PCIAddress, a.Serial, strings.TrimSpace(a.Driver + " " + a.DriverVersion),
				a.Firmware, mem, temp, u(a.ECCUncorrectable), status(a.Status)})
		}
	}
	m.table([]string{"Kind", "Model", "PCI", "Serial", "Driver", "Firmware", ">Memory", ">Temp", ">ECC UE", "Status"}, rows)
}

func inventorySection(m *md, inv []model.Component) {
	if len(inv) == 0 {
		return
	}
	m.h(2, "Inventory")
	var rows [][]string
	for _, c := range inv {
		rows = append(rows, []string{c.Category, c.Location, c.Vendor, c.Model, c.PartNumber, c.Serial, c.Firmware, c.Detail, status(c.Status)})
	}
	m.table([]string{"Type", "Location", "Vendor", "Model", "Part number", "Serial", "Firmware", "Detail", "Health"}, rows)
}

func sourcesSection(m *md, s *model.Snapshot) {
	var rows [][]string
	for _, id := range append([]model.ModuleID{model.ModSystem}, model.Modules...) {
		sec := s.Section(id)
		if sec == nil {
			continue
		}
		for _, src := range sec.Head().Sources {
			if src.State != model.AvailOK {
				glyph := "○"
				if src.State == model.AvailNotApplicable {
					glyph = "·"
				}
				rows = append(rows, []string{moduleNames[id], glyph + " " + src.Name, string(src.State), src.Reason})
			}
		}
	}
	if len(rows) > 0 {
		m.h(2, "Data sources not used")
		m.table([]string{"Module", "Source", "State", "Reason"}, rows)
	}
}
