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
