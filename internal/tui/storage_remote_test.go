package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestStorageNetworkView(t *testing.T) {
	m := newLoaded(t, "k8s-longhorn", 150, 50)
	m.active = 3
	run(m, "v", "v", "v")
	s := ansi.Strip(screen(m))
	for _, want := range []string{"v Disks · RAID · Filesystems · Network", "Longhorn (2 volumes)", "session3", "session4", "FAILED", "gone", "cni0",
		"Longhorn v1 on k3s · 1 check fails", "enter timeline and logs", "the filesystem on sdh went read-only"} {
		if !strings.Contains(s, want) {
			t.Errorf("network view missing %q\n%s", want, s)
		}
	}
	// the node page: checks with the fix, Longhorn's volumes, data paths, replicas, logs, timeline
	m = newLoaded(t, "k8s-longhorn", 150, 50)
	m.active = 3
	run(m, "v", "v", "v")
	st := m.tabs[3].(*storageTab)
	if !st.lists[3].focus("multipathd") {
		t.Fatal("no node row")
	}
	_, page := st.Detail(m.theme, 150)
	page = ansi.Strip(page)
	for _, want := range []string{"Node checks", "To fix", `devnode "^sd[a-z0-9]+"`, "Volumes attached here", "READ-ONLY", "Data paths",
		"nvme0n1,nvme1n1", "failed: failed to write volume-head-003.img", "instance-manager  4×", "(9× since", "storage/node_multipathd"} {
		if !strings.Contains(page, want) {
			t.Errorf("node page missing %q\n%s", want, page)
		}
	}
	// no Network view without network storage
	h := newLoaded(t, "server-healthy", 150, 50)
	h.active = 3
	if s := ansi.Strip(screen(h)); strings.Contains(s, "Network") && strings.Contains(s, "Filesystems · Network") {
		t.Error("Network view on a host without network storage")
	}
	run(h, "v", "v", "v")
	if hs := h.tabs[3].(*storageTab); hs.view != 0 {
		t.Errorf("view %d after cycling three views", hs.view)
	}
}
