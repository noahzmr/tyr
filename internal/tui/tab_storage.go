package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/model"
)

type storageTab struct {
	st      *model.Storage
	w, h    int
	view    int // 0 disks, 1 RAID, 2 filesystems
	lists   [3]*listView
	keys    keyMap
	switchV key.Binding
}

func newStorageTab(p *prefs) *storageTab {
	return &storageTab{keys: newKeyMap(), switchV: key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "view")),
		lists: [3]*listView{newListView(p), newListView(p), newListView(p)}}
}

func (s *storageTab) Title() string             { return "Storage" }
func (s *storageTab) Module() model.ModuleID    { return model.ModStorage }
func (s *storageTab) SetData(x *model.Snapshot) { s.st = x.Storage; s.arrange() }
func (s *storageTab) SetSize(w, h int)          { s.w, s.h = w, h }
func (s *storageTab) Help() []key.Binding {
	return []key.Binding{s.keys.Up, s.keys.Enter, s.switchV, keyNext}
}

// raidItem is one row of the RAID view.
type raidItem struct {
	md   *model.MDArray
	ctrl *model.RAIDController
	vd   *model.VirtualDrive
	pd   *model.PhysicalDrive
	bat  *model.RAIDBattery
}

func (s *storageTab) raidItems() map[string]raidItem {
	out := map[string]raidItem{}
	for i := range s.st.MDArrays {
		out[s.st.MDArrays[i].Name] = raidItem{md: &s.st.MDArrays[i]}
	}
	for i := range s.st.Controllers {
		c := &s.st.Controllers[i]
		for j := range c.VDs {
			out["VD "+c.VDs[j].ID] = raidItem{ctrl: c, vd: &c.VDs[j]}
		}
		for j := range c.PDs {
			out["PD "+c.PDs[j].Slot] = raidItem{ctrl: c, pd: &c.PDs[j]}
		}
		if c.Battery != nil {
			out["BBU "+c.Model] = raidItem{ctrl: c, bat: c.Battery}
		}
	}
	return out
}

// arrange builds the items of all three views.
func (s *storageTab) arrange() {
	if s.st == nil {
		return
	}
	var disks []listItem
	for _, d := range s.st.Disks {
		disks = append(disks, listItem{key: d.Name, alt: []string{d.Path, d.Serial}, status: d.Status})
	}
	s.lists[0].arrange(disks)
	var raid []listItem
	for _, a := range s.st.MDArrays {
		raid = append(raid, listItem{key: a.Name, status: a.Status})
	}
	for _, c := range s.st.Controllers {
		for _, v := range c.VDs {
			raid = append(raid, listItem{key: "VD " + v.ID, alt: []string{v.ID}, status: v.Status})
		}
		for _, p := range c.PDs {
			raid = append(raid, listItem{key: "PD " + p.Slot, alt: []string{p.Slot}, status: p.Status})
		}
		if c.Battery != nil {
			raid = append(raid, listItem{key: "BBU " + c.Model, status: c.Battery.Status})
		}
	}
	s.lists[1].arrange(raid)
	var fs []listItem
	for _, f := range s.st.Filesystems {
		fs = append(fs, listItem{key: f.Mountpoint, alt: []string{f.Source}, status: f.Status})
	}
	s.lists[2].arrange(fs)
}

func (s *storageTab) Focus(subject string) bool {
	s.arrange()
	for v, l := range s.lists {
		if l.focus(subject) {
			s.view = v
			return true
		}
	}
	return false
}

func (s *storageTab) Update(msg tea.KeyPressMsg) tea.Cmd {
	if key.Matches(msg, s.switchV) {
		s.view = (s.view + 1) % 3
		return nil
	}
	s.lists[s.view].handle(msg, s.keys, max(1, s.h/2))
	return nil
}

func (s *storageTab) View(t *Theme) string {
	x := s.st
	if x == nil {
		return t.Dim.Render("storage data not collected")
	}
	s.arrange()
	var list, detail string
	switch s.view {
	case 0:
		detail = s.diskDetail(t)
	case 1:
		detail = s.raidDetail(t)
	}
	avail := s.h - lipgloss.Height(detail) - 1
	if detail == "" {
		avail++
	}
	rows := listHeight(avail, len(s.lists[s.view].shown), countNonOK(x.Findings))
	switch s.view {
	case 0:
		list = s.disks(t, rows)
	case 1:
		need := lipgloss.Height(s.raid(t, 1000))
		list = s.raid(t, listHeight(avail, need-1, countNonOK(x.Findings))-2)
	default:
		list = s.filesystems(t, rows)
	}
	parts := []string{list}
	if detail != "" {
		parts = append(parts, "", detail)
	}
	top := strings.Join(parts, "\n")
	return top + "\n\n" + findingsSection(t, "Storage", x.Findings, x.Sources, s.h-lipgloss.Height(top)-1, s.w)
}

// diskErrors sums SMART error counters; io adds kernel log I/O errors and
// the SCSI command errors that are new since Tyr started (ioerr_cnt since
// boot includes commands the device merely rejected, Tyr's own SMART
// queries among them).
func diskErrors(d model.Disk) (smart, io uint64) {
	if m := d.SMART; m != nil {
		for _, v := range []*uint64{m.Reallocated, m.Pending, m.OfflineUncorrect, m.MediaErrors, m.CRCErrors, m.UncorrectedErrors} {
			if v != nil {
				smart += *v
			}
		}
	}
	io = uint64(d.KernelErrors)
	if d.IOErrorsNew != nil {
		io += *d.IOErrorsNew
	}
	return smart, io
}

