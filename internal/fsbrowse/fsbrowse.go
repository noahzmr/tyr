// Package fsbrowse reads directories for the file browser: one directory's
// entries with what lstat tells, and the total size of every subtree,
// counted in the background like du / ncdu - on one filesystem only, hard
// links once, apparent and allocated size apart (sparse files: Longhorn
// replicas, VM images). It only reads.
package fsbrowse

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Entry is one directory entry.
type Entry struct {
	Name       string
	Path       string
	Mode       fs.FileMode
	Size       int64 // apparent size
	Alloc      int64 // allocated on disk (st_blocks × 512)
	Nlink      uint64
	Ino        uint64
	Dev        uint64
	UID, GID   uint32
	MTime      time.Time
	CTime      time.Time
	ATime      time.Time
	LinkTarget string
	OtherFS    bool   // a mount point: another filesystem
	Err        string // why it could not be read
}

// IsDir reports whether the entry is a directory (not a symlink to one).
func (e Entry) IsDir() bool { return e.Mode.IsDir() }

func entry(dir, name string) Entry {
	p := filepath.Join(dir, name)
	e := Entry{Name: name, Path: p}
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		e.Err = err.Error()
		return e
	}
	fill(&e, &st)
	if e.Mode&fs.ModeSymlink != 0 {
		e.LinkTarget, _ = os.Readlink(p)
	}
	return e
}

func fill(e *Entry, st *unix.Stat_t) {
	e.Mode = fs.FileMode(st.Mode & 0o777)
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		e.Mode |= fs.ModeDir
	case unix.S_IFLNK:
		e.Mode |= fs.ModeSymlink
	case unix.S_IFIFO:
		e.Mode |= fs.ModeNamedPipe
	case unix.S_IFSOCK:
		e.Mode |= fs.ModeSocket
	case unix.S_IFBLK:
		e.Mode |= fs.ModeDevice
	case unix.S_IFCHR:
		e.Mode |= fs.ModeDevice | fs.ModeCharDevice
	}
	if st.Mode&unix.S_ISUID != 0 {
		e.Mode |= fs.ModeSetuid
	}
	if st.Mode&unix.S_ISGID != 0 {
		e.Mode |= fs.ModeSetgid
	}
	if st.Mode&unix.S_ISVTX != 0 {
		e.Mode |= fs.ModeSticky
	}
	e.Size, e.Alloc = st.Size, st.Blocks*512
	e.Nlink, e.Ino, e.Dev = uint64(st.Nlink), st.Ino, uint64(st.Dev)
	e.UID, e.GID = st.Uid, st.Gid
	e.MTime = time.Unix(st.Mtim.Unix())
	e.CTime = time.Unix(st.Ctim.Unix())
	e.ATime = time.Unix(st.Atim.Unix())
}

// List reads one directory. Entries on another filesystem (mount points)
// are marked.
func List(dir string) (Entry, []Entry, error) {
	var st unix.Stat_t
	if err := unix.Stat(dir, &st); err != nil {
		return Entry{}, nil, err
	}
	self := Entry{Name: filepath.Base(dir), Path: dir}
	fill(&self, &st)
	f, err := os.Open(dir)
	if err != nil {
		return self, nil, err
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	out := make([]Entry, 0, len(names))
	for _, n := range names {
		e := entry(dir, n)
		e.OtherFS = e.IsDir() && e.Err == "" && e.Dev != self.Dev
		out = append(out, e)
	}
	return self, out, err
}

// Stat is the total of a subtree.
type Stat struct {
	Apparent, Alloc int64
	Files, Dirs     int64
	Errors          int64 // entries that could not be read (permission)
	Done            bool  // the whole subtree is counted
}

// Scanner counts subtrees in the background and keeps every directory's
// total, so moving into a counted directory shows its sizes at once.
type Scanner struct {
	mu     sync.Mutex
	stats  map[string]Stat
	links  map[[2]uint64]bool // (dev, inode) of files with more than one link
	cancel context.CancelFunc
	gen    int

	files, bytes atomic.Int64
	current      atomic.Pointer[string]
	running      atomic.Bool
}

// NewScanner returns an empty scanner.
func NewScanner() *Scanner {
	return &Scanner{stats: map[string]Stat{}, links: map[[2]uint64]bool{}}
}

// Get returns the total of a directory counted so far.
func (s *Scanner) Get(p string) (Stat, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.stats[p]
	return st, ok
}

// Progress: files counted, bytes seen, the directory being read, running.
func (s *Scanner) Progress() (files, bytes int64, current string, running bool) {
	if c := s.current.Load(); c != nil {
		current = *c
	}
	return s.files.Load(), s.bytes.Load(), current, s.running.Load()
}

// Forget drops the totals below p (to count them again).
func (s *Scanner) Forget(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pre := filepath.Clean(p) + string(filepath.Separator)
	for k := range s.stats {
		if k == p || len(k) > len(pre) && k[:len(pre)] == pre {
			delete(s.stats, k)
		}
	}
	// the parents' totals are stale too
	for d := filepath.Dir(p); ; d = filepath.Dir(d) {
		delete(s.stats, d)
		if d == "/" || d == "." {
			break
		}
	}
	s.links = map[[2]uint64]bool{}
}

// Stop ends the scan that is running.
func (s *Scanner) Stop() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
}

