// Package tui is the interactive Bubble Tea front end. Collectors run
// asynchronously as tea.Cmds; the UI never blocks on hardware access.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/report"
	"github.com/noahzmr/tyr/internal/stress"
)

// Options configure the TUI.
type Options struct {
	Refresh      time.Duration // live refresh interval (default 5s)
	FixtureName  string        // non-empty in --fixtures mode (shown in header)
	ExportDir    string        // where `e` writes snapshots (default ".")
	Mouse        bool          // enable mouse (click tabs, wheel scrolls); disables terminal text selection
	NetBoxFile   string        // NetBox settings (N); "" = netbox.Paths
	TagsFile     string        // tag rules for NetBox (N); "" = netbox.Paths
	WebhooksFile string        // webhooks (W); "" = notify.Path
}

// tab is one tab's sub-model.
type tab interface {
	Title() string
	Module() model.ModuleID // "" for tabs without a module (Stress)
	SetData(s *model.Snapshot)
	SetSize(w, h int)
	Update(msg tea.KeyPressMsg) tea.Cmd
	View(t *Theme) string
	Help() []key.Binding
}

type (
	snapshotMsg struct {
		snap *model.Snapshot
		took time.Duration
	}
	tickMsg      struct{ seq int }
	clockMsg     struct{}
	switchTabMsg struct {
		module  model.ModuleID
		subject string // select this component in the target tab
	}
	statusMsg string
	// explainMsg opens the explanation page of one alert.
	explainMsg struct{ alert report.Alert }
)

// Model is the root model.
type Model struct {
	env  *collector.Env
	cols []collector.Collector
	opts Options

	ctx    context.Context
	cancel context.CancelFunc

	keys    keyMap
	help    help.Model
	spin    spinner.Model
	theme   *Theme
	tabs    []tab
	active  int
	showAll bool

	dark     bool                 // terminal background (BackgroundColorMsg)
	profile  colorprofile.Profile // colour depth (ColorProfileMsg)
	sess     *session
	page     *detailPage    // full-screen component page (enter), nil when closed
	showHelp bool           // help overlay
	reports  *reportDialog  // R: generate a report, nil when closed
	nb       *netboxDialog  // N: NetBox settings, tag rules, write, nil when closed
	wh       *webhookDialog // W: webhooks for test notifications, nil when closed
	iso      *isoDialog     // I: build the ISO, write a USB stick, nil when closed
	blurred  bool           // terminal window not focused: refresh slower
	lastAt   time.Time      // when the last snapshot arrived
	tabXs    []int          // right edge of each rendered tab (mouse hit test)

	snap       *model.Snapshot
	collecting bool
	paused     bool
	tickSeq    int
	lastTook   time.Duration
	message    string
	messageAt  time.Time

	width, height int
}

// New creates the root model.
func New(env *collector.Env, cols []collector.Collector, implemented func(model.ModuleID) bool, opts Options) *Model {
	if opts.Refresh <= 0 {
		opts.Refresh = 5 * time.Second
	}
	if opts.ExportDir == "" {
		opts.ExportDir = "."
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Model{
		env: env, cols: cols, opts: opts, ctx: ctx, cancel: cancel,
		keys: newKeyMap(), help: help.New(), theme: NewTheme(true), sess: newSession(), dark: true, profile: colorprofile.TrueColor,
		spin:  spinner.New(spinner.WithSpinner(spinner.Dot)),
		width: 80, height: 24,
	}
	m.theme.sess = m.sess
	m.sess.prefs.mouse = opts.Mouse
	p := &m.sess.prefs
	m.help.Styles = helpStyles(m.theme)
	m.help.ShortSeparator = "  "
	m.tabs = []tab{
		newOverviewTab(implemented, p),
		newCPUTab(p),
		newMemoryTab(p),
		newStorageTab(p),
		newPowerTab(p),
		newNetworkTab(p),
		newAccelTab(p),
		newEventsTab(),
		newStressTab(ctx, &stress.Env{Collector: env, Cols: cols}),
		newInventoryTab(p),
	}
	m.resize()
	if cpu, ok := stress.OfflineCorePending(); ok && env != nil && env.FS != nil && env.FS.IsHost() {
		m.flash(fmt.Sprintf("an offline-core test did not finish: cpu%d may still be online - run: sudo tyr core-test --restore", cpu))
	}
	return m
}

// Init starts the first collection and asks for the background color.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.spin.Tick, m.collect(), clockTick())
}

