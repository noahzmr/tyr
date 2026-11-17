package system

import (
	"strings"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/model"
)

// the text form older systemd prints (the server's own output)
func TestParseBootsText(t *testing.T) {
	out := ` -2 70524487425e45d899aa69df20136c39 Fri 2026-11-13 13:26:45 CET—Fri 2026-11-13 13:28:56 CET
 -1 80e751c45685472aa1a0948f262075a6 Fri 2026-11-13 13:51:28 CET Fri 2026-11-13 13:52:29 CET
  0 21f266c726324f9c81077d645941c297 Fri 2026-11-13 14:00:23 CET Fri 2026-11-13 18:32:16 CET
`
	bs := parseBoots([]byte(out))
	if len(bs) != 3 || bs[0].ID != "70524487425e45d899aa69df20136c39" || bs[1].End.Sub(bs[1].Start) != 61*time.Second {
		t.Fatalf("boots %+v", bs)
	}
}

func TestBootFindings(t *testing.T) {
	now := time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC)
	yes, no := true, false
	b := func(start, end time.Time, clean *bool) model.Boot {
		return model.Boot{ID: "x", Start: start, End: end, Clean: clean}
	}
	d := func(s string) time.Time { v, _ := time.Parse("2006-01-02 15:04", s); return v }
	out := &model.System{Boots: []model.Boot{
		b(d("2026-11-11 08:05"), d("2026-11-11 08:45"), &yes),
		b(d("2026-11-11 08:50"), d("2026-11-13 11:22"), &no),
		b(d("2026-11-13 12:26"), d("2026-11-13 12:28"), &no),
		b(d("2026-11-13 12:51"), d("2026-11-13 12:52"), &no),
		{ID: "cur", Start: d("2026-11-13 13:00"), End: now, Current: true},
	}}
	var h model.Header
	bootFindings(&h, out, now)
	if len(h.Findings) != 1 || h.Findings[0].Status != model.StatusCrit || !strings.HasPrefix(h.Findings[0].Message, "3 of 4 boots in the last 30 days") ||
		!strings.Contains(h.Findings[0].Message, "after 1m") {
		t.Errorf("findings %+v", h.Findings)
	}
}

func TestPSUHealth(t *testing.T) {
	for text, want := range map[string]model.Status{"Present, OK": model.StatusOK, "Present, Critical": model.StatusCrit,
		"Present, Non-critical": model.StatusWarn} {
		if got := (model.PSUFRU{Present: true, StatusText: text}).Health(); got != want {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
	if l := (model.PSUFRU{Name: "Power Supply 2"}).Label(); l != "Power Supply 2" {
		t.Errorf("label %q", l)
	}
}
