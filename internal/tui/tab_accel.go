package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
)

type accelTab struct {
	acc  *model.Accel
	w, h int
	list *listView
	keys keyMap
}

func newAccelTab(p *prefs) *accelTab { return &accelTab{list: newListView(p), keys: newKeyMap()} }

func (a *accelTab) Title() string          { return "GPU/NPU" }
func (a *accelTab) Module() model.ModuleID { return model.ModAccel }
func (a *accelTab) SetData(s *model.Snapshot) {
	a.acc = s.Accel
	a.arrange()
}
func (a *accelTab) SetSize(w, h int) { a.w, a.h = w, h }
func (a *accelTab) Help() []key.Binding {
	return []key.Binding{a.keys.Up, a.keys.Enter, keyNext}
}

func accelName(x model.Accelerator) string {
	return fmt.Sprintf("%s%d", strings.ToUpper(x.Kind), x.Index)
}

func (a *accelTab) arrange() {
	var out []listItem
	for _, x := range a.items() {
		out = append(out, listItem{key: x.PCIAddress, alt: []string{accelName(x), x.Name, x.UUID}, status: x.Status})
	}
	a.list.arrange(out)
}

func (a *accelTab) byAddr(addr string) (model.Accelerator, bool) {
	for _, x := range a.items() {
		if x.PCIAddress == addr {
			return x, true
		}
	}
	return model.Accelerator{}, false
}

func (a *accelTab) Focus(subject string) bool {
	a.arrange()
	return a.list.focus(subject)
}

func (a *accelTab) items() []model.Accelerator {
	if a.acc == nil {
		return nil
	}
	return append(append([]model.Accelerator{}, a.acc.GPUs...), a.acc.NPUs...)
}

func (a *accelTab) Update(msg tea.KeyPressMsg) tea.Cmd {
	a.list.handle(msg, a.keys, max(1, a.h/2))
	return nil
}

func maxTemp(x model.Accelerator) (*float64, model.Status) {
	var m *float64
	st := model.StatusOK
	for _, t := range x.Temps {
		if t.Value != nil && (m == nil || *t.Value > *m) {
			m = t.Value
		}
		st = model.Worst(st, t.Status)
	}
	return m, st
}