// clockTick re-renders once a second so "updated Ns ago" stays current.
func clockTick() tea.Cmd { return tea.Tick(time.Second, func(time.Time) tea.Msg { return clockMsg{} }) }

func (m *Model) collect() tea.Cmd { return m.collectWith(m.ctx) }

// collectFresh is r: also re-reads SMART instead of using the cached data.
func (m *Model) collectFresh() tea.Cmd {
	return m.collectWith(collector.WithSMARTMaxAge(m.ctx, 0))
}

func (m *Model) collectWith(ctx context.Context) tea.Cmd {
	if m.collecting {
		return nil
	}
	m.collecting = true
	prev := m.snap
	env, cols := m.env, m.cols
	return func() tea.Msg {
		start := time.Now()
		s := collector.Collect(ctx, env, cols, prev)
		return snapshotMsg{snap: s, took: time.Since(start)}
	}
}

func (m *Model) scheduleTick() tea.Cmd {
	m.tickSeq++
	seq := m.tickSeq
	d := m.opts.Refresh
	if m.blurred {
		d *= 3 // terminal in the background: refresh less often
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{seq} })
}

// messageLine renders the footer message as a toast; the glyph follows
// the kind of message.
func (m *Model) messageLine() string {
	t := m.theme
	msg := m.message
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "failed") || strings.Contains(low, "error"):
		return toast(t, model.StatusCrit, msg)
	case strings.HasPrefix(low, "exported") || strings.Contains(low, "resumed"):
		return toast(t, model.StatusOK, msg)
	case strings.Contains(low, "running") || strings.Contains(low, "paused") || strings.Contains(low, "stalled") || strings.Contains(low, "no row"):
		return toast(t, model.StatusWarn, msg)
	}
	return toast(t, model.StatusUnknown, msg)
}

func (m *Model) flash(s string) {
	m.message, m.messageAt = s, time.Now()
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil

	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.retheme()
		return m, nil

	case tea.ColorProfileMsg:
		m.profile = msg.Profile
		m.retheme()
		return m, nil

	case clockMsg:
		return m, clockTick()

	case tea.BlurMsg:
		m.blurred = true
		return m, nil

	case tea.FocusMsg:
		m.blurred = false
		if m.snap != nil && !m.paused && time.Since(m.lastAt) > m.opts.Refresh {
			return m, m.collect()
		}
		return m, nil

	case tea.MouseClickMsg:
		return m, m.handleClick(msg)

	case tea.MouseWheelMsg:
		k := tea.KeyPressMsg{Code: tea.KeyDown}
		if msg.Button == tea.MouseWheelUp {
			k = tea.KeyPressMsg{Code: tea.KeyUp}
		}
		return m, m.handleKey(k)

	case snapshotMsg:
		m.collecting = false
		m.snap, m.lastTook, m.lastAt = msg.snap, msg.took, time.Now()
		m.sess.update(m.snap)
		m.sess.prefs.gen++
		for _, t := range m.tabs {
			t.SetData(m.snap)
		}
		if m.paused {
			return m, nil
		}
		return m, m.scheduleTick()

	case tickMsg:
		if msg.seq != m.tickSeq || m.paused {
			return m, nil
		}
		if m.stressRunning() { // the stress monitor feeds snapshots meanwhile
			return m, m.scheduleTick()
		}
		return m, m.collect()

	case spinner.TickMsg:
		if m.snap != nil && !m.collecting && !m.syncing() && !m.isoRunning() {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case explainMsg:
		m.openExplain(msg.alert)
		return m, nil
	case switchTabMsg:
		for i, t := range m.tabs {
			if t.Module() == msg.module && (i > 0 || msg.module != model.ModSystem) {
				m.active = i
			}
		}
		if msg.subject != "" {
			if f, ok := m.tabs[m.active].(focuser); ok && !f.Focus(msg.subject) {
				m.flash("selected tab " + m.tabs[m.active].Title() + " (no row for " + msg.subject + ")")
			}
		}
		return m, nil

	case statusMsg:
		m.flash(string(msg))
		return m, nil

	case netboxDoneMsg:
		m.netboxDone(msg)
		return m, nil

	case nbChangeMsg, nbLogMsg:
		return m, m.syncMsg(msg)

	case netboxChoicesMsg:
		m.netboxChoices(msg)
		return m, nil

	case webhookTestMsg:
		m.webhookTested(msg)
		return m, nil

	case isoLineMsg, isoBuiltMsg, isoProgMsg, isoFinishMsg, isoDockerMsg:
		return m, m.isoMsg(msg)

	case tea.PasteMsg:
		return m, m.paste(msg.Content)

	case tea.ClipboardMsg:
		return m, m.paste(msg.Content)

	case tea.KeyPressMsg:
		return m, m.handleKey(msg)

	case virtualNICsMsg:
		m.env.Opts.NICAll = msg.show
		m.flash(map[bool]string{true: "showing virtual interfaces", false: "hiding virtual interfaces"}[msg.show])
		return m, m.collect()

	case stressQuitMsg:
		m.cancel()
		return m, tea.Quit
	}
	for _, t := range m.tabs {
		if h, ok := t.(msgHandler); ok {
			if cmd, handled := h.HandleMsg(msg); handled {
				return m, cmd
			}
		}
	}
	return m, nil
}

