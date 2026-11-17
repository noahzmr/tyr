package netbox

import (
	"strings"
	"testing"
)

func TestExpr(t *testing.T) {
	f := Facts{}
	for _, d := range FactDefs {
		f[d.Name] = Value{K: d.Kind}
	}
	f["memory_gb"], f["cores"], f["threads"] = Num(256), Num(32), Num(64)
	f["model"], f["verdict"], f["virtual"] = Str("ProLiant DL360 Gen10"), Str("PASS"), Bool(false)
	f["findings"] = Str("link_down pending_sectors")
	cases := []struct {
		src  string
		want bool
	}{
		{`memory_gb >= 192`, true},
		{`memory_gb > 256`, false},
		{`cores >= 16 && threads >= 32`, true},
		{`cores >= 64 || threads >= 64`, true},
		{`!(cores >= 64)`, true},
		{`verdict == "pass"`, true}, // case-insensitive
		{`verdict != "FAIL"`, true},
		{`model ~ "DL3[68]0 Gen1[01]"`, true},
		{`model !~ "Gen11"`, true},
		{`findings ~ "\bpending_sectors\b"`, true},
		{`!virtual`, true},
		{`virtual == false`, true},
		{`memory_gb >= 192 && (model ~ 'dl360' || cores > 100) && verdict == "PASS"`, true},
		{`gpus`, false},
		{`hostname`, false},
	}
	for _, c := range cases {
		e, err := Parse(c.src)
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		got, err := e.Eval(f)
		if err != nil || got != c.want {
			t.Errorf("%s = %v, %v; want %v", c.src, got, err, c.want)
		}
	}
	bad := map[string]string{
		``:                      "empty",
		`memory_gb >=`:          "incomplete",
		`memory = 1`:            "use ==",
		`memroy_gb > 1`:         "unknown fact",
		`memory_gb > "a lot"`:   "cannot compare",
		`model ~ cores`:         "pattern in quotes",
		`(cores > 1`:            "missing )",
		`cores > 1 cores`:       "unexpected",
		`model == "x`:           "not closed",
		`model ~ "["`:           "pattern",
		`cores > 1 && && x > 1`: "unexpected",
	}
	for src, want := range bad {
		if _, err := Parse(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) = %v, want error with %q", src, err, want)
		}
	}
}

func TestTagsValidate(t *testing.T) {
	ok := Tags{Rules: []TagRule{{"hw-memory", "memory_gb >= 192"}, {"Defect", `verdict == "FAIL" || failed_parts > 0`}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Tags{
		{Rules: []TagRule{{"", "cores > 1"}}},
		{Rules: []TagRule{{"a", "cores > 1"}, {"A", "cores > 2"}}},
		{Rules: []TagRule{{"x", "nope > 1"}}},
	} {
		if bad.Validate() == nil {
			t.Errorf("%+v: no error", bad.Rules)
		}
	}
}

func TestExpandAndSlug(t *testing.T) {
	f := Facts{"memory_gb": Num(384), "cores": Num(24)}
	if got := Expand("{memory_gb} GB / {cores} cores / {unknown}", f); got != "384 GB / 24 cores / {unknown}" {
		t.Error(got)
	}
	for in, want := range map[string]string{"HW Memory": "hw-memory", " Ünïcode!! tag ": "n-code-tag", "a_b": "a_b", "--x--": "x"} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
