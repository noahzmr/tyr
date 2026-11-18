package storage

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

// nodeChecks checks what iSCSI and Longhorn need on this node, like
// `longhornctl check preflight` but offline. They run on hosts with iSCSI
// sessions (the iSCSI part) or on a Longhorn node (all of it); a missing
// piece is critical where Longhorn needs it to attach volumes.
func nodeChecks(env *collector.Env, out *model.Storage, lh *model.Longhorn, sessions []sysfs.Session, procs []proc) []model.NodeCheck {
	f := env.FS
	iscsi := lh != nil
	live := false
	for _, s := range sessions {
		if s.Transport == "iscsi" {
			iscsi, live = true, true
		}
	}
	var cs []model.NodeCheck
	put := func(c model.NodeCheck) {
		if c.OK {
			c.Status = model.StatusOK
		}
		cs = append(cs, c)
	}
	need := model.StatusWarn // what a missing piece costs
	if lh != nil {
		need = model.StatusCrit
	}
	module := func(names ...string) bool {
		for _, n := range names {
			if hostfs.Exists(f, "/sys/module/"+n) {
				return true
			}
		}
		return false
	}
	if iscsi {
		_, err := env.Exec.LookPath("iscsiadm")
		put(model.NodeCheck{ID: "open_iscsi", Scope: "iscsi", Name: "open-iscsi (iscsiadm)", OK: err == nil, Status: need,
			Detail: map[bool]string{true: "installed", false: "iscsiadm is not installed: volumes cannot be attached"}[err == nil],
			Fix:    "apt install open-iscsi  ·  dnf install iscsi-initiator-utils  ·  zypper install open-iscsi"})
		up := running(procs, "iscsid")
		sev := need
		if live {
			sev = model.StatusCrit // nobody recovers a dropped connection
		}
		put(model.NodeCheck{ID: "iscsid", Scope: "iscsi", Name: "iscsid", OK: up, Status: sev,
			Detail: map[bool]string{true: "running", false: "not running: no logins, and a dropped connection is not recovered"}[up],
			Fix:    "systemctl enable --now iscsid"})
		tcp := module("iscsi_tcp")
		if !tcp && live && lh == nil { // the sessions run over an offload HBA (bnx2i, qla4xxx, be2iscsi)
			tcp = true
		}
		put(model.NodeCheck{ID: "iscsi_tcp", Scope: "iscsi", Name: "kernel module iscsi_tcp", OK: tcp, Status: need,
			Detail: map[bool]string{true: "loaded", false: "not loaded: iscsiadm cannot log in over TCP"}[tcp],
			Fix:    "modprobe iscsi_tcp; echo iscsi_tcp > /etc/modules-load.d/iscsi.conf"})
		name := initiatorName(f)
		c := model.NodeCheck{ID: "initiator_name", Scope: "iscsi", Name: "iSCSI initiator name", OK: name != "", Status: need,
			Detail: name + " (must be unique per node: a cloned image copies it)",
			Fix:    "systemctl restart iscsid  (generates it)  ·  or: echo InitiatorName=$(iscsi-iname) > /etc/iscsi/initiatorname.iscsi"}
		if name == "" {
			c.Detail = "/etc/iscsi/initiatorname.iscsi has no InitiatorName yet"
		}
		put(c)
	}
	if lh != nil {
		cs = append(cs, multipathCheck(f, out, procs))
		nfs := false
		for _, p := range []string{"/sbin/mount.nfs4", "/usr/sbin/mount.nfs4", "/sbin/mount.nfs", "/usr/sbin/mount.nfs"} {
			nfs = nfs || hostfs.Exists(f, p)
		}
		put(model.NodeCheck{ID: "nfs_client", Scope: "longhorn", Name: "NFS client", OK: nfs, Status: model.StatusWarn,
			Detail: map[bool]string{true: "installed", false: "mount.nfs4 is missing: ReadWriteMany volumes and NFS backup targets cannot be mounted"}[nfs],
			Fix:    "apt install nfs-common  ·  dnf install nfs-utils  ·  zypper install nfs-client"})
		_, cerr := env.Exec.LookPath("cryptsetup")
		crypt := cerr == nil
		put(model.NodeCheck{ID: "dm_crypt", Scope: "longhorn", Name: "cryptsetup (encrypted volumes)", OK: crypt, Status: model.StatusOK,
			Detail: map[bool]string{true: "installed", false: "not installed: only needed for encrypted volumes"}[crypt],
			Fix:    "apt install cryptsetup  ·  dnf install cryptsetup"})
		// the v2 data engine (SPDK): only a finding where it is in use
		v2 := strings.Contains(lh.Engine, "v2")
		sev := model.StatusOK
		if v2 {
			sev = model.StatusCrit
		}
		nt := module("nvme_tcp")
		put(model.NodeCheck{ID: "nvme_tcp", Scope: "longhorn-v2", Name: "kernel module nvme_tcp", OK: nt, Status: sev,
			Detail: map[bool]string{true: "loaded", false: "not loaded: the v2 data engine attaches volumes over NVMe/TCP"}[nt],
			Fix:    "modprobe nvme_tcp; echo nvme_tcp > /etc/modules-load.d/nvme-tcp.conf"})
		uio := module("vfio_pci", "uio_pci_generic")
		put(model.NodeCheck{ID: "vfio_pci", Scope: "longhorn-v2", Name: "kernel module vfio_pci or uio_pci_generic", OK: uio, Status: sev,
			Detail: map[bool]string{true: "loaded", false: "neither is loaded: SPDK cannot take over NVMe disks"}[uio],
			Fix:    "modprobe vfio_pci  ·  or: modprobe uio_pci_generic"})
		hp, _ := strconv.Atoi(hostfs.StringOr(f, "/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages", "0"))
		put(model.NodeCheck{ID: "hugepages", Scope: "longhorn-v2", Name: "2 MiB huge pages", OK: hp >= 1024, Status: sev,
			Detail: fmt.Sprintf("%d of the 1024 the v2 data engine needs", hp),
			Fix:    "echo vm.nr_hugepages=1024 > /etc/sysctl.d/90-longhorn.conf; sysctl --system  (then restart the kubelet)"})
	}
	return cs
}

