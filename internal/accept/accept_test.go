package accept

import (
	"strings"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/stress"
)

func snap(mem uint64, edacCE uint64, findings ...model.Finding) *model.Snapshot {
	s := &model.Snapshot{Hostname: "h", Memory: &model.Memory{TotalBytes: mem, EDAC: []model.EDACController{{Name: "mc0", CorrectableErrors: edacCE}}}}
	s.Memory.Findings = findings
	return s
}

func TestProfiles(t *testing.T) {
	p, err := Builtin("burnin")
	if err != nil || p.Total(false) != 225*time.Minute || p.Total(true) != 235*time.Minute || p.CPUEngine != "torture" {
		t.Fatalf("%v %v %+v", err, p.Total(false), p)
	}
	if _, err := Builtin("nope"); err == nil || !strings.Contains(err.Error(), "burnin, extended, quick") {
		t.Errorf("unknown profile: %v", err)
	}
	// a limits file merges durations per key and replaces lists
	q, err := Merge(p, []byte(`{"durations":{"cpu":"2h"},"ignore":["link_down"],"max_temp_c":{"cpu":90},"min":{"memory_gb":256}}`))
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(q.Durations["cpu"]) != 2*time.Hour || time.Duration(q.Durations["memory"]) != 90*time.Minute ||
		len(q.Ignore) != 1 || q.MaxTempC["cpu"] != 90 || q.Min.MemoryGB != 256 || len(q.FailOnWarn) != len(p.FailOnWarn) {
		t.Errorf("merged %+v", q)
	}
	// the built-in profile is not changed by a merge
	if b, _ := Builtin("burnin"); time.Duration(b.Durations["cpu"]) != 90*time.Minute {
		t.Errorf("builtin changed: %v", b.Durations)
	}
	for _, bad := range []string{`{"durations":{"gpu":"1h"}}`, `{"durations":{"cpu":"soon"}}`, `{"max_temp_c":{"nvme":70}}`, `{"typo":1}`, `{"cpu_engine":"mprime"}`} {
		if _, err := Merge(p, []byte(bad)); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

func TestJudge(t *testing.T) {
	p, _ := Builtin("quick")
	warn := func(code, msg string) model.Finding {
		return model.Finding{Status: model.StatusWarn, Code: code, Subject: "x", Message: msg}
	}
	cases := []struct {
		name          string
		before, after *model.Snapshot
		runs          []*stress.Result
		incomplete    []string
		want          Verdict
		has           string
	}{
		{"clean", snap(64<<30, 0), snap(64<<30, 0), nil, nil, Pass, ""},
		{"plain warning is a remark", snap(64<<30, 0), snap(64<<30, 0, warn("numa_imbalance", "NUMA uneven")), nil, nil, Remarks, "memory: NUMA uneven"},
		{"hardware warning fails", snap(64<<30, 0), snap(64<<30, 0, warn("ecc_correctable", "3 CE on DIMM A1")), nil, nil, Fail, "memory: 3 CE on DIMM A1"},
		{"module/code form", snap(64<<30, 0, warn("unclean_shutdown", "crash")), snap(64<<30, 0), nil, nil, Pass, ""},
		{"CE growth fails", snap(64<<30, 0), snap(64<<30, 2), nil, nil, Fail, "counter edac.mc0.ce +2"},
		{"stress fail", snap(64<<30, 0), snap(64<<30, 0),
			[]*stress.Result{{Component: "cpu", Verdict: stress.VerdictFail, Reasons: []string{"1 verification error (wrong results)", "new finding: x"}}}, nil, Fail, "cpu stress: 1 verification error"},
		{"stress aborted", snap(64<<30, 0), snap(64<<30, 0),
			[]*stress.Result{{Component: "memory", Verdict: stress.VerdictAborted, Reasons: []string{"could not run: no memory"}}}, nil, Incomplete, "memory stress"},
		{"not run", snap(64<<30, 0), snap(64<<30, 0), nil, []string{"storage: could not run"}, Incomplete, "storage: could not run"},
		{"too little memory", snap(16<<30, 0), snap(16<<30, 0), nil, nil, Fail, "16 GB installed, at least 32 GB"},
		{"too hot", snap(64<<30, 0), snap(64<<30, 0),
			[]*stress.Result{{Component: "cpu", Verdict: stress.VerdictPass, Metrics: []stress.MetricSummary{{Name: "cpu_temp", Max: 97, Samples: 10}}}}, nil, Fail, "cpu temperature reached 97°C (limit 95°C)"},
	}
	p.Min.MemoryGB = 32
	p.MaxTempC["cpu"] = 95
	p.Ignore = append(p.Ignore, "memory/unclean_shutdown")
	for _, c := range cases {
		r := &Result{}
		Judge(p, r, c.before, c.after, c.runs, c.incomplete)
		all := strings.Join(append(append([]string{}, r.Failures...), r.Remarks...), "\n")
		if r.Verdict != c.want || (c.has != "" && !strings.Contains(all, c.has)) {
			t.Errorf("%s: %s\n%s", c.name, r.Verdict, all)
		}
	}
}

func TestMarkdown(t *testing.T) {
	p, _ := Builtin("quick")
	r := &Result{Verdict: Fail, Profile: p, Machine: Machine{Vendor: "HPE", Product: "ProLiant DL380 Gen10", Serial: "CZ123"},
		Failures: []string{"memory: 3 CE on DIMM A1"}, Steps: []Step{{Name: "cpu", Verdict: "PASS", File: "runs/a-cpu.json"}}}
	md := Markdown(r, nil)
	for _, want := range []string{"# Acceptance test: FAIL", "not fit for production", "| Serial | CZ123 |", "## Failures", "- memory: 3 CE on DIMM A1", "([runs/a-cpu.md](runs/a-cpu.md))", "`pending_sectors`"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in\n%s", want, md)
		}
	}
}
