package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
)

// helpTexter is implemented by tabs: the "This tab" block of the help
// overlay (canvas board Help).
type helpTexter interface {
	HelpText() string
}

// helpOverlay draws the key reference over the dimmed screen: three key
// columns, the active tab's block, the status legend. Any key closes it.
func (m *Model) helpOverlay(base string) string {
	t := m.theme
	inner := min(96, m.width-4) - 4
	colW := inner / 3
	compact := m.height < 32 || m.width < 100
	kw := 11
	if compact {
		kw = 10
	}
	col := func(title string, rows [][2]string) string {
		lines := []string{t.Section.Render(title)}
		for _, r := range rows {
			lines = append(lines, truncate(t.Key.Render(pad(r[0], kw))+t.Dim.Render(r[1]), colW-1))
		}
		return strings.Join(lines, "\n")
	}
	nav := col("Navigation", [][2]string{{"1–0", "switch tab"}, {"tab ⇧tab", "next / prev tab"}, {"↑↓ jk", "move"},
		{"←→ hl", "move / regions"}, {"pgup pgdn", "page"}, {"g G", "top / end"}, {"enter", "open / action"}, {"esc", "back · close"}})
	health := col("Health", [][2]string{{"n", "next problem"}, {"!", "problems only"}, {"o", "worst ↔ physical"},
		{"v", "switch view"}, {"/", "search"}, {"t", "CPU load ↔ temp"}, {"a", "empty DIMM slots"}, {"h", "virtual NICs"}})
	data := col("Data · stress", [][2]string{{"r", "refresh now"}, {"p", "pause / resume"}, {"e", "export JSON"}, {"c", "copy serial"},
		{"s", "Stress tab"}, {"S", "stress this"}, {"F", "full test"}, {"q ctrl+c", "quit · stop all"}})
	keys := lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(colW).Render(nav),
		lipgloss.NewStyle().Width(colW).Render(health), lipgloss.NewStyle().Width(colW).Render(data))

	tb := m.tabs[m.active]
	this := ""
	if h, ok := tb.(helpTexter); ok {
		this = h.HelpText()
	}
	var hints []string
	for _, b := range tb.Help() {
		if h := b.Help(); h.Key != "" {
			hints = append(hints, t.Key.Render(h.Key)+" "+t.Dim.Render(h.Desc))
		}
	}
	block := t.Section.Render("This tab · " + tb.Title())
	if this != "" {
		block += "\n" + lipgloss.NewStyle().Width(inner).Render(this)
	}
	if len(hints) > 0 && !compact {
		block += "\n" + lipgloss.NewStyle().Width(inner).Render(strings.Join(hints, "  "))
	}

	na := lipgloss.NewStyle().Foreground(t.NA)
	legend := t.Section.Render("Status") + "\n" +
		t.Dot(model.StatusOK) + " OK   " + t.Dot(model.StatusWarn) + " WARN   " + t.Dot(model.StatusCrit) + " CRIT   " +
		t.Dot(model.StatusUnknown) + " UNKNOWN   " + na.Render("○") + " unavailable (with reason)   " + na.Render("·") + " not applicable\n" +
		t.NewTag() + " appeared this session   " + t.StatusText(model.StatusWarn, "↑ +38") + " counter grew   " +
		t.Dot(model.StatusOK) + " resolved " + t.Sub.Render("went away this session")
	body := keys + "\n\n" + block + "\n\n" + legend + "\n\n" + t.Sub.Render("Any key closes this window.")
	if compact { // fit 80×24: no blank rows, the legend without its heading
		legend = strings.SplitN(legend, "\n", 2)[1]
		body = keys + "\n" + block + "\n" + legend + "\n" + t.Sub.Render("Any key closes this window.")
	}

	right := "? to close"
	if m.snap != nil && m.snap.TyrVersion != "" {
		right = "tyr " + m.snap.TyrVersion + " · " + right
	}
	return overlayR(t, base, t.Accent, "Keys", right, body, m.width, m.height)
}

func (o *overviewTab) HelpText() string {
	return "←→↑↓ cards · ↓ past the last row or n → Findings · enter opens the module tab with that component selected · esc in Findings returns to the cards"
}

func (c *cpuTab) HelpText() string {
	return "v Grid · Cores · Details · ←→↑↓ move through the core grid · t load ↔ temperature · n next problem CPU · enter opens the CPU page"
}

func (m *memoryTab) HelpText() string {
	return "v Table · Slot map · a shows or hides empty slots · enter opens the DIMM page · c copies the serial"
}

func (s *storageTab) HelpText() string {
	return "v Disks · RAID · Filesystems · n next problem · enter opens the page with SMART attributes, counters and / search · c copies the serial"
}

func (p *powerTab) HelpText() string {
	return "↑↓ power supplies · enter opens the PSU page · c copies the serial · live readings arrive with the Power collector"
}

func (n *networkTab) HelpText() string {
	return "↑↓ interfaces (bond and VLAN members under their ports) · h shows virtual interfaces · enter opens the port page with transceiver and counters"
}

func (a *accelTab) HelpText() string {
	return "↑↓ devices · enter opens the device page (PCIe, power, reliability) · c copies the serial"
}

func (e *eventsTab) HelpText() string {
	return "f severity · s source · / search · space expands the selected event · enter jumps to the component in its tab · ! warn and crit only"
}

func (s *stressTab) HelpText() string {
	switch s.state {
	case stRunning, stPreparing:
		return "esc aborts cleanly; all workers stop · other tabs stay live · q is blocked while a test runs (ctrl+c stops it and quits)"
	case stResult:
		return "enter or esc back to the form · ↑↓ scroll · e exports the snapshot"
	case stConfirm:
		return "type yes and press enter to start · esc cancels"
	}
	return "↑↓ field · ←→ change · enter edits a text field or starts (asks for confirmation) · i installs missing engines through the package manager · ↓ past Start reaches recent runs, enter opens one"
}

func (v *inventoryTab) HelpText() string {
	if v.view == 1 {
		return "→ or space opens a branch, ← closes it or goes to the parent · enter opens the component in its tab · c copies the serial · v list"
	}
	return "/ searches every column · f cycles the category · esc clears the search, then the category · enter opens the page · c copies the serial · v tree"
}
