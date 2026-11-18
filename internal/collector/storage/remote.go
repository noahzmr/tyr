package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/kernellog"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

// Network block storage: the iSCSI sessions and NVMe-oF controllers, what
// the kernel and iscsid logged about them, and - on a Longhorn node - the
// volumes, data disks, replicas, node prerequisites and logs. Everything
// is read locally; Tyr does not talk to the Kubernetes API.

// longhornIQN matches a Longhorn volume's target: iqn.2019-10.io.longhorn:<volume>
// (v1 engine, tgt) or an NQN with io.longhorn (v2 engine, SPDK).
var longhornIQN = regexp.MustCompile(`io\.longhorn[\w.-]*:(.+)$`)

// LonghornVolume returns the Longhorn volume a target IQN / NQN belongs to.
func LonghornVolume(target string) string {
	m := longhornIQN.FindStringSubmatch(target)
	if m == nil {
		return ""
	}
	return m[1]
}

// TransportName is the display name of a remote transport.
func TransportName(t string) string {
	switch t {
	case "iscsi":
		return "iSCSI"
	case "fc":
		return "Fibre Channel"
	case "nvme-tcp":
		return "NVMe/TCP"
	case "nvme-rdma":
		return "NVMe/RDMA"
	case "nvme-fc":
		return "NVMe/FC"
	}
	return strings.ToUpper(t)
}

// iscsiErrors are the kernel's iSCSI connection error codes (enum
// iscsi_err in include/scsi/iscsi_if.h).
var iscsiErrors = map[int]string{
	1001: "data sequence number error", 1002: "data offset error", 1003: "max command sequence number error",
	1004: "expected command sequence number error", 1005: "bad opcode", 1006: "data length error", 1007: "AHS length error",
	1008: "protocol error", 1009: "LUN error", 1010: "bad task tag", 1011: "connection failed", 1012: "R2T sequence error",
	1013: "session failed", 1014: "header digest error (data corrupted on the way)", 1015: "data digest error (data corrupted on the way)",
	1016: "parameter not found", 1017: "no SCSI command", 1018: "invalid host", 1019: "sending failed",
	1020: "the target closed the TCP connection", 1021: "the SCSI error handler reset the session (commands timed out)",
	1022: "no answer to the NOP ping (network or target hung)",
}

// ISCSIError decodes a connection error code: "1020 (the target closed the TCP connection)".
func ISCSIError(code int) string {
	if t, ok := iscsiErrors[code]; ok {
		return fmt.Sprintf("%d (%s)", code, t)
	}
	return strconv.Itoa(code)
}

type remoteRule struct {
	re *regexp.Regexp
	fn func(m []string, e *model.RemoteEvent, x *timelineCtx)
}

// timelineCtx maps SCSI hosts and disks to sessions while the log is read.
type timelineCtx struct {
	hostSess map[string]string // "9" -> session1
	diskSess map[string]string // sdf -> session1
	target   map[string]string // session1 -> IQN (from iscsid)
	portal   map[string]string
	// the SCSI host of the latest login, until iscsid names its session
	lastHost   string
	lastHostAt time.Time
	renamed    map[string]string // host12 -> session5
}

