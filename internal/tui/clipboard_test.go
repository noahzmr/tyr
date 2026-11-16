package tui

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestMain keeps every test away from the desktop: no clipboard tool is
// ever run, the environment looks like a plain terminal.
func TestMain(m *testing.M) {
	getenv = func(string) string { return "" }
	runTool = func(clipboardTool, string) error { return errors.New("no clipboard tools in tests") }
	os.Exit(m.Run())
}

// fakeDesktop sets the environment and the installed tools for one test.
func fakeDesktop(t *testing.T, env map[string]string, tools ...string) *[]string {
	t.Helper()
	oldEnv, oldLook, oldRun := getenv, lookPath, runTool
	t.Cleanup(func() { getenv, lookPath, runTool = oldEnv, oldLook, oldRun })
	getenv = func(k string) string { return env[k] }
	lookPath = func(name string) (string, error) {
		for _, x := range tools {
			if x == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", errors.New("not found")
	}
	var ran []string
	runTool = func(c clipboardTool, text string) error {
		ran = append(ran, c.name+" "+strings.Join(c.args, " ")+" <- "+text)
		return nil
	}
	return &ran
}

// runAll executes a (batched) command and returns the messages.
func runAll(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if b, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, runAll(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func TestCopyUsesTheDesktopClipboardLocally(t *testing.T) {
	// Ptyxis on GNOME Wayland with only xclip (this machine): xclip, via XWayland
	ran := fakeDesktop(t, map[string]string{"VTE_VERSION": "8400", "WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"}, "xclip")
	cmd, msg := copyCmd("hello", "the report")
	if !strings.HasPrefix(msg, "copying the report") {
		t.Errorf("message %q", msg)
	}
	var status string
	for _, m := range runAll(cmd) {
		if s, ok := m.(statusMsg); ok {
			status = string(s)
		}
	}
	if len(*ran) != 1 || (*ran)[0] != "xclip -selection clipboard -in <- hello" || status != "copied the report to the clipboard" {
		t.Errorf("ran %v, status %q", *ran, status)
	}
	// wl-copy wins on Wayland when installed
	ran = fakeDesktop(t, map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"}, "xclip", "wl-copy")
	cmd, _ = copyCmd("x", "y")
	runAll(cmd)
	if len(*ran) != 1 || !strings.HasPrefix((*ran)[0], "wl-copy ") {
		t.Errorf("wayland: %v", *ran)
	}
	// a failing tool is reported
	fakeDesktop(t, map[string]string{"DISPLAY": ":0"}, "xsel")
	runTool = func(clipboardTool, string) error { return errors.New("can't open display") }
	cmd, _ = copyCmd("x", "the S/N")
	var got string
	for _, m := range runAll(cmd) {
		if s, ok := m.(statusMsg); ok {
			got = string(s)
		}
	}
	if got != "copy failed: xsel: can't open display" {
		t.Errorf("failure %q", got)
	}
}

func TestCopyOverSSHAndWithoutTools(t *testing.T) {
	// over SSH no tool runs (it would fill the remote clipboard): OSC 52 only
	ran := fakeDesktop(t, map[string]string{"SSH_CONNECTION": "1 2 3 4", "DISPLAY": ":0"}, "xclip")
	cmd, msg := copyCmd("x", "the report")
	runAll(cmd)
	if len(*ran) != 0 || msg != "copied the report via OSC 52" {
		t.Errorf("ssh: ran %v, %q", *ran, msg)
	}
	// … and a VTE terminal on the far side is named
	fakeDesktop(t, map[string]string{"SSH_TTY": "/dev/pts/1", "VTE_VERSION": "8400"})
	if _, msg := copyCmd("x", "the report"); !strings.Contains(msg, "ignores it: save it instead") {
		t.Errorf("ssh vte: %q", msg)
	}
	// local VTE without any tool cannot copy and says why
	fakeDesktop(t, map[string]string{"VTE_VERSION": "8400", "DISPLAY": ":0"})
	if _, msg := copyCmd("x", "the report"); !strings.HasPrefix(msg, "cannot copy the report") || !strings.Contains(msg, "xclip") {
		t.Errorf("vte without tools: %q", msg)
	}
	// tmux needs set-clipboard
	fakeDesktop(t, map[string]string{"TMUX": "/tmp/tmux"})
	if _, msg := copyCmd("x", "the report"); !strings.Contains(msg, "set-clipboard on") {
		t.Errorf("tmux: %q", msg)
	}
}

// TestCopyLiveDesktop copies through the real desktop tool and reads the
// clipboard back (TYR_CLIP_LIVE=1; restores the previous contents).
func TestCopyLiveDesktop(t *testing.T) {
	if os.Getenv("TYR_CLIP_LIVE") == "" {
		t.Skip("set TYR_CLIP_LIVE=1 on a desktop session")
	}
	getenv = os.Getenv
	defer func() { getenv = func(string) string { return "" } }()
	tool, ok := localClipboardTool()
	if !ok {
		t.Skip("no local clipboard tool")
	}
	prev, _ := exec.Command("xclip", "-selection", "clipboard", "-out").Output()
	defer func() { _ = runClipboardTool(tool, string(prev)) }()
	start := time.Now()
	if err := runClipboardTool(tool, "tyr clipboard check"); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	got, err := exec.Command("xclip", "-selection", "clipboard", "-out").Output()
	if err != nil || string(got) != "tyr clipboard check" || took > time.Second {
		t.Errorf("%s: read back %q (%v) after %v", tool.name, got, err, took)
	}
	t.Logf("%s copied in %v", tool.name, took)
}
