package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/fsbrowse"
)

func waitScan(t *testing.T, dir string) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if st, ok := fileScan.Get(dir); ok && st.Done {
			return
		}
	}
	t.Fatal("scan did not finish")
}

func TestFileBrowser(t *testing.T) {
	fsckHostOnly = false
	t.Cleanup(func() { fsckHostOnly = true })
	fileScan = fsbrowse.NewScanner()
	root := t.TempDir()
	for p, n := range map[string]int{"big/data.bin": 3 << 20, "big/sub/more.bin": 1 << 20, "small/notes.txt": 1200, "zz.log": 5000} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), make([]byte, n), 0o644)
	}
	for _, sz := range [][2]int{{80, 24}, {160, 50}} {
		m := newLoaded(t, "server-healthy", sz[0], sz[1])
		m.openFiles(root)
		waitScan(t, root)
		m.Update(filesTickMsg{})
		s := screen(m)
		if m.files == nil || !strings.Contains(s, "B Files") || !strings.Contains(s, "big/") || !strings.Contains(s, "4.2 MB") {
			t.Fatalf("%v:\n%s", sz, s)
		}
		checkFits(t, "files", s, sz[0], sz[1])
		// largest first: big/ is selected; enter opens it, ← returns to it
		m.Update(press("enter"))
		if m.files.dir != filepath.Join(root, "big") || !strings.Contains(screen(m), "data.bin") {
			t.Fatalf("enter: %s\n%s", m.files.dir, screen(m))
		}
		m.Update(press("left"))
		if e, _ := m.files.current(); m.files.dir != root || e.Name != "big" {
			t.Fatalf("back: %s %s", m.files.dir, e.Name)
		}
		m.Update(press("s")) // by name
		if e, _ := m.files.current(); e.Name != "big" || m.files.sortBy != "name" || m.files.ents[m.files.shown[0]].Name != "big" {
			t.Fatalf("sort by name: %+v", m.files.shown)
		}
		m.Update(press("/"))
		typeText(m, "zz")
		m.Update(press("enter"))
		if len(m.files.shown) != 1 {
			t.Fatalf("filter: %d shown", len(m.files.shown))
		}
		m.Update(press("esc"))
		if m.files != nil {
			t.Fatal("esc closes the browser")
		}
	}
}
