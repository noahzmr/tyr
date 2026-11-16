package sysfs

import (
	"os"
	"slices"
	"testing"

	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
)

const fx = "../../../testdata/fixtures/"

func read(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseProcStatReal(t *testing.T) {
	ps, err := ParseProcStat(read(t, fx+"laptop-amd-nonroot/root/proc/stat"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ps.CPUs) != 16 || ps.BTime < 1_700_000_000 || ps.All.Total == 0 || ps.All.Busy >= ps.All.Total {
		t.Errorf("cpus=%d btime=%d all=%+v", len(ps.CPUs), ps.BTime, ps.All)
	}
}

func TestParseProcStatOfflineCPU(t *testing.T) {
	ps, err := ParseProcStat(read(t, fx+"server-degraded/root/proc/stat"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ps.CPUs[7]; ok || len(ps.CPUs) != 63 {
		t.Errorf("offline cpu7 must be absent; got %d cpus", len(ps.CPUs))
	}
	if _, err := ParseProcStat([]byte("intr 1\n")); err == nil {
		t.Error("missing cpu line must fail")
	}
}

func TestUtilization(t *testing.T) {
	u := Utilization(CPUStat{Busy: 100, Total: 1000}, CPUStat{Busy: 150, Total: 1100})
	if u == nil || *u != 50 {
		t.Errorf("got %v", u)
	}
	if Utilization(CPUStat{Busy: 100, Total: 1000}, CPUStat{Busy: 10, Total: 1100}) != nil {
		t.Error("counter reset must give nil")
	}
	if Utilization(CPUStat{Total: 5}, CPUStat{Total: 5}) != nil {
		t.Error("no elapsed time must give nil")
	}
}

func TestParseInterrupts(t *testing.T) {
	in, err := ParseInterrupts(read(t, fx+"laptop-amd-nonroot/root/proc/interrupts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(in["MCE"]) != 16 || len(in["TRM"]) != 16 {
		t.Errorf("MCE=%v TRM=%v", in["MCE"], in["TRM"])
	}
	// Columns follow the header: with cpu7 offline the 8th column is CPU8.
	in, err = ParseInterrupts(read(t, fx+"server-degraded/root/proc/interrupts"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := in["TRM"][7]; ok || in["TRM"][20] != 14 || in["MCP"][63] != 2592 {
		t.Errorf("TRM=%v", in["TRM"])
	}
}

func TestParseCPUInfoX86(t *testing.T) {
	ci := ParseCPUInfo(read(t, fx+"laptop-amd-nonroot/root/proc/cpuinfo"))
	if ci.Vendor != "AuthenticAMD" || ci.Microcode == "" || len(ci.MHz) != 16 || !slices.Contains(ci.Flags, "svm") {
		t.Errorf("%+v", ci.Vendor)
	}
	if got := VirtFlags(ci.Flags); !slices.Contains(got, "svm") || !slices.Contains(got, "npt") {
		t.Errorf("virt flags %v", got)
	}
}

func TestParseCPUInfoArm64(t *testing.T) {
	// Ampere Altra (Neoverse-N1), as printed by Linux 6.x
	in := `processor	: 0
BogoMIPS	: 50.00
Features	: fp asimd evtstrm aes pmull sha1 sha2 crc32 atomics fphp asimdhp cpuid asimdrdm lrcpc dcpop asimddp ssbs
CPU implementer	: 0x41
CPU architecture: 8
CPU variant	: 0x3
CPU part	: 0xd0c
CPU revision	: 1

processor	: 1
BogoMIPS	: 50.00
CPU implementer	: 0x41
CPU part	: 0xd0c
`
	ci := ParseCPUInfo([]byte(in))
	if ci.Vendor != "ARM" || ci.Model != "Neoverse-N1" || !slices.Contains(ci.Flags, "crc32") {
		t.Errorf("%+v", ci)
	}
}

func TestReadCPUTopoAndMasks(t *testing.T) {
	f := hostfs.New(fx + "server-degraded/root")
	m, err := ReadCPUMasks(f)
	if err != nil {
		t.Fatal(err)
	}
	if m.Online.String() != "0-6,8-63" || m.Offline.String() != "7" || len(m.Present) != 64 {
		t.Errorf("masks %+v", m)
	}
	if tp := ReadCPUTopo(f, 48); tp.PackageID != 1 || tp.CoreID != 0 || tp.Node != 1 {
		t.Errorf("cpu48 %+v", tp)
	}
	if tp := ReadCPUTopo(f, 7); tp.PackageID != -1 || tp.CoreID != -1 {
		t.Errorf("offline cpu7 must have unknown topology: %+v", tp)
	}
	if n := NodeCPUs(f); n[0].String() != "0-6,8-15,32-47" {
		t.Errorf("node0 %v", n[0])
	}
	if c, _, ok := ReadThrottle(f, 20); !ok || c != 14 {
		t.Errorf("throttle %d %v", c, ok)
	}
}

func TestReadHwmon(t *testing.T) {
	chips := ReadHwmon(hostfs.New(fx + "server-degraded/root"))
	var core []HwmonChip
	for _, c := range chips {
		if c.Name == "coretemp" {
			core = append(core, c)
		}
	}
	if len(core) != 2 || core[0].Device != "/sys/devices/platform/coretemp.0" || chips[0].Name != "acpitz" {
		t.Fatalf("chips %+v", chips)
	}
	s := core[1].Sensors[0]
	if s.Label != "Package id 1" || *s.Input != 93 || *s.Max != 90 || *s.Crit != 100 {
		t.Errorf("sensor %+v", s)
	}
	th := s.Thresholds()
	if th == nil || *th.UpperWarn != 90 || *th.UpperCrit != 100 || th.Origin != "hwmon" {
		t.Errorf("thresholds %+v", th)
	}
	// real k10temp: only Tctl, no limits
	chips = ReadHwmon(hostfs.New(fx + "laptop-amd-nonroot/root"))
	found := false
	for _, c := range chips {
		if c.Name == "k10temp" {
			found = true
			if len(c.Sensors) == 0 || c.Sensors[0].Label != "Tctl" || c.Sensors[0].Kind != model.KindTemperature || c.Sensors[0].Thresholds() != nil {
				t.Errorf("k10temp %+v", c.Sensors)
			}
		}
	}
	if !found {
		t.Error("k10temp not found")
	}
}

func TestDetectVirt(t *testing.T) {
	if v := DetectVirt(hostfs.New(fx + "server-healthy/root")); v != "none" {
		t.Errorf("server: %q", v)
	}
	if v := DetectVirt(hostfs.New(fx + "laptop-amd-nonroot/root")); v != "none" {
		t.Errorf("laptop: %q", v)
	}
	dir := t.TempDir()
	os.MkdirAll(dir+"/sys/class/dmi/id", 0o755)
	os.WriteFile(dir+"/sys/class/dmi/id/sys_vendor", []byte("QEMU\n"), 0o644)
	if v := DetectVirt(hostfs.New(dir)); v != "kvm" {
		t.Errorf("qemu: %q", v)
	}
}

func TestParseMeminfo(t *testing.T) {
	mi, err := ParseMeminfo(read(t, fx+"laptop-amd-nonroot/root/proc/meminfo"))
	if err != nil {
		t.Fatal(err)
	}
	if mi["MemTotal"] < 16<<30 || mi["MemAvailable"] == 0 || mi["Hugepagesize"] != 2<<20 {
		t.Errorf("%v %v %v", mi["MemTotal"], mi["MemAvailable"], mi["Hugepagesize"])
	}
	if _, ok := mi["HugePages_Total"]; !ok {
		t.Error("HugePages_Total (a count, no unit) missing")
	}
	node, err := ParseMeminfo([]byte("Node 1 MemTotal:       230162432 kB\nNode 1 MemFree:  1 kB\n"))
	if err != nil || node["MemTotal"] != 230162432*1024 || node["MemFree"] != 1024 {
		t.Errorf("node meminfo %v %v", node, err)
	}
	if _, err := ParseMeminfo([]byte("garbage")); err == nil {
		t.Error("expected error")
	}
}

func TestReadMemoryBlocks(t *testing.T) {
	b, err := ReadMemoryBlocks(hostfs.New(fx + "laptop-amd-nonroot/root"))
	if err != nil {
		t.Fatal(err)
	}
	if b.BlockSize != 128<<20 || b.Online != 251 || b.Offline != 0 {
		t.Errorf("%+v", b)
	}
	b, err = ReadMemoryBlocks(hostfs.New(fx + "server-degraded/root"))
	if err != nil || b.BlockSize != 2<<30 || b.OnlineBytes() != 480<<30 {
		t.Errorf("%+v %v", b, err)
	}
}

func TestReadEDAC(t *testing.T) {
	mcs, err := ReadEDAC(hostfs.New(fx + "server-degraded/root"))
	if err != nil {
		t.Fatal(err)
	}
	if len(mcs) != 8 || mcs[5].Name != "mc5" || mcs[5].CE != 14 || mcs[5].Type != "Intel_10nm Socket#1 IMC#1" {
		t.Fatalf("%+v", mcs)
	}
	d := mcs[5].DIMMs[0]
	if d.Name != "dimm0" || d.CE != 14 || d.SizeBytes != 32<<30 || d.Label != "CPU_SrcID#1_MC#1_Chan#0_DIMM#0" {
		t.Errorf("%+v", d)
	}
	if mcs, err := ReadEDAC(hostfs.New(fx + "laptop-amd-nonroot/root")); err != nil || len(mcs) != 0 {
		t.Errorf("laptop: %v %v", mcs, err)
	}
}