// cmdErrors is the SCSI command error breakdown: "0 new · 1106 since boot
// (173 by Tyr's SMART reads)".
func cmdErrors(t *Theme, d model.Disk) string {
	if d.IOErrors == nil {
		return ""
	}
	var nw, tyr uint64
	if d.IOErrorsNew != nil {
		nw = *d.IOErrorsNew
	}
	if d.IOErrorsTyr != nil {
		tyr = *d.IOErrorsTyr
	}
	s := fmt.Sprintf("%d new", nw)
	if nw > 0 {
		s = t.StatusBold(model.StatusWarn, s)
	}
	rest := fmt.Sprintf(" · %s since boot", grouped(*d.IOErrors))
	if tyr > 0 {
		rest += fmt.Sprintf(" (%d by Tyr's SMART reads)", tyr)
	}
	return s + t.Sub.Render(rest)
}

// remoteState colours an iSCSI session / FC port state.
func remoteState(t *Theme, r *model.RemoteDisk) string {
	switch {
	case r.State == "":
		return ""
	case r.State == "LOGGED_IN" || r.State == "Online":
		return t.StatusText(model.StatusOK, r.State)
	}
	return t.StatusBold(model.StatusCrit, r.State)
}

func transportName(r *model.RemoteDisk) string {
	if r.Transport == "iscsi" {
		return "iSCSI"
	}
	return strings.ToUpper(r.Transport)
}

// ago is "3 min ago" for the SMART age, relative to the snapshot (a
// saved or recorded one is not "now").
func ago(at, now time.Time) string {
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h ago", int(d.Hours()))
}

func diskType(d model.Disk) string {
	switch strings.ToLower(d.Kind) {
	case "nvme":
		return "NVMe"
	case "ssd", "hdd":
		return strings.ToUpper(d.Kind)
	case "iscsi":
		return "iSCSI"
	case "fc":
		return "FC"
	}
	return d.Kind
}

// smartCell is the SMART column: PASSED / FAILED / – (unknown).
func smartCell(t *Theme, d model.Disk) string {
	m := d.SMART
	switch {
	case m == nil || m.Passed == nil:
		return t.Sub.Render("–")
	case !*m.Passed:
		return t.StatusBold(model.StatusCrit, "FAILED")
	}
	return t.StatusText(model.StatusOK, "PASSED")
}

// why repeats the row's problem: its findings without the "sdd: " prefix.
func why(fs []model.Finding, key string) string {
	var out []string
	for _, f := range sortFindings(relatedFindings(fs, key)) {
		if f.Status != model.StatusOK {
			out = append(out, strings.TrimPrefix(f.Message, key+": "))
		}
	}
	return strings.Join(out, " · ")
}

func (s *storageTab) disks(t *Theme, maxRows int) string {
	wide, narrow := s.w >= 150, s.w < 100
	cols := []column{{"Disk", 8}, {"Type", 5}, {"Model", 0}, {"Serial", -1}, {">Size", 7}, {">°C", 3}, {">Wear", 5}, {"SMART", 6}, {">Err", 6}}
	switch {
	case narrow:
		cols = []column{{"Disk", 8}, {"Type", 5}, {"Serial", -1}, {">Size", 7}, {">°C", 3}, {">Wear", 5}, {"SMART", 6}, {">Err", 6}}
	case wide:
		cols = []column{{"Disk", 8}, {"Type", 5}, {"Model", 22}, {"Serial", -1}, {">Size", 7}, {">°C", 3}, {">Wear", 5}, {"SMART", 6}, {">Errors", 6}, {"Why", 0}}
	}
	byName := map[string]model.Disk{}
	for _, d := range s.st.Disks {
		byName[d.Name] = d
	}
	l := s.lists[0]
	for i := range l.shown {
		d := byName[l.shown[i].key]
		wear := t.Sub.Render("–")
		if m := d.SMART; m != nil && m.WearPercentUsed != nil {
			wear = fmt.Sprintf("%.0f %%", *m.WearPercentUsed)
			if *m.WearPercentUsed >= 90 {
				wear = t.StatusBold(model.StatusWarn, wear)
			}
		}
		temp := t.Sub.Render("–")
		if d.TempCelsius != nil {
			st := model.StatusOK
			if d.TempThreshold != nil {
				st = checkEval(d.TempCelsius, d.TempThreshold)
			}
			temp = fmt.Sprintf("%.0f", *d.TempCelsius)
			if st >= model.StatusWarn {
				temp = t.StatusText(st, temp)
			}
		}
		sm, io := diskErrors(d)
		errs := t.Sub.Render("0")
		switch {
		case io > 0:
			errs = t.StatusBold(model.StatusCrit, fmt.Sprintf("%d I/O", io))
		case sm > 0:
			errs = t.StatusText(model.StatusWarn, fmt.Sprint(sm))
		}
		cells := map[string]string{"Disk": d.Name, "Type": diskType(d), "Model": orDash(strings.TrimSpace(d.Vendor + " " + d.Model)),
			"Serial": orDash(d.Serial), ">Size": bytesSI(d.SizeBytes), ">°C": temp, ">Wear": wear, "SMART": smartCell(t, d),
			">Err": errs, ">Errors": errs, "Why": t.Dim.Render(orDash(why(s.st.Findings, d.Name)))}
		var r row
		for _, c := range cols {
			r.cells = append(r.cells, cells[c.title])
		}
		l.shown[i].row = r
	}
	return l.render(t, cols, s.w, maxRows)
}

