#!/usr/bin/env bash
# capture-fixtures.sh - record a Tyr fixture from a real host.
#
# Usage: sudo scripts/capture-fixtures.sh [--redact] <output-dir> [description]
#
# Only READ-ONLY commands are executed. The result is a directory with
#   manifest.json  root/{proc,sys,dev}  cmd/*.txt
# that `tyr --fixtures <dir>` and the unit tests can replay.
# Review the output before committing it: --redact replaces serial numbers,
# UUIDs, MAC addresses and the hostname, but it cannot know every vendor field.
set -u
export LC_ALL=C

REDACT=0
if [ "${1:-}" = "--redact" ]; then REDACT=1; shift; fi
OUT=${1:?usage: $0 [--redact] <output-dir> [description]}
DESC=${2:-"captured $(date -u +%Y-%m-%d)"}
if [ -e "$OUT" ] && [ -n "$(ls -A "$OUT" 2>/dev/null)" ]; then
	echo "refusing to write into non-empty $OUT" >&2; exit 1
fi
mkdir -p "$OUT/root" "$OUT/cmd"
OUT=$(cd "$OUT" && pwd)
ROOT="$OUT/root"
CMDS=()

# mirror PATH: copy a file (or create a dir) at the same path under root/,
# recreating every symlink on the way so sysfs class links keep working.
mirror() {
	local src=$1 cur="" comp target rel
	local IFS=/
	read -ra parts <<<"${src#/}"
	unset IFS
	for comp in "${parts[@]}"; do
		cur="$cur/$comp"
		if [ -L "$cur" ]; then
			if [ ! -L "$ROOT$cur" ] && [ ! -e "$ROOT$cur" ]; then
				mkdir -p "$(dirname "$ROOT$cur")"
				target=$(readlink "$cur")
				if [ "${target#/}" != "$target" ]; then
					rel=$(realpath -m --relative-to="$(dirname "$cur")" "$target")
					target=$rel
				fi
				ln -s "$target" "$ROOT$cur"
			fi
			cur=$(readlink -f "$cur")
		fi
	done
	if [ -d "$cur" ]; then
		mkdir -p "$ROOT$cur"
	elif [ -f "$cur" ] && [ -r "$cur" ]; then
		mkdir -p "$(dirname "$ROOT$cur")"
		timeout 2 cat "$cur" >"$ROOT$cur" 2>/dev/null || rm -f "$ROOT$cur"
	elif [ -c "$cur" ] || [ -b "$cur" ]; then
		mkdir -p "$(dirname "$ROOT$cur")"
		: >"$ROOT$cur" # marker only
	fi
}

# cap GLOB...: mirror every match (no error if nothing matches)
cap() {
	local g p
	for g in "$@"; do
		for p in $g; do
			[ -e "$p" ] || [ -L "$p" ] || continue
			mirror "$p"
		done
	done
}

