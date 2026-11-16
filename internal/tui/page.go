package tui

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/explain"
	"github.com/noahzmr/tyr/internal/model"
)

// detailer is implemented by tabs that can show a full page for the
// selected component.
type detailer interface {
	Detail(t *Theme, w int) (title string, body string)
}

// focuser is implemented by tabs that can select the component a finding
// subject refers to.
type focuser interface {
	Focus(subject string) bool
}

// pageInfo names the component a page shows, for its breadcrumb:
// "esc ◂ 4 Storage › Disks › sdd  ✖ CRIT   Seagate ST4000NM0035 · S/N …".
type pageInfo struct {
	path   []string // view, component
	status model.Status
	facts  []string
}

// pageInfoer is implemented by tabs that describe their page's component.
type pageInfoer interface {
	PageInfo() (pageInfo, bool)
}

// detailPage is a full-screen, scrollable, searchable page. A breadcrumb
// replaces the tab bar; the query and its match count sit in the footer.
type detailPage struct {
	title   string
	body    string
	tab     string // "4 Storage"
	info    pageInfo
	hasInfo bool
	vp      viewport.Model
	search  lineInput
	query   string
	matches int
	cur     int   // current match (0-based)
	lines   []int // line of each match
	// copyText is what "c" copies on a report page (Markdown with Mermaid
	// charts, or JSON); empty on component pages, where c copies the serial.
	copyText, copyKind string
}

func newDetailPage(t *Theme, title, body string, w, h int) *detailPage {
	p := &detailPage{title: title, body: body, search: lineInput{placeholder: "search this page", limit: 64}}
	p.vp = viewport.New(viewport.WithWidth(w), viewport.WithHeight(max(1, h-2)))
	p.vp.SoftWrap = false
	p.vp.HighlightStyle = lipgloss.NewStyle().Background(t.Match).Foreground(t.Ink)
	p.vp.SelectedHighlightStyle = lipgloss.NewStyle().Background(t.Accent).Foreground(t.OnAccent).Bold(true)
	if t.NoColor {
		p.vp.HighlightStyle = lipgloss.NewStyle().Underline(true)
		p.vp.SelectedHighlightStyle = lipgloss.NewStyle().Reverse(true)
	}
	p.vp.SetContent(body)
	return p
}

func (p *detailPage) resize(w, h int) {
	p.vp.SetWidth(w)
	p.vp.SetHeight(max(1, h-2))
}

// capturing reports whether the search input takes all keys.
func (p *detailPage) capturing() bool { return p.search.Focused() }

// update handles a key; it returns true when the page should close.
func (p *detailPage) update(msg tea.KeyPressMsg) bool {
	if p.search.Focused() {
		switch msg.String() {
		case "esc":
			p.search.SetValue("")
			p.search.Blur()
			p.applySearch()
		case "enter":
			p.search.Blur()
		default:
			p.search.Update(msg)
			p.applySearch()
		}
		return false
	}
	switch msg.String() {
	case "esc", "backspace", "left", "h":
		if p.query != "" && msg.String() == "esc" {
			p.search.SetValue("")
			p.applySearch()
			return false
		}
		return true
	case "/":
		p.search.Focus()
	case "n":
		if p.matches > 0 {
			top := p.vp.YOffset()
			p.vp.HighlightNext()
			p.vp.SetYOffset(top)
			p.cur = (p.cur + 1) % p.matches
			p.showMatch()
		}
	case "N":
		if p.matches > 0 {
			top := p.vp.YOffset()
			p.vp.HighlightPrevious()
			p.vp.SetYOffset(top)
			p.cur = (p.cur - 1 + p.matches) % p.matches
			p.showMatch()
		}
	case "g", "home":
		p.vp.GotoTop()
	case "G", "end":
		p.vp.GotoBottom()
	case " ", "space":
		p.vp.PageDown()
	default:
		p.vp, _ = p.vp.Update(msg)
	}
	return false
}

var ansiSeq = regexp.MustCompile("\x1b\\[[0-9;:?]*[A-Za-z]")

