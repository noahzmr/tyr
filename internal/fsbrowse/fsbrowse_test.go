package fsbrowse

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScan(t *testing.T) {
	root := t.TempDir()
	w := func(p string, n int) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		if err := os.WriteFile(filepath.Join(root, p), make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("a/one", 10000)
	w("a/b/two", 20000)
	w("c/three", 5000)
	os.Link(filepath.Join(root, "a/one"), filepath.Join(root, "c/one-link")) // counted once
	os.Symlink("a/one", filepath.Join(root, "sym"))
	sp, _ := os.Create(filepath.Join(root, "sparse.img"))
	sp.Truncate(1 << 30) // 1 GiB apparent, almost nothing allocated
	sp.Close()

	s := NewScanner()
	s.Scan(root)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if st, ok := s.Get(root); ok && st.Done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, _ := s.Get(root)
	if st.Apparent != 10000+20000+5000+(1<<30)+int64(len("a/one")) || st.Files != 6 || st.Dirs != 3 || st.Errors != 0 {
		t.Errorf("root %+v", st)
	}
	if st.Alloc >= 1<<29 {
		t.Errorf("sparse file counted by its apparent size: alloc %d", st.Alloc)
	}
	if a, ok := s.Get(filepath.Join(root, "a")); !ok || !a.Done || a.Apparent != 30000 || a.Files != 2 || a.Dirs != 1 {
		t.Errorf("a %+v", a)
	}
	_, ents, err := List(root)
	if err != nil || len(ents) != 4 {
		t.Fatalf("list %v %v", ents, err)
	}
	s.SortBy(ents, "size")
	if ents[0].Name != "a" { // by allocated size: a (30 kB) before the sparse image
		t.Errorf("order %s %s %s %s", ents[0].Name, ents[1].Name, ents[2].Name, ents[3].Name)
	}
	for _, e := range ents {
		if e.Name == "sym" && e.LinkTarget != "a/one" {
			t.Errorf("symlink %+v", e)
		}
	}
	s.Forget(filepath.Join(root, "a"))
	if _, ok := s.Get(root); ok {
		t.Error("parent total kept after forgetting a child")
	}
	if Fscrypt(root) != "" {
		t.Error("tmp dir reported as fscrypt-encrypted")
	}
}
