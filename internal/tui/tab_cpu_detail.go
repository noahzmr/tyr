package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/model"
)

// coresDetail: the selected CPU as a key/value block (location, state,
// MCE with growth) and its idle-state residency as bars.
func (c *cpuTab) coresDetail(t *Theme) string {
	if len(c.cpu.Logical) == 0 {
		return ""
	}
	l := c.cpu.Logical[c.sel]
	id := cpuKey(l.ID)
	head := t.HeadingFocus(id+" · idle states", "", "enter full page with graphs", c.w)
	state := t.inlineStatus(model.StatusOK, "online")
	if !l.Online {
		state = t.inlineStatus(model.StatusCrit, "offline")
	}
	mce := uint64(0)
	if l.MCEExceptions != nil {
		mce += *l.MCEExceptions
	}
	if l.MCELogged != nil {
		mce += *l.MCELogged
	}
	mceTxt := fmt.Sprint(mce)
	if g := growthSuffix(t, "."+id); g != "" {
		mceTxt += " " + g
	}
	loc := fmt.Sprintf("socket %s · core %s · node %s", intOrDash(l.SocketID), intOrDash(l.CoreID), intOrDash(l.NUMANode))
	left := kvBlock(t, []kvPair{{"Location", loc}, {"State", state}, {"MCE", mceTxt}, {"Microcode", l.Microcode}}, "")
	var total uint64
	for _, s := range l.IdleStates {
		if s.TimeUs != nil {
			total += *s.TimeUs
		}
	}
	var bars []string
	for _, s := range l.IdleStates {
		pct := 0.0
		if s.TimeUs != nil && total > 0 {
			pct = float64(*s.TimeUs) / float64(total) * 100
		}
		bars = append(bars, t.Dim.Render(pad(s.Name, 6))+memBar(t, pct, model.StatusOK, 24)+fmt.Sprintf(" %3.0f %%", pct))
	}
	if len(bars) == 0 {
		bars = []string{t.Sub.Render("no idle-state data")}
	}
	right := t.Dim.Render("Idle state residency (since boot)") + "\n" + strings.Join(bars, "\n")
	if c.w < 100 {
		return head + "\n" + left
	}
	return head + "\n" + kvColumns(c.w, left, right)
}

// growthSuffix is "↑ +N this session" for counters ending in suffix.
func growthSuffix(t *Theme, suffix string) string {
	var n uint64
	for _, c := range t.sess.counters(func(k string) bool { return strings.HasSuffix(k, suffix) }) {
		n += c.SinceStart
	}
	if n == 0 {
		return ""
	}
	return t.StatusText(model.StatusWarn, fmt.Sprintf("↑ +%d this session", n))
}

// detail describes the selected logical CPU in three lines: identity,
// health counters, and idle-state residency.
func (c *cpuTab) detail(t *Theme) string {
	if len(c.cpu.Logical) == 0 {
		return ""
	}
	l := c.cpu.Logical[c.sel]
	state := t.StatusText(model.StatusOK, "online")
	if !l.Online {
		state = t.StatusText(model.StatusCrit, "OFFLINE")
	}
	id := []string{fmt.Sprintf("socket %d core %d node %d", l.SocketID, l.CoreID, l.NUMANode)}
	if l.CoreType != "" {
		id = append(id, coreTypeName(l.CoreType))
	}
	if l.PrefRank != nil {
		id = append(id, fmt.Sprintf("preferred-core rank %d", *l.PrefRank))
	}
	if l.Siblings != "" {
		id = append(id, "SMT siblings "+l.Siblings)
	}
	if l.Microcode != "" {
		id = append(id, "µcode "+l.Microcode)
	}
	s1 := t.Section.Render(fmt.Sprintf("cpu%d", l.ID)) + " " + state + t.Dim.Render(" · ") + strings.Join(id, t.Dim.Render(" · "))

	temp := optF(l.TempCelsius, "%.0f°C")
	if l.TempCelsius != nil {
		temp = t.StatusText(tempStatus(c.cpu, &l), temp)
	}
	s2 := fmt.Sprintf("load %s · %s MHz (%s–%s) · %s · throttle %s · MCE %s exceptions, %s logged",
		optF(l.UtilizationPercent, "%.0f%%"), optF(l.FreqMHz, "%.0f"), optF(l.MinFreqMHz, "%.0f"), optF(l.MaxFreqMHz, "%.0f"),
		temp, optU(l.ThrottleCount), optU(l.MCEExceptions), optU(l.MCELogged))
	if l.CoreTest != "" {
		st := model.StatusOK
		if l.CoreTest == model.CoreTestFail || l.CoreTest == model.CoreTestTimeout {
			st = model.StatusCrit
		}
		s2 += " · core test " + t.StatusText(st, string(l.CoreTest))
		if l.CoreTestDetail != "" {
			s2 += t.Dim.Render(" (" + l.CoreTestDetail + ")")
		}
	}
	return s1 + "\n  " + s2 + "\n  " + t.Dim.Render(idleText(l.IdleStates))
}

