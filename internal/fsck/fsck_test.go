package fsck

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/hostfs"
)

func lookAll(string) (string, error) { return "/usr/sbin/x", nil }

func sysWith(users ...User) System {
	return System{Root: true, LookPath: lookAll, Users: func([]string) []User { return users }}
}

func argvs(p Plan) []string {
	var out []string
	for _, s := range p.Steps {
		out = append(out, s.String())
	}
	return out
}

func TestPlanUnmounted(t *testing.T) {
	tg := Target{Dev: "sdb1", Path: "/dev/sdb1", FSType: "ext4"}
	p := MakePlan(tg, Check, sysWith())
	if !p.OK() || p.Confirm != "" || p.Unmount || strings.Join(argvs(p), "|") != "e2fsck -f -n /dev/sdb1" {
		t.Fatalf("check %+v", p)
	}
	p = MakePlan(tg, Repair, sysWith())
	if !p.OK() || p.Confirm != "/dev/sdb1" || strings.Join(argvs(p), "|") != "e2fsck -f -y /dev/sdb1" {
		t.Fatalf("repair %+v", p)
	}
	live := sysWith()
	live.Live = true
	if p := MakePlan(tg, Repair, live); p.OK() || !strings.Contains(p.Blockers[0], "live ISO") {
		t.Fatalf("live repair must be blocked: %+v", p)
	}
	if p := MakePlan(tg, Check, live); !p.OK() {
		t.Fatalf("live check must run: %+v", p)
	}
	nonroot := sysWith()
	nonroot.Root = false
	if p := MakePlan(tg, Check, nonroot); p.OK() {
		t.Fatal("check without root")
	}
	missing := sysWith()
	missing.LookPath = func(n string) (string, error) { return "", errors.New("no") }
	if p := MakePlan(Target{Dev: "sdc1", Path: "/dev/sdc1", FSType: "xfs"}, Check, missing); p.OK() || !strings.Contains(p.Blockers[0], "xfsprogs") {
		t.Fatalf("missing tool %+v", p)
	}
}

func TestPlanMounted(t *testing.T) {
	tg := Target{Dev: "sdb1", Path: "/dev/sdb1", FSType: "ext4", Mounts: []Mount{
		{Point: "/srv/data", Root: "/", Opts: "rw,noatime", SuperOpt: "rw,errors=remount-ro"},
		{Point: "/srv/data/www-bind", Root: "/www", Opts: "ro,noatime"},
	}}
	p := MakePlan(tg, Repair, sysWith())
	want := []string{"umount /srv/data/www-bind", "umount /srv/data", "e2fsck -f -y /dev/sdb1",
		"mount -t ext4 -o rw,noatime,errors=remount-ro /dev/sdb1 /srv/data", "mount --bind /srv/data/www /srv/data/www-bind",
		"mount -o remount,bind,ro /srv/data/www-bind"}
	if !p.OK() || !p.Unmount || p.Confirm != "/dev/sdb1" || strings.Join(argvs(p), "|") != strings.Join(want, "|") {
		t.Fatalf("plan\n%s\nwant\n%s\n%+v", strings.Join(argvs(p), "\n"), strings.Join(want, "\n"), p.Blockers)
	}
	if p := MakePlan(tg, Check, sysWith()); p.Confirm != "y" || !p.Unmount {
		t.Fatalf("check of a mounted fs unmounts after a y: %+v", p)
	}
	busy := MakePlan(tg, Check, sysWith(User{PID: 42, Comm: "postgres", Path: "/srv/data/pg/base"}))
	if busy.OK() || !strings.Contains(busy.Blockers[0], "postgres (pid 42)") {
		t.Fatalf("busy %+v", busy.Blockers)
	}
	sub := sysWith()
	sub.Mounts = []Mount{{Point: "/srv/data"}, {Point: "/srv/data/www-bind"}, {Point: "/srv/data/archive"}}
	sub.Swaps = []string{"/srv/data/swapfile"}
	if p := MakePlan(tg, Check, sub); p.OK() || len(p.Blockers) != 2 {
		t.Fatalf("submount and swap file %+v", p.Blockers)
	}
	root := Target{Dev: "md0", Path: "/dev/md0", FSType: "ext4", Mounts: []Mount{{Point: "/", Root: "/"}}}
	if p := MakePlan(root, Check, sysWith()); p.OK() || !strings.Contains(p.Blockers[0], "fsck.mode=force") {
		t.Fatalf("root %+v", p.Blockers)
	}
	lh := Target{Dev: "sdd", Path: "/dev/sdd", FSType: "ext4", Longhorn: "pvc-1", Mounts: []Mount{{Point: "/var/lib/kubelet/plugins/kubernetes.io/csi/driver.longhorn.io/x/globalmount", Root: "/"}}}
	if p := MakePlan(lh, Repair, sysWith()); p.OK() || !strings.Contains(p.Blockers[0], "maintenance") {
		t.Fatalf("longhorn in use %+v", p.Blockers)
	}
	lh.Mounts = nil
	if p := MakePlan(lh, Repair, sysWith()); !p.OK() || len(p.Notes) == 0 {
		t.Fatalf("longhorn in maintenance mode %+v", p)
	}
}

