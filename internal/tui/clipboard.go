package tui

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Copying to the clipboard. OSC 52 (tea.SetClipboard) is always sent: it
// is the only way that works over SSH, and kitty, WezTerm, foot, iTerm2,
// Windows Terminal and tmux (set-clipboard on) honour it. VTE terminals
// (GNOME Terminal, Ptyxis, Tilix) ignore it, so in a local session the
// text also goes to the desktop's clipboard tool. Over SSH a tool would
// fill the remote host's clipboard, so none is run there.

// clipboardTool is a command that reads the text on stdin.
type clipboardTool struct {
	name string
	args []string
}

var (
	lookPath = exec.LookPath
	getenv   = os.Getenv
	runTool  = runClipboardTool
)

// runClipboardTool runs a clipboard tool with the text on stdin. xclip and
// wl-copy fork a child that keeps serving the clipboard: it must not hold
// a pipe of ours, or Run would wait for it, so stdout and stderr go to
// /dev/null.
func runClipboardTool(t clipboardTool, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, t.name, t.args...)
	c.Stdin = strings.NewReader(text)
	return c.Run()
}

// remoteSession reports an SSH session: the desktop is elsewhere.
func remoteSession() bool { return getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" }

// localClipboardTool picks the desktop clipboard tool: wl-copy on Wayland,
// else xclip or xsel on X11 (GNOME and KDE bridge the X11 clipboard to
// Wayland apps). ok is false over SSH or without a display or a tool.
func localClipboardTool() (clipboardTool, bool) {
	if remoteSession() {
		return clipboardTool{}, false
	}
	var cands []clipboardTool
	if getenv("WAYLAND_DISPLAY") != "" {
		cands = append(cands, clipboardTool{"wl-copy", nil})
	}
	if getenv("DISPLAY") != "" {
		cands = append(cands, clipboardTool{"xclip", []string{"-selection", "clipboard", "-in"}},
			clipboardTool{"xsel", []string{"--clipboard", "--input"}})
	}
	for _, c := range cands {
		if _, err := lookPath(c.name); err == nil {
			return c, true
		}
	}
	return clipboardTool{}, false
}

// vteTerminal: GNOME Terminal, Ptyxis, Tilix and other VTE terminals do
// not support OSC 52.
func vteTerminal() bool { return getenv("VTE_VERSION") != "" }

// copyCmd copies text: OSC 52 and, locally, the desktop tool. what names
// the copied thing for the footer ("the report (Markdown, 23 kB)").
func copyCmd(text, what string) (tea.Cmd, string) {
	osc := tea.SetClipboard(text)
	if tool, ok := localClipboardTool(); ok {
		run := func() tea.Msg {
			if err := runTool(tool, text); err != nil {
				return statusMsg("copy failed: " + tool.name + ": " + err.Error())
			}
			return statusMsg("copied " + what + " to the clipboard")
		}
		return tea.Batch(osc, run), "copying " + what + " …"
	}
	switch {
	case remoteSession() && vteTerminal():
		return osc, "sent " + what + " via OSC 52, but this terminal (VTE: GNOME Terminal, Ptyxis) ignores it: save it instead"
	case vteTerminal():
		return osc, "cannot copy " + what + ": this terminal (VTE) ignores OSC 52 and no wl-copy, xclip or xsel is installed"
	}
	msg := "copied " + what + " via OSC 52"
	if getenv("TMUX") != "" {
		msg += " (tmux needs set -g set-clipboard on)"
	}
	return osc, msg
}
