package tui

import (
	"fmt"
	"strconv"
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
	src    int // 0 all, 1 BMC, 2 kernel
	search lineInput
	expand bool
	list   *listView
}

const eventsMissing = "not collected"

var (
	keySeverity = key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "severity"))
	keySource   = key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "source"))
	keyDetails  = key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "details"))
)

func newEventsTab() *eventsTab {
	l := newListView(nil)
	l.noSort = true // newest first
	return &eventsTab{keys: newKeyMap(), search: lineInput{placeholder: "search events", limit: 64}, list: l}
}

func (e *eventsTab) Title() string             { return "Events" }
func (e *eventsTab) Module() model.ModuleID    { return model.ModEvents }
func (e *eventsTab) SetSize(w, h int)          { e.w, e.h = w, h }
func (e *eventsTab) SetData(s *model.Snapshot) { e.snap = s; e.arrange() }
func (e *eventsTab) Capturing() bool           { return e.search.Focused() }

// Claims: on this tab "s" is the source filter, not "go to Stress".
func (e *eventsTab) Claims(msg tea.KeyPressMsg) bool { return key.Matches(msg, keySource) }

func (e *eventsTab) Help() []key.Binding {
	return []key.Binding{e.keys.Up, key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
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
		e.arrange()
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
	case key.Matches(msg, keyNext):
		e.list.nextProblem()
		return nil
	default:
		e.list.handle(msg, e.keys, max(1, e.h/2))
		return nil
	}
	e.arrange()
	return nil
}

func (e *eventsTab) events() []model.Event {
	if e.snap == nil || e.snap.Events == nil {
		return nil
	}
	return e.snap.Events.Entries
}

// arrange applies the filters; the list keys are entry indexes.
func (e *eventsTab) arrange() {
	q := strings.ToLower(strings.TrimSpace(e.search.Value()))
	evs := e.events()
	var items []listItem
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		switch {
		case e.sev == 1 && (ev.Severity < model.StatusWarn || ev.Severity == model.StatusUnknown):
			continue
		case e.sev == 2 && ev.Severity != model.StatusCrit:
			continue
		case e.src == 1 && ev.Source == "kernel", e.src == 2 && ev.Source != "kernel":
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(ev.Message+" "+ev.Component+" "+ev.Class+" "+ev.Log), q) {
			continue
		}
		items = append(items, listItem{key: fmt.Sprint(i), status: ev.Severity})
	}
	e.list.arrange(items)
}

func (e *eventsTab) event(key string) (model.Event, bool) {
	i, err := strconv.Atoi(key)
	evs := e.events()
	if err != nil || i < 0 || i >= len(evs) {
		return model.Event{}, false
	}
	return evs[i], true
}

func evTime(ev model.Event) string {
	if ev.Time.IsZero() {
		return "–"
	}
	return ev.Time.Local().Format("01-02 15:04")
}

func evSource(ev model.Event) string {
	switch ev.Source {
	case "kernel":
		return "kernel"
	case "ipmi":
		return "SEL"
	}
	l := ev.Log
	if i := strings.Index(l, " ("); i > 0 {
		l = l[:i]
	}
	return l
}

// Detail: the whole entry.
func (e *eventsTab) Detail(t *Theme, w int) (string, string) {
	it, ok := e.list.current()
	if !ok {
		return "", ""
	}
	ev, _ := e.event(it.key)
	doc := newDoc(t, w)
	doc.section("Event", t.Badge(ev.Severity))
	when := ""
	if !ev.Time.IsZero() {
		when = ev.Time.Local().Format("2006-01-02 15:04:05")
	}
	upd := ""
	if !ev.Updated.IsZero() && !ev.Updated.Equal(ev.Time) {
		upd = ev.Updated.Local().Format("2006-01-02 15:04:05")
	}
	rep := ""
	if ev.Repaired != nil {
		rep = map[bool]string{true: t.StatusText(model.StatusOK, "yes"), false: t.StatusText(model.StatusWarn, "no")}[*ev.Repaired]
	}
	count := ""
	if ev.Count > 1 {
		count = fmt.Sprint(ev.Count)
	}
	doc.kv([2]string{"Time", when}, [2]string{"Last seen", upd}, [2]string{"Log", ev.Log}, [2]string{"Entry", ev.ID},
		[2]string{"Class", ev.Class}, [2]string{"Component", ev.Component}, [2]string{"Count", count}, [2]string{"Repaired", rep},
		[2]string{"Message", ev.Message})
	return evSource(ev) + " " + ev.ID, doc.String()
}

