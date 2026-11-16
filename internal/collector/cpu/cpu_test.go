package cpu

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/affinity"
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/fixture"
	"github.com/noahzmr/tyr/internal/model"
)

const fixtures = "../../../testdata/fixtures/"

func collect(t *testing.T, name string, coreTest bool) (*model.CPU, *collector.Env) {
	t.Helper()
	opts := collector.DefaultOptions()
	opts.CoreTest = coreTest
	env, _, err := fixture.Load(fixtures+name, opts)
	if err != nil {
		t.Fatal(err)
	}
	snap := collector.Collect(context.Background(), env, []collector.Collector{New()}, nil)
	if snap.CPU == nil {
		t.Fatal("no CPU section")
	}
	if os.Getenv("TYR_DUMP") != "" {
		b, _ := json.MarshalIndent(snap, "", "  ")
		t.Logf("%s", b)
	}
	return snap.CPU, env
}

func finding(c *model.CPU, code string) *model.Finding {
	for i := range c.Findings {
		if c.Findings[i].Code == code {
			return &c.Findings[i]
		}
	}
	return nil
}

func TestServerHealthy(t *testing.T) {
	c, _ := collect(t, "server-healthy", true)
	if c.Status != model.StatusOK {
		t.Errorf("status %v, findings %+v", c.Status, c.Findings)
	}
	if c.Summary != "64/64 cores online, max 57°C" {
		t.Errorf("summary %q", c.Summary)
	}
	if c.SocketCount != 2 || c.CoreCount != 32 || c.ThreadCount != 64 {
		t.Errorf("counts sockets=%d cores=%d threads=%d", c.SocketCount, c.CoreCount, c.ThreadCount)
	}
	if c.Vendor != "GenuineIntel" || !strings.Contains(c.Model, "Silver 4314") || c.Microcode != "0xd0003e7" {
		t.Errorf("identity %q %q %q", c.Vendor, c.Model, c.Microcode)
	}
	if c.Governor != "performance" || c.FreqDriver != "intel_pstate" || c.BaseFreqMHz == nil || *c.BaseFreqMHz != 2400 {
		t.Errorf("freq info %q %q %v", c.Governor, c.FreqDriver, c.BaseFreqMHz)
	}
	if len(c.Sockets) != 2 || c.Sockets[1].Designation != "CPU2" || c.Sockets[1].ID != 1 || c.Sockets[1].Online != 32 {
		t.Errorf("sockets %+v", c.Sockets)
	}
	if len(c.PackageTemps) != 2 || *c.PackageTemps[0].Value != 52 || c.PackageTemps[0].Thresholds.Origin != "hwmon" {
		t.Errorf("package temps %+v", c.PackageTemps)
	}
	l := c.Logical[40] // HT sibling of socket 0 core 8
	if l.SocketID != 0 || l.CoreID != 8 || l.NUMANode != 0 || l.TempCelsius == nil || *l.TempCelsius != 44+(8*7)%12 {
		t.Errorf("logical 40: %+v", l)
	}
	if l.CoreTest != model.CoreTestPass || c.CoreTest == nil || c.CoreTest.Passed != 64 {
		t.Errorf("core test %+v %+v", l.CoreTest, c.CoreTest)
	}
	if !strings.Contains(strings.Join(c.VirtFlags, ","), "vmx") {
		t.Errorf("virt flags %v", c.VirtFlags)
	}
	if st, _ := c.Source("kernel_log"); st.State != model.AvailOK || st.Reason != "via kmsg" {
		t.Errorf("kernel log source %+v", st)
	}
	// Full collection without a previous sample measures over a short window;
	// fixture /proc/stat is static, so utilization is 0 but present.
	if c.Logical[0].UtilizationPercent != nil && *c.Logical[0].UtilizationPercent != 0 {
		t.Errorf("util %v", *c.Logical[0].UtilizationPercent)
	}
}