func (s *storageTab) disk(name string) (model.Disk, bool) {
	for _, d := range s.st.Disks {
		if d.Name == name {
			return d, true
		}
	}
	return model.Disk{}, false
}

// diskDetail: the selected disk as three aligned key/value columns
// (identity · gauges · counters); a three-line block below 100 columns.
func (s *storageTab) diskDetail(t *Theme) string {
	it, ok := s.lists[0].current()
	if !ok {
		return ""
	}
	d, _ := s.disk(it.key)
	where := []string{}
	if len(d.Holders) > 0 {
		where = append(where, strings.Join(d.Holders, ",")+" member")
	}
	if len(d.Mounted) > 0 {
		where = append(where, "mounted "+strings.Join(d.Mounted, ","))
	}
	where = append(where, "enter full SMART page")
	head := t.HeadingFocus(d.Name, "", strings.Join(where, " · "), s.w)
	sm, io := diskErrors(d)
	temp := t.Sub.Render("no sensor")
	if d.TempCelsius != nil {
		sen := model.Sensor{Value: d.TempCelsius, Unit: model.UnitCelsius, Thresholds: d.TempThreshold}
		temp = sensorGaugeLine(t, sen, 14)
	}
	smartLine := smartCell(t, d)
	if d.SMART != nil && d.SMART.Passed != nil && !*d.SMART.Passed {
		smartLine = t.inlineStatus(model.StatusCrit, "FAILED")
	}
	ioTxt := fmt.Sprint(d.KernelErrors)
	if d.KernelErrors > 0 {
		ioTxt = t.StatusBold(model.StatusCrit, ioTxt)
	}
	if g := growth(t, "disk."+d.Name+"."); g != "" && io > 0 {
		ioTxt += " " + g
	}
	u := func(v *uint64) string {
		if v == nil {
			return ""
		}
		if *v > 0 {
			return t.StatusBold(model.StatusWarn, fmt.Sprint(*v))
		}
		return fmt.Sprint(*v)
	}
	var m model.SMART
	if d.SMART != nil {
		m = *d.SMART
	}
	poh := ""
	if m.PowerOnHours != nil {
		poh = fmt.Sprintf("%d h", *m.PowerOnHours) + t.Sub.Render(fmt.Sprintf(" (%.1f y)", float64(*m.PowerOnHours)/8766))
	}
	if r := d.Remote; r != nil { // a volume of a storage system: its path, not SMART
		path := r.Session
		if r.Portal != "" {
			path += " via " + r.Portal
		}
		if s.w < 100 {
			return head + "\n" + t.Dim.Render(transportName(r)+" ") + orDash(r.Target) + "\n" + t.Dim.Render("State ") + orDash(remoteState(t, r)) +
				t.Dim.Render("  kernel I/O errors ") + ioTxt
		}
		c1 := kvBlock(t, []kvPair{{"Volume", strings.TrimSpace(d.Vendor + " " + d.Model)}, {"Size", bytesSI(d.SizeBytes)}, {"Path", d.Path + " · " + transportName(r)}}, "")
		c2 := kvBlock(t, []kvPair{{"Target", r.Target}, {"Session", path}, {"State", remoteState(t, r)}}, "")
		c3 := kvBlock(t, []kvPair{{"Kernel I/O", ioTxt}, {"SCSI cmds", cmdErrors(t, d)}, {"SMART", t.Sub.Render("on the storage system")}}, "")
		return head + "\n" + kvColumns(s.w, c1, c2, c3)
	}
	if s.w < 100 {
		l1 := t.Dim.Render("Model ") + orDash(d.Model) + t.Dim.Render("  S/N ") + t.Bold.Render(orDash(d.Serial)) + t.Dim.Render("  fw ") + orDash(d.Firmware)
		l2 := t.Dim.Render("SMART ") + smartLine + t.Dim.Render("  realloc ") + orDash(stripStyle(u(m.Reallocated))) + t.Dim.Render("  I/O err ") + ioTxt
		l3 := t.Dim.Render("Temp  ") + temp
		return head + "\n" + l1 + "\n" + l2 + "\n" + l3
	}
	c1 := kvBlock(t, []kvPair{{"Model", strings.TrimSpace(d.Vendor + " " + d.Model)}, {"Serial", t.Bold.Render(d.Serial)},
		{"Firmware", d.Firmware}, {"Path", d.Path + " · " + d.Interface}}, "")
	c2 := kvBlock(t, []kvPair{{"Temp", temp}, {"Realloc", u(m.Reallocated)}, {"Pending", u(m.Pending)}, {"SMART", smartLine}}, "")
	c3 := kvBlock(t, []kvPair{{"Kernel I/O", ioTxt}, {"SCSI cmds", cmdErrors(t, d)}, {"SMART errors", fmt.Sprint(sm)}, {"CRC errors", u(m.CRCErrors)}, {"Power-on", poh}}, "")
	return head + "\n" + kvColumns(s.w, c1, c2, c3)
}

