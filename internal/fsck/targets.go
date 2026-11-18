// Package fsck checks and repairs filesystems: it lists every block device
// with a filesystem, plans the commands for a check or a repair (with the
// unmount and mount around them when needed), says what stands in the way,
// and runs the plan with the output streamed.
//
// A check only reads. A repair writes to the filesystem: it runs only on an
// unmounted filesystem, only after the caller confirmed with the device
// path, never on the live ISO, and never with btrfs check --repair or
// xfs_repair -L, which can destroy more than they fix.
package fsck

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/source/crypt"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

// Supported filesystem types and the package with their tools.
var Supported = map[string]string{
	"ext2": "e2fsprogs", "ext3": "e2fsprogs", "ext4": "e2fsprogs",
	"xfs": "xfsprogs", "btrfs": "btrfs-progs", "vfat": "dosfstools", "exfat": "exfatprogs",
}

// Target is a block device with a filesystem.
type Target struct {
	Dev       string // sda3, dm-0, md0
	Path      string // /dev/sda3, /dev/mapper/vg0-data
	FSType    string
	Label     string
	UUID      string
	SizeBytes uint64
	Mounts    []Mount
	Longhorn  string // pvc-… when the device is a Longhorn volume
	Remote    string // iscsi, nvme-tcp: a network volume
	Ext       *ExtSuper
}

// Mounted reports whether the filesystem is mounted anywhere.
func (t Target) Mounted() bool { return len(t.Mounts) > 0 }

// Mount is one place the filesystem is mounted.
type Mount struct {
	Point    string
	Root     string // "/" for the filesystem's root, else a bind mount of a subdirectory
	Opts     string // per-mount options (rw,noatime)
	SuperOpt string // filesystem options (errors=remount-ro)
	FSType   string
	Source   string
	MajMin   string
}

// ReadMounts parses /proc/self/mountinfo with the options kept.
func ReadMounts(f hostfs.FS) []Mount {
	b, err := f.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	var out []Mount
	for _, line := range strings.Split(string(b), "\n") {
		x := strings.Fields(line)
		sep := -1
		for i, v := range x {
			if v == "-" {
				sep = i
				break
			}
		}
		if len(x) < 6 || sep < 0 || sep+2 >= len(x) {
			continue
		}
		m := Mount{MajMin: x[2], Root: unescape(x[3]), Point: unescape(x[4]), Opts: x[5], FSType: x[sep+1], Source: unescape(x[sep+2])}
		if sep+3 < len(x) {
			m.SuperOpt = x[sep+3]
		}
		out = append(out, m)
	}
	return out
}

