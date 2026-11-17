// Package explain says what a finding means: a plain explanation, the
// usual causes, and the commands that show where the problem is. The TUI
// shows it next to a finding and on component pages; reports list it for
// every kind of alert they contain.
package explain

import (
	"sort"
	"strings"

	"github.com/noahzmr/tyr/internal/model"
)

// Entry explains one kind of finding. Check commands may contain {dev}:
// the finding's component (sdd, eno1, cpu7, P2-DIMMB1, 0000:ca:00.0).
type Entry struct {
	Title   string
	Meaning string
	Causes  []string
	Check   []string
}

// For returns the explanation of a finding code in a module.
func For(mod model.ModuleID, code string) (Entry, bool) {
	e, ok := entries[string(mod)+"/"+code]
	return e, ok
}

// Known is one catalog entry with its module and code.
type Known struct {
	Module model.ModuleID
	Code   string
	Entry
}

// All lists the catalog by module (in display order), then title.
func All() []Known {
	order := map[model.ModuleID]int{}
	for i, m := range append([]model.ModuleID{model.ModSystem}, model.Modules...) {
		order[m] = i
	}
	var out []Known
	for k, e := range entries {
		mod, code, _ := strings.Cut(k, "/")
		out = append(out, Known{Module: model.ModuleID(mod), Code: code, Entry: e})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Module != out[j].Module {
			return order[out[i].Module] < order[out[j].Module]
		}
		return out[i].Title < out[j].Title
	})
	return out
}

// Commands returns the check commands with {dev} replaced by subject.
// A finding about several components ("cpu22,cpu46") gets each command
// once per component (at most four), never "cpu22,cpu46" in a path.
func (e Entry) Commands(subject string) []string {
	subs := strings.Split(subject, ",")
	if len(subs) > 4 {
		subs = subs[:4]
	}
	var out []string
	for _, c := range e.Check {
		if !strings.Contains(c, "{dev}") {
			out = append(out, c)
			continue
		}
		for _, s := range subs {
			out = append(out, strings.ReplaceAll(c, "{dev}", strings.TrimSpace(s)))
		}
	}
	return out
}

var disk = []string{"journalctl -k -b | grep -w {dev}", "smartctl -x /dev/{dev}"}

