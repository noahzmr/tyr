package tui

import (
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/noahzmr/tyr/internal/model"
)

// listItem is one selectable row. key identifies the component across
// refreshes and matches finding subjects; alt are other names findings may
// use for it.
type listItem struct {
	key    string
	alt    []string
	status model.Status
	row    row
}

// listView is the selection, ordering and filtering shared by every table:
// problems first (unless natural order is chosen), an optional
// problems-only filter, selection kept by key across refreshes and
// re-sorting, "n" to jump to the next problem, and focus by finding subject.
type listView struct {
	prefs  *prefs
	noSort bool // hierarchical lists (network) keep their order
	selKey string
	sel    int
	gen    int // prefs.gen at the last arrange
	all    []listItem
	shown  []listItem
}

func newListView(p *prefs) *listView { return &listView{prefs: p} }

// arrange applies filter and order to items and resolves the selection.
func (l *listView) arrange(items []listItem) []listItem {
	l.all = items
	var out []listItem
	for _, it := range items {
		if l.prefs != nil && l.prefs.problemsOnly && it.status == model.StatusOK {
			continue
		}
		out = append(out, it)
	}
	if !l.noSort && (l.prefs == nil || !l.prefs.naturalOrder) {
		sort.SliceStable(out, func(i, j int) bool { return out[i].status > out[j].status })
	}
	l.shown = out
	found := false
	if l.selKey != "" {
		for i, it := range out {
			if it.key == l.selKey {
				l.sel, found = i, true
				break
			}
		}
	}
	if !found {
		// the next row takes the selection; on the first arrange after a
		// refresh a vanished row is reported in the footer
		if l.prefs != nil && l.selKey != "" && l.gen != l.prefs.gen && !hasKey(items, l.selKey) {
			l.prefs.lost = displayKey(l.selKey)
		}
		l.sel = min(max(0, l.sel), max(0, len(out)-1))
	}
	if l.prefs != nil {
		l.gen = l.prefs.gen
	}
	if len(out) > 0 {
		l.selKey = out[l.sel].key
	}
	return out
}

func hasKey(items []listItem, k string) bool {
	for _, it := range items {
		if it.key == k {
			return true
		}
	}
	return false
}

// displayKey names a row key for messages: "disk|/dev/sdd|S/N" → "/dev/sdd",
// a tree path → its last part.
func displayKey(k string) string {
	if p := strings.Split(k, "|"); len(p) > 1 {
		return p[1]
	}
	if i := strings.LastIndex(k, "/"); i >= 0 && strings.HasPrefix(k, "/") {
		return k[i+1:]
	}
	return k
}

func (l *listView) current() (listItem, bool) {
	if l.sel < 0 || l.sel >= len(l.shown) {
		return listItem{}, false
	}
	return l.shown[l.sel], true
}

func (l *listView) setSel(i int) {
	if len(l.shown) == 0 {
		l.sel = 0
		return
	}
	l.sel = min(max(0, i), len(l.shown)-1)
	l.selKey = l.shown[l.sel].key
}

var (
	keyPgUp = key.NewBinding(key.WithKeys("pgup"))
	keyPgDn = key.NewBinding(key.WithKeys("pgdown"))
	keyHome = key.NewBinding(key.WithKeys("home", "g"))
	keyEnd  = key.NewBinding(key.WithKeys("end", "G"))
	keyNext = key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next problem"))
)

// handle moves the selection; it reports whether the key was used.
func (l *listView) handle(msg tea.KeyPressMsg, k keyMap, page int) bool {
	switch {
	case key.Matches(msg, k.Up):
		l.setSel(l.sel - 1)
	case key.Matches(msg, k.Down):
		l.setSel(l.sel + 1)
	case key.Matches(msg, keyPgUp):
		l.setSel(l.sel - max(1, page))
	case key.Matches(msg, keyPgDn):
		l.setSel(l.sel + max(1, page))
	case key.Matches(msg, keyHome):
		l.setSel(0)
	case key.Matches(msg, keyEnd):
		l.setSel(len(l.shown) - 1)
	case key.Matches(msg, keyNext):
		l.nextProblem()
	default:
		return false
	}
	return true
}

// nextProblem selects the next non-OK row after the selection (wrapping).
func (l *listView) nextProblem() bool {
	n := len(l.shown)
	for i := 1; i <= n; i++ {
		j := (l.sel + i) % n
		if l.shown[j].status != model.StatusOK {
			l.setSel(j)
			return true
		}
	}
	return false
}

// focus selects the item a finding subject refers to.
func (l *listView) focus(subject string) bool {
	for _, it := range l.all {
		names := append([]string{it.key}, it.alt...)
		for _, n := range names {
			if subjectMatches(subject, n) {
				l.selKey = it.key
				for i, s := range l.shown {
					if s.key == it.key {
						l.sel = i
					}
				}
				return true
			}
		}
	}
	return false
}

// render draws the arranged items.
func (l *listView) render(t *Theme, cols []column, width, maxRows int) string {
	rows := make([]row, len(l.shown))
	for i, it := range l.shown {
		rows[i] = it.row
		rows[i].status = it.status
	}
	if len(rows) == 0 && l.prefs != nil && l.prefs.problemsOnly && len(l.all) > 0 {
		return renderList(t, cols, nil, 0, width, maxRows) + "\n" + t.StatusText(model.StatusOK, "  ● no problems") + t.Dim.Render(" · press ! to show all")
	}
	return renderList(t, cols, rows, l.sel, width, maxRows)
}

// filterNote describes the active filter/order for a heading.
func filterNote(t *Theme, p *prefs, shown, total int) string {
	var parts []string
	if p != nil && p.problemsOnly {
		parts = append(parts, t.StatusText(model.StatusWarn, "problems only")+t.Dim.Render(" (!)"))
	}
	if shown != total {
		parts = append(parts, t.Dim.Render(strconv.Itoa(shown)+" of "+strconv.Itoa(total)))
	}
	return strings.Join(parts, t.Dim.Render(" · "))
}
