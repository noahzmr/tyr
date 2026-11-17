// Package fixture loads a recorded host (sysfs/procfs tree plus recorded
// command output) so collectors, report and TUI run without real hardware.
//
// Layout of a fixture directory:
//
//	manifest.json   metadata, privileges, tools, recorded commands, simulation
//	root/           /proc, /sys, /dev subset (symlinks preserved, relative)
//	cmd/            recorded stdout/stderr of external commands
package fixture

import (
	"encoding/json"
	"fmt"
	"github.com/noahzmr/tyr/internal/source/redfish"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/execx"
	"github.com/noahzmr/tyr/internal/hostfs"
)

// Manifest describes a fixture.
type Manifest struct {
	Description    string            `json:"description"`
	Hostname       string            `json:"hostname"`
	Arch           string            `json:"arch"`
	Virtualization string            `json:"virtualization"`
	Now            time.Time         `json:"now"`
	Privileges     Privileges        `json:"privileges"`
	Tools          map[string]string `json:"tools"`
	Commands       []Command         `json:"commands"`
	Sim            collector.Sim     `json:"sim"`
}

// Privileges simulates what the recorded process could access.
type Privileges struct {
	Root bool `json:"root"`
	DMI  bool `json:"dmi"`
	Kmsg bool `json:"kmsg"`
	IPMI bool `json:"ipmi"`
}

// Command is one recorded command.
type Command struct {
	Argv   []string `json:"argv"`
	Stdout string   `json:"stdout,omitempty"` // file relative to the fixture dir
	Stderr string   `json:"stderr,omitempty"` // inline text
	Exit   int      `json:"exit"`
}

// Load reads a fixture and returns a collection environment for it.
func Load(dir string, opts collector.Options) (*collector.Env, *Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("fixture: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, nil, fmt.Errorf("fixture %s: manifest: %w", dir, err)
	}
	rp := &execx.Replay{Tools: m.Tools, Commands: map[string]execx.Recorded{}}
	if rp.Tools == nil {
		rp.Tools = map[string]string{}
	}
	for _, c := range m.Commands {
		if len(c.Argv) == 0 {
			return nil, nil, fmt.Errorf("fixture %s: command with empty argv", dir)
		}
		rec := execx.Recorded{Exit: c.Exit, Stderr: []byte(c.Stderr)}
		if c.Stdout != "" {
			if rec.Stdout, err = os.ReadFile(filepath.Join(dir, c.Stdout)); err != nil {
				return nil, nil, fmt.Errorf("fixture %s: %w", dir, err)
			}
		}
		rp.Commands[strings.Join(c.Argv, " ")] = rec
	}
	now := m.Now
	nowFn := time.Now
	if !now.IsZero() {
		// Deterministic clock: advances only with real elapsed time so
		// durations stay sensible while timestamps are reproducible.
		base := time.Now()
		nowFn = func() time.Time { return now.Add(time.Since(base)) }
	}
	env := &collector.Env{
		FS:        hostfs.New(filepath.Join(dir, "root")),
		Exec:      rp,
		Now:       nowFn,
		Opts:      opts,
		Priv:      collector.Privileges(m.Privileges),
		Virt:      m.Virtualization,
		Arch:      m.Arch,
		Hostname:  m.Hostname,
		Simulated: true,
		Sim:       m.Sim,
	}
	if env.Virt == "" {
		env.Virt = "none"
	}
	if hostfs.Exists(hostfs.New(dir), "/redfish/v1/index.json") {
		env.Redfish = redfish.FileGetter{Root: dir}
	}
	return env, &m, nil
}
