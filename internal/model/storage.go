package model

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Storage is the storage module section.
type Storage struct {
	Header
	Disks       []Disk           `json:"disks"`
	MDArrays    []MDArray        `json:"md_arrays"`
	Controllers []RAIDController `json:"raid_controllers"`
	Filesystems []Filesystem     `json:"filesystems"`
}

// Disk is one physical (or virtual, in VMs) block device.
type Disk struct {
	Name          string      `json:"name"` // sda, nvme0n1
	Path          string      `json:"path"` // /dev/sda
	Model         string      `json:"model,omitempty"`
	Vendor        string      `json:"vendor,omitempty"`
	Serial        string      `json:"serial,omitempty"`
	WWN           string      `json:"wwn,omitempty"`
	Firmware      string      `json:"firmware,omitempty"`
	SizeBytes     uint64      `json:"size_bytes"`
	Kind          string      `json:"kind"`      // nvme, ssd, hdd, virtual, usb, iscsi, fc, unknown
	Interface     string      `json:"interface"` // nvme, sata, sas, usb, virtio, scsi, mmc
	RotationRPM   *int        `json:"rotation_rpm"`
	Removable     bool        `json:"removable"`
	ReadOnly      bool        `json:"read_only"`
	TempCelsius   *float64    `json:"temp_celsius"`
	TempThreshold *Thresholds `json:"temp_thresholds,omitempty"`
	IOErrors      *uint64     `json:"io_errors"` // SCSI ioerr_cnt: commands that failed or were rejected since boot
	// IOErrorsTyr is the part of IOErrors this Tyr process caused itself:
	// SMART queries the device or controller rejects. IOErrorsNew are the
	// errors that appeared since Tyr started without its queries.
	IOErrorsTyr  *uint64     `json:"io_errors_tyr,omitempty"`
	IOErrorsNew  *uint64     `json:"io_errors_new,omitempty"`
	KernelErrors int         `json:"kernel_log_errors"`
	KernelLast   string      `json:"kernel_log_last,omitempty"` // the latest kernel I/O error line
	Remote       *RemoteDisk `json:"remote,omitempty"`          // iSCSI / FC volume of a storage system
	SMARTAt      time.Time   `json:"smart_at,omitzero"`         // when SMART was read (it is cached)
	Holders      []string    `json:"holders,omitempty"`         // md/dm devices using it
	Mounted      []string    `json:"mountpoints,omitempty"`
	SMART        *SMART      `json:"smart,omitempty"`
	Status       Status      `json:"status"`
}

// SMART is drive self-monitoring data (ATA attributes or NVMe health log).
type SMART struct {
	Source            string   `json:"source"` // nvme_ioctl, smartctl, nvme-cli
	Passed            *bool    `json:"passed"`
	PowerOnHours      *uint64  `json:"power_on_hours"`
	PowerCycles       *uint64  `json:"power_cycles"`
	Reallocated       *uint64  `json:"reallocated_sectors"`
	Pending           *uint64  `json:"pending_sectors"`
	OfflineUncorrect  *uint64  `json:"offline_uncorrectable"`
	ReportedUncorrect *uint64  `json:"reported_uncorrectable"`
	CRCErrors         *uint64  `json:"crc_errors"`
	WearPercentUsed   *float64 `json:"wear_percent_used"` // 0 = new, 100 = rated endurance used
	NVMeCriticalWarn  *uint64  `json:"nvme_critical_warning"`
	AvailableSpare    *float64 `json:"available_spare_percent"`
	AvailableSpareThr *float64 `json:"available_spare_threshold_percent"`
	MediaErrors       *uint64  `json:"media_errors"`
	ErrorLogEntries   *uint64  `json:"error_log_entries"`
	UnsafeShutdowns   *uint64  `json:"unsafe_shutdowns"`
	DataWrittenBytes  *uint64  `json:"data_written_bytes"`
	DataReadBytes     *uint64  `json:"data_read_bytes"`
	SelfTestFailed    bool     `json:"self_test_failed"`
	GrownDefects      *uint64  `json:"scsi_grown_defects"`
	UncorrectedErrors *uint64  `json:"scsi_uncorrected_errors"`
	// Attributes is the full ATA SMART attribute table (smartctl).
	Attributes []SMARTAttr `json:"attributes,omitempty"`
}

// SMARTAttr is one ATA SMART attribute. Value/Worst/Thresh are the
// normalized vendor values (higher is better; failing at <= Thresh).
type SMARTAttr struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Value      int    `json:"value"`
	Worst      int    `json:"worst"`
	Thresh     int    `json:"thresh"`
	Raw        uint64 `json:"raw"`
	RawString  string `json:"raw_string,omitempty"` // as smartctl prints it ("21 (Min/Max 18/40)")
	WhenFailed string `json:"when_failed,omitempty"`
}

// MDArray is a Linux software RAID array.
type MDArray struct {
	Name          string     `json:"name"` // md0
	Level         string     `json:"level"`
	State         string     `json:"state"` // active, inactive, ...
	Devices       int        `json:"raid_devices"`
	ActiveDevices int        `json:"active_devices"`
	Degraded      bool       `json:"degraded"`
	Members       []MDMember `json:"members"`
	SizeBytes     uint64     `json:"size_bytes"`
	SyncAction    string     `json:"sync_action,omitempty"` // resync, recover, check, reshape
	SyncPercent   *float64   `json:"sync_percent"`
	SyncFinish    string     `json:"sync_finish,omitempty"`
	Status        Status     `json:"status"`
}

