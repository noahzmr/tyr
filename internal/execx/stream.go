package execx

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Stream runs a long-lived tool (stress engines) and calls onLine for every
// stdout ("out") and stderr ("err") line as it arrives. The process runs in
// its own process group; when ctx ends the whole group gets SIGTERM and,
// after grace, SIGKILL, so no helper process survives. dir sets the working
// directory ("" = inherit). The returned error is nil on exit 0, an
// *ExitError otherwise, or ctx.Err() when cancelled.
func Stream(ctx context.Context, dir string, onLine func(stream, line string), name string, args ...string) error {
	path, err := Host().LookPath(name)
	if err != nil {
		return err
	}
	cmd := exec.Command(path, args...)
	cmd.Env = []string{"LC_ALL=C", "LANG=C", "PATH=" + strings.Join(extraPath, ":"), "HOME=" + os.TempDir()}
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	read := func(r io.Reader, stream string) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			mu.Lock()
			onLine(stream, sc.Text())
			mu.Unlock()
		}
	}
	wg.Add(2)
	go read(stdout, "out")
	go read(stderr, "err")

	done := make(chan error, 1)
	go func() {
		wg.Wait() // pipes closed: drain before Wait
		done <- cmd.Wait()
	}()
	select {
	case err = <-done:
	case <-ctx.Done():
		pgid := cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			<-done
		}
		// Children that ignored SIGTERM and outlived the leader.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Cmd: name, Code: ee.ExitCode()}
	}
	return err
}
