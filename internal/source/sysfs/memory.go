package sysfs

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/hostfs"
)

// Meminfo holds /proc/meminfo values in bytes (HugePages_* are counts).
type Meminfo map[string]uint64

// ParseMeminfo parses /proc/meminfo (or a node's meminfo, whose lines start
// with "Node N "). Values with a "kB" unit are converted to bytes.
func ParseMeminfo(data []byte) (Meminfo, error) {
	m := Meminfo{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Node ") {
			if f := strings.Fields(line); len(f) > 2 {
				line = strings.Join(f[2:], " ")
			}
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		m[strings.TrimSpace(k)] = n
	}
	if _, ok := m["MemTotal"]; !ok {
		return m, fmt.Errorf("meminfo: no MemTotal")
	}
	return m, nil
}

// MemoryBlocks summarizes /sys/devices/system/memory.
type MemoryBlocks struct {
	BlockSize uint64
	Online    int
	Offline   int
	// OfflineIDs lists offline block numbers (first 32).
	OfflineIDs []int
}

// OnlineBytes is the memory the kernel manages, including its own memory
// (unlike MemTotal, which excludes kernel code and reservations).
func (b MemoryBlocks) OnlineBytes() uint64 { return uint64(b.Online) * b.BlockSize }

// ReadMemoryBlocks reads memory hotplug blocks. block_size_bytes is hex.
func ReadMemoryBlocks(f hostfs.FS) (MemoryBlocks, error) {
	var b MemoryBlocks
	s, err := hostfs.String(f, "/sys/devices/system/memory/block_size_bytes")
	if err != nil {
		return b, err
	}
	if b.BlockSize, err = strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64); err != nil || b.BlockSize == 0 {
		return b, fmt.Errorf("block_size_bytes %q: invalid", s)
	}
	dirs, _ := f.Glob("/sys/devices/system/memory/memory[0-9]*")
	sort.Slice(dirs, func(i, j int) bool { return natLess(dirs[i], dirs[j]) })
	for _, d := range dirs {
		st := hostfs.StringOr(f, d+"/state", "")
		if st == "" {
			if hostfs.StringOr(f, d+"/online", "1") == "1" {
				st = "online"
			}
		}
		if st == "online" {
			b.Online++
			continue
		}
		b.Offline++
		if len(b.OfflineIDs) < 32 {
			id, _ := strconv.Atoi(strings.TrimPrefix(path.Base(d), "memory"))
			b.OfflineIDs = append(b.OfflineIDs, id)
		}
	}
	if b.Online+b.Offline == 0 {
		return b, fmt.Errorf("no memory blocks")
	}
	return b, nil
}

// EDACController is one /sys/devices/system/edac/mc/mcN.
type EDACController struct {
	Name      string // mc0
	Type      string // mc_name: "Intel_10nm Socket#0 IMC#0", "F19h_M10h", ...
	SizeBytes uint64
	CE, UE    uint64
	CENoInfo  uint64
	UENoInfo  uint64
	DIMMs     []EDACDIMM
}

// EDACDIMM is one dimmN (or a csrow channel on old drivers).
type EDACDIMM struct {
	Name      string // dimm0 or csrow0_ch1
	Label     string // dimm_label: "CPU_SrcID#0_MC#0_Chan#0_DIMM#0" or a ras-mc-ctl label
	Location  string // "channel 0 slot 0"
	SizeBytes uint64
	MemType   string
	CE, UE    uint64
}

