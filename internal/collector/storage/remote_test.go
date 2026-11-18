package storage

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

const (
	pvcA = "pvc-3f8e2a10-6c1d-4b8e-9f3a-1c2d3e4f5a6b" // flapping
	pvcB = "pvc-7b2c9d4e-1a3f-4c5e-8d7b-2e3f4a5b6c7d" // timed out, read-only
	pvcC = "pvc-c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f" // orphaned session, mount fails
	pvcD = "pvc-d4e5f6a7-b8c9-4d0e-9f1a-2b3c4d5e6f70" // failed replica
)

func sess(s *model.Storage, id string) *model.RemoteSession {
	for i := range s.Remote.Sessions {
		if s.Remote.Sessions[i].ID == id {
			return &s.Remote.Sessions[i]
		}
	}
	return nil
}

func TestHealthyHasNoNetworkStorage(t *testing.T) {
	s := collect(t, "server-healthy")
	if s.Remote != nil || s.Longhorn != nil {
		t.Errorf("remote %+v longhorn %+v", s.Remote, s.Longhorn)
	}
}

func TestDegradedISCSI(t *testing.T) {
	s := collect(t, "server-degraded")
	if s.Remote == nil || s.Longhorn != nil {
		t.Fatalf("remote %+v longhorn %+v", s.Remote, s.Longhorn)
	}
	x := sess(s, "session1")
	if x == nil || x.State != "FAILED" || x.Interface != "bond0.100" || x.Local != "10.20.0.21" || len(x.Disks) != 1 || x.Disks[0] != "sdf" ||
		x.RecoveryTimeouts != 1 || x.ConnErrors != 1 || x.RecoveryTimeout == nil || *x.RecoveryTimeout != 120 || x.Status != model.StatusCrit {
		t.Fatalf("session1 %+v", x)
	}
	c := codes(s)
	if len(c["iscsi_recovery_timeout"]) != 1 || !strings.Contains(c["iscsi_recovery_timeout"][0].Message, "via bond0.100") {
		t.Errorf("recovery timeout %v", c["iscsi_recovery_timeout"])
	}
	// the drop ended in the timeout, the session is down: no separate warning; the disk reports the state
	if len(c["iscsi_conn_errors"]) != 0 || len(c["iscsi_session"]) != 1 || c["iscsi_session"][0].Subject != "sdf" {
		t.Errorf("conn errors %v session %v", c["iscsi_conn_errors"], c["iscsi_session"])
	}
	for _, ch := range s.Remote.Checks {
		if !ch.OK {
			t.Errorf("check %+v", ch)
		}
		if ch.Scope != "iscsi" {
			t.Errorf("Longhorn check on a host without Longhorn: %+v", ch)
		}
	}
	var kinds []string
	for _, e := range s.Remote.Events {
		kinds = append(kinds, e.Kind)
	}
	want := []string{"login", "attach", "ping_timeout", "conn_error", "io_error", "io_error", "recovery_timeout", "offline_io", "connect_failed"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("timeline %v", kinds)
	}
	if e := s.Remote.Events[3]; !strings.Contains(e.Message, "1020 (the target closed the TCP connection)") {
		t.Errorf("decoded error %+v", e)
	}
	last := s.Remote.Events[len(s.Remote.Events)-1]
	if last.Session != "session1" || !strings.Contains(last.Message, "(3× since") {
		t.Errorf("folded retries %+v", last)
	}
	if !strings.Contains(c["remote_io_errors"][0].Message, "storage system") {
		t.Errorf("SAN advice %v", c["remote_io_errors"])
	}
}

