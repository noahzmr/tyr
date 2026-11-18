package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/netbox"
)

// nbSync is a write (or dry run) to NetBox while it runs and after: the
// objects stream in as they are written, with a progress bar, the time
// and the counts.
type nbSync struct {
	dry      bool
	url      string
	device   string
	start    time.Time
	end      time.Time
	ch       chan tea.Msg
	cancel   context.CancelFunc
	done     int
	total    int
	counts   map[string]int // create, update, unchanged, set, remove, delete
	recent   []netbox.Change
	problems []string
	log      []string
	result   *netboxDoneMsg // set when finished
}

type (
	nbChangeMsg struct {
		c           netbox.Change
		done, total int
	}
	nbLogMsg string
)

// waitSync waits for the sync's next message.
func waitSync(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// startSync writes this machine to NetBox (or dry-runs it) in the
// background and shows it live.
func (m *Model) startSync(cfg netbox.Config, tags netbox.Tags, snap *model.Snapshot, dry bool) tea.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	st := &nbSync{dry: dry, url: cfg.URL, start: time.Now(), ch: make(chan tea.Msg, 256), cancel: cancel,
		counts: map[string]int{}, device: snap.Hostname}
	m.nb.sync = st
	go func() {
		defer cancel()
		defer close(st.ch)
		var log []string
		in := netbox.Input{Snap: snap, OnChange: func(c netbox.Change, done, total int) {
			st.ch <- nbChangeMsg{c: c, done: done, total: total}
		}}
		rep, err := netbox.Sync(ctx, cfg, tags, in, dry, func(f string, a ...any) {
			line := fmt.Sprintf(f, a...)
			log = append(log, line)
			st.ch <- nbLogMsg(line)
		})
		st.ch <- netboxDoneMsg{dry: dry, log: log, rep: rep, err: err}
	}()
	return tea.Batch(waitSync(st.ch), m.spin.Tick)
}

// syncing reports whether a sync runs (the spinner keeps turning).
func (m *Model) syncing() bool {
	return m.nb != nil && (m.nb.busy != "" || (m.nb.sync != nil && m.nb.sync.result == nil))
}

// syncMsg handles the stream; it returns the command for the next message.
func (m *Model) syncMsg(msg tea.Msg) tea.Cmd {
	if m.nb == nil || m.nb.sync == nil {
		return nil
	}
	st := m.nb.sync
	switch msg := msg.(type) {
	case nbChangeMsg:
		st.done, st.total = msg.done, msg.total
		st.counts[msg.c.Action]++
		st.recent = append(st.recent, msg.c)
		if msg.c.Object == "device" {
			st.device = msg.c.Name
		}
	case nbLogMsg:
		line := string(msg)
		st.log = append(st.log, line)
		if strings.Contains(line, "✖") {
			st.problems = append(st.problems, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "✖")))
		}
	}
	return waitSync(st.ch)
}

// syncDone keeps the finished sync on screen with its summary.
func (m *Model) syncDone(msg netboxDoneMsg) {
	st := m.nb.sync
	st.result, st.end = &msg, time.Now()
	if msg.rep != nil {
		st.total = st.done
		if msg.rep.Device != "" {
			st.device = msg.rep.Device
		}
	}
	switch {
	case msg.err != nil:
		m.flash("NetBox: not written")
	case msg.dry:
		m.flash("NetBox dry run: nothing written")
	default:
		m.flash("written to NetBox")
	}
}

func (m *Model) updateSync(msg tea.KeyPressMsg) tea.Cmd {
	d, st := m.nb, m.nb.sync
	switch msg.String() {
	case "esc", "q":
		if st.result == nil {
			st.cancel() // the sync ends with "context canceled"
			return nil
		}
		d.sync = nil // back to the settings
	case "enter":
		if st.result != nil {
			m.netboxDone(*st.result) // the full page: tables per kind of object, the log
		}
	}
	return nil
}

var syncMarks = map[string]string{"create": "+", "update": "~", "unchanged": "=", "set": "+", "remove": "-", "delete": "-"}

