package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/fsck"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
)

// fsckDialog (K): pick a filesystem, see the plan - the commands, what is
// in the way - confirm, watch the output.
type fsckDialog struct {
	targets []fsck.Target
	sel     int
	plan    *fsck.Plan
	input   lineInput // the typed confirmation of a repair
	run     *fsckRun
	msg     string
}

type fsckRun struct {
	plan   fsck.Plan
	log    []string
	step   int
	start  time.Time
	res    *fsck.Result
	ch     chan tea.Msg
	cancel context.CancelFunc
}

type (
	fsckLineMsg fsck.Event
	fsckDoneMsg fsck.Result
)

// The host the dialog works on; tests replace them.
var (
	fsckTargets = func() []fsck.Target {
		f := hostfs.Host()
		ts := fsck.Targets(f)
		if os.Geteuid() == 0 {
			for i := range ts {
				if strings.HasPrefix(ts[i].FSType, "ext") {
					ts[i].Ext, _ = fsck.ReadExtSuper(f, ts[i].Path)
				}
			}
		}
		return ts
	}
	fsckSystem = func() fsck.System { return fsck.HostSystem(hostfs.Host()) }
	fsckRunner = fsck.Run
	// fsckHostOnly: no checks against a fixture (tests turn it off)
	fsckHostOnly = true
)

// openFsck opens the dialog on the filesystem at subject (a mountpoint or
// a device), if there is one.
func (m *Model) openFsck(subject string) {
	d := &fsckDialog{input: lineInput{limit: 256}}
	if fsckHostOnly && m.env != nil && m.env.FS != nil && !m.env.FS.IsHost() {
		d.msg = "filesystem checks run on the live host only (this is a fixture)"
	} else {
		d.targets = fsckTargets()
	}
	for i, t := range d.targets {
		if t.Path == subject || "/dev/"+t.Dev == subject || t.Dev == strings.TrimPrefix(subject, "/dev/") {
			d.sel = i
		}
		for _, mt := range t.Mounts {
			if mt.Point == subject {
				d.sel = i
			}
		}
	}
	m.fsck = d
}

// fsckSubject: the filesystem or disk selected on the Storage tab.
func (m *Model) fsckSubject() string {
	st, ok := m.tabs[m.active].(*storageTab)
	if !ok || st.st == nil {
		return ""
	}
	it, ok := st.lists[st.view].current()
	if !ok {
		return ""
	}
	if st.view == 2 {
		for _, f := range st.st.Filesystems {
			if f.Mountpoint == it.key {
				return f.Source
			}
		}
	}
	return it.key
}

func (m *Model) fsckRunning() bool {
	return m.fsck != nil && m.fsck.run != nil && m.fsck.run.res == nil
}

func (m *Model) updateFsck(msg tea.KeyPressMsg) tea.Cmd {
	d := m.fsck
	k := msg.String()
	switch {
	case d.run != nil && d.run.res == nil: // running
		if k == "esc" || k == "ctrl+c" {
			if d.run.plan.Mode == fsck.Repair {
				m.flash("a repair runs to its end: stopping it halfway can leave the filesystem worse")
				return nil
			}
			d.run.cancel()
			d.msg = "stopping the check…"
		}
		return nil
	case d.run != nil: // finished
		switch k {
		case "c":
			return m.copyReport(strings.Join(d.run.log, "\n")+"\n\n"+d.run.res.State+": "+d.run.res.Text, "fsck output")
		case "esc", "q", "enter":
			d.run, d.plan = nil, nil
			d.targets = fsckTargets()
		}
		return nil
	case d.plan != nil: // the plan, waiting for a go
		p := d.plan
		if p.Confirm != "" && p.Confirm != "y" && p.OK() {
			switch k {
			case "esc":
				d.plan, d.msg = nil, ""
				d.input.SetValue("")
			case "enter":
				if d.input.Value() != p.Confirm {
					d.msg = "type " + p.Confirm + " exactly to start the repair"
					return nil
				}
				return m.startFsck()
			default:
				d.input.Update(msg)
			}
			return nil
		}
		switch {
		case k == "esc" || k == "n":
			d.plan, d.msg = nil, ""
		case !p.OK():
		case p.Confirm == "y" && k == "y", p.Confirm == "" && k == "enter":
			return m.startFsck()
		}
		return nil
	}
	switch k {
	case "esc", "q":
		m.fsck = nil
	case "up", "k":
		if d.sel > 0 {
			d.sel--
		}
	case "down", "j":
		if d.sel < len(d.targets)-1 {
			d.sel++
		}
	case "r":
		if len(d.targets) > 0 {
			d.plan, d.msg = m.fsckPlan(fsck.Repair), ""
			d.input.SetValue("")
			d.input.Focus()
		}
	case "c", "enter":
		if len(d.targets) > 0 {
			d.plan, d.msg = m.fsckPlan(fsck.Check), ""
		}
	case "u":
		d.targets, d.msg = fsckTargets(), "list updated"
	}
	return nil
}