func TestServerDegraded(t *testing.T) {
	c, _ := collect(t, "server-degraded", true)
	if c.Status != model.StatusCrit {
		t.Fatalf("status %v", c.Status)
	}
	f := finding(c, "core_offline")
	if f == nil || f.Status != model.StatusCrit || f.Message != "1 core offline (cpu7)" || f.Subject != "cpu7" {
		t.Errorf("offline finding %+v", f)
	}
	if !strings.HasPrefix(c.Summary, "63/64 cores online, max 96°C") {
		t.Errorf("summary %q", c.Summary)
	}
	if f := finding(c, "core_test_failed"); f == nil || !strings.Contains(f.Message, "cpu21 FAIL") {
		t.Errorf("core test finding %+v", f)
	}
	// package 1 at 93 > max 90 -> WARN; core 4 at 96 -> WARN (crit is 100)
	if f := finding(c, "cpu_temp"); f == nil || f.Status != model.StatusWarn || f.Message != "CPU1 Package at 93°C (warn 90°C)" {
		t.Errorf("cpu_temp %+v", f)
	}
	if f := finding(c, "core_temp"); f == nil || f.Subject != "CPU1 Core 4" {
		t.Errorf("core_temp %+v", f)
	}
	if f := finding(c, "mce"); f == nil || f.Subject != "cpu12" || f.Status != model.StatusWarn || !strings.Contains(f.Message, "2 machine check errors on cpu12 (corrected)") {
		t.Errorf("mce %+v", f)
	}
	var cpu7 *model.LogicalCPU
	for i := range c.Logical {
		if c.Logical[i].ID == 7 {
			cpu7 = &c.Logical[i]
		}
	}
	if cpu7 == nil || cpu7.Online || !cpu7.Present || cpu7.Status != model.StatusCrit || cpu7.CoreTest != model.CoreTestSkip || cpu7.CoreID != -1 {
		t.Errorf("cpu7 %+v", cpu7)
	}
	if c.Topology.Offline != "7" || c.Topology.Online != "0-6,8-63" {
		t.Errorf("topology %+v", c.Topology)
	}
	if f := finding(c, "throttle_history"); f == nil || f.Status != model.StatusOK || !strings.HasPrefix(f.Message, "37 ") {
		t.Errorf("throttle %+v", f)
	}
	// findings are ordered most severe first
	if c.Findings[0].Status != model.StatusCrit {
		t.Errorf("first finding %+v", c.Findings[0])
	}
}

func TestRefreshDetectsThrottlingAndKeepsInventory(t *testing.T) {
	c, env := collect(t, "server-degraded", false)
	prev := *c
	prev.Logical = append([]model.LogicalCPU(nil), c.Logical...)
	for i := range prev.Logical {
		if prev.Logical[i].ID == 20 {
			prev.Logical[i].ThrottleCount = model.U(10) // now 14 -> rising
		}
	}
	env.NewRound()
	sec, err := New().Collect(context.Background(), env, &prev)
	if err != nil {
		t.Fatal(err)
	}
	r := sec.(*model.CPU)
	if f := finding(r, "throttling"); f == nil || f.Subject != "cpu20" || f.Status != model.StatusWarn {
		t.Errorf("throttling finding %+v", r.Findings)
	}
	if len(r.Sockets) != 2 || r.Sockets[0].Designation != "CPU1" {
		t.Errorf("sockets not carried over: %+v", r.Sockets)
	}
}