# capdir DIR: mirror all regular files directly in DIR
capdir() {
	local d
	for d in $1; do
		[ -d "$d" ] || continue
		mirror "$d"
		local f
		for f in "$d"/*; do
			[ -f "$f" ] && mirror "$f"
		done
	done
}

json_str() { # minimal JSON string escaping
	local s=$1
	s=${s//\\/\\\\}; s=${s//\"/\\\"}; s=${s//$'\t'/\\t}; s=${s//$'\n'/\\n}; s=${s//$'\r'/}
	printf '"%s"' "$s"
}

# run NAME ARGS...: record a command if the tool exists
run() {
	local tool=$1; shift
	command -v "$tool" >/dev/null 2>&1 || return 0
	local name="$tool${*:+_$*}"
	name=$(printf '%s' "$name" | tr -c 'A-Za-z0-9._-' '_' | cut -c1-120)
	local out="cmd/$name.txt" errf
	errf=$(mktemp)
	timeout 60 "$tool" "$@" >"$OUT/$out" 2>"$errf"
	local rc=$?
	local argv="" a
	for a in "$tool" "$@"; do argv+="${argv:+, }$(json_str "$a")"; done
	CMDS+=("{\"argv\": [$argv], \"stdout\": $(json_str "$out"), \"stderr\": $(json_str "$(head -c 2000 "$errf")"), \"exit\": $rc}")
	rm -f "$errf"
}

echo "capturing /proc and /sys ..." >&2
# /proc/self points at the reading process; copy the mount table explicitly.
mkdir -p "$ROOT/proc/self"
cat /proc/self/mountinfo >"$ROOT/proc/self/mountinfo"
# /proc/net is a link into /proc/self as well: copy bonding/VLAN state as files.
for f in /proc/net/dev /proc/net/bonding/* /proc/net/vlan/*; do
	[ -f "$f" ] || continue
	mkdir -p "$ROOT$(dirname "$f")"
	cat "$f" >"$ROOT$f" 2>/dev/null
done
cap /proc/cpuinfo /proc/stat /proc/meminfo /proc/interrupts /proc/mdstat /proc/vmstat \
	/proc/swaps /proc/partitions /proc/cmdline /proc/version \
	/proc/uptime /proc/loadavg /proc/pressure/cpu /proc/pressure/memory /proc/pressure/io \
	/proc/sys/kernel/dmesg_restrict
cap /sys/devices/system/cpu/{present,possible,online,offline,kernel_max} \
	/sys/devices/system/cpu/smt/control /sys/devices/system/cpu/smt/active \
	"/sys/devices/system/cpu/cpu[0-9]*/online" \
	"/sys/devices/system/cpu/cpu[0-9]*/node[0-9]*" \
	"/sys/devices/system/cpu/cpu[0-9]*/microcode/version" \
	"/sys/devices/system/cpu/cpu[0-9]*/acpi_cppc/nominal_freq" \
	"/sys/devices/system/cpu/cpu[0-9]*/topology/ppin"
capdir "/sys/devices/system/cpu/cpu[0-9]*/topology"
capdir "/sys/devices/system/cpu/cpu[0-9]*/cpufreq"
capdir "/sys/devices/system/cpu/cpu[0-9]*/thermal_throttle"
# CPU details: caches, C-states, security vulnerabilities, isolation, hybrid core lists
capdir "/sys/devices/system/cpu/cpu[0-9]*/cache/index[0-9]*"
capdir "/sys/devices/system/cpu/cpu[0-9]*/cpuidle/state[0-9]*"
capdir /sys/devices/system/cpu/vulnerabilities
cap /sys/devices/system/cpu/{isolated,nohz_full} /sys/devices/system/cpu/cpufreq/boost \
	/sys/devices/system/cpu/intel_pstate/no_turbo /sys/devices/cpu_core/cpus /sys/devices/cpu_atom/cpus \
	"/sys/devices/system/cpu/cpu[0-9]*/cpu_capacity"
cap "/sys/devices/system/node/node[0-9]*/cpulist" "/sys/devices/system/node/node[0-9]*/meminfo" \
	/sys/devices/system/node/{online,possible}
# memory blocks (online/offline state, block size is hex)
cap /sys/devices/system/memory/block_size_bytes "/sys/devices/system/memory/memory[0-9]*/state" \
	"/sys/devices/system/memory/memory[0-9]*/online"
# DMI identity (non-root readable subset is fine)
capdir /sys/class/dmi/id
cap /sys/hypervisor/type /proc/xen/capabilities /.dockerenv /run/.containerenv
# hwmon + thermal
for h in /sys/class/hwmon/hwmon*; do
	[ -e "$h" ] || continue
	mirror "$h/name"
	[ -L "$h/device" ] && mirror "$h/device"
	for f in "$h"/{temp,in,fan,power,curr}*_*; do [ -f "$f" ] && mirror "$f"; done
done
for z in /sys/class/thermal/thermal_zone*; do
	[ -e "$z" ] || continue
	cap "$z/type" "$z/temp" "$z/trip_point_*_type" "$z/trip_point_*_temp"
done
# EDAC (uevent keeps the mc/ dir even without controllers)
cap /sys/devices/system/edac/mc/uevent
capdir "/sys/devices/system/edac/mc/mc[0-9]*"
capdir "/sys/devices/system/edac/mc/mc[0-9]*/dimm[0-9]*"
capdir "/sys/devices/system/edac/mc/mc[0-9]*/rank[0-9]*"
capdir "/sys/devices/system/edac/mc/mc[0-9]*/csrow[0-9]*"
# PCIe AER
cap "/sys/bus/pci/devices/*/aer_dev_correctable" "/sys/bus/pci/devices/*/aer_dev_fatal" \
	"/sys/bus/pci/devices/*/aer_dev_nonfatal"
# power capping (RAPL)
for r in /sys/class/powercap/intel-rapl:*; do
	[ -e "$r" ] || continue
	cap "$r/name" "$r/energy_uj" "$r/max_energy_range_uj" "$r/constraint_0_power_limit_uw"
done
# PCI devices (network, GPU, NPU, storage controllers)
for p in /sys/bus/pci/devices/*; do
	[ -e "$p" ] || continue
	cap "$p/class" "$p/vendor" "$p/device" "$p/subsystem_vendor" "$p/subsystem_device" "$p/numa_node" \
		"$p/current_link_speed" "$p/current_link_width" "$p/max_link_speed" "$p/max_link_width" \
		"$p/power_state" "$p/power/runtime_status" "$p/power/runtime_active_time" "$p/driver" "$p/fw_version" "$p/vbnv" "$p/npu_busy_time_us" \
		"$p/gpu_busy_percent" "$p/mem_info_vram_total" "$p/mem_info_vram_used" "$p/vbios_version" \
		"$p/ras/umc_err_count" "$p/ras/gpu_vram_bad_pages" "$p/tile0/gt0/freq0/act_freq" \
		"$p/product_name" "$p/product_number" "$p/serial_number" "$p/unique_id"
	# PCI VPD (binary, root only): NIC/GPU serial and part numbers
	[ -r "$p/vpd" ] && timeout 2 cat "$p/vpd" >/dev/null 2>&1 && mirror "$p/vpd"
done
for c in /sys/class/drm/card[0-9]* /sys/class/accel/accel[0-9]*; do
	[ -e "$c" ] || continue
	cap "$c/device" "$c/gt_cur_freq_mhz" "$c/uevent"
done
# pci.ids trimmed to the vendors/devices present (full file is ~1.4 MB)
for ids in /usr/share/hwdata/pci.ids /usr/share/misc/pci.ids; do
	[ -r "$ids" ] || continue
	want=$(for p in /sys/bus/pci/devices/*; do printf '%s:%s\n' "$(sed 's/0x//' "$p/vendor")" "$(sed 's/0x//' "$p/device")"; done | sort -u)
	mkdir -p "$ROOT$(dirname "$ids")"
	echo "$want" | awk -v ids="$ids" '
		NR==FNR { split($0, a, ":"); v[a[1]]=1; d[a[1]":"a[2]]=1; next }
		/^C / { exit }
		/^[0-9a-f][0-9a-f][0-9a-f][0-9a-f]  / { cur=$1; if (v[cur]) print; next }
		/^\t[0-9a-f][0-9a-f][0-9a-f][0-9a-f]  / { split($0, x, " "); id=substr(x[1], 1); gsub(/\t/, "", id); if (d[cur":"id]) print }
	' - "$ids" >"$ROOT$ids"
	break
done
# NVMe controllers and disk serials
for n in /sys/class/nvme/nvme*; do
	[ -e "$n" ] || continue
	cap "$n/model" "$n/serial" "$n/firmware_rev" "$n/transport" "$n/state"
done
if [ -d /dev/disk/by-id ]; then
	mkdir -p "$ROOT/dev/disk/by-id"
	for l in /dev/disk/by-id/*; do [ -L "$l" ] && ln -sf "$(readlink "$l")" "$ROOT/dev/disk/by-id/$(basename "$l")"; done
fi
# block devices
for b in /sys/block/*; do
	[ -e "$b" ] || continue
	mirror "$b"
	cap "$b/size" "$b/ro" "$b/removable" "$b/dev" "$b/stat" "$b/queue/rotational" \
		"$b/queue/logical_block_size" "$b/queue/physical_block_size" "$b/device/model" \
		"$b/device/vendor" "$b/device/rev" "$b/device/serial" "$b/device/firmware_rev" \
		"$b/device/state" "$b/md/level" "$b/md/array_state" "$b/md/degraded" "$b/md/sync_action" \
		"$b/md/sync_completed" "$b/md/raid_disks" "$b/holders/*" "$b/slaves/*" "$b/*/partition" \
		"$b/*/size" "$b/*/holders/*" "$b/device/ioerr_cnt" "$b/device/model" "$b/device/serial" "$b/device/firmware_rev" "$b/device/transport" \
		"$b/wwid" "$b/device/wwid"
done
# network
for n in /sys/class/net/*; do
	[ -e "$n" ] || continue
	cap "$n/address" "$n/operstate" "$n/carrier" "$n/carrier_changes" "$n/carrier_up_count" \
		"$n/carrier_down_count" "$n/speed" "$n/duplex" "$n/mtu" "$n/type" "$n/flags" "$n/ifindex" \
		"$n/iflink" "$n/addr_assign_type" "$n/statistics/*" "$n/device" "$n/device/numa_node" \
		"$n/device/vendor" "$n/device/device" "$n/device/subsystem_vendor" "$n/device/subsystem_device" \
		"$n/device/current_link_speed" "$n/device/current_link_width" "$n/device/max_link_speed" \
		"$n/device/max_link_width" "$n/bonding/*" "$n/bridge/bridge_id" "$n/brif/*" "$n/master" \
		"$n/lower_*" "$n/upper_*" "$n/phys_port_name" "$n/dev_port" "$n/uevent" "$n/device/driver" "$n/wireless"
done
cap /dev/ipmi0 /dev/ipmi/0 /dev/ipmidev/0  # markers; /dev/kmsg is written below only if readable

echo "running read-only commands ..." >&2
run dmidecode
run journalctl -k -b -o json --no-pager --output-fields=MESSAGE,PRIORITY,__REALTIME_TIMESTAMP
if timeout 1 head -c 1 /dev/kmsg >/dev/null 2>&1; then
	# store kmsg records as lines; replaces the empty device marker
	mkdir -p "$ROOT/dev"
	timeout 3 dd if=/dev/kmsg iflag=nonblock bs=16k 2>/dev/null >"$ROOT/dev/kmsg" || true
fi
run ipmiutil sensor -c
run ipmiutil sel -c
run ipmiutil fru -c
run ipmiutil lan -c
run ipmiutil health -c
run ipmiutil dcmi power
run ipmitool sensor
run ipmitool sel info
run ipmitool sel elist
run ipmitool fru print
run ipmitool lan print
run ipmitool mc info
run ipmitool dcmi power reading
run ipmitool sdr type "Power Supply"
run smartctl --scan-open -j
for d in /dev/sd? /dev/sd?? /dev/nvme?n1 /dev/nvme??n1; do
	[ -b "$d" ] || continue
	run smartctl -j -x "$d"
done
run nvme list -o json
for d in /dev/nvme?; do [ -c "$d" ] && run nvme smart-log "$d" -o json; done
for d in /dev/md[0-9]* /dev/md/*; do [ -b "$d" ] && run mdadm --detail "$d"; done
run lsblk -J -b -O
run storcli64 /call show all J
run storcli64 /call/eall/sall show all J
run storcli /call/eall/sall show all J
run perccli64 /call/eall/sall show all J
run storcli /call show all J
run perccli64 /call show all J
run perccli /call show all J
run ssacli ctrl all show config detail
for n in /sys/class/net/*; do
	i=$(basename "$n")
	[ "$i" = lo ] && continue
	run ethtool "$i"
	run ethtool -i "$i"
	run ethtool -S "$i"
	run ethtool -m "$i"
done
run lldpctl -f json
run ip -j addr show
run df -B1 --output=target,size,used,avail,itotal,iused
run nvidia-smi -q -x
# AMD XDNA NPUs: power, power mode, AIE columns, running contexts (read-only)
for p in /sys/bus/pci/drivers/amdxdna/0000:*; do
	[ -e "$p" ] && run xrt-smi --batch examine -d "$(basename "$p")" -r platform -r aie-partitions
done

TOOLS=""
for t in dmidecode journalctl dmesg ipmiutil ipmitool smartctl nvme mdadm lsblk storcli storcli64 perccli perccli64 ssacli \
	ethtool ip lldpctl iperf3 df nvidia-smi xrt-smi; do
	p=$(command -v "$t" 2>/dev/null) || continue
	TOOLS+="${TOOLS:+, }$(json_str "$t"): $(json_str "$p")"
done

priv_root=false; [ "$(id -u)" = 0 ] && priv_root=true
priv_dmi=false; [ -r /sys/firmware/dmi/tables/DMI ] && priv_dmi=true
priv_kmsg=false; timeout 1 head -c 1 /dev/kmsg >/dev/null 2>&1 && priv_kmsg=true # -r ignores dmesg_restrict
priv_ipmi=false; { [ -r /dev/ipmi0 ] || [ -r /dev/ipmi/0 ] || [ -r /dev/ipmidev/0 ]; } && priv_ipmi=true
virt=$(systemd-detect-virt 2>/dev/null || true); [ -z "$virt" ] && virt=none
host=$(hostname)

if [ $REDACT = 1 ]; then
	echo "redacting ..." >&2
	files=$(find "$OUT/cmd" "$ROOT/sys/class/dmi" "$ROOT/sys/devices/virtual/dmi" -type f 2>/dev/null)
	# shellcheck disable=SC2086
	sed -i -E \
		-e 's/((Serial Number|Asset Tag|UUID|Serial|serial_number|wwn)[":[:space:]]+)[A-Za-z0-9._:-]{4,}/\1REDACTED/g' \
		-e 's/([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}/02:00:00:00:00:00/g' \
		-e "s/$host/fixture-host/g" $files 2>/dev/null
	find "$ROOT/sys" -name address -path '*/net/*' -exec sh -c 'echo 02:00:00:00:00:00 >"$1"' _ {} \; 2>/dev/null
	for f in product_serial board_serial chassis_serial product_uuid; do
		find "$ROOT/sys" -name "$f" -exec sh -c 'echo REDACTED >"$1"' _ {} \; 2>/dev/null
	done
	# disk serials appear in sysfs, lsblk/smartctl output and /dev/disk/by-id names
	serials=$({
		cat /sys/class/nvme/*/serial /sys/block/*/device/serial 2>/dev/null
		for l in /dev/disk/by-id/*; do
			n=$(basename "$l")
			case $n in *-part* | wwn-* | nvme-eui.* | dm-* | md-* | lvm-*) continue ;; esac
			echo "${n##*_}"
		done
	} | tr -d ' ' | grep -E '^[A-Za-z0-9_-]{6,}$' | sort -u)
	i=0
	for s in $serials; do
		i=$((i + 1))
		r=$(printf 'SERIAL%04d' "$i")
		grep -rlF -- "$s" "$OUT" 2>/dev/null | xargs -r sed -i "s/$s/$r/g"
		for l in "$ROOT"/dev/disk/by-id/*"$s"*; do
			[ -L "$l" ] && mv "$l" "${l//$s/$r}"
		done
	done
	# other unique identifiers: WWNs, PPINs, GPU serials/unique ids, VPD
	find "$ROOT/sys" \( -name wwid -o -name ppin -o -name serial_number -o -name unique_id \) -type f \
		-exec sh -c 'echo REDACTED >"$1"' _ {} \; 2>/dev/null
	find "$ROOT/sys" -name vpd -type f -delete 2>/dev/null
	sed -i -E 's/<(serial|uuid)>[^<]*</<\1>REDACTED</g' "$OUT"/cmd/nvidia-smi* 2>/dev/null
	host=fixture-host
fi

{
	echo "{"
	echo "  \"description\": $(json_str "$DESC"),"
	echo "  \"hostname\": $(json_str "$host"),"
	echo "  \"arch\": \"$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')\","
	echo "  \"virtualization\": $(json_str "$virt"),"
	echo "  \"now\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\","
	echo "  \"privileges\": {\"root\": $priv_root, \"dmi\": $priv_dmi, \"kmsg\": $priv_kmsg, \"ipmi\": $priv_ipmi},"
	echo "  \"tools\": {$TOOLS},"
	echo "  \"commands\": ["
	n=${#CMDS[@]}
	for i in "${!CMDS[@]}"; do
		sep=","; [ $((i + 1)) -eq "$n" ] && sep=""
		echo "    ${CMDS[$i]}$sep"
	done
	echo "  ],"
	echo "  \"sim\": {}"
	echo "}"
} >"$OUT/manifest.json"

echo "fixture written to $OUT ($(du -sh "$OUT" | cut -f1))" >&2
