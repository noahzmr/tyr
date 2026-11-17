package gpu

import (
	"os"
	"testing"

	"github.com/noahzmr/tyr/internal/hostfs"
)

func TestNvidiaSMI(t *testing.T) {
	b, err := os.ReadFile("../../../testdata/fixtures/server-degraded/cmd/nvidia-smi_-q_-x.txt")
	if err != nil {
		t.Fatal(err)
	}
	gs, err := ParseNvidiaSMI(b)
	if err != nil || len(gs) != 2 {
		t.Fatalf("%v %d", err, len(gs))
	}
	g := gs[1]
	if g.BusID != "0000:ca:00.0" || g.Product != "NVIDIA A100 80GB PCIe" || g.DriverVersion != "550.127.05" || g.VBIOS != "92.00.68.00.01" {
		t.Errorf("identity %+v", g)
	}
	if *g.TempC != 88 || *g.TempSlowC != 87 || *g.TempMaxC != 92 || *g.MemTempC != 92 || *g.MemTempMaxC != 95 {
		t.Errorf("temps %+v", g)
	}
	if *g.PowerW != 62.41 || *g.PowerCapW != 300 || *g.MemTotalBytes != 81920<<20 || *g.UtilPercent != 0 || *g.ClockMHz != 1410 {
		t.Errorf("power/mem %+v", g)
	}
	if *g.LinkWidth != 8 || *g.MaxLinkWidth != 16 || g.LinkGen != 4 || *g.ECCVolatileUE != 2 || *g.ECCAggregateUE != 2 || !*g.RemapPending || *g.RemapFailed {
		t.Errorf("link/ecc %+v", g)
	}
	if g.RetiredPages != nil || len(g.ThrottleReasons) != 0 {
		t.Errorf("N/A retired pages / idle throttle must be empty: %+v %v", g.RetiredPages, g.ThrottleReasons)
	}
}

// Pre-Ampere driver (e.g. 470 on V100): power_readings, clocks_throttle_reasons,
// single/double bit ECC, retired pages.
func TestNvidiaSMIOldSchema(t *testing.T) {
	in := `<?xml version="1.0" ?><nvidia_smi_log><driver_version>470.223.02</driver_version>
<gpu id="00000000:3B:00.0"><product_name>Tesla V100-PCIE-32GB</product_name>
<clocks_throttle_reasons><clocks_throttle_reason_gpu_idle>Not Active</clocks_throttle_reason_gpu_idle>
<clocks_throttle_reason_hw_thermal_slowdown>Active</clocks_throttle_reason_hw_thermal_slowdown></clocks_throttle_reasons>
<ecc_errors><volatile><single_bit><device_memory>4</device_memory><total>4</total></single_bit><double_bit><total>0</total></double_bit></volatile></ecc_errors>
<retired_pages><multiple_single_bit_retirement><retired_count>2</retired_count></multiple_single_bit_retirement>
<double_bit_retirement><retired_count>1</retired_count></double_bit_retirement><pending_retirement>Yes</pending_retirement></retired_pages>
<temperature><gpu_temp>79 C</gpu_temp></temperature><power_readings><power_draw>250.10 W</power_draw><power_limit>250.00 W</power_limit></power_readings>
</gpu></nvidia_smi_log>`
	gs, err := ParseNvidiaSMI([]byte(in))
	if err != nil || len(gs) != 1 {
		t.Fatal(err)
	}
	g := gs[0]
	if g.BusID != "0000:3b:00.0" || *g.ECCVolatileCE != 4 || *g.ECCVolatileUE != 0 || *g.RetiredPages != 3 || !g.RetirePending ||
		*g.PowerW != 250.1 || *g.PowerCapW != 250 || len(g.ThrottleReasons) != 1 || g.ThrottleReasons[0] != "hw_thermal_slowdown" {
		t.Errorf("%+v", g)
	}
	if _, err := ParseNvidiaSMI([]byte("<foo/>")); err == nil {
		t.Error("missing root must fail")
	}
}

func TestReadAMD(t *testing.T) {
	dir := t.TempDir()
	dev := "/sys/devices/pci0000:00/0000:03:00.0"
	w := func(p, v string) {
		os.MkdirAll(dir+dev+p[:len(p)-len(lastElem(p))], 0o755)
		os.WriteFile(dir+dev+p, []byte(v), 0o644)
	}
	w("/gpu_busy_percent", "37\n")
	w("/mem_info_vram_total", "68702699520\n")
	w("/mem_info_vram_used", "1073741824\n")
	w("/vbios_version", "113-D67301-063\n")
	w("/ras/umc_err_count", "ue: 1\nce: 12\n")
	w("/ras/gpu_vram_bad_pages", "0x00000000001a2b3c : 0x00001000 : R\n0x00000000001a2b3d : 0x00001000 : P\n")
	a := ReadAMD(hostfs.New(dir), dev)
	if *a.BusyPercent != 37 || *a.VRAMTotal != 68702699520 || a.VBIOS != "113-D67301-063" || *a.ECCUE != 1 || *a.ECCCE != 12 || *a.BadPages != 2 {
		t.Errorf("%+v", a)
	}
}

func lastElem(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// DGX Spark: GB10 on NVLink-C2C, no memory of its own, T.Limit instead of a
// slowdown threshold.
func TestNvidiaSMIGB10(t *testing.T) {
	b, err := os.ReadFile("testdata/gb10.xml")
	if err != nil {
		t.Fatal(err)
	}
	gs, err := ParseNvidiaSMI(b)
	if err != nil || len(gs) != 1 {
		t.Fatalf("%v %d", err, len(gs))
	}
	g := gs[0]
	if g.BusID != "000f:01:00.0" || g.Product != "NVIDIA GB10" || g.AddressingMode != "ATS" || g.C2CMode != "Enabled" {
		t.Errorf("identity %+v", g)
	}
	if g.MemTotalBytes != nil || g.MemUsedBytes != nil || g.TempSlowC != nil || *g.TempTLimitC != 56 || *g.TempC != 39 || *g.PowerW != 11.77 || g.PowerCapW != nil {
		t.Errorf("mem/temp/power %+v", g)
	}
	if len(g.Processes) != 3 || g.Processes[2].Name != "VLLM::EngineCore" || g.Processes[2].PID != 51787 || *g.ProcessMemBytes != (18+6+80564)<<20 {
		t.Errorf("processes %+v", g.Processes)
	}
	if g.ECCVolatileUE != nil || g.RetiredPages != nil || g.RemapPending != nil || len(g.ThrottleReasons) != 0 {
		t.Errorf("N/A counters must stay empty: %+v", g)
	}
}