func (e *eventsTab) PageInfo() (pageInfo, bool) {
	it, ok := e.list.current()
	if !ok {
		return pageInfo{}, false
	}
	ev, _ := e.event(it.key)
	return pageInfo{path: []string{evSource(ev), evTime(ev)}, status: ev.Severity}, true
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
	line := group("Severity", []string{"all", "warn+", "crit"}, e.sev) + "   " + group("Source", []string{"all", "BMC", "kernel"}, e.src) +
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
	out := []string{e.chips(t)}
	sec := e.snap.Events
	if sec == nil {
		out = append(out, "", t.Unavail("Events", eventsMissing))
		return strings.Join(out, "\n")
	}
	var logs []string
	for _, l := range sec.Logs {
		logs = append(logs, fmt.Sprintf("%s %d", l.Name, l.Entries))
	}
	line := t.Dim.Render("Logs  ") + strings.Join(logs, t.Dim.Render(" · "))
	if st, ok := sec.Source("redfish"); ok && st.State != model.AvailOK {
		line += "   " + t.Unavail("Redfish", st.Reason)
	}
	out = append(out, truncate(line, w), "")
	cols := []column{{"Time", 11}, {"Sev", 4}, {"Log", 14}, {"Component", 16}, {"Message", 0}}
	if w < 100 {
		cols = []column{{"Time", 11}, {"Sev", 4}, {"Message", 0}}
	}
	for i := range e.list.shown {
		ev, _ := e.event(e.list.shown[i].key)
		comp := ev.Component
		if comp == "" {
			comp = ev.Class
		}
		cells := map[string]string{"Time": evTime(ev), "Sev": t.StatusText(ev.Severity, strings.ToLower(ev.Severity.String())),
			"Log": evSource(ev), "Component": orDash(comp), "Message": ev.Message}
		if ev.Repaired != nil && *ev.Repaired {
			cells["Message"] = t.Sub.Render("(repaired) ") + ev.Message
		}
		var r row
		for _, c := range cols {
			r.cells = append(r.cells, cells[c.title])
		}
		e.list.shown[i].row = r
	}
	detail := 0
	if e.expand {
		detail = 6
	}
	rows := max(3, e.h-len(out)-detail-9)
	if len(e.list.shown) == 0 {
		head := renderList(t, cols, nil, -1, w, 1)
		msg := "no events"
		if strings.TrimSpace(e.search.Value()) != "" || e.sev != 0 || e.src != 0 {
			msg = "no events match the filters · esc clears"
		}
		out = append(out, strings.Split(head, "\n")[0], "   "+t.Sub.Render(msg))
	} else {
		out = append(out, strings.Split(e.list.render(t, cols, w, rows+1), "\n")...)
	}
	if e.expand {
		if it, ok := e.list.current(); ok {
			ev, _ := e.event(it.key)
			out = append(out, "", t.Heading(evSource(ev)+" "+ev.ID, t.Badge(ev.Severity), w))
			out = append(out, hang(" ", ev.Message, w-1))
			var facts []string
			for _, f := range []string{ev.Class, ev.Component} {
				if f != "" {
					facts = append(facts, f)
				}
			}
			if ev.Count > 1 {
				facts = append(facts, fmt.Sprintf("%d×", ev.Count))
			}
			if ev.Repaired != nil {
				facts = append(facts, map[bool]string{true: "repaired", false: "not repaired"}[*ev.Repaired])
			}
			if len(facts) > 0 {
				out = append(out, " "+t.Dim.Render(strings.Join(facts, " · ")))
			}
		}
	}
	out = append(out, "", findingsSection(t, "Events", sec.Head().Findings, sec.Head().Sources, max(3, e.h-len(out)-1), w))
	return strings.Join(out, "\n")
}