// growth renders "↑ +N this session" for counters under prefix.
func growth(t *Theme, prefix string) string {
	var n uint64
	for _, c := range t.sess.counters(func(k string) bool { return strings.HasPrefix(k, prefix) }) {
		n += c.SinceStart
	}
	if n == 0 {
		return ""
	}
	return t.StatusText(model.StatusCrit, fmt.Sprintf("↑ +%d this session", n))
}

// raid: software arrays as a table, hardware controllers as trees
// (controller → virtual drives → physical drives → battery).
func (s *storageTab) raid(t *Theme, maxRows int) string {
	l := s.lists[1]
	if len(l.all) == 0 {
		return t.Sub.Render("   no software or hardware RAID found")
	}
	var out []string
	sel, _ := l.current()
	if len(s.st.MDArrays) > 0 {
		out = append(out, t.HeadingRight("Software RAID", "", "/proc/mdstat", s.w))
		cols := []column{{"Array", 6}, {"Level", 7}, {">Size", 8}, {"State", 22}, {"Members", 0}, {"Sync", 18}}
		var rows []row
		selIdx := -1
		for _, a := range s.st.MDArrays {
			state := fmt.Sprintf("%s %d/%d", a.State, a.ActiveDevices, a.Devices)
			if a.Degraded {
				state = t.StatusBold(model.StatusCrit, fmt.Sprintf("degraded %d/%d active", a.ActiveDevices, a.Devices))
			}
			var mem []string
			for _, m := range a.Members {
				g, txt := t.Dot(model.StatusOK), m.Device
				if m.Failed {
					g, txt = t.Dot(model.StatusCrit), t.StatusText(model.StatusCrit, m.Device+" faulty")
				}
				mem = append(mem, g+" "+txt)
			}
			sync := t.Sub.Render("idle")
			if a.SyncAction != "" {
				sync = a.SyncAction
				if a.SyncPercent != nil {
					sync += fmt.Sprintf(" %.1f %%", *a.SyncPercent)
				}
			} else if a.Degraded {
				sync = t.StatusText(model.StatusWarn, "not running")
			}
			if a.Name == sel.key {
				selIdx = len(rows)
			}
			rows = append(rows, row{cells: []string{a.Name, a.Level, bytesGB(a.SizeBytes), state, strings.Join(mem, "  "), sync}, status: a.Status})
		}
		out = append(out, renderList(t, cols, rows, selIdx, s.w, len(rows)+1))
	}
	for _, c := range s.st.Controllers {
		out = append(out, "", t.HeadingRight("Hardware RAID", "", c.Tool, s.w))
		var rows []row
		selIdx := -1
		add := func(key, text string, st model.Status) {
			if key != "" && key == sel.key {
				selIdx = len(rows)
			}
			rows = append(rows, row{cells: []string{text}, status: st})
		}
		ctl := strings.TrimSpace(fmt.Sprintf("c%d %s", c.Index, c.Model))
		if c.Serial != "" {
			ctl += t.Dim.Render(" · S/N " + c.Serial)
		}
		if c.Firmware != "" {
			ctl += t.Dim.Render(" · fw " + c.Firmware)
		}
		add("", t.Bold.Render(ctl), c.Status)
		guide := t.Sub
		add("", guide.Render("├─ ")+t.Dim.Render("Virtual drives"), model.StatusOK)
		for i, v := range c.VDs {
			g := map[bool]string{true: "│  └─ ", false: "│  ├─ "}[i == len(c.VDs)-1]
			add("VD "+v.ID, guide.Render(g)+t.Dot(v.Status)+" "+t.Bold.Render("VD "+v.ID)+t.Dim.Render(" "+strings.TrimSpace(v.Level+" · "+v.SizeText+" · ")+v.State+" "+v.Name), v.Status)
		}
		last := c.Battery == nil
		add("", guide.Render(map[bool]string{true: "└─ ", false: "├─ "}[last])+t.Dim.Render("Physical drives"), model.StatusOK)
		pre := map[bool]string{true: "   ", false: "│  "}[last]
		for i, p := range c.PDs {
			g := pre + map[bool]string{true: "└─ ", false: "├─ "}[i == len(c.PDs)-1]
			txt := p.Slot + " " + p.State
			if p.Status >= model.StatusWarn {
				txt = t.StatusBold(p.Status, txt)
			}
			add("PD "+p.Slot, guide.Render(g)+t.Dot(p.Status)+" "+txt+t.Dim.Render(" · "+strings.TrimSpace(p.Model+" · S/N "+orDash(p.Serial))), p.Status)
		}
		if b := c.Battery; b != nil {
			add("BBU "+c.Model, guide.Render("└─ ")+t.Dim.Render("Battery / CacheVault ")+t.Dot(b.Status)+" "+b.State, b.Status)
		}
		for i := range rows {
			rows[i].noDot = true
		}
		out = append(out, strings.Join(strings.Split(renderList(t, []column{{"", 0}}, rows, selIdx, s.w, len(rows)+1), "\n")[1:], "\n"))
	}
	res := strings.Split(strings.Join(out, "\n"), "\n")
	if len(res) > maxRows+2 {
		res = res[:maxRows+2]
	}
	return strings.Join(res, "\n")
}