// applySearch highlights case-insensitive matches outside escape sequences.
func (p *detailPage) applySearch() {
	p.query = p.search.Value()
	p.vp.ClearHighlights()
	p.matches, p.cur, p.lines = 0, 0, nil
	if p.query == "" {
		return
	}
	content := p.vp.GetContent()
	esc := ansiSeq.FindAllStringIndex(content, -1)
	re := regexp.MustCompile("(?i)" + regexp.QuoteMeta(p.query))
	var keep [][]int
	for _, m := range re.FindAllStringIndex(content, -1) {
		inside := false
		for _, e := range esc {
			if m[0] < e[1] && m[1] > e[0] {
				inside = true
				break
			}
		}
		if !inside {
			keep = append(keep, m)
			p.lines = append(p.lines, strings.Count(content[:m[0]], "\n"))
		}
	}
	p.matches = len(keep)
	if len(keep) > 0 {
		// the viewport selects the first match at or below the top line
		top := p.vp.YOffset()
		for i, l := range p.lines {
			if l >= top {
				p.cur = i
				break
			}
		}
		p.vp.SetHighlights(keep)
		p.vp.SetYOffset(top) // undo the viewport's own scroll (see showMatch)
		p.showMatch()
	}
}

// showMatch scrolls the current match into view. The viewport maps match
// offsets to lines itself, which drifts on styled content; p.lines is exact.
func (p *detailPage) showMatch() {
	if p.cur >= len(p.lines) {
		return
	}
	l, top, h := p.lines[p.cur], p.vp.YOffset(), max(1, p.vp.Height())
	if l < top || l >= top+h {
		p.vp.SetYOffset(max(0, l-h/3))
	}
}

// searchStatus is the footer text: "/realloc  match 1 of 2".
func (p *detailPage) searchStatus(t *Theme) string {
	if p.search.Focused() {
		return t.Dim.Render("/") + t.Bold.Render(p.search.Value()) + lipgloss.NewStyle().Foreground(t.Accent).Render("█")
	}
	if p.query == "" {
		return ""
	}
	q := t.Dim.Render("/") + t.Bold.Render(p.query) + "  "
	if p.matches == 0 {
		return q + t.StatusText(model.StatusWarn, "no match")
	}
	return q + t.Dim.Render(fmt.Sprintf("match %d of %d", p.cur+1, p.matches))
}

// crumb is the breadcrumb row: esc ◂ tab › view › component, the badge,
// the key facts, and the scroll position on the right.
func (p *detailPage) crumb(t *Theme, w int) string {
	left := t.Key.Render("esc") + t.Sub.Render(" ◂ ") + t.Dim.Render(p.tab)
	path := []string{p.title}
	if p.hasInfo && len(p.info.path) > 0 {
		path = p.info.path
	}
	for i, seg := range path {
		st := t.Dim
		if i == len(path)-1 {
			st = t.Bold
		}
		left += t.Sub.Render(" › ") + st.Render(seg)
	}
	if p.hasInfo {
		left += "  " + t.Badge(p.info.status)
		if len(p.info.facts) > 0 {
			left += "   " + t.Dim.Render(strings.Join(p.info.facts, " · "))
		}
	}
	total := max(1, p.vp.TotalLineCount())
	top := p.vp.YOffset()
	right := t.Sub.Render(fmt.Sprintf("line %d–%d · %.0f %%", top+1, min(total, top+p.vp.Height()), p.vp.ScrollPercent()*100))
	room := w - lipgloss.Width(right) - 2
	if lipgloss.Width(left) > room {
		left = truncate(left, max(10, room))
	}
	return left + strings.Repeat(" ", max(2, w-lipgloss.Width(left)-lipgloss.Width(right))) + right
}

func (p *detailPage) view(t *Theme, w int) string {
	return truncate(p.crumb(t, w), w) + "\n\n" + p.vp.View()
}

// --- document builder ---------------------------------------------------------

// doc builds the body of a detail page: titled sections, label/value
// rows, gauges against thresholds, sparklines, tables, findings and
// counters with their growth during the session.
type doc struct {
	t     *Theme
	w     int
	lines []string
}

func newDoc(t *Theme, w int) *doc { return &doc{t: t, w: w} }

func (d *doc) String() string { return strings.Join(d.lines, "\n") }

func (d *doc) add(s ...string) {
	for _, x := range s {
		d.lines = append(d.lines, strings.Split(x, "\n")...)
	}
}

func (d *doc) section(title, note string) {
	if len(d.lines) > 0 {
		d.add("")
	}
	d.add(d.t.Heading(title, note, d.w))
}

// kv adds label/value rows; empty values are skipped.
func (d *doc) kv(rows ...[2]string) {
	var keep [][2]string
	for _, r := range rows {
		if v := strings.TrimSpace(stripStyle(r[1])); v != "" && v != "–" {
			keep = append(keep, r)
		}
	}
	if len(keep) > 0 {
		d.add(kvWrap(d.t, keep, d.w))
	}
}

