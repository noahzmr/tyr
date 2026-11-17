package model

import (
	"fmt"
	"strings"
)

// Component is one row of the hardware inventory.
type Component struct {
	Category   string `json:"category"` // system, chassis, board, bmc, cpu, dimm, disk, volume, raid_controller, raid_drive, nic, transceiver, gpu, npu, device (BMC device inventory), psu, fru
	Location   string `json:"location"` // slot, socket, device name, PCI address
	Vendor     string `json:"vendor,omitempty"`
	Model      string `json:"model,omitempty"`
	PartNumber string `json:"part_number,omitempty"`
	Serial     string `json:"serial,omitempty"`
	Firmware   string `json:"firmware,omitempty"`
	Detail     string `json:"detail,omitempty"` // size, speed, capacity
	Status     Status `json:"status"`
}

// InventoryCategories in display order.
var InventoryCategories = []string{"system", "chassis", "board", "bmc", "cpu", "dimm", "disk", "volume", "raid_controller",
	"raid_drive", "nic", "transceiver", "gpu", "npu", "device", "psu", "fru"}

func gib(b uint64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.1f TiB", float64(b)/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.0f GiB", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%.0f MiB", float64(b)/(1<<20))
}

func prefixed(p, v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	return p + v
}

func si(b uint64) string {
	if b >= 1e12 {
		return fmt.Sprintf("%.2g TB", float64(b)/1e12)
	}
	return fmt.Sprintf("%.0f GB", float64(b)/1e9)
}

// BuildInventory collects the identifiers of all components in s.
func BuildInventory(s *Snapshot) []Component {
	var out []Component
	add := func(c Component) {
		c.Serial = strings.TrimSpace(c.Serial)
		out = append(out, c)
	}
	if x := s.System; x != nil {
		add(Component{Category: "system", Location: s.Hostname, Vendor: x.Vendor, Model: x.Product, PartNumber: x.SKU, Serial: x.Serial,
			Firmware: strings.TrimSpace("BIOS " + x.BIOSVersion + " " + x.BIOSDate), Detail: prefixed("UUID ", x.UUID), Status: x.Status})
		if x.ChassisSerial != "" || x.ChassisType != "" {
			add(Component{Category: "chassis", Location: x.ChassisType, Vendor: x.ChassisVendor, Serial: x.ChassisSerial, Detail: assetTag(x.ChassisAsset)})
		}
		if x.BoardProduct != "" || x.BoardSerial != "" {
			add(Component{Category: "board", Location: "mainboard", Vendor: x.BoardVendor, Model: x.BoardProduct, Serial: x.BoardSerial})
		}
		if x.BMCFirmware != "" {
			add(Component{Category: "bmc", Location: "BMC", Vendor: x.BMCVendor, Firmware: x.BMCFirmware})
		}
		for _, p := range x.PSUs {
			d := ""
			if p.MaxWatts != nil {
				d = fmt.Sprintf("%.0f W", *p.MaxWatts)
			}
			st := StatusOK
			if !p.Present {
				st = StatusUnknown
			}
			add(Component{Category: "psu", Location: p.Location, Vendor: p.Manufacturer, Model: firstOf(p.Model, p.Name), Serial: p.Serial,
				Firmware: p.Revision, Detail: strings.TrimSpace(d + " " + p.StatusText), Status: st})
		}
		for _, f := range x.FRUs {
			add(Component{Category: "fru", Location: f.Name, Vendor: f.Manufacturer, Model: f.Product, PartNumber: f.PartNumber, Serial: f.Serial})
		}
	}
	if x := s.CPU; x != nil {
		for _, k := range x.Sockets {
			loc := k.Designation
			if loc == "" {
				loc = fmt.Sprintf("socket %d", k.ID)
			}
			if k.Populated != nil && !*k.Populated {
				add(Component{Category: "cpu", Location: loc, Detail: "empty socket", Status: StatusOK})
				continue
			}
			model := k.Version
			if model == "" {
				model = x.Model
			}
			d := ""
			if k.Cores != nil && k.Threads != nil {
				d = fmt.Sprintf("%d cores / %d threads", *k.Cores, *k.Threads)
			}
			serial := k.Serial
			if serial == "" && k.PPIN != "" {
				serial = "PPIN " + k.PPIN
			}
			add(Component{Category: "cpu", Location: loc, Vendor: x.Vendor, Model: model, PartNumber: k.PartNumber, Serial: serial,
				Firmware: "µcode " + x.Microcode, Detail: d, Status: k.Status})
		}
	}
	if x := s.Memory; x != nil {
		for _, d := range x.DIMMs {
			if !d.Populated {
				continue
			}
			sp := ""
			if d.SpeedMTs != nil {
				sp = fmt.Sprintf(" %d MT/s", *d.SpeedMTs)
			}
			add(Component{Category: "dimm", Location: d.Locator, Vendor: d.Manufacturer, Model: strings.TrimSpace(d.Type + " " + d.FormFactor),
				PartNumber: d.PartNumber, Serial: d.Serial, Detail: gib(d.SizeBytes) + sp, Status: d.Status})
		}
	}
	if x := s.Storage; x != nil {
		for _, d := range x.Disks {
			det := si(d.SizeBytes) + " " + strings.ToUpper(d.Kind)
			if d.WWN != "" {
				det += " " + d.WWN
			}
			cat := "disk"
			if r := d.Remote; r != nil { // a storage system's volume: where it comes from
				cat = "volume"
				det = strings.TrimSpace(strings.ToUpper(r.Transport) + " " + r.Target + " " + r.Portal)
			}
			add(Component{Category: cat, Location: d.Path, Vendor: d.Vendor, Model: d.Model, Serial: d.Serial, Firmware: d.Firmware, Detail: det, Status: d.Status})
		}
		osDisk := map[string]bool{}
		for _, d := range x.Disks {
			if k := serialKey(d.Serial); k != "" {
				osDisk[k] = true
			}
		}
		for _, c := range x.Controllers {
			add(Component{Category: "raid_controller", Location: strings.TrimSpace(fmt.Sprintf("controller %d %s", c.Index, c.Location)), Model: c.Model,
				Serial: c.Serial, Firmware: c.Firmware, Detail: map[bool]string{true: "via the BMC (Redfish)"}[c.Tool == "redfish"], Status: c.Status})
			for _, p := range c.PDs {
				if osDisk[serialKey(p.Serial)] {
					continue // the host sees the drive itself (JBOD, HBA mode): listed as a disk
				}
				add(Component{Category: "raid_drive", Location: fmt.Sprintf("c%d %s", c.Index, p.Slot), Model: p.Model, Serial: p.Serial,
					Firmware: p.Firmware, Detail: strings.TrimSpace(p.SizeText + " " + p.Interface + " " + p.Media + " " + p.State), Status: p.Status})
			}
		}
	}
	if x := s.Network; x != nil {
		for _, n := range x.Interfaces {
			if n.PCIAddress == "" {
				continue
			}
			add(Component{Category: "nic", Location: n.Name + " " + strings.TrimPrefix(n.PCIAddress, "0000:"), Vendor: n.Vendor, Model: n.Model,
				PartNumber: n.PartNumber, Serial: n.Serial, Firmware: n.Firmware, Detail: "MAC " + n.MAC, Status: n.Status})
			if m := n.Module; m != nil {
				add(Component{Category: "transceiver", Location: n.Name, Vendor: m.Vendor, Model: firstOf(m.Type, m.Identifier),
					PartNumber: m.PartNumber, Serial: m.Serial, Detail: m.Wavelength, Status: m.Status})
			}
		}
	}
	if x := s.Accel; x != nil {
		for _, list := range [][]Accelerator{x.GPUs, x.NPUs} {
			for _, a := range list {
				d := ""
				if a.MemTotalBytes != nil {
					d = gib(*a.MemTotalBytes)
				}
				if a.UUID != "" {
					d = strings.TrimSpace(d + " " + a.UUID)
				}
				add(Component{Category: a.Kind, Location: fmt.Sprintf("%s%d %s", strings.ToUpper(a.Kind), a.Index, strings.TrimPrefix(a.PCIAddress, "0000:")),
					Vendor: a.Vendor, Model: a.Model, PartNumber: a.PartNumber, Serial: a.Serial, Firmware: a.Firmware, Detail: d, Status: a.Status})
			}
		}
	}
	if x := s.System; x != nil {
		out = addDevices(out, x.Devices)
		fillFirmware(out, x.Firmware)
	}
	return out
}

