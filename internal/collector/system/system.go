// Package system collects host identity (vendor, model, serial, BIOS, board,
// BMC), kernel, uptime, chassis fans and inlet/exhaust temperatures.
package system

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/ipmi"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

// Collector is the system collector.
type Collector struct{}

// New returns a system collector.
func New() *Collector { return &Collector{} }

func (*Collector) ID() model.ModuleID { return model.ModSystem }

func (c *Collector) Collect(ctx context.Context, env *collector.Env, prevSec model.Section) (model.Section, error) {
	f := env.FS
	out := &model.System{Fans: []model.Sensor{}, Temps: []model.Sensor{}, PSUs: []model.PSUFRU{}, FRUs: []model.FRU{}}
	h := &out.Header
	prev, _ := prevSec.(*model.System)

	if prev != nil {
		identity := *prev
		out.Vendor, out.Product, out.Serial = identity.Vendor, identity.Product, identity.Serial
		out.BIOSVendor, out.BIOSVersion, out.BIOSDate = identity.BIOSVendor, identity.BIOSVersion, identity.BIOSDate
		out.BoardVendor, out.BoardProduct, out.BoardSerial = identity.BoardVendor, identity.BoardProduct, identity.BoardSerial
		out.BMCFirmware, out.Kernel = identity.BMCFirmware, identity.Kernel
		out.BMCVendor, out.UUID, out.SKU, out.AssetTag = identity.BMCVendor, identity.UUID, identity.SKU, identity.AssetTag
		out.ChassisVendor, out.ChassisType, out.ChassisSerial, out.ChassisAsset = identity.ChassisVendor, identity.ChassisType, identity.ChassisSerial, identity.ChassisAsset
		out.PSUs, out.FRUs = identity.PSUs, identity.FRUs
		h.Sources = append(h.Sources, prev.Sources...)
	} else {
		c.identity(ctx, env, out)
	}

	if s, err := hostfs.String(f, "/proc/uptime"); err == nil {
		if fs := strings.Fields(s); len(fs) > 0 {
			if v, err := strconv.ParseFloat(fs[0], 64); err == nil {
				out.UptimeSeconds = &v
			}
		}
	}

	// Fans and ambient temperatures: hwmon first (Super I/O chips), then BMC.
	for _, chip := range sysfs.ReadHwmon(f) {
		for _, s := range chip.Sensors {
			if s.Kind == model.KindFan && s.Input != nil && !s.Fault {
				sen := model.Sensor{Name: fmt.Sprintf("%s %s", chip.Name, s.Label), Kind: model.KindFan, Value: s.Input,
					Unit: model.UnitRPM, Thresholds: s.Thresholds(), Source: "hwmon"}
				check.EvalSensor(&sen)
				if s.Alarm {
					sen.Status = model.Worst(sen.Status, model.StatusWarn)
				}
				out.Fans = append(out.Fans, sen)
			}
		}
	}
	if !env.IsVM() {
		if sens, st := env.IPMISensors(ctx); sens != nil {
			c.bmcSensors(env, out, sens)
		} else if len(out.Fans) == 0 {
			h.Sources = appendSource(h.Sources, st)
		}
	}
	for _, s := range out.Fans {
		switch {
		case s.Status == model.StatusCrit:
			h.Find(model.StatusCrit, "fan_failed", s.Name, s.Source, fmt.Sprintf("%s failed or below critical speed (%s)", s.Name, rpm(s.Value)))
		case s.Status == model.StatusWarn:
			h.Find(model.StatusWarn, "fan_low", s.Name, s.Source, fmt.Sprintf("%s outside normal range (%s)", s.Name, rpm(s.Value)))
		}
	}
	for _, s := range out.Temps {
		if s.Status >= model.StatusWarn && s.Value != nil {
			h.Find(s.Status, "ambient_temp", s.Name, s.Source, fmt.Sprintf("%s at %s", s.Name, check.FmtTemp(*s.Value)))
		}
	}

	h.Summary = strings.TrimSpace(out.Vendor + " " + out.Product)
	if out.Vendor != "" && strings.HasPrefix(strings.ToLower(out.Product), strings.ToLower(out.Vendor)) {
		h.Summary = out.Product
	}
	if h.Summary == "" {
		h.Summary = "unknown system"
	}
	h.Rollup()
	return out, nil
}

func appendSource(ss []model.SourceState, s model.SourceState) []model.SourceState {
	for _, x := range ss {
		if x.Name == s.Name {
			return ss
		}
	}
	return append(ss, s)
}

func rpm(v *float64) string {
	if v == nil {
		return "no reading"
	}
	return fmt.Sprintf("%.0f RPM", *v)
}