func coreTypeName(s string) string {
	switch s {
	case "P":
		return "performance core"
	case "E":
		return "efficiency core"
	}
	return s + " core"
}

// idleText shows each C-state's share of the CPU's idle residency.
func idleText(ss []model.IdleState) string {
	var total uint64
	for _, s := range ss {
		if s.TimeUs != nil {
			total += *s.TimeUs
		}
	}
	if len(ss) == 0 {
		return "no cpuidle states"
	}
	var parts []string
	for _, s := range ss {
		p := s.Name
		if s.TimeUs != nil && total > 0 {
			p += fmt.Sprintf(" %.0f%%", float64(*s.TimeUs)/float64(total)*100)
		}
		if s.Disabled {
			p += " (disabled)"
		}
		parts = append(parts, p)
	}
	return "idle residency: " + strings.Join(parts, " · ")
}

// deepestShare is the share of idle time spent in the deepest enabled state.
func deepestShare(ss []model.IdleState) string {
	var total uint64
	for _, s := range ss {
		if s.TimeUs != nil {
			total += *s.TimeUs
		}
	}
	for i := len(ss) - 1; i >= 0; i-- {
		if s := ss[i]; !s.Disabled && s.TimeUs != nil && total > 0 {
			return fmt.Sprintf("%s %.0f%%", s.Name, float64(*s.TimeUs)/float64(total)*100)
		}
	}
	return "–"
}