// sensor adds one reading with a threshold gauge and its session history.
func (d *doc) sensor(s model.Sensor, hist []float64) {
	if s.Value == nil {
		d.add(d.t.Dot(model.StatusUnknown) + " " + pad(s.Name, 24) + d.t.Dim.Render("no reading"))
		return
	}
	val := fmtSensor(s)
	line := d.t.Dot(s.Status) + " " + pad(s.Name, 24) + d.t.StatusText(s.Status, pad(val, 10))
	if g := sensorGauge(d.t, s, 24); g != "" {
		line += " " + g
	}
	if th := thresholdText(s); th != "" {
		line += d.t.Dim.Render("  " + th)
	}
	if len(hist) > 1 && d.w >= 100 {
		line += "  " + d.t.Message.Render(sparkline(hist, 20))
	}
	if s.Shared {
		line += d.t.Dim.Render("  (shared)")
	}
	d.add(line)
}

func fmtSensor(s model.Sensor) string {
	v := *s.Value
	switch s.Unit {
	case model.UnitCelsius:
		return check.FmtTemp(v)
	case model.UnitRPM:
		return fmt.Sprintf("%.0f rpm", v)
	case "percent":
		return fmt.Sprintf("%.0f%%", v)
	case "":
		return fmt.Sprintf("%.2f", v)
	}
	if v == float64(int64(v)) {
		return fmt.Sprintf("%.0f %s", v, s.Unit)
	}
	return fmt.Sprintf("%.2f %s", v, s.Unit)
}

func thresholdText(s model.Sensor) string {
	th := s.Thresholds
	if th == nil {
		return ""
	}
	var p []string
	f := func(name string, v *float64) {
		if v != nil {
			p = append(p, fmt.Sprintf("%s %.4g", name, *v))
		}
	}
	f("lcrit", th.LowerCrit)
	f("lwarn", th.LowerWarn)
	f("warn", th.UpperWarn)
	f("crit", th.UpperCrit)
	if len(p) == 0 {
		return ""
	}
	o := ""
	if th.Origin != "" {
		o = " (" + th.Origin + ")"
	}
	return strings.Join(p, " · ") + o
}

// sensorGauge draws the value on a scale that spans the thresholds; cells
// take the status color of the value they represent and ┊ marks the warn
// and crit points.
func sensorGauge(t *Theme, s model.Sensor, w int) string {
	th := s.Thresholds
	if th == nil || s.Value == nil || (th.UpperWarn == nil && th.UpperCrit == nil && th.LowerCrit == nil && th.LowerWarn == nil) {
		return ""
	}
	lo, hi := *s.Value, *s.Value
	for _, v := range []*float64{th.LowerCrit, th.LowerWarn, th.UpperWarn, th.UpperCrit} {
		if v != nil {
			lo, hi = min(lo, *v), max(hi, *v)
		}
	}
	if s.Unit == model.UnitCelsius && th.LowerCrit == nil && th.LowerWarn == nil {
		lo = min(lo, 20)
	}
	span := hi - lo
	if span <= 0 {
		return ""
	}
	lo -= span * 0.1
	hi += span * 0.1
	at := func(i int) float64 { return lo + (float64(i)+0.5)/float64(w)*(hi-lo) }
	mark := map[int]bool{}
	for _, v := range []*float64{th.LowerCrit, th.LowerWarn, th.UpperWarn, th.UpperCrit} {
		if v != nil {
			mark[int((*v-lo)/(hi-lo)*float64(w))] = true
		}
	}
	pos := int((*s.Value - lo) / (hi - lo) * float64(w))
	var b strings.Builder
	for i := 0; i < w; i++ {
		v := at(i)
		st := check.Eval(&v, th)
		c := t.StatusColor(st)
		ch := "─"
		switch {
		case i == pos:
			ch = "●"
		case mark[i]:
			ch = "┊"
		}
		style := lipgloss.NewStyle().Foreground(c)
		if i != pos {
			style = style.Faint(true)
		}
		b.WriteString(style.Render(ch))
	}
	return b.String()
}

// heatColor maps a 0..1 position onto the OK→WARN→CRIT gradient.
func heatColor(t *Theme, x float64) color.Color {
	const steps = 21
	ramp := lipgloss.Blend1D(steps, t.OK, t.Warn, t.Crit)
	i := int(min(1, max(0, x)) * float64(steps-1))
	return ramp[i]
}