func (m *Model) fsckPlan(mode fsck.Mode) *fsck.Plan {
	p := fsck.MakePlan(m.fsck.targets[m.fsck.sel], mode, fsckSystem())
	return &p
}

func (m *Model) startFsck() tea.Cmd {
	d := m.fsck
	ctx, cancel := context.WithCancel(context.Background())
	r := &fsckRun{plan: *d.plan, start: time.Now(), ch: make(chan tea.Msg, 1024), cancel: cancel}
	d.run, d.msg = r, ""
	go func() {
		defer cancel()
		defer close(r.ch)
		res := fsckRunner(ctx, r.plan, func(e fsck.Event) { r.ch <- fsckLineMsg(e) })
		r.ch <- fsckDoneMsg(res)
	}()
	return tea.Batch(waitSync(r.ch), m.spin.Tick)
}

func (m *Model) fsckMsg(msg tea.Msg) tea.Cmd {
	if m.fsck == nil || m.fsck.run == nil {
		return nil
	}
	r := m.fsck.run
	switch msg := msg.(type) {
	case fsckLineMsg:
		if msg.Line != "" {
			r.log = append(r.log, msg.Line)
			if len(r.log) > 2000 {
				r.log = r.log[len(r.log)-2000:]
			}
		}
		r.step = msg.Step
	case fsckDoneMsg:
		res := fsck.Result(msg)
		r.res = &res
		m.flash(fmt.Sprintf("%s of %s: %s", r.plan.Mode, r.plan.Target.Path, res.State))
		return nil
	}
	return waitSync(r.ch)
}

func (m *Model) fsckView(base string) string {
	t, d := m.theme, m.fsck
	inner := min(96, m.width-4) - 4
	var lines []string
	right := ""
	switch {
	case d.run != nil:
		lines, right = m.fsckRunLines(inner), d.run.plan.Mode.String()+" "+d.run.plan.Target.Path
	case d.plan != nil:
		lines, right = m.fsckPlanLines(inner), d.plan.Mode.String()+" "+d.plan.Target.Path
	default:
		lines = m.fsckListLines(inner)
	}
	if d.msg != "" {
		lines = append(lines, "", t.Sub.Render(d.msg))
	}
	kind := t.Accent
	if (d.plan != nil && d.plan.Mode == fsck.Repair) || (d.run != nil && d.run.plan.Mode == fsck.Repair) {
		kind = t.StatusColor(model.StatusWarn)
	}
	return overlayR(t, base, kind, "Filesystem check", right, strings.Join(lines, "\n"), m.width, m.height)
}

func (m *Model) fsckListLines(inner int) []string {
	t, d := m.theme, m.fsck
	if len(d.targets) == 0 {
		if d.msg != "" {
			return nil
		}
		return []string{t.Sub.Render("no filesystem Tyr can check (ext2/3/4, xfs, btrfs, vfat, exfat)")}
	}
	health := map[string]*model.FSHealth{}
	if m.snap != nil && m.snap.Storage != nil {
		for _, f := range m.snap.Storage.Filesystems {
			health[f.Mountpoint] = f.Health
		}
	}
	lines := []string{t.Dim.Render(fmt.Sprintf("   %-24s %-6s %9s  %-26s %s", "Device", "Type", "Size", "Mounted at", "State"))}
	maxRows := max(4, m.height-14)
	start := max(0, min(d.sel-maxRows/2, len(d.targets)-maxRows))
	for i, tg := range d.targets {
		if i < start || i >= start+maxRows {
			continue
		}
		where := t.Sub.Render("not mounted")
		if tg.Mounted() {
			var ps []string
			for _, mt := range tg.Mounts {
				ps = append(ps, mt.Point)
			}
			where = strings.Join(ps, ", ")
		}
		var state []string
		if e := tg.Ext; e != nil {
			switch {
			case e.State == "errors" || e.Errors > 0:
				state = append(state, t.StatusText(model.StatusWarn, fmt.Sprintf("errors (%d)", e.Errors)))
			case e.State == "not clean" && !tg.Mounted():
				state = append(state, t.StatusText(model.StatusWarn, "not clean"))
			default:
				state = append(state, "clean")
			}
		}
		for _, mt := range tg.Mounts {
			if h := health[mt.Point]; h != nil && h.Errors != nil && *h.Errors > 0 && tg.Ext == nil {
				state = append(state, t.StatusText(model.StatusWarn, fmt.Sprintf("%d errors", *h.Errors)))
				break
			}
		}
		if tg.Longhorn != "" {
			state = append(state, "Longhorn "+tg.Longhorn)
		} else if tg.Remote != "" {
			state = append(state, tg.Remote)
		}
		cur := "  "
		row := fmt.Sprintf("%-24s %-6s %9s  %s %s", truncate(tg.Path, 24), tg.FSType, bytesSI(tg.SizeBytes), padCell(truncate(where, 26), 26, false),
			strings.Join(state, " · "))
		if i == d.sel {
			cur = lipgloss.NewStyle().Foreground(t.Accent).Render("▌ ")
			row = t.Bold.Render(row)
		}
		lines = append(lines, truncate(cur+" "+row, inner))
	}
	sel := d.targets[d.sel]
	if e := sel.Ext; e != nil {
		info := fmt.Sprintf("superblock: %s · %d errors · mounted %d times", e.State, e.Errors, e.MountCount)
		if !e.LastCheck.IsZero() {
			info += " · last checked " + e.LastCheck.Format("2006-01-02")
		}
		lines = append(lines, "", t.Dim.Render(truncate(info, inner)))
		if e.LastError != "" {
			lines = append(lines, t.Dim.Render(truncate("last error "+e.LastError, inner)))
		}
	}
	lines = append(lines, "", t.Dim.Render("↑↓ choose · c check (reads only) · r repair (writes) · u update · esc close"))
	return lines
}

