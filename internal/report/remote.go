package report

import (
	"fmt"
	"strings"

	"github.com/noahzmr/tyr/internal/model"
)

// remoteSection: iSCSI / NVMe-oF sessions, their problems this boot, the
// node checks, Longhorn and the volume errors in the node's logs.
func remoteSection(m *md, x *model.Storage) {
	rs := x.Remote
	if rs == nil {
		return
	}
	if len(rs.Sessions) > 0 {
		m.h(3, "Network storage sessions")
		var rows [][]string
		for _, s := range rs.Sessions {
			target := s.Target
			if s.Longhorn != "" {
				target = "Longhorn " + s.Longhorn
			}
			last := ""
			if s.LastError != "" {
				last = s.LastErrorAt.UTC().Format("2006-01-02 15:04") + " " + s.LastError
			}
			rows = append(rows, []string{s.ID, transportName(s.Transport), target, s.Portal, s.Interface, s.State, strings.Join(s.Disks, ", "),
				fmt.Sprint(s.ConnErrors), fmt.Sprint(s.RecoveryTimeouts), last, status(s.Status)})
		}
		m.table([]string{"Session", "Transport", "Target", "Portal", "Via", "State", "Disks", ">Drops", ">Timeouts", "Last problem", "Status"}, rows)
		if rs.Initiator != "" {
			m.p("_iSCSI initiator: %s_", rs.Initiator)
		}
	}
	var evs [][]string
	for _, e := range rs.Events {
		if e.Severity >= model.StatusWarn {
			evs = append(evs, []string{e.Time.UTC().Format("2006-01-02 15:04:05"), e.Session, strings.ReplaceAll(e.Kind, "_", " "), e.Message})
		}
	}
	if n := len(evs); n > 0 {
		if n > 40 {
			evs = evs[n-40:]
		}
		m.h(3, "Network storage problems this boot")
		m.table([]string{"Time (UTC)", "Session", "Event", "Message"}, evs)
	}
	if len(rs.Checks) > 0 {
		m.h(3, "Node checks (iSCSI, Longhorn)")
		var rows [][]string
		for _, c := range rs.Checks {
			res := "OK"
			fix := ""
			switch {
			case !c.OK && c.Status == model.StatusOK:
				res = "optional"
			case !c.OK:
				res, fix = c.Status.String(), c.Fix
			}
			rows = append(rows, []string{c.Name, res, c.Detail, fix})
		}
		m.table([]string{"Check", "Result", "Detail", "Fix"}, rows)
	}
	if lh := x.Longhorn; lh != nil {
		m.h(3, "Longhorn")
		m.kv([2]string{"Engine", lh.Engine}, [2]string{"Kubernetes", lh.Kubernetes}, [2]string{"Found by", strings.Join(lh.Evidence, " · ")})
		if len(lh.Volumes) > 0 {
			var rows [][]string
			for _, v := range lh.Volumes {
				rows = append(rows, []string{v.Name, v.Device, v.Session, map[bool]string{true: "read-only", false: "read-write"}[v.ReadOnly], fmt.Sprint(len(v.Mounts)), status(v.Status)})
			}
			m.table([]string{"Volume attached here", "Device", "Session", "Filesystem", ">Mounts", "Status"}, rows)
		}
		if len(lh.Disks) > 0 {
			var rows [][]string
			for _, d := range lh.Disks {
				rows = append(rows, []string{d.Path, d.Device, strings.Join(d.Disks, ", "), bytesSI(d.SizeBytes), f0(d.UsedPercent, " %"), fmt.Sprint(d.Replicas), status(d.Status)})
			}
			m.table([]string{"Data path", "Filesystem", "Disks", ">Size", ">Used", ">Replicas", "Status"}, rows)
		}
		if len(lh.Replicas) > 0 {
			var rows [][]string
			for _, r := range lh.Replicas {
				state := map[bool]string{true: "rebuilding", false: ""}[r.Rebuilding]
				if r.Error != "" {
					state = "failed: " + r.Error
				}
				rows = append(rows, []string{r.Volume, r.Disk, bytesSI(r.SizeBytes), state})
			}
			m.table([]string{"Replica of", "Data path", ">Size", "State"}, rows)
		}
	}
	if len(rs.Logs) > 0 {
		m.h(3, "Volume errors in the node's logs (24 h)")
		var rows [][]string
		for _, l := range rs.Logs {
			rows = append(rows, []string{l.Time.UTC().Format("2006-01-02 15:04"), l.Source, l.Level, fmt.Sprint(l.Count), strings.Join(strings.Fields(l.Message), " ")})
		}
		m.table([]string{"Last (UTC)", "Source", "Level", ">Count", "Message"}, rows)
	}
}

func transportName(t string) string {
	if n, ok := map[string]string{"iscsi": "iSCSI", "fc": "Fibre Channel", "nvme-tcp": "NVMe/TCP", "nvme-rdma": "NVMe/RDMA", "nvme-fc": "NVMe/FC", "nvme": "NVMe-oF"}[t]; ok {
		return n
	}
	return strings.ToUpper(t)
}