// raidDetail: the selected array, drive or controller as a key/value block.
func (s *storageTab) raidDetail(t *Theme) string {
	it, ok := s.lists[1].current()
	if !ok {
		return ""
	}
	ri := s.raidItems()[it.key]
	switch {
	case ri.md != nil:
		a := ri.md
		var failed []string
		for _, m := range a.Members {
			if m.Failed {
				failed = append(failed, m.Device)
			}
		}
		state := a.State
		if a.Degraded {
			state = t.StatusBold(model.StatusCrit, "degraded")
		}
		f := strings.Join(failed, ", ")
		if f != "" {
			f = t.StatusText(model.StatusCrit, f)
		}
		return t.HeadingFocus(a.Name, "", "enter array page", s.w) + "\n" + kvBlock(t, []kvPair{
			{"Level", fmt.Sprintf("%s · %d devices · %s", a.Level, a.Devices, bytesGB(a.SizeBytes))},
			{"State", state}, {"Failed", f}, {"Sync", a.SyncAction}}, "none")
	case ri.pd != nil:
		p := ri.pd
		return t.HeadingFocus("PD "+p.Slot, "", "enter drive page", s.w) + "\n" + kvBlock(t, []kvPair{
			{"Model", p.Model}, {"Serial", t.Bold.Render(p.Serial)}, {"State", t.StatusText(p.Status, p.State)}, {"Group", p.Group}}, "")
	case ri.vd != nil:
		v := ri.vd
		return t.HeadingFocus("VD "+v.ID, "", "enter page", s.w) + "\n" + kvBlock(t, []kvPair{
			{"Level", v.Level}, {"Size", v.SizeText}, {"State", t.StatusText(v.Status, v.State)}, {"Name", v.Name}}, "")
	}
	return ""
}

func (s *storageTab) filesystems(t *Theme, maxRows int) string {
	cols := []column{{"Mountpoint", 0}, {"Type", 6}, {"Size", 8}, {"Used", 20}, {"Inodes", 6}, {"", 2}}
	byMount := map[string]model.Filesystem{}
	for _, f := range s.st.Filesystems {
		byMount[f.Mountpoint] = f
	}
	l := s.lists[2]
	for i := range l.shown {
		f := byMount[l.shown[i].key]
		ro := ""
		if f.ReadOnly {
			ro = t.StatusText(model.StatusCrit, "ro")
		}
		l.shown[i].row = row{cells: []string{f.Mountpoint, f.FSType, bytesGB(f.SizeBytes), usageBar(t, f.UsedPercent, f.Status, 12),
			optF(f.InodesUsedPercent, "%.0f%%"), ro}}
	}
	return l.render(t, cols, s.w, maxRows)
}

// Detail is the full page of the selected disk, RAID item or filesystem.
func (s *storageTab) Detail(t *Theme, w int) (string, string) {
	if s.st == nil {
		return "", ""
	}
	it, ok := s.lists[s.view].current()
	if !ok {
		return "", ""
	}
	doc := newDoc(t, w)
	switch s.view {
	case 0:
		d, _ := s.disk(it.key)
		return "Disk " + d.Name, s.diskPage(doc, d)
	case 1:
		return it.key, s.raidPage(doc, s.raidItems()[it.key], it.key)
	}
	for _, f := range s.st.Filesystems {
		if f.Mountpoint != it.key {
			continue
		}
		doc.section("Filesystem", t.Badge(f.Status))
		ro := "read-write"
		if f.ReadOnly {
			ro = t.StatusText(model.StatusCrit, "READ-ONLY")
		}
		doc.kv([2]string{"Device", f.Source}, [2]string{"Type", f.FSType}, [2]string{"Mode", ro},
			[2]string{"Size", bytesGB(f.SizeBytes)}, [2]string{"Used", bytesGB(f.UsedBytes) + "  " + usageBar(t, f.UsedPercent, f.Status, 24)},
			[2]string{"Inodes used", optF(f.InodesUsedPercent, "%.0f%%")})
		doc.findings(model.ModStorage, relatedFindings(s.st.Findings, f.Mountpoint))
	}
	return "Filesystem " + it.key, doc.String()
}

// smartCounterKey maps ATA attribute IDs to the session counter that
// tracks their raw value (for the "Δ session" column).
var smartCounterKey = map[int]string{5: "reallocated", 187: "reported_uncorrectable", 197: "pending", 198: "offline_uncorrectable", 199: "crc"}

// attrStatus: CRIT at or below threshold (or failing now), WARN when
// smartctl reports a past failure.
func attrStatus(a model.SMARTAttr) model.Status {
	switch {
	case a.WhenFailed == "now" || (a.Thresh > 0 && a.Value <= a.Thresh):
		return model.StatusCrit
	case a.WhenFailed != "":
		return model.StatusWarn
	}
	return model.StatusOK
}

// grouped writes digits in groups of three: 27458112 → "27 458 112".
func grouped(v uint64) string {
	s := fmt.Sprint(v)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + " " + s[i:]
	}
	return s
}

