package report

import (
	"fmt"
	"slices"
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
	powerSection(&m, s.Power)
	eventsSection(&m, s.Events)
	inventorySection(&m, s.Inventory)
	explainSection(&m, s, alertsFrom(s))
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
			rows = append(rows, []string{p.Label(), strings.TrimSpace(p.Manufacturer + " " + p.Model), p.Serial, f0(p.MaxWatts, " W"), status(p.Health()) + " " + p.StatusText})
		}
		m.h(3, "Power supplies")
		m.table([]string{"PSU", "Model", "Serial", ">Rating", "State"}, rows)
	}
	sensorTable(m, "Fans and chassis temperatures", append(append([]model.Sensor{}, x.Fans...), x.Temps...))
	if len(x.Devices) > 0 {
		var rows [][]string
		for _, d := range x.Devices {
			rows = append(rows, []string{d.Location, d.Name, d.Type, d.PartNumber, d.Serial, d.Firmware, status(d.Status)})
		}
		m.h(3, "Devices (BMC)")
		m.table([]string{"Location", "Device", "Type", "Part number", "Serial", "Firmware", "Status"}, rows)
	}
	if len(x.Firmware) > 0 {
		var rows [][]string
		for _, f := range x.Firmware {
			rows = append(rows, []string{f.Name, f.Version, f.Location, status(f.Status)})
		}
		m.h(3, "Firmware (BMC)")
		m.table([]string{"Component", "Version", "Location", "Status"}, rows)
	}
	bootsTable(m, x)
}

// bootsTable lists the boots the journal knows and how each one ended.
func bootsTable(m *md, x *model.System) {
	if len(x.Boots) == 0 {
		return
	}
	var rows [][]string
	for i := len(x.Boots) - 1; i >= 0; i-- {
		b := x.Boots[i]
		end := "unknown"
		switch {
		case b.Current:
			end = "running"
		case b.Clean != nil && *b.Clean:
			end = "clean shutdown"
		case b.Clean != nil:
			end = "✖ no clean shutdown (crash, reset or power loss)"
		}
		rows = append(rows, []string{b.ID[:min(8, len(b.ID))], b.Start.UTC().Format("2006-01-02 15:04"), b.End.UTC().Format("2006-01-02 15:04"), dur(b.End.Sub(b.Start)), end})
	}
	m.h(3, "Boots")
	m.table([]string{"Boot", "Start (UTC)", "Last entry (UTC)", ">Ran", "Ended"}, rows)
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
	offlineTable(m, c)
	faultTable(m, c)
	mceHistoryTable(m, c)
	coresTable(m, c)
}

// offlineTable: per offline CPU since when and how, whether it survives a
// reboot and whether the other thread of its core still runs.
func offlineTable(m *md, c *model.CPU) {
	var rows [][]string
	for _, l := range c.Logical {
		o := l.Offline
		if o == nil {
			continue
		}
		since := "-"
		if !o.Since.IsZero() {
			since = o.Since.UTC().Format("2006-01-02 15:04 UTC")
		}
		how := map[string]string{"runtime": "at runtime", "boot": "at boot (kernel parameter)"}[o.How]
		if o.AfterMCE {
			how += ", after a machine check"
		}
		keep := "no: back online after a reboot"
		if o.Persistent != "" {
			keep = "yes: " + o.Persistent
		} else if o.How == "boot" {
			keep = "yes: kernel command line"
		}
		other := "-"
		if o.Sibling != nil {
			other = fmt.Sprintf("cpu%d offline too", *o.Sibling)
			if o.SiblingOnline {
				other = fmt.Sprintf("✖ cpu%d still runs work", *o.Sibling)
			}
			if o.SiblingInferred {
				other += " (from the numbering)"
			}
		}
		core := "-"
		if l.SocketID >= 0 {
			core = fmt.Sprintf("%d / %d", l.SocketID, l.CoreID)
		}
		crash := ""
		for _, cr := range c.CrashRecords {
			if cr.CPU == l.ID {
				crash = "✖ " + strings.TrimPrefix(cr.Reason, "Kernel panic - not syncing: ")
			}
		}
		rows = append(rows, []string{fmt.Sprintf("cpu%d", l.ID), core, since, how, keep, other, o.L3SharedWith, crash})
	}
	if len(rows) == 0 {
		return
	}
	m.h(3, "Offline CPUs")
	m.table([]string{"CPU", "Socket / core", "Offline since", "How", "Stays offline after reboot", "Other thread", "Shares L3 with", "Crashed the kernel"}, rows)
	m.p("Test an offline CPU in a maintenance window with `sudo tyr core-test cpuN`: online only for the test, isolated from other work, offline again after. A defective core can still raise a fatal machine check and reboot the server.")
}

