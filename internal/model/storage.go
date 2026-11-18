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
	// Remote is the initiator side of network block storage: every iSCSI
	// session and NVMe-over-Fabrics controller, what the kernel and iscsid
	// logged about them, and the node checks for Longhorn / Kubernetes.
	Remote *RemoteStorage `json:"remote_storage,omitempty"`
	// Longhorn is set on a Longhorn node: its data disks, replicas and logs.
	Longhorn *Longhorn `json:"longhorn,omitempty"`
	// Crypt are the dm-crypt mappings (LUKS, plain, BitLocker) of this host.
	Crypt []CryptDevice `json:"crypt_devices,omitempty"`
	// Swap are the active swap areas and whether they are encrypted.
	Swap []SwapArea `json:"swap,omitempty"`
}

// Encryption is whether data at rest on a disk, volume or filesystem is
// encrypted, and by what. Protected is true only when the key is not
// simply there for anyone who has the drive: LUKS, ZFS with a key, a
// self-encrypting drive with locking enabled, controller encryption.
type Encryption struct {
	Protected bool   `json:"protected"`
	Method    string `json:"method,omitempty"` // "LUKS2 aes-xts-plain64 512-bit", "Opal 2 SED", "ZFS aes-256-gcm", "HPE Smart Array"
	Via       string `json:"via,omitempty"`    // the layer that does it: dm-0 (luks-…), sda, the controller
	Note      string `json:"note,omitempty"`   // "locked", "media encryption without locking: no protection at rest"
}

// SED is what a drive's TCG Storage discovery and ATA security report.
type SED struct {
	SSC              string `json:"ssc,omitempty"` // Opal 2, Opal 1, Enterprise, Pyrite 2, Ruby, Opalite
	LockingSupported bool   `json:"locking_supported"`
	LockingEnabled   bool   `json:"locking_enabled"` // a locking range is set up with a password
	Locked           bool   `json:"locked"`
	MediaEncryption  bool   `json:"media_encryption"` // the drive encrypts everything it stores
	MBREnabled       bool   `json:"mbr_shadow,omitempty"`
	ATASecurity      string `json:"ata_security,omitempty"` // "enabled, frozen" (smartctl)
	Source           string `json:"source,omitempty"`       // opal ioctl, nvme security receive, smartctl
}

// CryptDevice is one dm-crypt mapping.
type CryptDevice struct {
	Name    string   `json:"name"`             // the mapping: luks-3f2…, cryptroot
	DM      string   `json:"dm"`               // dm-0
	Type    string   `json:"type"`             // LUKS1, LUKS2, PLAIN, BITLK, TCRYPT
	Cipher  string   `json:"cipher,omitempty"` // aes-xts-plain64 (from the LUKS header, root)
	KeyBits int      `json:"key_bits,omitempty"`
	Backing []string `json:"backing"`           // sda3, md0, sdc (a Longhorn volume)
	UsedBy  []string `json:"used_by,omitempty"` // mountpoints, swap, LVM
}

// SwapArea is one active swap device or file.
type SwapArea struct {
	Path       string      `json:"path"`
	Type       string      `json:"type"` // partition, file
	SizeBytes  uint64      `json:"size_bytes"`
	Encryption *Encryption `json:"encryption,omitempty"`
}

