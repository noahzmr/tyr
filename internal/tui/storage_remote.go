package tui

import (
	"fmt"
	"strings"

	"github.com/noahzmr/tyr/internal/model"
)

// The Network view of the Storage tab: iSCSI sessions and NVMe-oF
// controllers with their timeline, the node checks and Longhorn.

// nodeKey is the list row for the node checks, Longhorn and the logs.
const nodeKey = "node"

// storageViews names the views; Network only where there is network storage.
func (s *storageTab) storageViews() []string {
	v := []string{"Disks", "RAID", "Filesystems"}
	if s.st != nil && s.st.Remote != nil {
		v = append(v, "Network")
	}
	return v
}

func (s *storageTab) remoteItems() []listItem {
	rs := s.st.Remote
	if rs == nil {
		return nil
	}
	var out []listItem
	for _, x := range rs.Sessions {
		alt := append([]string{x.Target}, x.Disks...)
		if x.Longhorn != "" {
			alt = append(alt, x.Longhorn)
		}
		out = append(out, listItem{key: x.ID, alt: alt, status: x.Status})
	}
	st := model.StatusOK
	alt := []string{}
	for _, c := range rs.Checks {
		alt = append(alt, c.ID)
		if !c.OK {
			st = model.Worst(st, c.Status)
		}
	}
	if lh := s.st.Longhorn; lh != nil {
		for _, d := range lh.Disks {
			alt = append(alt, d.Path)
			st = model.Worst(st, d.Status)
		}
		for _, r := range lh.Replicas {
			alt = append(alt, r.Volume)
			if r.Error != "" {
				st = model.Worst(st, model.StatusWarn)
			}
		}
	}
	for _, f := range s.st.Findings {
		if f.Code == "volume_mount_failed" {
			alt = append(alt, f.Subject)
			st = model.Worst(st, f.Status)
		}
	}
	return append(out, listItem{key: nodeKey, alt: alt, status: st})
}

func (s *storageTab) session(id string) (model.RemoteSession, bool) {
	if s.st.Remote != nil {
		for _, x := range s.st.Remote.Sessions {
			if x.ID == id {
				return x, true
			}
		}
	}
	return model.RemoteSession{}, false
}

func transportLabel(t string) string {
	switch t {
	case "iscsi":
		return "iSCSI"
	case "fc":
		return "FC"
	case "nvme-tcp":
		return "NVMe/TCP"
	case "nvme-rdma":
		return "NVMe/RDMA"
	case "nvme-fc":
		return "NVMe/FC"
	case "nvme":
		return "NVMe-oF"
	}
	return strings.ToUpper(t)
}

// sessionState colours LOGGED_IN / live green, gone dim, the rest red.
func sessionState(t *Theme, x model.RemoteSession) string {
	switch x.State {
	case "":
		return t.Sub.Render("–")
	case "LOGGED_IN", "live":
		return t.StatusText(model.StatusOK, x.State)
	case "gone":
		return t.Dim.Render("gone")
	}
	return t.StatusBold(model.StatusCrit, x.State)
}

func sessionTarget(x model.RemoteSession) string {
	if x.Longhorn != "" {
		return "Longhorn " + x.Longhorn
	}
	return orDash(x.Target)
}

func (s *storageTab) remote(t *Theme, maxRows int) string {
	wide := s.w >= 150
	cols := []column{{"Session", 9}, {"Type", 8}, {"Target", 0}, {"Portal", -1}, {"Via", -1}, {"State", 10}, {"Disks", -1}, {">Drops", 6}}
	if wide {
		cols = append(cols, column{"Last problem", 0})
	}
	if s.w < 100 {
		cols = []column{{"Session", 9}, {"Target", 0}, {"State", 10}, {">Drops", 6}}
	}
	l := s.lists[3]
	for i := range l.shown {
		key := l.shown[i].key
		var cells map[string]string
		if key == nodeKey {
			cells = map[string]string{"Session": t.Bold.Render("node"), "Target": s.nodeSummary(), "Type": "", "Portal": "", "Via": "", "State": "",
				"Disks": "", ">Drops": "", "Last problem": t.Dim.Render("enter: checks, Longhorn, logs")}
		} else {
			x, _ := s.session(key)
			drops := t.Sub.Render("0")
			if x.ConnErrors > 0 {
				drops = t.StatusText(model.StatusWarn, fmt.Sprint(x.ConnErrors))
			}
			if x.RecoveryTimeouts > 0 {
				drops = t.StatusBold(model.StatusCrit, fmt.Sprint(x.ConnErrors))
			}
			last := ""
			if x.LastError != "" {
				last = t.Dim.Render(x.LastErrorAt.UTC().Format("01-02 15:04") + " " + x.LastError)
			}
			cells = map[string]string{"Session": x.ID, "Type": transportLabel(x.Transport), "Target": sessionTarget(x), "Portal": orDash(x.Portal),
				"Via": orDash(x.Interface), "State": sessionState(t, x), "Disks": orDash(strings.Join(x.Disks, ",")), ">Drops": drops, "Last problem": last}
		}
		var r row
		for _, c := range cols {
			r.cells = append(r.cells, cells[c.title])
		}
		r.dim = key != nodeKey && cells["State"] == t.Dim.Render("gone")
		l.shown[i].row = r
	}
	return l.render(t, cols, s.w, maxRows)
}