func TestPlanOnline(t *testing.T) {
	b := Target{Dev: "nvme0n1p3", Path: "/dev/nvme0n1p3", FSType: "btrfs", Mounts: []Mount{{Point: "/home", Root: "/"}}}
	p := MakePlan(b, Check, sysWith())
	if !p.OK() || !p.Online || p.Unmount || strings.Join(argvs(p), "|") != "btrfs scrub start -B -d -r /home" {
		t.Fatalf("btrfs check %+v", p)
	}
	if p := MakePlan(b, Repair, sysWith()); !p.OK() || p.Confirm != "/dev/nvme0n1p3" || strings.Join(argvs(p), "|") != "btrfs scrub start -B -d /home" {
		t.Fatalf("btrfs repair %+v", p)
	}
	b.Mounts = nil
	if p := MakePlan(b, Repair, sysWith()); p.OK() {
		t.Fatal("offline btrfs repair must not be offered")
	}
	x := Target{Dev: "sdc1", Path: "/dev/sdc1", FSType: "xfs", Mounts: []Mount{{Point: "/data", Root: "/"}}}
	if p := MakePlan(x, Check, sysWith()); !p.Online || strings.Join(argvs(p), "|") != "xfs_scrub -n -v /data" {
		t.Fatalf("xfs online %+v", p)
	}
}

func TestVerdict(t *testing.T) {
	chk, rep := Step{Kind: "check", Argv: []string{"e2fsck"}}, Step{Kind: "repair", Argv: []string{"e2fsck"}}
	for _, c := range []struct {
		s    Step
		code int
		want string
	}{{chk, 0, "clean"}, {chk, 4, "errors"}, {rep, 1, "fixed"}, {rep, 4, "errors"}, {rep, 8, "failed"},
		{Step{Kind: "check", Argv: []string{"xfs_repair", "-n"}}, 1, "errors"}, {Step{Kind: "repair", Argv: []string{"xfs_repair"}}, 2, "errors"},
		{Step{Kind: "check", Argv: []string{"btrfs", "scrub"}}, 3, "errors"}, {Step{Kind: "repair", Argv: []string{"fsck.vfat"}}, 1, "fixed"}} {
		if got, _ := Verdict("", c.s, c.code); got != c.want {
			t.Errorf("%v exit %d: %s, want %s", c.s.Argv, c.code, got, c.want)
		}
	}
}