// coresView is the per-core health table.
func (c *cpuTab) coresView(t *Theme) string {
	x := c.cpu
	hasRank, hasType, hasTest := false, false, false
	for _, l := range x.Logical {
		hasRank = hasRank || l.PrefRank != nil
		hasType = hasType || l.CoreType != ""
		hasTest = hasTest || l.CoreTest != ""
	}
	// canvas CPU-Cores: platform-specific columns (P/E type, AMD rank)
	// only when they apply; at 80 columns CPU, State, Load, °C, Thr, MCE,
	// Test; at ≥150 also SMT siblings and the deepest C-state.
	wide, narrow := c.w >= 150, c.w < 100
	cols := []column{{"CPU", 5}}
	if !narrow {
		cols = append(cols, column{">Skt", 3}, column{">Core", 4}, column{">Node", 4}, column{"SMT", 6})
	}
	if hasType && !narrow {
		cols = append(cols, column{"Type", 4})
	}
	if hasRank && !narrow {
		cols = append(cols, column{">Rank", 4})
	}
	cols = append(cols, column{"State", 7}, column{"Load", 13})
	if !narrow {
		cols = append(cols, column{">MHz", 5}, column{">Max", 5})
	}
	cols = append(cols, column{">°C", 3}, column{">Thr", 4}, column{">MCE", 3}, column{"Test", 5})
	if wide || !narrow {
		cols = append(cols, column{">C-idle", 7})
	}
	c.syncList()
	byKey := map[string]model.LogicalCPU{}
	for _, l := range x.Logical {
		byKey[cpuKey(l.ID)] = l
	}
	for li := range c.list.shown {
		l := byKey[c.list.shown[li].key]
		state := "online"
		if !l.Online {
			state = "OFFLINE"
		}
		mce := "–"
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
		load := t.Sub.Render("–")
		if l.UtilizationPercent != nil {
			u := *l.UtilizationPercent
			n := min(8, max(0, int(u/100*8+0.5)))
			load = lipgloss.NewStyle().Foreground(t.Series[0]).Render(strings.Repeat("█", n)) + lipgloss.NewStyle().Foreground(t.Track).Render(strings.Repeat("░", 8-n)) + fmt.Sprintf("%4.0f", u)
		}
		if !l.Online {
			state = t.StatusBold(model.StatusCrit, "offline")
		}
		temp := optF(l.TempCelsius, "%.0f")
		if st := tempStatus(c.cpu, &l); l.TempCelsius != nil && st >= model.StatusWarn {
			temp = t.StatusText(st, temp)
		}
		thr := optU(l.ThrottleCount)
		if l.ThrottleCount != nil && *l.ThrottleCount > 0 {
			thr = t.StatusBold(model.StatusWarn, thr)
		}
		if mce != "–" && mce != "0" {
			mce = t.StatusBold(model.StatusWarn, mce)
		}
		test := orDash(strings.ToLower(string(l.CoreTest)))
		if l.CoreTest == model.CoreTestFail || l.CoreTest == model.CoreTestTimeout {
			test = t.StatusBold(model.StatusCrit, test)
		}
		cells := map[string]string{"CPU": fmt.Sprintf("cpu%d", l.ID), ">Skt": intOrDash(l.SocketID), ">Core": intOrDash(l.CoreID),
			">Node": intOrDash(l.NUMANode), "SMT": orDash(l.Siblings), "Type": orDash(l.CoreType), ">Rank": optI(l.PrefRank, "%d"),
			"State": state, "Load": load, ">MHz": optF(l.FreqMHz, "%.0f"), ">Max": optF(l.MaxFreqMHz, "%.0f"),
			">°C": temp, ">Thr": thr, ">MCE": mce, "Test": test, ">C-idle": deepestShare(l.IdleStates)}
		var r row
		for _, col := range cols {
			r.cells = append(r.cells, cells[col.title])
		}
		c.list.shown[li].row = r
	}
	detail := c.coresDetail(t)
	avail := c.h - 1 - lipgloss.Height(detail)
	list := c.list.render(t, cols, c.w, listHeight(avail, len(c.list.shown), countNonOK(x.Findings)))
	top := strings.Join([]string{list, "", detail}, "\n")
	return top + "\n" + findingsSection(t, "CPU", x.Findings, x.Sources, c.h-lipgloss.Height(top), c.w)
}

func intOrDash(v int) string {
	if v < 0 {
		return "–"
	}
	return fmt.Sprint(v)
}