// msgHandler is implemented by tabs with their own background messages.
type msgHandler interface {
	HandleMsg(msg tea.Msg) (tea.Cmd, bool)
}

type stressQuitMsg struct{}

func (m *Model) stressTab() *stressTab {
	for _, t := range m.tabs {
		if s, ok := t.(*stressTab); ok {
			return s
		}
	}
	return nil
}

func (m *Model) stressRunning() bool {
	s := m.stressTab()
	return s != nil && s.Running()
}

// quit stops a running stress test first and waits (bounded) until its
// workers and external engines are gone, so nothing outlives Tyr.
func (m *Model) quit() tea.Cmd {
	s := m.stressTab()
	if s == nil || s.run == nil {
		m.cancel()
		return tea.Quit
	}
	r := s.run
	r.Abort("Tyr is quitting")
	s.queue = s.queue[:s.runIdx+1]
	m.flash("stopping the stress test …")
	return func() tea.Msg {
		done := make(chan struct{})
		go func() { r.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
		return stressQuitMsg{}
	}
}

// keyClaimer is a tab that uses a global key for itself (Events: s =
// source filter instead of "go to Stress").
type keyClaimer interface {
	Claims(msg tea.KeyPressMsg) bool
}

// capturer is implemented by tabs that take all keys while an input is active.
type capturer interface{ Capturing() bool }

var (
	keyProblems = key.NewBinding(key.WithKeys("!"), key.WithHelp("!", "problems"))
	keyOrder    = key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "order"))
	keyCopy     = key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copy serial"))
	keyReport   = key.NewBinding(key.WithKeys("R", "shift+r"), key.WithHelp("R", "reports"))
	keyAlerts   = key.NewBinding(key.WithKeys("A", "shift+a"), key.WithHelp("A", "alert reference"))
	keyNetBox   = key.NewBinding(key.WithKeys("N", "shift+n"), key.WithHelp("N", "NetBox"))
	keyWebhooks = key.NewBinding(key.WithKeys("W", "shift+w"), key.WithHelp("W", "webhooks"))
	keyISO      = key.NewBinding(key.WithKeys("I", "shift+i"), key.WithHelp("I", "ISO / USB stick"))
	keyExplain  = key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "explain"))
)

// openAlertReference shows every kind of alert Tyr raises: what it means,
// the usual causes and how to check.
func (m *Model) openAlertReference() {
	md := report.AlertReference()
	m.page = markdownPage(m.theme, "A alerts", "Alert reference", md, m.width, m.contentHeight()+3)
	m.page.copyText, m.page.copyKind = md, "alert reference"
}

