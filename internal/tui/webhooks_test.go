package tui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/noahzmr/tyr/internal/notify"
)

func TestWebhookDialog(t *testing.T) {
	const w, h = 120, 50
	for _, k := range []string{"URL", "FORMAT", "EVENTS", "TOKEN", "SECRET", "INSECURE"} {
		t.Setenv("TYR_WEBHOOK_"+k, "")
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Tyr-Event") == "test" {
			hits.Add(1)
		}
	}))
	defer srv.Close()
	m := newLoaded(t, "server-healthy", w, h)
	m.opts.WebhooksFile = filepath.Join(t.TempDir(), "webhooks.json")

	m.Update(press("W"))
	if m.wh == nil || !strings.Contains(screen(m), "No webhooks") {
		t.Fatal("W did not open the empty dialog")
	}
	m.Update(press("a"))
	m.Update(press("down")) // URL
	m.Update(tea.PasteMsg{Content: srv.URL + "/hook\n"})
	m.Update(press("enter"))
	m.Update(press("down"))  // format
	m.Update(press("right")) // json -> slack
	for whFields[m.wh.field] != "pass" {
		m.Update(press("down"))
	}
	m.Update(press(" ")) // no message on PASS
	s := screen(m)
	checkFits(t, "webhook form", s, w, h)
	if !strings.Contains(s, "step_failed") || !strings.Contains(s, "a step fails (at once") {
		t.Errorf("form:\n%s", s)
	}
	for whFields[m.wh.field] != "done" {
		m.Update(press("down"))
	}
	m.Update(press("enter"))
	if m.wh.edit != nil || len(m.wh.cfg.Webhooks) != 1 {
		t.Fatalf("done: %+v err %q", m.wh.cfg.Webhooks, m.wh.err)
	}
	hook := m.wh.cfg.Webhooks[0]
	if hook.URL != srv.URL+"/hook" || hook.Format != "slack" || hook.Wants(notify.EvPass) || !hook.Wants(notify.EvFail) {
		t.Errorf("webhook %+v", hook)
	}
	// test notification
	_, cmd := m.Update(press("t"))
	runCmd(m, cmd)
	if hits.Load() != 1 || !strings.Contains(m.wh.msg, "delivered") {
		t.Errorf("test: hits %d msg %q err %q", hits.Load(), m.wh.msg, m.wh.err)
	}
	m.Update(press("s"))
	c, err := notify.Load(m.opts.WebhooksFile)
	if err != nil || len(c.Webhooks) != 1 || c.Webhooks[0].Format != "slack" {
		t.Errorf("saved %+v %v", c, err)
	}
	m.Update(press("esc"))
	if m.wh != nil {
		t.Fatal("esc after saving did not close")
	}
	// a webhook from the environment is shown, but not edited
	t.Setenv("TYR_WEBHOOK_URL", "https://hooks.example.com/env")
	m.Update(press("W"))
	if len(m.wh.cfg.Webhooks) != 2 || !strings.Contains(screen(m), "(environment)") {
		t.Fatalf("env webhook: %+v", m.wh.cfg.Webhooks)
	}
	m.Update(press("down"))
	m.Update(press("enter"))
	if m.wh.edit != nil || !strings.Contains(m.wh.msg, "environment") {
		t.Error("env webhook opened for editing")
	}
}