// Scan counts the subtree of root in the background, replacing a scan
// that is running. Directories already counted are not read again.
func (s *Scanner) Scan(root string) {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	if st, ok := s.stats[root]; ok && st.Done {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.gen++
	gen := s.gen
	s.mu.Unlock()
	var st unix.Stat_t
	if err := unix.Stat(root, &st); err != nil {
		cancel()
		return
	}
	s.running.Store(true)
	go func() {
		defer cancel()
		s.walkTop(ctx, root, uint64(st.Dev))
		s.mu.Lock()
		if s.gen == gen {
			s.running.Store(false)
		}
		s.mu.Unlock()
	}()
}

// walkTop counts root's children in parallel and updates root's total as
// each one finishes.
func (s *Scanner) walkTop(ctx context.Context, root string, dev uint64) {
	_, ents, _ := List(root)
	var total Stat
	var mu sync.Mutex
	add := func(x Stat) {
		mu.Lock()
		total.Apparent += x.Apparent
		total.Alloc += x.Alloc
		total.Files += x.Files
		total.Dirs += x.Dirs
		total.Errors += x.Errors
		cur := total
		mu.Unlock()
		s.put(root, cur)
	}
	var dirs []Entry
	for _, e := range ents {
		switch {
		case e.Err != "":
			add(Stat{Errors: 1})
		case e.IsDir() && !e.OtherFS:
			dirs = append(dirs, e)
		case e.IsDir():
		default:
			add(s.file(e))
		}
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, d := range dirs {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(d Entry) {
			defer wg.Done()
			defer func() { <-sem }()
			add(s.walk(ctx, d.Path, dev))
		}(d)
	}
	wg.Wait()
	total.Dirs += int64(len(dirs))
	total.Done = ctx.Err() == nil
	s.put(root, total)
}

func (s *Scanner) put(p string, st Stat) {
	s.mu.Lock()
	s.stats[p] = st
	s.mu.Unlock()
}

func (s *Scanner) file(e Entry) Stat {
	s.files.Add(1)
	if e.Nlink > 1 && !e.IsDir() {
		k := [2]uint64{e.Dev, e.Ino}
		s.mu.Lock()
		seen := s.links[k]
		s.links[k] = true
		s.mu.Unlock()
		if seen {
			return Stat{Files: 1}
		}
	}
	s.bytes.Add(e.Size)
	return Stat{Apparent: e.Size, Alloc: e.Alloc, Files: 1}
}

// walk counts one subtree depth-first and records every directory in it.
func (s *Scanner) walk(ctx context.Context, dir string, dev uint64) Stat {
	if st, ok := s.Get(dir); ok && st.Done {
		return st
	}
	p := dir
	s.current.Store(&p)
	var total Stat
	f, err := os.Open(dir)
	if err != nil {
		total.Errors++
		total.Done = true
		s.put(dir, total)
		return total
	}
	var st unix.Stat_t
	for {
		if ctx.Err() != nil {
			f.Close()
			s.put(dir, total)
			return total
		}
		names, err := f.Readdirnames(1024)
		for _, n := range names {
			c := filepath.Join(dir, n)
			if err := unix.Lstat(c, &st); err != nil {
				total.Errors++
				continue
			}
			if st.Mode&unix.S_IFMT == unix.S_IFDIR {
				if uint64(st.Dev) != dev {
					continue // another filesystem mounted here
				}
				sub := s.walk(ctx, c, dev)
				total.Apparent += sub.Apparent
				total.Alloc += sub.Alloc
				total.Files += sub.Files
				total.Dirs += sub.Dirs + 1
				total.Errors += sub.Errors
				continue
			}
			var e Entry
			fill(&e, &st)
			x := s.file(e)
			total.Apparent += x.Apparent
			total.Alloc += x.Alloc
			total.Files += x.Files
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				total.Errors++
			}
			break
		}
	}
	f.Close()
	total.Done = ctx.Err() == nil
	s.put(dir, total)
	return total
}

// SortBy orders entries: "size" (largest first, by total for directories),
// "name", "mtime" (newest first), "count" (most files first).
func (s *Scanner) SortBy(ents []Entry, by string) {
	size := func(e Entry) int64 {
		if e.IsDir() {
			if st, ok := s.Get(e.Path); ok {
				return st.Alloc
			}
			return -1
		}
		return e.Alloc
	}
	count := func(e Entry) int64 {
		if st, ok := s.Get(e.Path); ok {
			return st.Files
		}
		return 0
	}
	sort.SliceStable(ents, func(i, j int) bool {
		a, b := ents[i], ents[j]
		switch by {
		case "name":
		case "mtime":
			if !a.MTime.Equal(b.MTime) {
				return a.MTime.After(b.MTime)
			}
		case "count":
			if ca, cb := count(a), count(b); ca != cb {
				return ca > cb
			}
		default:
			if sa, sb := size(a), size(b); sa != sb {
				return sa > sb
			}
		}
		return a.Name < b.Name
	})
}

// fscrypt: FS_IOC_GET_ENCRYPTION_POLICY_EX = _IOWR('f', 22, __u8[9])
const fsIocGetEncryptionPolicyEx = 0xc0096616

// Fscrypt reports whether a directory is encrypted with fscrypt (ext4,
// f2fs, ubifs): "v1" / "v2", "" when not, or not supported.
func Fscrypt(dir string) string {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return ""
	}
	defer unix.Close(fd)
	var arg struct {
		Size   uint64
		Policy [24]byte // fscrypt_policy_v2: version, modes, flags, log2 du size, reserved, key identifier
	}
	arg.Size = uint64(len(arg.Policy))
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), fsIocGetEncryptionPolicyEx, uintptr(unsafe.Pointer(&arg)))
	if errno != 0 {
		return ""
	}
	switch arg.Policy[0] {
	case 0:
		return "v1"
	case 2:
		return "v2"
	}
	return "yes"
}
