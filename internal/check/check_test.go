package check

import (
	"testing"

	"github.com/noahzmr/tyr/internal/model"
)

func TestEval(t *testing.T) {
	th := &model.Thresholds{LowerCrit: model.F(10.8), LowerWarn: model.F(11.2), UpperWarn: model.F(12.8), UpperCrit: model.F(13.2)}
	cases := []struct {
		v    *float64
		want model.Status
	}{
		{nil, model.StatusUnknown},
		{model.F(12), model.StatusOK},
		{model.F(12.8), model.StatusWarn},
		{model.F(13.5), model.StatusCrit},
		{model.F(11.0), model.StatusWarn},
		{model.F(10.8), model.StatusCrit},
	}
	for _, c := range cases {
		if got := Eval(c.v, th); got != c.want {
			t.Errorf("Eval(%v) = %v, want %v", c.v, got, c.want)
		}
	}
	if Eval(model.F(999), nil) != model.StatusOK {
		t.Error("no thresholds must be OK")
	}
}

func TestTempThresholds(t *testing.T) {
	d := DefaultDefaults()
	got := TempThresholds(nil, d)
	if *got.UpperWarn != 75 || *got.UpperCrit != 90 || got.Origin != "default" {
		t.Errorf("defaults: %+v", got)
	}
	// coretemp style: max 84, crit 100 -> both from source
	got = TempThresholds(&model.Thresholds{UpperWarn: model.F(84), UpperCrit: model.F(100), Origin: "hwmon"}, d)
	if *got.UpperWarn != 84 || *got.UpperCrit != 100 || got.Origin != "hwmon" {
		t.Errorf("source: %+v", got)
	}
	// only crit from source, low crit -> warn derived below crit
	got = TempThresholds(&model.Thresholds{UpperCrit: model.F(70), Origin: "hwmon"}, d)
	if *got.UpperWarn != 60 || *got.UpperCrit != 70 || got.Origin != "hwmon+default" {
		t.Errorf("crit only: %+v", got)
	}
}

func TestSummaryLine(t *testing.T) {
	fs := []model.Finding{
		{Status: model.StatusWarn, Message: "cpu3 at 80°C"},
		{Status: model.StatusCrit, Message: "PSU2 failed"},
		{Status: model.StatusOK, Message: "ignored"},
		{Status: model.StatusCrit, Message: "1 core offline (cpu7)"},
	}
	if got, want := SummaryLine(fs, 200), "PSU2 failed; 1 core offline (cpu7); cpu3 at 80°C"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := SummaryLine(fs, 20), "PSU2 failed (+2 more)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if SummaryLine(nil, 10) != "" {
		t.Error("empty findings must give empty line")
	}
}
