package sysfs

import (
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/hostfs"
)

// BlockDev is one whole-disk block device from /sys/block.
type BlockDev struct {
	Name       string
	SysPath    string // canonical device path (…/nvme/nvme0 for NVMe namespaces' parent)
	SizeBytes  uint64
	Rotational bool
	Removable  bool
	ReadOnly   bool
	Model      string
	Vendor     string
	Serial     string
	WWN        string
	Firmware   string
	Interface  string // nvme, sata, sas, usb, virtio, scsi, mmc, xen, iscsi, fc
	NVMeCtrl   string // nvme0 (controller of an NVMe namespace)
	IOErrors   *uint64
	Remote     *RemoteLink // iSCSI / Fibre Channel: a volume of a storage system, not a local disk
	Holders    []string
	Partitions []string
}

// RemoteLink is the path of a network block device: the iSCSI session or
// the Fibre Channel remote port behind it.
type RemoteLink struct {
	Transport string // iscsi, fc
	Session   string // session3, rport-2:0-1
	Target    string // iSCSI target IQN, FC remote port WWPN
	Portal    string // iSCSI portal address:port
	State     string // LOGGED_IN, FAILED, FREE (iSCSI); Online, Blocked, … (FC)
}

// skipBlock lists device name prefixes that are not physical disks.
var skipBlock = []string{"loop", "ram", "zram", "dm-", "md", "sr", "nbd", "fd", "zd", "drbd"}

// ReadBlockDevs lists physical disks.
func ReadBlockDevs(f hostfs.FS) []BlockDev {
	dirs, _ := f.Glob("/sys/block/*")
	sort.Slice(dirs, func(i, j int) bool { return natLess(dirs[i], dirs[j]) })
	byID := diskSerials(f)
	var out []BlockDev
	for _, d := range dirs {
		name := path.Base(d)
		if hasPrefix(name, skipBlock) || strings.Contains(name, "boot") || strings.HasSuffix(name, "rpmb") {
			continue
		}
		b := BlockDev{Name: name}
		if n, err := hostfs.Uint(f, d+"/size"); err == nil {
			b.SizeBytes = n * 512
		}
		if b.SizeBytes == 0 && !hostfs.Exists(f, d+"/device") {
			continue
		}
		b.Rotational = hostfs.StringOr(f, d+"/queue/rotational", "0") == "1"
		b.Removable = hostfs.StringOr(f, d+"/removable", "0") == "1"
		b.ReadOnly = hostfs.StringOr(f, d+"/ro", "0") == "1"
		if p, err := f.EvalSymlinks(d + "/device"); err == nil {
			b.SysPath = p
		}
		dev := d + "/device"
		switch {
		case strings.HasPrefix(name, "nvme"):
			b.Interface = "nvme"
			b.NVMeCtrl = path.Base(b.SysPath)
			b.Model = hostfs.StringOr(f, dev+"/model", "")
			b.Serial = hostfs.StringOr(f, dev+"/serial", "")
			b.Firmware = hostfs.StringOr(f, dev+"/firmware_rev", "")
			if t := hostfs.StringOr(f, dev+"/transport", "pcie"); t != "pcie" {
				b.Interface = "nvme-" + t
			}
		case strings.HasPrefix(name, "vd"):
			b.Interface = "virtio"
		case strings.HasPrefix(name, "xvd"):
			b.Interface = "xen"
		case strings.HasPrefix(name, "mmcblk"):
			b.Interface = "mmc"
			b.Model = hostfs.StringOr(f, dev+"/name", "")
			b.Serial = hostfs.StringOr(f, dev+"/serial", "")
		default:
			b.Model = hostfs.StringOr(f, dev+"/model", "")
			b.Vendor = hostfs.StringOr(f, dev+"/vendor", "")
			b.Firmware = hostfs.StringOr(f, dev+"/rev", "")
			switch p := b.SysPath; {
			case strings.Contains(p, "/session"):
				b.Interface = "iscsi"
				b.Remote = iscsiLink(f, p)
			case strings.Contains(p, "/rport-"):
				b.Interface = "fc"
				b.Remote = fcLink(f, p)
			case strings.Contains(p, "/usb"):
				b.Interface = "usb"
			case strings.Contains(p, "/ata"):
				b.Interface = "sata"
			case strings.Contains(p, "/end_device-") || strings.Contains(p, "/expander-") || strings.Contains(p, "/port-"):
				b.Interface = "sas"
			case strings.Contains(p, "/virtio"):
				b.Interface = "virtio"
			default:
				b.Interface = "scsi"
			}
			if v, ok := ReadIOErrCount(f, name); ok {
				b.IOErrors = &v
			}
		}
		for _, p := range []string{d + "/wwid", dev + "/wwid"} {
			if w := hostfs.StringOr(f, p, ""); w != "" {
				b.WWN = w
				break
			}
		}
		if b.Vendor == "ATA" {
			b.Vendor = ""
		}
		if b.Serial == "" {
			b.Serial = byID[name]
		}
		b.Holders = append(b.Holders, globBase(f, d+"/holders/*")...)
		for _, p := range globBase(f, d+"/"+name+"*") {
			b.Partitions = append(b.Partitions, p)
			b.Holders = append(b.Holders, globBase(f, d+"/"+p+"/holders/*")...)
		}
		out = append(out, b)
	}
	return out
}

