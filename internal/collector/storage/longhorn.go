package storage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

// detectLonghorn reports a Longhorn node from what is visible locally:
// data paths (longhorn-disk.cfg), replica and engine processes, sessions to
// Longhorn volumes, /dev/longhorn and the pods' logs. nil: no Longhorn.
func detectLonghorn(f hostfs.FS, mounts []sysfs.Mount, sessions []sysfs.Session, procs []proc) *model.Longhorn {
	lh := &model.Longhorn{Evidence: []string{}}
	paths := map[string]bool{}
	cands := []string{"/var/lib/longhorn"}
	for _, m := range mounts {
		mp := m.Mountpoint
		if strings.HasPrefix(mp, "/var/lib/kubelet") || slices.ContainsFunc([]string{"/proc", "/sys", "/dev", "/run", "/snap"}, func(p string) bool { return mp == p || strings.HasPrefix(mp, p+"/") }) {
			continue
		}
		cands = append(cands, mp, path.Join(mp, "longhorn"))
	}
	v1, v2 := false, false
	for _, p := range procs {
		switch {
		case p.name() == "longhorn" && len(p.argv) > 2 && p.argv[1] == "replica":
			dir := strings.TrimPrefix(p.argv[2], "/host") // the instance-manager sees the host under /host
			cands = append(cands, path.Dir(path.Dir(dir)))
			v1 = true
		case p.name() == "longhorn" && len(p.argv) > 1 && p.argv[1] == "controller":
			v1 = true
		case p.name() == "spdk_tgt":
			v2 = true
		}
	}
	for _, c := range cands {
		if !paths[c] && hostfs.Exists(f, path.Join(c, "longhorn-disk.cfg")) {
			paths[c] = true
			lh.Disks = append(lh.Disks, model.LHDisk{Path: c})
		}
	}
	if len(lh.Disks) > 0 {
		lh.Evidence = append(lh.Evidence, fmt.Sprintf("%d data path(s) with longhorn-disk.cfg", len(lh.Disks)))
	}
	n := 0
	for _, s := range sessions {
		if LonghornVolume(s.Target) != "" {
			n++
			if strings.HasPrefix(s.Transport, "nvme") {
				v2 = true
			} else {
				v1 = true
			}
		}
	}
	if n > 0 {
		lh.Evidence = append(lh.Evidence, fmt.Sprintf("%d session(s) to Longhorn volumes", n))
	}
	if running(procs, "longhorn-manager", "longhorn-instance-manager") || v1 || v2 {
		lh.Evidence = append(lh.Evidence, "Longhorn processes")
	}
	if hostfs.Exists(f, "/dev/longhorn") {
		lh.Evidence = append(lh.Evidence, "/dev/longhorn")
	}
	if pods, _ := f.Glob("/var/log/pods/longhorn-system_*"); len(pods) > 0 {
		lh.Evidence = append(lh.Evidence, "Longhorn pods' logs in /var/log/pods")
	}
	if len(lh.Evidence) == 0 {
		return nil
	}
	switch {
	case v1 && v2:
		lh.Engine = "v1+v2"
	case v2:
		lh.Engine = "v2"
	case v1:
		lh.Engine = "v1"
	}
	lh.Kubernetes = kubernetes(f, procs)
	return lh
}

// kubernetes names the node's Kubernetes: k3s, rke2 or kubelet.
func kubernetes(f hostfs.FS, procs []proc) string {
	switch {
	case running(procs, "k3s") || hostfs.Exists(f, "/var/lib/rancher/k3s"):
		return "k3s"
	case running(procs, "rke2") || hostfs.Exists(f, "/var/lib/rancher/rke2"):
		return "rke2"
	case running(procs, "kubelet") || hostfs.Exists(f, "/var/lib/kubelet"):
		return "kubelet"
	}
	return ""
}

// jline is a journal message of iscsid, multipathd or the kubelet.
type jline struct {
	t        time.Time
	source   string
	msg      string
	priority int
}