// mceHistoryTable lists the machine checks of all sources, newest first.
func mceHistoryTable(m *md, c *model.CPU) {
	if len(c.MCEHistory) == 0 && len(c.CrashRecords) == 0 {
		return
	}
	m.h(3, "Machine check history")
	m.p("Errors in a core's own units (L1/L2 cache, fetch, load-store, execution) point at that core; errors in shared units (L3, memory controller, interconnect) are only reported by the CPU that noticed them.")
	var rows [][]string
	for i := len(c.MCEHistory) - 1; i >= 0 && len(rows) < 50; i-- {
		r := c.MCEHistory[i]
		when, cpu, bank := "-", "-", "-"
		if !r.Time.IsZero() {
			when = r.Time.UTC().Format("2006-01-02 15:04")
		}
		if r.CPU >= 0 {
			cpu = fmt.Sprintf("cpu%d", r.CPU)
		}
		if r.Bank >= 0 {
			bank = fmt.Sprint(r.Bank)
		}
		where := map[string]string{"core": "in the core", "shared": "shared"}[r.Scope]
		rows = append(rows, []string{when, cpu, bank, r.Unit, where, status(r.Severity), r.Source})
	}
	if len(c.MCEHistory) > len(rows) {
		m.p("Newest %d of %d records.", len(rows), len(c.MCEHistory))
	}
	m.table([]string{"Time (UTC)", "CPU", "Bank", "Unit", "Where", "Severity", "Source"}, rows)
	for _, cr := range c.CrashRecords {
		when := ""
		if !cr.Time.IsZero() {
			when = " (" + cr.Time.UTC().Format("2006-01-02 15:04 UTC") + ")"
		}
		m.p("**Crash record**%s `%s`: %s", when, cr.File, esc(cr.Reason))
		if len(cr.Lines) > 0 {
			m.p("```text\n%s\n```", strings.Join(cr.Lines, "\n"))
		}
	}
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
		var pds [][]string
		for _, c := range x.Controllers {
			for _, p := range c.PDs {
				var hrs string
				if p.PowerOnHours != nil {
					hrs = fmt.Sprintf("%d h", *p.PowerOnHours)
				}
				state := strings.TrimSpace(p.State + " " + strings.Join(p.Reasons, ", "))
				if p.FailurePredicted {
					state += " (failure predicted)"
				}
				pds = append(pds, []string{fmt.Sprintf("c%d %s", c.Index, p.Slot), p.Model, p.Serial, p.SizeText, strings.TrimSpace(p.Interface + " " + p.Media),
					f0(p.LifeLeftPercent, " %"), hrs, f0(p.TempCelsius, " °C"), state, status(p.Status)})
			}
		}
		if len(pds) > 0 {
			m.table([]string{"Drive", "Model", "Serial", ">Size", "Type", ">Life left", ">Power-on", ">Temp", "State", "Status"}, pds)
		}
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
	for _, n := range x.Interfaces {
		if n.Kind == "vpn" && n.VPN != nil {
			vpnSection(m, n, x.CollectedAt)
		}
	}
}

