// Package accel collects GPUs and NPUs: PCI inventory and link, driver and
// firmware, utilization, memory, temperatures, power, ECC/RAS, throttling
// and driver errors from the kernel log.
package accel

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/gpu"
	"github.com/noahzmr/tyr/internal/source/kernellog"
	"github.com/noahzmr/tyr/internal/source/pci"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

// Collector is the GPU/NPU collector.
type Collector struct{}

// New returns a GPU/NPU collector.
func New() *Collector { return &Collector{} }

func (*Collector) ID() model.ModuleID { return model.ModAccel }

var (
	npuDrivers = map[string]bool{"amdxdna": true, "intel_vpu": true, "ivpu": true, "habanalabs": true, "qaic": true}
	// Board/BMC and hypervisor display adapters: not GPUs.
	auxDisplay = map[uint16]string{0x1a03: "BMC", 0x102b: "BMC", 0x1234: "virtual", 0x1013: "virtual",
		0x15ad: "virtual", 0x1af4: "virtual", 0x1414: "virtual", 0x80ee: "virtual", 0x1b36: "virtual"}
	hwThrottle = map[string]bool{"hw_slowdown": true, "hw_thermal_slowdown": true, "hw_power_brake_slowdown": true, "sw_thermal_slowdown": true}
)

func (c *Collector) Collect(ctx context.Context, env *collector.Env, prevSec model.Section) (model.Section, error) {
	f := env.FS
	prev, _ := prevSec.(*model.Accel)
	out := &model.Accel{GPUs: []model.Accelerator{}, NPUs: []model.Accelerator{}}
	h := &out.Header
	if !hostfs.Exists(f, "/sys/bus/pci/devices") {
		h.SetSource("pci", model.AvailUnavailable, "no PCI bus in sysfs")
		h.Summary = "no PCI bus"
		h.Rollup()
		return out, nil
	}
	h.SetSource("pci", model.AvailOK, "")

	cards := classLinks(f, "/sys/class/drm/card[0-9]*")
	accels := classLinks(f, "/sys/class/accel/accel[0-9]*")
	hw := sysfs.ReadHwmon(f)
	klog, kst := env.KernelLog(ctx)
	h.Sources = append(h.Sources, kst)
	gpuErrs := map[string][]kernellog.Entry{}
	if klog != nil {
		for _, e := range klog.ByCategory(kernellog.CatGPU) {
			gpuErrs[e.Subject] = append(gpuErrs[e.Subject], e)
		}
	}

	var nv map[string]gpu.NvidiaGPU
	for _, d := range pci.List(f) {
		_, isAccelNode := accels[d.Path]
		isNPU := npuDrivers[d.Driver] || isAccelNode || d.ClassBase() == 0x12
		isGPU := d.ClassBase() == 0x03
		if !isNPU && !isGPU {
			continue
		}
		vendor, mdl := pci.Names(f, d.Vendor, d.Device)
		if isGPU && auxDisplay[d.Vendor] != "" {
			out.BMCDisplays = append(out.BMCDisplays, model.PCIDevice{Address: d.Address, Vendor: vendor, Model: mdl + " (" + auxDisplay[d.Vendor] + ")", Driver: d.Driver})
			continue
		}
		a := model.Accelerator{Kind: "gpu", PCIAddress: d.Address, Vendor: vendor, Model: mdl, Driver: d.Driver, NUMANode: d.NUMANode,
			LinkSpeed: d.LinkSpeed, LinkWidth: d.LinkWidth, MaxLinkSpeed: d.MaxLinkSpeed, MaxLinkWidth: d.MaxLinkWidth,
			PowerState: d.PowerState, Temps: []model.Sensor{}, Fans: []model.Sensor{}, ThrottleReasons: []string{}}
		a.RuntimeStatus = hostfs.StringOr(f, d.Path+"/power/runtime_status", "")
		a.Integrated = d.Vendor == 0x8086 && strings.HasSuffix(d.Address, "00:02.0") || (d.Vendor == 0x1002 || d.Vendor == 0x1022) && strings.Contains(d.Path, "/0000:00:08.")
		if isNPU {
			a.Kind = "npu"
			a.Name = accels[d.Path]
			if b := hostfs.StringOr(f, d.Path+"/vbnv", ""); b != "" {
				a.Model = b + " (" + mdl + ")"
			}
			a.Firmware = hostfs.StringOr(f, d.Path+"/fw_version", "")
			a.BusyTimeUs = gpu.NPUBusyUs(f, d.Path)
		} else {
			a.Name = cards[d.Path]
		}
		// hwmon sensors attached to this device (amdgpu, nouveau, i915/xe dGPU, ...)
		for _, chip := range hw {
			if chip.Device != d.Path {
				continue
			}
			for _, s := range chip.Sensors {
				switch {
				case s.Kind == model.KindTemperature && s.Input != nil:
					sen := model.Sensor{Name: s.Label, Kind: s.Kind, Value: s.Input, Unit: model.UnitCelsius,
						Thresholds: check.TempThresholds(s.Thresholds(), check.Defaults{WarnTempC: 85, CritTempC: 95}), Source: chip.Name}
					check.EvalSensor(&sen)
					a.Temps = append(a.Temps, sen)
				case s.Kind == model.KindFan && s.Input != nil:
					a.Fans = append(a.Fans, model.Sensor{Name: s.Label, Kind: s.Kind, Value: s.Input, Unit: model.UnitRPM, Status: model.StatusOK, Source: chip.Name})
				case s.Kind == model.KindPower && s.Input != nil && a.PowerWatts == nil:
					a.PowerWatts = s.Input
				}
			}
			if v, err := hostfs.Uint(f, chip.Path+"/power1_average"); err == nil && a.PowerWatts == nil {
				a.PowerWatts = model.F(float64(v) / 1e6)
			}
			if v, err := hostfs.Uint(f, chip.Path+"/power1_cap"); err == nil && v > 0 {
				a.PowerCapWatts = model.F(float64(v) / 1e6)
			}
			if v, err := hostfs.Uint(f, chip.Path+"/freq1_input"); err == nil && v > 0 {
				a.ClockMHz = model.F(float64(v) / 1e6)
			}
		}
		if v, err := hostfs.Uint(f, d.Path+"/power/runtime_active_time"); err == nil {
			a.RuntimeActiveMs = &v
		}
		if len(a.Temps) == 0 && (a.Integrated || isNPU) {
			// Integrated GPUs/NPUs without their own sensor sit on the CPU
			// die: report its temperature, marked as shared.
			if sen, ok := dieTemp(hw, env.Opts.Thresholds); ok {
				a.Temps = append(a.Temps, sen)
			}
		}
		switch d.Driver {
		case "amdxdna":
			c.xrt(ctx, env, h, &a, d.Address, prevAccel(prev, d.Address))
		case "amdgpu":
			am := gpu.ReadAMD(f, d.Path)
			a.UtilizationPercent, a.MemTotalBytes, a.MemUsedBytes, a.Firmware = am.BusyPercent, am.VRAMTotal, am.VRAMUsed, am.VBIOS
			a.ECCCorrectable, a.ECCUncorrectable, a.RetiredPages = am.ECCCE, am.ECCUE, am.BadPages
			a.Serial, a.PartNumber = am.Serial, am.PartNumber
			if am.ProductName != "" {
				a.Model = am.ProductName + " (" + a.Model + ")"
			}
		case "i915", "xe":
			card := "/sys/class/drm/" + a.Name
			a.ClockMHz = gpu.IntelFreqMHz(f, card, d.Path)
		case "nvidia":
			if nv == nil {
				nv = c.nvidia(ctx, env, h)
			}
			g, ok := nv[d.Address]
			if ok {
				fillNvidia(&a, g)
			}
			nvidiaC2C(&a, g)
		}
		if a.MemTotalBytes == nil && (a.Integrated || a.Interconnect != "") {
			a.SharedMemory = true
			if b, err := f.ReadFile("/proc/meminfo"); err == nil {
				if m, err := sysfs.ParseMeminfo(b); err == nil {
					a.SharedMemBytes = model.U(m["MemTotal"])
				}
			}
		}
		if a.Serial == "" {
			if v, ok := pci.ReadVPD(f, d.Path); ok {
				a.Serial, a.PartNumber = v.Serial, v.PartNumber
			}
		}
		if p := prevAccel(prev, d.Address); p != nil && a.RuntimeActiveMs != nil && p.RuntimeActiveMs != nil && *a.RuntimeActiveMs >= *p.RuntimeActiveMs {
			if dt := env.Now().Sub(prev.CollectedAt).Milliseconds(); dt > 0 {
				a.AwakePercent = model.F(min(100, float64(*a.RuntimeActiveMs-*p.RuntimeActiveMs)/float64(dt)*100))
			}
		}
		a.SensorNote = sensorNote(&a)
		if prev != nil && a.BusyTimeUs != nil {
			for _, p := range prev.NPUs {
				if p.PCIAddress == a.PCIAddress && p.BusyTimeUs != nil && *a.BusyTimeUs >= *p.BusyTimeUs {
					if dt := env.Now().Sub(prev.CollectedAt).Microseconds(); dt > 0 {
						a.UtilizationPercent = model.F(min(100, float64(*a.BusyTimeUs-*p.BusyTimeUs)/float64(dt)*100))
					}
				}
			}
		}
		a.KernelErrors = len(gpuErrs[d.Address])
		if a.Kind == "npu" {
			a.Index = len(out.NPUs)
		} else {
			a.Index = len(out.GPUs)
		}
		a.Status = findings(h, &a, gpuErrs[d.Address])
		if a.Kind == "npu" {
			out.NPUs = append(out.NPUs, a)
		} else {
			out.GPUs = append(out.GPUs, a)
		}
	}
	if len(out.GPUs)+len(out.NPUs) == 0 {
		h.Summary = "no GPU or NPU"
		if len(out.BMCDisplays) > 0 {
			h.Summary += fmt.Sprintf(" (%d BMC/virtual display)", len(out.BMCDisplays))
		}
	} else {
		h.Summary = summary(out)
	}
	h.Rollup()
	return out, nil
}

