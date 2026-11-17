package stress

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
)

// Storage tests never write to a block device. "read" scans the device
// surface read-only (O_DIRECT, bypassing the page cache); "file" writes
// CRC-protected blocks into an unlinked temporary file on a filesystem and
// reads them back. Raw-device writes are not implemented.

const (
	storBlock = 1 << 20 // I/O size
	slowRead  = 500 * time.Millisecond
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// readTargets resolves "read" mode targets: one device or "all".
func readTargets(cfg Config, env *Env) ([]model.Disk, string) {
	st := env.Baseline.Storage
	if st == nil {
		return nil, "storage information unavailable"
	}
	var out []model.Disk
	for _, d := range st.Disks {
		if cfg.Target == "all" && (d.Kind == "virtual" || slices.Contains(cfg.Exclude, d.Name) || slices.Contains(cfg.Exclude, d.Path)) {
			continue
		}
		if cfg.Target == "all" || cfg.Target == d.Path || cfg.Target == d.Name {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		if cfg.Target == "" {
			return nil, "choose a disk (--target /dev/sdX or all)"
		}
		return nil, "no disk " + cfg.Target
	}
	return out, ""
}

// fsTarget resolves the file test location: a mountpoint, or any directory
// inside a mounted filesystem (e.g. a home directory when / is not
// writable without root). The returned filesystem's Mountpoint is the
// directory the test file is created in.
func fsTarget(cfg Config, env *Env) (model.Filesystem, string) {
	st := env.Baseline.Storage
	if st == nil {
		return model.Filesystem{}, "storage information unavailable"
	}
	if cfg.Target == "" {
		return model.Filesystem{}, "choose a mounted filesystem or a directory on one (--target /data)"
	}
	dir := path.Clean(cfg.Target)
	var best model.Filesystem
	found := false
	for _, f := range st.Filesystems {
		mp := f.Mountpoint
		if dir == mp || strings.HasPrefix(dir, strings.TrimSuffix(mp, "/")+"/") {
			if !found || len(mp) > len(best.Mountpoint) {
				best, found = f, true
			}
		}
	}
	if !found {
		return model.Filesystem{}, cfg.Target + " is not on a mounted local filesystem"
	}
	if best.ReadOnly {
		return best, best.Mountpoint + " is mounted read-only"
	}
	fsMount := best.Mountpoint
	best.Mountpoint = dir
	if !env.Simulated() {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return best, dir + " is not a directory"
		}
		if unix.Access(dir, unix.W_OK) != nil {
			return best, "no write permission in " + dir + " (run as root, or pick a writable directory on " + fsMount + ")"
		}
	}
	return best, ""
}

// fileSize is the file test size: --size, default min(4 GiB, 10% of free),
// always leaving 10% of the filesystem free.
func fileSize(cfg Config, f model.Filesystem) (uint64, string) {
	free := f.SizeBytes - f.UsedBytes
	keep := f.SizeBytes / 10
	if free <= keep+storBlock*16 {
		return 0, fmt.Sprintf("%s has only %s free", f.Mountpoint, humanBytes(free))
	}
	size := cfg.SizeBytes
	if size == 0 {
		size = min(4<<30, free/10)
	}
	size = min(size, free-keep)
	return max(storBlock*16, size&^(storBlock-1)), ""
}

func storagePlan(cfg Config, env *Env, engine string) *Plan {
	p := &Plan{}
	mode := cfg.StorageMode
	if mode == "" {
		mode = "read"
	}
	if engine == EngStressNG && mode == "read" {
		p.Blocked = "stress-ng only runs the file test: use --mode file --target <mountpoint>"
		return p
	}
	if mode == "read" {
		disks, why := readTargets(cfg, env)
		if why != "" {
			p.Blocked = why
			return p
		}
		for _, d := range disks {
			if !env.Simulated() && unix.Access(d.Path, unix.R_OK) != nil {
				p.Blocked = d.Path + " is not readable: the surface scan needs root"
				return p
			}
			p.Scope = append(p.Scope, fmt.Sprintf("%s %s %s (%s)", d.Path, strings.TrimSpace(d.Vendor+" "+d.Model), orDash(d.Serial), humanBytes(d.SizeBytes)))
		}
		p.Impact = []string{"Reads the whole surface at full speed (read-only, nothing is written); other I/O on these disks gets slower.",
			fmt.Sprintf("Reads slower than %v and read errors are reported with their offset; the guard stops on new SMART errors.", slowRead)}
		var big uint64
		for _, d := range disks {
			big = max(big, d.SizeBytes)
		}
		if est := time.Duration(float64(big) / 200e6 * float64(time.Second)); est > cfg.Duration {
			p.Info = append(p.Info, fmt.Sprintf("At ~200 MB/s a full pass of the largest disk takes about %s; the scan stops after %s and reports how far it got.", fmtDur(est), fmtDur(cfg.Duration)))
		}
		return p
	}
	f, why := fsTarget(cfg, env)
	if why != "" {
		p.Blocked = why
		return p
	}
	size, why := fileSize(cfg, f)
	if size == 0 {
		p.Blocked = why
		return p
	}
	p.Scope = []string{fmt.Sprintf("%s (%s, %s) — %s test file", f.Mountpoint, f.FSType, f.Source, humanBytes(size))}
	p.Impact = []string{"Writes and re-reads a temporary file; it is invisible (O_TMPFILE / unlinked) and disappears even if Tyr is killed.",
		"Other I/O on this filesystem gets slower; the free space shrinks by the file size while the test runs."}
	if engine == EngStressNG {
		p.Impact[0] = "stress-ng writes temporary files in a directory under " + f.Mountpoint + " and removes them at the end (a killed run can leave the stress-ng-* directory behind)."
	}
	return p
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

// --- built-in -----------------------------------------------------------------

type storageBuiltin struct{}

func (storageBuiltin) Plan(cfg Config, env *Env) *Plan { return storagePlan(cfg, env, EngBuiltin) }

func (storageBuiltin) Run(ctx context.Context, cfg Config, _ *Plan, rt Runtime) error {
	env := runEnv(ctx)
	if cfg.StorageMode == "file" {
		f, _ := fsTarget(cfg, env)
		size, _ := fileSize(cfg, f)
		return fileTest(ctx, f.Mountpoint, size, rt)
	}
	disks, _ := readTargets(cfg, env)
	var wg sync.WaitGroup
	errs := make(chan error, len(disks))
	for _, d := range disks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := surfaceScan(ctx, env, d, rt); err != nil {
				errs <- fmt.Errorf("%s: %w", d.Name, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	return <-errs
}

// alignedBuf returns a page-aligned buffer suitable for O_DIRECT.
func alignedBuf(n int) ([]byte, func(), error) {
	b, err := unix.Mmap(-1, 0, n, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS)
	if err != nil {
		return nil, nil, err
	}
	return b, func() { _ = unix.Munmap(b) }, nil
}

// latHist is a log2-bucket latency histogram (microseconds).
type latHist struct {
	buckets [40]uint64
	n       uint64
	max     time.Duration
}

func (h *latHist) add(d time.Duration) {
	us := max(1, d.Microseconds())
	h.buckets[min(39, int(math.Log2(float64(us))))]++
	h.n++
	h.max = max(h.max, d)
}

func (h *latHist) quantile(q float64) time.Duration {
	want := uint64(math.Ceil(float64(h.n) * q))
	var acc uint64
	for i, c := range h.buckets {
		acc += c
		if acc >= want && c > 0 {
			return time.Duration(1<<uint(i+1)) * time.Microsecond
		}
	}
	return h.max
}

func blockDevSize(env *Env, d model.Disk) uint64 {
	if v, err := hostfs.Uint(env.Collector.FS, "/sys/block/"+path.Base(d.Path)+"/size"); err == nil {
		return v * 512
	}
	return d.SizeBytes
}

// surfaceScan reads the device sequentially (wrapping) with O_DIRECT.
func surfaceScan(ctx context.Context, env *Env, d model.Disk, rt Runtime) error {
	fd, err := unix.Open(d.Path, unix.O_RDONLY|unix.O_DIRECT|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s read-only: %w", d.Path, err)
	}
	f := os.NewFile(uintptr(fd), d.Path)
	defer f.Close()
	rt.Track(f)
	size := blockDevSize(env, d)
	buf, free, err := alignedBuf(storBlock)
	if err != nil {
		return err
	}
	defer free()
	var h latHist
	var off, done uint64
	pass := 1
	lastAt, lastDone, errCount, slow := time.Now(), uint64(0), 0, 0
	for ctx.Err() == nil {
		n := int(min(uint64(storBlock), size-off))
		t0 := time.Now()
		got, err := unix.Pread(fd, buf[:n], int64(off))
		lat := time.Since(t0)
		h.add(lat)
		if lat > slowRead {
			slow++
			if slow <= 20 {
				rt.Logf("%s: slow read %v at %s", d.Name, lat.Round(time.Millisecond), humanBytes(off))
			}
		}
		if err != nil || got != n {
			errCount++
			if errCount <= 50 {
				detail := fmt.Sprintf("read %d of %d bytes", got, n)
				if err != nil {
					detail = err.Error()
				}
				rt.VerifyError(VerifyError{Subject: fmt.Sprintf("%s @ %s (byte %d)", d.Path, humanBytes(off), off), Workload: "surface read", Detail: detail})
			}
			if errors.Is(err, unix.EINVAL) && off == 0 {
				return fmt.Errorf("%s does not support O_DIRECT reads", d.Path)
			}
		}
		off += uint64(n)
		done += uint64(n)
		if off >= size {
			off, pass = 0, pass+1
			rt.Logf("%s: full pass %d done (%s)", d.Name, pass-1, humanBytes(size))
		}
		if now := time.Now(); now.Sub(lastAt) >= time.Second {
			rt.Metric("read_bytes_per_s_"+d.Name, "bytes_per_second", float64(done-lastDone)/now.Sub(lastAt).Seconds())
			rt.Metric("read_p99_ms_"+d.Name, "ms", float64(h.quantile(0.99).Microseconds())/1000)
			rt.Metric("read_max_ms_"+d.Name, "ms", float64(h.max.Microseconds())/1000)
			rt.Progress(-1, fmt.Sprintf("%s pass %d: %.1f%% of %s", d.Name, pass, float64(off)/float64(size)*100, humanBytes(size)))
			lastAt, lastDone = now, done
		}
	}
	rt.Logf("%s: read %s, p50 %v p99 %v max %v, %d slow reads, %d read errors", d.Name, humanBytes(done),
		h.quantile(0.5), h.quantile(0.99), h.max.Round(time.Microsecond), slow, errCount)
	if slow > 0 {
		rt.Metric("slow_reads_"+d.Name, "count", float64(slow))
	}
	return nil
}

// tmpFile opens an unnamed file in dir (O_TMPFILE), or creates and unlinks
// one when the filesystem lacks O_TMPFILE.
func tmpFile(dir string) (*os.File, error) {
	fd, err := unix.Open(dir, unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0o600)
	if err == nil {
		return os.NewFile(uintptr(fd), dir+"/(tyr tmpfile)"), nil
	}
	f, err := os.CreateTemp(dir, ".tyr-stress-*")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(f.Name())
	return f, nil
}

// fillBlock writes a block: 8 bytes offset, 8 bytes pass, 4 bytes CRC32C of
// the payload, then seeded pseudo-random payload.
func fillBlock(b []byte, off uint64, pass int) {
	binary.LittleEndian.PutUint64(b[0:], off)
	binary.LittleEndian.PutUint64(b[8:], uint64(pass))
	x := (off ^ uint64(pass)*0x9E3779B97F4A7C15) | 1
	for i := 20; i+8 <= len(b); i += 8 {
		x = xorshift(x)
		binary.LittleEndian.PutUint64(b[i:], x)
	}
	binary.LittleEndian.PutUint32(b[16:], crc32.Checksum(b[20:], castagnoli))
}

func checkBlock(b []byte, off uint64, pass int) string {
	if o := binary.LittleEndian.Uint64(b[0:]); o != off {
		return fmt.Sprintf("block header says offset %d (misdirected write/read)", o)
	}
	if p := binary.LittleEndian.Uint64(b[8:]); p != uint64(pass) {
		return fmt.Sprintf("stale data from pass %d (lost write)", p)
	}
	if c := crc32.Checksum(b[20:], castagnoli); c != binary.LittleEndian.Uint32(b[16:]) {
		return "CRC32C mismatch (corrupted data)"
	}
	return ""
}

// corruptFile lets tests damage the file between write and read-back.
var corruptFile func(f *os.File, pass int)

func fileTest(ctx context.Context, dir string, size uint64, rt Runtime) error {
	f, err := tmpFile(dir)
	if err != nil {
		return fmt.Errorf("temporary file in %s: %w", dir, err)
	}
	defer f.Close()
	rt.Track(f)
	// Read back through a separate O_DIRECT descriptor so data comes from
	// the disk, not the page cache.
	var rf *os.File
	if fd, err := unix.Open("/proc/self/fd/"+strconv.Itoa(int(f.Fd())), unix.O_RDONLY|unix.O_DIRECT|unix.O_CLOEXEC, 0); err == nil {
		rf = os.NewFile(uintptr(fd), "direct")
		defer rf.Close()
	}
	buf, free, err := alignedBuf(storBlock)
	if err != nil {
		return err
	}
	defer free()
	for pass := 1; ctx.Err() == nil; pass++ {
		rt.Progress(-1, fmt.Sprintf("pass %d: writing %s", pass, humanBytes(size)))
		t0, last := time.Now(), time.Now()
		var lastOff uint64
		for off := uint64(0); off < size && ctx.Err() == nil; off += storBlock {
			fillBlock(buf, off, pass)
			if _, err := f.WriteAt(buf, int64(off)); err != nil {
				return fmt.Errorf("write at %s: %w", humanBytes(off), err)
			}
			if time.Since(last) >= time.Second {
				rt.Metric("file_write_bytes_per_s", "bytes_per_second", float64(off-lastOff)/time.Since(last).Seconds())
				last, lastOff = time.Now(), off
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		if err := f.Sync(); err != nil {
			return fmt.Errorf("fsync: %w", err)
		}
		wd := time.Since(t0)
		rt.Metric("file_write_bytes_per_s", "bytes_per_second", float64(size)/wd.Seconds())
		rt.Logf("pass %d: wrote %s in %v", pass, humanBytes(size), wd.Round(time.Millisecond))
		if corruptFile != nil {
			corruptFile(f, pass)
		}
		reader := rf
		if reader == nil {
			_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
			reader = f
		}
		rt.Progress(-1, fmt.Sprintf("pass %d: reading back and verifying", pass))
		last, lastOff = time.Now(), 0
		r0 := time.Now()
		bad := 0
		for off := uint64(0); off < size && ctx.Err() == nil; off += storBlock {
			if _, err := reader.ReadAt(buf, int64(off)); err != nil {
				rt.VerifyError(VerifyError{Subject: fmt.Sprintf("%s file offset %s", dir, humanBytes(off)), Workload: "file read-back", Detail: err.Error()})
				continue
			}
			if why := checkBlock(buf, off, pass); why != "" {
				bad++
				if bad <= 20 {
					rt.VerifyError(VerifyError{Subject: fmt.Sprintf("%s file offset %d", dir, off), Workload: "file read-back", Detail: why})
				}
			}
			if time.Since(last) >= time.Second {
				rt.Metric("file_read_bytes_per_s", "bytes_per_second", float64(off-lastOff)/time.Since(last).Seconds())
				last, lastOff = time.Now(), off
			}
		}
		if ctx.Err() == nil {
			rt.Metric("file_read_bytes_per_s", "bytes_per_second", float64(size)/time.Since(r0).Seconds())
		}
	}
	return nil
}

// --- stress-ng ----------------------------------------------------------------

type storageStressNG struct{}

func (storageStressNG) Plan(cfg Config, env *Env) *Plan {
	if cfg.StorageMode == "" {
		cfg.StorageMode = "file"
	}
	return storagePlan(cfg, env, EngStressNG)
}

func (storageStressNG) Run(ctx context.Context, cfg Config, _ *Plan, rt Runtime) error {
	env := runEnv(ctx)
	f, _ := fsTarget(cfg, env)
	size, _ := fileSize(cfg, f)
	n := 4
	dir, err := os.MkdirTemp(f.Mountpoint, ".tyr-stress-ng-")
	if err != nil {
		return fmt.Errorf("work directory on %s: %w", f.Mountpoint, err)
	}
	defer os.RemoveAll(dir)
	rt.Progress(-1, fmt.Sprintf("stress-ng --hdd %d × %s on %s", n, humanBytes(size/uint64(n)), f.Mountpoint))
	return stressNG(ctx, cfg, rt, f.Mountpoint, "--hdd", strconv.Itoa(n), "--hdd-bytes", strconv.FormatUint(size/uint64(n), 10)+"b", "--temp-path", dir)
}

// Disks lists candidate read-scan targets and filesystems for the UI.
func StorageTargets(env *Env) (disks []string, mounts []string) {
	if env.Baseline == nil || env.Baseline.Storage == nil {
		return nil, nil
	}
	for _, d := range env.Baseline.Storage.Disks {
		disks = append(disks, d.Path)
	}
	for _, f := range env.Baseline.Storage.Filesystems {
		if !f.ReadOnly {
			mounts = append(mounts, f.Mountpoint)
		}
	}
	sort.Strings(mounts)
	return disks, mounts
}
