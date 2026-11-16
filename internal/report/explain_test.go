package report

import (
	"strings"
	"testing"

	"github.com/noahzmr/tyr/internal/model"
)

func TestExplainAlert(t *testing.T) {
	s := snap(t, "server-degraded")
	var sdd, sdf Alert
	for _, a := range alertsFrom(s) {
		switch {
		case a.Code == "kernel_io_errors" && a.Subject == "sdd":
			sdd = a
		case a.Code == "remote_io_errors" && a.Subject == "sdf":
			sdf = a
		}
	}
	if sdd.Code == "" || sdf.Code == "" {
		t.Fatalf("fixture alerts missing: %+v %+v", sdd, sdf)
	}
	got := ExplainAlert(s, sdd)
	for _, want := range []string{"Reads or writes failed", "SATA port 4", "S/N WJG0A004", "Disk failure on sdd1",
		"## What it means", "## Usual causes", "smartctl -x /dev/sdd"} {
		if !strings.Contains(got, want) {
			t.Errorf("sdd explanation lacks %q:\n%s", want, got)
		}
	}
	got = ExplainAlert(s, sdf)
	for _, want := range []string{"iSCSI iqn.2024-01.net.example:backup.vol2 via 10.16.40.20:3260", "connection1:0", "iscsiadm -m session -P 3"} {
		if !strings.Contains(got, want) {
			t.Errorf("sdf explanation lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "S/N\n") || strings.Contains(got, "S/N ·") {
		t.Errorf("empty serial printed:\n%s", got)
	}
	// unknown codes still get a page
	if got := ExplainAlert(s, Alert{model.ModStorage, model.Finding{Code: "no_such_code", Subject: "x", Message: "m", Status: model.StatusWarn}}); !strings.Contains(got, "No explanation") {
		t.Errorf("unknown code: %s", got)
	}
}

func TestReportExplainsAlerts(t *testing.T) {
	md := Snapshot(snap(t, "server-degraded"))
	for _, want := range []string{"## What the alerts mean", "`storage/kernel_io_errors`", "**How to check**"} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	ref := AlertReference()
	for _, want := range []string{"# Alert reference", "## Storage", "`storage/crc_errors`", "## Network"} {
		if !strings.Contains(ref, want) {
			t.Errorf("reference lacks %q", want)
		}
	}
}