// findings adds related findings (most severe first) with NEW markers,
// then what each kind of alert means and how to check it (the alert
// reference, filled in for this component).
func (d *doc) findings(mod model.ModuleID, fs []model.Finding) {
	defer d.about(mod, fs)
	d.section("Findings", d.t.Counts(fs))
	n := 0
	for _, f := range sortFindings(fs) {
		if f.Status == model.StatusOK {
			continue
		}
		d.add(findingLine(d.t, f, "", d.w))
		n++
	}
	if n == 0 {
		d.add(d.t.StatusText(model.StatusOK, "● no problems found"))
	}
}

// about adds "About these alerts": per kind of alert its meaning, usual
// causes and the commands to check this component.
func (d *doc) about(mod model.ModuleID, fs []model.Finding) {
	t := d.t
	seen := map[string]bool{}
	var out []string
	for _, f := range sortFindings(fs) {
		if f.Status == model.StatusOK || seen[f.Code] {
			continue
		}
		seen[f.Code] = true
		e, ok := explain.For(mod, f.Code)
		if !ok {
			continue
		}
		para := func(label, text string) {
			out = append(out, hang("   "+t.Dim.Render(pad(label, 8)), text, d.w-1))
		}
		out = append(out, "", " "+t.Dot(f.Status)+" "+t.Bold.Render(e.Title)+t.Dim.Render("  "+string(mod)+"/"+f.Code))
		para("Means", e.Meaning)
		if len(e.Causes) > 0 {
			para("Causes", strings.Join(e.Causes, "; "))
		}
		for i, c := range e.Commands(f.Subject) {
			label := ""
			if i == 0 {
				label = "Check"
			}
			out = append(out, hang("   "+t.Dim.Render(pad(label, 8)), c, d.w-1, t.Key.Render))
		}
	}
	if len(out) == 0 {
		return
	}
	d.section("About these alerts", t.Dim.Render("A all alerts"))
	d.add(out[1:]...)
}

// counterNames are readable names for session counter keys.
var counterNames = map[string]string{"io_errors": "SCSI cmd errors (excl. Tyr)", "kernel_errors": "kernel log errors",
	"smart.crc": "CRC errors", "smart.offline_uncorrectable": "offline uncorrectable", "smart.pending": "pending sectors",
	"smart.reallocated": "reallocated sectors", "smart.reported_uncorrectable": "reported uncorrectable",
	"smart.media_errors": "media errors", "smart.error_log": "error log entries", "smart.grown_defects": "grown defects",
	"smart.scsi_uncorrected": "uncorrected errors"}

// counters adds the error/event counters of a component with growth.
func (d *doc) counters(cs []counterChange, prefix string) {
	if len(cs) == 0 {
		return
	}
	d.section("Counters", d.t.Dim.Render("since boot · growth this session"))
	for _, c := range cs {
		name := strings.TrimPrefix(c.Key, prefix)
		if n, ok := counterNames[name]; ok {
			name = n
		}
		line := "   " + pad(name, 30) + padCell(grouped(c.Value), 10, true) + "  "
		switch {
		case c.Last > 0:
			line += d.t.StatusText(model.StatusWarn, fmt.Sprintf("+%d this session", c.SinceStart)) + "  " +
				d.t.StatusBold(model.StatusWarn, fmt.Sprintf("↑ +%d last refresh", c.Last))
		case c.SinceStart > 0:
			line += d.t.StatusText(model.StatusWarn, fmt.Sprintf("+%d this session", c.SinceStart))
		default:
			line += d.t.Sub.Render("unchanged")
		}
		d.add(line)
	}
}

// table adds a static table (lipgloss/table) with a per-row status color.
func (d *doc) table(headers []string, rows [][]string, status []model.Status) {
	if len(rows) == 0 {
		return
	}
	tb := table.New().Headers(headers...).Rows(rows...).
		Border(lipgloss.NormalBorder()).BorderStyle(d.t.Rule).
		BorderColumn(false).BorderLeft(false).BorderRight(false).BorderTop(false).BorderBottom(false).BorderHeader(false).
		StyleFunc(func(r, c int) lipgloss.Style {
			st := lipgloss.NewStyle().PaddingRight(2)
			if r == table.HeaderRow {
				return st.Bold(true).Foreground(d.t.Muted)
			}
			if r >= 0 && r < len(status) && status[r] >= model.StatusWarn {
				return st.Foreground(d.t.StatusColor(status[r]))
			}
			return st
		})
	d.add(tb.Render())
}
