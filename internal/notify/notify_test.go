package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "webhooks.json")
	for _, k := range []string{"URL", "FORMAT", "EVENTS", "TOKEN", "SECRET", "INSECURE"} {
		t.Setenv("TYR_WEBHOOK_"+k, "")
	}
	c, err := Load(p)
	if err != nil || len(c.Webhooks) != 0 {
		t.Fatalf("missing file: %+v %v", c, err)
	}
	c.Webhooks = []Webhook{{Name: "ops", URL: "https://hooks.example.com/x", Format: "slack", Events: []string{EvFail, EvStepFailed}, Token: "t"}}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	t.Setenv("TYR_WEBHOOK_URL", "https://n8n.example.com/webhook/tyr")
	t.Setenv("TYR_WEBHOOK_FORMAT", "teams")
	t.Setenv("TYR_WEBHOOK_EVENTS", "started, fail")
	c, err = Load(p)
	if err != nil || len(c.Webhooks) != 2 || c.Webhooks[1].Format != "teams" || !c.Webhooks[1].FromEnv || len(c.Webhooks[1].Events) != 2 {
		t.Fatalf("with env: %+v %v", c, err)
	}
	if c.Webhooks[0].Wants(EvPass) || !c.Webhooks[0].Wants(EvFail) || !c.Webhooks[0].Wants(EvTest) {
		t.Error("event filter")
	}
	// saving does not copy the environment's webhook into the file
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYR_WEBHOOK_URL", "")
	if c, _ = Load(p); len(c.Webhooks) != 1 {
		t.Errorf("env webhook saved: %+v", c.Webhooks)
	}
	for _, bad := range []string{`{"webhooks":[{"url":"ftp://x"}]}`, `{"webhooks":[{"url":"https://x","format":"irc"}]}`,
		`{"webhooks":[{"url":"https://x","events":["finished"]}]}`, `{"webhooks":[{"url":"https://x","tokn":"t"}]}`} {
		os.WriteFile(p, []byte(bad), 0o600)
		if _, err := Load(p); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func event() Event {
	e := New(EvFail, "acceptance", Machine{Host: "srv-01", Vendor: "HPE", Product: "ProLiant DL360 Gen10", Serial: "CZ123"})
	e.Status, e.Profile, e.Duration = "FAIL", "burnin", "4h02m"
	e.Failures = []string{"DIMM PROC 1 DIMM 3: uncorrectable ECC error", "sdc: 8 pending sectors"}
	e.ResultsDir = "/mnt/results/tyr/CZ123-20261117T090000Z"
	return e
}

func TestBodies(t *testing.T) {
	e := event()
	for _, f := range append([]string{""}, Formats...) {
		b, err := e.Body(f)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: not JSON: %s", f, b)
		}
		s := string(b)
		if !strings.Contains(s, "srv-01") || !strings.Contains(s, "uncorrectable ECC") {
			t.Errorf("%s: %s", f, s)
		}
		switch f {
		case "", "json":
			if v["event"] != "fail" || v["serial"] != "CZ123" || v["run"] != "acceptance" {
				t.Errorf("json %s", s)
			}
		case "slack", "gchat":
			if !strings.HasPrefix(v["text"].(string), "❌ *Tyr acceptance test FAIL on srv-01 (S/N CZ123)*") {
				t.Errorf("%s %s", f, v["text"])
			}
		case "discord":
			if !strings.Contains(v["content"].(string), "**Failures**") {
				t.Errorf("discord %s", s)
			}
		case "teams":
			if v["type"] != "message" || !strings.Contains(s, "AdaptiveCard") || !strings.Contains(s, `"color":"Attention"`) {
				t.Errorf("teams %s", s)
			}
		}
	}
	// Discord's 2000 character limit
	e.Failures = []string{strings.Repeat("x", 3000)}
	b, _ := e.Body("discord")
	var v map[string]any
	json.Unmarshal(b, &v)
	if n := len([]rune(v["content"].(string))); n > 2000 {
		t.Errorf("discord content %d characters", n)
	}
	for verdict, ev := range map[string]string{"PASS": EvPass, "REMARKS": EvRemarks, "WARN": EvRemarks, "FAIL": EvFail, "INCOMPLETE": EvIncomplete, "ABORTED": EvIncomplete} {
		if FinishedEvent(verdict) != ev {
			t.Errorf("%s -> %s", verdict, FinishedEvent(verdict))
		}
	}
}