// journalPattern selects the volume-related messages (lowercase: journalctl
// matches it case-insensitively).
const journalPattern = `iscsi|longhorn|multipath|mountvolume|mountdevice|attachvolume|unmountvolume|unmountdevice|mapvolume|connection[0-9]+:[0-9]+|nvme|already mounted|mount point busy|error|fail`

var journalIDs = []string{"iscsid", "multipathd", "kubelet", "k3s", "rke2"}

// nodeJournal reads the last 24 hours of iscsid, multipathd and the
// kubelet (k3s, rke2) from the journal.
func nodeJournal(ctx context.Context, env *collector.Env, _ bool) []jline {
	if _, err := env.Exec.LookPath("journalctl"); err != nil {
		return nil
	}
	base := []string{"-o", "json", "--no-pager", "-q", "--since=-24h", "-n", "5000"}
	var ids []string
	for i, id := range journalIDs {
		if i > 0 {
			ids = append(ids, "+")
		}
		ids = append(ids, "SYSLOG_IDENTIFIER="+id)
	}
	b, err := env.Exec.Run(ctx, "journalctl", append(append(append([]string{}, base...), "-g", journalPattern), ids...)...)
	if err != nil && len(b) == 0 { // journalctl without pattern support
		b, _ = env.Exec.Run(ctx, "journalctl", append(append([]string{}, base...), ids...)...)
	}
	var out []jline
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var e map[string]any
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		l := jline{source: str(e["SYSLOG_IDENTIFIER"]), msg: journalMessage(e["MESSAGE"]), priority: 6}
		if l.msg == "" {
			continue
		}
		if p, err := strconv.Atoi(str(e["PRIORITY"])); err == nil {
			l.priority = p
		}
		if us, err := strconv.ParseInt(str(e["__REALTIME_TIMESTAMP"]), 10, 64); err == nil {
			l.t = time.UnixMicro(us).UTC()
		}
		out = append(out, l)
	}
	return out
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

// journalMessage: MESSAGE is a string, or an array of bytes when it is not
// valid UTF-8.
func journalMessage(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		b := make([]byte, 0, len(x))
		for _, c := range x {
			if n, ok := c.(float64); ok {
				b = append(b, byte(n))
			}
		}
		return string(b)
	}
	return ""
}

var (
	pvcRE       = regexp.MustCompile(`pvc-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	klogRE      = regexp.MustCompile(`(?s)^([EWIF])\d{4} \d\d:\d\d:\d\d\.\d+\s+\d+ [^\]]+\] (.*)$`)
	logrusLevel = regexp.MustCompile(`\blevel=(\w+)`)
	logrusMsg   = regexp.MustCompile(`\bmsg="((?:[^"\\]|\\.)*)"`)
	logrusErr   = regexp.MustCompile(`\b(?:error|err)="((?:[^"\\]|\\.)*)"`)
	foldDigits  = regexp.MustCompile(`[0-9a-f]{6,}|\d+`)
)

// parseLevel finds the level and the message of a kubelet (klog), Longhorn
// (logrus text or JSON) or plain log line.
func parseLevel(msg string, priority int) (level, text string) {
	text = strings.TrimSpace(msg)
	if m := klogRE.FindStringSubmatch(text); m != nil {
		text = m[2]
		switch m[1] {
		case "E", "F":
			return "error", text
		case "W":
			return "warning", text
		}
		return "info", text
	}
	if strings.HasPrefix(text, "{") {
		var j map[string]any
		if json.Unmarshal([]byte(text), &j) == nil {
			l := strings.ToLower(str(j["level"]))
			t := firstNonEmpty(str(j["msg"]), str(j["message"]))
			if e := firstNonEmpty(str(j["error"]), str(j["err"])); e != "" {
				t += ": " + e
			}
			return normLevel(l), t
		}
	}
	if m := logrusLevel.FindStringSubmatch(text); m != nil {
		t := text
		if mm := logrusMsg.FindStringSubmatch(text); mm != nil {
			t = unquote(mm[1])
			if e := logrusErr.FindStringSubmatch(text); e != nil {
				t += ": " + unquote(e[1])
			}
		}
		return normLevel(m[1]), t
	}
	switch {
	case priority <= 3:
		return "error", text
	case priority == 4:
		return "warning", text
	}
	return "info", text
}