func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Targets lists the block devices with a supported filesystem: from udev's
// records (no root needed), plus anything mounted that udev did not name.
func Targets(f hostfs.FS) []Target {
	mounts := ReadMounts(f)
	byDev := map[string][]Mount{}
	for _, m := range mounts {
		dev := crypt.DevName(f, m.Source)
		if dev == "" {
			dev = crypt.NameByMajMin(f, m.MajMin)
		}
		if dev != "" {
			byDev[dev] = append(byDev[dev], m)
		}
	}
	lh := longhornDevs(f)
	seen := map[string]bool{}
	var out []Target
	add := func(dev, fstype string, props map[string]string) {
		if seen[dev] || Supported[fstype] == "" {
			return
		}
		seen[dev] = true
		t := Target{Dev: dev, Path: "/dev/" + dev, FSType: fstype, Label: props["ID_FS_LABEL"], UUID: props["ID_FS_UUID"], Mounts: byDev[dev]}
		if strings.HasPrefix(dev, "dm-") {
			if n := hostfs.StringOr(f, "/sys/class/block/"+dev+"/dm/name", ""); n != "" {
				t.Path = "/dev/mapper/" + n
			}
		}
		if n, err := hostfs.Uint(f, "/sys/class/block/"+dev+"/size"); err == nil {
			t.SizeBytes = n * 512
		}
		t.Longhorn = lh[dev]
		disk := dev
		if p := crypt.Parent(f, dev); p != "" {
			disk = p
		}
		if real, err := f.EvalSymlinks("/sys/class/block/" + disk); err == nil {
			if strings.Contains(real, "/session") {
				t.Remote = "iscsi"
			}
		}
		for _, m := range t.Mounts {
			if strings.Contains(m.Point, "driver.longhorn.io") && t.Longhorn == "" {
				t.Longhorn = longhornFromPath(m.Point)
			}
		}
		out = append(out, t)
	}
	names, _ := f.ReadDir("/sys/class/block")
	for _, e := range names {
		dev := e.Name()
		if skipDev(dev) {
			continue
		}
		props := sysfs.UdevProps(f, dev)
		add(dev, props["ID_FS_TYPE"], props)
	}
	for dev, ms := range byDev { // mounted, udev did not say (containers, no udev)
		add(dev, ms[0].FSType, map[string]string{})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mounted() != out[j].Mounted() {
			return !out[i].Mounted() // unmounted first: those can be checked right away
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func skipDev(dev string) bool {
	for _, p := range []string{"loop", "ram", "zram", "sr", "fd", "nbd"} {
		if strings.HasPrefix(dev, p) {
			return true
		}
	}
	return false
}

// longhornDevs maps devices to Longhorn volumes by /dev/longhorn/<volume>.
func longhornDevs(f hostfs.FS) map[string]string {
	out := map[string]string{}
	links, _ := f.Glob("/dev/longhorn/*")
	for _, l := range links {
		if real, err := f.EvalSymlinks(l); err == nil {
			out[path.Base(real)] = path.Base(l)
		}
	}
	return out
}

func longhornFromPath(p string) string {
	for _, part := range strings.Split(p, "/") {
		if strings.HasPrefix(part, "pvc-") {
			return part
		}
	}
	return "a Longhorn volume"
}

// --- ext superblock -------------------------------------------------------------

// ExtSuper is what an ext2/3/4 superblock records about its health.
type ExtSuper struct {
	State      string // clean, not clean, errors
	Errors     uint32 // s_error_count
	LastCheck  time.Time
	MountCount int
	MaxMounts  int
	FirstError string // time and function
	LastError  string
}

// ReadExtSuper reads the superblock at byte 1024 of the device (root).
func ReadExtSuper(f hostfs.FS, dev string) (*ExtSuper, error) {
	fh, err := f.Open(dev)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	b := make([]byte, 2048)
	if _, err := io.ReadFull(fh, b); err != nil {
		return nil, err
	}
	return ParseExtSuper(b[1024:])
}

// ParseExtSuper decodes the fields of an ext superblock that tell its health.
func ParseExtSuper(b []byte) (*ExtSuper, error) {
	if len(b) < 0x200 || binary.LittleEndian.Uint16(b[0x38:]) != 0xEF53 {
		return nil, fmt.Errorf("no ext superblock")
	}
	le := binary.LittleEndian
	s := &ExtSuper{MountCount: int(le.Uint16(b[0x34:])), MaxMounts: int(int16(le.Uint16(b[0x36:]))), Errors: le.Uint32(b[0x194:])}
	switch st := le.Uint16(b[0x3A:]); {
	case st&2 != 0:
		s.State = "errors"
	case st&1 != 0:
		s.State = "clean"
	default:
		s.State = "not clean"
	}
	if t := le.Uint32(b[0x40:]); t > 0 {
		s.LastCheck = time.Unix(int64(t), 0).UTC()
	}
	errAt := func(tOff, fnOff int) string {
		t := le.Uint32(b[tOff:])
		if t == 0 {
			return ""
		}
		fn := b[fnOff : fnOff+32]
		if i := bytes.IndexByte(fn, 0); i >= 0 {
			fn = fn[:i]
		}
		s := time.Unix(int64(t), 0).UTC().Format("2006-01-02 15:04")
		if len(fn) > 0 {
			s += " in " + string(fn)
		}
		return s
	}
	s.FirstError = errAt(0x198, 0x1A8)
	s.LastError = errAt(0x1CC, 0x1E0)
	return s, nil
}

// IsLive reports whether Tyr runs from a live system (the Tyr ISO): the
// server's own disks are then never written.
func IsLive(f hostfs.FS) bool {
	if hostfs.Exists(f, "/run/live/medium") {
		return true
	}
	cmd := hostfs.StringOr(f, "/proc/cmdline", "")
	return strings.Contains(" "+cmd+" ", " boot=live ")
}