func TestDeliver(t *testing.T) {
	var mu sync.Mutex
	var got []*http.Request
	var bodies [][]byte
	fail := 1 // the first request gets a 503
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		got, bodies = append(got, r), append(bodies, b)
		switch {
		case strings.HasSuffix(r.URL.Path, "/bad"):
			http.Error(w, "no such hook", 404)
		case fail > 0:
			fail--
			http.Error(w, "busy", http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	var logs []string
	n := NewNotifier(Config{Webhooks: []Webhook{
		{Name: "ok", URL: srv.URL + "/ok", Token: "tok", Secret: "s3cret", Headers: map[string]string{"X-Team": "dc1"}},
		{Name: "bad", URL: srv.URL + "/bad"},
		{Name: "only-fail", URL: srv.URL + "/f", Events: []string{EvFail}},
	}}, func(f string, a ...any) { mu.Lock(); logs = append(logs, f); mu.Unlock() })
	n.backoff = []time.Duration{0, 10 * time.Millisecond, 10 * time.Millisecond}

	e := New(EvStarted, "acceptance", Machine{Host: "srv-01"})
	res := n.Deliver(context.Background(), e)
	if res["ok"] != nil || res["bad"] == nil || !strings.Contains(res["bad"].Error(), "404") {
		t.Errorf("results %v", res)
	}
	if _, ok := res["only-fail"]; ok {
		t.Error("only-fail got a started event")
	}
	// ok: 503 then 200 (retried); bad: 404 once (not retried)
	paths := map[string]int{}
	for _, r := range got {
		paths[r.URL.Path]++
	}
	if paths["/ok"] != 2 || paths["/bad"] != 1 {
		t.Errorf("requests %v", paths)
	}
	r, body := got[len(got)-1], bodies[len(bodies)-1]
	for i := range got {
		if got[i].URL.Path == "/ok" {
			r, body = got[i], bodies[i]
		}
	}
	if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-Team") != "dc1" || r.Header.Get("X-Tyr-Event") != "started" ||
		r.Header.Get("X-Tyr-Signature") != "sha256="+Sign("s3cret", body) {
		t.Errorf("headers %v", r.Header)
	}

	// Send runs in the background; Wait collects it
	got = nil
	n.Send(event())
	n.Wait(5 * time.Second)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 { // ok, bad, only-fail
		t.Errorf("%d requests for a fail event", len(got))
	}
}

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"https://hooks.slack.com/services/T0/B0/SECRET":      "https://hooks.slack.com/…",
		"https://chat.example.net/hooks/abc/SECRET?x=1":      "https://chat.example.net/…",
		"http://n8n.lab:5678":                                "http://n8n.lab:5678",
		"https://prod.logic.azure.com:443/workflows/a?sig=S": "https://prod.logic.azure.com:443/…",
		"not a url": "(webhook URL)",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
	// a delivery error names the host, never the secret path
	n := NewNotifier(Config{Webhooks: []Webhook{{Name: "chat", URL: "http://127.0.0.1:1/hooks/abc/SECRETTOKEN"}}}, nil)
	n.backoff = []time.Duration{0}
	res := n.Deliver(context.Background(), New(EvTest, "acceptance", Machine{Host: "h"}))
	if err := res["chat"]; err == nil || strings.Contains(err.Error(), "SECRETTOKEN") || !strings.Contains(err.Error(), "127.0.0.1:1/…") {
		t.Errorf("error %v", err)
	}
}
