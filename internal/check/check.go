// Package check holds threshold defaults and the logic that turns readings
// into a Status. Source thresholds (hwmon, IPMI, SMART, transceiver) always
// win; the configurable defaults fill the gaps.
package check

import (
	"fmt"
	"strings"

	"github.com/noahzmr/tyr/internal/model"
)

// Defaults are the configurable fallback thresholds.
type Defaults struct {
	WarnTempC float64 // --warn-temp
	CritTempC float64 // --crit-temp
}

// DefaultDefaults returns the built-in fallbacks.
func DefaultDefaults() Defaults { return Defaults{WarnTempC: 75, CritTempC: 90} }

// Eval returns the status of value against t. A nil value is UNKNOWN.
func Eval(value *float64, t *model.Thresholds) model.Status {
	if value == nil {
		return model.StatusUnknown
	}
	if t == nil {
		return model.StatusOK
	}
	v := *value
	switch {
	case t.UpperCrit != nil && v >= *t.UpperCrit, t.LowerCrit != nil && v <= *t.LowerCrit:
		return model.StatusCrit
	case t.UpperWarn != nil && v >= *t.UpperWarn, t.LowerWarn != nil && v <= *t.LowerWarn:
		return model.StatusWarn
	}
	return model.StatusOK
}

// TempThresholds merges source temperature limits with the defaults. The
// source's own values win; missing ones are filled from d. A source warn
// limit above the critical one is ignored.
func TempThresholds(src *model.Thresholds, d Defaults) *model.Thresholds {
	t := &model.Thresholds{Origin: "default"}
	if src != nil {
		*t = *src
	}
	filled := false
	if t.UpperCrit == nil {
		t.UpperCrit = model.F(d.CritTempC)
		filled = true
	}
	if t.UpperWarn == nil || *t.UpperWarn > *t.UpperCrit {
		w := d.WarnTempC
		if w >= *t.UpperCrit {
			w = *t.UpperCrit - 10
		}
		t.UpperWarn = model.F(w)
		filled = true
	}
	if src != nil && filled && t.Origin != "default" {
		t.Origin += "+default"
	}
	return t
}

// EvalSensor sets s.Status from its value and thresholds.
func EvalSensor(s *model.Sensor) { s.Status = Eval(s.Value, s.Thresholds) }

// FmtTemp formats a temperature for messages.
func FmtTemp(v float64) string { return fmt.Sprintf("%.0f°C", v) }

// SummaryLine builds the Nagios-style one-line summary from findings:
// CRIT findings first, then WARN, joined with "; ", truncated with "(+N more)".
// It returns "" when there is nothing to report.
func SummaryLine(findings []model.Finding, maxLen int) string {
	var parts []string
	for _, lvl := range []model.Status{model.StatusCrit, model.StatusWarn, model.StatusUnknown} {
		for _, f := range findings {
			if f.Status == lvl {
				parts = append(parts, f.Message)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	for i, p := range parts {
		sep := ""
		if i > 0 {
			sep = "; "
		}
		more := ""
		if rest := len(parts) - i - 1; rest > 0 {
			more = fmt.Sprintf(" (+%d more)", rest)
		}
		if i > 0 && b.Len()+len(sep)+len(p)+len(more) > maxLen {
			fmt.Fprintf(&b, " (+%d more)", len(parts)-i)
			break
		}
		b.WriteString(sep)
		b.WriteString(p)
	}
	return b.String()
}