func TestLaptopNonRoot(t *testing.T) {
	c, _ := collect(t, "laptop-amd-nonroot", false)
	if c.Vendor != "AuthenticAMD" || c.ThreadCount != 16 || c.CoreCount != 8 || c.SocketCount != 1 {
		t.Errorf("identity %q threads=%d cores=%d sockets=%d", c.Vendor, c.ThreadCount, c.CoreCount, c.SocketCount)
	}
	if st, _ := c.Source("dmidecode"); st.State != model.AvailUnavailable || st.Reason != "requires root" {
		t.Errorf("dmidecode source %+v", st)
	}
	if st, _ := c.Source("temperature"); st.State != model.AvailOK || st.Reason != "via k10temp" {
		t.Errorf("temperature source %+v", st)
	}
	if len(c.PackageTemps) == 0 || c.PackageTemps[0].Value == nil {
		t.Errorf("no k10temp package temperature: %+v", c.PackageTemps)
	}
	if st, _ := c.Source("kernel_log"); st.State != model.AvailOK || st.Reason != "via journalctl" {
		t.Errorf("kernel log source %+v", st)
	}
	// Missing root must not make the module UNKNOWN; the only findings in
	// this capture are temperatures (the laptop was hot while capturing).
	if c.Status == model.StatusUnknown {
		t.Errorf("non-root laptop must not be UNKNOWN: %+v", c.Findings)
	}
	for _, f := range c.Findings {
		if f.Code != "cpu_temp" && f.Status != model.StatusOK {
			t.Errorf("unexpected finding %+v", f)
		}
	}
	if c.BaseFreqMHz == nil {
		t.Errorf("amd_pstate nominal frequency not read")
	}
}

func TestCoreTestDetectsInjectedFault(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real kernels on every CPU")
	}
	allowed, err := affinity.Allowed()
	if err != nil {
		t.Skip(err)
	}
	cpus := allowed
	if len(cpus) > 4 {
		cpus = cpus[:4]
	}
	victim := cpus[len(cpus)-1]
	corrupt := func(cpu int, kernel string, v uint64) uint64 {
		if cpu == victim && kernel == "fma" {
			return v ^ 1<<40
		}
		return v
	}
	res := RunCoreTest(context.Background(), cpus, allowed.Contains, 10*time.Second, corrupt)
	for _, c := range cpus {
		r := res[c]
		if c == victim {
			if r.State != model.CoreTestFail || !strings.Contains(r.Detail, "fma") {
				t.Errorf("cpu%d: injected fault not detected: %+v", c, r)
			}
		} else if r.State != model.CoreTestPass {
			t.Errorf("cpu%d: %+v", c, r)
		}
	}
}

func TestCoreTestTimeoutAndSkip(t *testing.T) {
	allowed, err := affinity.Allowed()
	if err != nil || len(allowed) == 0 {
		t.Skip("no affinity info")
	}
	block := make(chan struct{})
	defer close(block)
	cpu := allowed[0]
	hang := func(c int, kernel string, v uint64) uint64 {
		if kernel == "int" {
			<-block // simulate a core that never finishes
		}
		return v
	}
	res := RunCoreTest(context.Background(), []int{cpu, 99999}, func(c int) bool { return c != 99999 }, 50*time.Millisecond, hang)
	if res[cpu].State != model.CoreTestTimeout {
		t.Errorf("hung core: %+v", res[cpu])
	}
	if res[99999].State != model.CoreTestSkip {
		t.Errorf("disallowed core: %+v", res[99999])
	}
	_ = runtime.NumGoroutine()
}

func TestBMCOnlyTemperatures(t *testing.T) {
	c, _ := collect(t, "server-bmc-only", false)
	if st, _ := c.Source("temperature"); st.Reason != "via ipmi" {
		t.Fatalf("temperature source %+v", st)
	}
	if len(c.PackageTemps) != 2 || c.PackageTemps[1].Name != "CPU1 Package (CPU2 Temp)" || *c.PackageTemps[1].Value != 55 ||
		c.PackageTemps[1].Source != "ipmi" || c.PackageTemps[1].Thresholds.Origin != "ipmi" {
		t.Errorf("package temps %+v", c.PackageTemps)
	}
	if l := c.Logical[63]; l.TempCelsius == nil || *l.TempCelsius != 55 {
		t.Errorf("cpu63 temp %v", l.TempCelsius)
	}
	if c.Status != model.StatusOK || c.Summary != "64/64 cores online, max 55°C" {
		t.Errorf("%v %q %+v", c.Status, c.Summary, c.Findings)
	}
}

