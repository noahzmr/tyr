// Package memory collects memory capacity, DIMM inventory, ECC errors (EDAC),
// poisoned pages, NUMA balance and DIMM temperatures.
package memory

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/dmi"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

// Collector is the memory collector.
type Collector struct{}

// New returns a memory collector.
func New() *Collector { return &Collector{} }

func (*Collector) ID() model.ModuleID { return model.ModMemory }

func (c *Collector) Collect(ctx context.Context, env *collector.Env, prevSec model.Section) (model.Section, error) {
	var prev *model.Memory
	if p, ok := prevSec.(*model.Memory); ok && p != nil {
		prev = p
	}
	f := env.FS
	out := &model.Memory{DIMMs: []model.DIMM{}, EDAC: []model.EDACController{}, NUMANodes: []model.NUMANode{}, Temps: []model.Sensor{}}
	h := &out.Header

	b, err := f.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, fmt.Errorf("reading /proc/meminfo: %w", err)
	}
	mi, err := sysfs.ParseMeminfo(b)
	if err != nil {
		return nil, err
	}
	h.SetSource("meminfo", model.AvailOK, "")
	out.TotalBytes, out.AvailableBytes = mi["MemTotal"], mi["MemAvailable"]
	if out.AvailableBytes == 0 { // kernels < 3.14
		out.AvailableBytes = mi["MemFree"] + mi["Buffers"] + mi["Cached"]
	}
	if out.TotalBytes > out.AvailableBytes {
		out.UsedBytes = out.TotalBytes - out.AvailableBytes
	}
	out.SwapTotalBytes = mi["SwapTotal"]
	if mi["SwapTotal"] > mi["SwapFree"] {
		out.SwapUsedBytes = mi["SwapTotal"] - mi["SwapFree"]
	}
	out.HugePagesTotal, out.HugePageSizeBytes = mi["HugePages_Total"], mi["Hugepagesize"]
	out.HardwareCorruptedBytes = mi["HardwareCorrupted"]

	if blocks, err := sysfs.ReadMemoryBlocks(f); err == nil {
		out.OnlineBytes = model.U(blocks.OnlineBytes())
		out.OfflineBlocks = blocks.Offline
		if blocks.Offline > 0 {
			h.Find(model.StatusWarn, "memory_offline", "memory", "sysfs",
				fmt.Sprintf("%d memory blocks offline (%s)", blocks.Offline, gib(uint64(blocks.Offline)*blocks.BlockSize)))
		}
	}
	for _, n := range sysfs.ReadNodeMemory(f) {
		out.NUMANodes = append(out.NUMANodes, model.NUMANode{ID: n.ID, TotalBytes: n.TotalBytes, FreeBytes: n.FreeBytes})
	}

	// --- DIMM inventory (SMBIOS), only on full collections ---------------
	if prev == nil {
		tables, st := env.DMI(ctx)
		h.Sources = append(h.Sources, st)
		if tables != nil {
			inventory(out, tables)
		}
	} else {
		carryInventory(out, prev)
	}

	// --- EDAC ---------------------------------------------------------------
	c.edac(env, out)

	// --- DIMM temperatures (jc42 = DDR4 TSOD, spd5118 = DDR5 hub) -----------
	for _, chip := range sysfs.ReadHwmon(f) {
		if chip.Name != "jc42" && chip.Name != "spd5118" {
			continue
		}
		for _, s := range chip.Sensors {
			if s.Kind != model.KindTemperature || s.Input == nil {
				continue
			}
			name := fmt.Sprintf("DIMM %s %s", chip.Name, lastPathElem(chip.Device))
			sen := model.Sensor{Name: name, Kind: model.KindTemperature, Value: s.Input, Unit: model.UnitCelsius,
				Thresholds: check.TempThresholds(s.Thresholds(), dimmDefaults(env.Opts.Thresholds)), Source: chip.Name}
			check.EvalSensor(&sen)
			if s.CritAlarm {
				sen.Status = model.StatusCrit
			}
			out.Temps = append(out.Temps, sen)
			if sen.Status >= model.StatusWarn {
				h.Find(sen.Status, "dimm_temp", name, chip.Name, fmt.Sprintf("%s at %s", name, check.FmtTemp(*s.Input)))
			}
		}
	}
	if len(out.Temps) > 0 {
		h.SetSource("dimm_temperature", model.AvailOK, "via hwmon")
	} else if !env.IsVM() {
		if sens, st := env.IPMISensors(ctx); sens != nil {
			ipmiDIMMTemps(out, sens, env.Opts.Thresholds)
			if len(out.Temps) > 0 {
				h.SetSource("dimm_temperature", model.AvailOK, "via ipmi")
			} else {
				h.SetSource("dimm_temperature", model.AvailUnavailable, "no DIMM temperature sensors in hwmon or BMC")
			}
		} else {
			h.SetSource("dimm_temperature", model.AvailUnavailable, "no jc42/spd5118 hwmon sensors; BMC: "+st.Reason)
		}
	} else {
		h.SetSource("dimm_temperature", model.AvailNotApplicable, "virtual machine ("+env.Virt+")")
	}

	// --- memory events in the BMC event log -----------------------------------
	if !env.IsVM() {
		sel, st := env.SEL(ctx)
		h.Sources = append(h.Sources, st)
		if sel != nil {
			selFindings(out, sel)
		}
	}

	findings(out)
	for i := range out.DIMMs {
		out.DIMMs[i].Status = dimmStatus(&out.DIMMs[i])
	}

	size := out.TotalBytes
	if out.InstalledBytes != nil {
		size = *out.InstalledBytes
	}
	h.Summary = gib(size)
	switch {
	case out.CorrectableErrors != nil:
		n := *out.CorrectableErrors + *out.UncorrectableErrors
		h.Summary += fmt.Sprintf(", %d ECC errors", n)
	default:
		h.Summary += ", ECC n/a"
	}
	h.Rollup()
	return out, nil
}