// FSHealth is what the filesystem itself recorded about errors.
type FSHealth struct {
	Errors      *uint64 `json:"errors"` // ext4 errors_count; btrfs device error counters
	FirstError  string  `json:"first_error,omitempty"`
	LastError   string  `json:"last_error,omitempty"`
	LastChecked string  `json:"last_checked,omitempty"` // ext: last fsck
	State       string  `json:"state,omitempty"`        // ext: clean, not clean, errors
	Source      string  `json:"source,omitempty"`
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
	IOErrorsTyr *uint64 `json:"io_errors_tyr,omitempty"`
	IOErrorsNew *uint64 `json:"io_errors_new,omitempty"`
	// IOErrorsShared rose on every disk behind the same controller at the
	// same moment, without kernel I/O errors: commands of a monitoring
	// agent (smartd, HPE amsd) the controller rejects, not disk errors.
	IOErrorsShared *uint64     `json:"io_errors_controller,omitempty"`
	KernelErrors   int         `json:"kernel_log_errors"`
	KernelLast     string      `json:"kernel_log_last,omitempty"`  // the latest kernel I/O error line
	Remote         *RemoteDisk `json:"remote,omitempty"`           // iSCSI / FC volume of a storage system
	Location       string      `json:"location,omitempty"`         // controller (driver) · port · bay
	KernelLines    []string    `json:"kernel_log_lines,omitempty"` // the latest kernel log lines that name the disk (or its session)
	SMARTAt        time.Time   `json:"smart_at,omitzero"`          // when SMART was read (it is cached)
	Holders        []string    `json:"holders,omitempty"`          // md/dm devices using it
	SED            *SED        `json:"sed,omitempty"`
	Encryption     *Encryption `json:"encryption,omitempty"` // nil: not known
	Mounted        []string    `json:"mountpoints,omitempty"`
	SMART          *SMART      `json:"smart,omitempty"`
	Status         Status      `json:"status"`
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

// RAIDController is a hardware RAID controller (storcli/perccli), or one
// the BMC reports over Redfish (HPE Smart Array and other Redfish Storage).
type RAIDController struct {
	Index    int    `json:"index"`
	Model    string `json:"model"`
	Serial   string `json:"serial,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	Location string `json:"location,omitempty"` // Redfish: "Slot 12", "Embedded RAID"
	State    string `json:"state"`
	Tool     string `json:"tool"` // storcli, perccli, redfish
	// Encryption is the controller's own encryption (HPE Secure Encryption): "enabled", "disabled", "" not reported.
	Encryption string          `json:"encryption,omitempty"`
	VDs        []VirtualDrive  `json:"virtual_drives"`
	PDs        []PhysicalDrive `json:"physical_drives"`
	Battery    *RAIDBattery    `json:"battery,omitempty"`
	Status     Status          `json:"status"`
}

// VirtualDrive is a hardware RAID logical volume.
type VirtualDrive struct {
	ID       string `json:"id"` // "0/0" (DG/VD)
	Name     string `json:"name,omitempty"`
	Level    string `json:"level"`
	State    string `json:"state"` // Optl, Dgrd, Pdgd, OfLn, Rec
	SizeText string `json:"size"`
	// Encrypted by the controller (HPE Secure Encryption, Redfish Volume.Encrypted); nil: not reported.
	Encrypted *bool  `json:"encrypted,omitempty"`
	Status    Status `json:"status"`
}

// PhysicalDrive is a disk behind a RAID controller.
type PhysicalDrive struct {
	Slot      string `json:"slot"`  // EID:Slt (storcli), Port:Box:Bay "1I:1:3" (Redfish)
	State     string `json:"state"` // Onln, Offln, UGood, UBad, Rbld, Failed, GHS, DHS, JBOD; Redfish: OK, Warning, Critical
	Group     string `json:"group,omitempty"`
	SizeText  string `json:"size"`
	SizeBytes uint64 `json:"size_bytes,omitempty"`
	Interface string `json:"interface,omitempty"`
	Media     string `json:"media,omitempty"`
	Model     string `json:"model,omitempty"`
	Serial    string `json:"serial,omitempty"`
	WWN       string `json:"wwn,omitempty"`
	Firmware  string `json:"firmware,omitempty"`
	// From the BMC (Redfish): what the controller knows about the drive's health.
	LifeLeftPercent  *float64 `json:"life_left_percent,omitempty"` // SSD endurance left
	PowerOnHours     *uint64  `json:"power_on_hours,omitempty"`
	TempCelsius      *float64 `json:"temp_celsius,omitempty"`
	FailurePredicted bool     `json:"failure_predicted,omitempty"`
	SED              string   `json:"sed,omitempty"`            // storcli SED column, Redfish EncryptionAbility
	Encryption       string   `json:"encryption,omitempty"`     // Redfish EncryptionStatus: Unencrypted, Unlocked, Locked, Foreign
	Reasons          []string `json:"status_reasons,omitempty"` // HPE DiskDriveStatusReasons, Redfish Status.Conditions
	Status           Status   `json:"status"`
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
	Mountpoint        string      `json:"mountpoint"`
	Source            string      `json:"source"`
	FSType            string      `json:"fstype"`
	ReadOnly          bool        `json:"read_only"`
	SizeBytes         uint64      `json:"size_bytes"`
	UsedBytes         uint64      `json:"used_bytes"`
	UsedPercent       *float64    `json:"used_percent"`
	InodesUsedPercent *float64    `json:"inodes_used_percent"`
	Encryption        *Encryption `json:"encryption,omitempty"`
	Health            *FSHealth   `json:"health,omitempty"`
	Status            Status      `json:"status"`
}

// RemoteDisk is the path to a network block device.
type RemoteDisk struct {
	Transport string `json:"transport"`                 // iscsi, fc
	Target    string `json:"target,omitempty"`          // iSCSI IQN, FC WWPN
	Portal    string `json:"portal,omitempty"`          // iSCSI address:port
	Session   string `json:"session,omitempty"`         // session3, rport-2:0-1, nvme2
	State     string `json:"state,omitempty"`           // LOGGED_IN / FAILED (iSCSI), Online / Blocked (FC), live / connecting (NVMe-oF)
	Longhorn  string `json:"longhorn_volume,omitempty"` // the Longhorn volume (pvc-…) this LUN is
}

// RemoteStorage is this host as an initiator of network block storage.
type RemoteStorage struct {
	Initiator string          `json:"iscsi_initiator,omitempty"` // InitiatorName from /etc/iscsi/initiatorname.iscsi
	HostNQN   string          `json:"nvme_hostnqn,omitempty"`    // /etc/nvme/hostnqn
	Sessions  []RemoteSession `json:"sessions"`
	// Events is the timeline of the current boot: logins, connection
	// errors, recoveries, offline devices, filesystems gone read-only.
	Events []RemoteEvent `json:"events,omitempty"`
	// Checks are the node checks (open-iscsi, iscsid, multipathd, Longhorn's
	// prerequisites) - run on hosts with sessions or Longhorn / Kubernetes.
	Checks []NodeCheck `json:"checks,omitempty"`
	// Logs are the errors of the last 24 hours that the kubelet (k3s,
	// rke2), the CSI plugin, Longhorn, iscsid and multipathd logged about
	// volumes, folded with their repeats.
	Logs []NodeLogEntry `json:"logs,omitempty"`
}

// RemoteSession is one iSCSI session or NVMe-oF controller.
type RemoteSession struct {
	Transport string   `json:"transport"`            // iscsi, nvme-tcp, nvme-rdma, nvme-fc
	ID        string   `json:"id"`                   // session3, nvme2
	Target    string   `json:"target,omitempty"`     // IQN / subsystem NQN
	Portal    string   `json:"portal,omitempty"`     // address:port
	Local     string   `json:"local_addr,omitempty"` // the initiator's address
	Interface string   `json:"interface,omitempty"`  // the network interface the portal is reached through
	State     string   `json:"state,omitempty"`      // LOGGED_IN / FAILED / FREE; live / connecting / resetting
	ConnState string   `json:"conn_state,omitempty"` // iSCSI connection: up / down / failed
	Disks     []string `json:"disks,omitempty"`      // sdc, nvme2n1
	// RecoveryTimeout is how long the kernel holds I/O while the path is
	// down before it fails it (iSCSI replacement_timeout, NVMe ctrl_loss_tmo).
	RecoveryTimeout *int   `json:"recovery_timeout_s,omitempty"`
	HeaderDigest    string `json:"header_digest,omitempty"`
	DataDigest      string `json:"data_digest,omitempty"`
	Longhorn        string `json:"longhorn_volume,omitempty"` // a Longhorn volume's engine, not a storage system
	// from the kernel log and iscsid since boot
	ConnErrors       int       `json:"conn_errors"`
	Recoveries       int       `json:"recoveries"`        // the connection came back
	RecoveryTimeouts int       `json:"recovery_timeouts"` // the kernel gave up and failed the I/O
	DigestErrors     int       `json:"digest_errors"`
	LastError        string    `json:"last_error,omitempty"`
	LastErrorAt      time.Time `json:"last_error_at,omitzero"`
	Status           Status    `json:"status"`
}

// Gone reports a session that is in the log but no longer in sysfs.
func (s RemoteSession) Gone() bool { return s.State == "gone" }

// RemoteEvent is one line of the network storage timeline.
type RemoteEvent struct {
	Time     time.Time `json:"time,omitzero"`
	Session  string    `json:"session,omitempty"` // session3, nvme2
	Disk     string    `json:"disk,omitempty"`
	Kind     string    `json:"kind"` // login, logout, conn_error, ping_timeout, recovered, recovery_timeout, offline_io, io_error, fs_error, fs_readonly, reconnect, digest_error
	Source   string    `json:"source"`
	Severity Status    `json:"severity"`
	Message  string    `json:"message"`
}

// NodeCheck is one prerequisite of iSCSI / Longhorn on this node.
type NodeCheck struct {
	ID     string `json:"id"`    // iscsid, iscsi_tcp, initiator_name, multipathd, nfs_client, dm_crypt, nvme_tcp, hugepages, …
	Scope  string `json:"scope"` // iscsi, longhorn, longhorn-v2
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Status Status `json:"status"` // OK, or how bad the missing piece is here
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// Longhorn is what a Longhorn node shows locally, without the Kubernetes
// API: its data disks, the replicas on them, the volumes attached here and
// the errors Longhorn, the CSI plugin and the kubelet logged.
type Longhorn struct {
	Kubernetes string      `json:"kubernetes,omitempty"` // k3s, rke2, kubelet
	Evidence   []string    `json:"evidence"`             // why Tyr thinks this is a Longhorn node
	Engine     string      `json:"engine,omitempty"`     // v1, v2, v1+v2: the data engines seen in use
	Volumes    []LHVolume  `json:"volumes,omitempty"`
	Disks      []LHDisk    `json:"disks,omitempty"`
	Replicas   []LHReplica `json:"replicas,omitempty"`
}

// LHVolume is a Longhorn volume attached to this node.
type LHVolume struct {
	Name     string   `json:"name"`             // pvc-…
	Device   string   `json:"device,omitempty"` // sdc, nvme3n1
	Session  string   `json:"session,omitempty"`
	Mounts   []string `json:"mountpoints,omitempty"`
	ReadOnly bool     `json:"read_only"` // mounted read-only (after a filesystem error)
	// Encrypted: a Longhorn encrypted volume (dm-crypt on the LUN).
	Encrypted bool   `json:"encrypted,omitempty"`
	Status    Status `json:"status"`
}

// LHDisk is a Longhorn data path (a directory with longhorn-disk.cfg).
type LHDisk struct {
	Path        string   `json:"path"`
	UUID        string   `json:"uuid,omitempty"`
	Device      string   `json:"device,omitempty"` // the filesystem's source
	Disks       []string `json:"disks,omitempty"`  // the physical disks under it
	FSType      string   `json:"fstype,omitempty"`
	SizeBytes   uint64   `json:"size_bytes"`
	UsedPercent *float64 `json:"used_percent"`
	Replicas    int      `json:"replicas"`
	Status      Status   `json:"status"`
}

// LHReplica is a replica directory on one of the data disks.
type LHReplica struct {
	Dir        string `json:"dir"`
	Volume     string `json:"volume"`
	Disk       string `json:"disk"` // data path
	SizeBytes  uint64 `json:"size_bytes"`
	Rebuilding bool   `json:"rebuilding"`
	Error      string `json:"error,omitempty"`
}

// NodeLogEntry is a log line of Longhorn, the CSI plugin, the kubelet,
// iscsid or multipathd, folded with its repeats.
type NodeLogEntry struct {
	Time    time.Time `json:"time,omitzero"` // the latest
	Source  string    `json:"source"`        // kubelet, longhorn-manager, instance-manager, longhorn-csi-plugin, iscsid, multipathd
	Level   string    `json:"level"`         // error, warning
	Message string    `json:"message"`
	Count   int       `json:"count"`
	Volume  string    `json:"volume,omitempty"`
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
			if d.IOErrorsShared != nil {
				v -= min(v, *d.IOErrorsShared)
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
