// Command genfixture generates the synthetic server fixtures under
// testdata/fixtures. Real captures (scripts/capture-fixtures.sh) are preferred;
// these exist because the scenarios we must test (offline cores, ECC errors,
// failed PSUs, degraded arrays and bonds) cannot be captured on demand.
//
// Usage: go run ./tools/genfixture [-out testdata/fixtures]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// scenario describes one generated host.
type scenario struct {
	Name        string
	Description string
	Hostname    string

	Sockets, CoresPerSocket, ThreadsPerCore int

	Offline      []int       // logical CPUs present but offline
	CoreTemps    map[int]int // "socket*100+core" -> °C override
	PkgTemps     map[int]int // socket -> °C
	ThrottleCPUs map[int]int // cpu -> core_throttle_count
	MCEExc       map[int]int // cpu -> MCE exceptions in /proc/interrupts
	Kmsg         []kmsgLine
	Journal      []journalLine     // kernel messages of earlier boots
	Pstore       map[string]string // crash records: path -> content
	Sim          map[string]any

	// memory
	DIMMOverride  map[string]dimmSpec // locator -> spec (populated slots are *1 slots)
	EDACErrors    map[string][2]int   // locator -> {CE, UE}
	MappedOut     []string            // populated DIMMs the BIOS disabled
	HWCorruptedKB int

	// BMC
	IPMITool   string         // "ipmiutil", "ipmitool" or "" (no BMC tool installed)
	NoCoretemp bool           // coretemp not loaded: CPU temps only from the BMC
	DIMMTemps  map[string]int // locator -> °C override for BMC DIMM sensors
	SEL        []selRec

	Faults faults // storage, network, GPU faults
}

type selRec struct {
	At       time.Time
	Type     string // IPMI sensor type
	Sensor   string // name or "#0x87"
	Event    string
	Asserted bool
	Sev      string // ipmiutil severity column: INF, MIN, MAJ, CRT
}

type dimmSpec struct {
	Manufacturer, Part string
	Speed, Configured  int
}

type journalLine struct {
	At   time.Time
	Boot string
	Msg  string
}

type kmsgLine struct {
	AfterBoot time.Duration
	Pri       int
	Msg       string
}

var now = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
var boot = now.Add(-9 * 24 * time.Hour)