func normLevel(l string) string {
	switch strings.ToLower(l) {
	case "error", "fatal", "panic", "err", "crit":
		return "error"
	case "warning", "warn":
		return "warning"
	}
	return "info"
}

func unquote(s string) string {
	if u, err := strconv.Unquote(`"` + s + `"`); err == nil {
		return u
	}
	return s
}

// maxNodeLogs bounds the folded log lines kept.
const maxNodeLogs = 80

// logFolder folds repeated messages.
type logFolder struct {
	by    map[string]*model.NodeLogEntry
	order []*model.NodeLogEntry
}

func (lf *logFolder) add(t time.Time, source, level, msg, raw string) {
	if level != "error" && level != "warning" {
		return
	}
	vol := pvcRE.FindString(raw) // the message alone may have lost it (logrus volume=…)
	key := source + "|" + level + "|" + vol + "|" + foldDigits.ReplaceAllString(pvcRE.ReplaceAllString(msg, ""), "#")
	if e, ok := lf.by[key]; ok {
		e.Count++
		if !t.Before(e.Time) {
			e.Time, e.Message = t, msg
		}
		return
	}
	e := &model.NodeLogEntry{Time: t, Source: source, Level: level, Message: clip(msg, 400), Count: 1, Volume: vol}
	lf.by[key] = e
	lf.order = append(lf.order, e)
}

func (lf *logFolder) entries() []model.NodeLogEntry {
	sort.SliceStable(lf.order, func(i, j int) bool { return lf.order[i].Time.Before(lf.order[j].Time) })
	out := make([]model.NodeLogEntry, 0, len(lf.order))
	for _, e := range lf.order {
		out = append(out, *e)
	}
	if len(out) > maxNodeLogs {
		out = out[len(out)-maxNodeLogs:]
	}
	return out
}

// nodeLogs folds the journal's volume errors and, on a Longhorn node, the
// errors in the Longhorn pods' logs of the last 24 hours.
func nodeLogs(env *collector.Env, jl []jline, longhorn bool) []model.NodeLogEntry {
	lf := &logFolder{by: map[string]*model.NodeLogEntry{}}
	for _, l := range jl {
		if l.source == "iscsid" && inTimeline(l.msg) {
			continue
		}
		lvl, text := parseLevel(l.msg, l.priority)
		if _, after, ok := strings.Cut(text, " Error: "); ok && l.source != "iscsid" { // the kubelet: what failed
			text = after
		}
		src := l.source
		if src == "k3s" || src == "rke2" {
			src += " (kubelet)"
		}
		lf.add(l.t, src, lvl, text, l.msg)
	}
	if longhorn {
		since := env.Now().Add(-24 * time.Hour)
		for _, file := range podLogFiles(env.FS) {
			src := path.Base(path.Dir(file)) // the container
			for _, l := range tailLines(env.FS, file, 512<<10) {
				t, msg, ok := parseCRI(l)
				if !ok || t.Before(since) {
					continue
				}
				lvl, text := parseLevel(msg, 6)
				if lvl != "error" { // the pods' warnings are mostly routine
					continue
				}
				lf.add(t, src, lvl, text, msg)
			}
		}
	}
	return lf.entries()
}

// inTimeline reports an iscsid message the timeline already shows.
func inTimeline(msg string) bool {
	return slices.ContainsFunc(iscsidRules, func(r remoteRule) bool { return r.re.MatchString(msg) })
}

