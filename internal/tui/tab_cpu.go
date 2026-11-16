package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/model"
)

const cellW = 8 // " 127 99%" incl. separator

// cpuTab has three views: a grid of all logical CPUs, a per-core health
// table, and a scrollable details page (identification, caches, features,
// vulnerabilities, power settings).
type cpuTab struct {
	cpu      *model.CPU
	w, h     int
	sel      int
	view     int // 0 grid, 1 cores, 2 details
	scroll   int // details view
	tempMode bool
	list     *listView // cores view
	keys     keyMap
	toggle   key.Binding
	switchV  key.Binding
}

func newCPUTab(p *prefs) *cpuTab {
	return &cpuTab{list: newListView(p), keys: newKeyMap(), toggle: key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "load/temp")),
		switchV: key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "view"))}
}

// syncList arranges the cores list and keeps it on the grid selection.
func (c *cpuTab) syncList() {
	if c.cpu == nil {
		return
	}
	var items []listItem
	for _, l := range c.cpu.Logical {
		items = append(items, listItem{key: cpuKey(l.ID), status: l.Status})
	}
	if c.sel < len(c.cpu.Logical) {
		c.list.selKey = cpuKey(c.cpu.Logical[c.sel].ID)
	}
	c.list.arrange(items)
}

// nextProblemCPU selects the next CPU with a problem (wrapping).
func (c *cpuTab) nextProblemCPU() {
	n := len(c.cpu.Logical)
	for i := 1; i <= n; i++ {
		j := (c.sel + i) % n
		if c.cpu.Logical[j].Status >= model.StatusWarn {
			c.sel = j
			return
		}
	}
}

// selectKey moves the grid selection to logical CPU "cpuN".
func (c *cpuTab) selectKey(k string) bool {
	for i, l := range c.cpu.Logical {
		if cpuKey(l.ID) == k {
			c.sel = i
			return true
		}
	}
	return false
}

// Focus selects the logical CPU a finding refers to ("cpu7", "cpu7,cpu9",
// "CPU0 Core 4" via its first thread).
func (c *cpuTab) Focus(subject string) bool {
	if c.cpu == nil {
		return false
	}
	for _, part := range strings.Split(subject, ",") {
		if c.selectKey(strings.TrimSpace(part)) {
			c.view = 1
			c.syncList()
			return true
		}
	}
	var pkg, core int
	if _, err := fmt.Sscanf(subject, "CPU%d Core %d", &pkg, &core); err == nil {
		for i, l := range c.cpu.Logical {
			if l.SocketID == pkg && l.CoreID == core {
				c.sel, c.view = i, 1
				c.syncList()
				return true
			}
		}
	}
	return false
}

func (c *cpuTab) Title() string             { return "CPU" }
func (c *cpuTab) Module() model.ModuleID    { return model.ModCPU }
func (c *cpuTab) SetData(s *model.Snapshot) { c.cpu = s.CPU; c.clamp() }
func (c *cpuTab) SetSize(w, h int)          { c.w, c.h = w, h }
func (c *cpuTab) Help() []key.Binding {
	switch c.view {
	case 0:
		return []key.Binding{c.keys.Left, c.keys.Enter, c.switchV, c.toggle}
	case 1:
		return []key.Binding{c.keys.Up, c.keys.Enter, c.switchV, keyNext}
	}
	return []key.Binding{c.keys.Up, c.switchV}
}
func (c *cpuTab) perRow() int { return max(1, c.w/cellW) }
func (c *cpuTab) clamp() {
	if c.cpu != nil && c.sel >= len(c.cpu.Logical) {
		c.sel = max(0, len(c.cpu.Logical)-1)
	}
}

