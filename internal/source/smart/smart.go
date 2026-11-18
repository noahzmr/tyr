// Package smart reads drive health: smartctl JSON (ATA/SCSI/NVMe), nvme-cli
// JSON, or the NVMe SMART/health log read directly with an admin ioctl.
package smart

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/noahzmr/tyr/internal/model"
)

// Info is everything a SMART source can tell about a drive.
type Info struct {
	SMART       model.SMART
	Model       string
	Serial      string
	Firmware    string
	SizeBytes   uint64
	RotationRPM *int
	TempC       *float64
	Protocol    string // ATA, SCSI, NVMe
}

func u(v uint64) *uint64   { return &v }
func f(v float64) *float64 { return &v }

// --- smartctl -j -------------------------------------------------------------

type ctlAttr struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Value      int    `json:"value"`
	Worst      int    `json:"worst"`
	Thresh     int    `json:"thresh"`
	WhenFailed string `json:"when_failed"`
	Raw        struct {
		Value  uint64 `json:"value"`
		String string `json:"string"`
	} `json:"raw"`
}

type ctlJSON struct {
	Smartctl struct {
		ExitStatus int `json:"exit_status"`
		Messages   []struct {
			String   string `json:"string"`
			Severity string `json:"severity"`
		} `json:"messages"`
	} `json:"smartctl"`
	Device struct {
		Protocol string `json:"protocol"`
	} `json:"device"`
	ModelName    string `json:"model_name"`
	ScsiModel    string `json:"scsi_model_name"`
	ScsiVendor   string `json:"scsi_vendor"`
	Serial       string `json:"serial_number"`
	Firmware     string `json:"firmware_version"`
	ScsiRevision string `json:"scsi_revision"`
	UserCapacity struct {
		Bytes uint64 `json:"bytes"`
	} `json:"user_capacity"`
	RotationRate *int `json:"rotation_rate"`
	SmartStatus  *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current *float64 `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours uint64 `json:"hours"`
	} `json:"power_on_time"`
	PowerCycleCount *uint64 `json:"power_cycle_count"`
	ATAAttrs        *struct {
		Table []ctlAttr `json:"table"`
	} `json:"ata_smart_attributes"`
	NVMe *struct {
		CriticalWarning  uint64  `json:"critical_warning"`
		Temperature      float64 `json:"temperature"`
		AvailableSpare   float64 `json:"available_spare"`
		AvailableSpareTh float64 `json:"available_spare_threshold"`
		PercentageUsed   float64 `json:"percentage_used"`
		DataUnitsRead    uint64  `json:"data_units_read"`
		DataUnitsWritten uint64  `json:"data_units_written"`
		PowerCycles      uint64  `json:"power_cycles"`
		PowerOnHours     uint64  `json:"power_on_hours"`
		UnsafeShutdowns  uint64  `json:"unsafe_shutdowns"`
		MediaErrors      uint64  `json:"media_errors"`
		NumErrLogEntries uint64  `json:"num_err_log_entries"`
	} `json:"nvme_smart_health_information_log"`
	LogicalBlockSize uint64 `json:"logical_block_size"`
	// ATA Device Statistics log (ACS-3, GP log 0x04), printed by -x: the
	// standard place for written sectors and the endurance indicator when
	// the attribute table leaves them out (vendor firmware often does).
	DevStats *struct {
		Pages []struct {
			Number int `json:"number"`
			Table  []struct {
				Name  string      `json:"name"`
				Value json.Number `json:"value"` // signed for temperatures; absent when not valid
				Flags struct {
					Valid bool `json:"valid"`
				} `json:"flags"`
			} `json:"table"`
		} `json:"pages"`
	} `json:"ata_device_statistics"`
	ATAErrorLog *struct {
		Extended *struct {
			Count *uint64 `json:"count"`
		} `json:"extended"`
		Summary *struct {
			Count *uint64 `json:"count"`
		} `json:"summary"`
	} `json:"ata_smart_error_log"`
	ScsiGrownDefects *uint64 `json:"scsi_grown_defect_list"`
	ScsiErrors       *struct {
		Read  scsiErrCounters `json:"read"`
		Write scsiErrCounters `json:"write"`
	} `json:"scsi_error_counter_log"`
	ScsiEndurance *float64 `json:"scsi_percentage_used_endurance_indicator"`
	ScsiStartStop *struct {
		Cycles *uint64 `json:"accumulated_start_stop_cycles"`
	} `json:"scsi_start_stop_cycle_counter"`
}

type scsiErrCounters struct {
	Uncorrected uint64 `json:"total_uncorrected_errors"`
	GB          any    `json:"gigabytes_processed"` // "1234.567" (a string in most smartctl versions)
}

// bytes is the data volume the counter log has processed, in bytes.
func (c scsiErrCounters) bytes() *uint64 {
	var gb float64
	switch v := c.GB.(type) {
	case string:
		x, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return nil
		}
		gb = x
	case float64:
		gb = v
	default:
		return nil
	}
	if gb <= 0 {
		return nil
	}
	return u(uint64(gb * 1e9))
}

// smartctl exit status bits (see smartctl(8) "RETURN VALUES").
const (
	ctlBitOpenFailed   = 1 << 1
	ctlBitDiskFailing  = 1 << 3
	ctlBitSelfTestErrs = 1 << 7
)

// ParseSmartctl parses `smartctl -j -x <dev>`. smartctl exits non-zero for
// warnings, so callers pass stdout even when the command "failed".
func ParseSmartctl(data []byte) (*Info, error) {
	var j ctlJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("smartctl json: %w", err)
	}
	if j.Smartctl.ExitStatus&ctlBitOpenFailed != 0 {
		msg := "device open failed"
		if len(j.Smartctl.Messages) > 0 {
			msg = j.Smartctl.Messages[0].String
		}
		return nil, errors.New("smartctl: " + msg)
	}
	in := &Info{Model: firstNonEmpty(j.ModelName, strings.TrimSpace(j.ScsiVendor+" "+j.ScsiModel)), Serial: j.Serial,
		Firmware: firstNonEmpty(j.Firmware, j.ScsiRevision), SizeBytes: j.UserCapacity.Bytes, Protocol: j.Device.Protocol}
	s := &in.SMART
	s.Source = "smartctl"
	if j.RotationRate != nil && *j.RotationRate > 0 {
		in.RotationRPM = j.RotationRate
	}
	if j.SmartStatus != nil {
		p := j.SmartStatus.Passed
		s.Passed = &p
	}
	if j.Smartctl.ExitStatus&ctlBitDiskFailing != 0 {
		p := false
		s.Passed = &p
	}
	s.SelfTestFailed = j.Smartctl.ExitStatus&ctlBitSelfTestErrs != 0
	if j.Temperature != nil && j.Temperature.Current != nil {
		in.TempC = j.Temperature.Current
	}
	if j.PowerOnTime != nil {
		s.PowerOnHours = u(j.PowerOnTime.Hours)
	}
	s.PowerCycles = j.PowerCycleCount
	if j.ATAAttrs != nil {
		parseATAAttrs(j, s)
	}
	parseDevStats(j, s)
	if e := j.ATAErrorLog; e != nil {
		switch {
		case e.Extended != nil && e.Extended.Count != nil:
			s.ErrorLogEntries = e.Extended.Count
		case e.Summary != nil && e.Summary.Count != nil:
			s.ErrorLogEntries = e.Summary.Count
		}
	}
	if n := j.NVMe; n != nil {

		s.NVMeCriticalWarn = u(n.CriticalWarning)
		s.AvailableSpare, s.AvailableSpareThr = f(n.AvailableSpare), f(n.AvailableSpareTh)
		s.WearPercentUsed = f(n.PercentageUsed)
		s.MediaErrors, s.ErrorLogEntries, s.UnsafeShutdowns = u(n.MediaErrors), u(n.NumErrLogEntries), u(n.UnsafeShutdowns)
		s.PowerOnHours, s.PowerCycles = u(n.PowerOnHours), u(n.PowerCycles)
		s.DataReadBytes, s.DataWrittenBytes = u(n.DataUnitsRead*512000), u(n.DataUnitsWritten*512000)
		if in.TempC == nil && n.Temperature > 0 {
			in.TempC = f(n.Temperature)
		}
	}
	s.GrownDefects = j.ScsiGrownDefects
	if e := j.ScsiErrors; e != nil {
		s.UncorrectedErrors = u(e.Read.Uncorrected + e.Write.Uncorrected)
		if s.DataWrittenBytes == nil {
			s.DataWrittenBytes = e.Write.bytes()
		}
		if s.DataReadBytes == nil {
			s.DataReadBytes = e.Read.bytes()
		}
	}
	if s.PowerCycles == nil && j.ScsiStartStop != nil {
		s.PowerCycles = j.ScsiStartStop.Cycles
	}
	if j.ScsiEndurance != nil {
		s.WearPercentUsed = j.ScsiEndurance
	}
	return in, nil
}

// wearAttrs count down from 100 (remaining life), most specific first; 173
// (wear leveling / average erase count) is the last resort.
var wearAttrs = []int{231, 233, 177, 202, 169, 173}

func parseATAAttrs(j ctlJSON, s *model.SMART) {
	byID := map[int]ctlAttr{}
	for _, a := range j.ATAAttrs.Table {
		raw := a.Raw.Value
		byID[a.ID] = a
		s.Attributes = append(s.Attributes, model.SMARTAttr{ID: a.ID, Name: a.Name, Value: a.Value, Worst: a.Worst,
			Thresh: a.Thresh, Raw: raw, RawString: strings.TrimSpace(a.Raw.String), WhenFailed: a.WhenFailed})
		switch a.ID {
		case 5:
			s.Reallocated = u(raw & 0xffffffff) // some vendors pack extra data in high bytes
		case 187:
			s.ReportedUncorrect = u(raw & 0xffffffff)
		case 197:
			s.Pending = u(raw & 0xffffffff)
		case 198:
			s.OfflineUncorrect = u(raw & 0xffffffff)
		case 199:
			s.CRCErrors = u(raw & 0xffffffff)
		}
		if a.WhenFailed == "now" {
			p := false
			s.Passed = &p
		}
	}
	for _, id := range wearAttrs {
		if a, ok := byID[id]; ok && a.Value > 0 && a.Value <= 100 {
			s.WearPercentUsed = f(float64(100 - a.Value))
			break
		}
	}
	// reserved blocks left, normalized to 100 (Intel 170/232, Kioxia and
	// others 180); the attribute's threshold is the drive's own limit
	for _, id := range []int{232, 170, 180} {
		if a, ok := byID[id]; ok && a.Value > 0 && a.Value <= 100 {
			s.AvailableSpare = f(float64(a.Value))
			if a.Thresh > 0 {
				s.AvailableSpareThr = f(float64(a.Thresh))
			}
			break
		}
	}
	for _, id := range []int{241, 246, 225} {
		if a, ok := byID[id]; ok {
			if b := writtenBytes(a, j.LogicalBlockSize); b != nil {
				s.DataWrittenBytes = b
				break
			}
		}
	}
	if a, ok := byID[242]; ok && strings.Contains(strings.ToLower(a.Name), "read") {
		s.DataReadBytes = writtenBytes(a, j.LogicalBlockSize)
	}
}

// writtenBytes converts a host-writes attribute by the unit its name gives.
func writtenBytes(a ctlAttr, lbs uint64) *uint64 {
	n, raw := strings.ToLower(a.Name), a.Raw.Value
	if raw == 0 {
		return nil
	}
	switch {
	case strings.Contains(n, "32mib"):
		return u(raw << 25)
	case strings.Contains(n, "gib") || strings.Contains(n, "_gb"):
		return u(raw << 30)
	case strings.Contains(n, "lba") || strings.Contains(n, "sector"):
		if lbs == 0 {
			lbs = 512
		}
		return u(raw * lbs)
	}
	return nil
}

// parseDevStats fills what the attribute table did not give from the ATA
// Device Statistics log.
func parseDevStats(j ctlJSON, s *model.SMART) {
	if j.DevStats == nil {
		return
	}
	lbs := j.LogicalBlockSize
	if lbs == 0 {
		lbs = 512
	}
	for _, p := range j.DevStats.Pages {
		for _, e := range p.Table {
			if !e.Flags.Valid || e.Value == "" {
				continue
			}
			v, err := strconv.ParseUint(string(e.Value), 10, 64)
			if err != nil {
				continue
			}
			set := func(dst **uint64, x uint64) {
				if *dst == nil {
					*dst = u(x)
				}
			}
			switch e.Name {
			case "Percentage Used Endurance Indicator":
				s.WearPercentUsed = f(float64(v)) // the standard value beats a vendor attribute
			case "Logical Sectors Written":
				set(&s.DataWrittenBytes, v*lbs)
			case "Logical Sectors Read":
				set(&s.DataReadBytes, v*lbs)
			case "Power-on Hours":
				set(&s.PowerOnHours, v)
			case "Lifetime Power-On Resets":
				set(&s.PowerCycles, v)
			case "Number of Reported Uncorrectable Errors":
				set(&s.ReportedUncorrect, v)
			case "Number of Interface CRC Errors":
				set(&s.CRCErrors, v)
			}
		}
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// --- nvme-cli -----------------------------------------------------------------

// ParseNVMeCLI parses `nvme smart-log <dev> -o json` (nvme-cli 1.x and 2.x).
func ParseNVMeCLI(data []byte) (*Info, error) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("nvme-cli json: %w", err)
	}
	num := func(k string) (float64, bool) {
		switch v := m[k].(type) {
		case float64:
			return v, true
		case string: // large counters may be strings in nvme-cli 2.x
			var x float64
			if _, err := fmt.Sscan(strings.ReplaceAll(v, ",", ""), &x); err == nil {
				return x, true
			}
		}
		return 0, false
	}
	in := &Info{Protocol: "NVMe"}
	s := &in.SMART
	s.Source = "nvme-cli"
	cw, ok := num("critical_warning")
	if !ok {
		return nil, errors.New("nvme-cli: no critical_warning in output")
	}
	s.NVMeCriticalWarn = u(uint64(cw))
	if t, ok := num("temperature"); ok {
		if t > 200 { // Kelvin
			t -= 273
		}
		in.TempC = f(t)
	}
	if v, ok := num("avail_spare"); ok {
		s.AvailableSpare = f(v)
	}
	if v, ok := num("spare_thresh"); ok {
		s.AvailableSpareThr = f(v)
	}
	for _, k := range []string{"percent_used", "percentage_used"} {
		if v, ok := num(k); ok {
			s.WearPercentUsed = f(v)
		}
	}
	set := func(dst **uint64, k string, mul uint64) {
		if v, ok := num(k); ok {
			*dst = u(uint64(v) * mul)
		}
	}
	set(&s.MediaErrors, "media_errors", 1)
	set(&s.ErrorLogEntries, "num_err_log_entries", 1)
	set(&s.UnsafeShutdowns, "unsafe_shutdowns", 1)
	set(&s.PowerOnHours, "power_on_hours", 1)
	set(&s.PowerCycles, "power_cycles", 1)
	set(&s.DataReadBytes, "data_units_read", 512000)
	set(&s.DataWrittenBytes, "data_units_written", 512000)
	return in, nil
}

// --- NVMe admin ioctl ---------------------------------------------------------

// ParseNVMeLog decodes the 512-byte SMART / Health Information log page (0x02).
func ParseNVMeLog(b []byte) (*Info, error) {
	if len(b) < 512 {
		return nil, fmt.Errorf("nvme smart log: %d bytes, want 512", len(b))
	}
	le := binary.LittleEndian
	lo := func(off int) uint64 { return le.Uint64(b[off:]) } // low 64 bits of a 128-bit counter
	in := &Info{Protocol: "NVMe"}
	s := &in.SMART
	s.Source = "nvme_ioctl"
	s.NVMeCriticalWarn = u(uint64(b[0]))
	if k := le.Uint16(b[1:]); k > 0 {
		in.TempC = f(float64(k) - 273)
	}
	s.AvailableSpare, s.AvailableSpareThr, s.WearPercentUsed = f(float64(b[3])), f(float64(b[4])), f(float64(b[5]))
	s.DataReadBytes, s.DataWrittenBytes = u(lo(32)*512000), u(lo(48)*512000)
	s.PowerCycles, s.PowerOnHours, s.UnsafeShutdowns = u(lo(112)), u(lo(128)), u(lo(144))
	s.MediaErrors, s.ErrorLogEntries = u(lo(160)), u(lo(176))
	return in, nil
}

// nvmeAdminCmd mirrors struct nvme_admin_cmd (linux/nvme_ioctl.h), 72 bytes.
type nvmeAdminCmd struct {
	Opcode      uint8
	Flags       uint8
	Rsvd1       uint16
	NSID        uint32
	Cdw2, Cdw3  uint32
	Metadata    uint64
	Addr        uint64
	MetadataLen uint32
	DataLen     uint32
	Cdw10       uint32
	Cdw11       uint32
	Cdw12       uint32
	Cdw13       uint32
	Cdw14       uint32
	Cdw15       uint32
	TimeoutMs   uint32
	Result      uint32
}

// nvmeIoctlAdminCmd = _IOWR('N', 0x41, struct nvme_admin_cmd)
const nvmeIoctlAdminCmd = 0xC0484E41

// ReadNVMeLog reads the SMART log from an NVMe controller character device
// (/dev/nvme0) with a Get Log Page admin command. Requires CAP_SYS_ADMIN.
// It is read-only: opcode 0x02 never modifies the device.
func ReadNVMeLog(ctrlDev string) ([]byte, error) {
	if unsafe.Sizeof(nvmeAdminCmd{}) != 72 {
		return nil, errors.New("nvme: admin command struct has wrong size")
	}
	fd, err := unix.Open(ctrlDev, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	buf := make([]byte, 512)
	numd := uint32(len(buf)/4 - 1)
	cmd := nvmeAdminCmd{
		Opcode: 0x02, NSID: 0xffffffff,
		Addr: uint64(uintptr(unsafe.Pointer(&buf[0]))), DataLen: uint32(len(buf)),
		Cdw10: numd<<16 | 0x02, TimeoutMs: 5000,
	}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), nvmeIoctlAdminCmd, uintptr(unsafe.Pointer(&cmd)))
	runtime.KeepAlive(buf)
	if errno != 0 {
		return nil, errno
	}
	return buf, nil
}
