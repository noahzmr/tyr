package redfish

import (
	"context"
	"testing"

	"github.com/noahzmr/tyr/internal/model"
)

// An HPE ProLiant Gen10 (iLO 5) behind a Smart Array P408i-a, shaped like
// real recordings (serials made up): the HPE and the standard view of the
// same drives, a failed drive that reports only its bay and condition, a
// worn SSD, a logical drive, the device inventory and firmware inventory.
var iloHW = mapGetter{
	"/redfish/v1/Systems": `{"Members":[{"@odata.id":"/redfish/v1/Systems/1/"}]}`,
	"/redfish/v1/Systems/1/SmartStorage/ArrayControllers": `{"Members":[{"@odata.id":"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/"}]}`,
	"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/": `{"@odata.id":"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/","Model":"HPE Smart Array P408i-a SR Gen10","SerialNumber":"PEYHC0TEST0001 ",
		"Location":"Slot 0","FirmwareVersion":{"Current":{"VersionString":"3.53"}},"BackupPowerSourceStatus":"Present","Status":{"Health":"Warning","State":"Enabled"},
		"Links":{"PhysicalDrives":{"@odata.id":"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/"},"LogicalDrives":{"@odata.id":"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/LogicalDrives/"}}}`,
	"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/": `{"Members":[{"@odata.id":"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/0/"},{"@odata.id":"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/1/"},{"@odata.id":"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/2/"},{"@odata.id":"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/3/"}]}`,
	"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/0/": `{"Name":"HpeSmartStorageDiskDrive","Model":"VK000960GWSRT","SerialNumber":"S4NBTEST000001","Location":"1I:1:3","CapacityMiB":915715,
		"InterfaceType":"SATA","MediaType":"SSD","FirmwareVersion":{"Current":{"VersionString":"HPG4"}},"SSDEnduranceUtilizationPercentage":7,"PowerOnHours":20468,
		"CurrentTemperatureCelsius":30,"DiskDriveStatusReasons":["None"],"Status":{"Health":"OK","State":"Enabled"}}`,
	"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/1/": `{"Name":"HpeSmartStorageDiskDrive","Model":"MZ7KH960HAJR","SerialNumber":"S47NTEST000002","Location":"1I:1:4","CapacityMiB":915715,
		"InterfaceType":"SATA","MediaType":"SSD","SSDEnduranceUtilizationPercentage":96,"PowerOnHours":41000,"Status":{"Health":"OK","State":"Enabled"}}`,
	"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/2/":    `{"Name":"HpeSmartStorageDiskDrive","Model":null,"SerialNumber":null,"Location":"2I:1:5","DiskDriveStatusReasons":["DriveFailure"],"Status":{"Health":"Critical","State":"Enabled"}}`,
	"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/3/":    `{"Name":"HpeSmartStorageDiskDrive","Model":"VK000960GWSRT","SerialNumber":"","Location":"2I:1:6","Status":{"Health":"OK","State":"Enabled"}}`,
	"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/LogicalDrives/":   `{"Members":[{"@odata.id":"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/LogicalDrives/1/"}]}`,
	"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/LogicalDrives/1/": `{"LogicalDriveNumber":1,"LogicalDriveName":"001","Raid":"1","CapacityMiB":915683,"Status":{"Health":"Warning","State":"Enabled"}}`,
	"/redfish/v1/Systems/1/Storage":                                          `{"Members":[{"@odata.id":"/redfish/v1/Systems/1/Storage/DE07B000/"}]}`,
	"/redfish/v1/Systems/1/Storage/DE07B000/": `{"Name":"HPE Smart Array P408i-a SR Gen10","StorageControllers":[{"Model":"HPE Smart Array P408i-a SR Gen10","SerialNumber":"PEYHC0TEST0001","FirmwareVersion":"3.53"}],
		"Drives":[{"@odata.id":"/redfish/v1/Systems/1/Storage/DE07B000/Drives/2/"},{"@odata.id":"/redfish/v1/Systems/1/Storage/DE07B000/Drives/9/"}]}`,
	"/redfish/v1/Systems/1/Storage/DE07B000/Drives/2/": `{"Name":"960GB 6G SATA SSD","Model":"VK000960GWSRT","Revision":"HPG4","Protocol":"SATA","MediaType":"SSD","CapacityBytes":960197124096,
		"SerialNumber":"S4NBTEST000001","FailurePredicted":false,"PredictedMediaLifeLeftPercent":93.0,"Status":{"State":"Enabled","Health":"OK"},
		"PhysicalLocation":{"PartLocation":{"LocationType":"Bay","ServiceLabel":"Slot=0:Port=1I:Box=1:Bay=3"}}}`,
	"/redfish/v1/Systems/1/Storage/DE07B000/Drives/9/": `{"Name":"Drive","Model":null,"SerialNumber":null,"Status":{"State":"Enabled","Health":"Critical","Conditions":[{"MessageId":"StorageDevice.1.1.DriveFailure"}]},
		"PhysicalLocation":{"PartLocation":{"ServiceLabel":"Slot=0:Port=2I:Box=1:Bay=5"}}}`,
	"/redfish/v1/Chassis":                            `{"Members":[{"@odata.id":"/redfish/v1/Chassis/1/"}]}`,
	"/redfish/v1/Chassis/1/Devices":                  `{"Members":[{"@odata.id":"/redfish/v1/Chassis/1/Devices/1/"},{"@odata.id":"/redfish/v1/Chassis/1/Devices/3/"},{"@odata.id":"/redfish/v1/Chassis/1/Devices/4/"},{"@odata.id":"/redfish/v1/Chassis/1/Devices/5/"}]}`,
	"/redfish/v1/Chassis/1/Devices/1/":               `{"Id":"1","DeviceType":"Unknown","FirmwareVersion":{"Current":{"VersionString":"N/A"}},"Location":"PCI-E Slot 1","Name":"Empty slot 1","PartNumber":"","SerialNumber":"","Status":{"State":"Absent"}}`,
	"/redfish/v1/Chassis/1/Devices/3/":               `{"Id":"3","DeviceType":"LOM/NIC","FirmwareVersion":{"Current":{"VersionString":"20.33.41"}},"Location":"Embedded LOM","Name":"HPE Ethernet 1Gb 4-port 331i Adapter - NIC","PartNumber":"","SerialNumber":"N/A","Status":{"State":"Enabled"}}`,
	"/redfish/v1/Chassis/1/Devices/4/":               `{"Id":"4","DeviceType":"LOM/NIC","FirmwareVersion":{"Current":{"VersionString":"11.1.7"}},"Location":"Embedded ALOM","Manufacturer":"","Name":"HPE Ethernet 10Gb 2-port 562FLR-SFP+ Adpt","PartNumber":"790317-001","ProductPartNumber":"727055-B21","SerialNumber":"MYI0TEST01","Status":{"State":"Enabled"}}`,
	"/redfish/v1/Chassis/1/Devices/5/":               `{"Id":"5","DeviceType":"Smart Storage","Location":"Embedded RAID","Name":"HPE Smart Array P408i-a SR Gen10","PartNumber":"836260-001","SerialNumber":"PEYHC0TEST0001 ","Status":{"State":"Enabled"}}`,
	"/redfish/v1/UpdateService":                      `{"FirmwareInventory":{"@odata.id":"/redfish/v1/UpdateService/FirmwareInventory/"}}`,
	"/redfish/v1/UpdateService/FirmwareInventory":    `{"Members":[{"@odata.id":"/redfish/v1/UpdateService/FirmwareInventory/1/"},{"@odata.id":"/redfish/v1/UpdateService/FirmwareInventory/2/"},{"@odata.id":"/redfish/v1/UpdateService/FirmwareInventory/9/"}]}`,
	"/redfish/v1/UpdateService/FirmwareInventory/1/": `{"Id":"1","Name":"iLO 5","Version":"2.72 Sep 04 2022","Status":{"Health":"OK","State":"Enabled"},"Oem":{"Hpe":{"DeviceContext":"System Board"}}}`,
	"/redfish/v1/UpdateService/FirmwareInventory/2/": `{"Id":"2","Name":"System ROM","Version":"U32 v2.68 (07/14/2022)","Status":{"Health":"OK","State":"Enabled"},"Oem":{"Hpe":{"DeviceContext":"System Board"}}}`,
	"/redfish/v1/UpdateService/FirmwareInventory/9/": `{"Id":"9","Name":"HPE Smart Array P408i-a SR Gen10","Version":"3.53","Status":{"Health":"OK","State":"Enabled"},"Oem":{"Hpe":{"DeviceContext":"Slot 0"}}}`,
}

