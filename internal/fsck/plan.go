package fsck

import (
	"fmt"
	"sort"
	"strings"
)

// Mode is what the plan does to the filesystem.
type Mode int

const (
	Check  Mode = iota // read only
	Repair             // writes
)

func (m Mode) String() string { return map[Mode]string{Check: "check", Repair: "repair"}[m] }

// Step is one command of a plan.
type Step struct {
	Kind string // umount, check, repair, mount
	Argv []string
}

func (s Step) String() string { return strings.Join(s.Argv, " ") }

// Plan is what Tyr would run, or why it cannot.
type Plan struct {
	Target   Target
	Mode     Mode
	Online   bool // runs on the mounted filesystem (btrfs scrub, xfs_scrub)
	Unmount  bool // unmounts, runs the tool, mounts again
	Steps    []Step
	Blockers []string // nothing runs while any is set
	Notes    []string
	// Confirm is what the user types before it runs: "" (enter is
	// enough), "y" (unmounting for a check), the device path (repair).
	Confirm string
}

// OK reports whether the plan can run.
func (p Plan) OK() bool { return len(p.Blockers) == 0 && len(p.Steps) > 0 }

// User is a process that keeps a filesystem busy.
type User struct {
	PID  int
	Comm string
	Path string
}

// System is what a plan depends on outside the target.
type System struct {
	Root     bool
	Live     bool
	LookPath func(string) (string, error)
	Mounts   []Mount // all mounts of the host
	// Users returns the processes using files below any of the paths.
	Users func(paths []string) []User
	// Swaps are the active swap areas; Loops the files behind loop devices.
	Swaps []string
	Loops []string
}

// tool returns the check / repair command for an unmounted filesystem.
func tool(fstype string, mode Mode, dev string) []string {
	switch fstype {
	case "ext2", "ext3", "ext4":
		if mode == Check {
			return []string{"e2fsck", "-f", "-n", dev}
		}
		return []string{"e2fsck", "-f", "-y", dev}
	case "xfs":
		if mode == Check {
			return []string{"xfs_repair", "-n", dev}
		}
		return []string{"xfs_repair", dev}
	case "btrfs":
		if mode == Check {
			return []string{"btrfs", "check", "--readonly", dev}
		}
	case "vfat":
		if mode == Check {
			return []string{"fsck.vfat", "-n", dev}
		}
		return []string{"fsck.vfat", "-a", "-w", dev}
	case "exfat":
		if mode == Check {
			return []string{"fsck.exfat", "-n", dev}
		}
		return []string{"fsck.exfat", "-y", dev}
	}
	return nil
}

// systemMounts cannot be unmounted on a running system.
var systemMounts = map[string]bool{"/": true, "/usr": true, "/var": true, "/etc": true, "/var/log": true, "/run": true}

// MakePlan works out how to check or repair the target.
func MakePlan(t Target, mode Mode, sys System) Plan {
	p := Plan{Target: t, Mode: mode}
	block := func(s string, a ...any) { p.Blockers = append(p.Blockers, fmt.Sprintf(s, a...)) }
	if Supported[t.FSType] == "" {
		block("%s filesystems are not supported (ext2/3/4, xfs, btrfs, vfat, exfat)", t.FSType)
		return p
	}
	if !sys.Root {
		block("needs root: run sudo tyr")
	}
	if mode == Repair && sys.Live {
		block("the live ISO never writes to the server's disks: check only; repair after booting the installed system")
	}

	// mounted btrfs and xfs check online, btrfs repairs online (scrub
	// rewrites bad copies from good ones)
	if t.Mounted() {
		mp := t.Mounts[0].Point
		switch {
		case t.FSType == "btrfs":
			args := []string{"btrfs", "scrub", "start", "-B", "-d"}
			if mode == Check {
				args = append(args, "-r")
			} else {
				p.Notes = append(p.Notes, "a scrub repairs only what a second copy exists for (RAID1, DUP metadata); data on a single copy is reported, not fixed")
			}
			p.Online, p.Steps = true, []Step{{Kind: mode.String(), Argv: append(args, mp)}}
			p.need(sys, "btrfs")
			if mode == Repair {
				p.Confirm = t.Path
			}
			return p
		case t.FSType == "xfs" && mode == Check && have(sys, "xfs_scrub"):
			p.Online, p.Steps = true, []Step{{Kind: "check", Argv: []string{"xfs_scrub", "-n", "-v", mp}}}
			p.Notes = append(p.Notes, "xfs_scrub checks the mounted filesystem; it needs a kernel with online scrub (CONFIG_XFS_ONLINE_SCRUB)")
			return p
		}
	}
	if t.FSType == "btrfs" && mode == Repair {
		block("btrfs is not repaired offline: btrfs check --repair can make the damage worse (its own documentation warns). " +
			"Mount it and run a repair (a scrub), or restore from backup")
		return p
	}

	argv := tool(t.FSType, mode, t.Path)
	p.Steps = append(p.Steps, Step{Kind: mode.String(), Argv: argv})
	p.need(sys, argv[0])
	if mode == Repair {
		p.Confirm = t.Path
		if t.FSType == "xfs" {
			p.Notes = append(p.Notes, "xfs_repair stops when the log is dirty; Tyr never zeroes the log (-L loses the last writes): "+
				"mount and unmount the filesystem once to replay it, then repair again")
		}
	}
	if t.Ext != nil && t.Ext.State == "errors" {
		p.Notes = append(p.Notes, "the superblock says the filesystem has errors")
	}
	if !t.Mounted() {
		if t.Longhorn != "" {
			p.Notes = append(p.Notes, "Longhorn volume "+t.Longhorn+", attached here and not mounted (maintenance mode): detach it in Longhorn afterwards and scale the workload up again")
		}
		return p
	}

	// mounted: unmount every mountpoint (deepest first), run, mount again
	if t.Longhorn != "" {
		block("Longhorn volume %s is in use by a pod. Scale the workload down, attach the volume to this node in maintenance mode "+
			"(Longhorn UI: Volume → Attach → Maintenance), then check /dev/longhorn/%s here; detach it and scale up again afterwards", t.Longhorn, t.Longhorn)
		return p
	}
	p.Unmount = true
	var points []string
	for _, m := range t.Mounts {
		points = append(points, m.Point)
		if systemMounts[m.Point] {
			block("%s cannot be unmounted while the system runs: boot once with fsck.mode=force fsck.repair=yes on the kernel command line "+
				"(systemd checks it before mounting), or check it from a rescue system", m.Point)
		}
	}
	if len(p.Blockers) > 0 && containsAny(points, systemMounts) {
		return p
	}
	for _, m := range sys.Mounts {
		for _, pt := range points {
			if m.Point != pt && strings.HasPrefix(m.Point, strings.TrimSuffix(pt, "/")+"/") && !contains(points, m.Point) {
				block("%s is mounted below %s: unmount it first", m.Point, pt)
			}
		}
	}
	for _, s := range sys.Swaps {
		if under(s, points) {
			block("swap file %s is on it: swapoff %s first", s, s)
		}
	}
	for _, l := range sys.Loops {
		if under(l, points) {
			block("loop device backed by %s is on it: detach it first (losetup -d)", l)
		}
	}
	if sys.Users != nil {
		us := sys.Users(points)
		for i, u := range us {
			if i == 8 {
				block("… and %d more processes", len(us)-8)
				break
			}
			block("in use by %s (pid %d): %s", u.Comm, u.PID, u.Path)
		}
	}
	p.need(sys, "umount")
	p.need(sys, "mount")
	// deepest mountpoint first
	order := append([]Mount(nil), t.Mounts...)
	sort.SliceStable(order, func(i, j int) bool { return len(order[i].Point) > len(order[j].Point) })
	var pre []Step
	for _, m := range order {
		pre = append(pre, Step{Kind: "umount", Argv: []string{"umount", m.Point}})
	}
	p.Steps = append(pre, p.Steps...)
	p.Steps = append(p.Steps, remount(t)...)
	if mode == Check {
		p.Confirm = "y"
	}
	return p
}

