package tui

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"charm.land/tone"
	"github.com/charmbracelet/colorprofile"

	"github.com/noahzmr/tyr/internal/model"
)

func sameColor(a, b color.Color) bool {
	r1, g1, b1, a1 := a.RGBA()
	r2, g2, b2, a2 := b.RGBA()
	return r1 == r2 && g1 == g2 && b1 == b2 && a1 == a2
}

func TestThemeRoles(t *testing.T) {
	dark, light := NewThemeFor(true, colorprofile.TrueColor), NewThemeFor(false, colorprofile.TrueColor)
	for _, c := range []struct {
		name      string
		got, want color.Color
	}{
		{"dark ok", dark.OK, tone.Guac}, {"dark warn", dark.Warn, tone.Mustard}, {"dark crit", dark.Crit, tone.Bengal},
		{"dark fg", dark.Fg, tone.Salt}, {"dark accent", dark.Accent, tone.Hazy}, {"dark sel", dark.SelBg, tone.Char},
		{"light fg", light.Fg, tone.Pepper}, {"light sel", light.SelBg, tone.Sash}, {"light accent", light.Accent, tone.Charple},
		{"light ok", light.OK, lipgloss.Color("#1A7F37")}, {"light crit", light.Crit, lipgloss.Color("#CC163F")},
		{"dark series 3", dark.Series[2], tone.Cheeky}, {"light series 4", light.Series[3], lipgloss.Color("#88623C")},
	} {
		if !sameColor(c.got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
	// 16 colours: named ANSI colours from the token table, terminal default fg.
	b := NewThemeFor(true, colorprofile.ANSI)
	if b.Crit != lipgloss.Red || b.OK != lipgloss.Green || b.Accent != lipgloss.Magenta || b.NewBg != lipgloss.Cyan || !b.Basic {
		t.Errorf("16-colour mapping: crit %v ok %v accent %v new %v", b.Crit, b.OK, b.Accent, b.NewBg)
	}
	if _, ok := b.Fg.(lipgloss.NoColor); !ok {
		t.Errorf("16-colour fg should be the terminal default, got %v", b.Fg)
	}
	if bl := NewThemeFor(false, colorprofile.ANSI); bl.Muted != lipgloss.BrightBlack || bl.Ink != lipgloss.BrightWhite {
		t.Errorf("16-colour light: muted %v ink %v", bl.Muted, bl.Ink)
	}
	// No colour: the badge keeps glyph and word and becomes reverse video.
	nc := NewThemeFor(true, colorprofile.ASCII)
	badge := nc.Badge(model.StatusCrit)
	if !strings.Contains(badge, "✖ CRIT") || !strings.Contains(badge, "7m") {
		t.Errorf("no-colour badge %q", badge)
	}
}