func TestLonghorn(t *testing.T) {
	s := collect(t, "k8s-longhorn")
	rs, lh := s.Remote, s.Longhorn
	if rs == nil || lh == nil {
		t.Fatalf("remote %v longhorn %v", rs, lh)
	}
	if lh.Engine != "v1" || lh.Kubernetes != "k3s" || len(lh.Evidence) != 5 {
		t.Errorf("longhorn %+v", lh)
	}
	if rs.Initiator != "iqn.2004-10.com.ubuntu:01:8e1f4c2d9a7b" {
		t.Errorf("initiator %q", rs.Initiator)
	}
	a, b, cc, gone := sess(s, "session2"), sess(s, "session3"), sess(s, "session4"), sess(s, "session1")
	if a == nil || a.Longhorn != pvcA || a.ConnErrors != 3 || a.Recoveries != 3 || a.Interface != "cni0" || a.Status != model.StatusWarn {
		t.Errorf("session2 %+v", a)
	}
	if b == nil || b.RecoveryTimeouts != 1 || b.Status != model.StatusCrit {
		t.Errorf("session3 %+v", b)
	}
	if cc == nil || cc.State != "FAILED" || len(cc.Disks) != 0 || cc.Longhorn != pvcC {
		t.Errorf("session4 %+v", cc)
	}
	if gone == nil || !gone.Gone() || gone.Longhorn != "pvc-e1e2e3e4-f5f6-4a7b-8c9d-0e1f2a3b4c5d" || gone.Status != model.StatusOK {
		t.Errorf("gone session1 %+v", gone)
	}
	c := codes(s)
	for code, n := range map[string]int{"iscsi_conn_errors": 1, "iscsi_recovery_timeout": 1, "iscsi_orphan": 1, "longhorn_volume_readonly": 1,
		"longhorn_replica_error": 1, "node_multipathd": 1, "volume_mount_failed": 1, "remote_io_errors": 1, "remote_fs_error": 0, "longhorn_disk_full": 0} {
		if len(c[code]) != n {
			t.Errorf("%s: %d findings, want %d: %v", code, len(c[code]), n, c[code])
		}
	}
	if f := c["volume_mount_failed"]; len(f) == 1 && (f[0].Subject != pvcC || !strings.Contains(f[0].Message, "pod postgres-0") ||
		!strings.Contains(f[0].Message, "5×") || !strings.Contains(f[0].Message, "already mounted or mount point busy")) {
		t.Errorf("mount failure %+v", f[0])
	}
	if f := c["remote_io_errors"]; len(f) == 1 && !strings.Contains(f[0].Message, "Longhorn engine") {
		t.Errorf("Longhorn advice %+v", f[0])
	}
	// volumes: the orphaned session is none
	if len(lh.Volumes) != 2 || lh.Volumes[0].Name != pvcA || lh.Volumes[0].ReadOnly || len(lh.Volumes[0].Mounts) != 2 ||
		lh.Volumes[1].Name != pvcB || !lh.Volumes[1].ReadOnly || lh.Volumes[1].Device != "sdh" {
		t.Errorf("volumes %+v", lh.Volumes)
	}
	if len(lh.Disks) != 1 || lh.Disks[0].Path != "/var/lib/longhorn" || lh.Disks[0].Replicas != 3 || strings.Join(lh.Disks[0].Disks, ",") != "nvme0n1,nvme1n1" ||
		lh.Disks[0].UsedPercent == nil || lh.Disks[0].UUID == "" {
		t.Errorf("data paths %+v", lh.Disks)
	}
	var failed, rebuilding []string
	for _, r := range lh.Replicas {
		if r.Error != "" {
			failed = append(failed, r.Volume)
		}
		if r.Rebuilding {
			rebuilding = append(rebuilding, r.Volume)
		}
	}
	if len(lh.Replicas) != 3 || strings.Join(failed, ",") != pvcD || strings.Join(rebuilding, ",") != pvcB {
		t.Errorf("replicas %+v", lh.Replicas)
	}
	checks := map[string]model.NodeCheck{}
	for _, ch := range rs.Checks {
		checks[ch.ID] = ch
	}
	if ch := checks["multipathd"]; ch.OK || ch.Status != model.StatusWarn {
		t.Errorf("multipathd %+v", ch)
	}
	if ch := checks["hugepages"]; ch.OK || ch.Status != model.StatusOK { // v2 not in use: informational
		t.Errorf("hugepages %+v", ch)
	}
	for _, id := range []string{"open_iscsi", "iscsid", "iscsi_tcp", "initiator_name", "nfs_client"} {
		if !checks[id].OK {
			t.Errorf("%s %+v", id, checks[id])
		}
	}
	// logs: folded, 24 h window, iscsid's timeline lines left out
	var sources []string
	for _, l := range rs.Logs {
		sources = append(sources, l.Source)
		if strings.Contains(l.Message, "three days old") || strings.HasPrefix(l.Message, "Kernel reported") {
			t.Errorf("log %+v", l)
		}
		if l.Source == "instance-manager" && (l.Count != 4 || l.Volume != pvcB || !strings.Contains(l.Message, "connection refused")) {
			t.Errorf("instance-manager %+v", l)
		}
	}
	slices.Sort(sources)
	if strings.Join(sources, ",") != "instance-manager,k3s (kubelet),longhorn-manager" {
		t.Errorf("log sources %v", sources)
	}
	// the iscsid login names the kernel's session
	for _, e := range rs.Events {
		if e.Session == "session3" && e.Kind == "login" && !strings.Contains(e.Message, "logged in to iqn.2019-10.io.longhorn:"+pvcB+" at 10.42.3.17:3260 (SCSI host 11)") {
			t.Errorf("login %+v", e)
		}
		if strings.HasPrefix(e.Session, "host") {
			t.Errorf("unnamed session %+v", e)
		}
	}
}