var kernelRules = []remoteRule{
	{regexp.MustCompile(`connection(\d+):\d+: detected conn error \((\d+)\)`), func(m []string, e *model.RemoteEvent, _ *timelineCtx) {
		code, _ := strconv.Atoi(m[2])
		e.Session, e.Kind, e.Severity = "session"+m[1], "conn_error", model.StatusWarn
		if code == 1014 || code == 1015 {
			e.Kind, e.Severity = "digest_error", model.StatusCrit
		}
		e.Message = "connection error " + ISCSIError(code)
	}},
	{regexp.MustCompile(`connection(\d+):\d+: ping timeout of (\d+) secs expired`), func(m []string, e *model.RemoteEvent, _ *timelineCtx) {
		e.Session, e.Kind, e.Severity, e.Message = "session"+m[1], "ping_timeout", model.StatusWarn, "no answer to the NOP ping within "+m[2]+" s"
	}},
	{regexp.MustCompile(`session(\d+): session recovery timed out after (\d+) secs`), func(m []string, e *model.RemoteEvent, _ *timelineCtx) {
		e.Session, e.Kind, e.Severity = "session"+m[1], "recovery_timeout", model.StatusCrit
		e.Message = "the path stayed down for " + m[2] + " s (replacement_timeout): the kernel failed the queued I/O"
	}},
	{regexp.MustCompile(`scsi host(\d+): iSCSI Initiator over TCP/IP`), func(m []string, e *model.RemoteEvent, x *timelineCtx) {
		if _, ok := x.hostSess[m[1]]; !ok { // an iSCSI host from now on; iscsid names its session
			x.hostSess[m[1]] = "host" + m[1]
			x.lastHost, x.lastHostAt = m[1], e.Time
		}
		e.Session, e.Kind, e.Severity, e.Message = x.hostSess[m[1]], "login", model.StatusOK, "new iSCSI session (SCSI host "+m[1]+")"
	}},
	{regexp.MustCompile(`sd (\d+):\d+:\d+:\d+: \[(\w+)\] Attached SCSI disk`), func(m []string, e *model.RemoteEvent, x *timelineCtx) {
		s, ok := x.hostSess[m[1]]
		if !ok {
			return
		}
		x.diskSess[m[2]] = s
		e.Session, e.Disk, e.Kind, e.Severity, e.Message = s, m[2], "attach", model.StatusOK, m[2]+" attached"
	}},
	{regexp.MustCompile(`sd (\d+):\d+:\d+:\d+: \[(\w+)\] Synchronizing SCSI cache`), func(m []string, e *model.RemoteEvent, x *timelineCtx) {
		s, ok := x.hostSess[m[1]]
		if !ok {
			return
		}
		e.Session, e.Disk, e.Kind, e.Severity, e.Message = s, m[2], "detach", model.StatusOK, m[2]+" removed (logout)"
	}},
	{regexp.MustCompile(`sd (\d+):\d+:\d+:\d+: rejecting I/O to offline device`), func(m []string, e *model.RemoteEvent, x *timelineCtx) {
		s, ok := x.hostSess[m[1]]
		if !ok {
			return
		}
		e.Session, e.Kind, e.Severity, e.Message = s, "offline_io", model.StatusCrit, "I/O rejected: the device is offline"
	}},
	{regexp.MustCompile(`(?:I/O|critical medium|critical target) error, dev (\w+), sector`), func(m []string, e *model.RemoteEvent, x *timelineCtx) {
		s, ok := x.diskSess[m[1]]
		if !ok {
			return
		}
		e.Session, e.Disk, e.Kind, e.Severity, e.Message = s, m[1], "io_error", model.StatusCrit, "I/O error on "+m[1]
	}},
	{regexp.MustCompile(`EXT4-fs \((\w+)\): Remounting filesystem read-only|EXT4-fs error \(device (\w+)\)|XFS \((\w+)\): .*(?:[Ss]hutting down filesystem|[Cc]orruption)`), func(m []string, e *model.RemoteEvent, x *timelineCtx) {
		dev := firstNonEmpty(m[1], m[2], m[3])
		disk := dev
		if _, ok := x.diskSess[disk]; !ok {
			disk = strings.TrimRight(dev, "0123456789")
		}
		s, ok := x.diskSess[disk]
		if !ok {
			return
		}
		e.Session, e.Disk, e.Kind, e.Severity = s, disk, "fs_error", model.StatusCrit
		e.Message = "filesystem error on " + dev
		if strings.Contains(m[0], "read-only") || strings.Contains(m[0], "hutting down") {
			e.Kind, e.Message = "fs_readonly", "the filesystem on "+dev+" went read-only"
		}
	}},
	// NVMe over Fabrics
	{regexp.MustCompile(`nvme (nvme\d+): new ctrl: NQN "([^"]+)", addr ([^\s,]+)`), func(m []string, e *model.RemoteEvent, x *timelineCtx) {
		x.target[m[1]], x.portal[m[1]] = m[2], m[3]
		e.Session, e.Kind, e.Severity, e.Message = m[1], "login", model.StatusOK, "connected to "+m[2]+" at "+m[3]
	}},
	{regexp.MustCompile(`nvme (nvme\d+): (starting error recovery|Reconnecting in \d+ seconds|failed to connect.*|.*keep-alive.*timed out.*|I/O \d+ (?:\(\S+\) )?QID \d+ timeout.*|queue \d+: timeout.*)`), func(m []string, e *model.RemoteEvent, x *timelineCtx) {
		if _, ok := x.target[m[1]]; !ok {
			return // a local PCIe drive: the disk checks report it
		}
		e.Session, e.Kind, e.Severity, e.Message = m[1], "conn_error", model.StatusWarn, m[2]
	}},
	{regexp.MustCompile(`nvme (nvme\d+): Successfully reconnected`), func(m []string, e *model.RemoteEvent, x *timelineCtx) {
		if _, ok := x.target[m[1]]; !ok {
			return
		}
		e.Session, e.Kind, e.Severity, e.Message = m[1], "recovered", model.StatusOK, "reconnected"
	}},
	{regexp.MustCompile(`nvme (nvme\d+): Removing ctrl: NQN "([^"]+)"`), func(m []string, e *model.RemoteEvent, x *timelineCtx) {
		e.Session, e.Kind, e.Severity, e.Message = m[1], "logout", model.StatusOK, "disconnected from "+m[2]
	}},
}

