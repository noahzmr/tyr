package netbox

import (
	"testing"

	"github.com/noahzmr/tyr/internal/model"
)

func TestManufacturer(t *testing.T) {
	for _, c := range []struct{ vendor, model, want string }{
		{"GenuineIntel", "", "Intel"},
		{"Intel Corporation", "", "Intel"},
		{"AuthenticAMD", "", "AMD"},
		{"Advanced Micro Devices, Inc. [AMD/ATI]", "", "AMD"},
		{"SUPERMICRO", "", "Supermicro"},
		{"Supermicro", "", "Supermicro"},
		{"Super Micro Computer, Inc.", "", "Supermicro"},
		{"Hewlett Packard Enterprise", "", "HPE"},
		{"HP", "", "HP"},
		{"Dell Inc.", "", "Dell"},
		{"FINISAR CORP.", "", "Finisar"},
		{"Micro-Star International Co., Ltd.", "", "MSI"},
		{"ASUSTeK COMPUTER INC.", "", "ASUS"},
		{"LENOVO", "", "Lenovo"},
		{"QEMU", "", "QEMU"},
		{"Samsung", "", "Samsung"},
		{"", "SAMSUNG MZQL21T9HCJR-00A07", "Samsung"},
		{"", "ST4000NM000A-2HZ100", "Seagate"},
		{"ATA", "WDC WD40EFRX-68N32N0", "Western Digital"},
		{"", "MTFDDAK960TDS", "Micron"},
		{"", "KIOXIA KCD81RUG1T92", "Kioxia"},
		{"", "something", ""},
	} {
		if got := Manufacturer(c.vendor, c.model); got != c.want {
			t.Errorf("Manufacturer(%q, %q) = %q, want %q", c.vendor, c.model, got, c.want)
		}
	}
}

func TestMergeCards(t *testing.T) {
	in := []model.Component{
		{Category: "nic", Location: "eno1 03:00.0", Model: "I350", Serial: "AB", Detail: "1 Gbit/s"},
		{Category: "cpu", Location: "CPU1"},
		{Category: "nic", Location: "eno2 03:00.1", Model: "I350", Serial: "AB", Detail: "1 Gbit/s", Status: model.StatusCrit},
		{Category: "nic", Location: "ens1f0 4b:00.0", Model: "E810", Serial: ""},
	}
	out := mergeCards(in)
	if len(out) != 3 {
		t.Fatalf("%d items: %+v", len(out), out)
	}
	n := out[0]
	if n.Location != "03:00" || n.Detail != "1 Gbit/s · ports eno1, eno2" || n.Status != model.StatusCrit || itemName(n) != "NIC 03:00" {
		t.Errorf("merged %+v name %q", n, itemName(n))
	}
	if out[2].Location != "ens1f0 4b:00.0" {
		t.Errorf("NIC without serial changed: %+v", out[2])
	}
}
