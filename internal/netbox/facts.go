package netbox

import (
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/noahzmr/tyr/internal/accept"
	"github.com/noahzmr/tyr/internal/model"
)

// Facts are the values rules and custom fields can use.
type Facts map[string]Value

// FactDef describes one fact.
type FactDef struct {
	Name string
	Kind kind
	Help string
}

// FactDefs lists every fact in display order.
var FactDefs = []FactDef{
	{"vendor", kStr, "system manufacturer (SMBIOS)"},
	{"model", kStr, "system product name"},
	{"serial", kStr, "system serial number"},
	{"hostname", kStr, "host name"},
	{"arch", kStr, "amd64, arm64 …"},
	{"virtual", kBool, "a virtual machine"},
	{"bios", kStr, "BIOS vendor and version"},
	{"os", kStr, "operating system: \"Ubuntu 24.04.3 LTS\" (os-release)"},
	{"os_id", kStr, "its ID: ubuntu, debian, rhel, rocky …"},
	{"os_version", kStr, "its version: 24.04, 12, 9.4 …"},
	{"kernel", kStr, "kernel release: 6.8.0-49-generic"},
	{"live_system", kBool, "running from a live medium (the Tyr ISO): os is the live system's"},
	{"cpu_model", kStr, "CPU model"},
	{"cpu_vendor", kStr, "GenuineIntel, AuthenticAMD …"},
	{"sockets", kNum, "CPU sockets"},
	{"cores", kNum, "physical cores, all sockets"},
	{"threads", kNum, "logical CPUs"},
	{"memory_gb", kNum, "installed memory, GiB (DIMMs; else what the OS sees)"},
	{"dimms", kNum, "populated DIMM slots"},
	{"dimm_slots", kNum, "DIMM slots"},
	{"ecc", kBool, "ECC memory"},
	{"disks", kNum, "physical disks (not USB, not virtual)"},
	{"disk_tb", kNum, "capacity of those disks, TB"},
	{"ssds", kNum, "SSDs (SATA/SAS)"},
	{"nvme", kNum, "NVMe drives"},
	{"hdds", kNum, "hard disks"},
	{"raid_controllers", kNum, "hardware RAID controllers"},
	{"nics", kNum, "physical network ports"},
	{"nic_speed_max", kNum, "fastest port, Mbit/s (10 GbE = 10000)"},
	{"nics_10g", kNum, "ports of 10 Gbit/s or more"},
	{"nic_models", kStr, "NIC models, comma-separated"},
	{"vpns", kNum, "VPN interfaces (WireGuard, OpenVPN, Tailscale …)"},
	{"vpn_types", kStr, "their types, comma-separated"},
	{"gpus", kNum, "GPUs"},
	{"gpu_model", kStr, "GPU models, comma-separated"},
	{"npus", kNum, "NPUs"},
	{"psus", kNum, "power supplies"},
	{"status", kStr, "health now: OK, WARN, CRIT"},
	{"critical", kNum, "critical findings now"},
	{"warnings", kNum, "warnings now"},
	{"findings", kStr, "codes of all findings now, space-separated (findings ~ \"pending_sectors\")"},
	{"failed_parts", kNum, "components with critical status"},
	{"verdict", kStr, "acceptance test: PASS, REMARKS, FAIL, INCOMPLETE; empty without a test"},
	{"profile", kStr, "acceptance profile: quick, burnin, extended; empty without a test"},
}

var factIndex = func() map[string]FactDef {
	m := map[string]FactDef{}
	for _, d := range FactDefs {
		m[d.Name] = d
	}
	return m
}()