// View (canvas GPU): device table with load and memory bars, BMC display
// adapters dimmed as "not counted", the selected device as three columns
// (identity · sensors · reliability), findings stating what was checked,
// and the NPU section ("· not applicable" when there is none).
func (a *accelTab) View(t *Theme) string {
	x := a.acc
	if x == nil {
		return t.Dim.Render("GPU/NPU data not collected")
	}
	a.arrange()
	cols := []column{{"Device", 6}, {"Model", 0}, {"Driver", -1}, {"Load", 14}, {"Memory", 22}, {">Power", 10}, {">°C", 3}, {"PCIe", 11}}
	if a.w < 100 {
		cols = []column{{"Device", 6}, {"Model", 0}, {"Load", 14}, {">°C", 3}, {"PCIe", 11}}
	}
	for li := range a.list.shown {
		it, _ := a.byAddr(a.list.shown[li].key)
		load := t.Sub.Render(loadText(it))
		if it.UtilizationPercent != nil {
			load = memBar(t, *it.UtilizationPercent, model.StatusOK, 8) + fmt.Sprintf("%4.0f %%", *it.UtilizationPercent)
		}
		mem := t.Sub.Render("–")
		if it.MemTotalBytes != nil {
			used := uint64(0)
			if it.MemUsedBytes != nil {
				used = *it.MemUsedBytes
			}
			pct := float64(used) / float64(*it.MemTotalBytes) * 100
			mem = memBar(t, pct, model.StatusOK, 8) + " " + bytesGB(used) + "/" + bytesGB(*it.MemTotalBytes)
		}
		pw := watts(it.PowerWatts)
		if it.PowerCapWatts != nil {
			pw += fmt.Sprintf(" / %.0f", *it.PowerCapWatts)
		}
		temp, tst := maxTemp(it)
		tc := optF(temp, "%.0f")
		if tst >= model.StatusWarn {
			tc = t.StatusBold(tst, tc)
		}
		link := t.Sub.Render("–")
		if it.Integrated {
			link = t.Sub.Render("internal")
		} else if it.LinkWidth != nil {
			link = fmt.Sprintf("x%d", *it.LinkWidth)
			if g := gen(it.LinkSpeed); g > 0 {
				link = fmt.Sprintf("Gen%d ", g) + link
			}
			if it.MaxLinkWidth != nil && *it.MaxLinkWidth > *it.LinkWidth {
				link = t.StatusBold(model.StatusWarn, link+fmt.Sprintf("/x%d", *it.MaxLinkWidth))
			}
		}
		cells := map[string]string{"Device": strings.ToLower(accelName(it)), "Model": it.Model, "Driver": orDash(it.Driver), "Load": load,
			"Memory": mem, ">Power": pw, ">°C": tc, "PCIe": link}
		var r row
		for _, c := range cols {
			r.cells = append(r.cells, cells[c.title])
		}
		a.list.shown[li].row = r
	}
	listH := len(a.list.shown) + 1
	var parts []string
	if len(a.list.all) == 0 {
		parts = append(parts, " "+t.NotApplicable("GPU", "none found (checked PCI display and accelerator classes)"))
	} else {
		list := a.list.render(t, cols, a.w, listH)
		for _, d := range x.BMCDisplays {
			list += "\n" + t.Sub.Render(fmt.Sprintf("   –      %s · %s · not counted", d.Model, shortPCI(d.Address)))
		}
		parts = append(parts, list)
		if it, ok := a.list.current(); ok {
			sel, _ := a.byAddr(it.key)
			parts = append(parts, "", a.inspectorBlock(t, sel))
		}
	}
	top := strings.Join(parts, "\n")
	fs := findingsSection(t, "GPU/NPU", x.Findings, x.Sources, max(3, a.h-lipgloss.Height(top)-4), a.w)
	if countNonOK(x.Findings) == 0 && len(a.list.all) > 0 {
		fs = t.HeadingRight("Findings · GPU/NPU", "", "", a.w) + "\n " + t.Dot(model.StatusOK) + " " +
			t.Dim.Render(fmt.Sprintf("No problems. %s checked: ECC, retired pages, row remapping, throttle reasons, driver errors.",
				countLabel(len(a.list.all), "device", "devices")))
	}
	out := top + "\n\n" + fs
	if len(x.NPUs) == 0 {
		out += "\n\n" + t.Heading("NPU", "", a.w) + "\n " + t.NotApplicable("not applicable:", "no NPU found (checked /sys/class/accel, amdxdna, intel_vpu)")
	}
	return out
}

