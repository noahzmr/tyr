package tui

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

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
	if m.nb.picker == nil || m.nb.picker.field != "tag" {
		t.Fatal("a with URL and token: no tag list")
	}
	m.Update(press("esc")) // type the tag instead
	typeText(m, "hw-memory")
	m.Update(press("enter"))
	typeText(m, "memory_gb >= 1")
	if !strings.Contains(screen(m), "matches this machine now") {
		t.Error("no live match while editing")
	}
	m.Update(press("enter"))
	m.Update(press("a"))
	m.Update(press("esc"))
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

// fakeChoices answers the picker's list requests like NetBox.
func fakeChoices(t *testing.T) *httptest.Server {
	lists := map[string]string{
		"/api/dcim/sites/":           `[{"name":"Lab","slug":"lab"},{"name":"DC Frankfurt","slug":"dc-fra"},{"name":"DC Berlin","slug":"dc-ber"}]`,
		"/api/dcim/device-roles/":    `[{"name":"Server","slug":"server"}]`,
		"/api/tenancy/tenants/":      `[{"name":"Customer A","slug":"a"}]`,
		"/api/extras/tags/":          `[{"name":"burnin-ok","slug":"burnin-ok"},{"name":"defect","slug":"defect"}]`,
		"/api/extras/custom-fields/": `[{"name":"memory","object_types":["dcim.device"],"type":{"label":"Text"}}]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token tok" {
			http.Error(w, "{}", http.StatusForbidden)
			return
		}
		l, ok := lists[r.URL.Path]
		if !ok {
			http.Error(w, "{}", 404)
			return
		}
		fmt.Fprintf(w, `{"count":1,"next":null,"results":%s}`, l)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runCmd executes a command and feeds its message back, like the program.
func runCmd(m *Model, cmd tea.Cmd) {
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m.Update(msg)
		}
	}
}

func TestNetBoxPasteAndPickers(t *testing.T) {
	const w, h = 120, 50
	m := newLoaded(t, "server-healthy", w, h)
	dir := t.TempDir()
	m.opts.NetBoxFile, m.opts.TagsFile = filepath.Join(dir, "netbox.json"), filepath.Join(dir, "tags.json")
	t.Setenv("TYR_NETBOX_TOKEN", "")
	t.Setenv("TYR_NETBOX_URL", "")
	srv := fakeChoices(t)
	m.Update(press("N"))

	// paste onto the URL field (not editing): it becomes the value
	m.Update(tea.PasteMsg{Content: srv.URL + "/api/\n"})
	m.Update(press("enter"))
	m.Update(press("down"))
	m.Update(tea.PasteMsg{Content: "tok"})
	m.Update(press("enter"))
	if m.nb.cfg.URL != srv.URL || m.nb.cfg.Token != "tok" {
		t.Fatalf("pasted cfg %+v", m.nb.cfg)
	}
	// paste while editing appends; ctrl+c copies instead of quitting
	m.Update(press("up"))
	m.Update(press("enter"))
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if m.nb == nil || !m.nb.editing {
		t.Fatal("ctrl+c in a field closed the dialog")
	}
	m.Update(press("esc"))

	// site: chosen from NetBox, filtered as you type
	for nbFields[m.nb.field].id != "site" {
		m.Update(press("down"))
	}
	_, cmd := m.Update(press("enter"))
	if m.nb.picker == nil || !m.nb.picker.loading {
		t.Fatal("site picker did not open")
	}
	runCmd(m, cmd)
	typeText(m, "dc ")
	s := screen(m)
	checkFits(t, "site picker", s, w, h)
	if !strings.Contains(s, "DC Berlin") || !strings.Contains(s, "DC Frankfurt") || strings.Contains(s, "  Lab ") {
		t.Errorf("filter dc:\n%s", s)
	}
	typeText(m, "fr")
	m.Update(press("enter"))
	if m.nb.cfg.Site != "DC Frankfurt" || m.nb.picker != nil {
		t.Fatalf("site %q", m.nb.cfg.Site)
	}
	// a new site: typed, offered as new
	_, cmd = m.Update(press("enter"))
	runCmd(m, cmd)
	typeText(m, "Lab 2")
	if !strings.Contains(screen(m), "+ Lab 2") {
		t.Error("new site not offered")
	}
	m.Update(press("enter"))
	if m.nb.cfg.Site != "Lab 2" {
		t.Errorf("new site %q", m.nb.cfg.Site)
	}
	// tab: type it yourself
	_, cmd = m.Update(press("enter"))
	runCmd(m, cmd)
	m.Update(press("tab"))
	if !m.nb.editing || m.nb.input.Value() != "Lab 2" {
		t.Error("tab does not switch to typing")
	}
	m.Update(press("esc"))

	// categories: multi-select, all = empty
	for nbFields[m.nb.field].id != "categories" {
		m.Update(press("down"))
	}
	m.Update(press("enter"))
	m.Update(press(" ")) // board off
	m.Update(press("enter"))
	if len(m.nb.cfg.Categories) != len(netbox.DefaultCategories)-1 || slices.Contains(m.nb.cfg.Categories, "board") {
		t.Errorf("categories %v", m.nb.cfg.Categories)
	}

	// a new tag rule starts with NetBox's tags
	m.Update(press("tab"))
	_, cmd = m.Update(press("a"))
	runCmd(m, cmd)
	if m.nb.picker == nil || m.nb.picker.field != "tag" {
		t.Fatal("tag picker did not open on a")
	}
	typeText(m, "burn")
	m.Update(press("enter"))
	if m.nb.ruleTag.Value() != "burnin-ok" || m.nb.editPart != 1 {
		t.Errorf("tag %q part %d", m.nb.ruleTag.Value(), m.nb.editPart)
	}
	m.Update(tea.PasteMsg{Content: `verdict == "PASS"`})
	m.Update(press("enter"))
	if len(m.nb.tags.Rules) != 1 || m.nb.tags.Rules[0].When != `verdict == "PASS"` {
		t.Errorf("rules %+v", m.nb.tags.Rules)
	}

	// without NetBox the fields are typed
	m.Update(press("tab"))
	m.nb.cfg.Token = ""
	m.Update(press("up"))
	for nbFields[m.nb.field].id != "role" {
		m.Update(press("up"))
	}
	m.Update(press("enter"))
	if m.nb.picker != nil || !m.nb.editing {
		t.Error("picker without a token")
	}
}

func TestNetBoxSyncLive(t *testing.T) {
	const w, h = 120, 50
	m := newLoaded(t, "server-healthy", w, h)
	dir := t.TempDir()
	m.opts.NetBoxFile, m.opts.TagsFile = filepath.Join(dir, "netbox.json"), filepath.Join(dir, "tags.json")
	m.Update(press("N"))
	cancelled := false
	st := &nbSync{dry: true, url: "https://nb.example.com", start: time.Now(), ch: make(chan tea.Msg, 8),
		cancel: func() { cancelled = true }, counts: map[string]int{}, device: "srv-healthy-01"}
	m.nb.sync = st
	feed := []tea.Msg{
		nbChangeMsg{c: netbox.Change{Object: "device", Name: "srv-healthy-01", Action: "create", Detail: "Supermicro SYS-120U-TNR"}, done: 1, total: 40},
		nbChangeMsg{c: netbox.Change{Object: "interface", Name: "eno1", Action: "create", Detail: "1000base-t, MAC 3C:EC:EF:10:00:01"}, done: 2, total: 40},
		nbChangeMsg{c: netbox.Change{Object: "inventory item", Name: "CPU1", Action: "unchanged"}, done: 3, total: 40},
		nbChangeMsg{c: netbox.Change{Object: "inventory item", Name: "Memory P1-DIMMA1", Action: "update", Detail: `serial "3A5F0000"`}, done: 4, total: 40},
		nbLogMsg("  ✖ inventory item GPU0: HTTP 400 {\"name\":[\"too long\"]}"),
	}
	for _, msg := range feed {
		m.Update(msg)
	}
	s := screen(m)
	checkFits(t, "sync live", s, w, h)
	for _, want := range []string{"Dry run against https://nb.example.com", "10%", "4 of ~40", "2 would create", "1 would change",
		"1 unchanged", "1 problems", "+ interface eno1", "~ inventory item Memory P1-DIMMA1", "✖ inventory item GPU0", "esc cancels"} {
		if !strings.Contains(s, want) {
			t.Errorf("live view without %q:\n%s", want, s)
		}
	}
	m.Update(press("esc"))
	if !cancelled || m.nb.sync == nil {
		t.Error("esc while running must cancel, not close")
	}
	m.Update(netboxDoneMsg{dry: true, rep: &netbox.Report{Device: "srv-healthy-01", Changes: []netbox.Change{{Object: "device", Name: "srv-healthy-01", Action: "create"}}}})
	s = screen(m)
	if !strings.Contains(s, "✔ would create 1") || !strings.Contains(s, "100%") || !strings.Contains(s, "enter: all objects") {
		t.Errorf("finished view:\n%s", s)
	}
	m.Update(press("enter"))
	if m.page == nil || m.nb != nil || !strings.Contains(screen(m), "Plan") {
		t.Error("enter does not open the tables")
	}
}
