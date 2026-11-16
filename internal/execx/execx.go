// Package execx runs external tools safely: no shell, a context deadline,
// a C locale, a capped output size, and the whole process group killed on
// cancel. A replay implementation serves recorded output for fixtures.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrNotFound is returned by LookPath when a tool is not installed.
var ErrNotFound = errors.New("not installed")

// ErrNotRecorded is returned by the replay runner for unknown commands.
var ErrNotRecorded = errors.New("command not recorded in fixture")

// MaxOutput caps captured stdout/stderr per command.
const MaxOutput = 16 << 20

// Runner executes external commands.
type Runner interface {
	// LookPath returns the absolute path of a tool or ErrNotFound.
	LookPath(name string) (string, error)
	// Run executes name with args and returns stdout. A non-zero exit
	// returns *ExitError, which also carries stdout and stderr.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExitError is returned when a command exits non-zero.
type ExitError struct {
	Cmd    string
	Code   int
	Stdout []byte
	Stderr []byte
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(string(e.Stderr))
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if msg == "" {
		return fmt.Sprintf("%s: exit status %d", e.Cmd, e.Code)
	}
	return fmt.Sprintf("%s: exit status %d: %s", e.Cmd, e.Code, msg)
}

// Host returns a Runner for the live system.
func Host() Runner { return hostRunner{} }

type hostRunner struct{}

// extraPath is searched in addition to $PATH; non-root users often lack sbin.
var extraPath = []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin", "/opt/MegaRAID/storcli", "/opt/MegaRAID/perccli", "/opt/dell/srvadmin/bin"}

func (hostRunner) LookPath(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, d := range extraPath {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: %w", name, ErrNotFound)
}

type capped struct {
	bytes.Buffer
	truncated bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := MaxOutput - c.Len(); room < len(p) {
		if room > 0 {
			c.Buffer.Write(p[:room])
		}
		c.truncated = true
		return len(p), nil
	}
	return c.Buffer.Write(p)
}

func (h hostRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	path, err := h.LookPath(name)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"LC_ALL=C", "LANG=C", "PATH=" + strings.Join(extraPath, ":")}
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill the whole process group so helpers spawned by the tool die too.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr capped
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		return stdout.Bytes(), fmt.Errorf("%s: %w", name, ctx.Err())
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return stdout.Bytes(), &ExitError{Cmd: name, Code: ee.ExitCode(), Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	}
	if err != nil {
		return stdout.Bytes(), fmt.Errorf("%s: %w", name, err)
	}
	return stdout.Bytes(), nil
}

// Recorded is one recorded command for the replay runner.
type Recorded struct {
	Stdout []byte
	Stderr []byte
	Exit   int
}

// Replay serves recorded command output. Keys of Commands are the argv
// joined with single spaces, e.g. "smartctl -j -a /dev/sda".
type Replay struct {
	Tools    map[string]string // tool name -> path; absent = not installed
	Commands map[string]Recorded
}

func (r *Replay) LookPath(name string) (string, error) {
	if p, ok := r.Tools[name]; ok {
		return p, nil
	}
	return "", fmt.Errorf("%s: %w", name, ErrNotFound)
}

func (r *Replay) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if _, err := r.LookPath(name); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := strings.Join(append([]string{name}, args...), " ")
	rec, ok := r.Commands[key]
	if !ok {
		return nil, fmt.Errorf("%q: %w", key, ErrNotRecorded)
	}
	if rec.Exit != 0 {
		return rec.Stdout, &ExitError{Cmd: name, Code: rec.Exit, Stdout: rec.Stdout, Stderr: rec.Stderr}
	}
	return rec.Stdout, nil
}
