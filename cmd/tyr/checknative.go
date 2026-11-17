package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/execx"
	"github.com/noahzmr/tyr/internal/source/dmi"
	"github.com/noahzmr/tyr/internal/source/ipmi"
	"github.com/noahzmr/tyr/internal/source/netinfo"
)

// runCheckNative reads SMBIOS, NIC data and the BMC both natively and with
// dmidecode, ethtool and ipmitool, and lists every difference: the way to
// confirm the native readers on a new kind of server. Read-only.
func runCheckNative(stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	run := execx.Host()
	diffs := 0
	report := func(area string, problems []string, ok string) {
		if len(problems) == 0 {
			fmt.Fprintf(stdout, "✓ %s: %s\n", area, ok)
			return
		}
		diffs += len(problems)
		fmt.Fprintf(stdout, "✗ %s: %d differences\n", area, len(problems))
		for _, p := range problems {
			fmt.Fprintln(stdout, "    "+p)
		}
	}
	skip := func(area, why string) { fmt.Fprintf(stdout, "- %s: %s\n", area, why) }

	// SMBIOS
	if tab, err := os.ReadFile("/sys/firmware/dmi/tables/DMI"); err != nil {
		skip("SMBIOS", "table not readable ("+err.Error()+"): run as root")
	} else if _, err := exec.LookPath("dmidecode"); err != nil {
		skip("SMBIOS", "dmidecode not installed: nothing to compare with")
	} else {
		entry, _ := os.ReadFile("/sys/firmware/dmi/tables/smbios_entry_point")
		nat, err1 := dmi.ParseTable(tab, entry)
		tool, err2 := dmi.Read(ctx, run)
		switch {
		case err1 != nil:
			report("SMBIOS", []string{"native: " + err1.Error()}, "")
		case err2 != nil:
			skip("SMBIOS", "dmidecode: "+err2.Error())
		default:
			var p []string
			for _, typ := range []int{0, 1, 2, 3} {
				a, b := nat.ByType(typ), tool.ByType(typ)
				if len(a) == 0 || len(b) == 0 {
					continue
				}
				for _, k := range []string{"Vendor", "Version", "Release Date", "Manufacturer", "Product Name", "Serial Number", "UUID", "SKU Number", "Asset Tag", "Type"} {
					if x, y := a[0].Get(k), b[0].Get(k); x != y {
						p = append(p, fmt.Sprintf("type %d %s: native %q, dmidecode %q", typ, k, x, y))
					}
				}
			}
			cmp := func(what string, a, b any) {
				if !reflect.DeepEqual(a, b) {
					p = append(p, fmt.Sprintf("%s:\n      native    %s\n      dmidecode %s", what, show(a), show(b)))
				}
			}
			cmp("processors", nat.Processors(), tool.Processors())
			cmp("memory arrays", nat.MemoryArrays(), tool.MemoryArrays())
			cmp("DIMMs", nat.MemoryDevices(), tool.MemoryDevices())
			cmp("power supplies", nat.PowerSupplies(), tool.PowerSupplies())
			report("SMBIOS", p, fmt.Sprintf("native and dmidecode agree (%d records)", len(nat.Records)))
		}
	}

	// NICs
	if _, err := exec.LookPath("ethtool"); err != nil {
		skip("NICs", "ethtool not installed: nothing to compare with")
	} else if et, err := netinfo.OpenEthtool(); err != nil {
		report("NICs", []string{"native: " + err.Error()}, "")
	} else {
		var p []string
		devs, _ := filepath.Glob("/sys/class/net/*/device")
		n := 0
		eth := func(args ...string) []byte {
			b, _ := run.Run(ctx, "ethtool", args...)
			return b
		}
		for _, d := range devs {
			name := filepath.Base(filepath.Dir(d))
			if _, err := os.Stat("/sys/class/net/" + name + "/wireless"); err == nil {
				continue
			}
			n++
			if li, err := et.LinkInfo(name); err == nil {
				want := netinfo.ParseLinkInfo(eth(name))
				if !reflect.DeepEqual(li.MaxSpeedMbps, want.MaxSpeedMbps) || li.Port != want.Port || li.Autoneg != want.Autoneg {
					p = append(p, fmt.Sprintf("%s link: native %s, ethtool %s", name, show(li), show(want)))
				}
			}
			if r, err := et.RingParams(name); err == nil {
				if want, ok := netinfo.ParseRing(eth("-g", name)); ok && r != want {
					p = append(p, fmt.Sprintf("%s ring: native %+v, ethtool %+v", name, r, want))
				}
			}
			if q, err := et.RxQueues(name); err == nil {
				if want, ok := netinfo.ParseChannels(eth("-l", name)); ok && q != want {
					p = append(p, fmt.Sprintf("%s queues: native %d, ethtool %d", name, q, want))
				}
			}
			if typ, b, err := et.ModuleEEPROM(name); err == nil {
				nm, tm := netinfo.ParseModuleEEPROM(typ, b), netinfo.ParseModule(eth("-m", name))
				if nm != nil && tm != nil {
					if nm.Vendor != tm.Vendor || nm.PartNumber != tm.PartNumber || nm.Serial != tm.Serial || nm.DateCode != tm.DateCode {
						p = append(p, fmt.Sprintf("%s module: native %s/%s/%s/%s, ethtool %s/%s/%s/%s", name, nm.Vendor, nm.PartNumber, nm.Serial, nm.DateCode,
							tm.Vendor, tm.PartNumber, tm.Serial, tm.DateCode))
					}
					tv := map[string]float64{}
					for _, s := range tm.Sensors {
						if s.Value != nil {
							tv[s.Name] = *s.Value
						}
					}
					for _, s := range nm.Sensors {
						if v, ok := tv[s.Name]; ok && s.Value != nil && math.Abs(v-*s.Value) > 0.2 {
							p = append(p, fmt.Sprintf("%s module %s: native %.2f, ethtool %.2f", name, s.Name, *s.Value, v))
						}
					}
				}
			}
		}
		et.Close()
		report("NICs", p, fmt.Sprintf("native and ethtool agree (%d interfaces)", n))
	}

	// BMC
	nat, err := ipmi.OpenNative()
	tool, terr := ipmi.Find(run)
	switch {
	case err != nil:
		skip("BMC", "native: "+err.Error()+" (no /dev/ipmi0, or not root)")
	case terr != nil || tool.Tool != ipmi.IPMITool:
		skip("BMC", "ipmitool not installed: nothing to compare with")
	default:
		var p []string
		v1, f1, _ := nat.MCInfo(ctx)
		v2, f2, _ := tool.MCInfo(ctx)
		if f1 != f2 {
			p = append(p, fmt.Sprintf("firmware: native %q, ipmitool %q (vendor %q / %q)", f1, f2, v1, v2))
		}
		ns, e1 := nat.Sensors(ctx)
		ts, e2 := tool.Sensors(ctx)
		if e1 != nil || e2 != nil {
			p = append(p, fmt.Sprintf("sensors: native %v, ipmitool %v", e1, e2))
		} else {
			byName := map[string]ipmi.Sensor{}
			for _, s := range ts {
				byName[s.Name] = s
			}
			var only []string
			for _, s := range ns {
				t, ok := byName[s.Name]
				if !ok {
					only = append(only, s.Name)
					continue
				}
				delete(byName, s.Name)
				switch {
				case (s.Value == nil) != (t.Value == nil):
					p = append(p, fmt.Sprintf("sensor %s: native %s, ipmitool %s", s.Name, val(s.Value, s.Unit), val(t.Value, t.Unit)))
				case s.Value != nil && math.Abs(*s.Value-*t.Value) > math.Max(0.5, math.Abs(*t.Value)*0.03):
					p = append(p, fmt.Sprintf("sensor %s: native %s, ipmitool %s", s.Name, val(s.Value, s.Unit), val(t.Value, t.Unit)))
				case s.Value != nil && (s.Unit != t.Unit || s.Kind != t.Kind):
					p = append(p, fmt.Sprintf("sensor %s: native unit %q kind %v, ipmitool %q %v", s.Name, s.Unit, s.Kind, t.Unit, t.Kind))
				case s.Status != t.Status && t.Status != 0:
					p = append(p, fmt.Sprintf("sensor %s: native status %v, ipmitool %v", s.Name, s.Status, t.Status))
				}
			}
			var missing []string
			for n := range byName {
				missing = append(missing, n)
			}
			sort.Strings(missing)
			if len(only) > 0 {
				p = append(p, "only native: "+strings.Join(only, ", "))
			}
			if len(missing) > 0 {
				p = append(p, "only ipmitool: "+strings.Join(missing, ", "))
			}
		}
		if a, b := countSEL(nat, ctx), countSEL(tool, ctx); a != b {
			p = append(p, fmt.Sprintf("SEL: native %s, ipmitool %s", a, b))
		}
		fa, _ := nat.FRUs(ctx)
		fb, _ := tool.FRUs(ctx)
		if len(fa) > 0 && len(fb) > 0 && (fa[0].Serial != fb[0].Serial || fa[0].MfgDate != fb[0].MfgDate) {
			p = append(p, fmt.Sprintf("FRU 0: native %s, ipmitool %s", show(fa[0]), show(fb[0])))
		}
		la, _ := nat.LanPrint(ctx)
		lb, _ := tool.LanPrint(ctx)
		if x, y := netinfo.ParseIPMILan(la), netinfo.ParseIPMILan(lb); x != nil && y != nil && (x.IP != y.IP || x.MAC != y.MAC) {
			p = append(p, fmt.Sprintf("LAN: native %s, ipmitool %s", show(x), show(y)))
		}
		report("BMC", p, fmt.Sprintf("native and ipmitool agree (%d sensors)", len(ns)))
	}
	if diffs > 0 {
		fmt.Fprintln(stdout, "\nSend this output to the developers: the native readers need a fix for this hardware.")
		return 1
	}
	return 0
}

func countSEL(c *ipmi.Client, ctx context.Context) string {
	s, err := c.SEL(ctx)
	if err != nil {
		return err.Error()
	}
	last := ""
	if n := len(s.Entries); n > 0 {
		e := s.Entries[n-1]
		last = fmt.Sprintf(", last %s %s %q", e.SensorType, e.Sensor, e.Event)
	}
	return fmt.Sprintf("%d entries%s", len(s.Entries), last)
}

func val(v *float64, unit string) string {
	if v == nil {
		return "no reading"
	}
	return fmt.Sprintf("%.2f %s", *v, unit)
}

func show(v any) string {
	s := fmt.Sprintf("%+v", deref(v))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func deref(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer && !rv.IsNil() {
		return rv.Elem().Interface()
	}
	return v
}
