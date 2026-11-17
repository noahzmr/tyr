package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noahzmr/tyr/internal/netbox"
)

func typeText(m *Model, s string) {
	for _, r := range s {
		m.Update(press(string(r)))
	}
}

func TestNetBoxDialog(t *testing.T) {
	const w, h = 120, 50
	m := newLoaded(t, "server-healthy", w, h)
	dir := t.TempDir()
	m.opts.NetBoxFile, m.opts.TagsFile = filepath.Join(dir, "netbox.json"), filepath.Join(dir, "tags.json")
	t.Setenv("TYR_NETBOX_TOKEN", "")
	t.Setenv("TYR_NETBOX_URL", "")

	m.Update(press("N"))
	if m.nb == nil {
		t.Fatal("N did not open the dialog")
	}
	s := screen(m)
	checkFits(t, "netbox connection", s, w, h)
	for _, want := range []string{"NetBox", "Connection", "Tag rules", "URL", "Token", "Write to NetBox"} {
		if !strings.Contains(s, want) {
			t.Errorf("connection page without %q", want)
		}
	}

	// URL and token
	m.Update(press("enter"))
	typeText(m, "https://nb.example.com/api/")
	m.Update(press("enter"))
	m.Update(press("down"))
	m.Update(press("enter"))
	typeText(m, "0123456789abcdef")
	m.Update(press("enter"))
	if m.nb.cfg.URL != "https://nb.example.com" || m.nb.cfg.Token != "0123456789abcdef" {
		t.Fatalf("cfg %+v", m.nb.cfg)
	}
	if s := screen(m); strings.Contains(s, "0123456789abcdef") {
		t.Error("token shown in clear text")
	}

	// a tag rule, live match against this machine
	m.Update(press("tab"))
	if !strings.Contains(screen(m), "No rules") {
		t.Error("empty tags page does not say so")
	}
	m.Update(press("a"))
	typeText(m, "hw-memory")
	m.Update(press("enter"))
	typeText(m, "memory_gb >= 1")
	if !strings.Contains(screen(m), "matches this machine now") {
		t.Error("no live match while editing")
	}
	m.Update(press("enter"))
	m.Update(press("a"))
	typeText(m, "broken")
	m.Update(press("enter"))
	typeText(m, "cores >")
	m.Update(press("enter"))
	if m.nb.err == "" || len(m.nb.tags.Rules) != 1 {
		t.Errorf("broken rule accepted: %+v %q", m.nb.tags.Rules, m.nb.err)
	}
	m.Update(press("esc"))
	s = screen(m)
	checkFits(t, "netbox tags", s, w, h)
	if !strings.Contains(s, "✔ set") || !strings.Contains(s, "hw-memory") {
		t.Errorf("tags page:\n%s", s)
	}
	m.Update(press("f"))
	if !strings.Contains(screen(m), "memory_gb") {
		t.Error("facts not shown")
	}

	// esc with unsaved changes asks once
	m.Update(press("esc"))
	if m.nb == nil || !m.nb.confirm {
		t.Fatal("closed with unsaved changes")
	}
	m.Update(press("s"))
	if m.nb.dirty || m.nb.err != "" {
		t.Fatalf("save: %q", m.nb.err)
	}
	cfg, err := netbox.LoadConfig(m.opts.NetBoxFile)
	if err != nil || cfg.URL != "https://nb.example.com" || cfg.Token != "0123456789abcdef" {
		t.Errorf("saved config %+v %v", cfg, err)
	}
	if fi, err := os.Stat(m.opts.NetBoxFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("netbox.json mode %v %v", fi.Mode(), err)
	}
	tags, err := netbox.LoadTags(m.opts.TagsFile)
	if err != nil || len(tags.Rules) != 1 || tags.Rules[0].When != "memory_gb >= 1" {
		t.Errorf("saved tags %+v %v", tags, err)
	}
	m.Update(press("esc"))
	if m.nb != nil {
		t.Fatal("esc did not close after saving")
	}

	// a finished sync opens a page with the log
	m.Update(press("N"))
	m.Update(netboxDoneMsg{log: []string{"device srv-01 (id 7)", "journal entry written (success)"},
		rep: &netbox.Report{Device: "srv-01", DeviceURL: "https://nb.example.com/dcim/devices/7/", Tags: []string{"hw-memory"}}})
	if m.nb != nil || m.page == nil {
		t.Fatal("no result page")
	}
	s = screen(m)
	checkFits(t, "netbox result", s, w, h)
	if !strings.Contains(s, "journal entry written") || !strings.Contains(s, "hw-memory") {
		t.Errorf("result page:\n%s", s)
	}
	m.page = nil
	m.Update(press("N"))
	m.Update(netboxDoneMsg{err: errors.New("NetBox https://nb.example.com: HTTP 403")})
	if !strings.Contains(screen(m), "Not written") {
		t.Error("error not shown")
	}
}