// MDMember is one member device of an md array.
type MDMember struct {
	Device string `json:"device"`
	Role   int    `json:"role"`
	Failed bool   `json:"failed"`
	Spare  bool   `json:"spare"`
}

// RAIDController is a hardware RAID controller (storcli/perccli).
type RAIDController struct {
	Index    int             `json:"index"`
	Model    string          `json:"model"`
	Serial   string          `json:"serial,omitempty"`
	Firmware string          `json:"firmware,omitempty"`
	State    string          `json:"state"`
	Tool     string          `json:"tool"`
	VDs      []VirtualDrive  `json:"virtual_drives"`
	PDs      []PhysicalDrive `json:"physical_drives"`
	Battery  *RAIDBattery    `json:"battery,omitempty"`
	Status   Status          `json:"status"`
}

// VirtualDrive is a hardware RAID logical volume.
type VirtualDrive struct {
	ID       string `json:"id"` // "0/0" (DG/VD)
	Name     string `json:"name,omitempty"`
	Level    string `json:"level"`
	State    string `json:"state"` // Optl, Dgrd, Pdgd, OfLn, Rec
	SizeText string `json:"size"`
	Status   Status `json:"status"`
}

// PhysicalDrive is a disk behind a RAID controller.
type PhysicalDrive struct {
	Slot      string `json:"slot"`  // EID:Slt
	State     string `json:"state"` // Onln, Offln, UGood, UBad, Rbld, Failed, GHS, DHS, JBOD
	Group     string `json:"group,omitempty"`
	SizeText  string `json:"size"`
	Interface string `json:"interface,omitempty"`
	Media     string `json:"media,omitempty"`
	Model     string `json:"model,omitempty"`
	Serial    string `json:"serial,omitempty"`
	WWN       string `json:"wwn,omitempty"`
	Firmware  string `json:"firmware,omitempty"`
	Status    Status `json:"status"`
}

// RAIDBattery is a BBU or CacheVault.
type RAIDBattery struct {
	Type        string   `json:"type"` // BBU, CacheVault
	Model       string   `json:"model,omitempty"`
	State       string   `json:"state"`
	TempCelsius *float64 `json:"temp_celsius"`
	Status      Status   `json:"status"`
}

// Filesystem is a mounted local filesystem.
type Filesystem struct {
	Mountpoint        string   `json:"mountpoint"`
	Source            string   `json:"source"`
	FSType            string   `json:"fstype"`
	ReadOnly          bool     `json:"read_only"`
	SizeBytes         uint64   `json:"size_bytes"`
	UsedBytes         uint64   `json:"used_bytes"`
	UsedPercent       *float64 `json:"used_percent"`
	InodesUsedPercent *float64 `json:"inodes_used_percent"`
	Status            Status   `json:"status"`
}

// RemoteDisk is the path to a network block device.
type RemoteDisk struct {
	Transport string `json:"transport"`         // iscsi, fc
	Target    string `json:"target,omitempty"`  // iSCSI IQN, FC WWPN
	Portal    string `json:"portal,omitempty"`  // iSCSI address:port
	Session   string `json:"session,omitempty"` // session3, rport-2:0-1
	State     string `json:"state,omitempty"`   // LOGGED_IN / FAILED (iSCSI), Online / Blocked (FC)
}

// Local reports a disk in this host (not an iSCSI or FC volume).
func (d Disk) Local() bool { return d.Remote == nil }

func (s *Storage) counters(c map[string]uint64) {
	for _, d := range s.Disks {
		p := "disk." + d.Name + "."
		if d.IOErrors != nil { // without the errors Tyr's own SMART queries caused
			v := *d.IOErrors
			if d.IOErrorsTyr != nil {
				v -= min(v, *d.IOErrorsTyr)
			}
			c[p+"io_errors"] = v
		}
		c[p+"kernel_errors"] = uint64(d.KernelErrors)
		if m := d.SMART; m != nil {
			for k, v := range map[string]*uint64{"reallocated": m.Reallocated, "pending": m.Pending, "offline_uncorrectable": m.OfflineUncorrect,
				"reported_uncorrectable": m.ReportedUncorrect, "crc": m.CRCErrors, "media_errors": m.MediaErrors,
				"error_log": m.ErrorLogEntries, "grown_defects": m.GrownDefects, "scsi_uncorrected": m.UncorrectedErrors} {
				if v != nil {
					c[p+"smart."+k] = *v
				}
			}
		}
	}
}

// NVMeWarningText decodes the NVMe SMART critical warning bits,
// e.g. "0x05 (reliability degraded, spare below threshold)".
func NVMeWarningText(w uint64) string {
	var parts []string
	for bit, s := range map[uint64]string{1: "spare below threshold", 2: "temperature", 4: "reliability degraded", 8: "read-only", 16: "volatile backup failed", 32: "PMR read-only"} {
		if w&bit != 0 {
			parts = append(parts, s)
		}
	}
	sort.Strings(parts)
	return fmt.Sprintf("0x%02x (%s)", w, strings.Join(parts, ", "))
}