func scenarios() []scenario {
	healthy := scenario{
		Name:        "server-healthy",
		Description: "synthetic: Supermicro SYS-120U-TNR, 2x Xeon Silver 4314 (64 threads), root, all healthy",
		Hostname:    "srv-healthy-01",
		Sockets:     2, CoresPerSocket: 16, ThreadsPerCore: 2,
		Kmsg: []kmsgLine{
			{5 * time.Second, 6, "Linux version 6.8.0-49-generic (buildd@lcy02-amd64-028) #49-Ubuntu SMP"},
			{9 * time.Second, 6, "EDAC MC0: Giving out device to module i10nm_edac controller Intel_10nm Socket#0 IMC#0: DEV 0000:7e:0c.0 (INTERRUPT)"},
			{11 * time.Second, 6, "ice 0000:4b:00.0 ens1f0: NIC Link is up 25 Gbps Full Duplex, Requested FEC: RS-FEC, Negotiated FEC: RS-FEC, Autoneg Advertised: On, Autoneg Negotiated: False, Flow Control: None"},
		},
		IPMITool: "ipmiutil",
		SEL: []selRec{
			{boot.Add(-time.Hour), "Event Logging Disabled", "#07", "Log area reset/cleared", true, "INF"},
			{boot.Add(time.Minute), "System Event", "#ff", "Timestamp Clock Sync", true, "INF"},
		},
	}
	bmcOnly := healthy
	bmcOnly.Name = "server-bmc-only"
	bmcOnly.Hostname = "srv-bmconly-01"
	bmcOnly.Description = "synthetic: like server-healthy but coretemp is not loaded and only ipmitool is installed; CPU and DIMM temperatures come from the BMC"
	bmcOnly.NoCoretemp = true
	bmcOnly.IPMITool = "ipmitool"
	degraded := scenario{
		Name:        "server-degraded",
		Description: "synthetic: same host as server-healthy with faults: cpu7 offline, hot socket 1, throttling, corrected MCEs on cpu12, simulated core-test failure on cpu21",
		Hostname:    "srv-degraded-01",
		Sockets:     2, CoresPerSocket: 16, ThreadsPerCore: 2,
		Offline:      []int{7},
		PkgTemps:     map[int]int{1: 93},
		CoreTemps:    map[int]int{100 + 4: 96, 100 + 5: 91},
		ThrottleCPUs: map[int]int{20: 14, 21: 9, 52: 14},
		MCEExc:       map[int]int{12: 0},
		Kmsg: []kmsgLine{
			{5 * time.Second, 6, "Linux version 6.8.0-49-generic (buildd@lcy02-amd64-028) #49-Ubuntu SMP"},
			{3 * 24 * time.Hour, 4, "mce: [Hardware Error]: Machine check events logged"},
			{3*24*time.Hour + time.Millisecond, 4, "mce: [Hardware Error]: CPU 12: Machine Check: 0 Bank 13: cc00008000010090"},
			{3*24*time.Hour + 2*time.Millisecond, 4, "mce: [Hardware Error]: TSC 0 ADDR 3a2f40a18 MISC 9020000086 "},
			{5 * 24 * time.Hour, 4, "mce: [Hardware Error]: CPU 12: Machine Check: 0 Bank 13: cc00008000010090"},
			{6 * 24 * time.Hour, 4, "mce: CPU20: Core temperature above threshold, cpu clock throttled (total events = 14)"},
			{6*24*time.Hour + time.Second, 4, "mce: CPU52: Core temperature above threshold, cpu clock throttled (total events = 14)"},
			{8 * 24 * time.Hour, 6, "smpboot: CPU 7 is now offline"},
			{4 * 24 * time.Hour, 4, "EDAC MC5: 14 CE memory read error on CPU_SrcID#1_MC#1_Chan#0_DIMM#0 (channel:0 slot:0 page:0x3a2f40 offset:0xa18 grain:32 syndrome:0x0 - err_code:0x0000:0x009f)"},
			{7 * 24 * time.Hour, 0, "EDAC MC4: 1 UE memory read error on CPU_SrcID#1_MC#0_Chan#1_DIMM#0 (channel:1 slot:0 page:0x1f0a3 offset:0x0 grain:32)"},
			{7*24*time.Hour + time.Millisecond, 3, "Memory failure: 0x1f0a3: recovery action for dirty LRU page: Recovered"},
			{8*24*time.Hour + 3*time.Hour, 3, "ata4.00: exception Emask 0x0 SAct 0x40000 SErr 0x0 action 0x0"},
			{8*24*time.Hour + 3*time.Hour + time.Millisecond, 3, "I/O error, dev sdd, sector 1953525160 op 0x0:(READ) flags 0x80700 phys_seg 1 prio class 0"},
			{8*24*time.Hour + 3*time.Hour + 2*time.Millisecond, 2, "md/raid:md1: Disk failure on sdd1, disabling device."},
			{8*24*time.Hour + 4*time.Hour, 3, "XFS (md1): metadata I/O error in \"xfs_buf_ioend+0x1a0/0x380\" at daddr 0x2 len 1 error 5"},
			{8*24*time.Hour + 4*time.Hour + 30*time.Minute, 3, " connection1:0: detected conn error (1020)"},
			{8*24*time.Hour + 4*time.Hour + 31*time.Minute, 3, "I/O error, dev sdf, sector 2048 op 0x1:(WRITE) flags 0x800 phys_seg 1 prio class 0"},
			{8*24*time.Hour + 4*time.Hour + 32*time.Minute, 3, "I/O error, dev sdf, sector 4096 op 0x1:(WRITE) flags 0x800 phys_seg 1 prio class 0"},
			{8*24*time.Hour + 5*time.Hour, 6, "ice 0000:4b:00.1 ens1f1: NIC Link is Down"},
			{8*24*time.Hour + 6*time.Hour, 3, "NVRM: Xid (PCI:0000:ca:00): 48, pid=48211, name=python3, An uncorrectable double bit error (DBE) has been detected on GPU in the framebuffer at partition 6, subpartition 0."},
		},
		// earlier boots: corrected errors in cpu7's data cache (and one in
		// instruction fetch on its other thread cpu39), one memory
		// controller error reported by cpu12; then a fatal machine check on
		// cpu7 crashed that boot (pstore).
		Journal: []journalLine{
			{boot.Add(-20 * 24 * time.Hour), "6b1e0c8a4f3d4e2b9a7c5d3e1f0a2b4c", "mce: [Hardware Error]: Machine check events logged"},
			{boot.Add(-20*24*time.Hour + time.Millisecond), "6b1e0c8a4f3d4e2b9a7c5d3e1f0a2b4c", "mce: [Hardware Error]: CPU 7: Machine Check: 0 Bank 1: 9c00000000010151"},
			{boot.Add(-14 * 24 * time.Hour), "6b1e0c8a4f3d4e2b9a7c5d3e1f0a2b4c", "mce: [Hardware Error]: CPU 12: Machine Check: 0 Bank 13: cc00008000010090"},
			{boot.Add(-3 * 24 * time.Hour), "d0f4a6b8c1e3459a8b7c6d5e4f3a2b1c", "mce: [Hardware Error]: CPU 7: Machine Check: 0 Bank 1: 9c00000000010151"},
			{boot.Add(-2 * 24 * time.Hour), "d0f4a6b8c1e3459a8b7c6d5e4f3a2b1c", "mce: [Hardware Error]: CPU 39: Machine Check: 0 Bank 0: 9800000000000150"},
			{boot.Add(-1 * 24 * time.Hour), "d0f4a6b8c1e3459a8b7c6d5e4f3a2b1c", "mce: [Hardware Error]: CPU 7: Machine Check: 0 Bank 1: 9c00000000010151"},
		},
		Pstore: map[string]string{
			"/var/lib/systemd/pstore/1794041280/dmesg-erst-7340032000001": "Panic#1 Part1\n" +
				"<4>[690112.402113] mce: [Hardware Error]: CPU 7: Machine Check Exception: 5 Bank 1: bd80000000100134\n" +
				"<4>[690112.402118] mce: [Hardware Error]: RIP !INEXACT! 10:<ffffffff8e7b1c4e> {native_safe_halt+0xe/0x20}\n" +
				"<4>[690112.402121] mce: [Hardware Error]: TSC 2a1f0c4d8e1 ADDR 3a2f40000 MISC 86\n" +
				"<4>[690112.402124] mce: [Hardware Error]: PROCESSOR 0:606a6 TIME 1794041280 SOCKET 0 APIC e microcode d0003e7\n" +
				"<0>[690112.402130] Kernel panic - not syncing: Fatal machine check\n" +
				"<0>[690112.402302] Kernel Offset: 0xd400000 from 0xffffffff81000000\n",
		},
		Sim:           map[string]any{"core_test_fail": []int{21}},
		DIMMOverride:  map[string]dimmSpec{"P1-DIMMD1": {"Micron Technology", "MTA36ASF4G72PZ-3G2R1", 3200, 2933}},
		EDACErrors:    map[string][2]int{"P2-DIMMC1": {14, 0}, "P2-DIMMB1": {0, 1}},
		MappedOut:     []string{"P2-DIMMH1"},
		HWCorruptedKB: 4,
		IPMITool:      "ipmitool",
		DIMMTemps:     map[string]int{"P2-DIMMC1": 82},
		Faults: faults{
			MDFailed: "sdd1", Realloc: map[string]int{"sdc": 24}, Pending: map[string]int{"sdc": 8},
			NVMeWear: map[string]int{"nvme1n1": 93}, NVMeMedia: map[string]int{"nvme1n1": 3},
			DataFullPct: 96, DataReadOnly: true, IOErrCnt: map[string]int{"sdd": 0x1f}, ISCSI: true,
			MegaRAID: true, MegaDegraded: true,
			BondMemberDown: "ens1f1", CRCErrors: map[string]int{"ens1f0": 1532}, CarrierChanges: map[string]int{"ens1f0": 35},
			RxPowerDBm: map[string]float64{"ens1f0": -13.9}, MgmtSpeed: 100, MgmtHalf: true,
			GPUWidth8: true, GPUUE: 2, GPURemapPend: true, GPUTemp: 88,
		},
		SEL: []selRec{
			{boot.Add(-time.Hour), "Event Logging Disabled", "#0x07", "Log area reset/cleared", true, "INF"},
			{boot.Add(4 * 24 * time.Hour), "Memory", "#0x87", "Correctable ECC @DIMMC1(CPU2)", true, "MIN"},
			{boot.Add(7 * 24 * time.Hour), "Memory", "#0x87", "Uncorrectable ECC @DIMMB1(CPU2)", true, "CRT"},
			{boot.Add(8 * 24 * time.Hour), "Power Supply", "PS2 Status", "Power Supply AC lost", true, "MAJ"},
			{boot.Add(8*24*time.Hour + time.Second), "Power Unit", "#0xc9", "Redundancy Lost", true, "MAJ"},
			{boot.Add(8*24*time.Hour + time.Hour), "Temperature", "CPU2 Temp", "Upper Non-critical going high", true, "MIN"},
			{boot.Add(8*24*time.Hour + 2*time.Hour), "Temperature", "CPU2 Temp", "Upper Non-critical going high", false, "INF"},
		},
	}
	return []scenario{healthy, degraded, bmcOnly}
}