// podLogFiles returns the newest log of each container of the Longhorn
// pods (longhorn-manager, instance-manager, longhorn-csi-plugin, …).
func podLogFiles(f hostfs.FS) []string {
	conts, _ := f.Glob("/var/log/pods/longhorn-system_*/*")
	var out []string
	for _, c := range conts {
		logs, _ := f.Glob(c + "/*.log")
		if len(logs) == 0 {
			continue
		}
		sort.Slice(logs, func(i, j int) bool { return natLess(path.Base(logs[i]), path.Base(logs[j])) })
		out = append(out, logs[len(logs)-1]) // 0.log, 1.log … after a restart: the highest is the current one
	}
	return out
}

func natLess(a, b string) bool {
	na, ea := strconv.Atoi(strings.TrimSuffix(a, ".log"))
	nb, eb := strconv.Atoi(strings.TrimSuffix(b, ".log"))
	if ea == nil && eb == nil {
		return na < nb
	}
	return a < b
}

// tailLines reads the last max bytes of a file as lines.
func tailLines(f hostfs.FS, p string, max int64) []string {
	fh, err := f.Open(p)
	if err != nil {
		return nil
	}
	defer fh.Close()
	if st, err := fh.Stat(); err == nil && st.Size() > max {
		if _, err := fh.Seek(-max, io.SeekEnd); err != nil {
			return nil
		}
	}
	b, err := io.ReadAll(fh)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(b), "\n")
	if len(b) >= int(max) && len(lines) > 0 {
		lines = lines[1:] // the first one is cut
	}
	return lines
}

// parseCRI splits a container log line: "<RFC3339Nano> <stream> <P|F> <message>".
func parseCRI(l string) (time.Time, string, bool) {
	parts := strings.SplitN(l, " ", 4)
	if len(parts) < 4 {
		return time.Time{}, "", false
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", false
	}
	return t.UTC(), parts[3], true
}

// mountOps are the kubelet's volume operations that fail visibly to a pod.
var mountOps = regexp.MustCompile(`(MountVolume\.SetUp|MountVolume\.MountDevice|MapVolume\.\w+|AttachVolume\.Attach|UnmountVolume\.TearDown|UnmountDevice|MountVolume\.WaitForAttach) failed`)

var podRE = regexp.MustCompile(`pod "([^"]+)"`)

// mountCause is the reason of a failed volume operation: what the CSI
// driver answered ("desc = …"), else the text after "Error: ".
func mountCause(msg string) string {
	c := msg
	if _, after, ok := strings.Cut(msg, "desc = "); ok {
		c = after
	} else if _, after, ok := strings.Cut(msg, "Error: "); ok {
		c = after
	}
	return strings.Join(strings.Fields(c), " ")
}

// mountFindings: volumes the kubelet could not attach, mount or unmount.
func mountFindings(h *model.Header, logs []model.NodeLogEntry) {
	type agg struct {
		op, last string
		n        int
		t        time.Time
	}
	by := map[string]*agg{}
	var order []string
	for _, l := range logs {
		if !strings.Contains(l.Source, "kubelet") {
			continue
		}
		m := mountOps.FindStringSubmatch(l.Message)
		if m == nil {
			continue
		}
		vol := firstNonEmpty(l.Volume, "a volume")
		a := by[vol]
		if a == nil {
			a = &agg{}
			by[vol] = a
			order = append(order, vol)
		}
		a.n += l.Count
		if !l.Time.Before(a.t) {
			a.t, a.op, a.last = l.Time, m[1], l.Message
		}
	}
	for i, vol := range order {
		if i == 5 {
			h.Find(model.StatusWarn, "volume_mount_failed", "kubelet", "journal", fmt.Sprintf("… %d more volumes the kubelet could not mount", len(order)-5))
			break
		}
		a := by[vol]
		pod := ""
		if m := podRE.FindStringSubmatch(a.last); m != nil {
			pod = " (pod " + m[1] + ")"
		}
		h.Find(model.StatusWarn, "volume_mount_failed", vol, "journal", fmt.Sprintf("kubelet: %s failed for %s%s %d× in 24 h, last %s: %s", a.op, vol, pod, a.n, when(a.t), clip(mountCause(a.last), 220)))
	}
}

