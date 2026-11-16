// Package hostfs provides read-only access to the host's /proc, /sys and /dev
// through a root prefix, so collectors can run against the real system ("/")
// or against a fixture tree in tests and --fixtures mode.
package hostfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// FS is a read-only view of a host filesystem. All paths are absolute host
// paths such as "/sys/devices/system/cpu/online".
type FS interface {
	ReadFile(path string) ([]byte, error)
	ReadDir(path string) ([]fs.DirEntry, error)
	Stat(path string) (fs.FileInfo, error)
	Lstat(path string) (fs.FileInfo, error)
	Readlink(path string) (string, error)
	// EvalSymlinks resolves path to its canonical host path.
	EvalSymlinks(path string) (string, error)
	Glob(pattern string) ([]string, error)
	// Open opens a file for reading (used for /dev/kmsg and block devices).
	Open(path string) (*os.File, error)
	// Readable reports whether the current process may read path.
	Readable(path string) bool
	// Real returns the path on the actual filesystem.
	Real(path string) string
	// IsHost reports whether this is the live host ("/") rather than a fixture.
	IsHost() bool
}

// New returns an FS rooted at root. Use "/" for the live host.
func New(root string) FS {
	root = filepath.Clean(root)
	if root == "/" {
		return rooted{root: ""}
	}
	return rooted{root: root}
}

// Host returns the live host filesystem.
func Host() FS { return New("/") }

type rooted struct{ root string }

func (r rooted) Real(p string) string {
	if r.root == "" {
		return filepath.Clean(p)
	}
	return filepath.Join(r.root, filepath.Clean("/"+p))
}

func (r rooted) IsHost() bool { return r.root == "" }

func (r rooted) ReadFile(p string) ([]byte, error) { return os.ReadFile(r.Real(p)) }
func (r rooted) ReadDir(p string) ([]fs.DirEntry, error) {
	return os.ReadDir(r.Real(p))
}
func (r rooted) Stat(p string) (fs.FileInfo, error)  { return os.Stat(r.Real(p)) }
func (r rooted) Lstat(p string) (fs.FileInfo, error) { return os.Lstat(r.Real(p)) }
func (r rooted) Readlink(p string) (string, error)   { return os.Readlink(r.Real(p)) }
func (r rooted) Open(p string) (*os.File, error)     { return os.Open(r.Real(p)) }

func (r rooted) EvalSymlinks(p string) (string, error) {
	real, err := filepath.EvalSymlinks(r.Real(p))
	if err != nil {
		return "", err
	}
	return r.strip(real)
}

func (r rooted) Glob(pattern string) ([]string, error) {
	m, err := filepath.Glob(r.Real(pattern))
	if err != nil {
		return nil, err
	}
	out := m[:0]
	for _, p := range m {
		if s, err := r.strip(p); err == nil {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r rooted) Readable(p string) bool {
	if r.root != "" {
		// Fixture trees are always readable by the developer; privileges
		// are simulated by the fixture manifest instead.
		_, err := os.Stat(r.Real(p))
		return err == nil
	}
	return unix.Access(p, unix.R_OK) == nil
}

func (r rooted) strip(real string) (string, error) {
	if r.root == "" {
		return real, nil
	}
	rel, err := filepath.Rel(r.root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", errors.New("hostfs: path escapes fixture root: " + real)
	}
	return "/" + rel, nil
}

// String reads a file and returns its content with surrounding whitespace trimmed.
func String(f FS, p string) (string, error) {
	b, err := f.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// StringOr returns the trimmed content of p, or def on any error.
func StringOr(f FS, p, def string) string {
	s, err := String(f, p)
	if err != nil {
		return def
	}
	return s
}

// Int reads a decimal integer from p.
func Int(f FS, p string) (int64, error) {
	s, err := String(f, p)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(s, 10, 64)
}

// Uint reads a decimal unsigned integer from p.
func Uint(f FS, p string) (uint64, error) {
	s, err := String(f, p)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(s, 10, 64)
}

// Exists reports whether p exists (following symlinks).
func Exists(f FS, p string) bool {
	_, err := f.Stat(p)
	return err == nil
}

// IsPermission reports whether err is a permission error.
func IsPermission(err error) bool { return errors.Is(err, fs.ErrPermission) }