func prevAccel(prev *model.Accel, addr string) *model.Accelerator {
	if prev == nil {
		return nil
	}
	for _, list := range [][]model.Accelerator{prev.GPUs, prev.NPUs} {
		for i := range list {
			if list[i].PCIAddress == addr {
				return &list[i]
			}
		}
	}
	return nil
}

// dieTemp returns the CPU die temperature (k10temp/zenpower Tdie or Tctl,
// coretemp package 0) as a shared sensor.
func dieTemp(hw []sysfs.HwmonChip, d check.Defaults) (model.Sensor, bool) {
	var best *sysfs.HwmonSensor
	var src string
	for _, c := range hw {
		for i := range c.Sensors {
			s := &c.Sensors[i]
			if s.Kind != model.KindTemperature || s.Input == nil {
				continue
			}
			switch {
			case (c.Name == "k10temp" || strings.HasPrefix(c.Name, "zenpower")) && (s.Label == "Tdie" || s.Label == "Tctl" && best == nil):
				best, src = s, c.Name
			case c.Name == "coretemp" && s.Label == "Package id 0" && best == nil:
				best, src = s, c.Name
			}
		}
	}
	if best == nil {
		return model.Sensor{}, false
	}
	label := best.Label
	if src == "coretemp" {
		label = "Package"
	}
	sen := model.Sensor{Name: "CPU die (" + label + ")", Kind: model.KindTemperature, Value: best.Input, Unit: model.UnitCelsius,
		Thresholds: check.TempThresholds(best.Thresholds(), d), Source: src, Shared: true}
	check.EvalSensor(&sen)
	return sen, true
}

