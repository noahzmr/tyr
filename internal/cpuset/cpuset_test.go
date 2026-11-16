package cpuset

import (
	"slices"
	"testing"
)

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in   string
		want Set
		str  string
	}{
		{"", Set{}, ""},
		{"0", Set{0}, "0"},
		{"0-3", Set{0, 1, 2, 3}, "0-3"},
		{"0-2,8,10-11\n", Set{0, 1, 2, 8, 10, 11}, "0-2,8,10-11"},
		{"5,1,1,3", Set{1, 3, 5}, "1,3,5"},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("Parse(%q) = %v, want %v", c.in, got, c.want)
		}
		if got.String() != c.str {
			t.Errorf("String(%v) = %q, want %q", got, got.String(), c.str)
		}
	}
	for _, bad := range []string{"a", "3-1", "-1", "1-", "0-999999"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", bad)
		}
	}
}

func TestMinusContains(t *testing.T) {
	s := MustParse("0-7").Minus(MustParse("2,5"))
	if s.String() != "0-1,3-4,6-7" || s.Contains(2) || !s.Contains(6) {
		t.Fatalf("got %v", s)
	}
}