// detailsView is a scrollable page with everything known about the CPUs.
func (c *cpuTab) detailsView(t *Theme) string {
	x := c.cpu
	w := c.w
	var lines []string
	add := func(s ...string) { lines = append(lines, s...) }
	section := func(title, note string) {
		if len(lines) > 0 {
			add("")
		}
		add(t.Heading(title, note, w))
	}
	kvs := func(rows [][2]string) {
		var keep [][2]string
		for _, r := range rows {
			if strings.TrimSpace(r[1]) != "" {
				keep = append(keep, r)
			}
		}
		add(strings.Split(kv(t, keep), "\n")...)
	}

	section("Identification", "")
	fms := ""
	if x.Family != "" {
		fms = fmt.Sprintf("family %s · model %s · stepping %s", x.Family, x.ModelNumber, x.Stepping)
		if hex := famModelHex(x.Family, x.ModelNumber); hex != "" {
			fms += t.Dim.Render("  (" + hex + ")")
		}
	}
	kvs([][2]string{{"Vendor", x.Vendor}, {"Model", x.Model}, {"Signature", fms}, {"Microcode", microcodeText(t, x)}, {"Architecture", x.Arch}})

	section("Sockets", fmt.Sprintf("%d", len(x.Sockets)))
	if len(x.Sockets) == 0 {
		add(t.Dim.Render("no socket data (SMBIOS needs root)"))
	}
	for _, s := range x.Sockets {
		var parts []string
		if s.Cores != nil || s.Threads != nil {
			parts = append(parts, fmt.Sprintf("%s cores / %s threads", optI(s.Cores, "%d"), optI(s.Threads, "%d")))
		}
		parts = append(parts, fmt.Sprintf("%d threads online", s.Online))
		if s.MaxSpeedMHz != nil {
			parts = append(parts, fmt.Sprintf("max %.0f MHz", *s.MaxSpeedMHz))
		}
		if s.StatusText != "" {
			parts = append(parts, s.StatusText)
		}
		var ids []string
		if s.Serial != "" {
			ids = append(ids, "S/N "+s.Serial)
		}
		if s.PartNumber != "" {
			ids = append(ids, "PN "+s.PartNumber)
		}
		if s.PPIN != "" {
			ids = append(ids, "PPIN "+s.PPIN)
		}
		if s.AssetTag != "" {
			ids = append(ids, "asset "+s.AssetTag)
		}
		add(t.Dot(s.Status) + " " + t.Bold.Render(orDash(s.Designation)) + "  " + orDash(s.Version))
		add("    " + strings.Join(parts, t.Dim.Render(" · ")))
		if len(ids) > 0 {
			add("    " + strings.Join(ids, t.Dim.Render(" · ")))
		}
	}

	section("Topology", "")
	smt := x.SMTControl
	if x.SMTActive != nil {
		smt += fmt.Sprintf(" (active: %v)", *x.SMTActive)
	}
	kvs([][2]string{
		{"Counts", fmt.Sprintf("%d sockets · %d cores · %d threads", x.SocketCount, x.CoreCount, x.ThreadCount)},
		{"Online", x.Topology.Online}, {"Offline", x.Topology.Offline}, {"Present", x.Topology.Present}, {"Possible", x.Topology.Possible},
		{"SMT", smt}, {"Core types", coreTypeSummary(x)}, {"Isolated", x.Isolated}, {"nohz_full", x.NoHzFull},
	})

	section("Frequency and power", "")
	boost := ""
	if x.Boost != nil {
		boost = map[bool]string{true: "enabled", false: "disabled"}[*x.Boost]
	}
	kvs([][2]string{
		{"Driver", x.FreqDriver}, {"Governor", x.Governor}, {"EPP", x.EPP}, {"Boost", boost},
		{"Base / max", fmt.Sprintf("%s / %s MHz", optF(x.BaseFreqMHz, "%.0f"), optF(x.MaxFreqMHz, "%.0f"))},
		{"Idle states", idleSummary(x)},
	})

	section("Temperatures", "")
	if len(x.PackageTemps) == 0 {
		add(t.Dim.Render("no CPU temperature sensor"))
	}
	for _, s := range x.PackageTemps {
		if s.Value == nil {
			continue
		}
		th := ""
		if s.Thresholds != nil && s.Thresholds.UpperWarn != nil {
			th = t.Dim.Render(fmt.Sprintf("  warn %s · crit %s (%s)", check.FmtTemp(*s.Thresholds.UpperWarn), optF(s.Thresholds.UpperCrit, "%.0f°C"), s.Thresholds.Origin))
		}
		add(t.Dot(s.Status) + " " + pad(s.Name, 22) + t.StatusText(s.Status, check.FmtTemp(*s.Value)) + th + t.Dim.Render("  "+s.Source))
	}

	section("Caches", "")
	if len(x.Caches) == 0 {
		add(t.Dim.Render("no cache information"))
	}
	for _, k := range x.Caches {
		name := fmt.Sprintf("L%d", k.Level)
		switch k.Type {
		case "Data":
			name += "d"
		case "Instruction":
			name += "i"
		}
		d := fmt.Sprintf("%s × %d", bytesIEC(k.SizeBytes), k.Instances)
		var extra []string
		if k.Ways > 0 {
			extra = append(extra, fmt.Sprintf("%d-way", k.Ways))
		}
		if k.LineBytes > 0 {
			extra = append(extra, fmt.Sprintf("%d B lines", k.LineBytes))
		}
		if k.SharedBy != "" {
			extra = append(extra, "shared by CPUs "+k.SharedBy)
		}
		add(pad(name, 5) + pad(d, 16) + t.Dim.Render(strings.Join(extra, " · ")))
	}

	vulnNote := ""
	nv := 0
	for _, v := range x.Vulnerabilities {
		if v.Status == model.StatusWarn {
			nv++
		}
	}
	if len(x.Vulnerabilities) > 0 {
		vulnNote = t.StatusText(model.StatusOK, fmt.Sprintf("%d ok", len(x.Vulnerabilities)-nv))
		if nv > 0 {
			vulnNote += "  " + t.StatusText(model.StatusWarn, fmt.Sprintf("%d vulnerable", nv))
		}
	}
	section("Security vulnerabilities", vulnNote)
	if len(x.Vulnerabilities) == 0 {
		add(t.Dim.Render("not reported by this kernel"))
	}
	vs := slices.Clone(x.Vulnerabilities)
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].Status > vs[j].Status })
	for _, v := range vs {
		add(t.Dot(v.Status) + " " + pad(v.Name, 26) + t.StatusText(v.Status, v.State))
	}

	section("Features", fmt.Sprintf("%d flags", len(x.Flags)))
	if hl := notableFlags(x.Flags); len(hl) > 0 {
		add(t.Bold.Render("Notable  ") + strings.Join(hl, " "))
	}
	if len(x.VirtFlags) > 0 {
		add(t.Bold.Render("Virt     ") + strings.Join(x.VirtFlags, " "))
	}
	add(wrapWords(x.Flags, w, t)...)

	// scroll window
	maxOff := max(0, len(lines)-c.h)
	c.scroll = min(c.scroll, maxOff)
	end := min(len(lines), c.scroll+c.h)
	out := lines[c.scroll:end]
	if maxOff > 0 && len(out) > 0 { // position on the last line
		out[len(out)-1] = t.Sub.Render(fmt.Sprintf("lines %d–%d of %d · ↑↓ pgup pgdn", c.scroll+1, end, len(lines)))
	}
	return strings.Join(out, "\n")
}