func hasPrefix(s string, ps []string) bool {
	for _, p := range ps {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func globBase(f hostfs.FS, pattern string) []string {
	m, _ := f.Glob(pattern)
	out := make([]string, 0, len(m))
	for _, p := range m {
		out = append(out, path.Base(p))
	}
	sort.Strings(out)
	return out
}

// diskSerials derives serial numbers from /dev/disk/by-id link names
// (ata-MODEL_SERIAL, scsi-SATA_MODEL_SERIAL, nvme-MODEL_SERIAL, usb-…),
// which are world-readable unlike the VPD pages.
func diskSerials(f hostfs.FS) map[string]string {
	out := map[string]string{}
	ents, _ := f.ReadDir("/dev/disk/by-id")
	for _, e := range ents {
		n := e.Name()
		if strings.Contains(n, "-part") || strings.HasPrefix(n, "wwn-") || strings.HasPrefix(n, "dm-") || strings.HasPrefix(n, "md-") || strings.HasPrefix(n, "lvm-") {
			continue
		}
		target, err := f.Readlink("/dev/disk/by-id/" + n)
		if err != nil {
			continue
		}
		dev := path.Base(target)
		i := strings.LastIndexByte(n, '_')
		if i < 0 || i == len(n)-1 {
			continue
		}
		serial := n[i+1:]
		if strings.HasPrefix(n, "nvme-eui.") || strings.HasPrefix(n, "scsi-3") || strings.HasPrefix(n, "scsi-0") {
			continue
		}
		if _, ok := out[dev]; !ok || strings.HasPrefix(n, "ata-") || strings.HasPrefix(n, "nvme-") {
			out[dev] = serial
		}
	}
	return out
}

// Mount is one line of /proc/self/mountinfo.
type Mount struct {
	Mountpoint string
	Source     string
	FSType     string
	Root       string
	MajMin     string
	ReadOnly   bool
}

// ParseMountinfo parses /proc/self/mountinfo.
func ParseMountinfo(data []byte) []Mount {
	var out []Mount
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		sep := -1
		for i, x := range f {
			if x == "-" {
				sep = i
				break
			}
		}
		if len(f) < 6 || sep < 0 || sep+2 >= len(f) {
			continue
		}
		m := Mount{MajMin: f[2], Root: unescapeMount(f[3]), Mountpoint: unescapeMount(f[4]), FSType: f[sep+1], Source: unescapeMount(f[sep+2])}
		for _, o := range strings.Split(f[5], ",") {
			if o == "ro" {
				m.ReadOnly = true
			}
		}
		out = append(out, m)
	}
	return out
}

// unescapeMount decodes \040 (space) style octal escapes.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ReadIOErrCount reads a SCSI disk's ioerr_cnt: commands that completed
// with any error since the driver loaded. That includes commands the device
// rejects (unsupported SMART or pass-through queries), not only failed I/O.
func ReadIOErrCount(f hostfs.FS, name string) (uint64, bool) {
	s, err := hostfs.String(f, "/sys/block/"+name+"/device/ioerr_cnt")
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(s), "0x"), 16, 64)
	return v, err == nil
}

// pathPart returns the path element starting with prefix ("session3").
func pathPart(p, prefix string) string {
	for _, e := range strings.Split(p, "/") {
		if strings.HasPrefix(e, prefix) {
			return e
		}
	}
	return ""
}

// iscsiLink reads the session behind an iSCSI LUN: target IQN, state and
// the portal of its first connection.
func iscsiLink(f hostfs.FS, sysPath string) *RemoteLink {
	sess := pathPart(sysPath, "session")
	l := &RemoteLink{Transport: "iscsi", Session: sess}
	if sess == "" {
		return l
	}
	base := "/sys/class/iscsi_session/" + sess
	l.Target = hostfs.StringOr(f, base+"/targetname", "")
	l.State = hostfs.StringOr(f, base+"/state", "")
	conns, _ := f.Glob("/sys/class/iscsi_connection/connection" + strings.TrimPrefix(sess, "session") + ":*")
	sort.Strings(conns)
	if len(conns) > 0 {
		addr := hostfs.StringOr(f, conns[0]+"/persistent_address", hostfs.StringOr(f, conns[0]+"/address", ""))
		port := hostfs.StringOr(f, conns[0]+"/persistent_port", hostfs.StringOr(f, conns[0]+"/port", ""))
		if addr != "" {
			l.Portal = addr
			if port != "" {
				l.Portal += ":" + port
			}
		}
	}
	return l
}

// fcLink reads the Fibre Channel remote port behind a LUN.
func fcLink(f hostfs.FS, sysPath string) *RemoteLink {
	rport := pathPart(sysPath, "rport-")
	l := &RemoteLink{Transport: "fc", Session: rport}
	if rport != "" {
		base := "/sys/class/fc_remote_ports/" + rport
		l.Target = hostfs.StringOr(f, base+"/port_name", "")
		l.State = hostfs.StringOr(f, base+"/port_state", "")
	}
	return l
}