// remount mounts the filesystem where it was, with its options: the
// filesystem's root first, bind mounts of subdirectories after it.
func remount(t Target) []Step {
	var primary *Mount
	for i := range t.Mounts {
		if t.Mounts[i].Root == "/" {
			primary = &t.Mounts[i]
			break
		}
	}
	if primary == nil {
		primary = &t.Mounts[0]
	}
	var out []Step
	out = append(out, Step{Kind: "mount", Argv: []string{"mount", "-t", t.FSType, "-o", mountOpts(*primary), t.Path, primary.Point}})
	for _, m := range t.Mounts {
		if m.Point == primary.Point {
			continue
		}
		src := strings.TrimSuffix(primary.Point, "/") + m.Root
		if m.Root == "/" {
			src = primary.Point
		}
		out = append(out, Step{Kind: "mount", Argv: []string{"mount", "--bind", src, m.Point}})
		if hasOpt(m.Opts, "ro") {
			out = append(out, Step{Kind: "mount", Argv: []string{"mount", "-o", "remount,bind,ro", m.Point}})
		}
	}
	return out
}

// mountOpts merges the per-mount and the filesystem options as mount(8)
// takes them.
func mountOpts(m Mount) string {
	var out []string
	seen := map[string]bool{}
	for _, o := range append(strings.Split(m.Opts, ","), strings.Split(m.SuperOpt, ",")...) {
		if o == "" || seen[o] || o == "seclabel" {
			continue
		}
		if (o == "rw" || o == "ro") && (seen["rw"] || seen["ro"]) {
			continue
		}
		seen[o] = true
		out = append(out, o)
	}
	return strings.Join(out, ",")
}

func hasOpt(opts, o string) bool {
	for _, x := range strings.Split(opts, ",") {
		if x == o {
			return true
		}
	}
	return false
}

func (p *Plan) need(sys System, name string) {
	if !have(sys, name) {
		pkg := map[string]string{"e2fsck": "e2fsprogs", "xfs_repair": "xfsprogs", "xfs_scrub": "xfsprogs", "btrfs": "btrfs-progs",
			"fsck.vfat": "dosfstools", "fsck.exfat": "exfatprogs", "mount": "util-linux", "umount": "util-linux"}[name]
		p.Blockers = append(p.Blockers, fmt.Sprintf("%s is not installed (package %s)", name, pkg))
	}
}

func have(sys System, name string) bool {
	if sys.LookPath == nil {
		return false
	}
	_, err := sys.LookPath(name)
	return err == nil
}

func under(p string, points []string) bool {
	for _, pt := range points {
		if p == pt || strings.HasPrefix(p, strings.TrimSuffix(pt, "/")+"/") {
			return true
		}
	}
	return false
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func containsAny(xs []string, set map[string]bool) bool {
	for _, x := range xs {
		if set[x] {
			return true
		}
	}
	return false
}