func main() {
	out := flag.String("out", "testdata/fixtures", "output directory")
	flag.Parse()
	for _, s := range scenarios() {
		dir := filepath.Join(*out, s.Name)
		if err := os.RemoveAll(dir); err != nil {
			log.Fatal(err)
		}
		g := &gen{dir: dir, s: s}
		g.build()
		if g.err != nil {
			log.Fatalf("%s: %v", s.Name, g.err)
		}
		fmt.Println("wrote", dir)
	}
}

type gen struct {
	dir  string
	s    scenario
	err  error
	cmds []map[string]any
}

func (g *gen) write(p, content string) {
	if g.err != nil {
		return
	}
	full := filepath.Join(g.dir, "root", p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		g.err = err
		return
	}
	g.err = os.WriteFile(full, []byte(content), 0o644)
}

func (g *gen) symlink(p, target string) {
	if g.err != nil {
		return
	}
	full := filepath.Join(g.dir, "root", p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		g.err = err
		return
	}
	g.err = os.Symlink(target, full)
}

func (g *gen) cmd(argv []string, stdout string, exit int) {
	if g.err != nil {
		return
	}
	name := strings.NewReplacer("/", "_", " ", "_", "|", "_").Replace(strings.Join(argv, "_")) + ".txt"
	rel := filepath.Join("cmd", name)
	if err := os.MkdirAll(filepath.Join(g.dir, "cmd"), 0o755); err != nil {
		g.err = err
		return
	}
	g.err = os.WriteFile(filepath.Join(g.dir, rel), []byte(stdout), 0o644)
	g.cmds = append(g.cmds, map[string]any{"argv": argv, "stdout": rel, "exit": exit})
}

// Intel enumeration: first all physical cores of all sockets, then the
// hyper-thread siblings in the same order.
func (g *gen) topo(cpu int) (socket, core int) {
	n := g.s.Sockets * g.s.CoresPerSocket
	c := cpu % n
	return c / g.s.CoresPerSocket, c % g.s.CoresPerSocket
}

func (g *gen) total() int { return g.s.Sockets * g.s.CoresPerSocket * g.s.ThreadsPerCore }