// longhornDetails fills the volumes, data disks and replicas.
func longhornDetails(env *collector.Env, out *model.Storage, lh *model.Longhorn, rs *model.RemoteStorage, mounts []sysfs.Mount) {
	f := env.FS
	for _, s := range rs.Sessions {
		if s.Longhorn == "" || s.Gone() || len(s.Disks) == 0 { // an orphaned session is no attached volume
			continue
		}
		v := model.LHVolume{Name: s.Longhorn, Session: s.ID, Status: s.Status}
		if len(s.Disks) > 0 {
			v.Device = s.Disks[0]
		}
		for _, m := range mounts {
			if m.Source == "/dev/longhorn/"+v.Name || v.Device != "" && m.Source == "/dev/"+v.Device {
				v.Mounts = append(v.Mounts, m.Mountpoint)
				if m.SuperRO && !slices.Contains([]string{"squashfs", "iso9660"}, m.FSType) {
					v.ReadOnly = true
				}
			}
		}
		if v.ReadOnly {
			v.Status = model.StatusCrit
		}
		lh.Volumes = append(lh.Volumes, v)
	}
	for i := range lh.Disks {
		d := &lh.Disks[i]
		var cfg struct {
			UUID string `json:"diskUUID"`
		}
		if b, err := f.ReadFile(path.Join(d.Path, "longhorn-disk.cfg")); err == nil && json.Unmarshal(b, &cfg) == nil {
			d.UUID = cfg.UUID
		}
		var best *sysfs.Mount
		for j := range mounts {
			m := &mounts[j]
			if (d.Path == m.Mountpoint || strings.HasPrefix(d.Path, strings.TrimSuffix(m.Mountpoint, "/")+"/")) && (best == nil || len(m.Mountpoint) > len(best.Mountpoint)) {
				best = m
			}
		}
		if best != nil {
			d.Device, d.FSType = best.Source, best.FSType
			d.Disks = baseDisks(f, strings.TrimPrefix(best.Source, "/dev/"))
			d.Disks = mdMembers(out, d.Disks)
			for _, fs := range out.Filesystems {
				if fs.Source == best.Source {
					d.SizeBytes, d.UsedPercent = fs.SizeBytes, fs.UsedPercent
				}
			}
		}
		metas, _ := f.Glob(path.Join(d.Path, "replicas", "*", "volume.meta"))
		sort.Strings(metas)
		for _, m := range metas {
			dir := path.Base(path.Dir(m))
			r := model.LHReplica{Dir: dir, Volume: replicaVolume(dir), Disk: d.Path}
			var meta struct {
				Size       uint64 `json:"Size"`
				Rebuilding bool   `json:"Rebuilding"`
				Error      string `json:"Error"`
			}
			if b, err := f.ReadFile(m); err == nil && json.Unmarshal(b, &meta) == nil {
				r.SizeBytes, r.Rebuilding, r.Error = meta.Size, meta.Rebuilding, meta.Error
			}
			lh.Replicas = append(lh.Replicas, r)
			d.Replicas++
		}
	}
}

// mdMembers replaces software RAID arrays by their members' disks (from
// /proc/mdstat, where sysfs holds no slaves).
func mdMembers(out *model.Storage, disks []string) []string {
	var res []string
	for _, n := range disks {
		var a *model.MDArray
		for i := range out.MDArrays {
			if out.MDArrays[i].Name == n {
				a = &out.MDArrays[i]
			}
		}
		if a == nil || len(a.Members) == 0 {
			res = append(res, n)
			continue
		}
		for _, m := range a.Members {
			disk := m.Device
			for _, d := range out.Disks { // nvme0n1p2 -> nvme0n1, sda1 -> sda
				if strings.HasPrefix(m.Device, d.Name) && len(d.Name) > 0 && (disk == m.Device || len(d.Name) > len(disk)) {
					disk = d.Name
				}
			}
			if !slices.Contains(res, disk) {
				res = append(res, disk)
			}
		}
	}
	sort.Strings(res)
	return res
}