// openExplain shows one alert: where it happens, what it means, the usual
// causes and the commands to check that component.
func (m *Model) openExplain(a report.Alert) {
	if m.snap == nil {
		return
	}
	md := report.ExplainAlert(m.snap, a)
	m.page = markdownPage(m.theme, "1 Overview", a.Subject+" · "+a.Code, md, m.width, m.contentHeight()+3)
	m.page.copyText, m.page.copyKind = md, "explanation"
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	if msg.String() == "ctrl+c" {
		if v, ok := m.editingValue(); ok { // in a text field: copy, do not quit
			cmd, txt := copyCmd(v, "the field")
			m.flash(txt)
			return cmd
		}
		return m.quit()
	}
	if msg.String() == "ctrl+v" {
		if _, ok := m.editingValue(); ok { // the terminal's clipboard via OSC 52
			return func() tea.Msg { return tea.ReadClipboard() }
		}
	}
	if m.showHelp {
		m.showHelp = false // any key closes the help overlay
		return nil
	}
	if m.reports != nil {
		return m.updateReports(msg)
	}
	if m.nb != nil {
		return m.updateNetBox(msg)
	}
	if m.wh != nil {
		return m.updateWebhooks(msg)
	}
	if m.iso != nil {
		return m.updateISO(msg)
	}
	if m.page != nil {
		if !m.page.capturing() && key.Matches(msg, k.Quit) {
			return m.quit()
		}
		if !m.page.capturing() && key.Matches(msg, keyAlerts) && m.page.title != "Alert reference" {
			m.openAlertReference()
			return nil
		}
		if !m.page.capturing() && m.page.stressable && key.Matches(msg, keyStressThis) {
			if req := m.tabs[m.active].(stressTargeter).StressTarget(); req.why != "" {
				m.flash(req.why) // stay on the page
				return nil
			}
			m.page = nil
			return m.startStress(true)
		}
		if !m.page.capturing() && key.Matches(msg, keyCopy) {
			if m.page.copyText != "" {
				return m.copyReport(m.page.copyText, m.page.copyKind)
			}
			return m.copySerial()
		}
		if m.page.update(msg) {
			m.page = nil
		}
		return nil
	}
	if c, ok := m.tabs[m.active].(capturer); ok && c.Capturing() {
		return m.tabs[m.active].Update(msg)
	}
	if c, ok := m.tabs[m.active].(keyClaimer); ok && c.Claims(msg) {
		return m.tabs[m.active].Update(msg)
	}
	switch {
	case key.Matches(msg, keyProblems):
		m.sess.prefs.problemsOnly = !m.sess.prefs.problemsOnly
		m.flash(map[bool]string{true: "showing problems only (! to show all)", false: "showing all rows"}[m.sess.prefs.problemsOnly])
		m.refreshTabs()
		return nil
	case key.Matches(msg, keyReport):
		if m.snap != nil {
			m.openReports()
		}
		return nil
	case key.Matches(msg, keyAlerts):
		m.openAlertReference()
		return nil
	case key.Matches(msg, keyNetBox):
		m.openNetBox()
		return nil
	case key.Matches(msg, keyWebhooks):
		m.openWebhooks()
		return nil
	case key.Matches(msg, keyISO):
		return m.openISO()
	case key.Matches(msg, keyStressThis):
		return m.startStress(true)
	case key.Matches(msg, keyFullTest):
		return m.startStress(false)
	case key.Matches(msg, keyCopy):
		if _, ok := m.tabs[m.active].(serialer); ok {
			return m.copySerial()
		}
	case key.Matches(msg, keyOrder):
		m.sess.prefs.naturalOrder = !m.sess.prefs.naturalOrder
		m.flash(map[bool]string{true: "device order", false: "problems first"}[m.sess.prefs.naturalOrder])
		m.refreshTabs()
		return nil
	case key.Matches(msg, k.Enter) && m.active != 0:
		if d, ok := m.tabs[m.active].(detailer); ok {
			if title, body := d.Detail(m.theme, m.width); title != "" {
				m.page = newDetailPage(m.theme, title, body, m.width, m.contentHeight()+3)
				m.page.tab = fmt.Sprintf("%d %s", (m.active+1)%10, m.tabs[m.active].Title())
				_, m.page.stressable = m.tabs[m.active].(stressTargeter)
				if pi, ok := m.tabs[m.active].(pageInfoer); ok {
					m.page.info, m.page.hasInfo = pi.PageInfo()
				}
				return nil
			}
		}
		return m.tabs[m.active].Update(msg)
	}
	switch {
	case key.Matches(msg, k.Quit):
		if m.stressRunning() {
			m.flash("a stress test is running: esc on the Stress tab aborts it, ctrl+c stops it and quits")
			return nil
		}
		return m.quit()
	case key.Matches(msg, k.NextTab):
		m.active = (m.active + 1) % len(m.tabs)
	case key.Matches(msg, k.PrevTab):
		m.active = (m.active + len(m.tabs) - 1) % len(m.tabs)
	case key.Matches(msg, k.Tab1to9):
		i := int(msg.String()[0] - '1')
		if msg.String() == "0" {
			i = 9
		}
		if i < len(m.tabs) {
			m.active = i
		}
	case key.Matches(msg, k.Refresh):
		if m.collecting {
			m.flash("collection already running")
			return nil
		}
		m.flash("refreshing…")
		return tea.Batch(m.collectFresh(), m.spin.Tick)
	case key.Matches(msg, k.Pause):
		m.paused = !m.paused
		if m.paused {
			m.flash("live refresh paused (p to resume)")
			return nil
		}
		m.flash("live refresh resumed")
		return m.collect()
	case key.Matches(msg, k.Export):
		m.flash(m.export())
	case key.Matches(msg, k.Help):
		m.showHelp = true
	case key.Matches(msg, k.Stress):
		for i, t := range m.tabs {
			if _, ok := t.(*stressTab); ok {
				m.active = i
			}
		}
	default:
		return m.tabs[m.active].Update(msg)
	}
	return nil
}