// diskPage (canvas Detail-Disk): Identity beside Sensors and SMART health,
// the attribute table with session growth, counters, findings.
func (s *storageTab) diskPage(doc *doc, d model.Disk) string {
	t := doc.t
	w := doc.w
	lw := w
	if w >= 100 {
		lw = w/2 - 1
	}
	rw := w - lw - 2
	if w < 100 {
		rw = w
	}
	// Identity
	iface := strings.ToUpper(d.Interface)
	if d.Kind != "" {
		iface = strings.TrimSpace(diskType(d) + " over " + d.Interface)
	}
	capa := bytesSI(d.SizeBytes)
	if d.RotationRPM != nil {
		capa += fmt.Sprintf(" · %d rpm", *d.RotationRPM)
	}
	var where []string
	where = append(where, d.Mounted...)
	for _, h := range d.Holders {
		where = append(where, h+" member")
	}
	var flags []string
	if d.Removable {
		flags = append(flags, "removable")
	}
	if d.ReadOnly {
		flags = append(flags, t.StatusText(model.StatusCrit, "read-only"))
	}
	idRows := []kvPair{{"Device", d.Path}, {"Model", strings.TrimSpace(d.Vendor + " " + d.Model)}, {"Serial", t.Bold.Render(d.Serial)},
		{"WWN", d.WWN}, {"Firmware", d.Firmware}, {"Capacity", capa}, {"Interface", iface}, {"Used by", strings.Join(where, " · ")},
		{"Location", d.Location}}
	if len(flags) > 0 {
		idRows = append(idRows, kvPair{"Flags", strings.Join(flags, ", ")})
	}
	if r := d.Remote; r != nil {
		idRows[6] = kvPair{"Interface", transportName(r) + t.Sub.Render(" · a storage system's volume")}
		idRows = append(idRows, kvPair{"Target", r.Target}, kvPair{"Portal", r.Portal},
			kvPair{"Session", r.Session}, kvPair{"State", remoteState(t, r)})
	}
	left := t.Heading("Identity", "", lw) + "\n" + kvBlock(t, idRows, "")

	// Sensors + SMART health
	var right []string
	right = append(right, t.HeadingRight("Sensors", "", "session", rw))
	if d.TempCelsius != nil {
		sen := model.Sensor{Name: "Temperature", Value: d.TempCelsius, Unit: model.UnitCelsius, Thresholds: d.TempThreshold, Status: model.StatusOK}
		if d.TempThreshold != nil {
			sen.Status = checkEval(d.TempCelsius, d.TempThreshold)
		}
		gw := max(8, min(16, rw-36))
		line := t.Dim.Render("Temperature ") + sensorGaugeLine(t, sen, gw)
		if h := t.sess.history("storage/" + d.Name + "/temp"); len(h) > 1 {
			line += " " + spark(h, max(4, rw-lipgloss.Width(line)-1), 0, t.StatusColor(sen.Status))
		}
		right = append(right, line)
		if th := thresholdText(sen); th != "" {
			right = append(right, t.Sub.Render(pad("", 12)+"limits "+th))
		}
	} else {
		right = append(right, t.Sub.Render("no temperature reading"))
	}
	m := d.SMART
	right = append(right, "")
	if m == nil {
		why := "no SMART data (needs root, or smartctl/nvme-cli for this device)"
		if !d.Local() {
			why = "a volume of a storage system: its disks are checked there"
		}
		right = append(right, t.Heading("SMART health", "", rw), t.Sub.Render(why))
	} else {
		src := "via " + m.Source
		if !d.SMARTAt.IsZero() {
			src += " · read " + ago(d.SMARTAt, s.st.CollectedAt)
		}
		right = append(right, t.HeadingRight("SMART health", "", src, rw))
		overall := ""
		if m.Passed != nil {
			overall = t.StatusBold(model.StatusOK, "● PASSED")
			if !*m.Passed {
				overall = t.StatusBold(model.StatusCrit, "✖ FAILED")
				for _, a := range m.Attributes {
					if attrStatus(a) == model.StatusCrit {
						overall += t.Dim.Render(fmt.Sprintf(" attribute %d at threshold", a.ID))
						break
					}
				}
			}
		}
		selfTest := t.Sub.Render("no failure logged")
		if m.SelfTestFailed {
			selfTest = t.StatusText(model.StatusCrit, "✖ a self-test failed")
		}
		power := ""
		if m.PowerOnHours != nil {
			power = grouped(*m.PowerOnHours) + " h"
		}
		if m.PowerCycles != nil {
			power += fmt.Sprintf(" · %s cycles", grouped(*m.PowerCycles))
		}
		rows := []kvPair{{"Overall", overall}, {"Self-test", selfTest}, {"Power-on", strings.TrimPrefix(power, " · ")}}
		if m.WearPercentUsed != nil {
			wst := model.StatusOK
			if *m.WearPercentUsed >= 90 {
				wst = model.StatusWarn
			}
			rows = append(rows, kvPair{"Wear", usageBar(t, m.WearPercentUsed, wst, 16) + t.Dim.Render(" of endurance")})
		}
		if m.AvailableSpare != nil {
			sp := fmt.Sprintf("%.0f %%", *m.AvailableSpare)
			if m.AvailableSpareThr != nil {
				sp += t.Dim.Render(fmt.Sprintf(" (threshold %.0f %%)", *m.AvailableSpareThr))
			}
			rows = append(rows, kvPair{"Spare", sp})
		}
		if m.NVMeCriticalWarn != nil && *m.NVMeCriticalWarn != 0 {
			rows = append(rows, kvPair{"Critical", t.StatusText(model.StatusCrit, model.NVMeWarningText(*m.NVMeCriticalWarn))})
		}
		if len(m.Attributes) == 0 { // NVMe/SAS: the counters have no attribute table
			for _, c := range []struct {
				k string
				v *uint64
			}{{"Reallocated", m.Reallocated}, {"Pending", m.Pending}, {"Media errors", m.MediaErrors}, {"Error log", m.ErrorLogEntries},
				{"Grown defects", m.GrownDefects}, {"Uncorrected", m.UncorrectedErrors}, {"Unsafe shutdn", m.UnsafeShutdowns}} {
				if c.v != nil {
					v := grouped(*c.v)
					if *c.v > 0 && c.k != "Unsafe shutdn" && c.k != "Error log" {
						v = t.StatusText(model.StatusWarn, v)
					}
					rows = append(rows, kvPair{c.k, v})
				}
			}
		}
		if m.DataWrittenBytes != nil {
			rows = append(rows, kvPair{"Written", bytesSI(*m.DataWrittenBytes)})
		}
		right = append(right, kvBlock(t, rows, ""))
	}
	if w >= 100 {
		lh := lipgloss.Height(left)
		rs := strings.Join(right, "\n")
		h := max(lh, lipgloss.Height(rs))
		var out []string
		ll, rl := strings.Split(left, "\n"), strings.Split(rs, "\n")
		for i := 0; i < h; i++ {
			a, b := "", ""
			if i < len(ll) {
				a = truncate(ll[i], lw)
			}
			if i < len(rl) {
				b = truncate(rl[i], rw)
			}
			out = append(out, padCell(a, lw+2, false)+b)
		}
		doc.add(strings.Join(out, "\n"))
	} else {
		doc.add(left, "")
		doc.add(right...)
	}

	// SMART attributes
	if m != nil && len(m.Attributes) > 0 {
		attrs := append([]model.SMARTAttr(nil), m.Attributes...)
		sort.SliceStable(attrs, func(i, j int) bool {
			si, sj := attrStatus(attrs[i]), attrStatus(attrs[j])
			if si != sj {
				return si > sj
			}
			return attrs[i].ID < attrs[j].ID
		})
		growth := map[string]counterChange{}
		for _, c := range t.sess.counters(func(k string) bool { return strings.HasPrefix(k, "disk."+d.Name+".smart.") }) {
			growth[strings.TrimPrefix(c.Key, "disk."+d.Name+".smart.")] = c
		}
		var rows []row
		for _, a := range attrs {
			st := attrStatus(a)
			if st == model.StatusOK && a.ID == 197 && a.Raw > 0 {
				st = model.StatusWarn
			}
			delta, note := "", ""
			if k, ok := smartCounterKey[a.ID]; ok {
				if c, ok := growth[k]; ok {
					delta = fmt.Sprintf("+%d", c.SinceStart)
					if c.SinceStart > 0 {
						delta = t.StatusText(model.StatusWarn, delta)
					}
				}
			}
			switch {
			case attrStatus(a) == model.StatusCrit:
				note = t.StatusText(model.StatusCrit, "at threshold")
			case a.WhenFailed != "":
				note = t.StatusText(model.StatusWarn, "failed "+a.WhenFailed)
			case a.ID == 197 && a.Raw > 0:
				note = t.StatusText(model.StatusWarn, "pending remap")
			}
			name := a.Name
			if st >= model.StatusWarn {
				name = t.Bold.Render(name)
			}
			rows = append(rows, row{cells: []string{fmt.Sprint(a.ID), name, fmt.Sprint(a.Value), fmt.Sprint(a.Worst), fmt.Sprint(a.Thresh),
				rawText(a), delta, note}, status: st})
		}
		cols := []column{{">ID", 4}, {"Attribute", 26}, {">Value", 6}, {">Worst", 6}, {">Thresh", 7}, {">Raw", 14}, {">Δ session", 10}, {"", 0}}
		doc.add("", t.HeadingRight("SMART attributes", "", countLabel(len(attrs), "attribute", "attributes"), w))
		doc.add(renderList(t, cols, rows, -1, w, len(rows)+1))
	}
	ioErrorsSection(doc, d)
	kernelLogSection(doc, d)
	doc.counters(t.sess.counters(func(k string) bool { return strings.HasPrefix(k, "disk."+d.Name+".") }), "disk."+d.Name+".")
	doc.findings(model.ModStorage, relatedFindings(s.st.Findings, d.Name))
	return doc.String()
}

