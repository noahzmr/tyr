package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/isobuild"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/netbox"
	"github.com/noahzmr/tyr/internal/notify"
)

// isoDialog (I) builds the Tyr ISO with the current NetBox and webhook
// settings and writes it to a USB stick.
type isoDialog struct {
	src      string
	input    lineInput
	editing  bool
	arch     int // 0 amd64, 1 arm64
	netbox   bool
	webhooks bool
	nbCfg    netbox.Config
	nbTags   netbox.Tags
	whCfg    notify.Config
	disks    []isobuild.Disk
	target   int // 0: only the ISO file, i: disks[i-1]
	field    int
	docker   string // why Docker cannot be used, "" ok, "…" while checking
	confirm  bool
	run      *isoRun
	err, msg string
}

type isoRun struct {
	start, end time.Time
	arch       string
	disk       *isobuild.Disk
	phase      int // build phase, len(Phases) when built
	log        []string
	stage      string // build, write, verify, done
	done       int64
	total      int64
	stageStart time.Time
	iso        string
	err        error
	ch         chan tea.Msg
	cancel     context.CancelFunc
}

type (
	isoLineMsg  string
	isoBuiltMsg string // the ISO's path
	isoProgMsg  struct {
		stage       string
		done, total int64
	}
	isoFinishMsg struct{ err error }
	isoDockerMsg string
)

var isoArchs = []string{"amd64", "arm64"}

// The host the dialog looks at; tests replace them.
var (
	isoDisks  = func() ([]isobuild.Disk, error) { return isobuild.USBDisks("/") }
	isoDocker = func() string { return isobuild.Docker(context.Background()) }
	isoRoot   = func() bool { return os.Geteuid() == 0 }
)
var isoFields = []string{"source", "arch", "netbox", "webhooks", "target", "build"}

func (m *Model) openISO() tea.Cmd {
	d := &isoDialog{input: lineInput{limit: 1024}, docker: "…"}
	d.src = isobuild.FindSource()
	np, tp := netbox.Paths("")
	if m.opts.NetBoxFile != "" {
		np = m.opts.NetBoxFile
	}
	if m.opts.TagsFile != "" {
		tp = m.opts.TagsFile
	}
	var errs []string
	var err error
	if d.nbCfg, err = netbox.LoadConfig(np); err != nil {
		errs = append(errs, err.Error())
	}
	if d.nbTags, err = netbox.LoadTags(tp); err != nil {
		errs = append(errs, err.Error())
	}
	wp := m.opts.WebhooksFile
	if wp == "" {
		wp = notify.Path()
	}
	if d.whCfg, err = notify.Load(wp); err != nil {
		errs = append(errs, err.Error())
	}
	d.netbox, d.webhooks = d.nbCfg.Enabled(), len(d.whCfg.Webhooks) > 0
	d.err = strings.Join(errs, "; ")
	d.refreshDisks()
	m.iso = d
	return func() tea.Msg { return isoDockerMsg(isoDocker()) }
}

func (d *isoDialog) refreshDisks() {
	ds, err := isoDisks()
	if err != nil {
		d.err = err.Error()
	}
	d.disks = ds
	d.target = min(d.target, len(ds))
}

func (d *isoDialog) disk() *isobuild.Disk {
	if d.target == 0 || d.target > len(d.disks) {
		return nil
	}
	return &d.disks[d.target-1]
}

// problem is why the build cannot start, "" when it can.
func (d *isoDialog) problem() string {
	switch {
	case d.src == "" || !isobuild.IsSource(d.src):
		return "no Tyr source tree (iso/build.sh): enter its path, or set TYR_SOURCE"
	case d.docker == "…":
		return "checking Docker …"
	case d.docker != "":
		return d.docker
	}
	if k := d.disk(); k != nil {
		switch {
		case k.Why != "":
			return k.Dev + ": " + k.Why
		case !isoRoot():
			return "writing a stick needs root: start tyr with sudo (the build itself runs as you)"
		}
	}
	return ""
}

// isoRunning reports whether a build or write runs (the spinner turns).
func (m *Model) isoRunning() bool {
	return m.iso != nil && m.iso.run != nil && m.iso.run.stage != "done"
}