func microcodeText(t *Theme, x *model.CPU) string {
	revs := map[string]int{}
	for _, l := range x.Logical {
		if l.Online && l.Microcode != "" {
			revs[l.Microcode]++
		}
	}
	if len(revs) > 1 {
		var parts []string
		for r, n := range revs {
			parts = append(parts, fmt.Sprintf("%s on %d CPUs", r, n))
		}
		sort.Strings(parts)
		return t.StatusText(model.StatusWarn, "mixed: "+strings.Join(parts, ", "))
	}
	if x.Microcode != "" && len(revs) == 1 {
		return x.Microcode + t.Dim.Render(" (same on all online CPUs)")
	}
	return x.Microcode
}

func famModelHex(fam, mdl string) string {
	var f, m int
	if _, err := fmt.Sscan(fam, &f); err != nil {
		return ""
	}
	if _, err := fmt.Sscan(mdl, &m); err != nil {
		return ""
	}
	return fmt.Sprintf("%02Xh/%02Xh", f, m)
}

func coreTypeSummary(x *model.CPU) string {
	n := map[string]int{}
	for _, l := range x.Logical {
		if l.CoreType != "" {
			n[l.CoreType]++
		}
	}
	if len(n) == 0 {
		return ""
	}
	var parts []string
	for _, k := range []string{"P", "E", "big", "little"} {
		if n[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d threads on %ss", n[k], coreTypeName(k)))
		}
	}
	return strings.Join(parts, " · ")
}

func idleSummary(x *model.CPU) string {
	for _, l := range x.Logical {
		if len(l.IdleStates) == 0 {
			continue
		}
		var parts []string
		for _, s := range l.IdleStates {
			p := fmt.Sprintf("%s (%d µs)", s.Name, s.LatencyUs)
			if s.Disabled {
				p += " disabled"
			}
			parts = append(parts, p)
		}
		return strings.Join(parts, " · ")
	}
	return ""
}