func TestHPEHardware(t *testing.T) {
	hw, errs := ReadHardware(context.Background(), iloHW)
	if len(errs) > 0 {
		t.Fatalf("errors %v", errs)
	}
	// one controller: the HPE and the standard view joined on the serial
	if len(hw.Controllers) != 1 {
		t.Fatalf("controllers %+v", hw.Controllers)
	}
	c := hw.Controllers[0]
	if c.Model != "HPE Smart Array P408i-a SR Gen10" || c.Serial != "PEYHC0TEST0001" || c.Firmware != "3.53" || c.Location != "Slot 0" ||
		c.Tool != "redfish" || c.Status != model.StatusWarn || c.Battery == nil || c.Battery.Status != model.StatusOK {
		t.Errorf("controller %+v", c)
	}
	if len(c.VDs) != 1 || c.VDs[0].ID != "1" || c.VDs[0].Level != "RAID 1" || c.VDs[0].Status != model.StatusWarn || c.VDs[0].SizeText != "960 GB" {
		t.Errorf("logical drives %+v", c.VDs)
	}
	// 1I:1:3 (both views), 1I:1:4 (worn), 2I:1:5 (failed, no serial, both views);
	// 2I:1:6 has no serial and is healthy: discovery not finished, left out
	if len(c.PDs) != 3 {
		t.Fatalf("drives %+v", c.PDs)
	}
	d := c.PDs[0]
	if d.Slot != "1I:1:3" || d.Serial != "S4NBTEST000001" || d.Firmware != "HPG4" || d.SizeBytes != 915715<<20 || d.SizeText != "960 GB" ||
		d.LifeLeftPercent == nil || *d.LifeLeftPercent != 93 || d.PowerOnHours == nil || *d.PowerOnHours != 20468 ||
		d.TempCelsius == nil || *d.TempCelsius != 30 || len(d.Reasons) != 0 || d.Status != model.StatusOK {
		t.Errorf("drive 1I:1:3 %+v", d)
	}
	if w := c.PDs[1]; w.Slot != "1I:1:4" || w.Status != model.StatusWarn || *w.LifeLeftPercent != 4 {
		t.Errorf("worn drive %+v", w)
	}
	f := c.PDs[2]
	if f.Slot != "2I:1:5" || f.Serial != "" || f.Status != model.StatusCrit || len(f.Reasons) != 2 ||
		f.Reasons[0] != "DriveFailure" || f.Reasons[1] != "StorageDevice.1.1.DriveFailure" {
		t.Errorf("failed drive %+v", f)
	}

	// the empty slot is left out, N/A is no value, serials are trimmed
	if len(hw.Devices) != 3 {
		t.Fatalf("devices %+v", hw.Devices)
	}
	if n := hw.Devices[0]; n.Serial != "" || n.PartNumber != "" || n.Firmware != "20.33.41" || n.Location != "Embedded LOM" {
		t.Errorf("sparse device %+v", n)
	}
	if n := hw.Devices[1]; n.PartNumber != "790317-001" || n.ProductPart != "727055-B21" || n.Serial != "MYI0TEST01" || n.Firmware != "11.1.7" || n.Type != "LOM/NIC" {
		t.Errorf("full device %+v", n)
	}
	if n := hw.Devices[2]; n.Serial != "PEYHC0TEST0001" {
		t.Errorf("serial not trimmed %+v", n)
	}

	if len(hw.Firmware) != 3 || hw.Firmware[0].Name != "HPE Smart Array P408i-a SR Gen10" || hw.Firmware[1].Name != "System ROM" ||
		hw.Firmware[2].Version != "2.72 Sep 04 2022" || hw.Firmware[2].Location != "System Board" {
		t.Errorf("firmware %+v", hw.Firmware)
	}
}