// multipathCheck: multipathd grabs Longhorn's sdX devices unless they are
// blacklisted, and the kubelet then fails with "already mounted or mount
// point busy".
func multipathCheck(f hostfs.FS, out *model.Storage, procs []proc) model.NodeCheck {
	c := model.NodeCheck{ID: "multipathd", Scope: "longhorn", Name: "multipathd and Longhorn devices",
		Fix: `add to /etc/multipath.conf: blacklist { devnode "^sd[a-z0-9]+" }  (or device { vendor "IET" product "VIRTUAL-DISK" }), then systemctl restart multipathd`}
	// a Longhorn LUN already held by a multipath map
	for _, d := range out.Disks {
		if d.Remote == nil || d.Remote.Longhorn == "" {
			continue
		}
		for _, hd := range d.Holders {
			if strings.HasPrefix(hd, "dm-") && strings.HasPrefix(hostfs.StringOr(f, "/sys/block/"+hd+"/dm/uuid", ""), "mpath-") {
				c.Status = model.StatusCrit
				c.Detail = fmt.Sprintf("multipathd has claimed %s (Longhorn volume %s) as %s: the volume cannot be mounted", d.Name, d.Remote.Longhorn, hd)
				return c
			}
		}
	}
	if !running(procs, "multipathd") {
		c.OK, c.Detail = true, "not running"
		return c
	}
	if multipathBlacklisted(f) {
		c.OK, c.Detail = true, "running, Longhorn's devices are blacklisted"
		return c
	}
	c.Status = model.StatusWarn
	c.Detail = "multipathd runs without a blacklist for Longhorn's devices: it can claim a volume's sdX and the mount fails (MountVolume.SetUp failed … already mounted or mount point busy)"
	return c
}

var (
	mpBlock   = regexp.MustCompile(`(?s)\bblacklist\s*\{(.*?)\n\s*\}`)
	mpDevnode = regexp.MustCompile(`devnode\s+"([^"]+)"`)
)

// multipathBlacklisted reports whether multipath.conf blacklists the sdX
// devices or tgt's IET VIRTUAL-DISK (what Longhorn's v1 volumes are).
func multipathBlacklisted(f hostfs.FS) bool {
	files := []string{"/etc/multipath.conf"}
	more, _ := f.Glob("/etc/multipath/conf.d/*.conf")
	files = append(files, more...)
	for _, p := range files {
		b, err := f.ReadFile(p)
		if err != nil {
			continue
		}
		for _, blk := range mpBlock.FindAllStringSubmatch(stripComments(string(b)), -1) {
			body := blk[1]
			if strings.Contains(body, `"IET"`) && strings.Contains(body, "VIRTUAL-DISK") {
				return true
			}
			for _, m := range mpDevnode.FindAllStringSubmatch(body, -1) {
				if re, err := regexp.Compile(m[1]); err == nil && re.MatchString("sdc") && re.MatchString("sdaa") {
					return true
				}
			}
		}
	}
	return false
}

func stripComments(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		if i := strings.IndexByte(l, '#'); i >= 0 {
			l = l[:i]
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}
