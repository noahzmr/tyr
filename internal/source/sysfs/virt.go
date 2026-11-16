package sysfs

import (
	"slices"
	"strings"

	"github.com/noahzmr/tyr/internal/hostfs"
)

// DetectVirt returns "none" on bare metal or a short hypervisor/container
// name. It only reads files; systemd-detect-virt is not needed.
func DetectVirt(f hostfs.FS) string {
	if hostfs.Exists(f, "/.dockerenv") || hostfs.Exists(f, "/run/.containerenv") {
		return "container"
	}
	if t := hostfs.StringOr(f, "/sys/hypervisor/type", ""); t == "xen" {
		// dom0 is bare metal for our purposes.
		if strings.Contains(hostfs.StringOr(f, "/proc/xen/capabilities", ""), "control_d") {
			return "none"
		}
		return "xen"
	}
	vendor := strings.ToLower(hostfs.StringOr(f, "/sys/class/dmi/id/sys_vendor", ""))
	product := strings.ToLower(hostfs.StringOr(f, "/sys/class/dmi/id/product_name", ""))
	board := strings.ToLower(hostfs.StringOr(f, "/sys/class/dmi/id/board_vendor", ""))
	bios := strings.ToLower(hostfs.StringOr(f, "/sys/class/dmi/id/bios_vendor", ""))
	switch {
	case strings.Contains(vendor, "qemu") || strings.Contains(product, "kvm") || strings.Contains(bios, "seabios") && strings.Contains(vendor, "qemu"):
		return "kvm"
	case strings.Contains(vendor, "vmware"):
		return "vmware"
	case strings.Contains(vendor, "microsoft") && strings.Contains(product, "virtual"):
		return "hyperv"
	case strings.Contains(vendor, "innotek") || strings.Contains(product, "virtualbox"):
		return "oracle"
	case strings.Contains(vendor, "xen"):
		return "xen"
	case strings.Contains(vendor, "amazon ec2") && !strings.Contains(product, "metal"):
		return "amazon"
	case strings.Contains(vendor, "google") && strings.Contains(product, "compute engine"):
		return "google"
	case strings.Contains(board, "parallels"):
		return "parallels"
	}
	if b, err := f.ReadFile("/proc/cpuinfo"); err == nil {
		ci := ParseCPUInfo(b)
		if slices.Contains(ci.Flags, "hypervisor") {
			return "vm"
		}
	}
	return "none"
}
