package memory

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/fixture"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

const fixtures = "../../../testdata/fixtures/"

func collect(t *testing.T, name string) *model.Memory {
	t.Helper()
	env, _, err := fixture.Load(fixtures+name, collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	snap := collector.Collect(context.Background(), env, []collector.Collector{New()}, nil)
	if os.Getenv("TYR_DUMP") != "" {
		b, _ := json.MarshalIndent(snap.Memory, "", "  ")
		t.Logf("%s", b)
	}
	return snap.Memory
}

func findingsOf(m *model.Memory, code string) []model.Finding {
	var out []model.Finding
	for _, f := range m.Findings {
		if f.Code == code {
			out = append(out, f)
		}
	}
	return out
}

func dimm(m *model.Memory, loc string) *model.DIMM {
	for i := range m.DIMMs {
		if m.DIMMs[i].Locator == loc {
			return &m.DIMMs[i]
		}
	}
	return nil
}

func TestServerHealthy(t *testing.T) {
	m := collect(t, "server-healthy")
	if m.Status != model.StatusOK {
		t.Errorf("status %v: %+v", m.Status, m.Findings)
	}
	if m.Summary != "512 GB, 0 ECC errors" {
		t.Errorf("summary %q", m.Summary)
	}
	if m.Slots != 32 || m.PopulatedSlots != 16 || *m.InstalledBytes != 512<<30 || m.ECCType != "Multi-bit ECC" || *m.MaxCapacityBytes != 8<<40 {
		t.Errorf("inventory slots=%d pop=%d installed=%v ecc=%q max=%v", m.Slots, m.PopulatedSlots, m.InstalledBytes, m.ECCType, m.MaxCapacityBytes)
	}
	d := dimm(m, "P2-DIMMC1")
	if d == nil || d.Channel != "P2-C" || d.EDACLabel != "CPU_SrcID#1_MC#1_Chan#0_DIMM#0" || d.CorrectableErrors == nil || *d.ECC != true || d.PartNumber != "M393A4K40EB3-CWE" {
		t.Errorf("P2-DIMMC1 %+v", d)
	}
	if e := dimm(m, "P1-DIMMA2"); e == nil || e.Populated || e.PartNumber != "" || e.CorrectableErrors != nil {
		t.Errorf("empty slot %+v", e)
	}
	if len(m.EDAC) != 8 || len(m.NUMANodes) != 2 || *m.OnlineBytes != 512<<30 {
		t.Errorf("edac=%d nodes=%d online=%v", len(m.EDAC), len(m.NUMANodes), m.OnlineBytes)
	}
	// every populated DIMM must be matched to an EDAC DIMM
	for _, mc := range m.EDAC {
		for _, d := range mc.DIMMs {
			if d.Locator == "" {
				t.Errorf("unmatched EDAC DIMM %s/%s %q", mc.Name, d.Name, d.Label)
			}
		}
	}
}

func TestServerDegraded(t *testing.T) {
	m := collect(t, "server-degraded")
	if m.Status != model.StatusCrit {
		t.Fatalf("status %v", m.Status)
	}
	want := map[string]string{
		"hardware_corrupted":  "4 kB of memory poisoned by the kernel (HardwareCorrupted)",
		"ecc_uncorrectable":   "1 uncorrectable ECC error on P2-DIMMB1",
		"ecc_correctable":     "14 correctable ECC errors on P2-DIMMC1",
		"capacity_mismatch":   "512 GB installed, OS sees 480 GB",
		"numa_imbalance":      "NUMA node 1 has 220 GB, largest node has 252 GB",
		"dimm_speed_mismatch": "DIMMs run at different speeds: P1-DIMMD1 at 2933 MT/s, others up to 3200 MT/s",
	}
	for code, msg := range want {
		fs := findingsOf(m, code)
		if len(fs) != 1 || fs[0].Message != msg {
			t.Errorf("%s: got %+v, want %q", code, fs, msg)
		}
	}
	if fs := findingsOf(m, "mixed_part_numbers_system"); len(fs) != 1 || fs[0].Status != model.StatusOK {
		t.Errorf("mixed parts %+v", fs)
	}
	if m.Summary != "512 GB, 15 ECC errors" {
		t.Errorf("summary %q", m.Summary)
	}
	if d := dimm(m, "P2-DIMMB1"); d.Status != model.StatusCrit {
		t.Errorf("B1 status %v", d.Status)
	}
	// the mapped-out DIMM has no EDAC entry -> no error counts
	if d := dimm(m, "P2-DIMMH1"); d.CorrectableErrors != nil || d.EDACLabel != "" {
		t.Errorf("H1 %+v", d)
	}
	snap := &model.Snapshot{Memory: m}
	if c := snap.Counters(); c["edac.mc5.dimm0.ce"] != 14 || c["edac.mc4.ue"] != 1 || c["mem.hardware_corrupted_bytes"] != 4096 {
		t.Errorf("counters %v", c)
	}
}

func TestRefreshKeepsInventory(t *testing.T) {
	env, _, err := fixture.Load(fixtures+"server-degraded", collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	first, err := New().Collect(context.Background(), env, nil)
	if err != nil {
		t.Fatal(err)
	}
	env.NewRound()
	sec, err := New().Collect(context.Background(), env, first)
	if err != nil {
		t.Fatal(err)
	}
	r := sec.(*model.Memory)
	if len(r.DIMMs) != 32 || r.InstalledBytes == nil || dimm(r, "P2-DIMMC1").CorrectableErrors == nil || r.Status != model.StatusCrit {
		t.Errorf("refresh lost inventory or EDAC mapping: dimms=%d", len(r.DIMMs))
	}
	if d := dimm(first.(*model.Memory), "P2-DIMMC1"); *d.CorrectableErrors != 14 {
		t.Error("refresh modified prev")
	}
}

func TestLaptopNonRoot(t *testing.T) {
	m := collect(t, "laptop-amd-nonroot")
	if m.TotalBytes < 29<<30 || m.InstalledBytes != nil || len(m.DIMMs) != 0 {
		t.Errorf("total=%d installed=%v dimms=%d", m.TotalBytes, m.InstalledBytes, len(m.DIMMs))
	}
	if st, _ := m.Source("edac"); st.State != model.AvailUnavailable || st.Reason != "no EDAC memory controller driver loaded" {
		t.Errorf("edac source %+v", st)
	}
	if !strings.HasSuffix(m.Summary, ", ECC n/a") || m.Status != model.StatusOK {
		t.Errorf("summary %q status %v %+v", m.Summary, m.Status, m.Findings)
	}
	if m.OnlineBytes == nil || *m.OnlineBytes != 251*128<<20 {
		t.Errorf("online %v", m.OnlineBytes)
	}
}

func TestParseLocator(t *testing.T) {
	cases := []struct {
		loc, bank string
		want      slot
		group     string
	}{
		{"P1-DIMMA1", "P0_Node0_ChannelA_Dimm0", slot{1, "A", 1}, "P1-A"},
		{"CPU2_DIMM_F2", "", slot{2, "F", 2}, "P2-F"},
		{"CPU1 DIMM B1", "", slot{1, "B", 1}, "P1-B"},
		{"DIMM_C1", "P1_Node1_Channel2_Dimm0", slot{2, "C", 1}, "P2-C"},
		{"DIMM_C1", "BANK 2", slot{-1, "C", 1}, "C"},
		{"ChannelA-DIMM0", "BANK 0", slot{-1, "A", 1}, "A"},
		{"DIMM 0", "P0_Node0_Channel3_Dimm1", slot{1, "D", 2}, "P1-D"}, // AMD EPYC (Gigabyte)
		{"A1", "", slot{Socket: -1}, ""},                               // Dell: channel not encoded
		{"PROC 1 DIMM 12", "", slot{Socket: -1}, ""},                   // HPE
	}
	for _, c := range cases {
		got := parseLocator(c.loc, c.bank)
		if got != c.want || got.group() != c.group {
			t.Errorf("parseLocator(%q,%q) = %+v %q, want %+v %q", c.loc, c.bank, got, got.group(), c.want, c.group)
		}
	}
}

func TestDIMMMatcher(t *testing.T) {
	dimms := []model.DIMM{
		{Locator: "DIMM_A1", Populated: true},
		{Locator: "P1-DIMMB1", Populated: true},
		{Locator: "P2-DIMMD1", Populated: true},
	}
	mcs := []sysfs.EDACController{{DIMMs: []sysfs.EDACDIMM{
		{Label: "CPU_SrcID#1_MC#1_Chan#1_DIMM#0"},
		{Label: "CPU_SrcID#1_MC#0_Chan#1_DIMM#0"},
	}}}
	m := newDIMMMatcher(dimms, mcs)
	if i := m.match(sysfs.EDACDIMM{Label: "DIMM_A1"}); i != 0 {
		t.Errorf("exact label: %d", i)
	}
	if i := m.match(sysfs.EDACDIMM{Label: "SOCKET0_P1-DIMMB1"}); i != 1 {
		t.Errorf("suffix label: %d", i)
	}
	if i := m.match(mcs[0].DIMMs[0]); i != 2 { // socket 1, mc1*2+chan1 = D
		t.Errorf("intel label: %d", i)
	}
	if i := m.match(sysfs.EDACDIMM{Label: "mc#0csrow#0channel#0"}); i != -1 {
		t.Errorf("unknown label matched %d", i)
	}
}

func TestCsrowAndJC42(t *testing.T) {
	dir := t.TempDir()
	w := func(p, v string) {
		os.MkdirAll(dir+p[:strings.LastIndexByte(p, '/')], 0o755)
		os.WriteFile(dir+p, []byte(v+"\n"), 0o644)
	}
	// legacy csrow-only driver (e.g. old amd64_edac)
	mc := "/sys/devices/system/edac/mc/mc0"
	w(mc+"/mc_name", "F17h")
	w(mc+"/ce_count", "3")
	w(mc+"/ue_count", "0")
	w(mc+"/csrow0/ch0_ce_count", "2")
	w(mc+"/csrow0/ch0_dimm_label", "DIMM_A1")
	w(mc+"/csrow0/ch1_ce_count", "1")
	w("/proc/meminfo", "MemTotal: 16384 kB\nMemAvailable: 8192 kB\nHardwareCorrupted: 0 kB")
	w("/sys/devices/platform/i2c/0-0018/hwmon/hwmon5/name", "jc42")
	w("/sys/devices/platform/i2c/0-0018/hwmon/hwmon5/temp1_input", "41250")
	w("/sys/devices/platform/i2c/0-0018/hwmon/hwmon5/temp1_crit", "95000")
	os.MkdirAll(dir+"/sys/class/hwmon", 0o755)
	os.Symlink("../../devices/platform/i2c/0-0018/hwmon/hwmon5", dir+"/sys/class/hwmon/hwmon5")
	os.Symlink("../..", dir+"/sys/devices/platform/i2c/0-0018/hwmon/hwmon5/device")
	os.WriteFile(dir+"/manifest.json", []byte(`{"hostname":"t","virtualization":"none"}`), 0o644)
	os.Rename(dir+"/sys", dir+"/root-sys")
	os.Rename(dir+"/proc", dir+"/root-proc")
	os.MkdirAll(dir+"/root", 0o755)
	os.Rename(dir+"/root-sys", dir+"/root/sys")
	os.Rename(dir+"/root-proc", dir+"/root/proc")

	env, _, err := fixture.Load(dir, collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	sec, err := New().Collect(context.Background(), env, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := sec.(*model.Memory)
	if len(m.EDAC) != 1 || len(m.EDAC[0].DIMMs) != 2 || m.EDAC[0].DIMMs[0].Label != "DIMM_A1" || *m.CorrectableErrors != 3 {
		t.Errorf("csrow edac %+v", m.EDAC)
	}
	if len(m.Temps) != 1 || *m.Temps[0].Value != 41.25 || *m.Temps[0].Thresholds.UpperCrit != 95 || *m.Temps[0].Thresholds.UpperWarn != 85 {
		t.Errorf("jc42 %+v", m.Temps)
	}
	if fs := findingsOf(m, "ecc_correctable"); len(fs) != 2 {
		t.Errorf("csrow CE findings %+v", fs)
	}
}

func TestBMCDIMMTempsAndSEL(t *testing.T) {
	m := collect(t, "server-degraded")
	if st, _ := m.Source("dimm_temperature"); st.Reason != "via ipmi" {
		t.Errorf("dimm temp source %+v", st)
	}
	if d := dimm(m, "P2-DIMMC1"); d.TempCelsius == nil || *d.TempCelsius != 82 {
		t.Errorf("C1 temp %v", d.TempCelsius)
	}
	if fs := findingsOf(m, "dimm_temp"); len(fs) != 1 || fs[0].Status != model.StatusWarn || fs[0].Message != "P2-DIMMC1 Temp at 82°C" {
		t.Errorf("dimm_temp %+v", fs)
	}
	if fs := findingsOf(m, "sel_memory_critical"); len(fs) != 1 || fs[0].Message != "1 memory event in BMC SEL, latest 2026-11-14 09:00: Uncorrectable ECC @DIMMB1(CPU2)" {
		t.Errorf("sel crit %+v", fs)
	}
	if fs := findingsOf(m, "sel_memory_warning"); len(fs) != 1 {
		t.Errorf("sel warn %+v", fs)
	}
	if st, _ := m.Source("ipmi_sel"); st.State != model.AvailOK || st.Reason != "via ipmitool" {
		t.Errorf("sel source %+v", st)
	}
	// healthy host via ipmiutil: no thresholds in output -> defaults, all OK
	h := collect(t, "server-healthy")
	if len(h.Temps) != 16 || h.Temps[0].Thresholds.Origin != "default" || h.Status != model.StatusOK {
		t.Errorf("healthy temps=%d status=%v %+v", len(h.Temps), h.Status, h.Findings)
	}
}