func (s *storageTab) raidPage(doc *doc, it raidItem, name string) string {
	t := doc.t
	switch {
	case it.md != nil:
		a := it.md
		doc.section("Array", t.Badge(a.Status))
		sync := a.SyncAction
		if a.SyncPercent != nil {
			sync += fmt.Sprintf(" %.1f%%", *a.SyncPercent)
		}
		if a.SyncFinish != "" {
			sync += " · finish " + a.SyncFinish
		}
		doc.kv([2]string{"Level", a.Level}, [2]string{"State", a.State}, [2]string{"Devices", fmt.Sprintf("%d active of %d", a.ActiveDevices, a.Devices)},
			[2]string{"Size", bytesGB(a.SizeBytes)}, [2]string{"Sync", sync})
		doc.section("Members", "")
		for _, m := range a.Members {
			st := model.StatusOK
			state := "in sync"
			if m.Failed {
				st, state = model.StatusCrit, "FAILED"
			}
			doc.add(t.Dot(st) + " " + pad(m.Device, 12) + t.StatusText(st, state))
		}
		doc.findings(model.ModStorage, relatedFindings(s.st.Findings, a.Name))
		return doc.String()
	case it.ctrl != nil:
		c := it.ctrl
		switch {
		case it.vd != nil:
			v := it.vd
			doc.section("Virtual drive "+v.ID, t.Badge(v.Status))
			doc.kv([2]string{"Name", v.Name}, [2]string{"Level", v.Level}, [2]string{"State", v.State}, [2]string{"Size", v.SizeText})
		case it.pd != nil:
			p := it.pd
			doc.section("Physical drive "+p.Slot, t.Badge(p.Status))
			doc.kv([2]string{"State", p.State}, [2]string{"Drive group", p.Group}, [2]string{"Model", p.Model}, [2]string{"Serial", p.Serial},
				[2]string{"WWN", p.WWN}, [2]string{"Firmware", p.Firmware}, [2]string{"Media", p.Media}, [2]string{"Interface", p.Interface},
				[2]string{"Size", p.SizeText})
		case it.bat != nil:
			b := it.bat
			doc.section(b.Type, t.Badge(b.Status))
			doc.kv([2]string{"Model", b.Model}, [2]string{"State", b.State}, [2]string{"Temperature", optF(b.TempCelsius, "%.0f°C")})
		}
		doc.section("Controller", t.Badge(c.Status))
		doc.kv([2]string{"Model", c.Model}, [2]string{"Serial", c.Serial}, [2]string{"Firmware", c.Firmware},
			[2]string{"State", c.State}, [2]string{"Read via", c.Tool})
		keys := []string{name}
		if it.vd != nil {
			keys = append(keys, it.vd.ID)
		}
		if it.pd != nil {
			keys = append(keys, it.pd.Slot)
		}
		doc.findings(model.ModStorage, relatedFindings(s.st.Findings, keys...))
	}
	return doc.String()
}