// inspectorBlock: identity · sensors · reliability for one device.
func (a *accelTab) inspectorBlock(t *Theme, x model.Accelerator) string {
	head := t.HeadingFocus(strings.ToLower(accelName(x))+" · "+x.Model, "", "enter full page", a.w)
	drv := x.Driver
	if x.DriverVersion != "" {
		drv += " " + x.DriverVersion
	}
	pci := shortPCI(x.PCIAddress)
	if x.NUMANode >= 0 {
		pci += fmt.Sprintf(" · NUMA %d", x.NUMANode)
	}
	uuid := x.UUID
	if len(uuid) > 16 {
		uuid = uuid[:8] + "…" + uuid[len(uuid)-4:]
	}
	c1 := kvBlock(t, []kvPair{{"Serial", t.Bold.Render(x.Serial)}, {"Part", x.PartNumber}, {"UUID", uuid}, {"PCI", pci},
		{"Firmware", x.Firmware}, {"Driver", drv}}, "")
	var rows []kvPair
	for _, s := range x.Temps {
		lbl := s.Name
		if s.Shared {
			lbl += "*"
		}
		rows = append(rows, kvPair{lbl, sensorGaugeLine(t, s, 12)})
	}
	if x.PowerWatts != nil {
		ps := model.Sensor{Value: x.PowerWatts, Unit: "W"}
		if x.PowerCapWatts != nil {
			ps.Thresholds = &model.Thresholds{UpperCrit: x.PowerCapWatts}
		}
		rows = append(rows, kvPair{"Power", sensorGaugeLine(t, ps, 12)})
	}
	if x.UtilizationPercent != nil {
		rows = append(rows, kvPair{"Util", spark(t.sess.history("accel/"+x.PCIAddress+"/load"), 16, 100, t.Series[0]) + fmt.Sprintf(" %.0f %%", *x.UtilizationPercent)})
	}
	if x.LinkWidth != nil && !x.Integrated {
		l := fmt.Sprintf("x%d", *x.LinkWidth)
		if x.MaxLinkWidth != nil {
			l += fmt.Sprintf(" of x%d", *x.MaxLinkWidth)
		}
		rows = append(rows, kvPair{"Link", l + " " + t.Sub.Render(x.LinkSpeed)})
	}
	if x.PowerMode != "" {
		rows = append(rows, kvPair{"Mode", x.PowerMode})
	}
	if len(rows) == 0 {
		rows = append(rows, kvPair{"Sensors", t.Sub.Render(orDash(x.SensorNote))})
	}
	c2 := kvBlock(t, rows, "")
	ecc := ""
	if x.ECCCorrectable != nil || x.ECCUncorrectable != nil {
		ecc = fmt.Sprintf("ce %s · ue %s", optU(x.ECCCorrectable), optU(x.ECCUncorrectable))
		if x.ECCUncorrectable != nil && *x.ECCUncorrectable > 0 {
			ecc = t.StatusBold(model.StatusCrit, ecc)
		}
	}
	remap := t.Dot(model.StatusOK) + " none pending"
	if x.RemapPending != nil && *x.RemapPending {
		remap = t.StatusBold(model.StatusWarn, "▲ pending")
	}
	if x.RemapFailed != nil && *x.RemapFailed {
		remap = t.StatusBold(model.StatusCrit, "✖ failed")
	}
	if x.RemapPending == nil && x.RemapFailed == nil {
		remap = ""
	}
	thr := strings.Join(x.ThrottleReasons, ", ")
	if thr == "" {
		thr = "none"
	}
	derr := fmt.Sprint(x.KernelErrors)
	if x.KernelErrors > 0 {
		derr = t.StatusBold(model.StatusWarn, derr)
	}
	c3 := kvBlock(t, []kvPair{{"ECC", ecc}, {"Retired", optU(x.RetiredPages)}, {"Remap", remap}, {"Throttle", thr},
		{"Driver err", derr}, {"Power state", strings.TrimSpace(x.PowerState + " " + x.RuntimeStatus)}}, "")
	if a.w < 100 {
		return head + "\n" + c1
	}
	return head + "\n" + kvColumns(a.w, c1, c2, c3)
}

// loadText is utilization if the driver reports it, else the runtime
// power state (an idle NPU is runtime-suspended).
func loadText(a model.Accelerator) string {
	switch {
	case a.UtilizationPercent != nil:
		return fmt.Sprintf("%.0f%%", *a.UtilizationPercent)
	case a.RuntimeStatus == "suspended":
		return "idle"
	case a.RuntimeStatus == "active":
		return "on"
	}
	return "–"
}

func watts(v *float64) string {
	switch {
	case v == nil:
		return "–"
	case *v < 1:
		return fmt.Sprintf("%.0fmW", *v*1000)
	case *v < 10:
		return fmt.Sprintf("%.1fW", *v)
	}
	return fmt.Sprintf("%.0fW", *v)
}

func gen(speed string) int {
	switch {
	case strings.HasPrefix(speed, "2.5 "):
		return 1
	case strings.HasPrefix(speed, "5.0 "), strings.HasPrefix(speed, "5 "):
		return 2
	case strings.HasPrefix(speed, "8.0 "), strings.HasPrefix(speed, "8 "):
		return 3
	case strings.HasPrefix(speed, "16"):
		return 4
	case strings.HasPrefix(speed, "32"):
		return 5
	case strings.HasPrefix(speed, "64"):
		return 6
	}
	return 0
}

