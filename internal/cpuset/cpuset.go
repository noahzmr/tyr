// Package cpuset parses and formats Linux CPU list strings such as "0-15,32-47".
package cpuset

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Set is a sorted set of CPU (or NUMA node) IDs.
type Set []int

// Parse parses a kernel list string ("0-3,8,10-11"). Empty input yields an empty set.
func Parse(s string) (Set, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Set{}, nil
	}
	var out Set
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil || a < 0 {
			return nil, fmt.Errorf("cpuset: bad element %q", part)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil || b < a {
				return nil, fmt.Errorf("cpuset: bad range %q", part)
			}
		}
		if b-a > 1<<16 {
			return nil, fmt.Errorf("cpuset: range too large %q", part)
		}
		for i := a; i <= b; i++ {
			out = append(out, i)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// MustParse is Parse that panics; for tests and constants.
func MustParse(s string) Set {
	set, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return set
}

// Contains reports whether id is in the set.
func (s Set) Contains(id int) bool {
	_, ok := slices.BinarySearch(s, id)
	return ok
}

// Minus returns the elements of s not in o.
func (s Set) Minus(o Set) Set {
	out := Set{}
	for _, v := range s {
		if !o.Contains(v) {
			out = append(out, v)
		}
	}
	return out
}

// String formats the set in kernel list notation.
func (s Set) String() string {
	var b strings.Builder
	for i := 0; i < len(s); {
		j := i
		for j+1 < len(s) && s[j+1] == s[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		if i == j {
			fmt.Fprintf(&b, "%d", s[i])
		} else {
			fmt.Fprintf(&b, "%d-%d", s[i], s[j])
		}
		i = j + 1
	}
	return b.String()
}