func list(ids []int) string {
	var b strings.Builder
	for i := 0; i < len(ids); {
		j := i
		for j+1 < len(ids) && ids[j+1] == ids[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		if i == j {
			fmt.Fprintf(&b, "%d", ids[i])
		} else {
			fmt.Fprintf(&b, "%d-%d", ids[i], ids[j])
		}
		i = j + 1
	}
	return b.String()
}

func (g *gen) online() []int {
	var out []int
	for i := 0; i < g.total(); i++ {
		if !slices.Contains(g.s.Offline, i) {
			out = append(out, i)
		}
	}
	return out
}

func (g *gen) build() {
	g.cpu()
	g.memory()
	g.ipmi()
	g.devices()
	g.dmi()
	g.kmsg()
	g.history()
	g.manifest()
}

func (g *gen) cpu() {
	n := g.total()
	all := make([]int, n)
	for i := range all {
		all[i] = i
	}
	on := g.online()
	base := "/sys/devices/system/cpu"
	g.write(base+"/present", list(all)+"\n")
	g.write(base+"/possible", list(all)+"\n")
	g.write(base+"/online", list(on)+"\n")
	g.write(base+"/offline", list(g.s.Offline)+"\n")
	g.write(base+"/kernel_max", "8191\n")
	g.write(base+"/smt/control", "on\n")
	g.write(base+"/smt/active", "1\n")
	g.write("/proc/version", "Linux version 6.8.0-49-generic (buildd@lcy02-amd64-028) (x86_64-linux-gnu-gcc-13 (Ubuntu 13.2.0-23ubuntu4) 13.2.0, GNU ld (GNU Binutils for Ubuntu) 2.42) #49-Ubuntu SMP PREEMPT_DYNAMIC Mon Nov  4 02:06:24 UTC 2024\n")
	g.write("/proc/cmdline", "BOOT_IMAGE=/vmlinuz-6.8.0-49-generic root=/dev/md0 ro quiet\n")

	sib := map[[2]int][]int{}
	pkgCPUs := map[int][]int{}
	for cpu := 0; cpu < n; cpu++ {
		socket, core := g.topo(cpu)
		sib[[2]int{socket, core}] = append(sib[[2]int{socket, core}], cpu)
		pkgCPUs[socket] = append(pkgCPUs[socket], cpu)
	}
	for _, v := range []struct{ name, state string }{
		{"meltdown", "Not affected"}, {"spectre_v1", "Mitigation: usercopy/swapgs barriers and __user pointer sanitization"},
		{"spectre_v2", "Mitigation: Enhanced / Automatic IBRS; IBPB: conditional; RSB filling; PBRSB-eIBRS: SW sequence; BHI: SW loop, KVM: SW loop"},
		{"mds", "Not affected"}, {"l1tf", "Not affected"}, {"retbleed", "Not affected"},
		{"gather_data_sampling", "Vulnerable: No microcode"}, {"spec_store_bypass", "Mitigation: Speculative Store Bypass disabled via prctl"},
		{"mmio_stale_data", "Mitigation: Clear CPU buffers; SMT vulnerable"}, {"srbds", "Not affected"},
	} {
		g.write(base+"/vulnerabilities/"+v.name, v.state+"\n")
	}
	g.write(base+"/isolated", "\n")
	g.write(base+"/cpufreq/boost", "1\n")

	nodeCPUs := map[int][]int{}
	for cpu := 0; cpu < n; cpu++ {
		socket, core := g.topo(cpu)
		d := fmt.Sprintf("%s/cpu%d", base, cpu)
		isOn := !slices.Contains(g.s.Offline, cpu)
		if cpu != 0 {
			g.write(d+"/online", map[bool]string{true: "1\n", false: "0\n"}[isOn])
		}
		g.write(d+"/microcode/version", "0xd0003e7\n")
		nodeCPUs[socket] = append(nodeCPUs[socket], cpu)
		if !isOn {
			continue // topology/, cpufreq/, thermal_throttle/ vanish when offline
		}
		g.symlink(d+fmt.Sprintf("/node%d", socket), fmt.Sprintf("../../node/node%d", socket))
		g.write(d+"/topology/core_id", fmt.Sprintf("%d\n", core))
		g.write(d+"/topology/physical_package_id", fmt.Sprintf("%d\n", socket))
		g.write(d+"/topology/die_id", "0\n")
		g.write(d+"/topology/ppin", fmt.Sprintf("0x%016x\n", 0x2f1a9c4e5b6d7000+uint64(socket)))
		freq := 2400000 + (cpu%7)*100000
		if _, hot := g.s.PkgTemps[socket]; hot {
			freq = 1800000
		}
		g.write(d+"/cpufreq/scaling_cur_freq", fmt.Sprintf("%d\n", freq))
		g.write(d+"/cpufreq/cpuinfo_max_freq", "3400000\n")
		g.write(d+"/cpufreq/cpuinfo_min_freq", "800000\n")
		g.write(d+"/cpufreq/base_frequency", "2400000\n")
		g.write(d+"/cpufreq/scaling_governor", "performance\n")
		g.write(d+"/cpufreq/scaling_driver", "intel_pstate\n")
		g.write(d+"/thermal_throttle/core_throttle_count", fmt.Sprintf("%d\n", g.s.ThrottleCPUs[cpu]))
		g.write(d+"/thermal_throttle/package_throttle_count", "0\n")
		var onSib []int // the kernel drops offline threads from the lists
		for _, c := range sib[[2]int{socket, core}] {
			if !slices.Contains(g.s.Offline, c) {
				onSib = append(onSib, c)
			}
		}
		siblings := list(onSib)
		g.write(d+"/topology/core_cpus_list", siblings+"\n")
		g.write(d+"/topology/thread_siblings_list", siblings+"\n")
		for i, c := range []struct {
			level      int
			typ, size  string
			ways       int
			sharedList string
		}{{1, "Data", "48K", 12, siblings}, {1, "Instruction", "32K", 8, siblings}, {2, "Unified", "1280K", 20, siblings},
			{3, "Unified", "24576K", 12, list(pkgCPUs[socket])}} {
			cd := fmt.Sprintf("%s/cache/index%d", d, i)
			g.write(cd+"/level", fmt.Sprintf("%d\n", c.level))
			g.write(cd+"/type", c.typ+"\n")
			g.write(cd+"/size", c.size+"\n")
			g.write(cd+"/ways_of_associativity", fmt.Sprintf("%d\n", c.ways))
			g.write(cd+"/coherency_line_size", "64\n")
			g.write(cd+"/shared_cpu_list", c.sharedList+"\n")
		}
		for i, st := range []struct {
			name, desc string
			lat        int
			share      uint64
		}{{"POLL", "CPUIDLE CORE POLL IDLE", 0, 1}, {"C1", "MWAIT 0x00", 2, 9}, {"C1E", "MWAIT 0x01", 10, 30}, {"C6", "MWAIT 0x20", 170, 60}} {
			sd := fmt.Sprintf("%s/cpuidle/state%d", d, i)
			g.write(sd+"/name", st.name+"\n")
			g.write(sd+"/desc", st.desc+"\n")
			g.write(sd+"/latency", fmt.Sprintf("%d\n", st.lat))
			g.write(sd+"/usage", fmt.Sprintf("%d\n", st.share*100000+uint64(cpu)))
			g.write(sd+"/time", fmt.Sprintf("%d\n", st.share*7_000_000_000/100))
			g.write(sd+"/disable", "0\n")
		}
	}
	for node, cpus := range nodeCPUs {
		var on []int
		for _, c := range cpus {
			if !slices.Contains(g.s.Offline, c) {
				on = append(on, c)
			}
		}
		slices.Sort(on)
		g.write(fmt.Sprintf("/sys/devices/system/node/node%d/cpulist", node), list(on)+"\n")
	}
	g.write("/sys/devices/system/node/online", fmt.Sprintf("0-%d\n", g.s.Sockets-1))

	// coretemp: one platform device per package
	for s := 0; s < g.s.Sockets && !g.s.NoCoretemp; s++ {
		hw := fmt.Sprintf("hwmon%d", s+1)
		dev := fmt.Sprintf("/sys/devices/platform/coretemp.%d", s)
		g.symlink("/sys/class/hwmon/"+hw, "../../devices/platform/"+fmt.Sprintf("coretemp.%d", s)+"/hwmon/"+hw)
		hd := dev + "/hwmon/" + hw
		g.write(hd+"/name", "coretemp\n")
		g.symlink(hd+"/device", "../../../coretemp."+fmt.Sprint(s))
		pkg := 52 + s*3
		if v, ok := g.s.PkgTemps[s]; ok {
			pkg = v
		}
		g.write(hd+"/temp1_label", fmt.Sprintf("Package id %d\n", s))
		g.write(hd+"/temp1_input", fmt.Sprintf("%d000\n", pkg))
		g.write(hd+"/temp1_max", "90000\n")
		g.write(hd+"/temp1_crit", "100000\n")
		g.write(hd+"/temp1_crit_alarm", "0\n")
		for c := 0; c < g.s.CoresPerSocket; c++ {
			idx := c + 2
			t := 44 + (c*7)%12 + s*2
			if v, ok := g.s.CoreTemps[s*100+c]; ok {
				t = v
			}
			g.write(fmt.Sprintf("%s/temp%d_label", hd, idx), fmt.Sprintf("Core %d\n", c))
			g.write(fmt.Sprintf("%s/temp%d_input", hd, idx), fmt.Sprintf("%d000\n", t))
			g.write(fmt.Sprintf("%s/temp%d_max", hd, idx), "90000\n")
			g.write(fmt.Sprintf("%s/temp%d_crit", hd, idx), "100000\n")
			g.write(fmt.Sprintf("%s/temp%d_crit_alarm", hd, idx), "0\n")
		}
	}
	// a non-CPU chip to make sure it is ignored
	g.symlink("/sys/class/hwmon/hwmon0", "../../devices/virtual/thermal/thermal_zone0/hwmon0")
	g.write("/sys/devices/virtual/thermal/thermal_zone0/hwmon0/name", "acpitz\n")
	g.write("/sys/devices/virtual/thermal/thermal_zone0/hwmon0/temp1_input", "27800\n")

	// /proc/cpuinfo
	var ci strings.Builder
	for _, cpu := range on {
		socket, core := g.topo(cpu)
		fmt.Fprintf(&ci, "processor\t: %d\nvendor_id\t: GenuineIntel\ncpu family\t: 6\nmodel\t\t: 106\n"+
			"model name\t: Intel(R) Xeon(R) Silver 4314 CPU @ 2.40GHz\nstepping\t: 6\nmicrocode\t: 0xd0003e7\n"+
			"cpu MHz\t\t: %d.000\ncache size\t: 24576 KB\nphysical id\t: %d\nsiblings\t: 32\ncore id\t\t: %d\ncpu cores\t: 16\n"+
			"flags\t\t: fpu vme de pse tsc msr pae mce cx8 apic sep mtrr pge mca cmov pat pse36 clflush dts acpi mmx fxsr sse sse2 ss ht tm pbe syscall nx pdpe1gb rdtscp lm constant_tsc art arch_perfmon pebs bts rep_good nopl xtopology nonstop_tsc cpuid aperfmperf pni pclmulqdq dtes64 monitor ds_cpl vmx smx est tm2 ssse3 sdbg fma cx16 xtpr pdcm pcid dca sse4_1 sse4_2 x2apic movbe popcnt tsc_deadline_timer aes xsave avx f16c rdrand lahf_lm abm 3dnowprefetch cpuid_fault epb cat_l3 invpcid_single intel_ppin ssbd mba ibrs ibpb stibp ibrs_enhanced tpr_shadow flexpriority ept vpid ept_ad fsgsbase tsc_adjust bmi1 avx2 smep bmi2 erms invpcid cqm rdt_a avx512f avx512dq rdseed adx smap avx512ifma clflushopt clwb intel_pt avx512cd sha_ni avx512bw avx512vl xsaveopt xsavec xgetbv1 xsaves cqm_llc cqm_occup_llc cqm_mbm_total cqm_mbm_local split_lock_detect wbnoinvd dtherm ida arat pln pts vnmi avx512vbmi umip pku ospke avx512_vbmi2 gfni vaes vpclmulqdq avx512_vnni avx512_bitalg tme avx512_vpopcntdq la57 rdpid fsrm md_clear pconfig flush_l1d arch_capabilities\n\n",
			cpu, 2400+(cpu%7)*100, socket, core)
	}
	g.write("/proc/cpuinfo", ci.String())

	// /proc/stat: online CPUs only
	var st strings.Builder
	var tu, ti uint64
	lines := []string{}
	for _, cpu := range on {
		user := uint64(400000 + cpu*1337%90000)
		idle := uint64(7000000 - cpu*977%300000)
		tu += user
		ti += idle
		lines = append(lines, fmt.Sprintf("cpu%d %d 120 %d %d 900 0 1500 0 0 0", cpu, user, user/4, idle))
	}
	fmt.Fprintf(&st, "cpu  %d %d %d %d %d 0 %d 0 0 0\n", tu, 120*len(on), tu/4, ti, 900*len(on), 1500*len(on))
	st.WriteString(strings.Join(lines, "\n") + "\n")
	fmt.Fprintf(&st, "intr 123456789 0\nctxt 987654321\nbtime %d\nprocesses 1234567\nprocs_running 3\nprocs_blocked 0\n", boot.Unix())
	g.write("/proc/stat", st.String())

	// /proc/interrupts: only online CPUs are columns
	var ir strings.Builder
	ir.WriteString("    ")
	for _, cpu := range on {
		fmt.Fprintf(&ir, " %10s", fmt.Sprintf("CPU%d", cpu))
	}
	ir.WriteString("\n")
	row := func(name string, val func(cpu int) int, desc string) {
		fmt.Fprintf(&ir, "%4s:", name)
		for _, cpu := range on {
			fmt.Fprintf(&ir, " %10d", val(cpu))
		}
		fmt.Fprintf(&ir, "   %s\n", desc)
	}
	row("0", func(int) int { return 0 }, " IO-APIC   2-edge      timer")
	row("NMI", func(int) int { return 12 }, "  Non-maskable interrupts")
	row("TRM", func(cpu int) int { return g.s.ThrottleCPUs[cpu] }, "  Thermal event interrupts")
	row("THR", func(int) int { return 0 }, "  Threshold APIC interrupts")
	row("MCE", func(cpu int) int { return g.s.MCEExc[cpu] }, "  Machine check exceptions")
	row("MCP", func(int) int { return 2592 }, "  Machine check polls")
	g.write("/proc/interrupts", ir.String())
	g.write("/proc/uptime", fmt.Sprintf("%.2f %.2f\n", now.Sub(boot).Seconds(), now.Sub(boot).Seconds()*50))
}

// history: the journal of earlier boots and crash records.
func (g *gen) history() {
	g.write("/proc/sys/kernel/random/boot_id", "9e4c2a7f-1b3d-4c5e-8f6a-0b1c2d3e4f5a\n")
	var b strings.Builder
	for _, l := range g.s.Journal {
		j, _ := json.Marshal(map[string]string{"__REALTIME_TIMESTAMP": strconv.FormatInt(l.At.UnixMicro(), 10), "_BOOT_ID": l.Boot, "MESSAGE": l.Msg})
		b.Write(j)
		b.WriteByte('\n')
	}
	g.cmd([]string{"journalctl", "_TRANSPORT=kernel", "-o", "json", "--no-pager", "-q", "--output-fields=MESSAGE", "--since=-365d", "-g", "Hardware Error|Kernel panic"}, b.String(), 0)
	paths := make([]string, 0, len(g.s.Pstore))
	for p := range g.s.Pstore {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		g.write(p, g.s.Pstore[p])
	}
}

func (g *gen) kmsg() {
	var b strings.Builder
	for i, l := range g.s.Kmsg {
		fmt.Fprintf(&b, "%d,%d,%d,-;%s\n", l.Pri, 1000+i, l.AfterBoot.Microseconds(), l.Msg)
	}
	g.write("/dev/kmsg", b.String())
}

func (g *gen) dmi() {
	var b strings.Builder
	b.WriteString("# dmidecode 3.5\nGetting SMBIOS data from sysfs.\nSMBIOS 3.3.0 present.\n\n")
	b.WriteString("Handle 0x0000, DMI type 0, 26 bytes\nBIOS Information\n\tVendor: American Megatrends International, LLC.\n\tVersion: 1.4b\n\tRelease Date: 06/14/2024\n\tROM Size: 64 MB\n\tBIOS Revision: 5.22\n\n")
	b.WriteString("Handle 0x0001, DMI type 1, 27 bytes\nSystem Information\n\tManufacturer: Supermicro\n\tProduct Name: SYS-120U-TNR\n\tVersion: 0123456789\n\tSerial Number: S412345X3A01234\n\tUUID: 00000000-0000-0000-0000-3cecef123456\n\tWake-up Type: Power Switch\n\tSKU Number: To be filled by O.E.M.\n\tFamily: SMC X12\n\n")
	b.WriteString("Handle 0x0002, DMI type 2, 15 bytes\nBase Board Information\n\tManufacturer: Supermicro\n\tProduct Name: X12DPU-6\n\tVersion: 1.02\n\tSerial Number: OM21AS012345\n\tAsset Tag: Base Board Asset Tag\n\tFeatures:\n\t\tBoard is a hosting board\n\t\tBoard is replaceable\n\tLocation In Chassis: Part Component\n\tChassis Handle: 0x0003\n\tType: Motherboard\n\tContained Object Handles: 0\n\n")
	b.WriteString("Handle 0x0003, DMI type 3, 22 bytes\nChassis Information\n\tManufacturer: Supermicro\n\tType: Rack Mount Chassis\n\tLock: Not Present\n\tVersion: 0123456789\n\tSerial Number: C1160LL12AB0123\n\tBoot-up State: Safe\n\tPower Supply State: Safe\n\tThermal State: Safe\n\tSecurity Status: None\n\tHeight: 1 U\n\n")
	for s := 0; s < g.s.Sockets; s++ {
		fmt.Fprintf(&b, "Handle 0x%04X, DMI type 4, 48 bytes\nProcessor Information\n\tSocket Designation: CPU%d\n\tType: Central Processor\n\tFamily: Xeon\n\tManufacturer: Intel(R) Corporation\n\tID: A6 06 06 00 FF FB EB BF\n\tSignature: Type 0, Family 6, Model 106, Stepping 6\n\tFlags:\n\t\tFPU (Floating-point unit on-chip)\n\t\tVME (Virtual mode extension)\n\t\tDE (Debugging extension)\n\tVersion: Intel(R) Xeon(R) Silver 4314 CPU @ 2.40GHz\n\tVoltage: 1.6 V\n\tExternal Clock: 100 MHz\n\tMax Speed: 4000 MHz\n\tCurrent Speed: 2400 MHz\n\tStatus: Populated, Enabled\n\tUpgrade: Other\n\tL1 Cache Handle: 0x%04X\n\tL2 Cache Handle: 0x%04X\n\tL3 Cache Handle: 0x%04X\n\tSerial Number: Not Specified\n\tAsset Tag: CPU%d\n\tPart Number: Not Specified\n\tCore Count: %d\n\tCore Enabled: %d\n\tThread Count: %d\n\tCharacteristics:\n\t\t64-bit capable\n\t\tMulti-Core\n\t\tHardware Thread\n\t\tExecute Protection\n\t\tEnhanced Virtualization\n\t\tPower/Performance Control\n\n",
			0x50+s, s+1, 0x60+s*3, 0x61+s*3, 0x62+s*3, s+1, g.s.CoresPerSocket, g.s.CoresPerSocket, g.s.CoresPerSocket*g.s.ThreadsPerCore)
	}
	b.WriteString(g.dmiMemory())
	for i := 1; i <= 2; i++ {
		fmt.Fprintf(&b, "Handle 0x%04X, DMI type 39, 22 bytes\nSystem Power Supply\n\tPower Unit Group: 1\n\tLocation: PSU%d\n\tName: PWS-1K22A-1R\n\tManufacturer: SUPERMICRO\n\tSerial Number: P1K22CL21%06d\n\tAsset Tag: N/A\n\tModel Part Number: PWS-1K22A-1R\n\tRevision: 1.4\n\tMax Power Capacity: 1200 W\n\tStatus: Present, OK\n\tType: Switching\n\tInput Voltage Range Switching: Auto-switch\n\tPlugged: Yes\n\tHot Replaceable: Yes\n\n", 0x2000+i, i, 123450+i)
	}
	b.WriteString("Handle 0xFFFF, DMI type 127, 4 bytes\nEnd Of Table\n\n")
	g.cmd([]string{"dmidecode"}, b.String(), 0)

	id := "/sys/devices/virtual/dmi/id"
	g.symlink("/sys/class/dmi", "../devices/virtual/dmi")
	for k, v := range map[string]string{"sys_vendor": "Supermicro", "product_name": "SYS-120U-TNR", "board_vendor": "Supermicro",
		"board_name": "X12DPU-6", "bios_vendor": "American Megatrends International, LLC.", "bios_version": "1.4b",
		"bios_date": "06/14/2024", "product_serial": "S412345X3A01234", "chassis_type": "23"} {
		g.write(id+"/"+k, v+"\n")
	}
	g.write("/sys/firmware/dmi/tables/DMI", "")
}

func (g *gen) manifest() {
	m := map[string]any{
		"description":    g.s.Description,
		"hostname":       g.s.Hostname,
		"arch":           "amd64",
		"virtualization": "none",
		"now":            now.Format(time.RFC3339),
		"privileges":     map[string]bool{"root": true, "dmi": true, "kmsg": true, "ipmi": true},
		"tools":          g.tools(),
		"commands":       g.cmds,
		"sim":            g.s.Sim,
	}
	if m["sim"] == nil {
		m["sim"] = map[string]any{}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		g.err = err
		return
	}
	if g.err == nil {
		g.err = os.WriteFile(filepath.Join(g.dir, "manifest.json"), append(b, '\n'), 0o644)
	}
}

const dimmSize = 32 << 30

// slots returns all DIMM locators (P1-DIMMA1 ... P2-DIMMH2) and whether the
// slot is populated (first slot of every channel).
func (g *gen) slots() (locs []string, populated map[string]bool) {
	populated = map[string]bool{}
	for s := 1; s <= g.s.Sockets; s++ {
		for ch := 'A'; ch <= 'H'; ch++ {
			for slot := 1; slot <= 2; slot++ {
				l := fmt.Sprintf("P%d-DIMM%c%d", s, ch, slot)
				locs = append(locs, l)
				populated[l] = slot == 1
			}
		}
	}
	return locs, populated
}

func (g *gen) dmiMemory() string {
	var b strings.Builder
	locs, pop := g.slots()
	for s := 0; s < g.s.Sockets; s++ {
		fmt.Fprintf(&b, "Handle 0x%04X, DMI type 16, 23 bytes\nPhysical Memory Array\n\tLocation: System Board Or Motherboard\n\tUse: System Memory\n\tError Correction Type: Multi-bit ECC\n\tMaximum Capacity: 4 TB\n\tError Information Handle: Not Provided\n\tNumber Of Devices: 16\n\n", 0x1000+s)
	}
	for i, l := range locs {
		socket := i / 16
		ch := l[len(l)-2]
		h := 0x1100 + i
		if !pop[l] {
			fmt.Fprintf(&b, "Handle 0x%04X, DMI type 17, 92 bytes\nMemory Device\n\tArray Handle: 0x%04X\n\tError Information Handle: Not Provided\n\tTotal Width: Unknown\n\tData Width: Unknown\n\tSize: No Module Installed\n\tForm Factor: DIMM\n\tSet: None\n\tLocator: %s\n\tBank Locator: P%d_Node%d_Channel%c_Dimm1\n\tType: Unknown\n\tType Detail: Unknown\n\tSpeed: Unknown\n\tManufacturer: NO DIMM\n\tSerial Number: NO DIMM\n\tAsset Tag: NO DIMM\n\tPart Number: NO DIMM\n\tRank: Unknown\n\tConfigured Memory Speed: Unknown\n\n", h, 0x1000+socket, l, socket, socket, ch)
			continue
		}
		spec := dimmSpec{"Samsung", "M393A4K40EB3-CWE    ", 3200, 3200}
		if o, ok := g.s.DIMMOverride[l]; ok {
			spec = o
		}
		fmt.Fprintf(&b, "Handle 0x%04X, DMI type 17, 92 bytes\nMemory Device\n\tArray Handle: 0x%04X\n\tError Information Handle: Not Provided\n\tTotal Width: 72 bits\n\tData Width: 64 bits\n\tSize: 32 GB\n\tForm Factor: DIMM\n\tSet: None\n\tLocator: %s\n\tBank Locator: P%d_Node%d_Channel%c_Dimm0\n\tType: DDR4\n\tType Detail: Synchronous Registered (Buffered)\n\tSpeed: %d MT/s\n\tManufacturer: %s\n\tSerial Number: %08X\n\tAsset Tag: %s_AssetTag (date:21/32)\n\tPart Number: %s\n\tRank: 2\n\tConfigured Memory Speed: %d MT/s\n\tMinimum Voltage: 1.2 V\n\tMaximum Voltage: 1.2 V\n\tConfigured Voltage: 1.2 V\n\tMemory Technology: DRAM\n\n",
			h, 0x1000+socket, l, socket, socket, ch, spec.Speed, spec.Manufacturer, 0x3A5F0000+i, l, spec.Part, spec.Configured)
	}
	return b.String()
}

func (g *gen) memory() {
	locs, pop := g.slots()
	perSocket := map[int]uint64{}
	var total uint64
	for i, l := range locs {
		if pop[l] && !slices.Contains(g.s.MappedOut, l) {
			perSocket[i/16] += dimmSize
			total += dimmSize
		}
	}
	// Block size 2 GiB on large servers; block_size_bytes is hex.
	const block = 2 << 30
	g.write("/sys/devices/system/memory/block_size_bytes", fmt.Sprintf("%x\n", block))
	for i := 0; i < int(total/block); i++ {
		g.write(fmt.Sprintf("/sys/devices/system/memory/memory%d/state", i+16), "online\n") // first 32 GiB is a hole in numbering
	}
	reserved := uint64(9) << 30 // kernel, firmware, crashkernel
	memTotal := total - reserved
	kb := func(b uint64) uint64 { return b >> 10 }
	g.write("/proc/meminfo", fmt.Sprintf(`MemTotal:       %d kB
MemFree:        %d kB
MemAvailable:   %d kB
Buffers:          812344 kB
Cached:         %d kB
SwapCached:            0 kB
Active:         %d kB
SwapTotal:       8388604 kB
SwapFree:        8388604 kB
Dirty:               228 kB
HardwareCorrupted: %5d kB
AnonHugePages:   2048000 kB
HugePages_Total:       0
HugePages_Free:        0
HugePages_Rsvd:        0
HugePages_Surp:        0
Hugepagesize:       2048 kB
Hugetlb:               0 kB
`, kb(memTotal), kb(memTotal)/2, kb(memTotal)*7/10, kb(memTotal)/5, kb(memTotal)/4, g.s.HWCorruptedKB))
	for s := 0; s < g.s.Sockets; s++ {
		t := perSocket[s] - reserved/uint64(g.s.Sockets)
		g.write(fmt.Sprintf("/sys/devices/system/node/node%d/meminfo", s), fmt.Sprintf(
			"Node %d MemTotal:       %d kB\nNode %d MemFree:        %d kB\nNode %d MemUsed:        %d kB\n",
			s, kb(t), s, kb(t)/2, s, kb(t)-kb(t)/2))
	}

	// EDAC i10nm: 4 IMCs per socket, 2 channels each, one DIMM per channel.
	edac := "/sys/devices/system/edac/mc"
	for s := 0; s < g.s.Sockets; s++ {
		for imc := 0; imc < 4; imc++ {
			mcIdx := s*4 + imc
			d := fmt.Sprintf("%s/mc%d", edac, mcIdx)
			var ce, ue int
			for chn := 0; chn < 2; chn++ {
				loc := fmt.Sprintf("P%d-DIMM%c1", s+1, 'A'+imc*2+chn)
				if slices.Contains(g.s.MappedOut, loc) {
					continue
				}
				e := g.s.EDACErrors[loc]
				ce += e[0]
				ue += e[1]
				dd := fmt.Sprintf("%s/dimm%d", d, chn*2)
				g.write(dd+"/dimm_label", fmt.Sprintf("CPU_SrcID#%d_MC#%d_Chan#%d_DIMM#0\n", s, imc, chn))
				g.write(dd+"/dimm_location", fmt.Sprintf("channel %d slot 0\n", chn))
				g.write(dd+"/size", "32768\n")
				g.write(dd+"/dimm_mem_type", "Registered-DDR4\n")
				g.write(dd+"/dimm_edac_mode", "SECDED\n")
				g.write(dd+"/dimm_ce_count", fmt.Sprintf("%d\n", e[0]))
				g.write(dd+"/dimm_ue_count", fmt.Sprintf("%d\n", e[1]))
			}
			g.write(d+"/mc_name", fmt.Sprintf("Intel_10nm Socket#%d IMC#%d\n", s, imc))
			g.write(d+"/size_mb", "65536\n")
			g.write(d+"/ce_count", fmt.Sprintf("%d\n", ce))
			g.write(d+"/ue_count", fmt.Sprintf("%d\n", ue))
			g.write(d+"/ce_noinfo_count", "0\n")
			g.write(d+"/ue_noinfo_count", "0\n")
		}
	}
}

func (g *gen) tools() map[string]string {
	t := map[string]string{"dmidecode": "/usr/sbin/dmidecode", "journalctl": "/usr/bin/journalctl", "smartctl": "/usr/sbin/smartctl",
		"df": "/usr/bin/df", "ethtool": "/usr/sbin/ethtool", "ip": "/usr/sbin/ip", "lldpctl": "/usr/sbin/lldpctl", "nvidia-smi": "/usr/bin/nvidia-smi"}
	if g.s.Faults.MegaRAID {
		t["storcli64"] = "/opt/MegaRAID/storcli/storcli64"
	}
	if g.s.IPMITool != "" {
		t[g.s.IPMITool] = "/usr/bin/" + g.s.IPMITool
	}
	return t
}

type ipmiRow struct {
	name, unit, status string
	val                float64
	na                 bool
	th                 [6]string // lnr lcr lnc unc ucr unr
	typ                string    // ipmiutil sensor type
}

func (g *gen) ipmiSensors() []ipmiRow {
	tth := [6]string{"5.000", "5.000", "10.000", "91.000", "96.000", "96.000"}
	var rows []ipmiRow
	for s := 0; s < g.s.Sockets; s++ {
		t := 52 + s*3
		if v, ok := g.s.PkgTemps[s]; ok {
			t = v
		}
		st := "ok"
		if t >= 96 {
			st = "cr"
		} else if t >= 91 {
			st = "nc"
		}
		rows = append(rows, ipmiRow{name: fmt.Sprintf("CPU%d Temp", s+1), unit: "degrees C", status: st, val: float64(t), th: tth, typ: "Temperature"})
	}
	rows = append(rows,
		ipmiRow{name: "PCH Temp", unit: "degrees C", status: "ok", val: 48, th: [6]string{"-11.000", "-8.000", "-5.000", "90.000", "95.000", "100.000"}, typ: "Temperature"},
		ipmiRow{name: "System Temp", unit: "degrees C", status: "ok", val: 31, th: [6]string{"-9.000", "-7.000", "-5.000", "80.000", "85.000", "90.000"}, typ: "Temperature"},
		ipmiRow{name: "Inlet Temp", unit: "degrees C", status: "ok", val: 24, th: [6]string{"-9.000", "-7.000", "-5.000", "40.000", "45.000", "50.000"}, typ: "Temperature"},
	)
	locs, pop := g.slots()
	for i, l := range locs {
		if !pop[l] {
			continue
		}
		t := 34 + i%5
		if v, ok := g.s.DIMMTemps[l]; ok {
			t = v
		}
		st := "ok"
		if t >= 85 {
			st = "cr"
		} else if t >= 80 {
			st = "nc"
		}
		rows = append(rows, ipmiRow{name: l + " Temp", unit: "degrees C", status: st, val: float64(t), th: [6]string{"1.000", "2.000", "4.000", "80.000", "85.000", "90.000"}, typ: "Temperature"})
	}
	fth := [6]string{"300.000", "500.000", "700.000", "25300.000", "25400.000", "25500.000"}
	for f := 1; f <= 6; f++ {
		rows = append(rows, ipmiRow{name: fmt.Sprintf("FAN%d", f), unit: "RPM", status: "ok", val: float64(6200 + f*140), th: fth, typ: "Fan"})
	}
	rows = append(rows,
		ipmiRow{name: "FANA", unit: "RPM", na: true, status: "na", th: fth, typ: "Fan"},
		ipmiRow{name: "12V", unit: "Volts", status: "ok", val: 12.192, th: [6]string{"10.144", "10.272", "10.784", "12.960", "13.280", "13.408"}, typ: "Voltage"},
		ipmiRow{name: "5VCC", unit: "Volts", status: "ok", val: 5.028, th: [6]string{"4.246", "4.298", "4.480", "5.390", "5.546", "5.598"}, typ: "Voltage"},
		ipmiRow{name: "3.3VCC", unit: "Volts", status: "ok", val: 3.366, th: [6]string{"2.789", "2.823", "2.959", "3.554", "3.656", "3.690"}, typ: "Voltage"},
		ipmiRow{name: "VBAT", unit: "Volts", status: "ok", val: 3.104, th: [6]string{"2.402", "2.454", "2.506", "3.474", "3.526", "3.578"}, typ: "Voltage"},
		ipmiRow{name: "Vcpu1", unit: "Volts", status: "ok", val: 1.818, th: [6]string{"1.242", "1.260", "1.395", "2.088", "2.187", "2.205"}, typ: "Voltage"},
	)
	return rows
}

func (g *gen) ipmi() {
	if g.s.IPMITool == "" {
		return
	}
	g.write("/dev/ipmi0", "")
	rows := g.ipmiSensors()
	var b strings.Builder
	switch g.s.IPMITool {
	case "ipmitool":
		for _, r := range rows {
			v := fmt.Sprintf("%.3f", r.val)
			if r.na {
				v = "na"
			}
			fmt.Fprintf(&b, "%-16s | %-10s | %-10s | %-5s | %-9s | %-9s | %-9s | %-9s | %-9s | %-9s\n",
				r.name, v, r.unit, r.status, r.th[0], r.th[1], r.th[2], r.th[3], r.th[4], r.th[5])
		}
		b.WriteString("PS1 Status       | 0x1        | discrete   | 0x0100| na        | na        | na        | na        | na        | na        \n")
		b.WriteString("PS2 Status       | 0x1        | discrete   | 0x0100| na        | na        | na        | na        | na        | na        \n")
		g.cmd([]string{"ipmitool", "sensor"}, b.String(), 0)

		var sel strings.Builder
		for i, e := range g.s.SEL {
			dir := "Asserted"
			if !e.Asserted {
				dir = "Deasserted"
			}
			fmt.Fprintf(&sel, "%4x | %s | %s | %s %s | %s | %s\n", i+1, e.At.Format("01/02/2006"), e.At.Format("15:04:05"), e.Type, e.Sensor, e.Event, dir)
		}
		g.cmd([]string{"ipmitool", "sel", "elist"}, sel.String(), 0)
		g.cmd([]string{"ipmitool", "sel", "info"}, fmt.Sprintf("SEL Information\nVersion          : 1.5 (v1.5, v2 compliant)\nEntries          : %d\nFree Space       : %d bytes\nPercent Used     : 0%%\nLast Add Time    : %s\nLast Del Time    : %s\nOverflow         : false\nSupported Cmds   : 'Reserve' 'Get Alloc Info' \n# of Alloc Units : 1023\nAlloc Unit Size  : 16\n",
			len(g.s.SEL), (1023-len(g.s.SEL))*16, now.Add(-time.Hour).Format("01/02/2006 15:04:05"), boot.Format("01/02/2006 15:04:05")), 0)
	case "ipmiutil":
		b.WriteString("ipmiutil sensor version 3.18\n-- BMC version 1.40, IPMI version 2.0 \nID   | SDRType | Type            |SNum| Name             |Status| Reading\n")
		for i, r := range rows {
			st := map[string]string{"ok": "OK", "nc": "Warn", "cr": "Crit", "na": "Absent"}[r.status]
			unit := map[string]string{"degrees C": "C", "RPM": "RPM", "Volts": "V"}[r.unit]
			reading := fmt.Sprintf("%.2f %s", r.val, unit)
			if r.na {
				reading = "na"
			}
			fmt.Fprintf(&b, "%04x | Full    | %-15s | %02x | %-16s | %-5s| %s\n", 0x10+i, r.typ, 0x30+i, r.name, st, reading)
		}
		b.WriteString("0060 | Compact | Power Supply    | c8 | PS1 Status       | OK   | Presence detected\n")
		b.WriteString("0061 | Compact | Power Supply    | c9 | PS2 Status       | OK   | Presence detected\n")
		b.WriteString("ipmiutil sensor, completed successfully\n")
		g.cmd([]string{"ipmiutil", "sensor", "-c"}, b.String(), 0)

		var sel strings.Builder
		fmt.Fprintf(&sel, "ipmiutil sel version 3.18\n--- BMC version 1.40, IPMI version 2.0 \nSEL Ver 51 Support 0f, Size = 1023 records (Used=%d, Free=%d)\nRecId Date/Time_______ SEV Src_ Evt_Type___ Sens# Evt_detail - Trig [Evt_data]\n", len(g.s.SEL), 1023-len(g.s.SEL))
		for i, e := range g.s.SEL {
			src := "BMC "
			if strings.Contains(e.Event, "Log area") {
				src = "BIOS"
			}
			dir := "asserted"
			if !e.Asserted {
				dir = "deasserted"
			}
			sensor := strings.TrimPrefix(strings.TrimPrefix(e.Sensor, "#0x"), "#")
			if len(sensor) != 2 {
				sensor = "51"
			}
			fmt.Fprintf(&sel, "%04x %s %s %s %s %s #%s %s %s 6f [02 ff ff]\n", i+1, e.At.Format("01/02/06"), e.At.Format("15:04:05"), e.Sev, src, e.Type, sensor, e.Event, dir)
		}
		sel.WriteString("ipmiutil sel, completed successfully\n")
		g.cmd([]string{"ipmiutil", "sel"}, sel.String(), 0)
	}
}
