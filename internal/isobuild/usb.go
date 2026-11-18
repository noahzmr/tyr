package isobuild

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Disk is a USB disk a stick can be written to.
type Disk struct {
	Name      string   // sdb
	Dev       string   // /dev/sdb
	Vendor    string   // SanDisk
	Model     string   // Ultra
	Serial    string   // of the USB device
	SizeBytes uint64   // 0: no medium
	Mounts    []string // mounted partitions: unmounted before writing
	System    bool     // holds / or /boot: never written
	Why       string   // why it cannot be written ("" can)
	devs      []string // major:minor of the disk and its partitions
}

// Label is "SanDisk Ultra · 14.3 GB".
func (d Disk) Label() string {
	s := strings.TrimSpace(d.Vendor + " " + d.Model)
	if s == "" {
		s = "USB disk"
	}
	return fmt.Sprintf("%s · %s", s, humanSize(d.SizeBytes))
}

func humanSize(b uint64) string {
	switch {
	case b >= 1e12:
		return fmt.Sprintf("%.1f TB", float64(b)/1e12)
	case b >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(b)/1e9)
	}
	return fmt.Sprintf("%d MB", b/1e6)
}

// USBDisks lists the disks attached over USB under root ("/" on a host):
// /sys/block, /proc/self/mountinfo.
func USBDisks(root string) ([]Disk, error) {
	sysBlock := filepath.Join(root, "sys", "block")
	ents, err := os.ReadDir(sysBlock)
	if err != nil {
		return nil, err
	}
	mounts := readMounts(filepath.Join(root, "proc", "self", "mountinfo"))
	var out []Disk
	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, "sd") {
			continue
		}
		dir := filepath.Join(sysBlock, name)
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || !strings.Contains(real, "/usb") {
			continue
		}
		d := Disk{Name: name, Dev: "/dev/" + name}
		d.Vendor = strings.TrimSpace(readStr(filepath.Join(dir, "device", "vendor")))
		d.Model = strings.TrimSpace(readStr(filepath.Join(dir, "device", "model")))
		if n, err := strconv.ParseUint(strings.TrimSpace(readStr(filepath.Join(dir, "size"))), 10, 64); err == nil {
			d.SizeBytes = n * 512
		}
		// the USB device's serial: the first "serial" file up the path
		for p := filepath.Dir(real); strings.Contains(p, "/usb"); p = filepath.Dir(p) {
			if s := strings.TrimSpace(readStr(filepath.Join(p, "serial"))); s != "" {
				d.Serial = s
				break
			}
		}
		d.devs = append(d.devs, strings.TrimSpace(readStr(filepath.Join(dir, "dev"))))
		parts, _ := filepath.Glob(filepath.Join(dir, name+"*"))
		for _, p := range parts {
			d.devs = append(d.devs, strings.TrimSpace(readStr(filepath.Join(p, "dev"))))
		}
		for _, m := range mounts {
			if !contains(d.devs, m.dev) {
				continue
			}
			switch m.point {
			case "/", "/boot", "/boot/efi", "/usr", "/var":
				d.System = true
			}
			d.Mounts = append(d.Mounts, m.point)
		}
		switch {
		case d.System:
			d.Why = "the running system is on it"
		case d.SizeBytes == 0:
			d.Why = "no medium"
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type mount struct{ dev, point string }

// readMounts reads major:minor and mount point from mountinfo.
func readMounts(path string) []mount {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []mount
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 5 {
			out = append(out, mount{dev: f[2], point: unescape(f[4])})
		}
	}
	return out
}

// unescape undoes mountinfo's \040 escapes.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func readStr(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// Progress reports bytes done of total in a stage ("write", "verify").
type Progress func(stage string, done, total int64)

// Write puts the image onto the disk: its mounted partitions are unmounted
// first, the device is opened exclusively, the data flushed as it goes,
// and the kernel re-reads the partition table. Then it reads the image back
// and compares checksums.
func Write(ctx context.Context, image string, d Disk, progress Progress) error {
	if d.System {
		return fmt.Errorf("%s: the running system is on it", d.Dev)
	}
	in, err := os.Open(image)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	if d.SizeBytes > 0 && uint64(size) > d.SizeBytes {
		return fmt.Errorf("%s holds %s, the image needs %s", d.Dev, humanSize(d.SizeBytes), humanSize(uint64(size)))
	}
	for i := len(d.Mounts) - 1; i >= 0; i-- {
		if err := unix.Unmount(d.Mounts[i], 0); err != nil && !errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("unmount %s: %w (close what uses it)", d.Mounts[i], err)
		}
	}
	out, err := os.OpenFile(d.Dev, os.O_WRONLY|unix.O_EXCL, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", d.Dev, err)
	}
	sum := sha256.New()
	buf := make([]byte, 4<<20)
	var done int64
	for {
		if ctx.Err() != nil {
			out.Close()
			return ctx.Err()
		}
		n, rerr := io.ReadFull(in, buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				out.Close()
				return fmt.Errorf("write %s: %w", d.Dev, err)
			}
			sum.Write(buf[:n])
			done += int64(n)
			if done%(64<<20) == 0 { // flush as it goes: honest progress, no long wait at the end
				if err := out.Sync(); err != nil {
					out.Close()
					return fmt.Errorf("write %s: %w", d.Dev, err)
				}
			}
			progress("write", done, size)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return fmt.Errorf("write %s: %w", d.Dev, err)
	}
	unix.IoctlSetInt(int(out.Fd()), unix.BLKRRPART, 0) // a regular file (tests) has no partition table to re-read
	out.Close()
	return verify(ctx, d.Dev, size, sum.Sum(nil), progress)
}

// verify reads the written bytes back past the page cache and compares.
func verify(ctx context.Context, dev string, size int64, want []byte, progress Progress) error {
	f, err := os.Open(dev)
	if err != nil {
		return err
	}
	defer f.Close()
	unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
	sum := sha256.New()
	buf := make([]byte, 4<<20)
	var done int64
	for done < size {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := f.Read(buf[:min(int64(len(buf)), size-done)])
		if n > 0 {
			sum.Write(buf[:n])
			done += int64(n)
			progress("verify", done, size)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}
	if done != size || !bytes.Equal(sum.Sum(nil), want) {
		return fmt.Errorf("%s: what was read back differs from the image - the stick may be faulty", dev)
	}
	return nil
}
