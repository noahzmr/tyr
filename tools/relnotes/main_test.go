package main

import (
	"strings"
	"testing"
)

func TestSemver(t *testing.T) {
	for _, c := range [][3]string{{"", "patch", "v1.0.0"}, {"v1.0.0", "patch", "v1.0.1"}, {"v1.4.2", "minor", "v1.5.0"}, {"v1.4.2", "major", "v2.0.0"}} {
		if got, err := Bump(c[0], c[1]); err != nil || got != c[2] {
			t.Errorf("Bump(%s, %s) = %s %v, want %s", c[0], c[1], got, err, c[2])
		}
	}
	if _, err := Bump("v1.0.0", "huge"); err == nil {
		t.Error("unknown bump accepted")
	}
	for _, bad := range []string{"1.0.0", "v1.0", "v01.0.0", "v1.0.0.1"} {
		if _, err := ParseSemver(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if !Less("v1.9.0", "v1.10.0") || !Less("v2.0.0-rc.1", "v2.0.0") || Less("v1.0.0", "v1.0.0") {
		t.Error("ordering")
	}
}

func TestClassify(t *testing.T) {
	for s, want := range map[string][2]string{
		"storage: encryption state of disks":       {"Storage", "encryption state of disks"},
		"fsck: check and repair":                   {"Storage", "check and repair"},
		"tui: read-only file browser (B)":          {"TUI", "read-only file browser (B)"},
		"docs: bring the documentation up to date": {"Documentation", "bring the documentation up to date"},
		"Power supply state, boot history":         {otherArea, "Power supply state, boot history"},
		"weird: something":                         {otherArea, "weird: something"},
		"netbox/sync: tags":                        {"NetBox", "tags"},
	} {
		a, sum := Classify(s)
		if a != want[0] || sum != want[1] {
			t.Errorf("%q: %s / %s", s, a, sum)
		}
	}
}

const cl = `# Changelog

All notable changes; versions follow semantic versioning.

## Unreleased

<!-- comment -->
First stable release.

- encryption state
`

func TestNotesAndRelease(t *testing.T) {
	if h := Unreleased(cl); h != "First stable release.\n\n- encryption state" {
		t.Fatalf("highlights %q", h)
	}
	cs := []Commit{{Hash: "0bf8e9b0000000000000000000000000000000aa", Subject: "storage: SATA wear from the device statistics"},
		{Hash: "cef34470000000000000000000000000000000bb", Subject: "docs: flags and keys"},
		{Hash: "1111111000000000000000000000000000000000", Subject: "Old style subject"}}
	n := Notes(Unreleased(cl), cs, "https://github.com/noahzmr/tyr", "v1.0.0", "v1.1.0")
	for _, want := range []string{"First stable release.", "### Changes", "#### Storage\n\n- SATA wear from the device statistics ([`0bf8e9b`](https://github.com/noahzmr/tyr/commit/0bf8e9b0000000000000000000000000000000aa))",
		"#### Documentation", "#### Other changes\n\n- Old style subject", "[v1.0.0...v1.1.0](https://github.com/noahzmr/tyr/compare/v1.0.0...v1.1.0)"} {
		if !strings.Contains(n, want) {
			t.Errorf("notes lack %q:\n%s", want, n)
		}
	}
	if !releaseCommit.MatchString("release: v1.2.0") || releaseCommit.MatchString("release: tooling for releases") {
		t.Error("release commits")
	}
	if upperFirst("iSCSI sessions") != "iSCSI sessions" || upperFirst("network volumes") != "Network volumes" {
		t.Error("capitalisation")
	}
	if strings.Index(n, "#### Storage") > strings.Index(n, "#### Documentation") {
		t.Error("areas out of order")
	}
	out, err := Release(cl, "v1.1.0", "2026-11-18", n)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "## Unreleased\n\n<!--") || !strings.Contains(out, "## [1.1.0] - 2026-11-18\n\nFirst stable release.") || Unreleased(out) != "" {
		t.Fatalf("changelog:\n%s", out)
	}
	// a second release goes above the first
	out2, err := Release(strings.Replace(out, "## Unreleased\n", "## Unreleased\n\nFix.\n", 1), "v1.1.1", "2026-11-19", "Fix.\n")
	if err != nil || strings.Index(out2, "[1.1.1]") > strings.Index(out2, "[1.1.0]") {
		t.Fatalf("second release %v:\n%s", err, out2)
	}
	if _, err := Release(out, "v1.1.0", "2026-11-18", n); err == nil {
		t.Error("the same version twice")
	}
}