var notable = []string{"avx", "avx2", "avx512f", "avx512_vnni", "avx512_bf16", "avx512_fp16", "avx_vnni", "amx_tile", "amx_bf16",
	"amx_int8", "fma", "aes", "vaes", "sha_ni", "pclmulqdq", "rdrand", "rdseed", "sev", "sev_es", "sev_snp", "sgx", "tdx_guest",
	"tme", "smep", "smap", "ibt", "user_shstk", "ht", "lm", "constant_tsc", "nonstop_tsc", "pdpe1gb",
	"asimd", "sve", "sve2", "sha2", "sha3", "atomics", "crc32"}

func notableFlags(flags []string) []string {
	have := map[string]bool{}
	for _, f := range flags {
		have[f] = true
	}
	var out []string
	for _, f := range notable {
		if have[f] {
			out = append(out, f)
		}
	}
	return out
}

func wrapWords(words []string, w int, t *Theme) []string {
	var lines []string
	cur := ""
	for _, wd := range words {
		if cur != "" && len(cur)+1+len(wd) > w {
			lines = append(lines, t.Dim.Render(cur))
			cur = ""
		}
		if cur != "" {
			cur += " "
		}
		cur += wd
	}
	if cur != "" {
		lines = append(lines, t.Dim.Render(cur))
	}
	return lines
}

func bytesIEC(b uint64) string {
	switch {
	case b >= 1<<20 && b%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", b>>20)
	case b >= 1<<10:
		return fmt.Sprintf("%d KiB", b>>10)
	}
	return fmt.Sprintf("%d B", b)
}

// Detail is the full page of the selected logical CPU.
func (c *cpuTab) Detail(t *Theme, w int) (string, string) {
	if c.cpu == nil || len(c.cpu.Logical) == 0 {
		return "", ""
	}
	if c.view == 2 {
		return "", "" // the details view is already a page
	}
	l := c.cpu.Logical[c.sel]
	id := cpuKey(l.ID)
	doc := newDoc(t, w)
	doc.section("Identity", t.Badge(l.Status))
	state := t.StatusText(model.StatusOK, "online")
	if !l.Online {
		state = t.StatusText(model.StatusCrit, "OFFLINE")
	}
	typ := ""
	if l.CoreType != "" {
		typ = coreTypeName(l.CoreType)
	}
	doc.kv([2]string{"State", state}, [2]string{"Socket / core / node", fmt.Sprintf("%s / %s / %s", intOrDash(l.SocketID), intOrDash(l.CoreID), intOrDash(l.NUMANode))},
		[2]string{"Core type", typ}, [2]string{"Preferred-core rank", optI(l.PrefRank, "%d")}, [2]string{"SMT siblings", l.Siblings},
		[2]string{"Microcode", l.Microcode}, [2]string{"Model", c.cpu.Model})
	for _, s := range c.cpu.Sockets {
		if s.ID == l.SocketID && s.ID >= 0 {
			ids := s.Serial
			if s.PPIN != "" {
				ids = strings.TrimSpace(ids + " PPIN " + s.PPIN)
			}
			doc.kv([2]string{"Socket", s.Designation + "  " + s.Version}, [2]string{"Socket IDs", ids})
		}
	}
	offlineSection(doc, c.cpu, l)
	mceHistorySection(doc, c.cpu, l)
	if l.Online {
		doc.section("Live", "")
	}
	hist := func(k string) string {
		h := t.sess.history("cpu/" + id + "/" + k)
		if len(h) < 2 {
			return ""
		}
		return "  " + t.Message.Render(sparkline(h, 30))
	}
	load := ""
	if l.UtilizationPercent != nil {
		load = usageBar(t, l.UtilizationPercent, model.StatusOK, 20) + hist("load")
	}
	freq := ""
	if l.FreqMHz != nil {
		freq = fmt.Sprintf("%.0f MHz", *l.FreqMHz) + t.Dim.Render(fmt.Sprintf("  (range %s–%s MHz)", optF(l.MinFreqMHz, "%.0f"), optF(l.MaxFreqMHz, "%.0f"))) + hist("mhz")
	}
	doc.kv([2]string{"Load", load}, [2]string{"Frequency", freq})
	if l.TempCelsius != nil {
		sen := model.Sensor{Name: "Temperature", Value: l.TempCelsius, Unit: model.UnitCelsius, Status: tempStatus(c.cpu, &l)}
		for _, p := range c.cpu.PackageTemps {
			if p.Value != nil && *p.Value == *l.TempCelsius {
				sen.Name, sen.Thresholds, sen.Source = p.Name, p.Thresholds, p.Source
			}
		}
		doc.sensor(sen, t.sess.history("cpu/"+id+"/temp"))
	}
	doc.section("Health counters", "")
	ct := string(l.CoreTest)
	if l.CoreTestDetail != "" {
		ct += " (" + l.CoreTestDetail + ")"
	}
	doc.kv([2]string{"Thermal throttling", optU(l.ThrottleCount)}, [2]string{"MCE exceptions", optU(l.MCEExceptions)},
		[2]string{"MCE logged", optU(l.MCELogged)}, [2]string{"Core test", ct})
	if len(l.IdleStates) > 0 {
		doc.section("Idle states", t.Dim.Render("share of idle residency since boot"))
		var total uint64
		for _, s := range l.IdleStates {
			if s.TimeUs != nil {
				total += *s.TimeUs
			}
		}
		var rows [][]string
		for _, s := range l.IdleStates {
			share := "–"
			if s.TimeUs != nil && total > 0 {
				share = fmt.Sprintf("%.1f%%", float64(*s.TimeUs)/float64(total)*100)
			}
			dis := ""
			if s.Disabled {
				dis = "disabled"
			}
			rows = append(rows, []string{s.Name, s.Desc, fmt.Sprintf("%d µs", s.LatencyUs), optU(s.Usage), share, dis})
		}
		doc.table([]string{"State", "Description", "Exit latency", "Entries", "Residency", ""}, rows, nil)
	}
	doc.counters(t.sess.counters(func(k string) bool { return strings.HasSuffix(k, "."+id) }), "")
	doc.findings(model.ModCPU, relatedFindings(c.cpu.Findings, id))
	return "Logical CPU " + id, doc.String()
}