// refreshTabs re-applies list preferences after a toggle.
func (m *Model) refreshTabs() {
	if m.snap != nil {
		for _, t := range m.tabs {
			t.SetData(m.snap)
		}
	}
}

// handleClick switches tabs when the tab bar is clicked.
func (m *Model) handleClick(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button != tea.MouseLeft || msg.Y != 1 || m.page != nil {
		return nil
	}
	start := 0
	for i, end := range m.tabXs {
		if msg.X >= start && msg.X < end {
			m.active = i
			return nil
		}
		start = end
	}
	return nil
}

func (m *Model) export() string {
	if m.snap == nil {
		return "nothing to export yet"
	}
	b, err := json.MarshalIndent(m.snap, "", "  ")
	if err != nil {
		return "export failed: " + err.Error()
	}
	name := fmt.Sprintf("tyr-snapshot-%s-%s", m.snap.Hostname, time.Now().UTC().Format("20060102T150405Z"))
	p := filepath.Join(m.opts.ExportDir, name)
	if err := os.WriteFile(p+".json", append(b, '\n'), 0o600); err != nil {
		return "export failed: " + err.Error()
	}
	if err := os.WriteFile(p+".md", []byte(report.Snapshot(m.snap)), 0o600); err != nil {
		return "exported " + p + ".json (report failed: " + err.Error() + ")"
	}
	return "exported " + p + ".json and .md"
}

// content area: header (1) + tabs (1) + rule (1) + footer (1 or more)
func (m *Model) footerHeight() int {
	if m.showAll {
		return 4
	}
	return 1
}

// contentHeight is what a tab's View gets: the screen minus header, tab
// bar, status strip and footer.
func (m *Model) contentHeight() int { return max(1, m.height-4-m.footerHeight()) }

func (m *Model) resize() {
	for _, t := range m.tabs {
		t.SetSize(m.width, m.contentHeight())
	}
	if m.page != nil {
		m.page.resize(m.width, m.contentHeight()+3)
	}
	m.help.SetWidth(m.width)
}

// View renders the whole screen.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.ReportFocus = true
	if m.opts.Mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	v.WindowTitle = "Tyr"
	if m.snap != nil {
		crit, warn := 0, 0
		for _, f := range m.snap.Findings() {
			switch f.Status {
			case model.StatusCrit:
				crit++
			case model.StatusWarn:
				warn++
			}
		}
		v.WindowTitle = fmt.Sprintf("%s %s – Tyr", m.snap.Status, m.snap.Hostname)
		if crit+warn > 0 {
			v.WindowTitle = fmt.Sprintf("%s %s (%d crit, %d warn) – Tyr", m.snap.Status, m.snap.Hostname, crit, warn)
		}
		if m.opts.FixtureName != "" {
			v.WindowTitle += " [fixture " + m.opts.FixtureName + "]"
		}
	}
	if st := m.stressTab(); st != nil {
		if title := st.windowTitle(); title != "" {
			v.WindowTitle = title
		}
	}
	return v
}