func (m *Model) updateISO(msg tea.KeyPressMsg) tea.Cmd {
	d := m.iso
	k := msg.String()
	if r := d.run; r != nil {
		switch k {
		case "esc", "q":
			if r.stage != "done" {
				r.cancel()
				return nil
			}
			m.iso = nil
		case "enter":
			if r.stage == "done" {
				m.iso = nil
			}
		}
		return nil
	}
	if d.confirm {
		switch k {
		case "y", "Y":
			d.confirm = false
			return m.startISO()
		default:
			d.confirm, d.msg = false, "not started"
		}
		return nil
	}
	if d.editing {
		switch k {
		case "esc":
			d.editing = false
		case "enter":
			d.src, d.editing = strings.TrimSpace(d.input.Value()), false
		default:
			d.input.Update(msg)
		}
		return nil
	}
	f := isoFields[d.field]
	switch k {
	case "esc", "q":
		m.iso = nil
	case "up", "k":
		d.field = max(0, d.field-1)
	case "down", "j", "tab":
		d.field = min(len(isoFields)-1, d.field+1)
	case "left", "h":
		if f == "target" {
			d.target = (d.target + len(d.disks)) % (len(d.disks) + 1)
			return nil
		}
		return m.updateISO(tea.KeyPressMsg{Code: ' ', Text: " "})
	case "right", "l", "space", " ":
		switch f {
		case "target":
			d.target = (d.target + 1) % (len(d.disks) + 1)
		case "arch":
			d.arch = 1 - d.arch
		case "netbox":
			d.netbox = !d.netbox && d.nbCfg.Enabled()
		case "webhooks":
			d.webhooks = !d.webhooks && len(d.whCfg.Webhooks) > 0
		}
	case "r":
		d.refreshDisks()
		d.msg = "USB disks read again"
	case "enter":
		switch f {
		case "source":
			d.editing = true
			d.input.SetValue(d.src)
			d.input.Focus()
		case "build":
			d.err, d.msg = "", ""
			if p := d.problem(); p != "" {
				d.err = p
				return nil
			}
			if d.disk() != nil {
				d.confirm = true
				return nil
			}
			return m.startISO()
		default:
			return m.updateISO(tea.KeyPressMsg{Code: ' ', Text: " "})
		}
	}
	return nil
}

// startISO writes the settings, builds the ISO and, with a stick chosen,
// writes and verifies it - in the background, streamed to the view.
func (m *Model) startISO() tea.Cmd {
	d := m.iso
	ctx, cancel := context.WithCancel(context.Background())
	r := &isoRun{start: time.Now(), arch: isoArchs[d.arch], disk: d.disk(), stage: "build", ch: make(chan tea.Msg, 512), cancel: cancel}
	d.run = r
	set := isobuild.Settings{}
	if d.netbox {
		set.NetBox, set.Tags = d.nbCfg, d.nbTags
	}
	if d.webhooks {
		set.Webhooks = d.whCfg
	}
	src := d.src
	go func() {
		defer cancel()
		defer close(r.ch)
		dir, err := isobuild.WriteSettings(set)
		if err != nil {
			r.ch <- isoFinishMsg{err: err}
			return
		}
		iso, err := isobuild.Build(ctx, src, r.arch, dir, func(l string) { r.ch <- isoLineMsg(l) })
		os.RemoveAll(dir)
		if err != nil {
			r.ch <- isoFinishMsg{err: err}
			return
		}
		r.ch <- isoBuiltMsg(iso)
		if r.disk != nil {
			last := time.Time{}
			err = isobuild.Write(ctx, iso, *r.disk, func(stage string, done, total int64) {
				if now := time.Now(); now.Sub(last) > 200*time.Millisecond || done == total {
					last = now
					r.ch <- isoProgMsg{stage: stage, done: done, total: total}
				}
			})
		}
		r.ch <- isoFinishMsg{err: err}
	}()
	return tea.Batch(waitSync(r.ch), m.spin.Tick)
}