func sensorNote(a *model.Accelerator) string {
	own := false
	for _, t := range a.Temps {
		own = own || !t.Shared
	}
	switch {
	case own:
		return ""
	case len(a.Temps) > 0:
		return "no own temperature sensor; shows the CPU die it is built into"
	case a.Driver == "amdxdna" || a.Driver == "intel_vpu":
		return a.Driver + " exposes no temperature sensor"
	}
	return "driver exposes no temperature sensor"
}

// xrt reads NPU power, power mode, AIE columns and running contexts with
// `xrt-smi examine` (read-only). Opening the device wakes a runtime-
// suspended NPU, so on refresh it only runs while the NPU is awake anyway;
// otherwise the static values are carried over.
func (c *Collector) xrt(ctx context.Context, env *collector.Env, h *model.Header, a *model.Accelerator, addr string, p *model.Accelerator) {
	if _, err := env.Exec.LookPath("xrt-smi"); err != nil {
		h.SetSource("xrt-smi", model.AvailUnavailable, "xrt-smi not installed (NPU power, power mode and contexts need AMD XRT)")
		return
	}
	if p != nil && a.RuntimeStatus != "active" {
		a.PowerMode, a.AIEColumns = p.PowerMode, p.AIEColumns
		if a.RuntimeStatus == "suspended" {
			a.HWContexts = model.I(0)
		}
		return
	}
	out, err := env.Exec.Run(ctx, "xrt-smi", "--batch", "examine", "-d", addr, "-r", "platform", "-r", "aie-partitions")
	if err != nil && len(out) == 0 {
		h.SetSource("xrt-smi", model.AvailUnavailable, err.Error())
		return
	}
	x := gpu.ParseXRTSMI(out)
	if x.PowerMode == "" && x.Columns == nil {
		h.SetSource("xrt-smi", model.AvailUnavailable, "no NPU platform report (no access to /dev/accel? needs the render group)")
		return
	}
	h.SetSource("xrt-smi", model.AvailOK, "")
	a.PowerMode, a.AIEColumns, a.HWContexts = x.PowerMode, x.Columns, x.Contexts
	if a.PowerWatts == nil {
		a.PowerWatts = x.PowerWatts
	}
}

