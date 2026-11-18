package tui

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/notify"
)

// webhookDialog (W) edits the webhooks that tests notify, and sends a test
// notification.
type webhookDialog struct {
	cfg     notify.Config
	sel     int
	edit    *notify.Webhook // the webhook in the form, nil in the list
	editIdx int             // its index, len(webhooks) for a new one
	field   int
	typing  bool
	input   lineInput
	dirty   bool
	confirm bool
	busy    string
	msg     string
	err     string
}

type webhookTestMsg struct {
	name string
	err  error
}

var whFields = []string{"name", "url", "format", "started", "step_failed", "step_warning", "pass", "remarks", "fail", "incomplete",
	"token", "secret", "insecure", "done"}

var whEventHelp = map[string]string{
	notify.EvStarted: "a test starts", notify.EvStepFailed: "a step fails (at once, not at the end)",
	notify.EvStepWarning: "a step ends with a warning (at once)", notify.EvPass: "finished: PASS",
	notify.EvRemarks: "finished: REMARKS / WARN", notify.EvFail: "finished: FAIL", notify.EvIncomplete: "finished: INCOMPLETE / ABORTED",
}

func (m *Model) openWebhooks() {
	d := &webhookDialog{input: lineInput{limit: 2048}}
	path := m.opts.WebhooksFile
	if path == "" {
		path = notify.Path()
	}
	c, err := notify.Load(path)
	if err != nil {
		d.err = err.Error()
	}
	c.Path = path
	d.cfg = c
	m.wh = d
}

func (d *webhookDialog) wants(ev string) bool {
	return len(d.edit.Events) == 0 || slices.Contains(d.edit.Events, ev)
}

func (d *webhookDialog) toggle(ev string) {
	if len(d.edit.Events) == 0 {
		d.edit.Events = slices.Clone(notify.AllEvents)
	}
	if i := slices.Index(d.edit.Events, ev); i >= 0 {
		d.edit.Events = slices.Delete(d.edit.Events, i, i+1)
	} else {
		var out []string // keep the order of AllEvents
		for _, e := range notify.AllEvents {
			if e == ev || slices.Contains(d.edit.Events, e) {
				out = append(out, e)
			}
		}
		d.edit.Events = out
	}
	if len(d.edit.Events) == len(notify.AllEvents) {
		d.edit.Events = nil // all
	}
}

func (d *webhookDialog) text(f string) *string {
	switch f {
	case "name":
		return &d.edit.Name
	case "url":
		return &d.edit.URL
	case "token":
		return &d.edit.Token
	case "secret":
		return &d.edit.Secret
	}
	return nil
}

func (m *Model) updateWebhooks(msg tea.KeyPressMsg) tea.Cmd {
	d := m.wh
	k := msg.String()
	if d.busy != "" {
		if k == "esc" {
			m.wh = nil
		}
		return nil
	}
	if d.edit != nil {
		return m.updateWebhookForm(msg)
	}
	if k != "esc" {
		d.confirm = false
	}
	n := len(d.cfg.Webhooks)
	switch k {
	case "esc", "q":
		if d.dirty && !d.confirm {
			d.confirm, d.msg = true, "unsaved changes: esc again to discard, or s to save"
			return nil
		}
		m.wh = nil
	case "up", "k":
		d.sel = max(0, d.sel-1)
	case "down", "j":
		d.sel = min(max(0, n-1), d.sel+1)
	case "a", "+":
		w := notify.Webhook{Format: "json"}
		d.edit, d.editIdx, d.field, d.err, d.msg = &w, n, 0, "", ""
	case "enter", "e":
		if n == 0 {
			w := notify.Webhook{Format: "json"}
			d.edit, d.editIdx, d.field = &w, 0, 0
			return nil
		}
		if d.cfg.Webhooks[d.sel].FromEnv {
			d.msg = "this one comes from TYR_WEBHOOK_* in the environment: change it there"
			return nil
		}
		w := d.cfg.Webhooks[d.sel]
		w.Events = slices.Clone(w.Events)
		d.edit, d.editIdx, d.field, d.err, d.msg = &w, d.sel, 0, "", ""
	case "d", "delete", "x":
		if n > 0 && !d.cfg.Webhooks[d.sel].FromEnv {
			d.cfg.Webhooks = slices.Delete(slices.Clone(d.cfg.Webhooks), d.sel, d.sel+1)
			d.sel = max(0, min(d.sel, len(d.cfg.Webhooks)-1))
			d.dirty = true
		}
	case "t":
		if n == 0 {
			return nil
		}
		w := d.cfg.Webhooks[d.sel]
		d.busy, d.msg, d.err = "sending a test to "+w.Label()+" …", "", ""
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			host, _ := os.Hostname()
			e := notify.New(notify.EvTest, "acceptance", notify.Machine{Host: host})
			e.Summary = "a test notification from the Tyr TUI"
			res := notify.NewNotifier(notify.Config{Webhooks: []notify.Webhook{w}}, nil).Deliver(ctx, e)
			return webhookTestMsg{name: w.Label(), err: res[w.Label()]}
		}
	case "s", "ctrl+s":
		d.msg, d.err = "", ""
		if err := d.cfg.Save(); err != nil {
			d.err = fmt.Sprintf("%s: %v", d.cfg.Path, err)
			return nil
		}
		d.dirty, d.msg = false, "saved "+d.cfg.Path
	}
	return nil
}

