package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/tone"
	"github.com/charmbracelet/colorprofile"

	"github.com/noahzmr/tyr/internal/model"
)

// role is a colour token of the design (design canvas "Tyr TUI
// redesign", board Tokens). Code asks for roles, never for raw colours.
type role int

const (
	rOK role = iota
	rWarn
	rCrit
	rUnknown
	rNA // unavailable / not applicable
	rFg
	rMuted
	rSubtle
	rBorder
	rTrack
	rAccent   // accent foreground: focus rules, ▌ bar, focused borders
	rAccentBg // active tab pill, primary button
	rOnAccent // text on the accent background
	rInk      // badge text on a status background
	rSel      // selection background
	rBar      // bar background
	rNew      // NEW badge background
	rUnkBg    // UNKNOWN badge background
	rMatch    // search match background
	rZebra    // every other table row
	rTabBg    // an inactive tab's chip
	rS1       // graph series
	rS2
	rS3
	rS4
	numRoles
)

// token is one role's colour per theme and its 16-colour ANSI fallback
// (the user's terminal palette renders those, no automatic downsampling).
type token struct {
	dark, light         color.Color
	ansiDark, ansiLight color.Color // nil = terminal default colour
}

func hex(s string) color.Color { return lipgloss.Color(s) }

// tokens is the role table. Values are tone v0.2.0 constants; explicit
// hexes only where tone has no colour with enough contrast on white
// (light ok/warn), or where the canvas derives one (light crit = Sriracha
// −25 % lightness, light graph.4 = Cumin −35 %).
var tokens = [numRoles]token{
	rOK:       {tone.Guac, hex("#1A7F37"), lipgloss.Green, lipgloss.Green},
	rWarn:     {tone.Mustard, hex("#9A6700"), lipgloss.Yellow, lipgloss.Yellow},
	rCrit:     {tone.Bengal, hex("#CC163F"), lipgloss.Red, lipgloss.Red},
	rUnknown:  {tone.Smoke, tone.Oyster, lipgloss.White, lipgloss.BrightBlack},
	rNA:       {tone.Squid, tone.Squid, lipgloss.BrightBlack, lipgloss.BrightBlack},
	rFg:       {tone.Salt, tone.Pepper, nil, nil},
	rMuted:    {tone.Smoke, tone.Oyster, lipgloss.White, lipgloss.BrightBlack},
	rSubtle:   {tone.Squid, tone.Squid, lipgloss.BrightBlack, lipgloss.BrightBlack},
	rBorder:   {tone.Iron, tone.Smoke, lipgloss.BrightBlack, lipgloss.BrightBlack},
	rTrack:    {tone.Iron, tone.Smoke, lipgloss.BrightBlack, lipgloss.BrightBlack},
	rAccent:   {tone.Hazy, tone.Charple, lipgloss.Magenta, lipgloss.Magenta},
	rAccentBg: {tone.Charple, tone.Charple, lipgloss.Magenta, lipgloss.Magenta},
	rOnAccent: {tone.Butter, tone.Butter, lipgloss.BrightWhite, lipgloss.BrightWhite},
	rInk:      {tone.Pepper, tone.Butter, lipgloss.Black, lipgloss.BrightWhite},
	rSel:      {tone.Char, tone.Sash, nil, nil}, // 16 colours: reverse instead
	rBar:      {tone.Char, tone.Sash, nil, nil},
	rZebra:    {tone.Pepper, tone.Salt, nil, nil}, // 16 colours: no stripes
	rTabBg:    {tone.BBQ, tone.Sash, nil, nil},
	rNew:      {tone.Malibu, tone.Damson, lipgloss.Cyan, lipgloss.Cyan},
	rUnkBg:    {tone.Iron, tone.Oyster, lipgloss.BrightBlack, lipgloss.BrightBlack},
	rMatch:    {tone.Mustard, tone.Mustard, lipgloss.Yellow, lipgloss.Yellow},
	rS1:       {tone.Malibu, tone.Damson, lipgloss.Cyan, lipgloss.Cyan},
	rS2:       {tone.Mauve, tone.Grape, lipgloss.Magenta, lipgloss.Magenta},
	rS3:       {tone.Cheeky, tone.Macaron, lipgloss.BrightMagenta, lipgloss.BrightMagenta},
	rS4:       {tone.Cumin, hex("#88623C"), lipgloss.Yellow, lipgloss.Yellow},
}