// offlineSection: when and how the CPU went offline, whether that survives
// a reboot, and whether the other thread of its core still runs.
func offlineSection(doc *doc, c *model.CPU, l model.LogicalCPU) {
	o := l.Offline
	if o == nil {
		return
	}
	t := doc.t
	id := cpuKey(l.ID)
	how := "offline since boot"
	switch o.How {
	case "runtime":
		how = fmt.Sprintf("taken offline after boot: echo 0 > /sys/devices/system/cpu/%s/online, or a RAS daemon", id)
	case "boot":
		how = "not started at boot: maxcpus= / nr_cpus= on the kernel command line"
	}
	since := ""
	if !o.Since.IsZero() {
		since = o.Since.Local().Format("2006-01-02 15:04:05")
		if o.AfterMCE {
			since += t.StatusText(model.StatusWarn, "  minutes after a machine check on it")
		}
	}
	keep := t.StatusText(model.StatusWarn, "no - it runs work again after a reboot")
	if o.Persistent != "" {
		keep = t.StatusText(model.StatusOK, "yes") + t.Dim.Render(" · "+o.Persistent)
	} else if o.How == "boot" {
		keep = t.StatusText(model.StatusOK, "yes") + t.Dim.Render(" · kernel command line")
	}
	other := t.Sub.Render("unknown (no SMT, or the numbering does not tell)")
	if o.Sibling != nil {
		s := cpuKey(*o.Sibling)
		if o.SiblingOnline {
			other = t.StatusText(model.StatusCrit, s+" is online: the physical core still runs work")
		} else {
			other = t.StatusText(model.StatusOK, s+" is offline too: the core is out of service")
		}
		if o.SiblingInferred {
			other += t.Dim.Render("  (from the CPU numbering)")
		}
	}
	doc.section("Offline", t.Dim.Render("nothing here changes the system"))
	doc.kv([2]string{"Since", since}, [2]string{"How", how}, [2]string{"After reboot", "offline? " + keep},
		[2]string{"Other thread", other}, [2]string{"Shares L3 with", o.L3SharedWith})
	for _, cr := range c.CrashRecords {
		if cr.CPU == l.ID {
			when := ""
			if !cr.Time.IsZero() {
				when = cr.Time.Local().Format("2006-01-02 15:04 ")
			}
			doc.kv([2]string{"Crashed the kernel", t.StatusText(model.StatusCrit, when+cr.Reason) + t.Dim.Render(" · "+cr.File)})
		}
	}
	doc.add("", hang("   ", t.Key.Render("S")+t.Sub.Render(" tests this core: online only for the test, isolated from all other work, offline again after. "+
		"A defective core can still raise a fatal machine check and reboot the server: use a maintenance window."), doc.w-1))
	if o.Persistent == "" && o.How != "boot" {
		doc.add(hang("   ", t.Sub.Render(fmt.Sprintf("To keep %s offline across reboots: a oneshot systemd unit with ExecStart=/bin/sh -c 'echo 0 > /sys/devices/system/cpu/%s/online'.", id, id)), doc.w-1))
	}
}

