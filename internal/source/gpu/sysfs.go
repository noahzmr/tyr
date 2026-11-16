package gpu

import (
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/hostfs"
)

// AMDInfo is amdgpu data from the PCI device directory.
type AMDInfo struct {
	BusyPercent *float64
	VRAMTotal   *uint64
	VRAMUsed    *uint64
	VBIOS       string
	ECCCE       *uint64
	ECCUE       *uint64
	BadPages    *uint64
	ProductName string
	PartNumber  string
	Serial      string
}

// ReadAMD reads amdgpu attributes (works without root).
func ReadAMD(f hostfs.FS, dev string) AMDInfo {
	var a AMDInfo
	if v, err := hostfs.Uint(f, dev+"/gpu_busy_percent"); err == nil {
		x := float64(v)
		a.BusyPercent = &x
	}
	if v, err := hostfs.Uint(f, dev+"/mem_info_vram_total"); err == nil {
		a.VRAMTotal = &v
	}
	if v, err := hostfs.Uint(f, dev+"/mem_info_vram_used"); err == nil {
		a.VRAMUsed = &v
	}
	a.VBIOS = hostfs.StringOr(f, dev+"/vbios_version", "")
	// FRU EEPROM data (Instinct, Radeon Pro): product_name/number, serial_number
	a.ProductName = hostfs.StringOr(f, dev+"/product_name", "")
	a.PartNumber = hostfs.StringOr(f, dev+"/product_number", "")
	a.Serial = hostfs.StringOr(f, dev+"/serial_number", "")
	if a.Serial == "" {
		a.Serial = hostfs.StringOr(f, dev+"/unique_id", "")
	}
	// RAS (Instinct/Radeon Pro): umc_err_count = "ue: N\nce: M"
	if s, err := hostfs.String(f, dev+"/ras/umc_err_count"); err == nil {
		for _, l := range strings.Split(s, "\n") {
			k, v, ok := strings.Cut(l, ":")
			if !ok {
				continue
			}
			n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			if err != nil {
				continue
			}
			switch strings.TrimSpace(k) {
			case "ue":
				a.ECCUE = &n
			case "ce":
				a.ECCCE = &n
			}
		}
	}
	// gpu_vram_bad_pages: one line per retired page "0x... : 0x... : R|P|F"
	if b, err := f.ReadFile(dev + "/ras/gpu_vram_bad_pages"); err == nil {
		var n uint64
		for _, l := range strings.Split(string(b), "\n") {
			if strings.Count(l, ":") >= 2 {
				n++
			}
		}
		a.BadPages = &n
	}
	return a
}

// IntelFreqMHz returns the current GT frequency for i915 (gt_cur_freq_mhz
// on the DRM card) or xe (tile0/gt0/freq0/act_freq on the device).
func IntelFreqMHz(f hostfs.FS, card, dev string) *float64 {
	for _, p := range []string{card + "/gt_cur_freq_mhz", card + "/gt/gt0/rps_cur_freq_mhz", dev + "/tile0/gt0/freq0/act_freq"} {
		if v, err := hostfs.Uint(f, p); err == nil {
			x := float64(v)
			return &x
		}
	}
	return nil
}

// NPUBusyUs returns intel_vpu's cumulative busy time in microseconds.
func NPUBusyUs(f hostfs.FS, dev string) *uint64 {
	if v, err := hostfs.Uint(f, dev+"/npu_busy_time_us"); err == nil {
		return &v
	}
	return nil
}