// syncLines draws the live view.
func (m *Model) syncLines(inner int) []string {
	t, st := m.theme, m.nb.sync
	verb := map[bool]string{true: "Dry run against", false: "Writing to"}[st.dry]
	elapsed := time.Since(st.start)
	if st.result != nil {
		elapsed = st.end.Sub(st.start)
	}
	head := t.Bold.Render(verb+" "+st.url) + t.Sub.Render("  "+fmtElapsed(elapsed))
	var lines []string
	if st.result == nil {
		lines = append(lines, m.spin.View()+" "+head)
	} else {
		lines = append(lines, head)
	}
	lines = append(lines, t.Sub.Render("device "+orDash(st.device)), "")

	pct := 0.0
	if st.total > 0 {
		pct = min(1, float64(st.done)/float64(st.total))
	}
	if st.result != nil && st.result.err == nil {
		pct = 1
	}
	lines = append(lines, progressBar(t, pct, max(20, min(60, inner-24)))+"  "+t.Bold.Render(fmt.Sprintf("%3.0f%%", pct*100))+t.Sub.Render(fmt.Sprintf("  %d of ~%d", st.done, st.total)))
	c := st.counts
	newWord, chWord := "new", "changed"
	if st.dry {
		newWord, chWord = "would create", "would change"
	}
	counts := []string{
		t.StatusText(model.StatusOK, fmt.Sprintf("%d %s", c["create"]+c["set"], newWord)),
		t.StatusText(model.StatusWarn, fmt.Sprintf("%d %s", c["update"]+c["remove"]+c["delete"], chWord)),
		t.Sub.Render(fmt.Sprintf("%d unchanged", c["unchanged"])),
	}
	if n := len(st.problems); n > 0 {
		counts = append(counts, t.StatusText(model.StatusCrit, fmt.Sprintf("%d problems", n)))
	}
	lines = append(lines, strings.Join(counts, t.Sub.Render(" · ")), "")

	// the latest objects, newest last (package-manager style)
	const show = 12
	from := max(0, len(st.recent)-show)
	for _, ch := range st.recent[from:] {
		mark := syncMarks[ch.Action]
		style := t.Sub
		switch ch.Action {
		case "create", "set":
			style = lipgloss.NewStyle().Foreground(t.Accent)
		case "update", "remove", "delete":
			style = lipgloss.NewStyle().Foreground(t.StatusColor(model.StatusWarn))
		}
		name := pad(truncate(ch.Object+" "+ch.Name, 34), 34)
		lines = append(lines, style.Render(mark+" "+name)+" "+t.Sub.Render(truncate(ch.Detail, max(10, inner-38))))
	}
	for i := len(st.recent[from:]); i < show; i++ {
		lines = append(lines, "")
	}
	for _, p := range st.problems[max(0, len(st.problems)-3):] {
		lines = append(lines, t.StatusText(model.StatusCrit, "✖ "+truncate(p, inner-2)))
	}

	lines = append(lines, "")
	if r := st.result; r != nil {
		switch {
		case r.err != nil:
			lines = append(lines, t.StatusText(model.StatusCrit, "✖ not written: "+truncate(r.err.Error(), inner-16)))
		case r.rep != nil:
			sum := r.rep.Summary()
			if st.dry {
				sum = "would " + sum
			}
			done := "✔ " + sum
			if r.rep.DeviceURL != "" {
				done += " · " + r.rep.DeviceURL
			}
			lines = append(lines, t.StatusText(model.StatusOK, truncate(done, inner)))
		}
		lines = append(lines, t.Sub.Render("enter: all objects as tables, and the log · esc: back to the settings"))
	} else {
		lines = append(lines, t.Sub.Render("esc cancels"))
	}
	return lines
}

// fmtElapsed: "4.2s", "1m07s".
func fmtElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// progressBar is a bar in the accent colour, half blocks for a finer
// edge: "████████▌░░░░░░".
func progressBar(t *Theme, pct float64, w int) string {
	halves := int(pct*float64(2*w) + 0.5)
	halves = min(max(halves, 0), 2*w)
	full, half := halves/2, halves%2
	s := strings.Repeat("█", full)
	if half == 1 {
		s += "▌"
	}
	return lipgloss.NewStyle().Foreground(t.Accent).Render(s) + t.Dim.Render(strings.Repeat("░", w-full-half))
}
