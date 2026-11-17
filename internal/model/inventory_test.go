package model

import "testing"

func TestBuildInventoryEmptyAndPlaceholders(t *testing.T) {
	if len(BuildInventory(&Snapshot{})) != 0 {
		t.Error("empty snapshot must give empty inventory")
	}
	pop := false
	s := &Snapshot{Hostname: "h", System: &System{Vendor: "V", Product: "P"},
		CPU: &CPU{Vendor: "GenuineIntel", Model: "Xeon", Sockets: []CPUSocket{{ID: 0, Designation: "CPU1", PPIN: "0xabc"}, {ID: -1, Designation: "CPU2", Populated: &pop}}}}
	inv := BuildInventory(s)
	if len(inv) != 3 || inv[0].Detail != "" || inv[1].Serial != "PPIN 0xabc" || inv[2].Detail != "empty socket" {
		t.Errorf("%+v", inv)
	}
}

// The BMC's devices and firmware: a device with the serial of a listed
// controller lends it its part number; the others become "device" rows; a
// drive the host sees itself is not listed twice; firmware fills a gap.
func TestBuildInventoryBMC(t *testing.T) {
	s := &Snapshot{Hostname: "h",
		System: &System{
			Devices: []Device{
				{Name: "HPE Smart Array P408i-a SR Gen10", Location: "Embedded RAID", PartNumber: "836260-001", Serial: "PEY01 ", Firmware: "3.53"},
				{Name: "HPE Smart Storage Battery 1", Location: "Embedded", Type: "Smart Storage Battery", PartNumber: "878643-001", Serial: "6EZ01"},
			},
			Firmware: []FirmwareItem{{Name: "MZ7KH960HAJR", Version: "HXM7"}},
		},
		Storage: &Storage{
			Disks: []Disk{{Name: "sda", Path: "/dev/sda", Serial: "S4N01"}},
			Controllers: []RAIDController{{Index: 0, Model: "HPE Smart Array P408i-a SR Gen10", Serial: "PEY01", Location: "Slot 0", Tool: "redfish",
				PDs: []PhysicalDrive{{Slot: "1I:1:3", Serial: "S4N01"}, {Slot: "1I:1:4", Model: "MZ7KH960HAJR", Serial: "S47N02"}}}},
		},
	}
	inv := BuildInventory(s)
	byCat := map[string][]Component{}
	for _, c := range inv {
		byCat[c.Category] = append(byCat[c.Category], c)
	}
	if rc := byCat["raid_controller"]; len(rc) != 1 || rc[0].PartNumber != "836260-001" || rc[0].Location != "controller 0 Slot 0" || rc[0].Detail != "via the BMC (Redfish)" {
		t.Errorf("controller %+v", rc)
	}
	if pd := byCat["raid_drive"]; len(pd) != 1 || pd[0].Serial != "S47N02" || pd[0].Firmware != "HXM7" {
		t.Errorf("drives %+v", pd)
	}
	if d := byCat["device"]; len(d) != 1 || d[0].Model != "HPE Smart Storage Battery 1" || d[0].PartNumber != "878643-001" || d[0].Serial != "6EZ01" {
		t.Errorf("devices %+v", d)
	}
}
