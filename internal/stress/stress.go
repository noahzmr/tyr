// Package stress runs hardware stress tests under one lifecycle: baseline
// snapshot → load (the test, plus a monitor sampling sensors and counters
// every second and a guard that aborts on dangerous conditions) → after
// snapshot → diff → verdict → JSON result file.
//
// Tests verify their own results (wrong computations, memory mismatches,
// read errors, corrupted network payloads) and report them as VerifyErrors.
// A test can use Tyr's built-in workloads or an external engine
// (stress-ng, mprime, memtester, iperf3) when one is installed.
package stress

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/noahzmr/tyr/internal/model"
)

// Component names a test.
type Component string

const (
	CompCPU     Component = "cpu"
	CompMemory  Component = "memory"
	CompStorage Component = "storage"
	CompNetwork Component = "network"
)

// Components in suite order ("full" runs them in this order).
var Components = []Component{CompCPU, CompMemory, CompStorage, CompNetwork}

// Engine names.
const (
	EngBuiltin   = "builtin"
	EngStressNG  = "stress-ng"
	EngMprime    = "mprime"
	EngMemtester = "memtester"
	EngIperf3    = "iperf3"
	EngSim       = "sim" // --fixtures mode
)

// Config is what the user chose.
type Config struct {
	Component  Component     `json:"component"`
	Engine     string        `json:"engine"`
	Duration   time.Duration `json:"-"`
	DurationMS int64         `json:"duration_ms"`

	CPUs          string `json:"cpus,omitempty"`           // "" = all allowed CPUs, else a cpu list "0-7"
	MemoryPercent int    `json:"memory_percent,omitempty"` // of MemAvailable (memory)
	StorageMode   string `json:"storage_mode,omitempty"`   // "read" (device surface) or "file" (write+verify on a filesystem)
	Target        string `json:"target,omitempty"`         // /dev/sdX, a mountpoint, or peer host:port
	SizeBytes     uint64 `json:"size_bytes,omitempty"`     // storage file test size
	Token         string `json:"-"`                        // net-peer token (not stored)
	Streams       int    `json:"streams,omitempty"`        // network
	Reverse       bool   `json:"reverse,omitempty"`        // network: peer sends

	SimError bool `json:"sim_error,omitempty"` // fixtures: inject one verify error
}

// Plan is everything the confirmation screen shows. Building it has no
// side effects.
type Plan struct {
	Component Component `json:"component"`
	Engine    string    `json:"engine"`
	Scope     []string  `json:"scope"`
	Duration  time.Duration
	Impact    []string `json:"impact"`
	Warnings  []string `json:"warnings"`
	Info      []string `json:"info"` // e.g. memtest86+ instructions
	// ConfirmToken, when set, must be typed by the user (destructive or
	// risky runs). Empty: a plain yes is enough.
	ConfirmToken string `json:"-"`
	// Blocked explains why the test cannot run (missing peer, no space...).
	Blocked string `json:"blocked,omitempty"`
}

// VerifyError is a wrong result: a mismatching computation, memory
// pattern, read error or corrupted payload.
type VerifyError struct {
	Time     time.Time `json:"time"`
	Subject  string    `json:"subject"`  // cpu7, 0x7f12..-0x7f12.., /dev/sdb @ 1.2 TB, stream 3
	Workload string    `json:"workload"` // kernel / pattern / engine
	Expected string    `json:"expected,omitempty"`
	Got      string    `json:"got,omitempty"`
	Detail   string    `json:"detail,omitempty"`
}

// Runtime is what a running test reports through.
type Runtime interface {
	Progress(fraction float64, stage string)
	Metric(name, unit string, v float64)
	VerifyError(e VerifyError)
	// Track registers a resource the engine closes if Run leaks it.
	Track(c io.Closer)
	Logf(format string, args ...any)
}

// Test is one component test with a chosen engine.
type Test interface {
	Plan(cfg Config, env *Env) *Plan
	// Run is the load phase: it must stop within 2s of ctx.Done() and
	// release everything it allocated.
	Run(ctx context.Context, cfg Config, plan *Plan, rt Runtime) error
}

// Verdict of a run.
type Verdict string

const (
	VerdictPass    Verdict = "PASS"
	VerdictWarn    Verdict = "WARN"
	VerdictFail    Verdict = "FAIL"
	VerdictAborted Verdict = "ABORTED"
)

// ExitCode maps a verdict to the stress exit codes (0/1/2/3).
func (v Verdict) ExitCode() int {
	switch v {
	case VerdictPass:
		return 0
	case VerdictWarn:
		return 1
	case VerdictFail:
		return 2
	}
	return 3
}

// Status maps a verdict to a model status (for colors).
func (v Verdict) Status() model.Status {
	switch v {
	case VerdictPass:
		return model.StatusOK
	case VerdictWarn:
		return model.StatusWarn
	case VerdictFail:
		return model.StatusCrit
	}
	return model.StatusUnknown
}

// MetricSummary is min/avg/max of one metric over the run.
type MetricSummary struct {
	Name    string  `json:"name"`
	Unit    string  `json:"unit"`
	Min     float64 `json:"min"`
	Avg     float64 `json:"avg"`
	Max     float64 `json:"max"`
	Last    float64 `json:"last"`
	Samples int     `json:"samples"`
}

func (m *MetricSummary) add(v float64) {
	if m.Samples == 0 || v < m.Min {
		m.Min = v
	}
	if m.Samples == 0 || v > m.Max {
		m.Max = v
	}
	m.Avg = (m.Avg*float64(m.Samples) + v) / float64(m.Samples+1)
	m.Last = v
	m.Samples++
}

// Aborted says who stopped a run early.
type Aborted struct {
	By     string `json:"by"` // user, guard, error
	Reason string `json:"reason"`
}

// Result is the JSON result file (schema docs/DESIGN.md §4.2).
type Result struct {
	SchemaVersion string           `json:"schema_version"`
	TyrVersion    string           `json:"tyr_version"`
	Hostname      string           `json:"hostname"`
	RunID         string           `json:"run_id"`
	Component     Component        `json:"component"`
	Engine        string           `json:"engine"`
	Config        Config           `json:"config"`
	Plan          *Plan            `json:"plan"`
	StartedAt     time.Time        `json:"started_at"`
	EndedAt       time.Time        `json:"ended_at"`
	Verdict       Verdict          `json:"verdict"`
	Aborted       *Aborted         `json:"aborted"`
	Reasons       []string         `json:"reasons"`
	Summary       string           `json:"summary"`
	Metrics       []MetricSummary  `json:"metrics"`
	VerifyErrors  []VerifyError    `json:"verify_errors"`
	CounterDeltas map[string]int64 `json:"counter_deltas"`
	NewFindings   []model.Finding  `json:"new_findings"`
	Log           []string         `json:"log,omitempty"`
	File          string           `json:"-"`
	Baseline      *model.Snapshot  `json:"baseline"`
	After         *model.Snapshot  `json:"after"`
}

// SummaryLine is the one-line result ("cpu stress (builtin): PASS, 5m00s, ...").
func (r *Result) SummaryLine() string {
	return fmt.Sprintf("%s: %s", r.Verdict, r.Summary)
}