func (m *Model) render() string {
	t := m.theme
	if m.width < 40 || m.height < 10 {
		return fit("Tyr: terminal too small (need at least 40x10)", m.width, m.height)
	}
	var b strings.Builder
	b.WriteString(fit(m.header(), m.width, 1) + "\n")
	ch := m.contentHeight()
	var body string
	switch {
	case m.snap == nil:
		b.WriteString(fit(m.tabBar(), m.width, 1) + "\n")
		body = "\n  " + m.spin.View() + " collecting hardware data…"
		ch += 2
	case m.page != nil:
		// detail pages replace the tab bar with their breadcrumb
		body = m.page.view(t, m.width)
		ch += 3
	default:
		b.WriteString(fit(m.tabBar(), m.width, 1) + "\n")
		if o, ok := m.tabs[m.active].(stripOwner); ok && o.OwnsStrip() {
			// the tab draws its own title/banner where the strip sits
			body = m.tabs[m.active].View(t)
			ch += 2
			break
		}
		strip := ""
		if s, ok := m.tabs[m.active].(stripper); ok {
			strip = s.Strip(t, m.width)
		}
		b.WriteString(stripLine(strip, m.width) + "\n\n") // one blank row below the strip
		body = m.tabs[m.active].View(t)
	}
	body = fit(body, m.width, ch)
	if n := strings.Count(body, "\n") + 1; n < ch {
		body += strings.Repeat("\n", ch-n)
	}
	b.WriteString(body + "\n")
	if lost := m.sess.prefs.lost; lost != "" { // set while the body arranged its lists
		m.sess.prefs.lost = ""
		m.flash(lost + " no longer reported")
	}
	b.WriteString(fit(m.footer(), m.width, m.footerHeight()))
	if m.reports != nil {
		return m.reportsView(b.String())
	}
	if m.nb != nil {
		return m.netboxView(b.String())
	}
	if m.wh != nil {
		return m.webhooksView(b.String())
	}
	if m.iso != nil {
		return m.isoView(b.String())
	}
	if m.showHelp {
		return m.helpOverlay(b.String())
	}
	return b.String()
}

// header: " TYR " pill, host (bold), product · S/N · uptime (muted); right:
// fixture/not-root/paused markers, spinner, "updated 2s ago", the problem
// counts and the host status badge. Narrow terminals drop the identity and
// shorten the right side.
func (m *Model) header() string {
	t := m.theme
	left := t.Title.Render(" TYR ")
	var info []string
	host := ""
	status := ""
	crit, warn := 0, 0
	if s := m.snap; s != nil {
		host = t.Header.Render(s.Hostname)
		if sys := s.System; sys != nil {
			if p := productName(sys); p != "" {
				info = append(info, p)
			}
			if sys.Serial != "" {
				info = append(info, "S/N "+sys.Serial)
			}
			if sys.UptimeSeconds != nil {
				info = append(info, "up "+duration(*sys.UptimeSeconds))
			}
		}
		if s.Virtualization != "" && s.Virtualization != "none" {
			info = append(info, "VM "+s.Virtualization)
		}
		status = t.Badge(s.Status)
		for _, f := range s.Findings() {
			switch f.Status {
			case model.StatusCrit:
				crit++
			case model.StatusWarn:
				warn++
			}
		}
	}
	narrow := m.width < 90
	var right []string
	if m.snap != nil && !m.snap.Privileged {
		right = append(right, t.StatusText(model.StatusWarn, "▲ not root"))
	}
	if m.opts.FixtureName != "" {
		right = append(right, t.Message.Render("fixture"))
	}
	switch {
	case m.paused:
		right = append(right, t.StatusBold(model.StatusWarn, "‖ paused"))
	case m.collecting && m.snap != nil:
		right = append(right, lipgloss.NewStyle().Foreground(t.Accent).Render(m.spin.Spinner.Frames[m.spinFrame()]))
	}
	if m.snap != nil {
		age := time.Since(m.lastAt)
		st := t.Sub
		txt := "updated " + ageText(age)
		if narrow {
			txt = strings.TrimSuffix(ageText(age), " ago")
		}
		if !m.paused && !m.lastAt.IsZero() && age > 2*m.opts.Refresh+time.Second {
			st = lipgloss.NewStyle().Foreground(t.Warn).Bold(true) // collection stalled
		}
		if m.lastAt.IsZero() {
			txt = m.snap.Timestamp.Local().Format("15:04:05")
		}
		right = append(right, st.Render(txt))
		if crit+warn > 0 {
			if narrow {
				right = append(right, t.StatusBold(model.StatusCrit, fmt.Sprint(crit))+t.Sub.Render("/")+t.StatusBold(model.StatusWarn, fmt.Sprint(warn)))
			} else {
				right = append(right, t.StatusBold(model.StatusCrit, fmt.Sprint(crit))+t.Dim.Render(" crit ")+t.StatusBold(model.StatusWarn, fmt.Sprint(warn))+t.Dim.Render(" warn"))
			}
		}
	}
	r := strings.Join(right, "  ")
	if status != "" {
		r += "  " + status
	}
	mid := ""
	if host != "" {
		mid = " " + host
		if len(info) > 0 && !narrow {
			mid += "  " + t.HeaderInfo.Render(strings.Join(info, " · "))
		}
	}
	space := m.width - lipgloss.Width(left) - lipgloss.Width(r) - 2
	if lipgloss.Width(mid) > space {
		mid = truncate(mid, max(0, space))
	}
	gap := max(1, m.width-lipgloss.Width(left)-lipgloss.Width(mid)-lipgloss.Width(r))
	return left + mid + strings.Repeat(" ", gap) + r
}