// A BMC without any of these resources (the Supermicro recording): no
// controllers, devices or firmware, and no errors either.
func TestHardwareAbsent(t *testing.T) {
	hw, errs := ReadHardware(context.Background(), ilo)
	if len(errs) > 0 || len(hw.Controllers)+len(hw.Devices)+len(hw.Firmware) != 0 {
		t.Errorf("hw %+v errs %v", hw, errs)
	}
}

// Dell lists previous and available firmware next to the installed one.
func TestFirmwareInstalledOnly(t *testing.T) {
	g := mapGetter{
		"/redfish/v1/UpdateService/FirmwareInventory": `{"Members":[{"@odata.id":"/fw/Installed-1"},{"@odata.id":"/fw/Previous-1"},{"@odata.id":"/fw/Available-1"}]}`,
		"/fw/Installed-1": `{"Id":"Installed-25227-6.10.30.20__RAID.Integrated.1-1","Name":"PERC H755 Front","Version":"52.16.1-4405"}`,
		"/fw/Previous-1":  `{"Id":"Previous-25227-6.10.00.00__RAID.Integrated.1-1","Name":"PERC H755 Front","Version":"52.14.0-3901"}`,
		"/fw/Available-1": `{"Id":"Available-25227-6.10.40.00","Name":"PERC H755 Front","Version":"52.26.0-5179"}`,
	}
	fw, err := readFirmware(context.Background(), g)
	if err != nil || len(fw) != 1 || fw[0].Version != "52.16.1-4405" || fw[0].Status != model.StatusOK {
		t.Errorf("fw %+v err %v", fw, err)
	}
}

func TestDriveBay(t *testing.T) {
	for in, want := range map[string]string{"Slot=0:Port=1I:Box=1:Bay=3": "1I:1:3", "Disk.Bay.4:Enclosure.Internal.0-1": "Disk.Bay.4:Enclosure.Internal.0-1", "": ""} {
		if got := driveBay(in); got != want {
			t.Errorf("driveBay(%q) = %q, want %q", in, got, want)
		}
	}
}
