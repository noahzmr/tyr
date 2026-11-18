package notify

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/buildinfo"
	"github.com/noahzmr/tyr/internal/model"
)

// Event is one notification. Its JSON is the generic format.
type Event struct {
	Event      string    `json:"event"` // started, step_failed, step_warning, pass, remarks, fail, incomplete, test
	Time       time.Time `json:"time"`
	Run        string    `json:"run"` // acceptance, stress
	Host       string    `json:"host"`
	Vendor     string    `json:"vendor,omitempty"`
	Product    string    `json:"product,omitempty"`
	Serial     string    `json:"serial,omitempty"`
	Profile    string    `json:"profile,omitempty"` // acceptance profile, stress components
	Step       string    `json:"step,omitempty"`    // step_*: the step
	Status     string    `json:"status,omitempty"`  // step_*: its result; finished: the verdict
	Summary    string    `json:"summary,omitempty"`
	Failures   []string  `json:"failures,omitempty"`
	Remarks    []string  `json:"remarks,omitempty"`
	Duration   string    `json:"duration,omitempty"` // started: expected; finished: took
	ResultsDir string    `json:"results_dir,omitempty"`
	TyrVersion string    `json:"tyr_version"`
}

// Machine is who the event is about.
type Machine struct {
	Host, Vendor, Product, Serial string
}

// MachineOf takes the identity from a snapshot.
func MachineOf(s *model.Snapshot) Machine {
	m := Machine{}
	if s == nil {
		m.Host, _ = os.Hostname()
		return m
	}
	m.Host = s.Hostname
	if y := s.System; y != nil {
		m.Vendor, m.Product, m.Serial = y.Vendor, y.Product, y.Serial
	}
	return m
}

// New makes an event for a machine.
func New(kind, run string, m Machine) Event {
	return Event{Event: kind, Time: time.Now().UTC(), Run: run, Host: m.Host, Vendor: m.Vendor, Product: m.Product,
		Serial: m.Serial, TyrVersion: buildinfo.Version}
}

// FinishedEvent maps a verdict (acceptance PASS/REMARKS/FAIL/INCOMPLETE,
// stress PASS/WARN/FAIL/ABORTED) to its event.
func FinishedEvent(verdict string) string {
	switch strings.ToUpper(verdict) {
	case "PASS":
		return EvPass
	case "REMARKS", "WARN":
		return EvRemarks
	case "FAIL":
		return EvFail
	}
	return EvIncomplete
}

// level is the event's severity for colours and emoji.
func (e Event) level() model.Status {
	switch e.Event {
	case EvStepFailed, EvFail:
		return model.StatusCrit
	case EvStepWarning, EvRemarks, EvIncomplete:
		return model.StatusWarn
	}
	return model.StatusOK
}

func (e Event) runName() string {
	if e.Run == "stress" {
		return "stress test"
	}
	return "acceptance test"
}

// Title is the one-line headline.
func (e Event) Title() string {
	who := e.Host
	if e.Serial != "" {
		who += " (S/N " + e.Serial + ")"
	}
	switch e.Event {
	case EvStarted:
		return fmt.Sprintf("Tyr %s started on %s", e.runName(), who)
	case EvStepFailed:
		return fmt.Sprintf("Tyr %s on %s: %s %s", e.runName(), who, e.Step, firstNonEmpty(e.Status, "failed"))
	case EvStepWarning:
		return fmt.Sprintf("Tyr %s on %s: %s %s", e.runName(), who, e.Step, firstNonEmpty(e.Status, "warning"))
	case EvTest:
		return "Tyr webhook test from " + who
	}
	return fmt.Sprintf("Tyr %s %s on %s", e.runName(), e.Status, who)
}

// Facts are the key/value lines below the title.
func (e Event) Facts() [][2]string {
	var f [][2]string
	add := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			f = append(f, [2]string{k, v})
		}
	}
	add("Machine", strings.TrimSpace(e.Vendor+" "+e.Product))
	add("Profile", e.Profile)
	if e.Event == EvStarted {
		add("Expected", e.Duration)
	} else {
		add("Took", e.Duration)
	}
	add("Summary", e.Summary)
	add("Results", e.ResultsDir)
	add("Tyr", e.TyrVersion)
	return f
}

func emoji(e Event) string {
	switch {
	case e.Event == EvStarted || e.Event == EvTest:
		return "▶️"
	case e.level() == model.StatusCrit:
		return "❌"
	case e.level() == model.StatusWarn:
		return "⚠️"
	}
	return "✅"
}

// text renders the event as chat text: bold marks ("*" Slack and Google
// Chat, "**" Discord and Teams).
func (e Event) text(bold string, limit int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s%s%s", emoji(e), bold, e.Title(), bold)
	for _, kv := range e.Facts() {
		fmt.Fprintf(&b, "\n%s%s:%s %s", bold, kv[0], bold, kv[1])
	}
	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s%s%s", bold, title, bold)
		for i, it := range items {
			if i == 10 {
				fmt.Fprintf(&b, "\n• … %d more", len(items)-10)
				break
			}
			fmt.Fprintf(&b, "\n• %s", it)
		}
	}
	list("Failures", e.Failures)
	list("Remarks", e.Remarks)
	s := b.String()
	if r := []rune(s); limit > 0 && len(r) > limit {
		s = string(r[:limit-1]) + "…"
	}
	return s
}

// Body is the request body in a format.
func (e Event) Body(format string) ([]byte, error) {
	switch format {
	case "slack":
		return json.Marshal(map[string]string{"text": e.text("*", 0)})
	case "gchat":
		return json.Marshal(map[string]string{"text": e.text("*", 4000)})
	case "discord":
		return json.Marshal(map[string]any{"content": e.text("**", 2000), "allowed_mentions": map[string]any{"parse": []string{}}})
	case "teams":
		return json.Marshal(teamsCard(e))
	}
	return json.Marshal(e)
}

// teamsCard is a Teams Workflows message with an Adaptive Card.
func teamsCard(e Event) map[string]any {
	color := map[model.Status]string{model.StatusOK: "Good", model.StatusWarn: "Warning", model.StatusCrit: "Attention"}[e.level()]
	if e.Event == EvStarted || e.Event == EvTest {
		color = "Accent"
	}
	body := []any{
		map[string]any{"type": "TextBlock", "text": emoji(e) + " " + e.Title(), "weight": "Bolder", "size": "Medium", "color": color, "wrap": true},
	}
	var facts []any
	for _, kv := range e.Facts() {
		facts = append(facts, map[string]string{"title": kv[0], "value": kv[1]})
	}
	if len(facts) > 0 {
		body = append(body, map[string]any{"type": "FactSet", "facts": facts})
	}
	for _, l := range []struct {
		title string
		items []string
	}{{"Failures", e.Failures}, {"Remarks", e.Remarks}} {
		if len(l.items) == 0 {
			continue
		}
		items := l.items
		if len(items) > 10 {
			items = append(items[:10:10], fmt.Sprintf("… %d more", len(l.items)-10))
		}
		body = append(body, map[string]any{"type": "TextBlock", "text": l.title, "weight": "Bolder", "wrap": true},
			map[string]any{"type": "TextBlock", "text": "- " + strings.Join(items, "\n- "), "wrap": true})
	}
	return map[string]any{
		"type": "message",
		"attachments": []any{map[string]any{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"content": map[string]any{
				"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
				"type":    "AdaptiveCard", "version": "1.4", "body": body,
			},
		}},
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