// classLinks maps canonical device paths to class device names (card0, accel0).
func classLinks(f hostfs.FS, pattern string) map[string]string {
	out := map[string]string{}
	m, _ := f.Glob(pattern)
	sort.Strings(m)
	for _, p := range m {
		if dev, err := f.EvalSymlinks(p + "/device"); err == nil {
			if _, dup := out[dev]; !dup {
				out[dev] = path.Base(p)
			}
		}
	}
	return out
}

func (c *Collector) nvidia(ctx context.Context, env *collector.Env, h *model.Header) map[string]gpu.NvidiaGPU {
	out := map[string]gpu.NvidiaGPU{}
	if _, err := env.Exec.LookPath("nvidia-smi"); err != nil {
		h.SetSource("nvidia-smi", model.AvailUnavailable, "nvidia-smi not installed (NVIDIA driver exposes no health data in sysfs)")
		return out
	}
	b, err := env.Exec.Run(ctx, "nvidia-smi", "-q", "-x")
	if err != nil {
		h.SetSource("nvidia-smi", model.AvailUnavailable, err.Error())
		return out
	}
	gs, err := gpu.ParseNvidiaSMI(b)
	if err != nil {
		h.SetSource("nvidia-smi", model.AvailUnavailable, err.Error())
		return out
	}
	h.SetSource("nvidia-smi", model.AvailOK, "")
	for _, g := range gs {
		out[g.BusID] = g
	}
	return out
}

