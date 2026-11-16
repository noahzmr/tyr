package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fx = "../../testdata/fixtures/"

func TestReportExitCodesAndInventory(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"report", "--fixtures", fx + "server-healthy"}, &out, &errb); code != 0 {
		t.Errorf("healthy exit %d: %s", code, errb.String())
	}
	if !strings.HasSuffix(strings.TrimSpace(out.String()), "OK - all checks passed") {
		t.Errorf("last line: %q", out.String())
	}
	out.Reset()
	if code := run([]string{"report", "--fixtures", fx + "server-degraded", "--inventory"}, &out, &errb); code != 2 {
		t.Errorf("degraded exit %d", code)
	}
	for _, want := range []string{"CRITICAL - ", "Inventory", "SERIAL", "ZFL4A002", "P1K22CL21123452"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
	out.Reset()
	if code := run([]string{"report", "--fixtures", fx + "server-degraded", "--format", "json", "--section", "cpu"}, &out, &errb); code != 2 {
		t.Errorf("json exit %d", code)
	}
	var snap map[string]any
	if err := json.Unmarshal(out.Bytes(), &snap); err != nil || snap["schema_version"] != "1.0" || snap["inventory"] == nil || snap["memory"] != nil {
		t.Errorf("json: %v", err)
	}
	if code := run([]string{"report", "--section", "bogus"}, &out, &errb); code != 3 {
		t.Errorf("bad section exit %d", code)
	}
	if code := run([]string{"report", "--warn-temp", "95", "--crit-temp", "90", "--fixtures", fx + "server-healthy"}, &out, &errb); code != 3 {
		t.Errorf("warn>crit exit %d", code)
	}
}

func TestMarkdownReportsAndHistory(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	// live (fixture) report as Markdown
	if code := run([]string{"report", "--fixtures", fx + "server-degraded", "--format", "markdown"}, &out, &errb); code != 2 ||
		!strings.Contains(out.String(), "# srv-degraded-01 · hardware report") || !strings.Contains(out.String(), "## Alerts") {
		t.Fatalf("markdown report exit %d\n%s", code, out.String())
	}
	// save two snapshots, render one back
	for _, name := range []string{"server-healthy", "server-degraded"} {
		out.Reset()
		run([]string{"report", "--fixtures", fx + name, "--format", "json"}, &out, &errb)
		if err := os.WriteFile(filepath.Join(dir, name+".json"), out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	if code := run([]string{"report", "--from", filepath.Join(dir, "server-degraded.json"), "--format", "markdown"}, &out, &errb); code != 2 ||
		!strings.Contains(out.String(), "**Status: ✖ CRIT**") {
		t.Errorf("--from exit %d\n%s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"report", "--from", filepath.Join(dir, "server-healthy.json")}, &out, &errb); code != 0 || !strings.Contains(out.String(), "OK - all checks passed") {
		t.Errorf("--from text exit %d\n%s", code, out.String())
	}
	// history of the directory: two hosts, Markdown and JSON
	out.Reset()
	if code := run([]string{"history", dir}, &out, &errb); code != 0 || !strings.Contains(out.String(), "# srv-degraded-01 · history") ||
		!strings.Contains(out.String(), "# srv-healthy-01 · history") {
		t.Errorf("history exit %d: %s\n%s", code, errb.String(), out.String())
	}
	out.Reset()
	if code := run([]string{"history", "--format", "json", "--host", "srv-degraded-01", dir}, &out, &errb); code != 0 {
		t.Fatalf("history json exit %d: %s", code, errb.String())
	}
	var hs []map[string]any
	if err := json.Unmarshal(out.Bytes(), &hs); err != nil || len(hs) != 1 || hs[0]["host"] != "srv-degraded-01" || hs[0]["alerts"] == nil {
		t.Errorf("history json: %v %v", err, hs)
	}
	// view without a terminal prints the Markdown
	out.Reset()
	if code := run([]string{"view", filepath.Join(dir, "server-healthy.json")}, &out, &errb); code != 0 || !strings.Contains(out.String(), "# srv-healthy-01 · hardware report") {
		t.Errorf("view exit %d\n%s", code, out.String())
	}
	if code := run([]string{"history", t.TempDir()}, &out, &errb); code != 3 {
		t.Errorf("empty history exit %d", code)
	}
}