// DIMM temps run cooler than CPUs; JEDEC TSOD defaults are 85 °C (high) and
// 95 °C (critical) when the sensor does not report its own limits.
func dimmDefaults(d check.Defaults) check.Defaults {
	if d.WarnTempC < 85 {
		d.WarnTempC = 85
	}
	if d.CritTempC < 95 {
		d.CritTempC = 95
	}
	return d
}

func lastPathElem(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func inventory(out *model.Memory, t *dmi.Tables) {
	arrays := t.MemoryArrays()
	var maxCap uint64
	for _, a := range arrays {
		maxCap += a.MaxCapacity
		if out.ECCType == "" || a.ErrorCorrection != "None" {
			out.ECCType = a.ErrorCorrection
		}
	}
	if maxCap > 0 {
		out.MaxCapacityBytes = model.U(maxCap)
	}
	var installed uint64
	for _, d := range t.MemoryDevices() {
		m := model.DIMM{
			Locator: d.Locator, BankLocator: d.BankLocator, Populated: d.Populated, SizeBytes: d.SizeBytes,
			Type: d.Type, FormFactor: d.FormFactor, SpeedMTs: d.SpeedMTs, ConfiguredSpeedMTs: d.ConfiguredMTs,
			Rank: d.Rank, Manufacturer: d.Manufacturer, PartNumber: strings.TrimSpace(d.PartNumber), Serial: d.Serial, AssetTag: d.AssetTag,
		}
		if d.Populated && d.TotalWidth != nil && d.DataWidth != nil {
			ecc := d.ECC()
			m.ECC = &ecc
		}
		m.Channel = parseLocator(d.Locator, d.BankLocator).group()
		out.Slots++
		if d.Populated {
			out.PopulatedSlots++
			installed += d.SizeBytes
		}
		out.DIMMs = append(out.DIMMs, m)
	}
	if installed > 0 {
		out.InstalledBytes = model.U(installed)
	}
}

func carryInventory(out, prev *model.Memory) {
	out.InstalledBytes, out.ECCType, out.MaxCapacityBytes = prev.InstalledBytes, prev.ECCType, prev.MaxCapacityBytes
	out.Slots, out.PopulatedSlots = prev.Slots, prev.PopulatedSlots
	out.DIMMs = make([]model.DIMM, len(prev.DIMMs))
	for i, d := range prev.DIMMs {
		d.CorrectableErrors, d.UncorrectableErrors, d.EDACLabel, d.TempCelsius = nil, nil, "", nil
		out.DIMMs[i] = d
	}
	if st, ok := prev.Source("dmidecode"); ok {
		out.Sources = append(out.Sources, st)
	}
}

func (c *Collector) edac(env *collector.Env, out *model.Memory) {
	h := &out.Header
	mcs, err := sysfs.ReadEDAC(env.FS)
	switch {
	case env.IsVM():
		h.SetSource("edac", model.AvailNotApplicable, "virtual machine ("+env.Virt+")")
		return
	case err != nil:
		h.SetSource("edac", model.AvailUnavailable, err.Error())
		return
	case len(mcs) == 0:
		reason := "no EDAC memory controller driver loaded"
		if out.ECCType == "None" {
			h.SetSource("edac", model.AvailNotApplicable, "memory without ECC")
			return
		}
		if strings.Contains(out.ECCType, "ECC") {
			h.Find(model.StatusOK, "edac_missing", "edac", "edac",
				"ECC memory present but no EDAC driver loaded: ECC errors are not visible to Linux")
		}
		h.SetSource("edac", model.AvailUnavailable, reason)
		return
	}
	h.SetSource("edac", model.AvailOK, "")
	var ce, ue uint64
	matcher := newDIMMMatcher(out.DIMMs, mcs)
	for _, mc := range mcs {
		m := model.EDACController{Name: mc.Name, Type: mc.Type, SizeBytes: mc.SizeBytes,
			CorrectableErrors: mc.CE, UncorrectableErrors: mc.UE, CorrectableNoInfo: mc.CENoInfo, UncorrectableNoInfo: mc.UENoInfo,
			DIMMs: []model.EDACDIMM{}}
		ce += mc.CE + mc.CENoInfo
		ue += mc.UE + mc.UENoInfo
		for _, d := range mc.DIMMs {
			ed := model.EDACDIMM{Name: d.Name, Label: d.Label, Location: d.Location, SizeBytes: d.SizeBytes,
				CorrectableErrors: d.CE, UncorrectableErrors: d.UE}
			if i := matcher.match(d); i >= 0 {
				dm := &out.DIMMs[i]
				ed.Locator = dm.Locator
				dm.EDACLabel = d.Label
				dm.CorrectableErrors = model.U(d.CE)
				dm.UncorrectableErrors = model.U(d.UE)
			}
			m.DIMMs = append(m.DIMMs, ed)
		}
		out.EDAC = append(out.EDAC, m)
	}
	// Controller totals can exceed the per-DIMM sums (noinfo counts, csrow
	// drivers); ce/ue are the totals the hardware reported.
	out.CorrectableErrors, out.UncorrectableErrors = model.U(ce), model.U(ue)
}

func findings(out *model.Memory) {
	h := &out.Header
	if out.HardwareCorruptedBytes > 0 {
		h.Find(model.StatusCrit, "hardware_corrupted", "memory", "meminfo",
			fmt.Sprintf("%s of memory poisoned by the kernel (HardwareCorrupted)", kib(out.HardwareCorruptedBytes)))
	}
	// ECC errors per DIMM, then whatever the controller counted that no DIMM
	// accounts for (noinfo counts, csrow-only drivers).
	for _, mc := range out.EDAC {
		var mappedCE, mappedUE uint64
		for _, d := range mc.DIMMs {
			name := d.Locator
			if name == "" {
				name = firstNonEmpty(d.Label, mc.Name+"/"+d.Name)
			}
			mappedCE += d.CorrectableErrors
			mappedUE += d.UncorrectableErrors
			if d.UncorrectableErrors > 0 {
				h.Find(model.StatusCrit, "ecc_uncorrectable", name, "edac", fmt.Sprintf("%d uncorrectable ECC %s on %s", d.UncorrectableErrors, plural(int(d.UncorrectableErrors), "error", "errors"), name))
			}
			if d.CorrectableErrors > 0 {
				h.Find(model.StatusWarn, "ecc_correctable", name, "edac", fmt.Sprintf("%d correctable ECC %s on %s", d.CorrectableErrors, plural(int(d.CorrectableErrors), "error", "errors"), name))
			}
		}
		if rest := mc.UncorrectableErrors + mc.UncorrectableNoInfo - min(mappedUE, mc.UncorrectableErrors+mc.UncorrectableNoInfo); rest > 0 {
			h.Find(model.StatusCrit, "ecc_uncorrectable", mc.Name, "edac", fmt.Sprintf("%d uncorrectable ECC errors on %s (DIMM unknown)", rest, mc.Name))
		}
		if rest := mc.CorrectableErrors + mc.CorrectableNoInfo - min(mappedCE, mc.CorrectableErrors+mc.CorrectableNoInfo); rest > 0 {
			h.Find(model.StatusWarn, "ecc_correctable", mc.Name, "edac", fmt.Sprintf("%d correctable ECC errors on %s (DIMM unknown)", rest, mc.Name))
		}
	}

	// Installed vs. seen by the OS. Online blocks include kernel memory; allow
	// for firmware/iGPU carve-outs: max(1 GiB, 3 %).
	if out.InstalledBytes != nil && out.OnlineBytes != nil {
		inst, seen := *out.InstalledBytes, *out.OnlineBytes
		tol := max(uint64(1)<<30, inst*3/100)
		if seen+tol < inst {
			h.Find(model.StatusWarn, "capacity_mismatch", "memory", "dmidecode",
				fmt.Sprintf("%s installed, OS sees %s", gib(inst), gib(seen)))
		}
	}

	speedFindings(out)
	mixedPartFindings(out)

	// NUMA balance: a node with clearly less memory often means a DIMM or
	// channel was mapped out by the BIOS.
	if len(out.NUMANodes) > 1 {
		var big uint64
		for _, n := range out.NUMANodes {
			big = max(big, n.TotalBytes)
		}
		for _, n := range out.NUMANodes {
			if n.TotalBytes > 0 && float64(n.TotalBytes) < 0.9*float64(big) {
				h.Find(model.StatusWarn, "numa_imbalance", fmt.Sprintf("node%d", n.ID), "sysfs",
					fmt.Sprintf("NUMA node %d has %s, largest node has %s", n.ID, gib(n.TotalBytes), gib(big)))
			}
		}
	}
}

func speedFindings(out *model.Memory) {
	h := &out.Header
	configured := map[int][]string{}
	var below []string
	for _, d := range out.DIMMs {
		if !d.Populated || d.ConfiguredSpeedMTs == nil {
			continue
		}
		configured[*d.ConfiguredSpeedMTs] = append(configured[*d.ConfiguredSpeedMTs], d.Locator)
		if d.SpeedMTs != nil && *d.ConfiguredSpeedMTs < *d.SpeedMTs {
			below = append(below, d.Locator)
		}
	}
	if len(configured) > 1 {
		speeds := make([]int, 0, len(configured))
		for s := range configured {
			speeds = append(speeds, s)
		}
		sort.Ints(speeds)
		slow := configured[speeds[0]]
		h.Find(model.StatusWarn, "dimm_speed_mismatch", strings.Join(slow, ","), "dmidecode",
			fmt.Sprintf("DIMMs run at different speeds: %s at %d MT/s, others up to %d MT/s", abbreviate(slow, 3), speeds[0], speeds[len(speeds)-1]))
		return
	}
	if len(below) > 0 {
		// Uniformly below rated speed: platform or population limit (e.g. 2 DIMMs per channel).
		var speed int
		for s := range configured {
			speed = s
		}
		h.Find(model.StatusOK, "dimm_speed_below_rated", "memory", "dmidecode",
			fmt.Sprintf("all DIMMs run at %d MT/s, below their rated speed (platform or population limit)", speed))
	}
}

func mixedPartFindings(out *model.Memory) {
	h := &out.Header
	byGroup := map[string]map[string]bool{}
	var order []string
	for _, d := range out.DIMMs {
		if !d.Populated || d.PartNumber == "" || d.Channel == "" {
			continue
		}
		if byGroup[d.Channel] == nil {
			byGroup[d.Channel] = map[string]bool{}
			order = append(order, d.Channel)
		}
		byGroup[d.Channel][d.PartNumber] = true
	}
	for _, g := range order {
		if parts := byGroup[g]; len(parts) > 1 {
			ps := make([]string, 0, len(parts))
			for p := range parts {
				ps = append(ps, p)
			}
			sort.Strings(ps)
			h.Find(model.StatusWarn, "mixed_part_numbers", g, "dmidecode",
				fmt.Sprintf("mixed DIMM part numbers in channel %s (%s)", g, strings.Join(ps, ", ")))
		}
	}
	all := map[string]bool{}
	for _, d := range out.DIMMs {
		if d.Populated && d.PartNumber != "" {
			all[d.PartNumber] = true
		}
	}
	if len(all) > 1 && !slices.ContainsFunc(h.Findings, func(f model.Finding) bool { return f.Code == "mixed_part_numbers" }) {
		h.Find(model.StatusOK, "mixed_part_numbers_system", "memory", "dmidecode",
			fmt.Sprintf("%d different DIMM part numbers installed", len(all)))
	}
}

func dimmStatus(d *model.DIMM) model.Status {
	switch {
	case d.UncorrectableErrors != nil && *d.UncorrectableErrors > 0:
		return model.StatusCrit
	case d.CorrectableErrors != nil && *d.CorrectableErrors > 0:
		return model.StatusWarn
	}
	return model.StatusOK
}

func gib(b uint64) string {
	g := float64(b) / (1 << 30)
	switch {
	case g >= 1024 && b%(1<<40) == 0:
		return fmt.Sprintf("%d TB", b>>40)
	case g >= 10 || b%(1<<30) == 0:
		return fmt.Sprintf("%.0f GB", g)
	default:
		return fmt.Sprintf("%.1f GB", g)
	}
}

func kib(b uint64) string {
	if b >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	}
	return fmt.Sprintf("%d kB", b>>10)
}

func abbreviate(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:n], ", ") + fmt.Sprintf(" +%d more", len(items)-n)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
