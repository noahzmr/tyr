package accel

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/fixture"
	"github.com/noahzmr/tyr/internal/model"
)

func collect(t *testing.T, dir string) *model.Accel {
	t.Helper()
	env, _, err := fixture.Load(dir, collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return collector.Collect(context.Background(), env, []collector.Collector{New()}, nil).Accel
}

func TestHealthyNvidia(t *testing.T) {
	a := collect(t, "../../../testdata/fixtures/server-healthy")
	if a.Status != model.StatusOK || len(a.GPUs) != 2 || len(a.NPUs) != 0 || len(a.BMCDisplays) != 1 || a.Summary != "2/2 GPUs OK, max 48°C" {
		t.Fatalf("%v %q %+v %+v", a.Status, a.Summary, a.GPUs, a.Findings)
	}
	g := a.GPUs[0]
	if g.Name != "card1" || g.Model != "NVIDIA A100 80GB PCIe" || g.DriverVersion != "550.127.05" || g.Vendor != "NVIDIA" || *g.MemTotalBytes != 81920<<20 ||
		g.Temps[0].Thresholds.Origin != "nvidia-smi" || *g.PowerCapWatts != 300 || g.NUMANode != 0 {
		t.Errorf("gpu0 %+v", g)
	}
	if a.BMCDisplays[0].Model != "ASPEED Graphics Family (BMC)" {
		t.Errorf("bmc %+v", a.BMCDisplays)
	}
}

func TestDegradedNvidia(t *testing.T) {
	a := collect(t, "../../../testdata/fixtures/server-degraded")
	got := map[string]model.Finding{}
	for _, f := range a.Findings {
		got[f.Code] = f
	}
	for code, st := range map[string]model.Status{"accel_ecc_ue": model.StatusCrit, "accel_driver_errors": model.StatusCrit,
		"remap_pending": model.StatusWarn, "pcie_width": model.StatusWarn, "accel_temp": model.StatusWarn} {
		if f, ok := got[code]; !ok || f.Status != st || f.Subject != "0000:ca:00.0" {
			t.Errorf("%s: %+v", code, f)
		}
	}
	if got["pcie_width"].Message != "GPU1 ca:00.0: PCIe link x8, device supports x16" {
		t.Errorf("message %q", got["pcie_width"].Message)
	}
	if a.GPUs[0].Status != model.StatusOK || a.GPUs[1].Status != model.StatusCrit || a.GPUs[1].KernelErrors != 1 {
		t.Errorf("statuses %v %v", a.GPUs[0].Status, a.GPUs[1].Status)
	}
}

func TestLaptopAMD(t *testing.T) {
	a := collect(t, "../../../testdata/fixtures/laptop-amd-nonroot")
	if len(a.GPUs) != 1 || len(a.NPUs) != 1 {
		t.Fatalf("%+v", a)
	}
	g, n := a.GPUs[0], a.NPUs[0]
	if g.Driver != "amdgpu" || !g.Integrated || g.MemTotalBytes == nil || len(g.Temps) == 0 || g.Firmware == "" || g.Model != "Krackan [Radeon 840M / 860M Graphics]" {
		t.Errorf("gpu %+v", g)
	}
	if n.Driver != "amdxdna" || n.Name != "accel0" || n.Firmware == "" || !n.Integrated || n.Model == "" {
		t.Errorf("npu %+v", n)
	}
	// amdxdna has no hwmon: the NPU shows the CPU die temperature (shared,
	// no NPU finding) and xrt-smi's power, mode, columns and contexts.
	if len(n.Temps) != 1 || !n.Temps[0].Shared || n.Temps[0].Source != "k10temp" || n.SensorNote == "" {
		t.Errorf("npu temps %+v note %q", n.Temps, n.SensorNote)
	}
	if n.PowerMode != "Default" || n.AIEColumns == nil || *n.AIEColumns != 8 || n.HWContexts == nil || *n.HWContexts != 0 ||
		n.PowerWatts == nil || n.RuntimeActiveMs == nil || *n.RuntimeActiveMs != 31845 {
		t.Errorf("npu xrt %+v", n)
	}
	for _, f := range a.Findings {
		if f.Code == "accel_temp" {
			t.Errorf("shared die temperature raised an accel finding: %+v", f)
		}
	}
	// Refresh: awake share from runtime PM, xrt-smi not re-run while the
	// NPU sleeps (static values carried over).
	env, _, err := fixture.Load("../../../testdata/fixtures/laptop-amd-nonroot", collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	prev := *a
	prev.NPUs = append([]model.Accelerator(nil), a.NPUs...)
	prev.CollectedAt = env.Now().Add(-10 * time.Second)
	prev.NPUs[0].RuntimeActiveMs = model.U(31845 - 2500)
	sec, err := New().Collect(context.Background(), env, &prev)
	if err != nil {
		t.Fatal(err)
	}
	r := sec.(*model.Accel).NPUs[0]
	if r.AwakePercent == nil || *r.AwakePercent < 24 || *r.AwakePercent > 26 || r.PowerMode != "Default" || r.AIEColumns == nil {
		t.Errorf("refresh %+v", r)
	}
}

// Intel iGPU (i915) + Intel NPU (intel_vpu) built as a minimal tree.
func TestIntelGPUAndNPU(t *testing.T) {
	dir := t.TempDir()
	w := func(p, v string) {
		os.MkdirAll(filepath.Dir(dir+"/root"+p), 0o755)
		os.WriteFile(dir+"/root"+p, []byte(v+"\n"), 0o644)
	}
	ln := func(p, target string) {
		os.MkdirAll(filepath.Dir(dir+"/root"+p), 0o755)
		os.Symlink(target, dir+"/root"+p)
	}
	for addr, attrs := range map[string][4]string{
		"0000:00:02.0": {"0x030000", "0x8086", "0x7d55", "i915"},
		"0000:00:0b.0": {"0x120000", "0x8086", "0x7d1d", "intel_vpu"},
	} {
		d := "/sys/devices/pci0000:00/" + addr
		w(d+"/class", attrs[0])
		w(d+"/vendor", attrs[1])
		w(d+"/device", attrs[2])
		w(d+"/power_state", "D0")
		ln(d+"/driver", "../../../bus/pci/drivers/"+attrs[3])
		ln("/sys/bus/pci/devices/"+addr, "../../../devices/pci0000:00/"+addr)
	}
	ln("/sys/class/drm/card0", "../../devices/pci0000:00/0000:00:02.0/drm/card0")
	ln("/sys/devices/pci0000:00/0000:00:02.0/drm/card0/device", "../../../0000:00:02.0")
	w("/sys/devices/pci0000:00/0000:00:02.0/drm/card0/gt_cur_freq_mhz", "1250")
	ln("/sys/class/accel/accel0", "../../devices/pci0000:00/0000:00:0b.0/accel/accel0")
	ln("/sys/devices/pci0000:00/0000:00:0b.0/accel/accel0/device", "../../../0000:00:0b.0")
	w("/sys/devices/pci0000:00/0000:00:0b.0/npu_busy_time_us", "1234567")
	w("/proc/stat", "cpu 1 0 1 1 0 0 0 0\nbtime 1700000000")
	os.WriteFile(dir+"/manifest.json", []byte(`{"hostname":"intel","virtualization":"none"}`), 0o644)
	a := collect(t, dir)
	if len(a.GPUs) != 1 || len(a.NPUs) != 1 || a.Status != model.StatusOK {
		t.Fatalf("%+v %+v", a, a.Findings)
	}
	if g := a.GPUs[0]; !g.Integrated || *g.ClockMHz != 1250 || g.Name != "card0" {
		t.Errorf("i915 %+v", g)
	}
	if n := a.NPUs[0]; n.Driver != "intel_vpu" || *n.BusyTimeUs != 1234567 || n.Name != "accel0" {
		t.Errorf("npu %+v", n)
	}
}