func (m *Model) fsckPlanLines(inner int) []string {
	t, p := m.theme, m.fsck.plan
	tg := p.Target
	var lines []string
	what := "A check only reads."
	if p.Mode == fsck.Repair {
		what = "A repair writes to the filesystem."
	}
	switch {
	case p.Online:
		what += " It runs on the mounted filesystem."
	case p.Unmount:
		what += " Tyr unmounts it, runs the tool and mounts it again."
	case !tg.Mounted():
		what += " It is not mounted."
	}
	lines = append(lines, wrapText(what, inner), "")
	lines = append(lines, t.Bold.Render("Commands"))
	for _, s := range p.Steps {
		lines = append(lines, truncate("  $ "+s.String(), inner))
	}
	for _, n := range p.Notes {
		lines = append(lines, "", t.Sub.Render(wrapText("note: "+n, inner)))
	}
	if !p.OK() {
		lines = append(lines, "", t.StatusBold(model.StatusCrit, "Cannot run"))
		for _, b := range p.Blockers {
			lines = append(lines, t.StatusText(model.StatusCrit, wrapText("✖ "+b, inner)))
		}
		lines = append(lines, "", t.Dim.Render("esc back"))
		return lines
	}
	lines = append(lines, "")
	switch p.Confirm {
	case "":
		lines = append(lines, t.Dim.Render("enter start · esc back"))
	case "y":
		lines = append(lines, "Unmount "+tg.Mounts[0].Point+" for the check? "+t.Dim.Render("y start · n back"))
	default:
		lines = append(lines, t.StatusBold(model.StatusWarn, "Type "+p.Confirm+" to start the repair:"), d0(m).input.Field(t, min(inner, 60)),
			t.Dim.Render("enter start · esc back"))
	}
	return lines
}

func d0(m *Model) *fsckDialog { return m.fsck }

func (m *Model) fsckRunLines(inner int) []string {
	t, r := m.theme, m.fsck.run
	var lines []string
	for i, s := range r.plan.Steps {
		mark := t.Sub.Render("○")
		switch {
		case r.res != nil || i < r.step:
			mark = t.StatusText(model.StatusOK, "●")
		case i == r.step:
			mark = lipgloss.NewStyle().Foreground(t.Accent).Render(m.spin.View())
		}
		lines = append(lines, truncate(mark+" "+s.String(), inner))
	}
	lines = append(lines, "")
	n := max(4, m.height-len(lines)-14)
	log := r.log
	if len(log) > n {
		log = log[len(log)-n:]
	}
	for _, l := range log {
		lines = append(lines, t.Dim.Render(truncate(l, inner)))
	}
	if res := r.res; res != nil {
		st := map[string]model.Status{"clean": model.StatusOK, "fixed": model.StatusOK, "errors": model.StatusWarn}[res.State]
		if st == model.StatusUnknown {
			st = model.StatusCrit
		}
		lines = append(lines, "", t.StatusBold(st, strings.ToUpper(res.State))+" "+wrapText(res.Text, inner-10),
			t.Dim.Render(fmt.Sprintf("took %s · log %s", res.Took.Round(time.Second), res.Log)), "",
			t.Dim.Render("c copy the output · esc back"))
	} else {
		took := time.Since(r.start).Round(time.Second)
		hint := "esc stops the check (the filesystem is mounted again)"
		if r.plan.Mode == fsck.Repair {
			hint = "a repair runs to its end"
		}
		lines = append(lines, "", t.Dim.Render(fmt.Sprintf("running %s · %s", took, hint)))
	}
	return lines
}

// wrapText breaks s at spaces to lines of at most w cells.
func wrapText(s string, w int) string {
	if w < 20 {
		return s
	}
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		if line != "" && len([]rune(line))+1+len([]rune(word)) > w {
			out = append(out, line)
			line = word
			continue
		}
		if line == "" {
			line = word
		} else {
			line += " " + word
		}
	}
	return strings.Join(append(out, line), "\n")
}
