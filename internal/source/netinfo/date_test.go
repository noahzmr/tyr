package netinfo

import "testing"

func TestModuleDate(t *testing.T) {
	for in, want := range map[string]string{"210314": "2021-03-14", "21031401": "2021-03-14", "2103": "", "21AB14": "", "211399": ""} {
		if got := moduleDate(in); got != want {
			t.Errorf("moduleDate(%q) = %q, want %q", in, got, want)
		}
	}
}
