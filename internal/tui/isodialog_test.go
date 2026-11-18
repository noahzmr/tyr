package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noahzmr/tyr/internal/isobuild"
)

func TestISODialog(t *testing.T) {
	const w, h = 120, 50
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "iso"), 0o755)
	os.WriteFile(filepath.Join(src, "iso", "build.sh"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("TYR_SOURCE", src)
	root := false
	isoDisks = func() ([]isobuild.Disk, error) {
		return []isobuild.Disk{
			{Name: "sdb", Dev: "/dev/sdb", Vendor: "SanDisk", Model: "Ultra", SizeBytes: 15_400_000_000, Serial: "4C530001", Mounts: []string{"/run/media/me/TYR_LIVE"}},
			{Name: "sdc", Dev: "/dev/sdc", Vendor: "Samsung", Model: "T7", SizeBytes: 500_000_000_000, System: true, Why: "the running system is on it"},
		}, nil
	}
	isoDocker = func() string { return "" }
	isoRoot = func() bool { return root }
	isoGo = func() string { return "/usr/local/go/bin/go" }
	m := newLoaded(t, "server-healthy", w, h)
	dir := t.TempDir()
	m.opts.NetBoxFile, m.opts.TagsFile, m.opts.WebhooksFile = filepath.Join(dir, "n.json"), filepath.Join(dir, "t.json"), filepath.Join(dir, "w.json")
	os.WriteFile(m.opts.NetBoxFile, []byte(`{"url":"https://nb.example.com","token":"tok","site":"Lab"}`), 0o600)
	os.WriteFile(m.opts.WebhooksFile, []byte(`{"webhooks":[{"name":"ops","url":"https://hooks.example.com/x","format":"slack"}]}`), 0o600)

	_, cmd := m.Update(press("I"))
	runCmd(m, cmd) // the Docker check
	if m.iso == nil || m.iso.src != src || !m.iso.netbox || !m.iso.webhooks || m.iso.docker != "" {
		t.Fatalf("dialog %+v", m.iso)
	}
	checkFits(t, "iso form", screen(m), w, h)
	s := stripStyle(screen(m))
	for _, want := range []string{"ISO and USB stick", "https://nb.example.com · site Lab", "ops", "only build the ISO file",
		"sdb  SanDisk Ultra · 15.4 GB", "mounted /run/media/me/TYR_LIVE", "the running system is on it", "Build ISO", "keep the ISO and the stick in-house"} {
		if !strings.Contains(s, want) {
			t.Errorf("form without %q:\n%s", want, s)
		}
	}
	// target: ←→ cycles; the system disk is shown but refused
	for isoFields[m.iso.field] != "target" {
		m.Update(press("down"))
	}
	m.Update(press("right"))
	if m.iso.disk() == nil || m.iso.disk().Name != "sdb" || !strings.Contains(stripStyle(screen(m)), "Build and write to sdb") {
		t.Fatal("right did not choose sdb")
	}
	m.Update(press("right"))
	m.Update(press("down")) // the button
	m.Update(press("enter"))
	if m.iso.run != nil || !strings.Contains(m.iso.err, "running system") {
		t.Errorf("system disk accepted: err %q", m.iso.err)
	}
	// sdb as a user: needs root
	m.Update(press("up"))
	m.Update(press("left"))
	m.Update(press("down"))
	m.Update(press("enter"))
	if m.iso.confirm || !strings.Contains(m.iso.err, "root") {
		t.Errorf("no root required: %q", m.iso.err)
	}
	// as root: asks, n does not start
	root = true
	m.Update(press("enter"))
	if !m.iso.confirm || !strings.Contains(stripStyle(screen(m)), "Erase /dev/sdb (SanDisk Ultra · 15.4 GB) and write the ISO to it? y / n") {
		t.Fatalf("no confirmation:\n%s", screen(m))
	}
	m.Update(press("n"))
	if m.iso.run != nil || m.iso.confirm {
		t.Fatal("n started the build")
	}

	// the run view, fed like the background work
	disk := m.iso.disks[0]
	m.iso.run = &isoRun{arch: "amd64", disk: &disk, stage: "build", cancel: func() {}}
	for _, l := range []string{"== tyr binary (linux/amd64)", "== build environment", "== image", "== root filesystem (Debian trixie, amd64)", "I: downloading packages"} {
		m.isoMsg(isoLineMsg(l))
	}
	checkFits(t, "iso run", screen(m), w, h)
	s = stripStyle(screen(m))
	for _, want := range []string{"Building the Tyr ISO (amd64) for /dev/sdb", "✔ Tyr binary", "Debian root filesystem", "write to sdb", "I: downloading packages", "esc cancels"} {
		if !strings.Contains(s, want) {
			t.Errorf("run view without %q:\n%s", want, s)
		}
	}
	m.isoMsg(isoBuiltMsg(filepath.Join(src, "dist", "tyr-live-x-amd64.iso")))
	m.isoMsg(isoProgMsg{stage: "write", done: 200 << 20, total: 400 << 20})
	if s = stripStyle(screen(m)); !strings.Contains(s, "200 MB of 400 MB") || !strings.Contains(s, "50%") {
		t.Errorf("write progress:\n%s", s)
	}
	m.isoMsg(isoFinishMsg{})
	if s = stripStyle(screen(m)); !strings.Contains(s, "written to /dev/sdb and verified") {
		t.Errorf("finished:\n%s", s)
	}
	m.Update(press("enter"))
	if m.iso != nil {
		t.Error("enter did not close")
	}
	// a failed build shows where
	m.Update(press("I"))
	m.iso.run = &isoRun{arch: "amd64", stage: "build", cancel: func() {}}
	m.isoMsg(isoLineMsg("== root filesystem (Debian trixie, amd64)"))
	m.isoMsg(isoFinishMsg{err: errors.New("iso/build.sh: exit status 1")})
	if s = stripStyle(screen(m)); !strings.Contains(s, "✖ Debian root filesystem") || !strings.Contains(s, "exit status 1") {
		t.Errorf("failed build:\n%s", s)
	}
}
