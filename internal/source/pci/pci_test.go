package pci

import (
	"os"
	"testing"

	"github.com/noahzmr/tyr/internal/hostfs"
)

const fx = "../../../testdata/fixtures/"

func TestVPDFromFixture(t *testing.T) {
	f := hostfs.New(fx + "server-degraded/root")
	v, ok := ReadVPD(f, "/sys/devices/pci0000:00/0000:4b:00.0")
	if !ok || v.Name != "Intel(R) Ethernet Network Adapter E810-XXV-2" || v.PartNumber != "K91258-006" || v.Serial != "B49691A00010" || v.EC != "A01" {
		t.Errorf("%+v %v", v, ok)
	}
	if _, ok := ReadVPD(f, "/sys/devices/pci0000:00/0000:4b:00.1"); ok {
		t.Error("function 1 has no VPD in the fixture")
	}
}

func TestVPDMalformed(t *testing.T) {
	for _, b := range [][]byte{{0x82}, {0x82, 0xff, 0xff, 'x'}, {0x90, 0x05, 0x00, 'S', 'N', 0x40, 'a'}, {0x78}, {}} {
		_ = ParseVPD(b) // must not panic
	}
	v := ParseVPD([]byte{0x82, 3, 0, 'N', 'I', 'C', 0x90, 7, 0, 'S', 'N', 4, 'A', 'B', 'C', 'D', 0x78})
	if v.Name != "NIC" || v.Serial != "ABCD" {
		t.Errorf("%+v", v)
	}
}

func TestListAndNames(t *testing.T) {
	f := hostfs.New(fx + "laptop-amd-nonroot/root")
	devs := List(f)
	var gpu *Device
	for i := range devs {
		if devs[i].Address == "0000:c4:00.0" {
			gpu = &devs[i]
		}
	}
	if gpu == nil || gpu.Driver != "amdgpu" || gpu.ClassBase() != 0x03 || *gpu.LinkWidth != 16 {
		t.Fatalf("%+v", gpu)
	}
	v, d := Names(f, gpu.Vendor, gpu.Device)
	if v != "AMD/ATI" || d != "Krackan [Radeon 840M / 860M Graphics]" {
		t.Errorf("%q %q", v, d)
	}
	if v, d := Names(hostfs.New(os.TempDir()), 0x1234, 0x5678); v != "0x1234" || d != "device 0x5678" {
		t.Errorf("fallback %q %q", v, d)
	}
	if Gen("16.0 GT/s PCIe") != 4 || Gen("2.5 GT/s PCIe") != 1 {
		t.Error("gen")
	}
}