var entries = map[string]Entry{
	// --- storage ------------------------------------------------------------
	"storage/kernel_io_errors": {
		Title:   "Reads or writes failed (kernel log)",
		Meaning: "The kernel sent a read or write to the disk and it failed even after retries. Data in the affected sectors may be unreadable; a RAID array hides this until another disk fails.",
		Causes: []string{"failing media: look at pending and reallocated sectors", "a loose or damaged cable, backplane slot or connector: look at CRC errors",
			"the controller, its firmware or driver", "power: a weak PSU or a bad power connector"},
		Check: []string{"journalctl -k -b | grep -w {dev}", "smartctl -x /dev/{dev}", "smartctl -l error /dev/{dev}", "cat /sys/block/{dev}/device/ioerr_cnt"},
	},
	"storage/io_errors": {
		Title: "SCSI command errors keep growing",
		Meaning: "ioerr_cnt counts every SCSI command that ended with an error, including commands the drive or controller rejects. Tyr leaves out the errors its own SMART " +
			"queries cause; these appeared while Tyr was running and came from somewhere else.",
		Causes: []string{"another tool sending commands the controller rejects (smartd, a vendor agent)", "real read or write errors (then the kernel log shows them too)"},
		Check:  []string{"journalctl -k -b | grep -w {dev}", "systemctl is-active smartd", "cat /sys/block/{dev}/device/ioerr_cnt   # twice, a minute apart"},
	},
	"storage/crc_errors": {
		Title: "Interface CRC errors (cable or backplane)",
		Meaning: "Data was corrupted on the way between the disk and the controller and had to be sent again. The disk's media is not affected, the connection is. " +
			"A few over a disk's life usually come from a hot-plug or a reseat.",
		Causes: []string{"a loose or damaged SATA/SAS cable", "a bad backplane slot or connector", "reseating or hot-plugging the drive (a one-time increase)"},
		Check: []string{"smartctl -A /dev/{dev} | grep -i crc   # does it grow?", "reseat the drive; if it keeps growing, move it to another bay: " +
			"errors that follow the drive point to the drive, errors that stay with the bay point to the backplane or cable"},
	},
	"storage/pending_sectors": {
		Title:   "Sectors the disk could not read",
		Meaning: "The disk failed to read these sectors and will replace them with spares the next time they are written. Until then the data in them is lost.",
		Causes:  []string{"media wear or damage", "a head problem (on HDDs)"},
		Check:   append(disk, "smartctl -t long /dev/{dev}   # then smartctl -l selftest"),
	},
	"storage/reallocated_sectors": {
		Title:   "Sectors replaced with spares",
		Meaning: "The disk found bad sectors and moved them to its spare area. A small stable number is tolerable; a growing number means the media is deteriorating.",
		Causes:  []string{"media wear or damage"},
		Check:   append(disk, "compare the count with an earlier report (tyr history)"),
	},
	"storage/offline_uncorrectable": {Title: "Sectors unreadable in the background scan", Meaning: "The disk's own offline scan found sectors it cannot read or correct.",
		Causes: []string{"media damage"}, Check: disk},
	"storage/reported_uncorrectable": {Title: "Errors the disk could not correct", Meaning: "The disk reported reads its error correction could not repair.",
		Causes: []string{"media damage"}, Check: disk},
	"storage/media_errors": {Title: "NVMe media and data integrity errors", Meaning: "The NVMe drive detected unrecovered data errors (ECC, end-to-end protection).",
		Causes: []string{"worn or failing flash"}, Check: []string{"nvme smart-log /dev/{dev}", "nvme error-log /dev/{dev}"}},
	"storage/scsi_uncorrected": {Title: "Uncorrected read or write errors (SCSI)", Meaning: "The drive's error counter log shows reads or writes it could not correct.",
		Causes: []string{"media damage"}, Check: []string{"smartctl -x /dev/{dev}"}},
	"storage/grown_defects": {Title: "Grown defects (SCSI)", Meaning: "Defects the drive found after it left the factory. A growing list means the media is deteriorating.",
		Causes: []string{"media wear or damage"}, Check: []string{"smartctl -x /dev/{dev}"}},
	"storage/smart_failed": {Title: "SMART overall health: FAILED", Meaning: "The drive itself predicts that it will fail: one of its attributes is at or below the vendor threshold.",
		Causes: []string{"see the failing attribute on the disk page"}, Check: disk},
	"storage/self_test_failed": {Title: "A SMART self-test failed", Meaning: "The drive's own test could not read all of its surface.",
		Causes: []string{"media damage"}, Check: []string{"smartctl -l selftest /dev/{dev}"}},
	"storage/wear": {Title: "Flash endurance used up", Meaning: "The SSD has written most of the data it is rated for. Worn flash fails writes and loses data.",
		Causes: []string{"write-heavy workload over the drive's life"}, Check: []string{"smartctl -x /dev/{dev}", "plan the replacement"}},
	"storage/spare_low": {Title: "NVMe spare capacity low", Meaning: "The drive's reserve blocks for replacing worn ones are nearly used up.",
		Causes: []string{"flash wear"}, Check: []string{"nvme smart-log /dev/{dev}"}},
	"storage/nvme_critical_warning": {Title: "NVMe critical warning", Meaning: "The drive raised a critical warning: spare, temperature, reliability, read-only mode or backup.",
		Causes: []string{"see the warning bits in the message"}, Check: []string{"nvme smart-log /dev/{dev}"}},
	"storage/remote_io_errors": {
		Title: "Reads or writes to a network volume failed",
		Meaning: "This is a volume of a storage system (iSCSI or Fibre Channel), not a disk in this server. The kernel could not complete reads or writes over the " +
			"path to it; applications using the volume saw I/O errors.",
		Causes: []string{"a network interruption or a flapping link to the storage system", "the target was overloaded, restarted or failed over",
			"the session timed out (replacement_timeout) before the path came back", "multipath not configured, so one path failure breaks I/O"},
		Check: []string{"iscsiadm -m session -P 3", "journalctl -k -b | grep -iE 'iscsi|connection[0-9]+:|{dev}'", "ping the portal shown on the volume",
			"the storage system's own logs for the same time"},
	},
	"storage/iscsi_session": {Title: "iSCSI session is not logged in", Meaning: "The connection to the storage target is down; the volume cannot be read or written.",
		Causes: []string{"the network to the portal", "the target is down or refused the login", "authentication (CHAP) changed"},
		Check:  []string{"iscsiadm -m session -P 1", "journalctl -u iscsid -b", "iscsiadm -m node --login   # after the cause is fixed"}},
	"storage/fc_port": {Title: "Fibre Channel port not online", Meaning: "The remote storage port is not reachable over the fabric.",
		Causes: []string{"a cable, SFP or switch port", "zoning on the fabric switch", "the storage system's port"},
		Check:  []string{"cat /sys/class/fc_host/host*/port_state", "the fabric switch's port and zoning"}},
	"storage/md_degraded": {Title: "Software RAID lost redundancy", Meaning: "A member of the md array failed or is missing; one more failure can lose data.",
		Causes: []string{"a failed disk (see its own findings)", "a disk that was removed or not found at boot"},
		Check:  []string{"cat /proc/mdstat", "mdadm --detail /dev/{dev}"}},
	"storage/md_member_failed": {Title: "A software RAID member failed", Meaning: "The array marked a member as faulty after I/O errors.",
		Causes: []string{"the member disk (see its findings)"}, Check: []string{"cat /proc/mdstat", "mdadm --detail /dev/{dev}"}},
	"storage/md_inactive": {Title: "Software RAID inactive", Meaning: "The array is not running; its data is not accessible.",
		Causes: []string{"too many members missing"}, Check: []string{"cat /proc/mdstat", "mdadm --examine --scan"}},
	"storage/md_sync": {Title: "Software RAID rebuilding", Meaning: "The array is resyncing or recovering; performance is lower and redundancy is not complete yet.",
		Causes: []string{"a disk replacement or an unclean shutdown"}, Check: []string{"cat /proc/mdstat"}},
	"storage/raid_controller": {Title: "Hardware RAID problem", Meaning: "The RAID controller reports a degraded volume, a failed drive or a battery/cache problem.",
		Causes: []string{"a failed drive", "the cache battery or capacitor"}, Check: []string{"storcli /c0 show all   (perccli for Dell)", "ssacli ctrl all show status   (HPE), or iLO: System Information > Storage"}},
	"storage/fs_read_only": {Title: "Filesystem switched to read-only", Meaning: "The kernel stopped writing to the filesystem after errors to protect it; writes by applications fail.",
		Causes: []string{"I/O errors on the device below it", "filesystem corruption"},
		Check:  []string{"journalctl -k -b | grep -iE 'ext4|xfs|btrfs|remount'", "fsck / xfs_repair after unmounting, once the device is healthy"}},
	"storage/fs_full": {Title: "Filesystem nearly full", Meaning: "When it is full, writes fail and services stop.", Causes: []string{"data growth, logs, old snapshots"},
		Check: []string{"df -h {dev}", "du -xh --max-depth=2 {dev} | sort -h | tail"}},
	"storage/fs_inodes": {Title: "Filesystem out of inodes", Meaning: "No new files can be created even if space is free.", Causes: []string{"very many small files"},
		Check: []string{"df -i {dev}"}},

	"storage/disk_temp": {Title: "Disk temperature high", Meaning: "The drive is near or above the temperature its vendor allows. Hot drives wear faster and throttle (NVMe).",
		Causes: []string{"airflow: a failed fan, a missing blank in an empty bay, a blocked intake", "a sustained heavy write load"},
		Check:  []string{"smartctl -x /dev/{dev} | grep -iE 'temp'", "the chassis fans (System tab)"}},
	"storage/raid_battery": {Title: "RAID cache battery or capacitor", Meaning: "The cache backup unit (BBU or supercap) is failed, missing, learning or charging; the controller then usually switches to write-through, which is much slower.",
		Causes: []string{"an aged battery or capacitor", "a disconnected cable", "a learn cycle (temporary)"},
		Check:  []string{"storcli /c0/bbu show all   or   storcli /c0/cv show all", "ssacli ctrl all show status   (HPE)"}},
	"storage/raid_vd": {Title: "Hardware RAID volume degraded", Meaning: "A virtual drive of the RAID controller lost redundancy or is offline.",
		Causes: []string{"a failed or missing physical drive (see raid_pd)"}, Check: []string{"storcli /c0/vall show all   (perccli for Dell)", "ssacli ctrl all show config   (HPE), or iLO: System Information > Storage"}},
	"storage/raid_pd": {Title: "Hardware RAID drive failed", Meaning: "The controller marked a physical drive as failed, missing or predictive failure, or (as the BMC reports it) the SSD's rated endurance is nearly used up.",
		Causes: []string{"the drive", "its slot or the backplane", "flash wear (endurance)"},
		Check:  []string{"storcli /c0/eall/sall show all   (perccli for Dell)", "ssacli ctrl all show config detail   (HPE), or iLO: System Information > Storage"}},

	// --- CPU ----------------------------------------------------------------
	"cpu/core_offline": {Title: "A CPU is offline", Meaning: "A logical CPU is not running work: taken offline by an administrator, or by the kernel after hardware errors.",
		Causes: []string{"an earlier machine check on that core", "manual offlining or a kernel parameter"},
		Check:  []string{"cat /sys/devices/system/cpu/{dev}/online", "journalctl -k | grep -iE 'mce|machine check|{dev}'", "tyr core-test {dev}   # guarded test of the offline core, maintenance window"}},
	"cpu/core_test_failed": {Title: "Core test failed", Meaning: "Tyr's per-core check computed a wrong result or timed out on these CPUs. A core that miscalculates is defective even when it reports no machine check.",
		Causes: []string{"a defective core (silent data corruption)", "unstable power or overclocking/undervolting", "overheating"},
		Check:  []string{"tyr stress cpu   # run the CPU test on all cores", "journalctl -k -b | grep -iE 'mce|machine check'"}},
	"cpu/fault_cluster": {Title: "Programs crash on one core", Meaning: "The kernel names the CPU a crashing program ran on (\"likely on CPU 46 (core 21, socket 1)\"). Crashes of unrelated programs - shells, compressors, Python - piling up on one physical core mean that core computes wrong results. Such cores often raise no machine check, so they look healthy to every other check: silent data corruption.",
		Causes: []string{"a defective CPU core (replace the CPU)", "rarely: unstable power, overclocking or undervolting"},
		Check:  []string{"journalctl -k -g 'likely on CPU' --since -30d", "echo 0 > /sys/devices/system/cpu/{dev}/online   # every thread of that core", "tyr core-test {dev}   # guarded test, maintenance window"}},
	"cpu/mce_history": {Title: "Machine checks in earlier boots", Meaning: "The journal, crash records or rasdaemon hold machine checks from before this boot. The unit tells where: errors in the core's own units (L1/L2 cache, fetch, load-store, execution) point at that core; errors in shared units (L3, memory controller, interconnect) are only reported by whichever CPU noticed them.",
		Causes: []string{"a failing core (same CPU and core unit again and again)", "a failing DIMM or memory channel (memory controller banks)", "overheating or unstable power"},
		Check:  []string{"journalctl _TRANSPORT=kernel -g 'Hardware Error' --since -90d", "journalctl --list-boots", "ras-mc-ctl --errors   (rasdaemon)"}},
	"cpu/crash_record": {Title: "The kernel crashed (pstore)", Meaning: "pstore holds the last kernel messages before a panic and reboot. A \"Fatal machine check\" names the CPU and bank that failed; repeated crashes on the same core are a strong sign it is defective.",
		Causes: []string{"an uncorrectable machine check (CPU core, cache, memory)", "a kernel or driver bug (the record shows the trace)"},
		Check:  []string{"ls /sys/fs/pstore /var/lib/systemd/pstore", "cat /var/lib/systemd/pstore/*/dmesg*", "ipmitool sel elist   # the BMC's view of the same crash"}},
	"cpu/core_half_offline": {Title: "Suspect core still in use", Meaning: "One hardware thread of a physical core is offline, but its other thread still runs work: both threads share the core's execution units and caches, so a defective core keeps computing for the system. Tyr's stress tests and core test leave it alone.",
		Causes: []string{"only one logical CPU was taken offline (echo 0 > …/cpuN/online)", "a RAS daemon offlined one thread after errors"},
		Check:  []string{"cat /sys/devices/system/cpu/{dev}/topology/thread_siblings_list   # while online", "echo 0 > /sys/devices/system/cpu/cpuM/online   # the other thread, to take the whole core out", "tyr core-test {dev}   # guarded test, maintenance window"}},
	"cpu/mce": {Title: "Machine check errors", Meaning: "The CPU reported a hardware error: in a core, a cache, the memory controller or the interconnect. Corrected ones were fixed; uncorrected ones can crash the system or corrupt data.",
		Causes: []string{"a failing CPU or DIMM (the record names the bank)", "overheating or unstable power"},
		Check:  []string{"journalctl -k -b | grep -iE 'mce|machine check'", "ras-mc-ctl --errors   (rasdaemon)", "mcelog --client"}},
	"cpu/mce_exceptions": {Title: "Machine check exceptions since boot", Meaning: "The CPU raised machine check exceptions; details are in the kernel log or rasdaemon.",
		Causes: []string{"hardware errors in a core, cache or memory controller"}, Check: []string{"grep MCE /proc/interrupts", "ras-mc-ctl --errors"}},
	"cpu/throttling": {Title: "CPU throttled by temperature", Meaning: "The CPU lowered its clock to stay below its maximum temperature; work runs slower.",
		Causes: []string{"cooling: fans, dust, heatsink contact, airflow, room temperature", "a sustained full load (expected during stress tests)"},
		Check:  []string{"sensors", "watch -n1 'grep MHz /proc/cpuinfo | sort | uniq -c'"}},
	"cpu/cpu_temp":  {Title: "CPU temperature high", Meaning: "The package is near or past its limit.", Causes: []string{"cooling", "load"}, Check: []string{"sensors"}},
	"cpu/core_temp": {Title: "CPU core temperature high", Meaning: "A core is near or past its limit.", Causes: []string{"cooling", "load"}, Check: []string{"sensors"}},
	"cpu/throttle_history": {Title: "Thermal throttling since boot", Meaning: "The CPUs throttled at some point since boot; not happening now.",
		Causes: []string{"earlier load peaks or a cooling problem"}, Check: []string{"grep . /sys/devices/system/cpu/cpu*/thermal_throttle/core_throttle_count"}},
	"cpu/smt_disabled": {Title: "Hyper-threading off", Meaning: "SMT is disabled: half the logical CPUs.", Causes: []string{"BIOS setting or nosmt (often a security choice)"},
		Check: []string{"cat /sys/devices/system/cpu/smt/control"}},
	"cpu/socket_disabled": {Title: "CPU socket disabled", Meaning: "A populated socket is disabled in the firmware.", Causes: []string{"BIOS setting", "a CPU fault"},
		Check: []string{"dmidecode -t 4", "the BMC event log"}},
	"cpu/socket_no_cpus": {Title: "CPU socket without online CPUs", Meaning: "A socket is populated but none of its CPUs run.", Causes: []string{"all its cores offlined after errors"},
		Check: []string{"lscpu", "journalctl -k | grep -i mce"}},
	"cpu/microcode_mismatch": {Title: "Sockets run different microcode", Meaning: "Mixed microcode can cause errata on one socket only.",
		Causes: []string{"a missing BIOS or microcode update"}, Check: []string{"grep microcode /proc/cpuinfo | sort | uniq -c"}},
	"cpu/cpu_missing": {Title: "CPUs missing", Meaning: "The kernel sees fewer CPUs than the firmware reports.", Causes: []string{"maxcpus=/nr_cpus= on the kernel command line", "a disabled socket or core in the BIOS"},
		Check: []string{"cat /proc/cmdline", "lscpu"}},

	// --- memory -------------------------------------------------------------
	"memory/ecc_correctable": {Title: "Corrected memory errors", Meaning: "ECC fixed bit errors in this DIMM. Occasional ones are tolerable; a rising rate predicts uncorrectable errors.",
		Causes: []string{"a degrading DIMM", "a bad slot or contact", "temperature"},
		Check:  []string{"edac-util -v", "ras-mc-ctl --summary", "grep . /sys/devices/system/edac/mc/mc*/dimm*/dimm_ce_count"}},
	"memory/ecc_uncorrectable": {Title: "Uncorrectable memory error", Meaning: "ECC could not fix an error: data in memory was corrupted. The DIMM should be replaced.",
		Causes: []string{"a failing DIMM"}, Check: []string{"edac-util -v", "ras-mc-ctl --errors", "ipmitool sel elist | grep -i memory"}},
	"memory/hardware_corrupted": {Title: "Memory poisoned by the kernel", Meaning: "The kernel took pages with uncorrectable errors out of use.",
		Causes: []string{"uncorrectable memory errors"}, Check: []string{"grep HardwareCorrupted /proc/meminfo", "journalctl -k | grep -i 'memory failure'"}},
	"memory/capacity_mismatch": {Title: "Installed memory not all visible", Meaning: "The OS sees less memory than the DIMMs installed.",
		Causes: []string{"a DIMM disabled by the BIOS after errors", "a mem= kernel parameter", "a mixed or unsupported population"},
		Check:  []string{"dmidecode -t 17", "free -g", "the BIOS/BMC event log"}},
	"memory/memory_offline": {Title: "Memory blocks offline", Meaning: "Part of the memory was taken offline.", Causes: []string{"memory errors", "manual offlining"},
		Check: []string{"grep -l offline /sys/devices/system/memory/memory*/state | wc -l"}},
	"memory/dimm_speed_mismatch": {Title: "DIMMs at different speeds", Meaning: "The modules run at different speeds; all channels fall to the slowest.",
		Causes: []string{"mixed DIMM types"}, Check: []string{"dmidecode -t 17 | grep -E 'Locator|Speed|Part'"}},
	"memory/dimm_speed_below_rated": {Title: "DIMMs below their rated speed", Meaning: "Memory runs slower than the modules support.", Causes: []string{"population rules (DIMMs per channel)", "BIOS setting", "CPU limit"},
		Check: []string{"dmidecode -t 17 | grep -E 'Speed'"}},
	"memory/mixed_part_numbers": {Title: "Mixed DIMM part numbers", Meaning: "Different module models in one system; vendors support this less well.", Causes: []string{"replacements over time"},
		Check: []string{"dmidecode -t 17 | grep 'Part Number'"}},
	"memory/numa_imbalance": {Title: "NUMA nodes unbalanced", Meaning: "One CPU socket has much less memory than another: workloads on it get slower remote memory.",
		Causes: []string{"an uneven population or a failed DIMM"}, Check: []string{"numactl -H"}},
	"memory/edac_missing": {Title: "No ECC error counting", Meaning: "No EDAC driver is loaded, so corrected memory errors cannot be seen.",
		Causes: []string{"the EDAC module for this platform is not loaded", "a virtual machine"}, Check: []string{"ls /sys/devices/system/edac/mc", "lsmod | grep -i edac"}},
	"memory/sel_memory_critical": {Title: "Memory error in the BMC event log", Meaning: "The BMC logged an uncorrectable memory error (or a DIMM it disabled). The BMC sees errors even when the OS does not, for example during boot.",
		Causes: []string{"a failing DIMM (the event names the slot)"}, Check: []string{"ipmitool sel elist | grep -i memory", "ipmitool sel get <id>   # details of one event"}},
	"memory/sel_memory_warning": {Title: "Memory event in the BMC event log", Meaning: "The BMC logged corrected memory errors or a memory warning.",
		Causes: []string{"a degrading DIMM"}, Check: []string{"ipmitool sel elist | grep -i memory"}},
	"memory/mixed_part_numbers_system": {Title: "Mixed DIMM models in the system", Meaning: "The system has DIMMs of different models; vendors validate uniform populations.",
		Causes: []string{"replacements over time"}, Check: []string{"dmidecode -t 17 | grep -E 'Locator|Part Number'"}},
	"memory/dimm_temp": {Title: "DIMM temperature high", Meaning: "The module is hot; errors become more likely.", Causes: []string{"airflow, blanking panels, fan failure"},
		Check: []string{"sensors"}},

	// --- network ------------------------------------------------------------
	"network/link_down": {Title: "Link down", Meaning: "The port has no link to the switch.", Causes: []string{"cable, transceiver or switch port", "the switch port is disabled"},
		Check: []string{"ethtool {dev}", "ip link show {dev}", "ethtool -m {dev}   # optics"}},
	"network/vpn_down": {Title: "VPN interface down", Meaning: "The tunnel interface has an address but no carrier: the program behind it (OpenVPN, Tailscale, ZeroTier, pppd) is not connected or not running, so traffic for the VPN goes nowhere.",
		Causes: []string{"the VPN service stopped or failed (see the service journal on the interface's page)", "the remote end is unreachable", "expired credentials or certificate"},
		Check:  []string{"ip link show {dev}", "systemctl status 'openvpn*' tailscaled zerotier-one", "journalctl -b -t openvpn -t tailscaled -t NetworkManager | grep {dev}"}},
	"network/vpn_peer": {Title: "WireGuard peer without a session", Meaning: "WireGuard handshakes every 2 minutes while a tunnel is in use and keeps a session 3 minutes. A peer with a keepalive whose last handshake is older, or one with an endpoint that never completed a handshake, is not connected: traffic to its allowed IPs is lost.",
		Causes: []string{"the peer is down or unreachable (firewall, NAT, UDP port blocked)", "wrong endpoint or a DNS name that no longer resolves", "a key mismatch after a key change on one side", "clock or MTU problems on the path"},
		Check:  []string{"wg show {dev}", "ping <the peer's tunnel address>", "journalctl -b -t wg-quick -t systemd-networkd | grep {dev}", "echo module wireguard +p > /sys/kernel/debug/dynamic_debug/control   # handshake messages in dmesg"}},
	"network/vpn_service": {Title: "VPN service errors", Meaning: "The service behind the VPN interface (wg-quick, systemd-networkd, NetworkManager, OpenVPN, strongSwan …) logged errors in the last 24 hours.",
		Causes: []string{"configuration errors (address in use, unresolvable endpoint, bad key)", "the remote end refusing the connection", "certificates or credentials expired"},
		Check:  []string{"journalctl --since=-24h -p err -t wg-quick -t systemd-networkd -t NetworkManager -t openvpn -t charon", "systemctl status wg-quick@{dev}"}},
	"network/bond_degraded": {Title: "Bond running on fewer ports", Meaning: "Some members of the bond are down; there is no redundancy left on the failed side.",
		Causes: []string{"a member's link (see its findings)"}, Check: []string{"cat /proc/net/bonding/{dev}"}},
	"network/rx_missed": {Title: "Packets lost in the NIC", Meaning: "The network card received packets but had no free buffer in its receive ring, so it threw them away (rx_missed). The CPUs did not empty the ring fast enough for the traffic: real packet loss, retransmits for TCP, gaps for UDP. A bond repeats its members' count.",
		Causes: []string{"a receive ring that is too small for the bursts (ethtool -g shows current and maximum)", "too few receive queues, or all interrupts on one CPU (RSS, irqbalance)", "busy or throttled CPUs (power saving, a CPU at its limit)"},
		Check:  []string{"ethtool -S {dev} | grep -iE 'miss|drop|no_buf|fifo'", "ethtool -g {dev}   # then: ethtool -G {dev} rx <maximum>", "ethtool -l {dev}", "grep {dev} /proc/interrupts"}},
	"network/rx_dropped": {Title: "Received packets dropped by the kernel", Meaning: "The NIC delivered the frames, but the kernel did not hand them to anyone (rx_dropped). Usually harmless: protocols or VLANs this host does not use (LLDP, CDP, STP from the switch), multicast nobody listens to. The backup member of an active-backup bond drops everything by design, and a bond repeats its members' count. It matters when the kernel backlog overflows too (softnet drops).",
		Causes: []string{"traffic the host does not use: tagged VLANs without an interface, LLDP/CDP/STP, multicast", "the backup member of an active-backup bond", "a full per-CPU receive backlog (see softnet_drops)"},
		Check:  []string{"ip -s -s link show {dev}", "tcpdump -eni {dev} -c 50   # what arrives", "cat /proc/net/bonding/*   # active member"}},
	"network/tx_dropped": {Title: "Packets dropped on send", Meaning: "The kernel could not queue packets for sending: the transmit queue or the driver's ring was full, or the link was down at that moment.",
		Causes: []string{"more traffic than the link carries (bursts, a slow link)", "a small transmit queue (txqueuelen) or qdisc limits", "link flaps"},
		Check:  []string{"tc -s qdisc show dev {dev}", "ip -s link show {dev}", "ethtool -S {dev} | grep -iE 'tx.*(drop|busy|restart)'"}},
	"network/softnet_drops": {Title: "Kernel receive backlog full", Meaning: "Packets arrived faster than the kernel's per-CPU receive backlog could hold them (netdev_max_backlog), so the kernel dropped them: real loss above the NIC.",
		Causes: []string{"all receive work on one CPU (missing RSS/RPS, interrupt affinity)", "a backlog too small for the packet rate"},
		Check:  []string{"awk '{print NR-1, strtonum(\"0x\"$2)}' /proc/net/softnet_stat   # drops per CPU", "sysctl net.core.netdev_max_backlog", "cat /proc/interrupts | grep -iE 'eth|eno|ens|mlx|i40e|ice'"}},
	"network/crc_errors": {Title: "Received frames with CRC errors", Meaning: "Frames arrived corrupted and were dropped. The cause is on the wire: this port, the cable or optics, or the switch port.",
		Causes: []string{"a dirty or damaged fibre or connector, a bad DAC cable", "a failing transceiver on either end", "a duplex mismatch (copper)"},
		Check:  []string{"ethtool -S {dev} | grep -iE 'crc|err|fcs'", "ethtool -m {dev}   # rx/tx power", "the switch port's error counters"}},
	"network/transceiver": {Title: "Transceiver optics out of range", Meaning: "Optical power, temperature or voltage of the module is outside its own limits.",
		Causes: []string{"a dirty or bent fibre, a bad patch panel", "a failing transceiver", "the distance is too long for the optics"},
		Check:  []string{"ethtool -m {dev}", "clean and reseat the fibre; swap the transceiver"}},
	"network/nic_temp": {Title: "Network card temperature high", Meaning: "The NIC chip is near its limit; it can throttle or drop the link.",
		Causes: []string{"airflow over the PCIe slots", "a high-power transceiver in a hot slot"}, Check: []string{"sensors", "ethtool -m {dev}   # module temperature"}},
	"network/half_duplex": {Title: "Half duplex", Meaning: "The port negotiated half duplex: collisions and errors under load.", Causes: []string{"autonegotiation off on one side", "a bad cable"},
		Check: []string{"ethtool {dev}"}},
	"network/speed_below_max": {Title: "Link below the port's speed", Meaning: "The port runs slower than it and its partner support.", Causes: []string{"a cable category or damaged pair", "a forced speed on one side"},
		Check: []string{"ethtool {dev}"}},
	"network/link_flap": {Title: "Link flapping", Meaning: "The link went down and up repeatedly.", Causes: []string{"cable, transceiver, switch port", "power saving (EEE)"},
		Check: []string{"journalctl -k -b | grep -i '{dev}.*link'", "ethtool {dev}"}},
	"network/bond_down": {Title: "Bond down", Meaning: "No member of the bond has a link.", Causes: []string{"all member links (see their findings)", "the switch side"},
		Check: []string{"cat /proc/net/bonding/{dev}"}},
	"network/bond_aggregator": {Title: "LACP members in different aggregators", Meaning: "Not all bond members joined the same LACP group; some carry no traffic.",
		Causes: []string{"the switch's port-channel configuration"}, Check: []string{"cat /proc/net/bonding/{dev}"}},
	"network/link_unused": {Title: "Port with link but no address", Meaning: "A physical port has a link but is not configured or part of a bond.", Causes: []string{"cabled but unused"},
		Check: []string{"ip addr show {dev}"}},
	"network/errors_history": {Title: "Error counters since boot", Meaning: "The interface counted errors at some point since boot; not rising now.", Causes: []string{"an earlier cable or link problem"},
		Check: []string{"ethtool -S {dev} | grep -iE 'err|crc'"}},
	"network/flap_history": {Title: "Link changes since boot", Meaning: "The link went down and up since boot.", Causes: []string{"cable or switch maintenance, an earlier flap"},
		Check: []string{"cat /sys/class/net/{dev}/carrier_changes"}},
	"network/errors_rising": {Title: "Error counters rising", Meaning: "The interface's error counters grew since the last refresh.", Causes: []string{"see the counter that grows"},
		Check: []string{"ethtool -S {dev} | grep -iE 'err|drop|crc'", "ip -s link show {dev}"}},

	// --- GPU/NPU ------------------------------------------------------------
	"accel/accel_driver_errors": {Title: "GPU driver errors (Xid)", Meaning: "The driver reported errors; the Xid number says which (48 = double-bit ECC, 79 = fell off the bus, 13/31 = application faults).",
		Causes: []string{"memory errors on the GPU", "PCIe link or power problems", "application bugs (some Xids)"},
		Check:  []string{"journalctl -k -b | grep -i xid", "nvidia-smi -q -i {dev}"}},
	"accel/accel_ecc_ue": {Title: "Uncorrectable GPU memory errors", Meaning: "GPU memory returned corrupted data.", Causes: []string{"failing GPU memory"},
		Check: []string{"nvidia-smi -q -d ECC,ROW_REMAPPER -i {dev}"}},
	"accel/remap_pending": {Title: "GPU memory row remap pending", Meaning: "The GPU will remap bad memory rows at the next reset.", Causes: []string{"memory errors"},
		Check: []string{"nvidia-smi -q -d ROW_REMAPPER -i {dev}", "reset the GPU when idle"}},
	"accel/accel_ecc_ce": {Title: "Corrected GPU memory errors", Meaning: "ECC fixed errors in GPU memory; a rising rate predicts uncorrectable ones.", Causes: []string{"degrading GPU memory"},
		Check: []string{"nvidia-smi -q -d ECC -i {dev}"}},
	"accel/remap_failed": {Title: "GPU memory row remap failed", Meaning: "The GPU ran out of spare rows: the board needs service.", Causes: []string{"failing GPU memory"},
		Check: []string{"nvidia-smi -q -d ROW_REMAPPER -i {dev}"}},
	"accel/retired_pages": {Title: "GPU memory pages retired", Meaning: "The GPU took memory pages with errors out of use.", Causes: []string{"GPU memory errors"},
		Check: []string{"nvidia-smi -q -d PAGE_RETIREMENT -i {dev}"}},
	"accel/accel_throttle": {Title: "GPU throttled", Meaning: "The GPU lowered its clocks (temperature, power cap or a slowdown event).", Causes: []string{"cooling", "the power cap", "power supply"},
		Check: []string{"nvidia-smi -q -d PERFORMANCE -i {dev}"}},
	"accel/no_driver": {Title: "GPU without a driver", Meaning: "The device is present but no driver is bound; it cannot be used or monitored.", Causes: []string{"the driver is not installed or failed to load"},
		Check: []string{"lspci -k -s {dev}", "journalctl -k -b | grep -iE 'nvidia|amdgpu|i915|xe'"}},
	"accel/accel_temp": {Title: "GPU temperature high", Meaning: "The GPU or its memory is near its slowdown or shutdown temperature.",
		Causes: []string{"cooling: fans, airflow, blocked intake", "a sustained full load"}, Check: []string{"nvidia-smi -q -d TEMPERATURE -i {dev}"}},
	"accel/pcie_width": {Title: "GPU PCIe link narrower than supported", Meaning: "The GPU runs on fewer PCIe lanes than it supports: lower bandwidth. Not raised for GPUs on NVLink-C2C (DGX Spark's GB10, GH200, GB200): their PCIe function is a stub (x1, 2.5 GT/s) and not the data path.",
		Causes: []string{"the slot, a riser, or a bad seat"}, Check: []string{"lspci -vv -s {dev} | grep -E 'LnkCap|LnkSta'"}},

	// --- system -------------------------------------------------------------
	"system/unclean_shutdown": {Title: "Boots ended without a clean shutdown", Meaning: "journald logs \"Journal stopped\" when the system shuts down in order. Boots without it ended in a kernel crash, a hard reset (watchdog, BMC, power button) or a power loss. Several short boots in a row are a crash loop.",
		Causes: []string{"kernel panics: a defective CPU or memory (see the CPU and memory alerts)", "the BMC's watchdog or someone resetting the server", "power loss"},
		Check:  []string{"journalctl --list-boots", "journalctl -b -1 -n 50   # the last lines before the crash", "ls /var/lib/systemd/pstore   # crash records", "ipmitool sel elist   # the BMC's view: resets, power events"}},
	"events/bmc_events": {Title: "Open BMC event", Meaning: "The BMC's event log (HPE IML, iLO event log, SEL) holds a warning or critical event of the last 30 days that is not marked repaired. The BMC sees what the OS cannot: power, fans, POST errors, processor and memory errors handled by the firmware.",
		Causes: []string{"the component the event names (processor, DIMM, power supply, fan, drive)", "an event that was fixed but not marked repaired in the BMC"},
		Check:  []string{"the Events tab: space shows the whole entry", "HPE iLO: Information → Integrated Management Log (mark fixed entries repaired)", "ilorest serverlogs --selectlog=IML"}},
	"power/psu_redundancy": {Title: "Power supply redundancy lost", Meaning: "The redundancy group has fewer working supplies than it needs: one more failure, or one more lost feed, and the server goes down.",
		Causes: []string{"a failed or unplugged power supply (see psu_status)", "both supplies on the same PDU or circuit"},
		Check:  []string{"the BMC's power page (HPE iLO: Power & Thermal → Power Supplies)", "ipmitool sdr type 'Power Supply'"}},
	"power/psu_status": {Title: "Power supply not OK", Meaning: "The power supply reports a critical or degraded state. Either the unit failed, or it has no AC input (cable, PDU, circuit breaker): an input of 0 V means no AC. The server runs on the other supply without redundancy.",
		Causes: []string{"no AC input: unplugged cable, a PDU or breaker off", "a failed power supply unit", "a power supply not seated fully"},
		Check:  []string{"ipmitool sdr type 'Power Supply'", "ipmitool sel elist | grep -i 'power supply'", "the BMC's power page (HPE iLO: Power & Thermal → Power Supplies)"}},
	"system/fan_failed": {Title: "Fan failed", Meaning: "A fan stopped or reads below its critical speed.", Causes: []string{"a failed fan or connector"},
		Check: []string{"ipmitool sdr type Fan", "sensors"}},
	"system/ambient_temp": {Title: "Inlet or exhaust temperature high", Meaning: "The air into or out of the server is too warm.",
		Causes: []string{"room cooling, hot aisle air recirculating, blocked vents"}, Check: []string{"ipmitool sdr type Temperature"}},
	"system/fan_low": {Title: "Fan slow", Meaning: "A fan runs below its warning speed.", Causes: []string{"a worn bearing, dust"}, Check: []string{"ipmitool sdr type Fan"}},
}