// Detail is the full page of the selected GPU or NPU.
func (a *accelTab) Detail(t *Theme, w int) (string, string) {
	cur, ok := a.list.current()
	if !ok {
		return "", ""
	}
	x, _ := a.byAddr(cur.key)
	name := accelName(x)
	doc := newDoc(t, w)
	doc.section("Identity", t.Badge(x.Status))
	numa := ""
	if x.NUMANode >= 0 {
		numa = fmt.Sprint(x.NUMANode)
	}
	drv := x.Driver
	if x.DriverVersion != "" {
		drv += " " + x.DriverVersion
	}
	doc.kv([2]string{"Vendor", x.Vendor}, [2]string{"Model", x.Model}, [2]string{"Serial", x.Serial}, [2]string{"Part number", x.PartNumber},
		[2]string{"UUID", x.UUID}, [2]string{"PCI address", x.PCIAddress}, [2]string{"Device node", x.Name}, [2]string{"NUMA node", numa},
		[2]string{"Driver", drv}, [2]string{"Firmware", x.Firmware}, [2]string{"Integrated", map[bool]string{true: "yes (part of the CPU package)", false: ""}[x.Integrated]})
	doc.section("PCIe and power state", "")
	link := ""
	if x.LinkWidth != nil {
		link = fmt.Sprintf("x%d %s", *x.LinkWidth, x.LinkSpeed)
		if x.MaxLinkWidth != nil {
			link += t.Dim.Render(fmt.Sprintf("  (device max x%d %s)", *x.MaxLinkWidth, x.MaxLinkSpeed))
		}
	}
	doc.kv([2]string{"Link", link}, [2]string{"Power state", x.PowerState}, [2]string{"Runtime PM", x.RuntimeStatus},
		[2]string{"Awake", optF(x.AwakePercent, "%.0f%% of last interval")}, [2]string{"Power mode", x.PowerMode})
	doc.section("Sensors", t.Dim.Render(x.SensorNote))
	for _, s := range x.Temps {
		doc.sensor(s, t.sess.history("accel/"+x.PCIAddress+"/"+s.Name))
	}
	for _, s := range x.Fans {
		doc.sensor(s, nil)
	}
	if x.PowerWatts != nil {
		ps := model.Sensor{Name: "Power", Value: x.PowerWatts, Unit: "W", Status: model.StatusOK}
		if x.PowerCapWatts != nil {
			ps.Thresholds = &model.Thresholds{UpperWarn: x.PowerCapWatts, Origin: "power cap"}
		}
		doc.sensor(ps, t.sess.history("accel/"+x.PCIAddress+"/power"))
	}
	if len(x.Temps)+len(x.Fans) == 0 && x.PowerWatts == nil {
		doc.add(t.Dim.Render("no sensor data"))
	}
	doc.section("Utilization", "")
	mem := ""
	if x.MemTotalBytes != nil {
		used := uint64(0)
		if x.MemUsedBytes != nil {
			used = *x.MemUsedBytes
		}
		pct := float64(used) / float64(*x.MemTotalBytes) * 100
		mem = usageBar(t, &pct, model.StatusOK, 20) + fmt.Sprintf("  %s of %s", bytesGB(used), bytesGB(*x.MemTotalBytes))
	}
	util := ""
	if x.UtilizationPercent != nil {
		util = usageBar(t, x.UtilizationPercent, model.StatusOK, 20)
		if h := t.sess.history("accel/" + x.PCIAddress + "/load"); len(h) > 1 {
			util += "  " + t.Message.Render(sparkline(h, 20))
		}
	}
	ctx := ""
	if x.HWContexts != nil {
		ctx = fmt.Sprint(*x.HWContexts)
	}
	cols := ""
	if x.AIEColumns != nil {
		cols = fmt.Sprint(*x.AIEColumns)
	}
	doc.kv([2]string{"Load", util}, [2]string{"Memory", mem}, [2]string{"Clock", optF(x.ClockMHz, "%.0f MHz")},
		[2]string{"Running contexts", ctx}, [2]string{"AIE columns", cols}, [2]string{"Busy time", optU(x.BusyTimeUs)})
	doc.section("Reliability", "")
	remap := ""
	if x.RemapPending != nil && *x.RemapPending {
		remap = t.StatusText(model.StatusWarn, "pending (reset the GPU)")
	}
	if x.RemapFailed != nil && *x.RemapFailed {
		remap = t.StatusText(model.StatusCrit, "FAILED")
	}
	doc.kv([2]string{"ECC correctable", optU(x.ECCCorrectable)}, [2]string{"ECC uncorrectable", optU(x.ECCUncorrectable)},
		[2]string{"Retired pages", optU(x.RetiredPages)}, [2]string{"Row remapping", remap},
		[2]string{"Throttle reasons", strings.Join(x.ThrottleReasons, ", ")}, [2]string{"Driver errors (kernel log)", fmt.Sprint(x.KernelErrors)})
	doc.counters(t.sess.counters(func(k string) bool { return strings.HasPrefix(k, "accel."+x.PCIAddress+".") }), "accel."+x.PCIAddress+".")
	doc.findings(relatedFindings(a.acc.Findings, x.PCIAddress))
	return name + " " + shortPCI(x.PCIAddress), doc.String()
}

func shortPCI(a string) string { return strings.TrimPrefix(a, "0000:") }