func (c *cpuTab) Update(msg tea.KeyPressMsg) tea.Cmd {
	if c.cpu == nil {
		return nil
	}
	n, r := len(c.cpu.Logical), c.perRow()
	if key.Matches(msg, c.switchV) {
		c.view = (c.view + 1) % 3
		return nil
	}
	switch c.view {
	case 1:
		c.syncList()
		if c.list.handle(msg, c.keys, max(1, c.h/2)) {
			if it, ok := c.list.current(); ok {
				c.selectKey(it.key)
			}
		}
		return nil
	case 2:
		switch {
		case key.Matches(msg, c.keys.Up):
			c.scroll = max(0, c.scroll-1)
		case key.Matches(msg, c.keys.Down):
			c.scroll++
		case msg.String() == "pgdown" || msg.String() == "space":
			c.scroll += max(1, c.h-2)
		case msg.String() == "pgup":
			c.scroll = max(0, c.scroll-max(1, c.h-2))
		case msg.String() == "home" || msg.String() == "g":
			c.scroll = 0
		}
		return nil
	}
	if key.Matches(msg, c.toggle) {
		c.tempMode = !c.tempMode
		return nil
	}
	if lay, ok := c.layout(); ok {
		switch {
		case key.Matches(msg, c.keys.Left):
			c.gridMove(lay, 0, -1)
		case key.Matches(msg, c.keys.Right):
			c.gridMove(lay, 0, 1)
		case key.Matches(msg, c.keys.Up):
			c.gridMove(lay, -1, 0)
		case key.Matches(msg, c.keys.Down):
			c.gridMove(lay, 1, 0)
		case msg.String() == "n":
			c.nextProblemCPU()
		}
		return nil
	}
	switch {
	case key.Matches(msg, c.keys.Left):
		c.sel = max(0, c.sel-1)
	case key.Matches(msg, c.keys.Right):
		c.sel = min(n-1, c.sel+1)
	case key.Matches(msg, c.keys.Up):
		c.sel = max(0, c.sel-r)
	case key.Matches(msg, c.keys.Down):
		c.sel = min(n-1, c.sel+r)
	case msg.String() == "n":
		c.nextProblemCPU()
	}
	return nil
}

func (c *cpuTab) View(t *Theme) string {
	x := c.cpu
	if x == nil {
		return t.Dim.Render("CPU data not collected")
	}
	switch c.view {
	case 1:
		return c.coresView(t)
	case 2:
		return c.detailsView(t)
	}
	return c.gridView(t)
}

// flatGridView is the fallback grid (no topology, or more cores than
// fit): logical CPUs in rows of cells.
func (c *cpuTab) flatGridView(t *Theme) string {
	x := c.cpu
	// --- info ---
	model_ := orDash(x.Model)
	topo := fmt.Sprintf("%d sockets · %d cores · %d threads", x.SocketCount, x.CoreCount, x.ThreadCount)
	freq := fmt.Sprintf("base %s · max %s MHz", optF(x.BaseFreqMHz, "%.0f"), optF(x.MaxFreqMHz, "%.0f"))
	if x.Governor != "" {
		freq += " · " + x.Governor + " (" + x.FreqDriver + ")"
	}
	var temps []string
	for _, s := range x.PackageTemps {
		if s.Value != nil {
			temps = append(temps, t.StatusText(s.Status, fmt.Sprintf("%s %s", strings.TrimPrefix(s.Name, "CPU"), check.FmtTemp(*s.Value))))
		}
	}
	rows := [][2]string{
		{"Model", model_ + t.Dim.Render("  µcode "+orDash(x.Microcode))},
		{"Topology", topo + t.Dim.Render("  online "+x.Topology.Online)},
		{"Frequency", freq},
	}
	if len(temps) > 0 {
		rows = append(rows, [2]string{"Temps", strings.Join(temps, "  ")})
	}
	var ids []string
	for _, s := range x.Sockets {
		id := s.Serial
		if id == "" && s.PPIN != "" {
			id = "PPIN " + s.PPIN
		}
		if id != "" {
			ids = append(ids, s.Designation+" "+id)
		}
	}
	if len(ids) > 0 {
		rows = append(rows, [2]string{"Serials", strings.Join(ids, " · ")})
	}
	if x.CoreTest != nil {
		ct := x.CoreTest
		st := model.StatusOK
		if ct.Failed+ct.TimedOut > 0 {
			st = model.StatusCrit
		}
		rows = append(rows, [2]string{"Core test", t.StatusText(st, fmt.Sprintf("%d pass, %d fail, %d timeout, %d skipped (%d ms)", ct.Passed, ct.Failed, ct.TimedOut, ct.Skipped, ct.DurationMS))})
	}
	info := kv(t, rows)

	// --- detail; findings fill whatever the grid leaves free ---
	detail := c.detail(t)
	legend := t.Dim.Render("load ") + c.legend(t)
	if c.tempMode {
		legend = t.Dim.Render("temperature ") + t.StatusText(model.StatusOK, "ok ") + t.StatusText(model.StatusWarn, "warn ") + t.StatusText(model.StatusCrit, "crit")
	}
	legend += t.Dim.Render("  ✖ offline")

	reserve := 1 + max(1, min(countNonOK(x.Findings), 4))
	fixed := lipgloss.Height(info) + 2 + 1 + lipgloss.Height(detail) + reserve
	grid, scroll := c.grid(t, max(2, c.h-fixed))
	top := strings.Join([]string{info, t.Heading("Logical CPUs", legend+scroll, c.w), grid,
		t.Heading("Selected", "", c.w), detail}, "\n")
	return top + "\n" + findingsSection(t, "CPU", x.Findings, x.Sources, c.h-lipgloss.Height(top), c.w)
}

