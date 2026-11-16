package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/report"
)

// reportStyle maps the theme to a glamour style: dark, light, or plain
// for 16-colour-less terminals (NO_COLOR keeps glyphs and layout).
func reportStyle(t *Theme) report.Style {
	switch {
	case t.NoColor:
		return report.StylePlain
	case !t.Dark:
		return report.StyleLight
	}
	return report.StyleDark
}

// markdownPage renders Markdown into a detail page (scroll, / search,
// breadcrumb).
func markdownPage(t *Theme, tab, title, markdown string, w, h int) *detailPage {
	body, err := report.Render(markdown, w, reportStyle(t))
	if err != nil {
		body = markdown
	}
	p := newDetailPage(t, title, body, w, h)
	p.tab = tab
	return p
}

// docView is `tyr view`: one Markdown document full screen, like glow.
type docView struct {
	title, markdown string
	copyText        string // what c copies (Mermaid Markdown); "" = markdown
	message         string
	w, h            int
	dark            bool
	profile         colorprofile.Profile
	theme           *Theme
	page            *detailPage
}

// ViewDocument shows a Markdown document in a pager: ↑↓ pgup pgdn g G
// scroll, / search, n N next/previous match, c copies copyText (or the
// document) to the clipboard, q or esc quits.
func ViewDocument(title, markdown, copyText string) error {
	_, err := tea.NewProgram(&docView{title: title, markdown: markdown, copyText: copyText, dark: true, profile: colorprofile.TrueColor}).Run()
	return err
}

func (d *docView) Init() tea.Cmd { return tea.RequestBackgroundColor }

func (d *docView) rebuild() {
	if d.w == 0 {
		return
	}
	d.theme = NewThemeFor(d.dark, d.profile)
	off := 0
	if d.page != nil {
		off = d.page.vp.YOffset()
	}
	d.page = markdownPage(d.theme, "tyr view", d.title, d.markdown, d.w, d.h-1)
	d.page.vp.SetYOffset(off)
}

func (d *docView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.w, d.h = msg.Width, msg.Height
		d.rebuild()
	case tea.BackgroundColorMsg:
		d.dark = msg.IsDark()
		d.rebuild()
	case tea.ColorProfileMsg:
		d.profile = msg.Profile
		d.rebuild()
	case tea.KeyPressMsg:
		if d.page == nil {
			return d, nil
		}
		if !d.page.capturing() && (msg.String() == "q" || msg.String() == "ctrl+c") {
			return d, tea.Quit
		}
		if msg.String() == "ctrl+c" {
			return d, tea.Quit
		}
		if !d.page.capturing() && msg.String() == "c" {
			text := d.copyText
			if text == "" {
				text = d.markdown
			}
			cmd, msg := copyCmd(text, "the document ("+sizeText(len(text))+")")
			d.message = msg
			return d, cmd
		}
		d.message = ""
		if d.page.update(msg) {
			return d, tea.Quit
		}
	case statusMsg: // result of the clipboard tool
		d.message = string(msg)
	case tea.MouseWheelMsg:
		if d.page != nil {
			d.page.vp, _ = d.page.vp.Update(msg)
		}
	}
	return d, nil
}

func (d *docView) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.WindowTitle = d.title + " – Tyr"
	if d.page == nil {
		return v
	}
	t := d.theme
	var parts []string
	for _, h := range [][2]string{{"q esc", "quit"}, {"c", "copy"}, {"/", "search"}, {"n N", "next/prev match"}, {"↑↓ pgup pgdn", "scroll"}, {"g G", "top/end"}} {
		parts = append(parts, t.Key.Render(h[0])+" "+t.Dim.Render(h[1]))
	}
	foot := strings.Join(parts, "  ")
	st := d.page.searchStatus(t)
	if d.message != "" {
		st = toast(t, model.StatusOK, d.message)
	}
	if st != "" {
		foot = truncate(foot, max(10, d.w-lipgloss.Width(st)-2))
		foot += strings.Repeat(" ", max(2, d.w-lipgloss.Width(foot)-lipgloss.Width(st))) + st
	}
	v.SetContent(fit(d.page.view(t, d.w), d.w, d.h-1) + "\n" + fit(foot, d.w, 1))
	return v
}