func TestParseExtSuper(t *testing.T) {
	b := make([]byte, 1024)
	le := binary.LittleEndian
	le.PutUint16(b[0x38:], 0xEF53)
	le.PutUint16(b[0x3A:], 3) // clean | errors
	le.PutUint32(b[0x40:], 1700000000)
	le.PutUint16(b[0x34:], 17)
	le.PutUint32(b[0x194:], 5)
	le.PutUint32(b[0x1CC:], 1795003065)
	copy(b[0x1E0:], "ext4_lookup")
	s, err := ParseExtSuper(b)
	if err != nil || s.State != "errors" || s.Errors != 5 || s.MountCount != 17 || s.LastCheck.Year() != 2023 ||
		s.LastError != "2026-11-18 11:57 in ext4_lookup" || s.FirstError != "" {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := ParseExtSuper(make([]byte, 1024)); err == nil {
		t.Error("zeros parsed")
	}
}

func TestTargets(t *testing.T) {
	root := t.TempDir()
	put := func(p, s string) {
		p = filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	blk := "sys/devices/pci0000:00/block/"
	put(blk+"sdb/dev", "8:16")
	put(blk+"sdb/size", "1000")
	put(blk+"sdb/sdb1/partition", "1")
	put(blk+"sdb/sdb1/dev", "8:17")
	put(blk+"sdb/sdb1/size", "800")
	put(blk+"sdb/sdb2/partition", "2")
	put(blk+"sdb/sdb2/dev", "8:18")
	put("run/udev/data/b8:17", "E:ID_FS_TYPE=ext4\nE:ID_FS_LABEL=data\n")
	put("run/udev/data/b8:18", "E:ID_FS_TYPE=swap\n")
	put("proc/self/mountinfo", "30 1 8:17 / /srv rw,noatime shared:1 - ext4 /dev/sdb1 rw,errors=remount-ro\n")
	put("dev/sdb1", "")
	for n, d := range map[string]string{"sdb": blk + "sdb", "sdb1": blk + "sdb/sdb1", "sdb2": blk + "sdb/sdb2"} {
		os.MkdirAll(filepath.Join(root, "sys/class/block"), 0o755)
		os.Symlink("../../../"+d, filepath.Join(root, "sys/class/block", n))
	}
	ts := Targets(hostfsNew(root))
	if len(ts) != 1 || ts[0].Path != "/dev/sdb1" || ts[0].Label != "data" || ts[0].SizeBytes != 800*512 || len(ts[0].Mounts) != 1 ||
		ts[0].Mounts[0].Point != "/srv" || ts[0].Mounts[0].SuperOpt != "rw,errors=remount-ro" {
		t.Fatalf("%+v", ts)
	}
}

// TestRun runs plans against stand-in tools: the unmount, the check and
// the mount happen in order; a failed repair leaves the filesystem
// unmounted; a failed unmount mounts back only what it unmounted.
func TestRun(t *testing.T) {
	bin := t.TempDir()
	trace := filepath.Join(bin, "trace")
	script := func(name, body string) {
		os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho \""+name+" $*\" >> "+trace+"\n"+body), 0o755)
	}
	script("umount", `case "$1" in *busy*) echo "target is busy" >&2; exit 32;; esac`)
	script("mount", "exit 0")
	script("e2fsck", "echo 'Pass 1: Checking inodes'\nprintf 'progress 50%%\\rprogress 100%%\\n'\nexit ${E2FSCK_EXIT:-0}")
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Chdir(t.TempDir()) // the log falls back to ./tyr-fsck

	tg := Target{Dev: "sdb1", Path: "/dev/sdb1", FSType: "ext4", Mounts: []Mount{{Point: "/srv", Root: "/", Opts: "rw"}}}
	read := func() string {
		b, _ := os.ReadFile(trace)
		os.Remove(trace)
		return strings.TrimSpace(string(b))
	}
	var lines []string
	res := Run(context.Background(), MakePlan(tg, Check, sysWith()), func(e Event) {
		if e.Line != "" {
			lines = append(lines, e.Line)
		}
	})
	if res.State != "clean" || read() != "umount /srv\ne2fsck -f -n /dev/sdb1\nmount -t ext4 -o rw /dev/sdb1 /srv" || res.Log == "" {
		t.Fatalf("check %+v", res)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "progress 50%\nprogress 100%") {
		t.Errorf("output lines %q", lines)
	}
	if b, _ := os.ReadFile(res.Log); !strings.Contains(string(b), "result: clean") {
		t.Errorf("log %s", b)
	}

	t.Setenv("E2FSCK_EXIT", "4")
	res = Run(context.Background(), MakePlan(tg, Repair, sysWith()), nil)
	if res.State != "errors" || !res.Unmounted || read() != "umount /srv\ne2fsck -f -y /dev/sdb1" {
		t.Fatalf("failed repair must stay unmounted %+v", res)
	}

	t.Setenv("E2FSCK_EXIT", "1")
	if res = Run(context.Background(), MakePlan(tg, Repair, sysWith()), nil); res.State != "fixed" || res.Unmounted {
		t.Fatalf("repair %+v", res)
	}
	read()

	busy := Target{Dev: "sdc1", Path: "/dev/sdc1", FSType: "ext4", Mounts: []Mount{{Point: "/a", Root: "/"}, {Point: "/a/busy-bind", Root: "/x"}}}
	p := MakePlan(busy, Check, sysWith())
	// deepest first: the bind goes, /a is busy (here: the bind's name) — only the bind comes back
	p.Steps[0], p.Steps[1] = Step{Kind: "umount", Argv: []string{"umount", "/a/x-ok"}}, Step{Kind: "umount", Argv: []string{"umount", "/a/busy"}}
	p.Steps = append(p.Steps, Step{Kind: "mount", Argv: []string{"mount", "--bind", "/a/x", "/a/x-ok"}})
	res = Run(context.Background(), p, nil)
	if res.State != "failed" || read() != "umount /a/x-ok\numount /a/busy\nmount --bind /a/x /a/x-ok" {
		t.Fatalf("busy %+v", res)
	}
	_ = time.Second
}

func hostfsNew(root string) hostfs.FS { return hostfs.New(root) }