func (m *Model) updateWebhookForm(msg tea.KeyPressMsg) tea.Cmd {
	d := m.wh
	f := whFields[d.field]
	if d.typing {
		switch msg.String() {
		case "esc":
			d.typing = false
		case "enter":
			*d.text(f) = strings.TrimSpace(d.input.Value())
			d.typing = false
		default:
			d.input.Update(msg)
		}
		return nil
	}
	change := func(delta int) {
		switch {
		case f == "format":
			i := max(0, slices.Index(notify.Formats, d.edit.Format))
			d.edit.Format = notify.Formats[((i+delta)%len(notify.Formats)+len(notify.Formats))%len(notify.Formats)]
		case f == "insecure":
			d.edit.Insecure = !d.edit.Insecure
		case slices.Contains(notify.AllEvents, f):
			d.toggle(f)
		}
	}
	switch msg.String() {
	case "esc":
		d.edit, d.err = nil, ""
	case "up", "k":
		d.field = max(0, d.field-1)
	case "down", "j", "tab":
		d.field = min(len(whFields)-1, d.field+1)
	case "left", "h":
		change(-1)
	case "right", "l", "space", " ":
		change(+1)
	case "enter":
		switch {
		case f == "done":
			if err := d.edit.Validate(); err != nil {
				d.err = err.Error()
				return nil
			}
			ws := slices.Clone(d.cfg.Webhooks)
			if d.editIdx >= len(ws) {
				ws = append(ws, *d.edit)
			} else {
				ws[d.editIdx] = *d.edit
			}
			d.cfg.Webhooks, d.sel, d.edit, d.dirty, d.err = ws, d.editIdx, nil, true, ""
			d.msg = "s saves the file"
		case d.text(f) != nil:
			d.typing = true
			d.input.SetValue(*d.text(f))
			d.input.Focus()
		default:
			change(+1)
		}
	}
	return nil
}

func (d *webhookDialog) paste(text string) {
	if d.edit == nil {
		return
	}
	f := whFields[d.field]
	if !d.typing {
		p := d.text(f)
		if p == nil {
			return
		}
		d.typing = true
		d.input.SetValue("")
		if f == "name" {
			d.input.SetValue(*p)
		}
		d.input.Focus()
	}
	d.input.Insert(text)
}

func (m *Model) webhookTested(msg webhookTestMsg) {
	if m.wh == nil {
		return
	}
	m.wh.busy = ""
	if msg.err != nil {
		m.wh.err = msg.name + ": " + msg.err.Error()
	} else {
		m.wh.msg = msg.name + ": test notification delivered"
	}
}