var iscsidRules = []remoteRule{
	{regexp.MustCompile(`Kernel reported iSCSI connection (\d+):\d+ error \((\d+) - ([A-Z0-9_]+): ([^)]*)\)`), func(m []string, e *model.RemoteEvent, _ *timelineCtx) {
		code, _ := strconv.Atoi(m[2])
		e.Session, e.Kind, e.Severity, e.Message = "session"+m[1], "conn_error", model.StatusWarn, "connection error "+ISCSIError(code)
		if code == 1014 || code == 1015 {
			e.Kind, e.Severity = "digest_error", model.StatusCrit
		}
	}},
	{regexp.MustCompile(`[Cc]onnection(\d+):\d+ to \[target: ([^,]+), portal: ([^,\]]+),(\d+)\] through \[iface: ([^\]]+)\] is operational now`), func(m []string, e *model.RemoteEvent, x *timelineCtx) {
		s := "session" + m[1]
		x.target[s], x.portal[s] = m[2], m[3]+":"+m[4]
		if h := x.lastHost; h != "" && x.hostSess[h] == "host"+h && e.Time.Sub(x.lastHostAt) < 30*time.Second {
			x.hostSess[h], x.renamed["host"+h], x.lastHost = s, s, ""
			for d, ds := range x.diskSess {
				if ds == "host"+h {
					x.diskSess[d] = s
				}
			}
		}
		e.Session, e.Kind, e.Severity, e.Message = s, "login", model.StatusOK, "logged in to "+m[2]+" at "+m[3]+":"+m[4]
	}},
	{regexp.MustCompile(`connection(\d+):\d+ is operational after recovery \((\d+) attempts?\)`), func(m []string, e *model.RemoteEvent, _ *timelineCtx) {
		e.Session, e.Kind, e.Severity, e.Message = "session"+m[1], "recovered", model.StatusOK, "the connection came back after "+m[2]+map[bool]string{true: " attempt", false: " attempts"}[m[2] == "1"]
	}},
	{regexp.MustCompile(`connect to (\S+) failed \(([^)]+)\)`), func(m []string, e *model.RemoteEvent, x *timelineCtx) {
		e.Kind, e.Severity, e.Message = "connect_failed", model.StatusWarn, "cannot connect to "+m[1]+": "+m[2]
		for s, p := range x.portal {
			if p == m[1] && strings.HasPrefix(s, "session") {
				e.Session = s
			}
		}
	}},
	{regexp.MustCompile(`(?i)login (?:negotiation )?failed.*|initiator reported error.*|could not log ?in.*`), func(m []string, e *model.RemoteEvent, _ *timelineCtx) {
		e.Kind, e.Severity, e.Message = "login_failed", model.StatusWarn, m[0]
	}},
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// logLine is one message of the kernel log or the journal.
type logLine struct {
	t      time.Time
	msg    string
	source string // kernel, iscsid
}

// kernelLines returns the kernel messages of the boot: the classified
// ones (kept in full) and the recent ones (with the logins).
func bootKernelLines(l *kernellog.Log) []logLine {
	if l == nil {
		return nil
	}
	var out []logLine
	first := time.Time{}
	if len(l.Recent) > 0 {
		first = l.Recent[0].Time
	}
	for _, e := range l.Entries { // classified messages before the recent window
		if !first.IsZero() && !e.Time.Before(first) {
			break
		}
		out = append(out, logLine{t: e.Time, msg: e.Message, source: "kernel"})
	}
	for _, e := range l.Recent {
		out = append(out, logLine{t: e.Time, msg: e.Message, source: "kernel"})
	}
	return out
}

// timeline turns kernel and iscsid lines into events and fills the
// sessions' counters. Sessions that only the log knows are added as gone.
func timeline(lines []logLine, sessions []sysfs.Session) ([]model.RemoteEvent, *timelineCtx) {
	x := &timelineCtx{hostSess: map[string]string{}, diskSess: map[string]string{}, target: map[string]string{}, portal: map[string]string{}, renamed: map[string]string{}}
	for _, s := range sessions {
		if s.Host != "" {
			x.hostSess[strings.TrimPrefix(s.Host, "host")] = s.ID
		}
		for _, d := range s.Disks {
			x.diskSess[d] = s.ID
		}
		if strings.HasPrefix(s.Transport, "nvme") {
			x.target[s.ID] = s.Target
		}
		if s.Portal != "" {
			x.portal[s.ID] = s.Portal
		}
	}
	var out []model.RemoteEvent
	for _, l := range lines {
		rules := kernelRules
		if l.source != "kernel" {
			rules = iscsidRules
		}
		for _, r := range rules {
			m := r.re.FindStringSubmatch(l.msg)
			if m == nil {
				continue
			}
			e := model.RemoteEvent{Time: l.t.UTC(), Source: l.source}
			r.fn(m, &e, x)
			if e.Kind != "" {
				out = append(out, e)
			}
			break
		}
	}
	for i := range out {
		if s, ok := x.renamed[out[i].Session]; ok {
			out[i].Session = s
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return dedupEvents(out), x
}

// dedupEvents folds iscsid's report of what the kernel logged too: a
// connection error (same session within 10 s) and a login (iscsid names
// the target, within 30 s).
func dedupEvents(in []model.RemoteEvent) []model.RemoteEvent {
	var out []model.RemoteEvent
	for _, e := range in {
		dup := false
		if e.Source == "iscsid" && (e.Kind == "conn_error" || e.Kind == "digest_error" || e.Kind == "login") {
			win := 10 * time.Second
			if e.Kind == "login" {
				win = 30 * time.Second
			}
			for i := len(out) - 1; i >= 0 && e.Time.Sub(out[i].Time) <= win; i-- {
				if o := &out[i]; o.Source == "kernel" && o.Session == e.Session && o.Kind == e.Kind {
					dup = true
					switch {
					case e.Kind == "login":
						o.Message = e.Message + strings.TrimPrefix(o.Message, "new iSCSI session")
					case !strings.Contains(o.Message, "("): // keep the decoded text
						o.Message = e.Message
					}
					break
				}
			}
		}
		if !dup {
			out = append(out, e)
		}
	}
	return foldRetries(out)
}

// foldRetries keeps the last of iscsid's repeated connect / login failures
// with their count: it retries every few seconds for as long as the
// target is away.
func foldRetries(in []model.RemoteEvent) []model.RemoteEvent {
	type agg struct {
		n     int
		first time.Time
	}
	seen := map[string]*agg{}
	key := func(e model.RemoteEvent) string { return e.Session + "|" + e.Kind + "|" + e.Message }
	for _, e := range in {
		if e.Kind == "connect_failed" || e.Kind == "login_failed" {
			if a := seen[key(e)]; a != nil {
				a.n++
			} else {
				seen[key(e)] = &agg{n: 1, first: e.Time}
			}
		}
	}
	var out []model.RemoteEvent
	left := map[string]int{}
	for _, e := range in {
		a := seen[key(e)]
		if a == nil || a.n == 1 {
			out = append(out, e)
			continue
		}
		left[key(e)]++
		if left[key(e)] < a.n {
			continue // keep the last one
		}
		e.Message += fmt.Sprintf(" (%d× since %s)", a.n, a.first.UTC().Format("01-02 15:04"))
		out = append(out, e)
	}
	return out
}

// nodeHints are paths that exist on a Kubernetes or Longhorn node.
var nodeHints = []string{"/var/lib/kubelet", "/var/lib/rancher", "/var/lib/longhorn", "/dev/longhorn", "/etc/iscsi/initiatorname.iscsi"}

// journalCache keeps the node's journal lines for a minute: the TUI
// refreshes every few seconds and journalctl -g over a day is not free.
type journalCache struct {
	at    time.Time
	lines []jline
	ok    bool
}

const journalTTL = time.Minute

func (c *Collector) cachedJournal(now time.Time) ([]jline, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.journal.ok && now.Sub(c.journal.at) < journalTTL && !now.Before(c.journal.at) {
		return c.journal.lines, true
	}
	return nil, false
}

// kernelSrc is the findings' source.
const kernelSrc = "kernel_log"

// maxRemoteEvents bounds the timeline kept in the snapshot.
const maxRemoteEvents = 200

// remoteStorage builds out.Remote and out.Longhorn; it runs after the
// disks and filesystems are known.
func (c *Collector) remoteStorage(ctx context.Context, env *collector.Env, out *model.Storage, h *model.Header, klog *kernellog.Log, mounts []sysfs.Mount) {
	f := env.FS
	sessions := sysfs.ReadSessions(f)
	if len(sessions) == 0 && !slices.ContainsFunc(nodeHints, func(p string) bool { return hostfs.Exists(f, p) }) {
		return // no network storage, no Kubernetes: nothing to look at (and no process scan every refresh)
	}
	procs := readProcs(f)
	lh := detectLonghorn(f, mounts, sessions, procs)
	if len(sessions) == 0 && lh == nil {
		return
	}
	rs := &model.RemoteStorage{Sessions: []model.RemoteSession{},
		Initiator: initiatorName(f), HostNQN: hostfs.StringOr(f, "/etc/nvme/hostnqn", "")}
	out.Remote = rs

	jl, cached := c.cachedJournal(env.Now())
	if !cached {
		jl = nodeJournal(ctx, env, lh != nil)
		c.mu.Lock()
		c.journal = journalCache{at: env.Now(), lines: jl, ok: true}
		c.mu.Unlock()
	}
	lines := bootKernelLines(klog)
	for _, l := range jl {
		if l.source == "iscsid" {
			lines = append(lines, logLine{t: l.t, msg: l.msg, source: "iscsid"})
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].t.Before(lines[j].t) })

	for i := range sessions { // the LUNs name their session even where the session's device link is missing
		if len(sessions[i].Disks) > 0 {
			continue
		}
		for _, d := range out.Disks {
			if d.Remote != nil && d.Remote.Session == sessions[i].ID {
				sessions[i].Disks = append(sessions[i].Disks, d.Name)
			}
		}
	}
	events, x := timeline(lines, sessions)
	routes := map[string]string{}
	for _, s := range sessions {
		rsess := model.RemoteSession{Transport: s.Transport, ID: s.ID, Target: s.Target, Portal: s.Portal, Local: s.Local,
			Interface: s.NetDev, State: s.State, ConnState: s.ConnState, Disks: s.Disks, RecoveryTimeout: s.RecoveryTmo,
			HeaderDigest: s.HeaderDigest, DataDigest: s.DataDigest, Longhorn: LonghornVolume(s.Target)}
		if rsess.Interface == "" && s.Portal != "" {
			rsess.Interface = routeDev(ctx, env, portalHost(s.Portal), routes)
		}
		rs.Sessions = append(rs.Sessions, rsess)
	}
	// sessions only the log knows: logged out or replaced since
	known := map[string]bool{}
	for _, s := range rs.Sessions {
		known[s.ID] = true
	}
	for _, e := range events {
		if e.Session == "" || known[e.Session] || strings.HasPrefix(e.Session, "host") {
			continue
		}
		known[e.Session] = true
		tr := "iscsi"
		if strings.HasPrefix(e.Session, "nvme") {
			tr = "nvme"
		}
		g := model.RemoteSession{Transport: tr, ID: e.Session, State: "gone", Target: x.target[e.Session], Portal: x.portal[e.Session]}
		g.Longhorn = LonghornVolume(g.Target)
		rs.Sessions = append(rs.Sessions, g)
	}
	byID := map[string]*model.RemoteSession{}
	for i := range rs.Sessions {
		byID[rs.Sessions[i].ID] = &rs.Sessions[i]
	}
	for _, e := range events {
		s := byID[e.Session]
		if s == nil {
			continue
		}
		switch e.Kind {
		case "conn_error", "digest_error":
			s.ConnErrors++
			if e.Kind == "digest_error" {
				s.DigestErrors++
			}
		case "recovered":
			s.Recoveries++
		case "recovery_timeout":
			s.RecoveryTimeouts++
		}
		switch e.Kind {
		case "conn_error", "digest_error", "ping_timeout", "recovery_timeout", "offline_io":
			if e.Kind != "ping_timeout" || s.LastErrorAt.IsZero() || e.Time.Sub(s.LastErrorAt) > 10*time.Second {
				s.LastError, s.LastErrorAt = e.Message, e.Time
			}
		}
	}
	if len(events) > maxRemoteEvents {
		events = events[len(events)-maxRemoteEvents:]
	}
	rs.Events = events

	// the LUNs know their Longhorn volume
	for i := range out.Disks {
		if r := out.Disks[i].Remote; r != nil {
			r.Longhorn = LonghornVolume(r.Target)
		}
	}
	now := env.Now()
	lastConn := map[string]string{} // the latest connection error of each session
	for _, e := range events {
		if e.Kind == "conn_error" {
			lastConn[e.Session] = e.Message
		}
	}
	for i := range rs.Sessions {
		sessionFindings(h, &rs.Sessions[i], now, lastConn[rs.Sessions[i].ID])
	}
	fsFindings(h, rs, out)

	rs.Checks = nodeChecks(env, out, lh, sessions, procs)
	for _, ch := range rs.Checks {
		if !ch.OK && ch.Status >= model.StatusWarn {
			h.Find(ch.Status, "node_"+ch.ID, ch.ID, "node", ch.Name+": "+ch.Detail)
		}
	}
	rs.Logs = nodeLogs(env, jl, lh != nil)
	mountFindings(h, rs.Logs)
	if lh != nil {
		out.Longhorn = lh
		longhornDetails(env, out, lh, rs, mounts)
		longhornFindings(h, out, lh)
	}
}

func portalHost(p string) string {
	if host, _, err := net.SplitHostPort(p); err == nil {
		return host
	}
	return strings.Trim(p, "[]")
}

// routeDev asks the kernel which interface reaches addr (ip route get).
func routeDev(ctx context.Context, env *collector.Env, addr string, cache map[string]string) string {
	if addr == "" {
		return ""
	}
	if d, ok := cache[addr]; ok {
		return d
	}
	cache[addr] = ""
	if _, err := env.Exec.LookPath("ip"); err != nil {
		return ""
	}
	b, err := env.Exec.Run(ctx, "ip", "-j", "route", "get", addr)
	if err != nil {
		return ""
	}
	var rs []struct {
		Dev string `json:"dev"`
	}
	if json.Unmarshal(b, &rs) == nil && len(rs) > 0 {
		cache[addr] = rs[0].Dev
	}
	return cache[addr]
}

// initiatorName reads the iSCSI initiator name ("" when not set yet:
// GenerateName=yes before iscsid's first start).
func initiatorName(f hostfs.FS) string {
	b, err := f.ReadFile("/etc/iscsi/initiatorname.iscsi")
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "InitiatorName="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func sessionLabel(s *model.RemoteSession) string {
	what := s.ID
	switch {
	case s.Longhorn != "":
		what += " (Longhorn volume " + s.Longhorn + ")"
	case s.Target != "":
		what += " to " + s.Target
	}
	if s.Portal != "" && s.Longhorn == "" {
		what += " at " + s.Portal
	}
	if s.Interface != "" {
		what += " via " + s.Interface
	}
	return what
}

func when(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04")
}

func sessionFindings(h *model.Header, s *model.RemoteSession, now time.Time, lastConn string) {
	st := model.StatusOK
	find := func(sev model.Status, code, msg string) {
		h.Find(sev, code, s.ID, kernelSrc, msg)
		st = model.Worst(st, sev)
	}
	nvme := strings.HasPrefix(s.Transport, "nvme")
	label := sessionLabel(s)
	if !s.Gone() {
		switch {
		case !nvme && s.State != "" && s.State != "LOGGED_IN" && len(s.Disks) == 0 && s.Longhorn != "":
			find(model.StatusWarn, "iscsi_orphan", fmt.Sprintf("%s is %s and has no disk: left over after the Longhorn engine or instance-manager restarted; iscsid keeps retrying it", label, s.State))
		case !nvme && s.State != "" && s.State != "LOGGED_IN" && len(s.Disks) == 0:
			find(model.StatusCrit, "iscsi_session", fmt.Sprintf("%s is %s", label, s.State))
		case nvme && s.State != "" && s.State != "live" && len(s.Disks) == 0:
			find(model.StatusCrit, "nvmeof_state", fmt.Sprintf("%s is %s: I/O waits until the controller is back or ctrl_loss_tmo ends", label, s.State))
		}
	}
	if s.DigestErrors > 0 {
		find(model.StatusCrit, "iscsi_digest", fmt.Sprintf("%s: %d digest errors since boot - data was corrupted between this host and the target", label, s.DigestErrors))
	}
	if s.RecoveryTimeouts > 0 {
		sev := model.StatusCrit
		gone := ""
		if s.Gone() {
			gone = ", the session is gone since"
			if now.Sub(s.LastErrorAt) > 24*time.Hour {
				sev = model.StatusWarn
			}
		}
		find(sev, "iscsi_recovery_timeout", fmt.Sprintf("%s: the path stayed down past the recovery timeout %d× since boot (last %s%s) - the kernel failed the I/O, applications saw errors",
			label, s.RecoveryTimeouts, when(s.LastErrorAt), gone))
	}
	down := s.State != "" && s.State != "LOGGED_IN" && s.State != "live" // reported above, or with its disks
	// drops that ended in a recovery timeout are reported as such
	if n := s.ConnErrors - s.DigestErrors; n > s.RecoveryTimeouts && !s.Gone() && !down {
		code := "iscsi_conn_errors"
		if nvme {
			code = "nvmeof_conn_errors"
		}
		back := ""
		if s.Recoveries > 0 {
			back = fmt.Sprintf(", came back %d×", s.Recoveries)
		}
		find(model.StatusWarn, code, fmt.Sprintf("%s lost its connection %d× since boot%s; last %s: %s", label, n, back, when(s.LastErrorAt), orStr(lastConn, s.LastError)))
	}
	if s.Gone() { // history: it no longer holds up I/O
		st = min(st, model.StatusWarn)
	}
	s.Status = st
}

// fsFindings: filesystems on network volumes that failed or went read-only.
func fsFindings(h *model.Header, rs *model.RemoteStorage, out *model.Storage) {
	byDisk := map[string]model.RemoteEvent{}
	var order []string
	for _, e := range rs.Events {
		if e.Kind != "fs_error" && e.Kind != "fs_readonly" {
			continue
		}
		if _, ok := byDisk[e.Disk]; !ok {
			order = append(order, e.Disk)
		}
		if p, ok := byDisk[e.Disk]; !ok || e.Kind == "fs_readonly" || p.Kind != "fs_readonly" {
			byDisk[e.Disk] = e
		}
	}
	for _, d := range order {
		e := byDisk[d]
		if slices.ContainsFunc(out.Disks, func(x model.Disk) bool { return x.Name == d && x.Remote != nil && x.Remote.Longhorn != "" }) {
			continue // a Longhorn volume: its read-only state is reported with the volume
		}
		what := d
		present := false
		for _, dk := range out.Disks {
			if dk.Name == d && dk.Remote != nil {
				present = true
				if dk.Remote.Longhorn != "" {
					what += " (Longhorn volume " + dk.Remote.Longhorn + ")"
				}
			}
		}
		sev := model.StatusCrit
		tail := " - applications get errors on write until the volume is remounted (a Longhorn volume: the pod restarted)"
		if !present {
			sev, tail = model.StatusWarn, "; the volume is no longer attached"
		}
		h.Find(sev, "remote_fs_error", d, kernelSrc, fmt.Sprintf("%s: %s at %s%s", what, e.Message, when(e.Time), tail))
	}
}

// proc is a running process: its pid and argv.
type proc struct {
	pid  string
	argv []string
}

func (p proc) name() string {
	if len(p.argv) == 0 {
		return ""
	}
	a := p.argv[0]
	return a[strings.LastIndexByte(a, '/')+1:]
}

// readProcs lists the processes' command lines.
func readProcs(f hostfs.FS) []proc {
	paths, _ := f.Glob("/proc/[0-9]*/cmdline")
	var out []proc
	for _, p := range paths {
		b, err := f.ReadFile(p)
		if err != nil || len(b) == 0 {
			continue
		}
		argv := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		out = append(out, proc{pid: strings.Split(p, "/")[2], argv: argv})
	}
	return out
}

func running(procs []proc, names ...string) bool {
	return slices.ContainsFunc(procs, func(p proc) bool { return slices.Contains(names, p.name()) })
}