func TestCPUSensorNames(t *testing.T) {
	yes := []string{"CPU1 Temp", "CPU 2 Temp", "02-CPU 1", "CPU0_TEMP", "TEMP_CPU1", "Processor 1 Temp"}
	no := []string{"CPU1_VRM Temp", "VRMCpu2 Temp", "P1-DIMMA1 Temp", "CPU1 Margin", "CPU_FAN1", "Inlet Temp", "CPU1 MEM Temp"}
	for _, n := range yes {
		if !cpuSensorName(n) {
			t.Errorf("%q should be a CPU sensor", n)
		}
	}
	for _, n := range no {
		if cpuSensorName(n) {
			t.Errorf("%q should not be a CPU sensor", n)
		}
	}
}

func TestCPUDetails(t *testing.T) {
	c, _ := collect(t, "server-degraded", false)
	if c.Family != "6" || c.ModelNumber != "106" || c.Stepping != "6" || len(c.Flags) < 100 {
		t.Errorf("identity %q %q %q %d flags", c.Family, c.ModelNumber, c.Stepping, len(c.Flags))
	}
	if len(c.Caches) != 4 || c.Caches[3].Level != 3 || c.Caches[3].Instances != 2 || c.Caches[0].Instances != 32 || // core 7 keeps cpu39 online

		c.Caches[3].SizeBytes != 24<<20 {
		t.Errorf("caches %+v", c.Caches)
	}
	vuln := map[string]model.Status{}
	for _, v := range c.Vulnerabilities {
		vuln[v.Name] = v.Status
	}
	if vuln["gather_data_sampling"] != model.StatusWarn || vuln["meltdown"] != model.StatusOK || vuln["spectre_v2"] != model.StatusOK {
		t.Errorf("vulnerabilities %+v", c.Vulnerabilities)
	}
	for _, f := range c.Findings {
		if strings.Contains(f.Message, "gather_data_sampling") {
			t.Errorf("vulnerability must not be a hardware finding: %+v", f)
		}
	}
	l := c.Logical[0]
	if l.Siblings != "0,32" || l.MaxFreqMHz == nil || *l.MaxFreqMHz != 3400 || l.Microcode != "0xd0003e7" || len(l.IdleStates) != 4 ||
		l.IdleStates[3].Name != "C6" || l.IdleStates[3].TimeUs == nil || l.CoreType != "" {
		t.Errorf("cpu0 %+v", l)
	}
	if c.SMTControl != "on" || c.SMTActive == nil || !*c.SMTActive || c.Boost == nil || !*c.Boost {
		t.Errorf("policy smt=%q boost=%v", c.SMTControl, c.Boost)
	}

	// Ryzen AI 350: Zen 5 (5.09 GHz) and Zen 5c (3.5 GHz) cores, AMD
	// preferred-core ranking, all with the same cpu_capacity.
	lap, _ := collect(t, "laptop-amd-nonroot", false)
	types := map[string]int{}
	for _, l := range lap.Logical {
		types[l.CoreType]++
		if l.PrefRank == nil {
			t.Errorf("cpu%d: no preferred-core rank", l.ID)
		}
	}
	if types["P"] != 8 || types["E"] != 8 {
		t.Errorf("core types %v", types)
	}
	if lap.Logical[2].CoreType != "E" || len(lap.Caches) != 4 || len(lap.Vulnerabilities) == 0 {
		t.Errorf("laptop cpu2 %+v caches %d vulns %d", lap.Logical[2].CoreType, len(lap.Caches), len(lap.Vulnerabilities))
	}
}

func TestMicrocodeMismatch(t *testing.T) {
	h := &model.Header{}
	ls := []model.LogicalCPU{{ID: 0, Online: true, Microcode: "0xa"}, {ID: 1, Online: true, Microcode: "0xb"}, {ID: 2, Online: false, Microcode: "0xc"}}
	microcodeFindings(h, ls)
	if len(h.Findings) != 1 || h.Findings[0].Status != model.StatusWarn || !strings.Contains(h.Findings[0].Message, "0xa on cpu0") {
		t.Fatalf("%+v", h.Findings)
	}
	h = &model.Header{}
	microcodeFindings(h, ls[:1])
	if len(h.Findings) != 0 {
		t.Fatalf("single revision: %+v", h.Findings)
	}
}