// ComputeFacts derives the facts from a snapshot and, after an
// acceptance test, its result (nil without one).
func ComputeFacts(s *model.Snapshot, r *accept.Result) Facts {
	f := Facts{}
	for _, d := range FactDefs { // every fact exists, empty when unknown
		switch d.Kind {
		case kNum:
			f[d.Name] = Num(0)
		case kBool:
			f[d.Name] = Bool(false)
		default:
			f[d.Name] = Str("")
		}
	}
	if s == nil {
		return f
	}
	f["hostname"] = Str(s.Hostname)
	f["arch"] = Str(s.Arch)
	f["virtual"] = Bool(s.Virtualization != "" && s.Virtualization != "none")
	if y := s.System; y != nil {
		f["vendor"] = Str(y.Vendor)
		f["model"] = Str(y.Product)
		f["serial"] = Str(y.Serial)
		f["bios"] = Str(strings.TrimSpace(y.BIOSVendor + " " + y.BIOSVersion))
		f["os"], f["os_id"], f["os_version"], f["kernel"] = Str(y.OS), Str(y.OSID), Str(y.OSVersion), Str(y.Kernel)
		f["live_system"] = Bool(y.LiveSystem)
		psus := 0
		for _, p := range y.PSUs {
			if p.Present {
				psus++
			}
		}
		f["psus"] = Num(float64(psus))
	}
	if p := s.Power; p != nil {
		n := 0
		for _, x := range p.Supplies {
			if x.Present {
				n++
			}
		}
		if n > 0 {
			f["psus"] = Num(float64(n))
		}
	}
	if c := s.CPU; c != nil {
		f["cpu_model"] = Str(c.Model)
		f["cpu_vendor"] = Str(c.Vendor)
		f["sockets"] = Num(float64(c.SocketCount))
		f["cores"] = Num(float64(c.CoreCount))
		f["threads"] = Num(float64(c.ThreadCount))
	}
	if m := s.Memory; m != nil {
		b := m.TotalBytes
		if m.InstalledBytes != nil && *m.InstalledBytes > 0 {
			b = *m.InstalledBytes
		}
		f["memory_gb"] = Num(math.Round(float64(b) / (1 << 30)))
		f["dimms"] = Num(float64(m.PopulatedSlots))
		f["dimm_slots"] = Num(float64(m.Slots))
		f["ecc"] = Bool(m.ECCType != "" && !strings.EqualFold(m.ECCType, "None") && !strings.EqualFold(m.ECCType, "Unknown"))
	}
	if st := s.Storage; st != nil {
		var n, ssd, nvme, hdd int
		var bytes uint64
		for _, d := range st.Disks {
			if d.Kind == "virtual" || d.Kind == "usb" || d.Interface == "usb" {
				continue
			}
			n++
			bytes += d.SizeBytes
			switch d.Kind {
			case "nvme":
				nvme++
			case "ssd":
				ssd++
			case "hdd":
				hdd++
			}
		}
		f["disks"] = Num(float64(n))
		f["disk_tb"] = Num(math.Round(float64(bytes)/1e12*100) / 100)
		f["ssds"], f["nvme"], f["hdds"] = Num(float64(ssd)), Num(float64(nvme)), Num(float64(hdd))
		f["raid_controllers"] = Num(float64(len(st.Controllers)))
	}
	if nw := s.Network; nw != nil {
		var n, ten, maxS int
		var models []string
		for _, x := range nw.Interfaces {
			if x.Kind != "physical" {
				continue
			}
			n++
			sp := nicSpeed(x)
			maxS = max(maxS, sp)
			if sp >= 10000 {
				ten++
			}
			if x.Model != "" && !slices.Contains(models, x.Model) {
				models = append(models, x.Model)
			}
		}
		f["nics"], f["nics_10g"], f["nic_speed_max"] = Num(float64(n)), Num(float64(ten)), Num(float64(maxS))
		f["nic_models"] = Str(strings.Join(models, ", "))
		var vpns []string
		nv := 0
		for _, x := range nw.Interfaces {
			if x.Kind == "vpn" {
				nv++
				if x.VPN != nil && !slices.Contains(vpns, x.VPN.Type) {
					vpns = append(vpns, x.VPN.Type)
				}
			}
		}
		f["vpns"], f["vpn_types"] = Num(float64(nv)), Str(strings.Join(vpns, ", "))
	}
	if a := s.Accel; a != nil {
		var models []string
		for _, g := range a.GPUs {
			if g.Model != "" && !slices.Contains(models, g.Model) {
				models = append(models, g.Model)
			}
		}
		f["gpus"], f["npus"] = Num(float64(len(a.GPUs))), Num(float64(len(a.NPUs)))
		f["gpu_model"] = Str(strings.Join(models, ", "))
	}
	worst, crit, warn := model.StatusOK, 0, 0
	var codes []string
	for _, id := range model.Modules {
		sec := s.Section(id)
		if sec == nil {
			continue
		}
		for _, x := range sec.Head().Findings {
			worst = model.Worst(worst, x.Status)
			switch x.Status {
			case model.StatusCrit:
				crit++
			case model.StatusWarn:
				warn++
			}
			if x.Status != model.StatusOK && !slices.Contains(codes, x.Code) {
				codes = append(codes, x.Code)
			}
		}
	}
	sort.Strings(codes)
	f["status"] = Str(worst.String())
	f["critical"], f["warnings"] = Num(float64(crit)), Num(float64(warn))
	f["findings"] = Str(strings.Join(codes, " "))
	failed := 0
	for _, c := range inventory(s) {
		if c.Status == model.StatusCrit {
			failed++
		}
	}
	f["failed_parts"] = Num(float64(failed))
	if r != nil {
		f["verdict"] = Str(string(r.Verdict))
		f["profile"] = Str(r.Profile.Name)
	}
	return f
}

// inventory is the snapshot's inventory, built when it is not stored.
func inventory(s *model.Snapshot) []model.Component {
	if len(s.Inventory) > 0 {
		return s.Inventory
	}
	return model.BuildInventory(s)
}

// nicSpeed is the port's speed in Mbit/s: the link speed, else what the
// port supports.
func nicSpeed(x model.NIC) int {
	sp := 0
	if x.SpeedMbps != nil && *x.SpeedMbps > 0 {
		sp = *x.SpeedMbps
	}
	if x.MaxSpeedMbps != nil && *x.MaxSpeedMbps > sp {
		sp = *x.MaxSpeedMbps
	}
	return sp
}

// RuleResult is one rule evaluated.
type RuleResult struct {
	Rule  TagRule
	Match bool
	Err   error
}

// Evaluate evaluates every rule.
func (t Tags) Evaluate(f Facts) []RuleResult {
	out := make([]RuleResult, 0, len(t.Rules))
	for _, r := range t.Rules {
		res := RuleResult{Rule: r}
		e, err := Parse(r.When)
		if err == nil {
			res.Match, err = e.Eval(f)
		}
		res.Err = err
		out = append(out, res)
	}
	return out
}

var braceRE = regexp.MustCompile(`\{([a-z_0-9]+)\}`)

// Expand replaces {fact} in a custom field template.
func Expand(tmpl string, f Facts) string {
	return braceRE.ReplaceAllStringFunc(tmpl, func(m string) string {
		v, ok := f[m[1:len(m)-1]]
		if !ok {
			return m
		}
		return plain(v)
	})
}