// vpnSection: a VPN's peers, its service's journal and kernel lines.
func vpnSection(m *md, n model.NIC, now time.Time) {
	v := n.VPN
	m.h(3, fmt.Sprintf("VPN %s (%s)", n.Name, v.Type))
	port := ""
	if v.ListenPort != nil {
		port = fmt.Sprint(*v.ListenPort)
	}
	m.kv([2]string{"Addresses", strings.Join(n.Addresses, ", ")}, [2]string{"Public key", v.PublicKey},
		[2]string{"Listen port", port}, [2]string{"Peer status", v.Note})
	if len(v.Peers) > 0 {
		var rows [][]string
		for _, p := range v.Peers {
			hs := "never"
			if !p.LatestHandshake.IsZero() {
				hs = p.LatestHandshake.UTC().Format("2006-01-02 15:04:05") + fmt.Sprintf(" (%s ago)", now.Sub(p.LatestHandshake).Round(time.Second))
			}
			ka := ""
			if p.KeepaliveS > 0 {
				ka = fmt.Sprintf("%d s", p.KeepaliveS)
			}
			rows = append(rows, []string{p.PublicKey, p.Endpoint, strings.Join(p.AllowedIPs, ", "), hs,
				bytesGiB(p.RxBytes), bytesGiB(p.TxBytes), ka, status(p.Status)})
		}
		m.table([]string{"Peer", "Endpoint", "Allowed IPs", "Last handshake", "Received", "Sent", "Keepalive", "Status"}, rows)
	}
	if len(v.Journal) > 0 {
		m.p("Journal of %s, last 24 h:", strings.Join(v.Services, ", "))
		var lines []string
		for _, l := range v.Journal {
			prio := map[int]string{0: "EMERG", 1: "ALERT", 2: "CRIT", 3: "ERR", 4: "WARN"}[l.Priority]
			lines = append(lines, strings.TrimSpace(fmt.Sprintf("%s %s %s %s", l.Time.UTC().Format("2006-01-02 15:04:05"), l.Service, prio, l.Message)))
		}
		m.p("```text\n%s\n```", strings.Join(lines, "\n"))
	}
	if len(v.KernelLines) > 0 {
		m.p("Kernel log:")
		m.p("```text\n%s\n```", strings.Join(v.KernelLines, "\n"))
	}
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
			} else if a.SharedMemory {
				mem = "shared"
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

// faultTable: program crashes (segfault, general protection, invalid
// opcode) per physical core, from the kernel's "likely on CPU" note.
func faultTable(m *md, c *model.CPU) {
	if len(c.ProcessFaults) == 0 {
		return
	}
	type grp struct {
		socket, core int
		n            int
		cpus, procs  []string
		first, last  time.Time
	}
	by := map[[2]int]*grp{}
	var order []*grp
	for _, f := range c.ProcessFaults {
		k := [2]int{f.Socket, f.Core}
		g := by[k]
		if g == nil {
			g = &grp{socket: f.Socket, core: f.Core, first: f.Time}
			by[k] = g
			order = append(order, g)
		}
		g.n++
		if cpu := fmt.Sprintf("cpu%d", f.CPU); !slices.Contains(g.cpus, cpu) {
			g.cpus = append(g.cpus, cpu)
		}
		if !slices.Contains(g.procs, f.Process) {
			g.procs = append(g.procs, f.Process)
		}
		g.last = f.Time
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].n > order[j].n })
	m.h(3, "Program crashes by core")
	m.p("The kernel names the CPU a crashing program ran on (segfault, general protection, invalid opcode). Unrelated programs crashing on one core mean the core computes wrong results, often without any machine check.")
	var rows [][]string
	for _, g := range order {
		procs := g.procs
		if len(procs) > 6 {
			procs = append(procs[:6:6], fmt.Sprintf("+%d", len(g.procs)-6))
		}
		rows = append(rows, []string{fmt.Sprintf("%d / %d", g.socket, g.core), strings.Join(g.cpus, ", "), fmt.Sprint(g.n),
			fmt.Sprintf("%d of %d", g.n, len(c.ProcessFaults)), esc(strings.Join(procs, ", ")), g.first.UTC().Format("2006-01-02 15:04"), g.last.UTC().Format("2006-01-02 15:04")})
	}
	m.table([]string{"Socket / core", "CPUs", ">Crashes", ">Share", "Programs", "First (UTC)", "Last (UTC)"}, rows)
}