func checkEval(v *float64, th *model.Thresholds) model.Status { return check.Eval(v, th) }

// rawText is a SMART raw value as smartctl prints it ("21 (Min/Max 18/40)"),
// else the number.
func rawText(a model.SMARTAttr) string {
	if a.RawString != "" {
		return a.RawString
	}
	return grouped(a.Raw)
}

// ioErrorsSection explains the two error sources on a disk page: the
// kernel log (failed reads and writes) and the SCSI command error counter
// (which also counts rejected commands, Tyr's SMART queries among them).
// kernelLogSection lists the kernel log lines that name the disk (its
// partitions, its ATA link, its NVMe controller or iSCSI connection),
// wrapped so every entry is readable in full.
func kernelLogSection(doc *doc, d model.Disk) {
	if len(d.KernelLines) == 0 {
		return
	}
	t := doc.t
	doc.section("Kernel log", t.Dim.Render(fmt.Sprintf("last %d lines naming %s · newest last · journalctl -k -b | grep -w %s", len(d.KernelLines), d.Name, d.Name)))
	for _, l := range d.KernelLines {
		stamp, msg := "", l
		if len(l) > 20 && l[4] == '-' && l[19] == ' ' {
			stamp, msg = l[:19], l[20:]
		}
		st := t.Message.Render
		if low := strings.ToLower(msg); strings.Contains(low, "error") || strings.Contains(low, "fail") || strings.Contains(low, "exception") {
			st = lipgloss.NewStyle().Foreground(t.StatusColor(model.StatusWarn)).Render
		}
		doc.add(hang(" "+t.Dim.Render(stamp)+" ", msg, doc.w-1, st))
	}
}

func ioErrorsSection(doc *doc, d model.Disk) {
	t := doc.t
	if d.IOErrors == nil && d.KernelErrors == 0 {
		return
	}
	doc.section("I/O errors", t.Dim.Render("what failed, and where to look"))
	kern := fmt.Sprint(d.KernelErrors)
	if d.KernelErrors > 0 {
		kern = t.StatusBold(model.StatusCrit, kern)
	}
	rows := [][2]string{{"Kernel errors", kern + t.Dim.Render(" failed reads and writes (lines below)")}}
	if d.KernelLast != "" {
		rows = append(rows, [2]string{"Last", d.KernelLast})
	}
	if d.IOErrors != nil {
		rows = append(rows, [2]string{"SCSI commands", cmdErrors(t, d)})
	}
	doc.kv(rows...)
	var hints []string
	if d.IOErrors != nil {
		hints = append(hints, "SCSI command errors (ioerr_cnt) count every command that ended with an error - also commands the drive "+
			"or controller rejects, such as SMART queries it does not pass through. Only new errors Tyr did not cause are a finding.")
	}
	switch {
	case !d.Local():
		hints = append(hints, "Kernel I/O errors on a volume mean the path to the storage system failed: check the session "+
			"(iscsiadm -m session -P 3), the network to the portal, and the target's own logs.")
	case d.KernelErrors > 0:
		hints = append(hints, "Kernel I/O errors are reads or writes the disk could not complete: check SMART (pending and reallocated "+
			"sectors), the cable or backplane (CRC errors), and journalctl -k for the disk.")
	}
	for _, h := range hints {
		doc.add(lipgloss.NewStyle().Width(max(40, doc.w-2)).Render(t.Sub.Render(h)))
	}
}
