package ipmi

import "testing"

func TestMfgDate(t *testing.T) {
	for in, want := range map[string]string{
		"Mon Mar  1 00:00:00 2021 UTC": "2021-03-01",
		"Fri Nov 13 14:00:00 2020":     "2020-11-13",
		"03/01/2021 00:00:00":          "2021-03-01",
		"Mon Jan  1 00:00:00 1996 UTC": "", // the FRU epoch: not set
		"":                             "",
	} {
		if got := mfgDate(in); got != want {
			t.Errorf("mfgDate(%q) = %q, want %q", in, got, want)
		}
	}
}