// nodeSummary is "Longhorn v1 on k3s · 4/6 checks OK" for the node row.
func (s *storageTab) nodeSummary() string {
	rs := s.st.Remote
	bad := 0
	for _, c := range rs.Checks {
		if !c.OK && c.Status >= model.StatusWarn {
			bad++
		}
	}
	var parts []string
	if lh := s.st.Longhorn; lh != nil {
		p := "Longhorn"
		if lh.Engine != "" {
			p += " " + lh.Engine
		}
		if lh.Kubernetes != "" {
			p += " on " + lh.Kubernetes
		}
		parts = append(parts, p)
	}
	switch {
	case bad > 0:
		parts = append(parts, countLabel(bad, "check fails", "checks fail"))
	case len(rs.Checks) > 0:
		parts = append(parts, "checks OK")
	}
	if n := len(rs.Logs); n > 0 {
		parts = append(parts, countLabel(n, "log error", "log errors"))
	}
	return strings.Join(parts, " · ")
}

// checkMark is ✓ / ▲ / ✖ / – (optional, missing) for a node check.
func checkMark(t *Theme, c model.NodeCheck) string {
	switch {
	case c.OK:
		return t.StatusText(model.StatusOK, "✓")
	case c.Status == model.StatusOK:
		return t.Sub.Render("–")
	}
	return t.StatusBold(c.Status, map[model.Status]string{model.StatusWarn: "▲", model.StatusCrit: "✖"}[c.Status])
}

func shortCheck(c model.NodeCheck) string {
	return map[string]string{"open_iscsi": "open-iscsi", "iscsid": "iscsid", "iscsi_tcp": "iscsi_tcp", "initiator_name": "initiator",
		"multipathd": "multipathd", "nfs_client": "NFS", "dm_crypt": "cryptsetup", "nvme_tcp": "nvme_tcp", "vfio_pci": "vfio/uio",
		"hugepages": "hugepages"}[c.ID]
}

