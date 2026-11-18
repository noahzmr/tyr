package tui

import (
	"fmt"
	"strings"

	"github.com/noahzmr/tyr/internal/model"
)

// encShort is the list cell for an encryption state: the method's first
// word (LUKS2, Opal, ZFS, RAID) when it is protected.
func encShort(t *Theme, e *model.Encryption) string {
	switch {
	case e == nil:
		return t.Sub.Render("–")
	case e.Protected:
		m := strings.Fields(e.Method)
		if len(m) == 0 {
			return t.StatusText(model.StatusOK, "yes")
		}
		w := m[0]
		if w == "RAID" {
			w = "ctrl"
		}
		return t.StatusText(model.StatusOK, w)
	case e.Method != "":
		return t.StatusText(model.StatusWarn, "part")
	}
	return t.Dim.Render("none")
}

// encText is the detail row: what encrypts it, through which layer.
func encText(t *Theme, e *model.Encryption) string {
	if e == nil {
		return t.Sub.Render("not known")
	}
	var s string
	switch {
	case e.Protected:
		s = t.StatusText(model.StatusOK, "● encrypted")
		if e.Method != "" {
			s += " · " + e.Method
		}
	case e.Method != "":
		s = t.StatusText(model.StatusWarn, "▲ "+e.Method)
	default:
		s = t.Dim.Render("not encrypted")
	}
	if e.Via != "" {
		s += t.Dim.Render(" · " + e.Via)
	}
	if e.Note != "" {
		s += t.Sub.Render(" · " + e.Note)
	}
	return s
}

// sedText: a drive's TCG and ATA security state.
func sedText(t *Theme, s *model.SED) string {
	if s == nil {
		return ""
	}
	var parts []string
	if s.SSC != "" {
		parts = append(parts, s.SSC)
		switch {
		case s.LockingEnabled && s.Locked:
			parts = append(parts, t.StatusText(model.StatusWarn, "locked"))
		case s.LockingEnabled:
			parts = append(parts, "locking enabled, unlocked")
		case s.LockingSupported:
			parts = append(parts, "locking not set up")
		}
		if s.MediaEncryption {
			parts = append(parts, "media encryption")
		}
		if s.MBREnabled {
			parts = append(parts, "shadow MBR")
		}
	}
	if s.ATASecurity != "" {
		parts = append(parts, "ATA security "+s.ATASecurity)
	}
	return strings.Join(parts, " · ")
}

// fsHealthRows: what the filesystem recorded about its own errors.
func fsHealthRows(t *Theme, h *model.FSHealth) [][2]string {
	if h == nil {
		return nil
	}
	var rows [][2]string
	if h.Errors != nil {
		v := fmt.Sprint(*h.Errors)
		if *h.Errors > 0 {
			v = t.StatusBold(model.StatusWarn, v) + t.Dim.Render(" - check it: K")
		}
		rows = append(rows, [2]string{"FS errors", v + t.Dim.Render(" ("+h.Source+")")})
	}
	if h.FirstError != "" {
		rows = append(rows, [2]string{"First error", h.FirstError})
	}
	if h.LastError != "" {
		rows = append(rows, [2]string{"Last error", h.LastError})
	}
	if h.LastChecked != "" {
		rows = append(rows, [2]string{"Last checked", h.LastChecked})
	}
	if h.State != "" {
		rows = append(rows, [2]string{"State", h.State})
	}
	return rows
}

// cryptSection lists the dm-crypt mappings and the swap areas on the
// filesystem page.
func cryptSection(doc *doc, st *model.Storage) {
	t := doc.t
	if len(st.Crypt) > 0 {
		doc.section("Encrypted volumes", t.Dim.Render("dm-crypt · cryptsetup status <name>"))
		for _, c := range st.Crypt {
			what := c.Type
			if c.Cipher != "" {
				what += " " + c.Cipher
			}
			if c.KeyBits > 0 {
				what += fmt.Sprintf(" %d-bit", c.KeyBits)
			}
			used := strings.Join(c.UsedBy, ", ")
			doc.add(fmt.Sprintf(" %s %s %s %s", t.Dot(model.StatusOK), pad(c.Name, 22), pad(what, 30),
				t.Dim.Render(c.DM+" on "+strings.Join(c.Backing, ", ")+wrapNonEmpty(used, " · "))))
		}
	}
	if len(st.Swap) > 0 {
		doc.section("Swap", "")
		for _, s := range st.Swap {
			doc.add(fmt.Sprintf(" %s %s %s", pad(s.Path, 24), pad(s.Type+" "+bytesSI(s.SizeBytes), 18), encText(t, s.Encryption)))
		}
	}
}

func wrapNonEmpty(s, pre string) string {
	if s == "" {
		return ""
	}
	return pre + s
}
