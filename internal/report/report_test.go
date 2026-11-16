package report

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/collector/all"
	"github.com/noahzmr/tyr/internal/fixture"
	"github.com/noahzmr/tyr/internal/model"
)

const fixtures = "../../testdata/fixtures/"

func snap(t *testing.T, name string) *model.Snapshot {
	t.Helper()
	env, _, err := fixture.Load(fixtures+name, collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return collector.Collect(context.Background(), env, all.Collectors(), nil)
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotReport(t *testing.T) {
	s := snap(t, "server-degraded")
	md := Snapshot(s)
	if os.Getenv("TYR_SHOW_MD") != "" {
		t.Log("\n" + md)
	}
	for _, want := range []string{"# srv-degraded-01 · hardware report", "**Status: ✖ CRIT**", "## Alerts", "| ✖ CRIT | CPU | cpu7 |",
		"## Memory", "P2-DIMMB1", "## Storage", "WJG0A004", "## Inventory"} {
		if !strings.Contains(md, want) {
			t.Errorf("snapshot report missing %q", want)
		}
	}
	if md := Snapshot(snap(t, "laptop-amd-nonroot")); !strings.Contains(md, "## Data sources not used") || !strings.Contains(md, "requires root") {
		t.Errorf("non-root report lacks the skipped sources")
	}
	out, err := Render(md, 120, StylePlain)
	if err != nil || !strings.Contains(out, "srv-degraded-01") {
		t.Errorf("render: %v\n%s", err, out)
	}
}

func TestHistory(t *testing.T) {
	dir := t.TempDir()
	a := snap(t, "server-degraded")
	b := snap(t, "server-degraded")
	b.Timestamp = a.Timestamp.Add(48 * time.Hour)
	// two days later: one alert resolved, sdc grew 5 I/O errors, a DIMM swapped, new BIOS
	var keep []model.Finding
	for _, f := range b.Storage.Findings {
		if f.Code != "fs_read_only" {
			keep = append(keep, f)
		}
	}
	b.Storage.Findings = keep
	for i := range b.Storage.Disks {
		if b.Storage.Disks[i].Name == "sdc" {
			n := uint64(5)
			b.Storage.Disks[i].IOErrors = &n
		}
	}
	for i := range b.Inventory {
		if b.Inventory[i].Location == "P2-DIMMB1" {
			b.Inventory[i].Serial = "NEW0001"
		}
	}
	b.System.BIOSVersion = "2.0"
	writeJSON(t, filepath.Join(dir, "a.json"), a)
	writeJSON(t, filepath.Join(dir, "b.json"), b)
	if err := os.WriteFile(filepath.Join(dir, "other.json"), []byte(`{"hello":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	docs, skipped, err := Load([]string{dir})
	if err != nil || len(docs) != 2 || len(skipped) != 1 {
		t.Fatalf("load: %v docs %d skipped %v", err, len(docs), skipped)
	}
	hs := BuildHistory(docs)
	if len(hs) != 1 || len(hs[0].Snapshots) != 2 {
		t.Fatalf("history %+v", hs)
	}
	h := hs[0]
	resolved := false
	for _, x := range h.Alerts {
		if x.Code == "fs_read_only" && !x.Open && x.ResolvedAt != nil && x.ResolvedAt.Equal(b.Timestamp) {
			resolved = true
		}
	}
	if !resolved {
		t.Error("fs_read_only not resolved")
	}
	grew := false
	for _, c := range h.Counters {
		if c.Counter == "disk.sdc.io_errors" && c.Change == 5 {
			grew = true
		}
	}
	if !grew {
		t.Errorf("counter trend missing: %+v", h.Counters)
	}
	var kinds []string
	for _, c := range h.Changes {
		kinds = append(kinds, c.Kind+" "+c.Component)
	}
	if j := strings.Join(kinds, ","); !strings.Contains(j, "replaced dimm P2-DIMMB1") || !strings.Contains(j, "firmware BIOS") {
		t.Errorf("changes: %s", j)
	}
	md := HistoryMarkdown(hs, skipped)
	if os.Getenv("TYR_SHOW_MD") != "" {
		t.Log("\n" + md)
	}
	for _, want := range []string{"# srv-degraded-01 · history", "## Alerts timeline", "resolved", "## Counter trends", "disk.sdc.io_errors",
		"## Sensor trends", "## Hardware changes", "NEW0001", "## Stress tests", "## Files not used", "other.json"} {
		if !strings.Contains(md, want) {
			t.Errorf("history missing %q", want)
		}
	}
}