// Theme holds resolved colours and styles for one background and colour
// profile. Bubble Tea's BackgroundColorMsg picks dark/light; its
// ColorProfileMsg picks truecolor/256 (tone hexes, downsampled by Lip Gloss
// for 256), 16 colours (named ANSI colours from the table) or none
// (NO_COLOR/ASCII: glyphs, words, bold, reverse and border weights carry
// the meaning).
type Theme struct {
	Dark bool
	// Basic is true for 16 colours or no colour: selection and badges use
	// reverse video instead of background colours.
	Basic   bool
	NoColor bool

	OK, Warn, Crit, Unknown, NA       color.Color
	Fg, Muted, Subtle, Border, Track  color.Color
	Accent, AccentBg, OnAccent, Ink   color.Color
	SelBg, BarBg, NewBg, UnkBg, Match color.Color
	ZebraBg, TabBg                    color.Color
	Series                            [4]color.Color

	Title, Header, HeaderInfo, Dim, Sub, Bold, Footer, Message lipgloss.Style
	Tab, ActiveTab, TabGap                                     lipgloss.Style
	Card, CardTitle, Section, Selected, Error                  lipgloss.Style
	TableHead, Rule, NewBadge, Key                             lipgloss.Style

	// sess gives renderers access to session state (NEW findings, counter
	// growth, sensor history). It may be nil (tests, first frame).
	sess *session
}

// NewTheme builds the truecolor theme for a dark or light background.
func NewTheme(dark bool) *Theme { return NewThemeFor(dark, colorprofile.TrueColor) }

// NewThemeFor builds the theme for a background and colour profile.
func NewThemeFor(dark bool, p colorprofile.Profile) *Theme {
	t := &Theme{Dark: dark, Basic: p <= colorprofile.ANSI, NoColor: p <= colorprofile.ASCII}
	c := func(r role) color.Color {
		tk := tokens[r]
		var v color.Color
		switch {
		case t.Basic && dark:
			v = tk.ansiDark
		case t.Basic:
			v = tk.ansiLight
		case dark:
			v = tk.dark
		default:
			v = tk.light
		}
		if v == nil {
			return lipgloss.NoColor{}
		}
		return v
	}
	t.OK, t.Warn, t.Crit, t.Unknown, t.NA = c(rOK), c(rWarn), c(rCrit), c(rUnknown), c(rNA)
	t.Fg, t.Muted, t.Subtle, t.Border, t.Track = c(rFg), c(rMuted), c(rSubtle), c(rBorder), c(rTrack)
	t.Accent, t.AccentBg, t.OnAccent, t.Ink = c(rAccent), c(rAccentBg), c(rOnAccent), c(rInk)
	t.SelBg, t.BarBg, t.NewBg, t.UnkBg, t.Match = c(rSel), c(rBar), c(rNew), c(rUnkBg), c(rMatch)
	t.ZebraBg, t.TabBg = c(rZebra), c(rTabBg)
	t.Series = [4]color.Color{c(rS1), c(rS2), c(rS3), c(rS4)}

	t.Title = lipgloss.NewStyle().Bold(true).Foreground(t.OnAccent).Background(t.AccentBg)
	t.Header = lipgloss.NewStyle().Bold(true).Foreground(t.Fg)
	t.HeaderInfo = lipgloss.NewStyle().Foreground(t.Muted)
	t.Dim = lipgloss.NewStyle().Foreground(t.Muted)
	t.Sub = lipgloss.NewStyle().Foreground(t.Subtle)
	t.Bold = lipgloss.NewStyle().Bold(true).Foreground(t.Fg)
	t.Footer = lipgloss.NewStyle().Foreground(t.Muted)
	t.Message = lipgloss.NewStyle().Foreground(t.Accent)
	t.Key = lipgloss.NewStyle().Bold(true).Foreground(t.Fg)
	t.Tab = lipgloss.NewStyle().Foreground(t.Muted).Background(t.TabBg).Padding(0, 1)
	t.ActiveTab = lipgloss.NewStyle().Bold(true).Foreground(t.OnAccent).Background(t.AccentBg).Padding(0, 1)
	t.TabGap = lipgloss.NewStyle().Foreground(t.Border)
	t.Card = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.Border).Padding(0, 1)
	t.CardTitle = lipgloss.NewStyle().Bold(true).Foreground(t.Fg)
	t.Section = lipgloss.NewStyle().Bold(true).Foreground(t.Accent)
	t.Selected = lipgloss.NewStyle().Bold(true).Background(t.SelBg)
	t.Error = lipgloss.NewStyle().Foreground(t.Crit)
	t.TableHead = lipgloss.NewStyle().Bold(true).Foreground(t.Muted).Underline(true)
	t.Rule = lipgloss.NewStyle().Foreground(t.Subtle)
	t.NewBadge = lipgloss.NewStyle().Bold(true).Foreground(t.Ink).Background(t.NewBg)
	if t.Basic {
		// 16 colours / no colour: reverse video for pills and badges.
		t.ActiveTab = lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 1)
		t.Tab = lipgloss.NewStyle().Padding(0, 1)
		t.Title = lipgloss.NewStyle().Bold(true).Reverse(true)
		t.Selected = lipgloss.NewStyle().Bold(true)
		t.NewBadge = lipgloss.NewStyle().Bold(true).Reverse(true)
	}
	return t
}