// mceHistorySection lists the machine checks of this CPU and the other
// thread of its core, from all sources, newest first.
func mceHistorySection(doc *doc, c *model.CPU, l model.LogicalCPU) {
	sib := -1
	if l.Offline != nil && l.Offline.Sibling != nil {
		sib = *l.Offline.Sibling
	}
	for _, x := range c.Logical {
		if x.Offline != nil && x.Offline.Sibling != nil && *x.Offline.Sibling == l.ID {
			sib = x.ID
		}
	}
	var rows [][]string
	var sts []model.Status
	for i := len(c.MCEHistory) - 1; i >= 0; i-- {
		r := c.MCEHistory[i]
		// records without a CPU number (BMC log, firmware reports) concern
		// the whole host: shown on every CPU page
		if r.CPU >= 0 && r.CPU != l.ID && (sib < 0 || r.CPU != sib) {
			continue
		}
		when := "-"
		if !r.Time.IsZero() {
			when = r.Time.Local().Format("2006-01-02 15:04")
		}
		scope := map[string]string{"core": "in the core", "shared": "shared: not the core"}[r.Scope]
		bank := "-"
		if r.Bank >= 0 {
			bank = fmt.Sprint(r.Bank)
		}
		cpu := "–"
		if r.CPU >= 0 {
			cpu = cpuKey(r.CPU)
		}
		rows = append(rows, []string{when, cpu, bank, r.Unit, scope, strings.ToLower(r.Severity.String()), r.Source})
		sts = append(sts, r.Severity)
	}
	if len(rows) == 0 {
		if l.Offline != nil {
			doc.section("Machine check history", doc.t.Dim.Render("none found"))
			lines := []string{"No machine check for this core in the kernel log, the journal, crash records (pstore), rasdaemon or the BMC event log."}
			if st, ok := c.Source("journal"); ok && strings.Contains(st.Reason, "not persistent") {
				lines = append(lines, "The journal is not persistent, so errors from before this boot are gone: mkdir /var/log/journal keeps them from now on.")
			}
			lines = append(lines, "Firmware-first servers log CPU errors in the BMC instead: HPE iLO's Integrated Management Log (iLO web UI, or ilorest serverlogs --selectlog=IML), Dell iDRAC's Lifecycle Log (racadm lclog view).",
				"ipmitool sel elist | grep -iE 'processor|cpu|ierr|machine'")
			for _, s := range lines {
				doc.add(hang("   ", doc.t.Sub.Render(s), doc.w-1))
			}
		}
		return
	}
	note := "this CPU"
	if sib >= 0 {
		note = "this CPU and " + cpuKey(sib) + " (same core)"
	}
	doc.section("Machine check history", doc.t.Dim.Render(note+" · this boot, earlier boots, crash records, rasdaemon, BMC"))
	doc.table([]string{"Time", "CPU", "Bank", "Unit", "Where", "Severity", "Source"}, rows, sts)
	doc.add(hang("   ", doc.t.Sub.Render("Errors in the core (L1/L2, fetch, load-store, execution) point at this core; errors in shared units (L3, memory controller, fabric) are only reported by it."), doc.w-1))
}