func (c *cpuTab) legend(t *Theme) string {
	var b strings.Builder
	for _, u := range []float64{10, 40, 70, 95} {
		b.WriteString(c.loadStyle(t, &u).Render(fmt.Sprintf("%.0f%% ", u)))
	}
	return b.String()
}

func (c *cpuTab) loadStyle(t *Theme, u *float64) lipgloss.Style {
	s := lipgloss.NewStyle()
	switch {
	case u == nil:
		return s.Foreground(t.Muted)
	case *u < 25:
		return s.Foreground(t.Muted)
	case *u < 60:
		return s.Foreground(t.Fg)
	case *u < 85:
		return s.Foreground(t.Accent)
	}
	return s.Foreground(t.Accent).Bold(true)
}

func (c *cpuTab) grid(t *Theme, maxRows int) (string, string) {
	ls := c.cpu.Logical
	r := c.perRow()
	total := (len(ls) + r - 1) / r
	selRow := c.sel / r
	start := 0
	if total > maxRows {
		start = min(max(0, selRow-maxRows/2), total-maxRows)
	}
	var lines []string
	for row := start; row < min(total, start+maxRows); row++ {
		var b strings.Builder
		for i := row * r; i < min(len(ls), (row+1)*r); i++ {
			b.WriteString(c.cell(t, &ls[i], i == c.sel))
		}
		lines = append(lines, b.String())
	}
	scroll := ""
	if total > maxRows {
		scroll = t.Dim.Render(fmt.Sprintf("  rows %d-%d of %d", start+1, min(total, start+maxRows), total))
	}
	return strings.Join(lines, "\n"), scroll
}

func (c *cpuTab) cell(t *Theme, l *model.LogicalCPU, selected bool) string {
	var text string
	st := lipgloss.NewStyle()
	switch {
	case l.Present && !l.Online:
		text = fmt.Sprintf("%4d ✖  ", l.ID)
		st = st.Foreground(t.Crit).Bold(true)
	case c.tempMode:
		// heat map: OK→WARN→CRIT gradient from 30°C up to the crit limit
		text = fmt.Sprintf("%4d %3s", l.ID, optF(l.TempCelsius, "%.0f°"))
		st = st.Foreground(t.StatusColor(model.StatusUnknown))
		if l.TempCelsius != nil {
			crit := c.critTemp(l)
			st = st.Foreground(heatColor(t, (*l.TempCelsius-30)/(crit-30)))
			if tempStatus(c.cpu, l) >= model.StatusWarn {
				st = st.Bold(true)
			}
		}
	default:
		u := "–"
		if l.UtilizationPercent != nil {
			u = fmt.Sprintf("%.0f%%", *l.UtilizationPercent)
		}
		text = fmt.Sprintf("%4d %3s", l.ID, u)
		st = c.loadStyle(t, l.UtilizationPercent)
		if l.Status >= model.StatusWarn {
			st = st.Foreground(t.StatusColor(l.Status)).Bold(true)
		}
	}
	text = pad(text, cellW)
	if selected {
		st = st.Background(t.SelBg).Bold(true)
		if l := c.cpu.Logical[c.sel]; l.Status < model.StatusWarn {
			st = st.Foreground(t.Fg)
		}
	}
	return st.Render(text)
}

// tempStatus finds the status of the sensor that produced l's temperature.
func tempStatus(x *model.CPU, l *model.LogicalCPU) model.Status {
	if l.TempCelsius == nil {
		return model.StatusUnknown
	}
	for _, s := range x.PackageTemps {
		if s.Value != nil && *s.Value == *l.TempCelsius {
			return s.Status
		}
	}
	return model.Worst(l.Status, model.StatusOK)
}

// critTemp is the critical limit of the sensor behind l's temperature.
func (c *cpuTab) critTemp(l *model.LogicalCPU) float64 {
	for _, s := range c.cpu.PackageTemps {
		if s.Value != nil && l.TempCelsius != nil && *s.Value == *l.TempCelsius && s.Thresholds != nil && s.Thresholds.UpperCrit != nil {
			return max(*s.Thresholds.UpperCrit, 40)
		}
	}
	return 100
}
