package tui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/noahzmr/tyr/internal/fsbrowse"
	"github.com/noahzmr/tyr/internal/fsck"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
)

// fileBrowser (B): a read-only, ncdu-like view of the filesystem - sizes
// on disk and apparent, file counts, owner, mode, dates; subtrees counted
// in the background on one filesystem; nothing can be changed.
type fileBrowser struct {
	dir       string
	self      fsbrowse.Entry
	ents      []fsbrowse.Entry
	shown     []int // indexes into ents after the filter
	sel       int
	sortBy    string
	filter    lineInput
	filtering bool
	moved     bool // the user moved the selection: keep it on its entry while sizes come in
	err       string
	remember  map[string]string // the entry selected in each directory
	users     map[uint32]string
	groups    map[uint32]string
	mounts    []fsck.Mount
}

type filesTickMsg struct{}

var sortOrders = []string{"size", "name", "mtime", "count"}

// fileScan is shared by every browser opened in this session: sizes once
// counted stay.
var fileScan = fsbrowse.NewScanner()

func (m *Model) openFiles(start string) tea.Cmd {
	if m.env != nil && m.env.FS != nil && !m.env.FS.IsHost() && fsckHostOnly {
		m.flash("the file browser works on the live host only (this is a fixture)")
		return nil
	}
	if start == "" {
		start = "/"
	}
	b := &fileBrowser{sortBy: "size", filter: lineInput{limit: 200}, remember: map[string]string{},
		users: idNames("/etc/passwd"), groups: idNames("/etc/group"), mounts: fsck.ReadMounts(hostfs.Host())}
	m.files = b
	b.open(start, "")
	return tea.Tick(300*time.Millisecond, func(time.Time) tea.Msg { return filesTickMsg{} })
}

// filesSubject: where B starts - the selected filesystem, or a mountpoint
// of the selected disk.
func (m *Model) filesSubject() string {
	st, ok := m.tabs[m.active].(*storageTab)
	if !ok || st.st == nil {
		return "/"
	}
	it, ok := st.lists[st.view].current()
	if !ok {
		return "/"
	}
	switch st.view {
	case 2:
		return it.key
	case 0:
		if d, ok := st.disk(it.key); ok && len(d.Mounted) > 0 {
			return d.Mounted[0]
		}
	}
	return "/"
}