func TestTimelineAndDecoding(t *testing.T) {
	if ISCSIError(1022) != "1022 (no answer to the NOP ping (network or target hung))" || ISCSIError(42) != "42" {
		t.Error(ISCSIError(1022), ISCSIError(42))
	}
	for in, want := range map[string]string{"iqn.2019-10.io.longhorn:pvc-1": "pvc-1", "nqn.2023-01.io.longhorn.spdk:vol-a": "vol-a", "iqn.2024-01.net.example:x": ""} {
		if got := LonghornVolume(in); got != want {
			t.Errorf("LonghornVolume(%q) = %q", in, got)
		}
	}
	t0 := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	lines := []logLine{
		{t: at(0), msg: `nvme nvme3: new ctrl: NQN "nqn.2023-01.io.longhorn.spdk:vol-a", addr 10.42.3.30:4420`, source: "kernel"},
		{t: at(10), msg: "nvme nvme3: starting error recovery", source: "kernel"},
		{t: at(11), msg: "nvme nvme3: Reconnecting in 10 seconds...", source: "kernel"},
		{t: at(21), msg: "nvme nvme3: Successfully reconnected (1 attempt)", source: "kernel"},
		{t: at(30), msg: "nvme nvme0: I/O 12 QID 3 timeout, aborting", source: "kernel"}, // a local drive
		{t: at(40), msg: " connection7:0: detected conn error (1015)", source: "kernel"},
		{t: at(41), msg: "Kernel reported iSCSI connection 7:0 error (1015 - ISCSI_ERR_DATA_DGST: Data digest mismatch) state (3)", source: "iscsid"},
	}
	evs, _ := timeline(lines, nil)
	var got []string
	for _, e := range evs {
		got = append(got, e.Session+":"+e.Kind)
	}
	if strings.Join(got, ",") != "nvme3:login,nvme3:conn_error,nvme3:conn_error,nvme3:recovered,session7:digest_error" {
		t.Errorf("events %v", got)
	}
	if e := evs[len(evs)-1]; e.Severity != model.StatusCrit || !strings.Contains(e.Message, "data digest error") {
		t.Errorf("digest %+v", e)
	}
	s := model.RemoteSession{ID: "session7", Transport: "iscsi", State: "LOGGED_IN", ConnErrors: 1, DigestErrors: 1}
	h := &model.Header{}
	sessionFindings(h, &s, t0, "")
	if len(h.Findings) != 1 || h.Findings[0].Code != "iscsi_digest" {
		t.Errorf("digest findings %+v", h.Findings)
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string][2]string{
		`E1116 08:30:00.123456    1510 operation_generator.go:622] "MountVolume.SetUp failed"` + "\nmore": {"error", "\"MountVolume.SetUp failed\"\nmore"},
		`W1116 08:30:00.1 1 x.go:1] slow`: {"warning", "slow"},
		`time="2026-11-16T03:00:00Z" level=error msg="Failed to connect" error="dial tcp: refused" volume=pvc-1`: {"error", "Failed to connect: dial tcp: refused"},
		`{"level":"warning","msg":"degraded","error":"one replica"}`:                                             {"warning", "degraded: one replica"},
		`plain message`: {"info", "plain message"},
	} {
		l, m := parseLevel(in, 6)
		if l != want[0] || m != want[1] {
			t.Errorf("parseLevel(%q) = %q, %q", in, l, m)
		}
	}
	if l, _ := parseLevel("connect failed", 3); l != "error" {
		t.Error("journal priority 3")
	}
	if ts, msg, ok := parseCRI("2026-11-16T03:00:05.123456789Z stderr F hello world"); !ok || msg != "hello world" || ts.Second() != 5 {
		t.Errorf("CRI %v %q %v", ts, msg, ok)
	}
	if replicaVolume("pvc-3f8e2a10-6c1d-4b8e-9f3a-1c2d3e4f5a6b-1a2b3c4d") != "pvc-3f8e2a10-6c1d-4b8e-9f3a-1c2d3e4f5a6b" {
		t.Error("replicaVolume")
	}
}