// checksLine is "✓ open-iscsi ✓ iscsid ▲ multipathd · v2: – nvme_tcp …".
func checksLine(t *Theme, cs []model.NodeCheck) string {
	var b strings.Builder
	v2 := false
	for i, c := range cs {
		if c.Scope == "longhorn-v2" && !v2 {
			v2 = true
			b.WriteString(t.Dim.Render(" · v2:"))
		}
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(checkMark(t, c) + " " + orStr(shortCheck(c), c.Name))
	}
	return b.String()
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func eventMark(t *Theme, e model.RemoteEvent) string {
	if e.Severity >= model.StatusWarn {
		return t.Dot(e.Severity)
	}
	return t.Sub.Render("·")
}

// remoteDetail is the block under the list for the selected row.
func (s *storageTab) remoteDetail(t *Theme) string {
	it, ok := s.lists[3].current()
	if !ok {
		return ""
	}
	rs := s.st.Remote
	if it.key == nodeKey {
		head := t.HeadingFocus("Node", "", "enter checks, Longhorn, logs", s.w)
		lines := []string{head}
		if len(rs.Checks) > 0 {
			lines = append(lines, t.Dim.Render("Checks   ")+checksLine(t, rs.Checks))
		}
		if lh := s.st.Longhorn; lh != nil {
			lines = append(lines, t.Dim.Render("Longhorn ")+longhornLine(t, lh))
		}
		if rs.Initiator != "" {
			lines = append(lines, t.Dim.Render("IQN      ")+rs.Initiator)
		}
		return strings.Join(lines, "\n")
	}
	x, _ := s.session(it.key)
	head := t.HeadingFocus(x.ID, transportLabel(x.Transport), "enter timeline and logs", s.w)
	tmo := ""
	if x.RecoveryTimeout != nil {
		tmo = fmt.Sprintf("%d s", *x.RecoveryTimeout)
	}
	via := x.Interface
	if x.Local != "" {
		via = strings.TrimSpace(x.Local + " " + map[bool]string{true: "on " + x.Interface, false: ""}[x.Interface != ""])
	}
	c1 := kvBlock(t, []kvPair{{"Target", sessionTarget(x)}, {"Portal", x.Portal}, {"From", via}}, "")
	c2 := kvBlock(t, []kvPair{{"State", sessionState(t, x)}, {"Connection", x.ConnState}, {"Recovery", tmo}, {"Disks", strings.Join(x.Disks, ", ")}}, "")
	c3 := kvBlock(t, []kvPair{{"Drops", fmt.Sprint(x.ConnErrors)}, {"Came back", fmt.Sprint(x.Recoveries)}, {"Timed out", fmt.Sprint(x.RecoveryTimeouts)}}, "")
	out := head + "\n" + kvColumns(s.w, c1, c2, c3)
	var evs []model.RemoteEvent
	for _, e := range rs.Events {
		if e.Session == x.ID {
			evs = append(evs, e)
		}
	}
	if n := len(evs); n > 4 {
		evs = evs[n-4:]
	}
	for _, e := range evs {
		out += "\n" + truncate(eventMark(t, e)+" "+t.Dim.Render(e.Time.UTC().Format("2006-01-02 15:04:05"))+" "+e.Message, s.w)
	}
	return out
}

// longhornLine: "v1 on k3s · 2 volumes (1 read-only) · /var/lib/longhorn 22% · 3 replicas (1 failed, 1 rebuilding)".
func longhornLine(t *Theme, lh *model.Longhorn) string {
	var parts []string
	p := lh.Engine
	if lh.Kubernetes != "" {
		p = strings.TrimSpace(p + " on " + lh.Kubernetes)
	}
	if p != "" {
		parts = append(parts, p)
	}
	ro := 0
	for _, v := range lh.Volumes {
		if v.ReadOnly {
			ro++
		}
	}
	v := countLabel(len(lh.Volumes), "volume attached", "volumes attached")
	if ro > 0 {
		v += t.StatusBold(model.StatusCrit, fmt.Sprintf(" (%d read-only)", ro))
	}
	parts = append(parts, v)
	for _, d := range lh.Disks {
		u := ""
		if d.UsedPercent != nil {
			u = fmt.Sprintf(" %.0f%%", *d.UsedPercent)
		}
		x := d.Path + u
		if d.Status >= model.StatusWarn {
			x = t.StatusText(d.Status, x)
		}
		parts = append(parts, x)
	}
	failed, reb := 0, 0
	for _, r := range lh.Replicas {
		if r.Error != "" {
			failed++
		}
		if r.Rebuilding {
			reb++
		}
	}
	r := countLabel(len(lh.Replicas), "replica", "replicas")
	var extra []string
	if failed > 0 {
		extra = append(extra, t.StatusText(model.StatusWarn, fmt.Sprintf("%d failed", failed)))
	}
	if reb > 0 {
		extra = append(extra, fmt.Sprintf("%d rebuilding", reb))
	}
	if len(extra) > 0 {
		r += " (" + strings.Join(extra, ", ") + ")"
	}
	return strings.Join(append(parts, r), t.Dim.Render(" · "))
}

func eventRows(t *Theme, evs []model.RemoteEvent, withSession bool) ([][]string, []model.Status) {
	var rows [][]string
	var st []model.Status
	for _, e := range evs {
		r := []string{e.Time.UTC().Format("2006-01-02 15:04:05")}
		if withSession {
			r = append(r, orStr(e.Session, "–"))
		}
		r = append(r, strings.ReplaceAll(e.Kind, "_", " "), e.Message)
		rows = append(rows, r)
		st = append(st, e.Severity)
	}
	return rows, st
}

// remotePage is the page of a session or of the node.
func (s *storageTab) remotePage(doc *doc, key string) (string, string) {
	t := doc.t
	rs := s.st.Remote
	if key == nodeKey {
		return "Network storage · node", s.nodePage(doc)
	}
	x, _ := s.session(key)
	doc.section("Session", t.Badge(x.Status))
	tmo := ""
	if x.RecoveryTimeout != nil {
		tmo = fmt.Sprintf("%d s (%s)", *x.RecoveryTimeout, map[bool]string{true: "ctrl_loss_tmo", false: "replacement_timeout"}[strings.HasPrefix(x.Transport, "nvme")])
	}
	dig := ""
	if x.HeaderDigest != "" || x.DataDigest != "" {
		dig = "header " + orStr(x.HeaderDigest, "–") + " · data " + orStr(x.DataDigest, "–")
	}
	doc.kv([2]string{"Transport", transportLabel(x.Transport)}, [2]string{"Target", x.Target}, [2]string{"Longhorn", x.Longhorn},
		[2]string{"Portal", x.Portal}, [2]string{"Local address", x.Local}, [2]string{"Interface", x.Interface},
		[2]string{"State", sessionState(t, x)}, [2]string{"Connection", x.ConnState}, [2]string{"Disks", strings.Join(x.Disks, ", ")},
		[2]string{"Recovery timeout", tmo}, [2]string{"Digests", dig})
	doc.section("Since boot", "kernel log and iscsid")
	last := ""
	if x.LastError != "" {
		last = x.LastErrorAt.UTC().Format("2006-01-02 15:04:05") + " " + x.LastError
	}
	doc.kv([2]string{"Connection drops", fmt.Sprint(x.ConnErrors)}, [2]string{"Came back", fmt.Sprint(x.Recoveries)},
		[2]string{"Recovery timeouts", fmt.Sprint(x.RecoveryTimeouts)}, [2]string{"Digest errors", fmt.Sprint(x.DigestErrors)}, [2]string{"Last problem", last})
	switch {
	case x.Longhorn != "":
		doc.add(t.Dim.Render("The target is the volume's Longhorn engine (instance-manager pod): a drop means the engine restarted or could not reach its replicas - kubectl -n longhorn-system get pods -o wide"))
	case x.Interface != "":
		doc.add(t.Dim.Render("The Network tab shows " + x.Interface + "'s errors and drops: a flapping link or an MTU mismatch there breaks the session."))
	}
	if lh := s.st.Longhorn; lh != nil && x.Longhorn != "" {
		for _, v := range lh.Volumes {
			if v.Name != x.Longhorn {
				continue
			}
			doc.section("Longhorn volume", t.Badge(v.Status))
			mode := "read-write"
			if v.ReadOnly {
				mode = t.StatusBold(model.StatusCrit, "READ-ONLY (filesystem error)")
			}
			doc.kv([2]string{"Volume", v.Name}, [2]string{"Device", v.Device}, [2]string{"Filesystem", mode})
			for _, m := range v.Mounts {
				doc.add(t.Dim.Render("  mounted ") + m)
			}
		}
		var reps [][]string
		for _, r := range lh.Replicas {
			if r.Volume == x.Longhorn {
				state := map[bool]string{true: "rebuilding", false: "ok"}[r.Rebuilding]
				if r.Error != "" {
					state = "failed: " + r.Error
				}
				reps = append(reps, []string{r.Dir, r.Disk, bytesSI(r.SizeBytes), state})
			}
		}
		if len(reps) > 0 {
			doc.section("Replicas on this node", "")
			doc.table([]string{"Replica", "Data path", "Size", "State"}, reps, nil)
		}
	}
	var evs []model.RemoteEvent
	for _, e := range rs.Events {
		if e.Session == x.ID {
			evs = append(evs, e)
		}
	}
	if len(evs) > 0 {
		doc.section("Timeline", fmt.Sprintf("%d events this boot", len(evs)))
		rows, st := eventRows(t, evs, false)
		doc.table([]string{"Time (UTC)", "Event", "Message"}, rows, st)
	}
	if x.Longhorn != "" {
		logSection(doc, rs.Logs, x.Longhorn)
	}
	doc.findings(model.ModStorage, relatedFindings(s.st.Findings, append([]string{x.ID, x.Longhorn}, x.Disks...)...))
	name := "Session " + x.ID
	if x.Longhorn != "" {
		name += " · " + x.Longhorn
	}
	return name, doc.String()
}

func logSection(doc *doc, logs []model.NodeLogEntry, volume string) {
	var rows [][]string
	var st []model.Status
	for _, l := range logs {
		if volume != "" && l.Volume != volume {
			continue
		}
		n := ""
		if l.Count > 1 {
			n = fmt.Sprintf("%d×", l.Count)
		}
		rows = append(rows, []string{l.Time.UTC().Format("01-02 15:04"), l.Source, n, truncate(strings.Join(strings.Fields(l.Message), " "), max(40, doc.w-50))})
		st = append(st, map[string]model.Status{"error": model.StatusCrit, "warning": model.StatusWarn}[l.Level])
	}
	if len(rows) == 0 {
		return
	}
	doc.section("Logs", "errors of the last 24 h, repeats folded")
	doc.table([]string{"Last (UTC)", "Source", "", "Message"}, rows, st)
}

func (s *storageTab) nodePage(doc *doc) string {
	t := doc.t
	rs := s.st.Remote
	if len(rs.Checks) > 0 {
		doc.section("Node checks", "what iSCSI and Longhorn need on this node")
		doc.kv([2]string{"iSCSI initiator", rs.Initiator}, [2]string{"NVMe host NQN", rs.HostNQN})
		var rows [][]string
		var st []model.Status
		var fixes [][2]string
		for _, c := range rs.Checks {
			rows = append(rows, []string{checkMark(t, c), c.Name, truncate(c.Detail, max(30, doc.w-50))})
			s := model.StatusOK
			if !c.OK {
				s = c.Status
				if c.Status >= model.StatusWarn {
					fixes = append(fixes, [2]string{c.Name, c.Fix})
				}
			}
			st = append(st, s)
		}
		doc.table([]string{"", "Check", "Result"}, rows, st)
		if len(fixes) > 0 {
			doc.section("To fix", "")
			doc.kv(fixes...)
		}
	}
	if lh := s.st.Longhorn; lh != nil {
		doc.section("Longhorn", lh.Engine)
		doc.kv([2]string{"Kubernetes", lh.Kubernetes}, [2]string{"Found by", strings.Join(lh.Evidence, " · ")})
		if len(lh.Volumes) > 0 {
			var rows [][]string
			var st []model.Status
			for _, v := range lh.Volumes {
				mode := "rw"
				if v.ReadOnly {
					mode = "READ-ONLY"
				}
				rows = append(rows, []string{v.Name, v.Device, v.Session, mode, fmt.Sprint(len(v.Mounts))})
				st = append(st, v.Status)
			}
			doc.section("Volumes attached here", "")
			doc.table([]string{"Volume", "Device", "Session", "Filesystem", "Mounts"}, rows, st)
		}
		if len(lh.Disks) > 0 {
			var rows [][]string
			var st []model.Status
			for _, d := range lh.Disks {
				u := "–"
				if d.UsedPercent != nil {
					u = fmt.Sprintf("%.0f%%", *d.UsedPercent)
				}
				rows = append(rows, []string{d.Path, d.Device + " " + d.FSType, strings.Join(d.Disks, ","), bytesSI(d.SizeBytes), u, fmt.Sprint(d.Replicas)})
				st = append(st, d.Status)
			}
			doc.section("Data paths", "longhorn-disk.cfg · Longhorn keeps 25 % free")
			doc.table([]string{"Path", "Filesystem", "Disks", "Size", "Used", "Replicas"}, rows, st)
		}
		if len(lh.Replicas) > 0 {
			var rows [][]string
			var st []model.Status
			for _, r := range lh.Replicas {
				state := ""
				s := model.StatusOK
				switch {
				case r.Error != "":
					state, s = "failed: "+r.Error, model.StatusWarn
				case r.Rebuilding:
					state = "rebuilding"
				}
				rows = append(rows, []string{r.Volume, r.Disk, bytesSI(r.SizeBytes), state})
				st = append(st, s)
			}
			doc.section("Replicas on this node", "volume.meta")
			doc.table([]string{"Volume", "Data path", "Size", "State"}, rows, st)
		}
	}
	logSection(doc, rs.Logs, "")
	if len(rs.Events) > 0 {
		evs := rs.Events
		if len(evs) > 60 {
			evs = evs[len(evs)-60:]
		}
		doc.section("Timeline", fmt.Sprintf("all sessions, this boot · %d events", len(rs.Events)))
		rows, st := eventRows(t, evs, true)
		doc.table([]string{"Time (UTC)", "Session", "Event", "Message"}, rows, st)
	}
	var fs []model.Finding
	for _, f := range s.st.Findings {
		if strings.HasPrefix(f.Code, "node_") || strings.HasPrefix(f.Code, "longhorn_") || f.Code == "volume_mount_failed" {
			fs = append(fs, f)
		}
	}
	doc.findings(model.ModStorage, fs)
	return doc.String()
}
