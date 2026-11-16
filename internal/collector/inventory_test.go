package collector_test

import (
	"context"
	"strings"
	"testing"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/collector/all"
	"github.com/noahzmr/tyr/internal/fixture"
	"github.com/noahzmr/tyr/internal/model"
)

func TestInventoryDegradedServer(t *testing.T) {
	env, _, err := fixture.Load("../../testdata/fixtures/server-degraded", collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	snap := collector.Collect(context.Background(), env, all.Collectors(), nil)
	count := map[string]int{}
	serials := map[string]string{}
	for _, c := range snap.Inventory {
		count[c.Category]++
		serials[c.Category+" "+c.Location] = c.Serial
	}
	want := map[string]int{"system": 1, "chassis": 1, "board": 1, "bmc": 1, "psu": 2, "fru": 3, "cpu": 2, "dimm": 16, "disk": 7,
		"raid_controller": 1, "raid_drive": 4, "nic": 4, "transceiver": 2, "gpu": 2}
	for k, v := range want {
		if count[k] != v {
			t.Errorf("%s: %d, want %d", k, count[k], v)
		}
	}
	for loc, sn := range map[string]string{
		"system srv-degraded-01": "S412345X3A01234", "chassis Rack Mount Chassis": "C1160LL12AB0123", "board mainboard": "OM21AS012345",
		"psu PSU2": "P1K22CL21123452", "cpu CPU2": "PPIN 0x2f1a9c4e5b6d7001", "dimm P2-DIMMB1": "3A5F0012", "disk /dev/sdd": "WJG0A004",
		"raid_drive c0 252:2": "ZFL4A002", "nic ens1f1 4b:00.1": "B49691A00010", "transceiver ens1f1": "X7A0B11",
		"gpu GPU1 ca:00.0": "1324420012346", "raid_controller controller 0": "SK94012345",
	} {
		if serials[loc] != sn {
			t.Errorf("%s: serial %q, want %q", loc, serials[loc], sn)
		}
	}
	for _, c := range snap.Inventory {
		if c.Category == "disk" && c.Location == "/dev/sda" && !strings.Contains(c.Detail, "naa.5000c500e4a10001") {
			t.Errorf("WWN missing: %+v", c)
		}
		if c.Category == "bmc" && c.Firmware != "1.40" {
			t.Errorf("bmc %+v", c)
		}
	}
	if snap.System.UUID == "" || snap.System.ChassisType != "Rack Mount Chassis" {
		t.Errorf("system ids %+v", snap.System)
	}
	_ = model.InventoryCategories
}