func TestMultipathBlacklist(t *testing.T) {
	for conf, want := range map[string]bool{
		"defaults {\n user_friendly_names yes\n}\n":                                           false,
		"blacklist {\n    devnode \"^sd[a-z0-9]+\"\n}\n":                                      true,
		"blacklist {\n  device {\n    vendor \"IET\"\n    product \"VIRTUAL-DISK\"\n  }\n}\n": true,
		"blacklist {\n    devnode \"^nvme\"\n}\n":                                             false,
		"# blacklist {\n#    devnode \"^sd[a-z0-9]+\"\n# }\n":                                 false,
	} {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, "etc"), 0o755)
		os.WriteFile(filepath.Join(dir, "etc", "multipath.conf"), []byte(conf), 0o644)
		if got := multipathBlacklisted(hostfs.New(dir)); got != want {
			t.Errorf("%q: %v", conf, got)
		}
	}
}

func TestNVMeOverTCP(t *testing.T) {
	dir := t.TempDir()
	w := func(p, v string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(v+"\n"), 0o644)
	}
	c := "/sys/class/nvme/nvme2"
	w(c+"/transport", "tcp")
	w(c+"/state", "connecting")
	w(c+"/subsysnqn", "nqn.2023-01.io.longhorn.spdk:vol-a")
	w(c+"/address", "traddr=10.42.3.30,trsvcid=4420,src_addr=10.42.3.1")
	w(c+"/ctrl_loss_tmo", "600")
	w(c+"/nvme2c2n1/size", "0")
	w("/sys/class/nvme/nvme0/transport", "pcie")
	ss := sysfs.ReadSessions(hostfs.New(dir))
	if len(ss) != 1 {
		t.Fatalf("sessions %+v", ss)
	}
	s := ss[0]
	if s.Transport != "nvme-tcp" || s.Portal != "10.42.3.30:4420" || s.Local != "10.42.3.1" || s.State != "connecting" ||
		strings.Join(s.Disks, ",") != "nvme2n1" || s.RecoveryTmo == nil || *s.RecoveryTmo != 600 || LonghornVolume(s.Target) != "vol-a" {
		t.Errorf("%+v", s)
	}
	rs := model.RemoteSession{ID: s.ID, Transport: s.Transport, State: s.State}
	h := &model.Header{}
	sessionFindings(h, &rs, time.Now(), "")
	if len(h.Findings) != 1 || h.Findings[0].Code != "nvmeof_state" {
		t.Errorf("findings %+v", h.Findings)
	}
}