// ReadEDAC reads all EDAC memory controllers. An empty result with nil error
// means the EDAC core is present but no controller driver is loaded.
func ReadEDAC(f hostfs.FS) ([]EDACController, error) {
	if !hostfs.Exists(f, "/sys/devices/system/edac/mc") {
		return nil, fmt.Errorf("EDAC not available in this kernel")
	}
	dirs, _ := f.Glob("/sys/devices/system/edac/mc/mc[0-9]*")
	sort.Slice(dirs, func(i, j int) bool { return natLess(dirs[i], dirs[j]) })
	var out []EDACController
	for _, d := range dirs {
		c := EDACController{Name: path.Base(d), Type: hostfs.StringOr(f, d+"/mc_name", "")}
		c.CE = uintOr(f, d+"/ce_count")
		c.UE = uintOr(f, d+"/ue_count")
		c.CENoInfo = uintOr(f, d+"/ce_noinfo_count")
		c.UENoInfo = uintOr(f, d+"/ue_noinfo_count")
		c.SizeBytes = uintOr(f, d+"/size_mb") << 20
		dimms, _ := f.Glob(d + "/dimm[0-9]*")
		if len(dimms) == 0 {
			dimms, _ = f.Glob(d + "/rank[0-9]*")
		}
		sort.Slice(dimms, func(i, j int) bool { return natLess(dimms[i], dimms[j]) })
		for _, dd := range dimms {
			c.DIMMs = append(c.DIMMs, EDACDIMM{
				Name:      path.Base(dd),
				Label:     hostfs.StringOr(f, dd+"/dimm_label", ""),
				Location:  hostfs.StringOr(f, dd+"/dimm_location", ""),
				SizeBytes: uintOr(f, dd+"/size") << 20, // MiB
				MemType:   hostfs.StringOr(f, dd+"/dimm_mem_type", ""),
				CE:        uintOr(f, dd+"/dimm_ce_count"),
				UE:        uintOr(f, dd+"/dimm_ue_count"),
			})
		}
		if len(c.DIMMs) == 0 {
			c.DIMMs = readCsrows(f, d)
		}
		out = append(out, c)
	}
	return out, nil
}

// readCsrows handles drivers that only expose the legacy csrow interface.
func readCsrows(f hostfs.FS, mc string) []EDACDIMM {
	rows, _ := f.Glob(mc + "/csrow[0-9]*")
	sort.Slice(rows, func(i, j int) bool { return natLess(rows[i], rows[j]) })
	var out []EDACDIMM
	for _, r := range rows {
		chans, _ := f.Glob(r + "/ch[0-9]*_ce_count")
		sort.Strings(chans)
		for _, ch := range chans {
			prefix := strings.TrimSuffix(path.Base(ch), "_ce_count")
			out = append(out, EDACDIMM{
				Name:     path.Base(r) + "_" + prefix,
				Label:    hostfs.StringOr(f, r+"/"+prefix+"_dimm_label", ""),
				Location: path.Base(r) + " " + prefix,
				MemType:  hostfs.StringOr(f, r+"/mem_type", ""),
				CE:       uintOr(f, ch),
			})
		}
		if len(chans) == 0 {
			out = append(out, EDACDIMM{Name: path.Base(r), Location: path.Base(r),
				CE: uintOr(f, r+"/ce_count"), UE: uintOr(f, r+"/ue_count"), SizeBytes: uintOr(f, r+"/size_mb") << 20})
		}
	}
	return out
}

func uintOr(f hostfs.FS, p string) uint64 {
	v, _ := hostfs.Uint(f, p)
	return v
}

// NodeMemory is the memory of one NUMA node.
type NodeMemory struct {
	ID         int
	TotalBytes uint64
	FreeBytes  uint64
}

// ReadNodeMemory reads /sys/devices/system/node/node*/meminfo.
func ReadNodeMemory(f hostfs.FS) []NodeMemory {
	dirs, _ := f.Glob("/sys/devices/system/node/node[0-9]*")
	sort.Slice(dirs, func(i, j int) bool { return natLess(dirs[i], dirs[j]) })
	var out []NodeMemory
	for _, d := range dirs {
		b, err := f.ReadFile(d + "/meminfo")
		if err != nil {
			continue
		}
		mi, err := ParseMeminfo(b)
		if err != nil {
			continue
		}
		id, _ := strconv.Atoi(strings.TrimPrefix(path.Base(d), "node"))
		out = append(out, NodeMemory{ID: id, TotalBytes: mi["MemTotal"], FreeBytes: mi["MemFree"]})
	}
	return out
}