var replicaSuffix = regexp.MustCompile(`-[0-9a-f]{8}$`)

// replicaVolume: a replica directory is "<volume>-<8 hex>".
func replicaVolume(dir string) string { return replicaSuffix.ReplaceAllString(dir, "") }

// baseDisks follows a block device down to its disks: a partition to its
// disk, an md or dm device to its members.
func baseDisks(f hostfs.FS, dev string) []string {
	if strings.HasPrefix(dev, "mapper/") {
		if t, err := f.Readlink("/dev/" + dev); err == nil {
			dev = path.Base(t)
		}
	}
	var out []string
	var walk func(name string, depth int)
	walk = func(name string, depth int) {
		if depth > 8 || name == "" {
			return
		}
		sys := "/sys/class/block/" + name
		if !hostfs.Exists(f, sys) {
			sys = "/sys/block/" + name
		}
		if hostfs.Exists(f, sys+"/partition") {
			if real, err := f.EvalSymlinks(sys); err == nil {
				walk(path.Base(path.Dir(real)), depth+1)
				return
			}
		}
		if slaves, _ := f.Glob(sys + "/slaves/*"); len(slaves) > 0 {
			for _, s := range slaves {
				walk(path.Base(s), depth+1)
			}
			return
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	walk(dev, 0)
	sort.Strings(out)
	return out
}

// lhFullWarn is Longhorn's default storage-minimal-available-percentage
// (25 %): above 75 % used it schedules no new replicas on the disk.
const lhFullWarn = 75

func longhornFindings(h *model.Header, out *model.Storage, lh *model.Longhorn) {
	status := map[string]model.Disk{}
	for _, d := range out.Disks {
		status[d.Name] = d
	}
	for i := range lh.Disks {
		d := &lh.Disks[i]
		if d.UsedPercent != nil && *d.UsedPercent >= lhFullWarn {
			h.Find(model.StatusWarn, "longhorn_disk_full", d.Path, "statfs", fmt.Sprintf(
				"Longhorn data path %s is %.0f%% full: with the default minimal available space of 25%% Longhorn schedules no new replicas here", d.Path, *d.UsedPercent))
			d.Status = model.Worst(d.Status, model.StatusWarn)
		}
		var bad []string
		for _, n := range d.Disks {
			if dk, ok := status[n]; ok && dk.Status >= model.StatusWarn {
				bad = append(bad, n+" "+dk.Status.String())
			}
		}
		if len(bad) > 0 && d.Replicas > 0 {
			h.Find(model.StatusWarn, "longhorn_disk_health", d.Path, "smart", fmt.Sprintf(
				"%d Longhorn replica(s) in %s on a disk with findings (%s): move them off before it fails (disable scheduling, evict)", d.Replicas, d.Path, strings.Join(bad, ", ")))
			d.Status = model.Worst(d.Status, model.StatusWarn)
		}
	}
	for _, r := range lh.Replicas {
		if r.Error != "" {
			h.Find(model.StatusWarn, "longhorn_replica_error", r.Volume, "longhorn", fmt.Sprintf("replica %s of %s in %s is marked failed: %s", r.Dir, r.Volume, r.Disk, clip(r.Error, 160)))
		}
	}
	for _, v := range lh.Volumes {
		if v.ReadOnly {
			h.Find(model.StatusCrit, "longhorn_volume_readonly", v.Name, "mountinfo", fmt.Sprintf(
				"Longhorn volume %s (%s) went read-only after a filesystem error: its pods cannot write until they restart and the volume is remounted", v.Name, orStr(v.Device, "no device")))
		}
	}
}