func fillNvidia(a *model.Accelerator, g gpu.NvidiaGPU) {
	a.Model, a.UUID, a.Firmware, a.DriverVersion = g.Product, g.UUID, g.VBIOS, g.DriverVersion
	a.Serial, a.PartNumber = g.Serial, g.BoardPart
	a.UtilizationPercent, a.MemTotalBytes, a.MemUsedBytes = g.UtilPercent, g.MemTotalBytes, g.MemUsedBytes
	a.PowerWatts, a.PowerCapWatts, a.ClockMHz = g.PowerW, g.PowerCapW, g.ClockMHz
	if g.TempC != nil {
		th := &model.Thresholds{Origin: "nvidia-smi", UpperWarn: g.TempSlowC, UpperCrit: g.TempMaxC}
		if g.TempSlowC == nil && g.TempMaxC == nil && g.TempTLimitC != nil && *g.TempTLimitC > 0 {
			// Blackwell reports T.Limit, the margin to slowdown, instead of
			// thresholds: at the limit the GPU throttles.
			lim := *g.TempC + *g.TempTLimitC
			th = &model.Thresholds{Origin: "nvidia-smi T.Limit", UpperWarn: model.F(lim - 5), UpperCrit: model.F(lim)}
		}
		s := model.Sensor{Name: "GPU", Kind: model.KindTemperature, Value: g.TempC, Unit: model.UnitCelsius,
			Thresholds: check.TempThresholds(th, check.Defaults{WarnTempC: 85, CritTempC: 95}), Source: "nvidia-smi"}
		check.EvalSensor(&s)
		a.Temps = append(a.Temps, s)
	}
	if g.MemTempC != nil {
		th := &model.Thresholds{Origin: "nvidia-smi", UpperCrit: g.MemTempMaxC}
		s := model.Sensor{Name: "Memory", Kind: model.KindTemperature, Value: g.MemTempC, Unit: model.UnitCelsius,
			Thresholds: check.TempThresholds(th, check.Defaults{WarnTempC: 85, CritTempC: 95}), Source: "nvidia-smi"}
		check.EvalSensor(&s)
		a.Temps = append(a.Temps, s)
	}
	if g.FanPercent != nil {
		a.Fans = append(a.Fans, model.Sensor{Name: "Fan", Kind: model.KindFan, Value: g.FanPercent, Unit: "percent", Status: model.StatusOK, Source: "nvidia-smi"})
	}
	a.ECCCorrectable, a.ECCUncorrectable = g.ECCVolatileCE, g.ECCVolatileUE
	if g.ECCAggregateUE != nil && (a.ECCUncorrectable == nil || *g.ECCAggregateUE > *a.ECCUncorrectable) {
		// keep lifetime UE visible even after a reset cleared the volatile counter
		a.ECCUncorrectable = g.ECCAggregateUE
	}
	a.RetiredPages, a.RemapPending, a.RemapFailed = g.RetiredPages, g.RemapPending, g.RemapFailed
	if g.RetirePending {
		t := true
		a.RemapPending = &t
	}
	a.ThrottleReasons = append(a.ThrottleReasons, g.ThrottleReasons...)
	for _, p := range g.Processes {
		a.Processes = append(a.Processes, model.GPUProcess{PID: p.PID, Type: p.Type, Name: p.Name, MemBytes: p.MemBytes})
	}
	sort.SliceStable(a.Processes, func(i, j int) bool {
		x, y := a.Processes[i].MemBytes, a.Processes[j].MemBytes
		return x != nil && (y == nil || *x > *y)
	})
	// nvidia-smi knows the trained link generation even when sysfs reads 2.5 GT/s at idle
	if g.LinkWidth != nil {
		a.LinkWidth = g.LinkWidth
	}
	if g.MaxLinkWidth != nil {
		a.MaxLinkWidth = g.MaxLinkWidth
	}
}

// grace matches the NVIDIA parts that sit next to a Grace CPU on
// NVLink-C2C: GB10 (DGX Spark), GH200, GB200, GB300.
var grace = regexp.MustCompile(`\bG[BH]\d{2,3}\b`)

// nvidiaC2C marks NVIDIA GPUs attached to the CPU over NVLink-C2C. Their
// PCIe function is a stub (GB10: x1 at 2.5 GT/s), so its link says nothing
// about the GPU. Recognised by name, by nvidia-smi's ATS addressing mode, or
// by a device that can do no more than PCIe Gen1, which no discrete NVIDIA
// GPU of the last fifteen years reports.
func nvidiaC2C(a *model.Accelerator, g gpu.NvidiaGPU) {
	if grace.MatchString(a.Model) || g.AddressingMode == "ATS" || g.C2CMode == "Enabled" || strings.HasPrefix(a.MaxLinkSpeed, "2.5 GT/s") {
		a.Interconnect = "NVLink-C2C"
		// GB10 puts the CPU and GPU dies in one package
		a.Integrated = strings.Contains(a.Model, "GB10")
		if a.MemTotalBytes == nil && g.ProcessMemBytes != nil {
			a.MemUsedBytes = g.ProcessMemBytes
		}
	}
}