// isoMsg handles the stream; it returns the command for the next message.
func (m *Model) isoMsg(msg tea.Msg) tea.Cmd {
	if m.iso == nil {
		return nil
	}
	d := m.iso
	if dm, ok := msg.(isoDockerMsg); ok {
		d.docker = string(dm)
		return nil
	}
	r := d.run
	if r == nil {
		return nil
	}
	switch msg := msg.(type) {
	case isoLineMsg:
		l := string(msg)
		if p := isobuild.Phase(l); p >= 0 {
			r.phase = p
		}
		r.log = append(r.log, l)
		if len(r.log) > 300 {
			r.log = r.log[len(r.log)-300:]
		}
	case isoBuiltMsg:
		r.iso, r.phase = string(msg), len(isobuild.Phases)
		if r.disk != nil {
			r.stage, r.stageStart = "write", time.Now()
		}
	case isoProgMsg:
		if msg.stage != r.stage {
			r.stage, r.stageStart = msg.stage, time.Now()
		}
		r.done, r.total = msg.done, msg.total
	case isoFinishMsg:
		r.err, r.stage, r.end = msg.err, "done", time.Now()
		switch {
		case msg.err != nil:
			m.flash("ISO: " + firstLine(msg.err.Error()))
		case r.disk != nil:
			m.flash("ISO written to " + r.disk.Dev + " and verified")
		default:
			m.flash("ISO built: " + r.iso)
		}
		return nil
	}
	return waitSync(r.ch)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

func (m *Model) isoView(base string) string {
	t, d := m.theme, m.iso
	inner := min(96, m.width-4) - 4
	var lines []string
	if d.run != nil {
		lines = m.isoRunLines(inner)
	} else {
		lines = m.isoFormLines(inner)
	}
	if d.err != "" {
		lines = append(lines, "", t.StatusText(model.StatusCrit, "✖ "+truncate(d.err, inner-2)))
	} else if d.msg != "" {
		lines = append(lines, "", t.Sub.Render(d.msg))
	}
	return overlayR(t, base, t.Accent, "ISO and USB stick", "", strings.Join(lines, "\n"), m.width, m.height)
}

func (m *Model) isoFormLines(inner int) []string {
	t, d := m.theme, m.iso
	const lw = 11
	row := func(i int, label, v string) string { return fieldLabel(t, label, d.field == i, lw) + v }
	var lines []string
	src := d.src
	if d.editing {
		src = d.input.Field(t, max(20, inner-lw-2))
	} else {
		in := lineInput{value: []rune(orDash(src))}
		src = in.Field(t, max(20, inner-lw-2))
	}
	lines = append(lines, row(0, "Source", src))
	note := "Docker: checking …"
	switch {
	case d.docker == "":
		note = "Docker ready · the build runs iso/build.sh (Go, Docker, internet; 5-10 min)"
	case d.docker != "…":
		note = "Docker: " + d.docker
	}
	lines = append(lines, strings.Repeat(" ", lw)+t.Sub.Render(truncate(note, inner-lw)))
	arch := ""
	for i, a := range isoArchs {
		arch += pill(t, a, d.arch == i)
	}
	lines = append(lines, row(1, "Arch", arch))
	onOff := func(on, avail bool, what, missing string) string {
		if !avail {
			return t.Sub.Render("not set - " + missing)
		}
		return pill(t, "off", !on) + pill(t, "on", on) + " " + t.Sub.Render(truncate(what, inner-lw-12))
	}
	nb := d.nbCfg.URL
	if d.nbCfg.Site != "" {
		nb += " · site " + d.nbCfg.Site
	}
	if n := len(d.nbTags.Rules); n > 0 {
		nb += fmt.Sprintf(" · %d tag rules", n)
	}
	lines = append(lines, row(2, "NetBox", onOff(d.netbox, d.nbCfg.Enabled(), nb, "N to set it up")))
	var names []string
	for _, w := range d.whCfg.Webhooks {
		names = append(names, w.Label())
	}
	lines = append(lines, row(3, "Webhooks", onOff(d.webhooks, len(names) > 0, strings.Join(names, ", "), "W to add one")))
	lines = append(lines, "", fieldLabel(t, "Target", d.field == 4, lw)+t.Sub.Render("←→ choose · r read the USB disks again"))
	target := func(i int, label, detail string, disabled bool) {
		mark := "○"
		if d.target == i {
			mark = "●"
		}
		l := mark + " " + pad(truncate(label, 34), 34) + " " + t.Sub.Render(truncate(detail, max(10, inner-lw-38)))
		switch {
		case disabled:
			l = lipgloss.NewStyle().Foreground(t.NA).Render(stripStyle(l))
		case d.target == i && d.field == 4:
			l = lipgloss.NewStyle().Foreground(t.Accent).Render(mark+" ") + t.Bold.Render(pad(truncate(label, 34), 34)) + " " + t.Sub.Render(truncate(detail, max(10, inner-lw-38)))
		}
		lines = append(lines, strings.Repeat(" ", lw)+l)
	}
	target(0, "only build the ISO file", "dist/ in the source tree, for BMC virtual media", false)
	for i, k := range d.disks {
		detail := k.Why
		if detail == "" {
			var parts []string
			if len(k.Mounts) > 0 {
				parts = append(parts, "mounted "+strings.Join(k.Mounts, ", "))
			}
			if k.Serial != "" {
				parts = append(parts, "S/N "+truncate(k.Serial, 16))
			}
			detail = strings.Join(parts, " · ")
		}
		target(i+1, k.Name+"  "+k.Label(), detail, k.Why != "")
	}
	if len(d.disks) == 0 {
		lines = append(lines, strings.Repeat(" ", lw)+t.Sub.Render("no USB disks - plug a stick in and press r"))
	}
	btn := "Build ISO"
	if k := d.disk(); k != nil {
		btn = "Build and write to " + k.Name
	}
	lines = append(lines, "", strings.Repeat(" ", lw)+button(t, btn, d.field == 5))
	if d.netbox || d.webhooks {
		lines = append(lines, strings.Repeat(" ", lw)+t.Sub.Render("tokens and URLs are inside the image: keep the ISO and the stick in-house"))
	}
	if d.confirm {
		k := d.disk()
		q := fmt.Sprintf("Erase %s (%s) and write the ISO to it? y / n", k.Dev, k.Label())
		lines = append(lines, "", t.StatusBold(model.StatusWarn, q))
		if len(k.Mounts) > 0 {
			lines = append(lines, t.Sub.Render("its mounted partitions are unmounted first: "+truncate(strings.Join(k.Mounts, ", "), inner-46)))
		}
	} else {
		lines = append(lines, "", t.Sub.Render("↑↓ field · ←→ change · enter edit / build · esc close"))
	}
	return lines
}

func (m *Model) isoRunLines(inner int) []string {
	t, r := m.theme, m.iso.run
	end := time.Now()
	if r.stage == "done" {
		end = r.end
	}
	title := "Building the Tyr ISO (" + r.arch + ")"
	if r.disk != nil {
		title += " for " + r.disk.Dev
	}
	head := t.Bold.Render(title) + t.Sub.Render("  "+fmtElapsed(end.Sub(r.start)))
	if r.stage != "done" {
		head = m.spin.View() + " " + head
	}
	lines := []string{head, ""}
	step := func(state model.Status, active bool, name, detail string) {
		mark := t.Sub.Render("·")
		switch {
		case active:
			mark = m.spin.View()
		case state == model.StatusOK:
			mark = t.StatusText(model.StatusOK, "✔")
		case state == model.StatusCrit:
			mark = t.StatusText(model.StatusCrit, "✖")
		}
		l := mark + " " + pad(name, 26)
		if active {
			l = mark + " " + t.Bold.Render(pad(name, 26))
		}
		lines = append(lines, l+t.Sub.Render(truncate(detail, max(10, inner-30))))
	}
	failedAt := -1
	if r.err != nil {
		failedAt = r.phase
	}
	building := r.iso == "" && r.stage != "done"
	for i, p := range isobuild.Phases {
		st, active := model.StatusUnknown, false
		switch {
		case i < r.phase || r.iso != "":
			st = model.StatusOK
		case i == failedAt && r.iso == "":
			st = model.StatusCrit
		case i == r.phase && building:
			active = true
		}
		step(st, active, p.Name, "")
	}
	if r.disk != nil {
		for _, s := range []string{"write", "verify"} {
			st, active, detail := model.StatusUnknown, false, ""
			name := map[string]string{"write": "write to " + r.disk.Name, "verify": "read back and compare"}[s]
			switch {
			case r.stage == s:
				active = true
				detail = byteProgress(r.done, r.total, time.Since(r.stageStart))
			case s == "write" && (r.stage == "verify" || (r.stage == "done" && r.err == nil)):
				st = model.StatusOK
			case s == "verify" && r.stage == "done" && r.err == nil:
				st = model.StatusOK
			}
			step(st, active, name, detail)
		}
	}
	lines = append(lines, "")
	// a bar: the build by phase, write and verify by bytes
	pct := float64(r.phase) / float64(len(isobuild.Phases))
	if r.stage == "write" || r.stage == "verify" {
		pct = float64(r.done) / float64(max(1, r.total))
	}
	if r.stage == "done" && r.err == nil {
		pct = 1
	}
	lines = append(lines, progressBar(t, pct, max(20, min(60, inner-10)))+t.Bold.Render(fmt.Sprintf("  %3.0f%%", pct*100)), "")
	// the build's latest output
	tail := r.log
	if len(tail) > 8 {
		tail = tail[len(tail)-8:]
	}
	for _, l := range tail {
		lines = append(lines, t.Sub.Render(truncate(l, inner)))
	}
	lines = append(lines, "")
	switch {
	case r.stage != "done":
		lines = append(lines, t.Sub.Render("esc cancels"))
	case r.err != nil:
		lines = append(lines, t.StatusText(model.StatusCrit, "✖ "+truncate(r.err.Error(), inner-2)), t.Sub.Render("esc close"))
	case r.disk != nil:
		lines = append(lines, t.StatusText(model.StatusOK, truncate("✔ "+r.iso+" written to "+r.disk.Dev+" and verified", inner)),
			t.Sub.Render("The stick boots the burn-in with these settings. enter close"))
	default:
		lines = append(lines, t.StatusText(model.StatusOK, truncate("✔ "+r.iso, inner)),
			t.Sub.Render("For BMC virtual media, or write it with dd. enter close"))
	}
	return lines
}

// byteProgress: "312 MB of 417 MB · 28 MB/s".
func byteProgress(done, total int64, took time.Duration) string {
	s := fmt.Sprintf("%d MB of %d MB", done>>20, total>>20)
	if took > time.Second && done > 0 {
		s += fmt.Sprintf(" · %.0f MB/s", float64(done)/(1<<20)/took.Seconds())
	}
	return s
}
