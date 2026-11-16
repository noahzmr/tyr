package report

import (
	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
)

// Style picks the glamour style for a terminal.
type Style int

const (
	StyleDark  Style = iota
	StyleLight       // light background
	StylePlain       // no colour (NO_COLOR, not a terminal): glyphs and layout only
)

// Render renders Markdown for a terminal of the given width, like glow.
func Render(markdown string, width int, style Style) (string, error) {
	name := styles.DarkStyle
	switch style {
	case StyleLight:
		name = styles.LightStyle
	case StylePlain:
		name = styles.NoTTYStyle
	}
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(name), glamour.WithWordWrap(max(40, width-2)), glamour.WithTableWrap(true))
	if err != nil {
		return "", err
	}
	return r.Render(markdown)
}
