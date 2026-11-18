// Package notify sends webhook notifications about tests: when one starts,
// at once when a step fails or ends with a warning, and when it finishes
// with its verdict. Formats: generic JSON, Slack (also Mattermost and
// Rocket.Chat), Microsoft Teams (Workflows, Adaptive Card), Discord and
// Google Chat.
//
// Settings come from webhooks.json (/etc/tyr as root, ~/.config/tyr as a
// user) and from TYR_WEBHOOK_* in the environment, which add one more
// webhook - how the live ISO gets them at build or boot time.
package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/noahzmr/tyr/internal/settings"
)

// Events a webhook can receive.
const (
	EvStarted     = "started"      // a test started
	EvStepFailed  = "step_failed"  // a step failed (stress FAIL or ABORTED, critical findings)
	EvStepWarning = "step_warning" // a step ended with a warning
	EvPass        = "pass"         // finished: PASS
	EvRemarks     = "remarks"      // finished: REMARKS (acceptance) or WARN (stress)
	EvFail        = "fail"         // finished: FAIL
	EvIncomplete  = "incomplete"   // finished: INCOMPLETE or ABORTED
	EvTest        = "test"         // tyr notify --test
)

// AllEvents are the events a webhook gets when it names none.
var AllEvents = []string{EvStarted, EvStepFailed, EvStepWarning, EvPass, EvRemarks, EvFail, EvIncomplete}

// Formats are the payload formats.
var Formats = []string{"json", "slack", "teams", "discord", "gchat"}

// Webhook is one receiver.
type Webhook struct {
	Name     string            `json:"name,omitempty"`
	URL      string            `json:"url"`
	Format   string            `json:"format,omitempty"` // json (default), slack, teams, discord, gchat
	Events   []string          `json:"events,omitempty"` // empty: all
	Token    string            `json:"token,omitempty"`  // sent as Authorization: Bearer
	Secret   string            `json:"secret,omitempty"` // HMAC-SHA256 of the body in X-Tyr-Signature
	Headers  map[string]string `json:"headers,omitempty"`
	Insecure bool              `json:"insecure,omitempty"`
	FromEnv  bool              `json:"-"`
}

// Wants reports whether the webhook takes an event.
func (w Webhook) Wants(ev string) bool {
	if ev == EvTest {
		return true
	}
	if len(w.Events) == 0 {
		return true
	}
	return slices.Contains(w.Events, ev)
}

// Label names the webhook in messages: its name, else its host.
func (w Webhook) Label() string {
	if w.Name != "" {
		return w.Name
	}
	u := strings.TrimPrefix(strings.TrimPrefix(w.URL, "https://"), "http://")
	h, _, _ := strings.Cut(u, "/")
	return h
}

// Validate checks one webhook.
func (w Webhook) Validate() error {
	if !strings.HasPrefix(w.URL, "https://") && !strings.HasPrefix(w.URL, "http://") {
		return fmt.Errorf("webhook %s: URL %q must start with https:// or http://", w.Label(), w.URL)
	}
	if w.Format != "" && !slices.Contains(Formats, w.Format) {
		return fmt.Errorf("webhook %s: format %q: %s", w.Label(), w.Format, strings.Join(Formats, ", "))
	}
	for _, e := range w.Events {
		if !slices.Contains(AllEvents, e) {
			return fmt.Errorf("webhook %s: event %q: %s", w.Label(), e, strings.Join(AllEvents, ", "))
		}
	}
	return nil
}

// Config is webhooks.json plus the environment's webhook.
type Config struct {
	Webhooks []Webhook `json:"webhooks"`
	Path     string    `json:"-"`
}

// Path is where webhooks.json is read from.
func Path() string { return settings.File("webhooks.json") }

// Load reads the file (missing: none) and adds the webhook from
// TYR_WEBHOOK_URL, TYR_WEBHOOK_FORMAT, TYR_WEBHOOK_EVENTS (comma-separated),
// TYR_WEBHOOK_TOKEN, TYR_WEBHOOK_SECRET and TYR_WEBHOOK_INSECURE.
func Load(path string) (Config, error) {
	c := Config{Path: path}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return c, err
	default:
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields() // a typo must not silently drop a setting
		if err := dec.Decode(&c); err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
		c.Path = path
	}
	if u := os.Getenv("TYR_WEBHOOK_URL"); u != "" {
		w := Webhook{Name: "env", URL: u, Format: os.Getenv("TYR_WEBHOOK_FORMAT"), Token: os.Getenv("TYR_WEBHOOK_TOKEN"),
			Secret: os.Getenv("TYR_WEBHOOK_SECRET"), FromEnv: true}
		w.Insecure = os.Getenv("TYR_WEBHOOK_INSECURE") == "1" || strings.EqualFold(os.Getenv("TYR_WEBHOOK_INSECURE"), "true")
		for _, e := range strings.Split(os.Getenv("TYR_WEBHOOK_EVENTS"), ",") {
			if e = strings.TrimSpace(e); e != "" {
				w.Events = append(w.Events, e)
			}
		}
		c.Webhooks = append(c.Webhooks, w)
	}
	return c, c.Validate()
}

// Validate checks every webhook.
func (c Config) Validate() error {
	for _, w := range c.Webhooks {
		if err := w.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Save writes the file's webhooks (not the environment's), mode 0600:
// tokens and secrets.
func (c Config) Save() error {
	if err := c.Validate(); err != nil {
		return err
	}
	out := Config{Webhooks: []Webhook{}}
	for _, w := range c.Webhooks {
		if !w.FromEnv {
			out.Webhooks = append(out.Webhooks, w)
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if c.Path == "" {
		return errors.New("no file to save to")
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	tmp := c.Path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.Path)
}
