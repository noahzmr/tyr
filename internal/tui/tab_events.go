package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
)

// eventsTab (canvas board Events): one timeline of SEL and kernel-log
// events with severity/source chips, search and an expandable detail.
// The Events collector does not exist yet, so the timeline shows its
// ○ unavailable state; the chips, search and keys already work.
type eventsTab struct {
	snap   *model.Snapshot
	w, h   int
	keys   keyMap
	sev    int // 0 all, 1 warn+, 2 crit
	src    int // 0 all, 1 SEL, 2 kernel
	search lineInput
	expand bool
}

const eventsMissing = "collector (SEL and kernel log) not implemented yet"

var (
	keySeverity = key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "severity"))
	keySource   = key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "source"))
	keyDetails  = key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "details"))
)

func newEventsTab() *eventsTab {
	return &eventsTab{keys: newKeyMap(), search: lineInput{placeholder: "search events", limit: 64}}
}

func (e *eventsTab) Title() string             { return "Events" }
func (e *eventsTab) Module() model.ModuleID    { return model.ModEvents }
func (e *eventsTab) SetSize(w, h int)          { e.w, e.h = w, h }
func (e *eventsTab) SetData(s *model.Snapshot) { e.snap = s }
func (e *eventsTab) Capturing() bool           { return e.search.Focused() }

// Claims: on this tab "s" is the source filter, not "go to Stress".
func (e *eventsTab) Claims(msg tea.KeyPressMsg) bool { return key.Matches(msg, keySource) }

func (e *eventsTab) Help() []key.Binding {
	return []key.Binding{e.keys.Up, key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "jump to component")),
		keySeverity, keySource, key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")), keyDetails}
}

func (e *eventsTab) Update(msg tea.KeyPressMsg) tea.Cmd {
	if e.search.Focused() {
		switch msg.String() {
		case "esc":
			if e.search.Value() != "" {
				e.search.SetValue("")
			} else {
				e.search.Blur()
			}
		case "enter", "up", "down":
			e.search.Blur()
		default:
			e.search.Update(msg)
		}
		return nil
	}
	switch {
	case key.Matches(msg, keySeverity):
		e.sev = (e.sev + 1) % 3
	case key.Matches(msg, keySource):
		e.src = (e.src + 1) % 3
	case key.Matches(msg, keyDetails):
		e.expand = !e.expand
	case msg.String() == "/":
		e.search.Focus()
	case key.Matches(msg, e.keys.Back):
		e.search.SetValue("")
		e.sev, e.src = 0, 0
	}
	return nil
}

func (e *eventsTab) Strip(t *Theme, w int) string {
	if e.snap == nil {
		return ""
	}
	if sec := e.snap.Events; sec != nil { // once the collector exists
		h := sec.Head()
		return statusStrip(t, h.Status, h.Summary, "", "", w)
	}
	return truncate(t.Unavail("Events", eventsMissing), w)
}

// chips: "Severity all warn+ crit   Source all SEL kernel   Since boot
// Search ╭─ … ─╮"; the active chip is an accent pill.
func (e *eventsTab) chips(t *Theme) string {
	group := func(label string, names []string, active int) string {
		var ps []string
		for i, n := range names {
			if i == active {
				ps = append(ps, pill(t, n, true))
			} else {
				ps = append(ps, t.Dim.Render(n))
			}
		}
		return t.Dim.Render(label+" ") + strings.Join(ps, " ")
	}
	since := "since boot"
	if e.snap != nil && e.snap.System != nil && e.snap.System.UptimeSeconds != nil {
		since += " (" + duration(*e.snap.System.UptimeSeconds) + ")"
	}
	line := group("Severity", []string{"all", "warn+", "crit"}, e.sev) + "   " + group("Source", []string{"all", "SEL", "kernel"}, e.src) +
		"   " + t.Sub.Render(since) + "   " + e.searchBox(t, 22)
	return truncate(line, e.w)
}

func (e *eventsTab) searchBox(t *Theme, w int) string {
	br := lipgloss.NewStyle().Foreground(t.Border)
	if e.search.Focused() {
		br = lipgloss.NewStyle().Foreground(t.Accent)
	}
	body := t.Sub.Render("/ to search") + " "
	if v := e.search.Value(); v != "" || e.search.Focused() {
		body = t.Bold.Render(v)
		if e.search.Focused() {
			body += lipgloss.NewStyle().Foreground(t.Accent).Render("█")
		}
	}
	return br.Render("╭─ ") + body + br.Render(strings.Repeat("─", max(0, w-4-lipgloss.Width(body)))+"╮")
}

func (e *eventsTab) View(t *Theme) string {
	if e.snap == nil {
		return ""
	}
	w := e.w
	out := []string{e.chips(t), t.Dim.Render("SEL fill  ") + t.Unavail("", "no SEL reader yet (BMC event log via ipmitool sel)"), ""}
	head := renderList(t, []column{{"Time", 12}, {"Sev", 4}, {"Source", 6}, {"Component", 11}, {"Message", 0}}, nil, -1, w, 1)
	out = append(out, strings.Split(head, "\n")[0], "   "+t.Unavail("no events", "Events "+eventsMissing))
	if q := strings.TrimSpace(e.search.Value()); q != "" || e.sev != 0 || e.src != 0 {
		out = append(out, t.Sub.Render("   filters apply once events are collected · esc clears"))
	}
	if sec := e.snap.Events; sec != nil {
		out = append(out, "", findingsSection(t, "Events", sec.Head().Findings, sec.Head().Sources, max(3, e.h-len(out)-1), w))
	} else {
		out = append(out, "", t.Heading("Findings · Events", "", w), t.Unavail("no findings", "Events "+eventsMissing))
	}
	return strings.Join(out, "\n")
}