func (m *Model) webhooksView(base string) string {
	t, d := m.theme, m.wh
	inner := min(96, m.width-4) - 4
	var lines []string
	if d.edit == nil {
		if len(d.cfg.Webhooks) == 0 {
			lines = append(lines, t.Sub.Render("No webhooks: tests send no notifications. a adds one."),
				t.Sub.Render("Or set TYR_WEBHOOK_URL (and TYR_WEBHOOK_FORMAT, _EVENTS, _TOKEN, _SECRET) in the environment."))
		}
		for i, w := range d.cfg.Webhooks {
			ev := "all events"
			if len(w.Events) > 0 {
				ev = strings.Join(w.Events, " ")
			}
			row := pad(truncate(w.Label(), 18), 18) + " " + pad(firstNonEmpty(w.Format, "json"), 8) + " " +
				t.Sub.Render(truncate(ev, max(10, inner-32)))
			if w.FromEnv {
				row = t.Dim.Render(stripStyle(row) + " (environment)")
			}
			if i == d.sel {
				row = lipgloss.NewStyle().Foreground(t.Accent).Render("▌") + row
			} else {
				row = " " + row
			}
			lines = append(lines, row)
			if i == d.sel {
				lines = append(lines, "  "+t.Sub.Render(truncate(w.URL, inner-2)))
			}
		}
		lines = append(lines, "", t.Sub.Render("Tests (tyr accept, tyr stress, the ISO) notify when they start, at once when a step fails"),
			t.Sub.Render("or warns, and when they finish. Formats: json, slack (Mattermost, Rocket.Chat), teams, discord, gchat."),
			"", t.Sub.Render(truncate("file: "+d.cfg.Path, inner)),
			t.Sub.Render("a add · enter edit · d delete · t send a test · s save · esc close"))
	} else {
		const lw = 13
		title := "New webhook"
		if d.editIdx < len(d.cfg.Webhooks) {
			title = "Edit webhook"
		}
		lines = append(lines, t.Bold.Render(title), "")
		onOff := func(b bool) string { return pill(t, "off", !b) + pill(t, "on", b) }
		for i, f := range whFields {
			focused := i == d.field
			var label, v string
			switch {
			case f == "done":
				lines = append(lines, "", strings.Repeat(" ", lw)+button(t, "Done", focused)+t.Sub.Render("  esc cancels"))
				continue
			case f == "format":
				label = "Format"
				for _, x := range notify.Formats {
					v += pill(t, x, firstNonEmpty(d.edit.Format, "json") == x)
				}
			case f == "insecure":
				label, v = "TLS", pill(t, "verify", !d.edit.Insecure)+pill(t, "insecure", d.edit.Insecure)
			case slices.Contains(notify.AllEvents, f):
				label = map[bool]string{true: "Events", false: ""}[f == notify.EvStarted]
				v = onOff(d.wants(f)) + " " + pad(f, 13) + t.Sub.Render(whEventHelp[f])
			default:
				label = map[string]string{"name": "Name", "url": "URL", "token": "Token", "secret": "Secret"}[f]
				txt := *d.text(f)
				if (f == "token" || f == "secret") && txt != "" {
					txt = strings.Repeat("•", min(12, len(txt))) + fmt.Sprintf(" (%d characters)", len(txt))
				}
				if d.typing && focused {
					v = d.input.Field(t, max(20, inner-lw-2))
				} else {
					in := lineInput{value: []rune(txt)}
					v = in.Field(t, max(20, inner-lw-2))
				}
			}
			lines = append(lines, fieldLabel(t, label, focused, lw)+v)
		}
		help := map[string]string{
			"name": "shown in Tyr's log; optional", "url": "the incoming webhook URL (Slack, Teams Workflows, Discord, Google Chat, your endpoint)",
			"format": "json: structured, for your own receiver; the others: chat messages",
			"token":  "optional: sent as Authorization: Bearer", "secret": "optional: HMAC-SHA256 of the body in X-Tyr-Signature",
		}[whFields[d.field]]
		if help != "" {
			lines = append(lines, "", t.Sub.Render(truncate(help, inner)))
		}
		lines = append(lines, t.Sub.Render("↑↓ field · ←→ space change · enter edit · paste onto a field · esc cancel"))
	}
	if d.busy != "" {
		lines = append(lines, "", m.spin.View()+" "+d.busy)
	}
	if d.err != "" {
		lines = append(lines, "", t.StatusText(model.StatusCrit, "✖ "+d.err))
	} else if d.msg != "" {
		lines = append(lines, "", t.Sub.Render(d.msg))
	}
	right := "saved"
	if d.dirty {
		right = "unsaved changes"
	}
	return overlayR(t, base, t.Accent, "Webhooks", right, strings.Join(lines, "\n"), m.width, m.height)
}