// powerSection: draw, cap, redundancy and every supply with its readings.
func powerSection(m *md, p *model.Power) {
	if p == nil || len(p.Supplies) == 0 {
		return
	}
	m.h(2, "Power")
	src := "SMBIOS (no live readings: the BMC's Redfish service was not readable)"
	if p.Source == "redfish" {
		src = "BMC Redfish"
	}
	limit := "none"
	if p.LimitWatts != nil {
		limit = fmt.Sprintf("%.0f W", *p.LimitWatts)
	}
	avg := ""
	if p.AvgWatts != nil {
		avg = fmt.Sprintf("%s (min %s, max %s, last %d min)", f0(p.AvgWatts, " W"), f0(p.MinWatts, " W"), f0(p.MaxWatts, " W"), p.IntervalMin)
	}
	kv := [][2]string{{"Source", src}, {"Draw", f0(p.ConsumedWatts, " W")}, {"Capacity", f0(p.CapacityWatts, " W")}, {"Average", avg}}
	if p.Source == "redfish" {
		kv = append(kv, [2]string{"Power cap", limit})
	}
	for _, r := range p.Redundancy {
		kv = append(kv, [2]string{"Redundancy", strings.TrimSpace(status(r.Status) + " " + r.Name + " " + r.Mode)})
	}
	m.kv(kv...)
	var rows [][]string
	for _, s := range p.Supplies {
		in := ""
		if s.InputVolts != nil {
			in = fmt.Sprintf("%.0f V", *s.InputVolts)
		}
		rows = append(rows, []string{s.Name, s.Bay, strings.TrimSpace(s.Manufacturer + " " + s.Model), s.Serial, s.SparePart, s.Firmware,
			strings.TrimSpace(status(s.Status) + " " + s.Health + " " + s.Detail), in, f0(s.OutputWatts, " W"), f0(s.CapacityWatts, " W")})
	}
	m.table([]string{"PSU", "Bay", "Model", "Serial", "Spare part", "Firmware", "State", ">Input", ">Output", ">Rating"}, rows)
}

// eventsSection: the open BMC events, then the newest warnings and errors
// of all logs.
func eventsSection(m *md, e *model.Events) {
	if e == nil || len(e.Logs) == 0 {
		return
	}
	m.h(2, "Event logs")
	var logs []string
	for _, l := range e.Logs {
		logs = append(logs, fmt.Sprintf("%s (%d)", l.Name, l.Entries))
	}
	m.p("Logs read: %s.", esc(strings.Join(logs, ", ")))
	var rows [][]string
	for i := len(e.Entries) - 1; i >= 0 && len(rows) < 60; i-- {
		ev := e.Entries[i]
		if ev.Severity < model.StatusWarn || ev.Severity == model.StatusUnknown {
			continue
		}
		when := "-"
		if !ev.Time.IsZero() {
			when = ev.Time.UTC().Format("2006-01-02 15:04")
		}
		rep := ""
		if ev.Repaired != nil {
			rep = map[bool]string{true: "repaired", false: "open"}[*ev.Repaired]
		}
		comp := ev.Component
		if comp == "" {
			comp = ev.Class
		}
		rows = append(rows, []string{when, status(ev.Severity), ev.Log, esc(comp), esc(ev.Message), rep})
	}
	if len(rows) == 0 {
		m.p("No warnings or errors in the logs.")
		return
	}
	m.p("Warnings and errors, newest first (at most 60):")
	m.table([]string{"Time (UTC)", "Severity", "Log", "Component", "Message", "Repaired"}, rows)
}
