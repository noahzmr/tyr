package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/noahzmr/tyr/internal/fsck"
)

// drain feeds the run's messages to the model until the run ends.
func drain(m *Model) {
	for msg := range m.fsck.run.ch {
		m.Update(msg)
	}
}

func TestFsckDialog(t *testing.T) {
	const w, h = 120, 40
	fsckHostOnly = false
	t.Cleanup(func() { fsckHostOnly = true })
	fsckTargets = func() []fsck.Target {
		return []fsck.Target{
			{Dev: "sdc1", Path: "/dev/sdc1", FSType: "xfs", SizeBytes: 4e12, Label: "archive"},
			{Dev: "sdb1", Path: "/dev/sdb1", FSType: "ext4", SizeBytes: 2e12, Mounts: []fsck.Mount{{Point: "/srv", Root: "/", Opts: "rw"}},
				Ext: &fsck.ExtSuper{State: "errors", Errors: 3, LastError: "2026-11-18 11:57 in ext4_lookup"}},
		}
	}
	fsckSystem = func() fsck.System {
		return fsck.System{Root: true, LookPath: func(string) (string, error) { return "/sbin/x", nil }, Users: func([]string) []fsck.User { return nil }}
	}
	release := make(chan struct{})
	var ran []fsck.Plan
	fsckRunner = func(ctx context.Context, p fsck.Plan, emit func(fsck.Event)) fsck.Result {
		ran = append(ran, p)
		emit(fsck.Event{Line: "$ " + p.Steps[0].String()})
		emit(fsck.Event{Line: "Pass 1: Checking inodes, blocks, and sizes", Step: 1})
		select {
		case <-release:
		case <-ctx.Done():
			return fsck.Result{State: "cancelled", Text: "cancelled"}
		}
		if p.Mode == fsck.Repair {
			return fsck.Result{State: "fixed", Text: "errors corrected", Log: "/var/lib/tyr/fsck/x.log"}
		}
		return fsck.Result{State: "clean", Text: "no errors"}
	}
	m := newLoaded(t, "server-healthy", w, h)
	m.Update(press("4"))
	m.Update(press("K"))
	s := screen(m)
	if m.fsck == nil || !strings.Contains(s, "/dev/sdc1") || !strings.Contains(s, "errors (3)") || !strings.Contains(s, "/srv") {
		t.Fatalf("list:\n%s", s)
	}
	checkFits(t, "fsck list", s, w, h)

	// repair of the mounted ext4: unmount, e2fsck -y, mount; typed confirmation
	m.Update(press("down"))
	m.Update(press("r"))
	s = screen(m)
	if !strings.Contains(s, "umount /srv") || !strings.Contains(s, "e2fsck -f -y /dev/sdb1") || !strings.Contains(s, "Type /dev/sdb1") {
		t.Fatalf("repair plan:\n%s", s)
	}
	checkFits(t, "fsck plan", s, w, h)
	m.Update(press("enter"))
	if m.fsck.run != nil || !strings.Contains(m.fsck.msg, "exactly") {
		t.Fatal("a repair must not start without the typed device path")
	}
	m.Update(tea.PasteMsg{Content: "/dev/sdb1"})
	m.Update(press("enter"))
	if m.fsck.run == nil {
		t.Fatalf("not started: %q", m.fsck.msg)
	}
	// ctrl+c and esc do not stop a repair, nor quit Tyr
	if cmd := m.handleKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); cmd != nil || m.fsck == nil || m.fsck.run.res != nil {
		t.Fatal("ctrl+c during a repair")
	}
	m.Update(press("esc"))
	close(release)
	drain(m)
	s = screen(m)
	if !strings.Contains(s, "FIXED") || !strings.Contains(s, "Pass 1") || !strings.Contains(s, "x.log") {
		t.Fatalf("done:\n%s", s)
	}
	checkFits(t, "fsck done", s, w, h)

	// a check of the unmounted xfs starts with enter; esc stops it
	m.Update(press("esc"))
	m.Update(press("up"))
	release = make(chan struct{})
	m.Update(press("c"))
	if !strings.Contains(screen(m), "xfs_repair -n /dev/sdc1") {
		t.Fatalf("check plan:\n%s", screen(m))
	}
	m.Update(press("enter"))
	m.Update(press("esc"))
	drain(m)
	if m.fsck.run.res == nil || m.fsck.run.res.State != "cancelled" || len(ran) != 2 || ran[1].Mode != fsck.Check {
		t.Fatalf("cancelled check %+v", m.fsck.run.res)
	}
}
