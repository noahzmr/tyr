// Package model holds the data types shared by collectors, stress tests,
// reports and the TUI. It must not import any other internal package.
package model

import "fmt"

// SchemaVersion is the version of the JSON schema written by report and stress.
// Additive changes bump the minor version, renames/removals bump the major.
const SchemaVersion = "1.0"

// Status is the health of an item, module or the whole system.
// The numeric order is the severity order used for rollup: Worst = max.
type Status uint8

const (
	StatusOK Status = iota
	StatusUnknown
	StatusWarn
	StatusCrit
)

var statusNames = [...]string{"OK", "UNKNOWN", "WARN", "CRIT"}

func (s Status) String() string {
	if int(s) < len(statusNames) {
		return statusNames[s]
	}
	return fmt.Sprintf("Status(%d)", s)
}

// Nagios returns the Nagios/Icinga plugin word for the status.
func (s Status) Nagios() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusWarn:
		return "WARNING"
	case StatusCrit:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

// ExitCode returns the Nagios-compatible exit code (0 OK, 1 WARN, 2 CRIT, 3 UNKNOWN).
func (s Status) ExitCode() int {
	switch s {
	case StatusOK:
		return 0
	case StatusWarn:
		return 1
	case StatusCrit:
		return 2
	default:
		return 3
	}
}

// Worst returns the most severe of the given statuses (OK for none).
func Worst(ss ...Status) Status {
	w := StatusOK
	for _, s := range ss {
		if s > w {
			w = s
		}
	}
	return w
}

func (s Status) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

func (s *Status) UnmarshalText(b []byte) error {
	for i, n := range statusNames {
		if n == string(b) {
			*s = Status(i)
			return nil
		}
	}
	return fmt.Errorf("unknown status %q", b)
}

// Availability describes whether a data source (or an item) could be read.
// It never takes part in the status rollup.
type Availability string

const (
	AvailOK            Availability = "ok"
	AvailUnavailable   Availability = "unavailable"    // tool missing, permission denied, error
	AvailNotApplicable Availability = "not_applicable" // e.g. IPMI in a VM
	AvailSkipped       Availability = "skipped"        // disabled by flag (--no-ipmi)
)