// OnBar renders s in style st on the bar background.
func (t *Theme) OnBar(st lipgloss.Style, s string) string {
	return st.Background(t.BarBg).Render(s)
}

// Heading renders "Title  note ───────── right" across w: the title bold,
// the rule subtle. Use HeadingFocus for the focused region.
func (t *Theme) Heading(title, note string, w int) string {
	return t.heading(t.Bold, t.Rule, title, note, "", w)
}

// HeadingRight is Heading with a hint at the right end of the rule.
func (t *Theme) HeadingRight(title, note, right string, w int) string {
	return t.heading(t.Bold, t.Rule, title, note, right, w)
}

// HeadingFocus is the accent heading of the focused/selected region.
func (t *Theme) HeadingFocus(title, note, right string, w int) string {
	acc := lipgloss.NewStyle().Foreground(t.Accent)
	return t.heading(t.Section, acc, title, note, right, w)
}

func (t *Theme) heading(ts, rs lipgloss.Style, title, note, right string, w int) string {
	h := ts.Render(title)
	if note != "" {
		h += " " + note
	}
	r := ""
	if right != "" {
		r = " " + t.Sub.Render(right)
	}
	if rest := w - lipgloss.Width(h) - lipgloss.Width(r) - 1; rest > 0 {
		h += " " + rs.Render(strings.Repeat("─", rest))
	}
	return truncate(h+r, w)
}

// Counts renders "7 ✖ · 14 ▲" for the non-OK findings (empty if none).
func (t *Theme) Counts(fs []model.Finding) string {
	var crit, warn, unk int
	for _, f := range fs {
		switch f.Status {
		case model.StatusCrit:
			crit++
		case model.StatusWarn:
			warn++
		case model.StatusUnknown:
			unk++
		}
	}
	var parts []string
	for _, c := range []struct {
		s model.Status
		n int
	}{{model.StatusCrit, crit}, {model.StatusWarn, warn}, {model.StatusUnknown, unk}} {
		if c.n > 0 {
			parts = append(parts, t.StatusText(c.s, fmt.Sprint(c.n))+" "+t.Dot(c.s))
		}
	}
	return strings.Join(parts, t.Sub.Render(" · "))
}

// StatusColor returns the colour for a status.
func (t *Theme) StatusColor(s model.Status) color.Color {
	switch s {
	case model.StatusOK:
		return t.OK
	case model.StatusWarn:
		return t.Warn
	case model.StatusCrit:
		return t.Crit
	}
	return t.Unknown
}

// Badge renders a status as a reverse block with glyph and word,
// e.g. " ✖ CRIT ": status colour background, ink text.
func (t *Theme) Badge(s model.Status) string {
	label := " " + statusSym(s) + " " + s.String() + " "
	if t.NoColor {
		return lipgloss.NewStyle().Bold(true).Reverse(true).Render(label)
	}
	bg, fg := t.StatusColor(s), t.Ink
	if s == model.StatusUnknown {
		bg, fg = t.UnkBg, t.Fg
	}
	if !t.Dark && !t.Basic && (s == model.StatusOK || s == model.StatusWarn) {
		fg = tone.Butter
	}
	return lipgloss.NewStyle().Bold(true).Foreground(fg).Background(bg).Render(label)
}

// StatusText renders text in the status colour.
func (t *Theme) StatusText(s model.Status, text string) string {
	return lipgloss.NewStyle().Foreground(t.StatusColor(s)).Render(text)
}

// StatusBold renders text bold in the status colour.
func (t *Theme) StatusBold(s model.Status, text string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(t.StatusColor(s)).Render(text)
}

// Dot renders the status glyph (● ▲ ✖ ?), bold in the status colour.
func (t *Theme) Dot(s model.Status) string {
	return lipgloss.NewStyle().Bold(true).Foreground(t.StatusColor(s)).Render(statusSym(s))
}

// Unavail renders "○ name  reason" for an unavailable source: muted,
// never red.
func (t *Theme) Unavail(name, reason string) string {
	if name == "" {
		return lipgloss.NewStyle().Foreground(t.NA).Render("○") + " " + t.Sub.Render(reason)
	}
	return lipgloss.NewStyle().Foreground(t.NA).Render("○") + " " + t.Dim.Render(name) + " " + t.Sub.Render(reason)
}

// NotApplicable renders "· name  reason".
func (t *Theme) NotApplicable(name, reason string) string {
	return lipgloss.NewStyle().Foreground(t.NA).Render("·") + " " + t.Dim.Render(name) + " " + t.Sub.Render(reason)
}

func statusSym(s model.Status) string {
	return map[model.Status]string{model.StatusOK: "●", model.StatusWarn: "▲", model.StatusCrit: "✖", model.StatusUnknown: "?"}[s]
}

// NewTag is the " NEW " badge.
func (t *Theme) NewTag() string { return t.NewBadge.Render(" NEW ") }
