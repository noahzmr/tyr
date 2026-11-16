// Package pci reads PCI devices from sysfs and resolves vendor/device names
// from the system's pci.ids database (hwdata/pciutils).
package pci

import (
	"bufio"
	"bytes"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/noahzmr/tyr/internal/hostfs"
)

// Device is one PCI function.
type Device struct {
	Address      string // 0000:3b:00.0
	Path         string // canonical sysfs path
	Class        uint32 // 0x030000
	Vendor       uint16
	Device       uint16
	SubVendor    uint16
	SubDevice    uint16
	Driver       string
	NUMANode     int
	LinkSpeed    string // "16.0 GT/s PCIe"
	LinkWidth    *int
	MaxLinkSpeed string
	MaxLinkWidth *int
	PowerState   string // D0, D3hot, D3cold
}

// ClassBase returns the base class byte (0x03 display, 0x02 network, 0x12 accelerator).
func (d Device) ClassBase() uint8 { return uint8(d.Class >> 16) }

// ClassSub returns base<<8|subclass, e.g. 0x0302 (3D controller), 0x1180.
func (d Device) ClassSub() uint16 { return uint16(d.Class >> 8) }

func hexU(f hostfs.FS, p string) uint64 {
	s := strings.TrimPrefix(hostfs.StringOr(f, p, ""), "0x")
	v, _ := strconv.ParseUint(s, 16, 64)
	return v
}

func intp(f hostfs.FS, p string) *int {
	v, err := hostfs.Int(f, p)
	if err != nil || v <= 0 || v > 64 {
		return nil
	}
	i := int(v)
	return &i
}

// Read reads one device by its sysfs directory (any path that resolves to
// the device, e.g. /sys/class/net/eth0/device).
func Read(f hostfs.FS, dir string) (Device, bool) {
	real, err := f.EvalSymlinks(dir)
	if err != nil || !hostfs.Exists(f, real+"/vendor") || !hostfs.Exists(f, real+"/class") {
		return Device{}, false
	}
	d := Device{
		Address:   path.Base(real),
		Path:      real,
		Class:     uint32(hexU(f, real+"/class")),
		Vendor:    uint16(hexU(f, real+"/vendor")),
		Device:    uint16(hexU(f, real+"/device")),
		SubVendor: uint16(hexU(f, real+"/subsystem_vendor")),
		SubDevice: uint16(hexU(f, real+"/subsystem_device")),
		NUMANode:  -1,
		LinkSpeed: hostfs.StringOr(f, real+"/current_link_speed", ""), LinkWidth: intp(f, real+"/current_link_width"),
		MaxLinkSpeed: hostfs.StringOr(f, real+"/max_link_speed", ""), MaxLinkWidth: intp(f, real+"/max_link_width"),
		PowerState: hostfs.StringOr(f, real+"/power_state", ""),
	}
	if n, err := hostfs.Int(f, real+"/numa_node"); err == nil {
		d.NUMANode = int(n)
	}
	if drv, err := f.Readlink(real + "/driver"); err == nil {
		d.Driver = path.Base(drv)
	}
	if strings.HasPrefix(d.LinkSpeed, "Unknown") {
		d.LinkSpeed = ""
	}
	return d, true
}

// List returns all PCI devices sorted by address.
func List(f hostfs.FS) []Device {
	dirs, _ := f.Glob("/sys/bus/pci/devices/*")
	sort.Strings(dirs)
	var out []Device
	for _, d := range dirs {
		if dev, ok := Read(f, d); ok {
			out = append(out, dev)
		}
	}
	return out
}

// --- pci.ids --------------------------------------------------------------

var idPaths = []string{"/usr/share/hwdata/pci.ids", "/usr/share/misc/pci.ids", "/usr/share/pci.ids", "/usr/local/share/pci.ids"}

type vendorEntry struct {
	name    string
	devices map[uint16]string
}

type db struct{ vendors map[uint16]*vendorEntry }

var (
	dbMu    sync.Mutex
	dbCache = map[string]*db{}
)

func load(f hostfs.FS) *db {
	key := f.Real("/")
	dbMu.Lock()
	defer dbMu.Unlock()
	if d, ok := dbCache[key]; ok {
		return d
	}
	d := &db{vendors: map[uint16]*vendorEntry{}}
	for _, p := range idPaths {
		b, err := f.ReadFile(p)
		if err != nil {
			continue
		}
		parseIDs(b, d)
		break
	}
	dbCache[key] = d
	return d
}

func parseIDs(b []byte, d *db) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var cur *vendorEntry
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		if strings.HasPrefix(line, "C ") {
			break // device classes follow; not needed
		}
		switch {
		case line[0] != '\t':
			if len(line) < 6 {
				continue
			}
			id, err := strconv.ParseUint(line[:4], 16, 16)
			if err != nil {
				cur = nil
				continue
			}
			cur = &vendorEntry{name: strings.TrimSpace(line[4:]), devices: map[uint16]string{}}
			d.vendors[uint16(id)] = cur
		case strings.HasPrefix(line, "\t\t"):
			// subsystem ids: not needed
		case cur != nil:
			l := line[1:]
			if len(l) < 6 {
				continue
			}
			if id, err := strconv.ParseUint(l[:4], 16, 16); err == nil {
				cur.devices[uint16(id)] = strings.TrimSpace(l[4:])
			}
		}
	}
}

// Names returns vendor and device names, falling back to hex IDs.
func Names(f hostfs.FS, vendor, device uint16) (string, string) {
	d := load(f)
	vn, dn := "", ""
	if v := d.vendors[vendor]; v != nil {
		vn, dn = v.name, v.devices[device]
	}
	if vn == "" {
		vn = "0x" + strconv.FormatUint(uint64(vendor), 16)
	}
	if dn == "" {
		dn = "device 0x" + strconv.FormatUint(uint64(device), 16)
	}
	return shortVendor(vn), dn
}

// shortVendor trims legal suffixes: "Intel Corporation" -> "Intel".
func shortVendor(v string) string {
	if i := strings.Index(v, " ["); i > 0 {
		// "Advanced Micro Devices, Inc. [AMD/ATI]" -> "AMD/ATI"
		if j := strings.IndexByte(v[i:], ']'); j > 0 {
			return v[i+2 : i+j]
		}
	}
	for _, suf := range []string{" Corporation", " Corp.", ", Inc.", " Inc.", " Co., Ltd.", " Technologies", " Ltd."} {
		v = strings.TrimSuffix(v, suf)
	}
	return v
}

// WidthDegraded reports whether the link trained below its maximum.
// Width reductions indicate a real problem; speed reductions on GPUs are
// often power management at idle.
func (d Device) WidthDegraded() bool {
	return d.LinkWidth != nil && d.MaxLinkWidth != nil && *d.LinkWidth < *d.MaxLinkWidth
}

// SpeedDegraded reports whether the current link speed is below the maximum.
func (d Device) SpeedDegraded() bool {
	return gts(d.LinkSpeed) > 0 && gts(d.MaxLinkSpeed) > 0 && gts(d.LinkSpeed) < gts(d.MaxLinkSpeed)
}

func gts(s string) float64 {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// Gen converts a link speed to a PCIe generation ("16.0 GT/s" -> 4).
func Gen(s string) int {
	switch gts(s) {
	case 2.5:
		return 1
	case 5:
		return 2
	case 8:
		return 3
	case 16:
		return 4
	case 32:
		return 5
	case 64:
		return 6
	}
	return 0
}