func (c *Collector) identity(ctx context.Context, env *collector.Env, out *model.System) {
	f := env.FS
	h := &out.Header
	tables, st := env.DMI(ctx)
	h.Sources = append(h.Sources, st)
	if tables != nil {
		if r := tables.ByType(1); len(r) > 0 {
			out.Vendor, out.Product, out.Serial = r[0].Get("Manufacturer"), r[0].Get("Product Name"), r[0].Get("Serial Number")
			out.UUID, out.SKU = r[0].Get("UUID"), r[0].Get("SKU Number")
		}
		if r := tables.ByType(3); len(r) > 0 {
			out.ChassisVendor, out.ChassisType = r[0].Get("Manufacturer"), r[0].Get("Type")
			out.ChassisSerial, out.ChassisAsset = r[0].Get("Serial Number"), r[0].Get("Asset Tag")
		}
		for _, p := range tables.PowerSupplies() {
			out.PSUs = append(out.PSUs, model.PSUFRU{Location: p.Location, Name: p.Name, Manufacturer: p.Manufacturer, Model: p.Model,
				Serial: p.Serial, Revision: p.Revision, MaxWatts: p.MaxWatts, StatusText: p.Status, Present: p.Present})
		}
		if r := tables.ByType(0); len(r) > 0 {
			out.BIOSVendor, out.BIOSVersion, out.BIOSDate = r[0].Get("Vendor"), r[0].Get("Version"), r[0].Get("Release Date")
		}
		if r := tables.ByType(2); len(r) > 0 {
			out.BoardVendor, out.BoardProduct, out.BoardSerial = r[0].Get("Manufacturer"), r[0].Get("Product Name"), r[0].Get("Serial Number")
			if out.AssetTag == "" {
				out.AssetTag = r[0].Get("Asset Tag")
			}
		}
	}
	// /sys/class/dmi/id: vendor/product/BIOS are world-readable, serials root-only.
	id := func(name string) string { return hostfs.StringOr(f, "/sys/class/dmi/id/"+name, "") }
	fill := func(dst *string, v string) {
		if *dst == "" && !placeholder(v) {
			*dst = v
		}
	}
	fill(&out.Vendor, id("sys_vendor"))
	fill(&out.Product, id("product_name"))
	fill(&out.Serial, id("product_serial"))
	fill(&out.BIOSVendor, id("bios_vendor"))
	fill(&out.BIOSVersion, id("bios_version"))
	fill(&out.BIOSDate, id("bios_date"))
	fill(&out.BoardVendor, id("board_vendor"))
	fill(&out.BoardProduct, id("board_name"))
	fill(&out.BoardSerial, id("board_serial"))
	fill(&out.UUID, id("product_uuid"))
	fill(&out.SKU, id("product_sku"))
	fill(&out.ChassisVendor, id("chassis_vendor"))
	fill(&out.ChassisSerial, id("chassis_serial"))
	fill(&out.ChassisAsset, id("chassis_asset_tag"))
	// BMC: firmware and FRU inventory (PSU, board and product serials).
	if cl, _ := env.IPMI(); cl != nil {
		if v, fw, err := cl.MCInfo(ctx); err == nil {
			out.BMCVendor, out.BMCFirmware = v, fw
		}
		if frus, err := cl.FRUs(ctx); err == nil {
			for _, f := range frus {
				out.FRUs = append(out.FRUs, model.FRU{Name: f.Name, Manufacturer: f.Manufacturer, Product: f.Product,
					PartNumber: f.PartNumber, Serial: f.Serial, ChassisSerial: f.ChassisSerial, ChassisPart: f.ChassisPart})
				fill(&out.ChassisSerial, f.ChassisSerial)
			}
			if len(frus) > 0 {
				fill(&out.Serial, frus[0].Serial)
			}
		}
	}
	if v, err := hostfs.String(f, "/proc/sys/kernel/osrelease"); err == nil {
		out.Kernel = v
	} else if v, err := hostfs.String(f, "/proc/version"); err == nil {
		if fs := strings.Fields(v); len(fs) >= 3 {
			out.Kernel = fs[2]
		}
	}
}

func placeholder(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "to be filled by o.e.m.", "default string", "system product name", "system manufacturer", "not specified", "none", "redacted",
		"chassis serial number", "base board serial number", "system serial number", "0123456789", "03000200-0400-0500-0006-000700080009":
		return true
	}
	return false
}

func (c *Collector) bmcSensors(env *collector.Env, out *model.System, sens []ipmi.Sensor) {
	haveHwmonFans := len(out.Fans) > 0
	for _, s := range sens {
		n := strings.ToLower(s.Name)
		switch {
		case s.Kind == model.KindFan && !haveHwmonFans:
			if s.Value == nil && s.Status == model.StatusUnknown {
				continue // unused fan header
			}
			out.Fans = append(out.Fans, s.Model())
		case s.Kind == model.KindTemperature && (strings.Contains(n, "inlet") || strings.Contains(n, "exhaust") ||
			strings.Contains(n, "ambient") || strings.Contains(n, "system temp") || strings.Contains(n, "outlet")):
			m := s.Model()
			if m.Thresholds == nil {
				m.Thresholds = check.TempThresholds(nil, check.Defaults{WarnTempC: 40, CritTempC: 45})
				m.Status = model.Worst(s.Status, check.Eval(m.Value, m.Thresholds))
			}
			out.Temps = append(out.Temps, m)
		}
	}
}
