// Package isobuild builds the Tyr live ISO with the current settings and
// writes it to a USB stick: the TUI's ISO dialog (I).
//
// The build is the source tree's iso/build.sh (Docker, Go, a few minutes);
// the settings go in through TYR_ISO_CONFIG, a temporary directory that is
// removed afterwards. Run as root (sudo tyr) the build runs as the user
// who called sudo, whose Go, Docker access and module cache it needs.
package isobuild

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// FindSource looks for the Tyr source tree (iso/build.sh): TYR_SOURCE,
// the working directory and its parents, the binary's directory and its
// parents (dist/tyr in a checkout).
func FindSource() string {
	var cands []string
	if d := os.Getenv("TYR_SOURCE"); d != "" {
		cands = append(cands, d)
	}
	if wd, err := os.Getwd(); err == nil {
		cands = append(cands, parents(wd)...)
	}
	if exe, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		cands = append(cands, parents(filepath.Dir(exe))...)
	}
	for _, d := range cands {
		if IsSource(d) {
			return d
		}
	}
	return ""
}

func parents(d string) []string {
	var out []string
	for i := 0; i < 4; i++ {
		out = append(out, d)
		p := filepath.Dir(d)
		if p == d {
			break
		}
		d = p
	}
	return out
}

// IsSource reports whether dir is a Tyr source tree with the ISO build.
func IsSource(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "iso", "build.sh"))
	return err == nil
}

// Settings are the files built into the image; nil leaves one out.
type Settings struct {
	NetBox   any // netbox.json
	Tags     any // tags.json
	Webhooks any // webhooks.json
	Limits   any // accept.json
}

// WriteSettings puts the settings into a new private directory (0700, the
// files 0600: tokens) and returns it; remove it after the build.
func WriteSettings(s Settings) (string, error) {
	dir, err := os.MkdirTemp("", "tyr-iso-config-")
	if err != nil {
		return "", err
	}
	for name, v := range map[string]any{"netbox.json": s.NetBox, "tags.json": s.Tags, "webhooks.json": s.Webhooks, "accept.json": s.Limits} {
		if v == nil {
			continue
		}
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0o600); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

// Phases of iso/build.sh, in order, by the line that starts them.
var Phases = []struct{ Mark, Name string }{
	{"== tyr binary", "Tyr binary"},
	{"== build environment", "build environment"},
	{"== image", "starting the build"},
	{"== root filesystem", "Debian root filesystem"},
	{"== squashfs", "squashfs"},
	{"== boot loader", "boot loader and ISO"},
}

// Phase returns the index of the phase a line starts, -1 for none.
func Phase(line string) int {
	for i, p := range Phases {
		if strings.HasPrefix(strings.TrimSpace(line), p.Mark) {
			return i
		}
	}
	return -1
}

var doneRE = regexp.MustCompile(`^== (/src/)?(dist/tyr-live-\S+\.iso)`)

// Build runs iso/build.sh in src for arch with the settings in cfgDir;
// every output line goes to line. It returns the ISO's path.
func Build(ctx context.Context, src, arch, cfgDir, goBin string, line func(string)) (string, error) {
	src, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("tyr-iso-%s-%d", arch, os.Getpid())
	env := []string{"TYR_ISO_CONFIG=" + cfgDir, "TYR_ISO_NAME=" + name}
	cmd := BuildCommand(src, arch, goBin, env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return "", err
	}
	stop := make(chan struct{})
	go func() { // cancel: the container first, then the script
		select {
		case <-ctx.Done():
			exec.Command("docker", "kill", name).Run()
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		case <-stop:
		}
	}()
	iso, lastLine := "", ""
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			l := strings.TrimRight(sc.Text(), "\r")
			if m := doneRE.FindStringSubmatch(l); m != nil {
				iso = filepath.Join(src, m[2])
			}
			if strings.TrimSpace(l) != "" {
				lastLine = strings.TrimSpace(l)
			}
			line(l)
		}
		io.Copy(io.Discard, pr)
	}()
	err = cmd.Wait()
	pw.Close()
	<-scanDone
	close(stop)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		if lastLine != "" {
			return "", fmt.Errorf("iso/build.sh: %w: %s", err, lastLine)
		}
		return "", fmt.Errorf("iso/build.sh: %w", err)
	}
	if iso == "" {
		return "", errors.New("the build ended without naming an ISO")
	}
	return iso, nil
}

// BuildCommand is how iso/build.sh is started: as the user behind sudo,
// with that user's login environment (Go in PATH, Docker group), so the
// image is built as on the command line; directly otherwise.
func BuildCommand(src, arch, goBin string, env []string) *exec.Cmd {
	script := "cd " + shq(src) + " && exec env " + strings.Join(quoteAll(env), " ") + " iso/build.sh " + shq(arch)
	if goBin != "" { // Go's directory first: login shells often lack it (PATH set in ~/.bashrc only)
		script = "export PATH=" + shq(filepath.Dir(goBin)) + `:"$PATH"; ` + script
	}
	if u := os.Getenv("SUDO_USER"); u != "" && u != "root" && os.Geteuid() == 0 {
		return exec.Command("runuser", "-l", u, "-c", script)
	}
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = src
	return cmd
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = shq(s)
	}
	return out
}

// shq quotes for the shell.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Docker reports why Docker cannot be used for the build, "" when it can.
func Docker(ctx context.Context) string {
	if _, err := exec.LookPath("docker"); err != nil {
		return "docker is not installed"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if u := os.Getenv("SUDO_USER"); u != "" && u != "root" && os.Geteuid() == 0 {
		cmd = exec.CommandContext(ctx, "runuser", "-l", u, "-c", "docker info --format '{{.ServerVersion}}'")
	} else {
		cmd = exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if i := strings.IndexByte(msg, '\n'); i > 0 {
			msg = msg[:i]
		}
		return "docker does not answer: " + msg
	}
	return ""
}

// FindGo finds the Go toolchain the build needs: in PATH, in the login
// shell of the user behind sudo, and where Go is usually installed
// (/usr/local/go, ~/sdk/go*, ~/.local/go, /usr/lib/go, /snap/bin). Empty
// when there is none.
func FindGo() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	if u := os.Getenv("SUDO_USER"); u != "" && u != "root" && os.Geteuid() == 0 {
		if out, err := exec.Command("runuser", "-l", u, "-c", "command -v go").Output(); err == nil {
			if p := strings.TrimSpace(string(out)); p != "" {
				return p
			}
		}
		if uu, err := user.Lookup(u); err == nil {
			home = uu.HomeDir
		}
	}
	cands := []string{"/usr/local/go/bin/go"}
	if home != "" {
		sdks, _ := filepath.Glob(filepath.Join(home, "sdk", "go*", "bin", "go"))
		sort.Sort(sort.Reverse(sort.StringSlice(sdks))) // the newest first
		cands = append(cands, sdks...)
		cands = append(cands, filepath.Join(home, ".local", "go", "bin", "go"), filepath.Join(home, "go", "bin", "go"))
	}
	cands = append(cands, "/usr/lib/go/bin/go", "/usr/bin/go", "/snap/bin/go")
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return c
		}
	}
	return ""
}