func ageText(d time.Duration) string {
	switch {
	case d < 2*time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm ago", int(d.Minutes()))
}

// spinFrame returns the spinner's current frame index (the spinner model
// keeps it private; its View carries foreign styling we cannot put on the bar).
func (m *Model) spinFrame() int {
	v := stripStyle(m.spin.View())
	for i, f := range m.spin.Spinner.Frames {
		if f == v {
			return i
		}
	}
	return 0
}

var shortTabNames = map[string]string{"Memory": "Mem", "Storage": "Stor", "Network": "Net", "GPU/NPU": "GPU", "Inventory": "Inv"}

// tabBar: inactive tabs as "2 CPU ✖3" (digit subtle, name muted, glyph
// and problem count in the status colour; ○ and a subtle name when the
// module has no data), the active tab as an accent pill. Overflow steps,
// first that fits: full names with 2-space gaps → 1-space gaps → short
// names (Mem, Stor, Net, GPU, Inv) → digit + glyph + count. Counts are
// never dropped; "? help" is right-aligned when it fits.
func (m *Model) tabBar() string {
	t := m.theme
	help := t.Sub.Render("? help")
	build := func(gap int, short, digits, chips bool) string {
		var parts []string
		m.tabXs = m.tabXs[:0]
		x := 0
		sep := strings.Repeat(" ", gap)
		for i, tb := range m.tabs {
			name := tb.Title()
			if short && i != m.active {
				if s, ok := shortTabNames[name]; ok {
					name = s
				}
			}
			st, have, unavailable := model.StatusOK, false, false
			if m.snap != nil {
				switch mod := tb.Module(); {
				case mod == model.ModSystem:
					st, have = m.snap.Status, true
				case mod != "":
					if sec := m.snap.Section(mod); sec != nil {
						st, have = sec.Head().Status, true
					} else {
						unavailable = true
					}
				}
			}
			n := 0
			mark, plain := "", ""
			switch {
			case have:
				n = m.problemCount(i)
				mark, plain = t.Dot(st), statusSym(st)
				if n > 0 {
					mark += t.StatusText(st, fmt.Sprint(n))
					plain += fmt.Sprint(n)
				}
			case unavailable:
				mark = lipgloss.NewStyle().Foreground(t.NA).Render("○")
				plain = "○"
			}
			digit := fmt.Sprint((i + 1) % 10)
			var part string
			switch {
			case i == m.active:
				label := digit + " " + tb.Title()
				if plain != "" {
					label += " " + plain
				}
				part = t.ActiveTab.Render(label)
			case digits:
				part = t.Sub.Render(digit) + mark
			default: // a chip: digit, name and count on the tab background
				ns := lipgloss.NewStyle().Foreground(t.Muted)
				if unavailable {
					ns = t.Sub
				}
				part = t.Sub.Render(digit) + " " + ns.Render(name)
				if mark != "" {
					part += " " + mark
				}
				if chips {
					part = " " + part + " "
					if !t.Basic {
						part = withBg(part, t.TabBg)
					}
				}
			}
			if i > 0 {
				x += gap
			}
			parts = append(parts, part)
			x += lipgloss.Width(part)
			m.tabXs = append(m.tabXs, x)
		}
		return strings.Join(parts, sep)
	}
	tiers := []struct {
		gap                  int
		short, digits, chips bool
	}{
		{1, false, false, true},  // chips, full names
		{1, false, false, false}, // plain, full names
		{1, true, false, false},  // plain, short names (Mem, Stor, Net …)
		{1, true, true, false},   // digit + glyph + count
	}
	var bar string
	for _, tr := range tiers {
		bar = build(tr.gap, tr.short, tr.digits, tr.chips)
		if w := lipgloss.Width(bar); w+lipgloss.Width(help)+2 <= m.width {
			return bar + strings.Repeat(" ", m.width-w-lipgloss.Width(help)) + help
		} else if w <= m.width && tr.digits {
			return bar
		}
	}
	return bar
}

// problemCount is the number of WARN/CRIT findings of tab i's module.
func (m *Model) problemCount(i int) int {
	mod := m.tabs[i].Module()
	if m.snap == nil || mod == "" {
		return 0
	}
	if mod == model.ModSystem {
		n := 0
		for _, f := range m.snap.Findings() {
			if f.Status >= model.StatusWarn {
				n++
			}
		}
		return n
	}
	if sec := m.snap.Section(mod); sec != nil {
		n := 0
		for _, f := range sec.Head().Findings {
			if f.Status >= model.StatusWarn {
				n++
			}
		}
		return n
	}
	return 0
}

// footer: key hints (key bold, description muted, 2 spaces apart;
// context keys first, "? help" last) and a right-aligned toast.
func (m *Model) footer() string {
	t := m.theme
	toastStr := ""
	switch {
	case m.message != "" && time.Since(m.messageAt) < 4*time.Second:
		toastStr = m.messageLine()
	case m.collecting && m.snap != nil:
		toastStr = lipgloss.NewStyle().Foreground(t.Accent).Render(m.spin.Spinner.Frames[m.spinFrame()]) + " " + t.Bold.Render("refreshing…")
	case m.snap != nil && !m.paused && !m.lastAt.IsZero() && time.Since(m.lastAt) > 2*m.opts.Refresh+time.Second:
		toastStr = toast(t, model.StatusWarn, fmt.Sprintf("collection stalled %d s", int(time.Since(m.lastAt).Seconds())))
	}
	var hints [][2]string
	add := func(bs ...key.Binding) {
		for _, b := range bs {
			if h := b.Help(); h.Key != "" {
				hints = append(hints, [2]string{h.Key, h.Desc})
			}
		}
	}
	if m.page != nil {
		hints = [][2]string{{"↑↓ pgup pgdn", "scroll"}, {"g G", "top/end"}, {"/", "search"}, {"n N", "next/prev match"}, {"esc", "back"}, {"q", "quit"}}
		if m.page.copyText != "" {
			hints = append(hints[:3], append([][2]string{{"c", "copy report"}}, hints[3:]...)...)
		} else if _, ok := m.tabs[m.active].(serialer); ok {
			hints = append(hints[:3], append([][2]string{{"c", "copy serial"}}, hints[3:]...)...)
		}
		if m.page.stressable {
			if req := m.tabs[m.active].(stressTargeter).StressTarget(); req.why == "" {
				what := "stress this"
				if req.engine == stress.EngOfflineCore {
					what = "test this core"
				}
				hints = append(hints[:3], append([][2]string{{"S", what}}, hints[3:]...)...)
			}
		}
		if st := m.page.searchStatus(t); st != "" && (m.message == "" || time.Since(m.messageAt) >= 4*time.Second) {
			toastStr = st
		}
		return footerHints(t, hints, toastStr, m.width)
	}
	add(m.tabs[m.active].Help()...)
	if st, ok := m.tabs[m.active].(*stressTab); ok {
		if st.Running() && toastStr == "" {
			toastStr = t.Sub.Render("q blocked while running")
		}
		if st.state != stForm {
			return footerHints(t, hints, toastStr, m.width)
		}
	}
	if _, ok := m.tabs[m.active].(stressTargeter); ok {
		add(keyStressThis)
	}
	if m.active != 0 {
		add(keyProblems)
	}
	add(m.keys.Quit)
	return footerHints(t, hints, toastStr, m.width)
}

// retheme rebuilds the theme for the current background and profile.
func (m *Model) retheme() {
	m.theme = NewThemeFor(m.dark, m.profile)
	m.theme.sess = m.sess
	m.help.Styles = helpStyles(m.theme)
}

// helpStyles: keys bold fg, descriptions muted (footer key hints).
func helpStyles(t *Theme) help.Styles {
	st := help.DefaultStyles(t.Dark)
	st.ShortKey, st.FullKey = t.Key, t.Key
	st.ShortDesc, st.FullDesc = t.Dim, t.Dim
	st.ShortSeparator, st.FullSeparator = t.Sub, t.Sub
	st.Ellipsis = t.Sub
	return st
}

// productName joins vendor and product without repeating the vendor
// ("HP" + "HP ZBook ..." -> "HP ZBook ...").
func productName(s *model.System) string {
	v, p := strings.TrimSpace(s.Vendor), strings.TrimSpace(s.Product)
	if v != "" && strings.HasPrefix(strings.ToLower(p), strings.ToLower(v)) {
		return p
	}
	return strings.TrimSpace(v + " " + p)
}
