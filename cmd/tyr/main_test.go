package main

import (
	"bytes"
	"encoding/json"
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
