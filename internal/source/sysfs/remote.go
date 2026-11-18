package sysfs

import (
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/hostfs"
)

// Session is one iSCSI session or NVMe-over-Fabrics controller as sysfs
// shows it, whether or not it has a disk.
type Session struct {
	Transport    string // iscsi, nvme-tcp, nvme-rdma, nvme-fc
	ID           string // session3, nvme2
	Host         string // host9 (iSCSI): the SCSI host, "sd 9:0:0:1" in the kernel log
	Target       string // IQN, subsystem NQN
	Portal       string // address:port
	Local        string // the initiator's address
	NetDev       string // the interface the iSCSI host is bound to (iface binding)
	State        string // LOGGED_IN / FAILED / FREE; live / connecting / resetting / deleting
	ConnState    string // iSCSI connection: up / down / failed / bound
	RecoveryTmo  *int   // seconds: replacement_timeout / ctrl_loss_tmo
	HeaderDigest string
	DataDigest   string
	Disks        []string
}

// ReadSessions lists the iSCSI sessions and the NVMe-oF controllers.
func ReadSessions(f hostfs.FS) []Session {
	return append(readISCSI(f), readNVMeOF(f)...)
}

func intOr(f hostfs.FS, p string) *int {
	s := hostfs.StringOr(f, p, "")
	if n, err := strconv.Atoi(s); err == nil {
		return &n
	}
	return nil
}

func readISCSI(f hostfs.FS) []Session {
	dirs, _ := f.Glob("/sys/class/iscsi_session/session*")
	sort.Slice(dirs, func(i, j int) bool { return natLess(dirs[i], dirs[j]) })
	var out []Session
	for _, d := range dirs {
		id := path.Base(d)
		s := Session{Transport: "iscsi", ID: id,
			Target:      hostfs.StringOr(f, d+"/targetname", ""),
			State:       hostfs.StringOr(f, d+"/state", ""),
			RecoveryTmo: intOr(f, d+"/recovery_tmo"),
		}
		// …/platform/host9/session1/iscsi_session/session1: the device is
		// the session1 directory under the SCSI host
		dev, err := f.EvalSymlinks(d + "/device")
		if err != nil {
			if real, err2 := f.EvalSymlinks(d); err2 == nil && strings.Contains(real, "/iscsi_session/") {
				dev = path.Dir(path.Dir(real))
			}
		}
		if dev != "" {
			s.Host = pathPart(dev, "host")
			blocks, _ := f.Glob(dev + "/target*/*:*/block/*")
			for _, b := range blocks {
				s.Disks = append(s.Disks, path.Base(b))
			}
			sort.Slice(s.Disks, func(i, j int) bool { return natLess(s.Disks[i], s.Disks[j]) })
		}
		if s.Host != "" {
			h := "/sys/class/iscsi_host/" + s.Host
			s.Local = cleanAddr(hostfs.StringOr(f, h+"/ipaddress", ""))
			s.NetDev = cleanAddr(hostfs.StringOr(f, h+"/netdev", ""))
		}
		conns, _ := f.Glob("/sys/class/iscsi_connection/connection" + strings.TrimPrefix(id, "session") + ":*")
		sort.Strings(conns)
		if len(conns) > 0 {
			c := conns[0]
			addr := hostfs.StringOr(f, c+"/persistent_address", hostfs.StringOr(f, c+"/address", ""))
			port := hostfs.StringOr(f, c+"/persistent_port", hostfs.StringOr(f, c+"/port", ""))
			s.Portal = joinPortal(addr, port)
			s.ConnState = hostfs.StringOr(f, c+"/state", "")
			s.HeaderDigest = hostfs.StringOr(f, c+"/header_digest", "")
			s.DataDigest = hostfs.StringOr(f, c+"/data_digest", "")
		}
		out = append(out, s)
	}
	return out
}

// cleanAddr drops the placeholders sysfs prints for unset iscsi_host values.
func cleanAddr(s string) string {
	switch s {
	case "<NULL>", "(null)", "0.0.0.0", "default", "":
		return ""
	}
	return s
}

func joinPortal(addr, port string) string {
	if addr == "" {
		return ""
	}
	if strings.Contains(addr, ":") && !strings.HasPrefix(addr, "[") { // IPv6
		addr = "[" + addr + "]"
	}
	if port == "" {
		return addr
	}
	return addr + ":" + port
}

var nvmeHidden = regexp.MustCompile(`^nvme(\d+)c\d+n(\d+)$`)

func readNVMeOF(f hostfs.FS) []Session {
	dirs, _ := f.Glob("/sys/class/nvme/nvme*")
	sort.Slice(dirs, func(i, j int) bool { return natLess(dirs[i], dirs[j]) })
	var out []Session
	for _, d := range dirs {
		tr := hostfs.StringOr(f, d+"/transport", "pcie")
		if tr == "pcie" || tr == "loop" || tr == "apple-nvme" {
			continue
		}
		s := Session{Transport: "nvme-" + tr, ID: path.Base(d),
			Target:      hostfs.StringOr(f, d+"/subsysnqn", ""),
			State:       hostfs.StringOr(f, d+"/state", ""),
			RecoveryTmo: intOr(f, d+"/ctrl_loss_tmo"),
		}
		addr := ParseNVMeAddress(hostfs.StringOr(f, d+"/address", ""))
		s.Portal = joinPortal(addr["traddr"], addr["trsvcid"])
		s.Local = addr["src_addr"]
		s.NetDev = addr["host_iface"]
		seen := map[string]bool{}
		ns, _ := f.Glob(d + "/nvme*n*")
		for _, n := range ns {
			name := path.Base(n)
			if m := nvmeHidden.FindStringSubmatch(name); m != nil { // native multipath: the path of nvme<subsys>n<ns>
				name = "nvme" + m[1] + "n" + m[2]
			}
			if !seen[name] {
				seen[name] = true
				s.Disks = append(s.Disks, name)
			}
		}
		sort.Slice(s.Disks, func(i, j int) bool { return natLess(s.Disks[i], s.Disks[j]) })
		out = append(out, s)
	}
	return out
}

// ParseNVMeAddress splits "traddr=10.0.0.5,trsvcid=4420,src_addr=10.0.0.9".
func ParseNVMeAddress(s string) map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Split(strings.TrimSpace(s), ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

// nvmeLink reads the fabric controller behind an NVMe-oF namespace; ctrl
// is the controller directory (/sys/class/nvme/nvme2).
func nvmeLink(f hostfs.FS, ctrl, transport string) *RemoteLink {
	addr := ParseNVMeAddress(hostfs.StringOr(f, ctrl+"/address", ""))
	return &RemoteLink{Transport: "nvme-" + transport, Session: path.Base(ctrl),
		Target: hostfs.StringOr(f, ctrl+"/subsysnqn", ""),
		Portal: joinPortal(addr["traddr"], addr["trsvcid"]),
		State:  hostfs.StringOr(f, ctrl+"/state", "")}
}