func findings(h *model.Header, a *model.Accelerator, errs []kernellog.Entry) model.Status {
	st := model.StatusOK
	name := strings.ToUpper(a.Kind) + fmt.Sprint(a.Index) + " " + shortAddr(a.PCIAddress)
	add := func(s model.Status, code, msg string) {
		h.Find(s, code, a.PCIAddress, "accel", name+": "+msg)
		st = model.Worst(st, s)
	}
	if a.Driver == "" {
		add(model.StatusWarn, "no_driver", "no kernel driver bound")
	}
	for _, t := range a.Temps {
		if t.Status >= model.StatusWarn && !t.Shared {
			add(t.Status, "accel_temp", fmt.Sprintf("%s temperature %s", t.Name, check.FmtTemp(*t.Value)))
		}
	}
	if v := a.ECCUncorrectable; v != nil && *v > 0 {
		add(model.StatusCrit, "accel_ecc_ue", fmt.Sprintf("%d uncorrectable ECC errors", *v))
	}
	if v := a.ECCCorrectable; v != nil && *v > 0 {
		add(model.StatusWarn, "accel_ecc_ce", fmt.Sprintf("%d correctable ECC errors", *v))
	}
	if a.RemapFailed != nil && *a.RemapFailed {
		add(model.StatusCrit, "remap_failed", "row remapping failed (GPU needs replacement)")
	}
	if a.RemapPending != nil && *a.RemapPending {
		add(model.StatusWarn, "remap_pending", "memory row remap/retirement pending (reset the GPU)")
	}
	if v := a.RetiredPages; v != nil && *v > 0 {
		add(model.StatusWarn, "retired_pages", fmt.Sprintf("%d retired memory pages", *v))
	}
	if !a.Integrated && a.Interconnect == "" && a.LinkWidth != nil && a.MaxLinkWidth != nil && *a.LinkWidth < *a.MaxLinkWidth {
		add(model.StatusWarn, "pcie_width", fmt.Sprintf("PCIe link x%d, device supports x%d", *a.LinkWidth, *a.MaxLinkWidth))
	}
	var hwt []string
	for _, r := range a.ThrottleReasons {
		if hwThrottle[r] {
			hwt = append(hwt, r)
		}
	}
	if len(hwt) > 0 {
		add(model.StatusWarn, "accel_throttle", "throttling: "+strings.Join(hwt, ", "))
	}
	if len(errs) > 0 {
		worst := model.StatusOK
		for _, e := range errs {
			worst = model.Worst(worst, e.Severity())
		}
		last := errs[len(errs)-1].Message
		if len(last) > 80 {
			last = last[:80] + "…"
		}
		noun := "errors"
		if len(errs) == 1 {
			noun = "error"
		}
		add(worst, "accel_driver_errors", fmt.Sprintf("%d driver %s in kernel log, last: %s", len(errs), noun, last))
	}
	return st
}

func shortAddr(a string) string { return strings.TrimPrefix(a, "0000:") }

func summary(a *model.Accel) string {
	var parts []string
	var maxT *float64
	for _, list := range [][]model.Accelerator{a.GPUs, a.NPUs} {
		if len(list) == 0 {
			continue
		}
		ok := 0
		for _, x := range list {
			if x.Status == model.StatusOK {
				ok++
			}
			for _, t := range x.Temps {
				if t.Value != nil && !t.Shared && (maxT == nil || *t.Value > *maxT) {
					maxT = model.F(*t.Value)
				}
			}
		}
		kind := strings.ToUpper(list[0].Kind)
		parts = append(parts, fmt.Sprintf("%d/%d %ss OK", ok, len(list), kind))
	}
	if maxT != nil {
		parts = append(parts, "max "+check.FmtTemp(*maxT))
	}
	return strings.Join(parts, ", ")
}