func serialKey(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// addDevices merges the BMC's device inventory: a device whose serial is
// already listed (a RAID controller, a NIC, a GPU) lends that component
// its part number and firmware; the others (cards the host lists only by
// PCI ID, the storage battery, embedded controllers) become "device" rows.
func addDevices(out []Component, devs []Device) []Component {
	bySerial := map[string]int{}
	for i, c := range out {
		if k := serialKey(c.Serial); k != "" {
			bySerial[k] = i
		}
	}
	for _, d := range devs {
		if k := serialKey(d.Serial); k != "" {
			if i, ok := bySerial[k]; ok {
				c := &out[i]
				if c.PartNumber == "" {
					c.PartNumber = d.PartNumber
				}
				if c.Firmware == "" {
					c.Firmware = d.Firmware
				}
				continue
			}
		}
		det := d.Type
		if d.ProductPart != "" {
			det = strings.TrimSpace(det + " option " + d.ProductPart)
		}
		if d.Model != "" && d.Model != d.Name {
			det = strings.TrimSpace(det + " " + d.Model)
		}
		out = append(out, Component{Category: "device", Location: d.Location, Vendor: d.Manufacturer, Model: d.Name, PartNumber: d.PartNumber,
			Serial: strings.TrimSpace(d.Serial), Firmware: d.Firmware, Detail: det, Status: d.Status})
	}
	return out
}

// fillFirmware gives a component without a firmware version the version
// of the BMC's firmware entry with exactly its model name.
func fillFirmware(out []Component, fw []FirmwareItem) {
	byName := map[string]string{}
	for _, f := range fw {
		byName[strings.ToLower(f.Name)] = f.Version
	}
	for i := range out {
		if out[i].Firmware != "" || out[i].Model == "" {
			continue
		}
		if v, ok := byName[strings.ToLower(out[i].Model)]; ok {
			out[i].Firmware = v
		}
	}
}

func assetTag(s string) string {
	if s == "" {
		return ""
	}
	return "asset " + s
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