func (b *fileBrowser) open(dir, selName string) {
	dir = filepath.Clean(dir)
	if b.dir != "" && len(b.shown) > 0 {
		b.remember[b.dir] = b.ents[b.shown[b.sel]].Name
	}
	self, ents, err := fsbrowse.List(dir)
	b.err = ""
	if err != nil {
		b.err = err.Error()
		if os.IsPermission(err) || strings.Contains(err.Error(), "permission denied") {
			b.err = "permission denied: run Tyr as root to look inside " + dir
		}
	}
	b.dir, b.self, b.ents, b.sel, b.shown = dir, self, ents, 0, nil
	b.moved = selName != "" || b.remember[dir] != ""
	b.filter.SetValue("")
	b.filtering = false
	fileScan.Scan(dir)
	b.resort(firstNonEmptyStr(selName, b.remember[dir]))
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// resort orders and filters the entries, keeping the selection on name.
func (b *fileBrowser) resort(name string) {
	if name == "" && b.moved && len(b.shown) > 0 && b.sel < len(b.shown) {
		name = b.ents[b.shown[b.sel]].Name
	}
	fileScan.SortBy(b.ents, b.sortBy)
	b.shown = b.shown[:0]
	f := strings.ToLower(b.filter.Value())
	for i, e := range b.ents {
		if f == "" || strings.Contains(strings.ToLower(e.Name), f) {
			b.shown = append(b.shown, i)
		}
	}
	b.sel = 0
	for i, k := range b.shown {
		if b.ents[k].Name == name {
			b.sel = i
		}
	}
}

func (b *fileBrowser) current() (fsbrowse.Entry, bool) {
	if b.sel < len(b.shown) {
		return b.ents[b.shown[b.sel]], true
	}
	return fsbrowse.Entry{}, false
}

func (m *Model) filesTick() tea.Cmd {
	if m.files == nil {
		return nil
	}
	m.files.resort("")
	return tea.Tick(300*time.Millisecond, func(time.Time) tea.Msg { return filesTickMsg{} })
}

func (m *Model) updateFiles(msg tea.KeyPressMsg) tea.Cmd {
	b := m.files
	k := msg.String()
	if b.filtering {
		switch k {
		case "enter":
			b.filtering = false
		case "esc":
			b.filtering = false
			b.filter.SetValue("")
		default:
			b.filter.Update(msg)
		}
		b.resort("")
		return nil
	}
	page := max(1, m.contentHeight()-8)
	switch k {
	case "up", "k", "down", "j", "pgup", "pgdown", "home", "g", "end", "G":
		b.moved = true
	}
	switch k {
	case "esc", "q":
		fileScan.Stop()
		m.files = nil
	case "up", "k":
		b.sel = max(0, b.sel-1)
	case "down", "j":
		b.sel = min(len(b.shown)-1, b.sel+1)
	case "pgup":
		b.sel = max(0, b.sel-page)
	case "pgdown":
		b.sel = max(0, min(len(b.shown)-1, b.sel+page))
	case "home", "g":
		b.sel = 0
	case "end", "G":
		b.sel = max(0, len(b.shown)-1)
	case "enter", "right", "l":
		if e, ok := b.current(); ok && e.IsDir() {
			b.open(e.Path, "")
		}
	case "left", "h", "backspace":
		if b.dir != "/" {
			b.open(filepath.Dir(b.dir), filepath.Base(b.dir))
		}
	case "s":
		for i, o := range sortOrders {
			if o == b.sortBy {
				b.sortBy = sortOrders[(i+1)%len(sortOrders)]
				break
			}
		}
		b.resort("")
		m.flash("sorted by " + b.sortBy)
	case "/":
		b.filtering = true
		b.filter.Focus()
	case "r":
		fileScan.Forget(b.dir)
		b.open(b.dir, "")
		m.flash("counting " + b.dir + " again")
	case "c":
		p := b.dir
		if e, ok := b.current(); ok {
			p = e.Path
		}
		cmd, txt := copyCmd(p, "the path")
		m.flash(txt)
		return cmd
	case "K", "shift+k":
		mp, _ := b.mountOf(b.dir)
		m.openFsck(mp.Source)
	}
	return nil
}

// mountOf is the mount the path lies on (the longest mountpoint prefix).
func (b *fileBrowser) mountOf(p string) (fsck.Mount, bool) {
	var best fsck.Mount
	ok := false
	for _, mt := range b.mounts {
		if (p == mt.Point || mt.Point == "/" || strings.HasPrefix(p, strings.TrimSuffix(mt.Point, "/")+"/")) && len(mt.Point) >= len(best.Point) {
			best, ok = mt, true
		}
	}
	return best, ok
}

func (m *Model) filesView(w, h int) string {
	t, b := m.theme, m.files
	var lines []string
	right := "sort " + b.sortBy
	if b.filter.Value() != "" || b.filtering {
		right = "filter " + b.filter.Value() + " · " + right
	}
	crumb := lipgloss.NewStyle().Foreground(t.Accent).Bold(true).Render("B Files") + t.Dim.Render(" › ") + t.Bold.Render(b.dir)
	lines = append(lines, padCell(crumb, w-lipgloss.Width(right)-1, false)+" "+t.Dim.Render(right))

	// the filesystem the directory is on, with what Tyr knows about it
	if mt, ok := b.mountOf(b.dir); ok {
		info := fmt.Sprintf("%s on %s · mounted at %s", mt.FSType, mt.Source, mt.Point)
		if m.snap != nil && m.snap.Storage != nil {
			for _, f := range m.snap.Storage.Filesystems {
				if f.Mountpoint != mt.Point {
					continue
				}
				if f.UsedPercent != nil {
					info += fmt.Sprintf(" · %s, %.0f%% used", bytesSI(f.SizeBytes), *f.UsedPercent)
				}
				if f.Encryption != nil {
					info += " · " + stripStyle(encText(t, f.Encryption))
				}
				if f.Health != nil && f.Health.Errors != nil && *f.Health.Errors > 0 {
					info += t.StatusText(model.StatusWarn, fmt.Sprintf(" · %d errors recorded (K check)", *f.Health.Errors))
				}
			}
		}
		lines = append(lines, t.Dim.Render(truncate(info, w)))
	} else {
		lines = append(lines, "")
	}
	total := ""
	if st, ok := fileScan.Get(b.dir); ok {
		pre := ""
		if !st.Done {
			pre = "≥ "
		}
		total = fmt.Sprintf("%s%s on disk · %s apparent · %s files · %s directories", pre, sizeH(st.Alloc), sizeH(st.Apparent),
			grouped(uint64(st.Files)), grouped(uint64(st.Dirs)))
		if st.Errors > 0 {
			total += t.StatusText(model.StatusWarn, fmt.Sprintf(" · %d unreadable", st.Errors))
		}
	}
	if files, _, cur, running := fileScan.Progress(); running {
		total += t.Dim.Render(fmt.Sprintf("  %s counting… %s files · %s", m.spin.View(), grouped(uint64(files)), truncate(cur, max(10, w/3))))
	}
	lines = append(lines, truncate(total, w), "")

	if b.err != "" {
		lines = append(lines, t.StatusText(model.StatusWarn, b.err))
	}
	// the list
	wide := w >= 120
	nameW := w - 2 - 10 - 1 - 12 - 1 - 10 - 1 - 8 - 1 - 16
	if wide {
		nameW -= 1 + 16 + 1 + 10
	}
	nameW = max(12, nameW)
	head := fmt.Sprintf("  %s %10s %-12s %10s %8s %-16s", padCell("Name", nameW, false), "On disk", "", "Apparent", "Files", "Modified")
	if wide {
		head += fmt.Sprintf(" %-16s %-10s", "Owner", "Mode")
	}
	lines = append(lines, t.Dim.Render(head))
	detailH := 4
	rows := max(3, h-len(lines)-detailH-1)
	start := max(0, min(b.sel-rows/2, len(b.shown)-rows))
	var parent int64
	if st, ok := fileScan.Get(b.dir); ok {
		parent = st.Alloc
	}
	for i := start; i < len(b.shown) && i < start+rows; i++ {
		e := b.ents[b.shown[i]]
		lines = append(lines, b.row(t, e, i == b.sel, nameW, parent, wide, w))
	}
	if len(b.shown) == 0 && b.err == "" {
		lines = append(lines, t.Sub.Render("  (empty)"))
	}
	for len(lines) < h-detailH {
		lines = append(lines, "")
	}
	lines = append(lines, b.detail(t, w)...)
	if b.filtering {
		lines = append(lines, "/"+b.filter.Field(t, min(40, w-2)))
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

func (b *fileBrowser) row(t *Theme, e fsbrowse.Entry, cur bool, nameW int, parent int64, wide bool, w int) string {
	name := e.Name
	var size, app, files string
	var alloc int64 = -1
	switch {
	case e.Err != "":
		size = t.StatusText(model.StatusWarn, "?")
	case e.IsDir() && e.OtherFS:
		name += "/"
		size = t.Dim.Render("other fs")
	case e.IsDir():
		name += "/"
		if st, ok := fileScan.Get(e.Path); ok {
			pre := ""
			if !st.Done {
				pre = "≥"
			}
			size, app, files = pre+sizeH(st.Alloc), sizeH(st.Apparent), grouped(uint64(st.Files))
			alloc = st.Alloc
		} else {
			size = t.Dim.Render("…")
		}
	default:
		size, app, alloc = sizeH(e.Alloc), sizeH(e.Size), e.Alloc
		if e.Mode&fs.ModeSymlink != 0 {
			name += t.Dim.Render(" → " + e.LinkTarget)
		}
	}
	bar := strings.Repeat(" ", 12)
	if alloc >= 0 && parent > 0 {
		n := min(10, int(float64(alloc)/float64(parent)*10+0.5))
		bar = t.Dim.Render("[") + lipgloss.NewStyle().Foreground(t.Accent).Render(strings.Repeat("■", n)) + t.Dim.Render(strings.Repeat("·", 10-n)+"]")
	}
	mod := ""
	if !e.MTime.IsZero() {
		mod = e.MTime.Format("2006-01-02 15:04")
	}
	nm := name
	if e.IsDir() {
		nm = t.Bold.Render(name)
	}
	r := fmt.Sprintf("%s %10s %s %10s %8s %-16s", padCell(truncate(nm, nameW), nameW, false), size, bar, app, files, mod)
	if wide {
		r += fmt.Sprintf(" %-16s %-10s", truncate(b.owner(e), 16), e.Mode.String())
	}
	if cur {
		return lipgloss.NewStyle().Foreground(t.Accent).Render("▌") + " " + truncate(r, w-2)
	}
	return "  " + truncate(r, w-2)
}

func (b *fileBrowser) owner(e fsbrowse.Entry) string {
	u, g := b.users[e.UID], b.groups[e.GID]
	if u == "" {
		u = strconv.Itoa(int(e.UID))
	}
	if g == "" {
		g = strconv.Itoa(int(e.GID))
	}
	return u + ":" + g
}

// detail: everything lstat says about the selected entry.
func (b *fileBrowser) detail(t *Theme, w int) []string {
	e, ok := b.current()
	if !ok {
		return []string{t.HeadingRight("Details", "", "", w), "", "", ""}
	}
	kind := "file"
	switch {
	case e.Mode&fs.ModeSymlink != 0:
		kind = "symlink → " + e.LinkTarget
	case e.IsDir() && e.OtherFS:
		kind = "directory · mount point of another filesystem"
	case e.IsDir():
		kind = "directory"
		if v := fsbrowse.Fscrypt(e.Path); v != "" {
			kind += " · fscrypt-encrypted (" + v + ")"
		}
	case e.Mode&fs.ModeDevice != 0:
		kind = "device"
	case e.Mode&fs.ModeNamedPipe != 0:
		kind = "fifo"
	case e.Mode&fs.ModeSocket != 0:
		kind = "socket"
	}
	size := ""
	if !e.IsDir() {
		size = fmt.Sprintf("%s apparent · %s on disk", sizeH(e.Size), sizeH(e.Alloc))
		if e.Size > 0 && e.Alloc*10 < e.Size {
			size += t.Dim.Render(fmt.Sprintf(" · sparse (%.0f%% allocated)", float64(e.Alloc)/float64(e.Size)*100))
		}
		if e.Nlink > 1 {
			size += t.Dim.Render(fmt.Sprintf(" · %d hard links", e.Nlink))
		}
	} else if st, ok := fileScan.Get(e.Path); ok {
		size = fmt.Sprintf("%s on disk · %s apparent · %s files · %s dirs", sizeH(st.Alloc), sizeH(st.Apparent),
			grouped(uint64(st.Files)), grouped(uint64(st.Dirs)))
	}
	if e.Err != "" {
		return []string{t.HeadingRight("Details", "", "", w), truncate(e.Path, w), t.StatusText(model.StatusWarn, e.Err), ""}
	}
	return []string{
		t.HeadingRight("Details", "", "c copy path · K check this filesystem", w),
		truncate(t.Bold.Render(e.Path)+t.Dim.Render("  "+kind), w),
		truncate(size, w),
		truncate(t.Dim.Render(fmt.Sprintf("%s %s · inode %d · modified %s · changed %s · accessed %s", e.Mode.String(), b.owner(e), e.Ino,
			e.MTime.Format("2006-01-02 15:04:05"), e.CTime.Format("2006-01-02 15:04"), e.ATime.Format("2006-01-02 15:04"))), w),
	}
}

// idNames reads uid/gid → name from /etc/passwd or /etc/group.
func idNames(p string) map[uint32]string {
	out := map[uint32]string{}
	b, err := os.ReadFile(p)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		x := strings.Split(line, ":")
		if len(x) > 2 {
			if id, err := strconv.ParseUint(x[2], 10, 32); err == nil {
				out[uint32(id)] = x[0]
			}
		}
	}
	return out
}

// sizeH is a byte count for the browser: 512 B, 4.2 MB, 37 GB.
func sizeH(b int64) string {
	if b < 1000 {
		return fmt.Sprintf("%d B", b)
	}
	f, u := float64(b), "B"
	for _, unit := range []string{"kB", "MB", "GB", "TB", "PB"} {
		f, u = f/1000, unit
		if f < 1000 {
			break
		}
	}
	if f < 10 {
		return fmt.Sprintf("%.1f %s", f, u)
	}
	return fmt.Sprintf("%.0f %s", f, u)
}
