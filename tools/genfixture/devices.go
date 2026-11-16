package main

import (
	"fmt"
	"strings"
)

// --- scenario knobs for storage, network and GPUs ---------------------------

type faults struct {
	// storage
	MDFailed     string // md1 member that failed (e.g. "sdd1")
	Realloc      map[string]int
	Pending      map[string]int
	NVMeWear     map[string]int
	NVMeMedia    map[string]int
	DataFullPct  int
	DataReadOnly bool
	IOErrCnt     map[string]int
	ISCSI        bool // an iSCSI LUN (sdf) whose session failed
	MegaRAID     bool
	MegaDegraded bool
	// network
	BondMemberDown string
	CRCErrors      map[string]int
	CarrierChanges map[string]int
	RxPowerDBm     map[string]float64
	MgmtSpeed      int
	MgmtHalf       bool
	// gpu
	GPUWidth8    bool
	GPUUE        int
	GPURemapPend bool
	GPUTemp      int
}

var pciIDs = `#	pci.ids trimmed for Tyr fixtures (from hwdata)
1000  Broadcom / LSI
	005d  MegaRAID SAS-3 3108 [Invader]
10de  NVIDIA Corporation
	20b5  GA100 [A100 PCIe 80GB]
144d  Samsung Electronics Co Ltd
	a80a  NVMe SSD Controller PM9A1/PM9A3/980PRO
1a03  ASPEED Technology, Inc.
	2000  ASPEED Graphics Family
8086  Intel Corporation
	1521  I350 Gigabit Network Connection
	159b  Ethernet Controller E810-XXV for SFP
	a182  C620 Series Chipset Family SATA Controller [AHCI mode]
`

// pciDev creates a PCI function with its attributes and the bus link.
func (g *gen) pciDev(addr, class, vendor, device, driver string, numa int, link, width, maxLink, maxWidth string) string {
	dir := "/sys/devices/pci0000:00/" + addr
	g.write(dir+"/class", class+"\n")
	g.write(dir+"/vendor", vendor+"\n")
	g.write(dir+"/device", device+"\n")
	g.write(dir+"/subsystem_vendor", vendor+"\n")
	g.write(dir+"/subsystem_device", "0x0000\n")
	g.write(dir+"/numa_node", fmt.Sprintf("%d\n", numa))
	g.write(dir+"/power_state", "D0\n")
	g.write(dir+"/power/runtime_status", "active\n")
	if link != "" {
		g.write(dir+"/current_link_speed", link+"\n")
		g.write(dir+"/current_link_width", width+"\n")
		g.write(dir+"/max_link_speed", maxLink+"\n")
		g.write(dir+"/max_link_width", maxWidth+"\n")
	}
	if driver != "" {
		g.symlink(dir+"/driver", "../../../bus/pci/drivers/"+driver)
	}
	g.symlink("/sys/bus/pci/devices/"+addr, "../../../devices/pci0000:00/"+addr)
	return dir
}

func (g *gen) devices() {
	f := g.s.Faults
	g.write("/usr/share/hwdata/pci.ids", pciIDs)
	g.storage(f)
	g.network(f)
	g.gpus(f)
}

// --- storage ---------------------------------------------------------------------

type disk struct {
	name, model, serial, fw string
	sizeGB                  uint64
	rpm, temp               int
}

func (g *gen) storage(f faults) {
	nvmes := []disk{
		{"nvme0n1", "SAMSUNG MZQL21T9HCJR-00A07", "S64GNE0R100001", "GDC5602Q", 1920, 0, 38},
		{"nvme1n1", "SAMSUNG MZQL21T9HCJR-00A07", "S64GNE0R100002", "GDC5602Q", 1920, 0, 41},
	}
	hdds := []disk{
		{"sda", "ST4000NM000A-2HZ100", "WJG0A001", "SN04", 4000, 7200, 33},
		{"sdb", "ST4000NM000A-2HZ100", "WJG0A002", "SN04", 4000, 7200, 34},
		{"sdc", "ST4000NM000A-2HZ100", "WJG0A003", "SN04", 4000, 7200, 35},
		{"sdd", "ST4000NM000A-2HZ100", "WJG0A004", "SN04", 4000, 7200, 36},
	}
	for i, d := range nvmes {
		addr := fmt.Sprintf("0000:c%d:00.0", i+1)
		dev := g.pciDev(addr, "0x010802", "0x144d", "0xa80a", "nvme", 1, "16.0 GT/s PCIe", "4", "16.0 GT/s PCIe", "4")
		ctrl := fmt.Sprintf("%s/nvme/nvme%d", dev, i)
		g.write(ctrl+"/model", d.model+"\n")
		g.write(ctrl+"/serial", d.serial+"\n")
		g.write(ctrl+"/firmware_rev", d.fw+"\n")
		g.write(ctrl+"/transport", "pcie\n")
		g.write(ctrl+"/state", "live\n")
		g.symlink(ctrl+"/device", "../../../"+addr)
		hw := fmt.Sprintf("%s/hwmon%d", ctrl, 10+i)
		g.write(hw+"/name", "nvme\n")
		g.write(hw+"/temp1_label", "Composite\n")
		g.write(hw+"/temp1_input", fmt.Sprintf("%d850\n", d.temp))
		g.write(hw+"/temp1_max", "79850\n")
		g.write(hw+"/temp1_crit", "84850\n")
		g.symlink(hw+"/device", "../../nvme"+fmt.Sprint(i))
		g.symlink(fmt.Sprintf("/sys/class/hwmon/hwmon%d", 10+i), "../../devices/pci0000:00/"+addr+fmt.Sprintf("/nvme/nvme%d/hwmon%d", i, 10+i))
		blk := ctrl + "/" + d.name
		g.write(blk+"/size", fmt.Sprintf("%d\n", d.sizeGB*1000*1000*1000/512))
		g.write(blk+"/ro", "0\n")
		g.write(blk+"/removable", "0\n")
		g.write(blk+"/queue/rotational", "0\n")
		g.write(blk+"/wwid", fmt.Sprintf("eui.36344730520%05d0025384500000001\n", i+1))
		g.symlink(blk+"/device", "../../nvme"+fmt.Sprint(i))
		for p := 1; p <= 2; p++ {
			g.write(fmt.Sprintf("%s/%sp%d/partition", blk, d.name, p), fmt.Sprintf("%d\n", p))
		}
		g.symlink(fmt.Sprintf("%s/%sp2/holders/md0", blk, d.name), "../../../../../../../../virtual/block/md0")
		g.symlink("/sys/block/"+d.name, "../devices/pci0000:00/"+addr+fmt.Sprintf("/nvme/nvme%d/%s", i, d.name))
		g.symlink("/dev/disk/by-id/nvme-"+strings.ReplaceAll(d.model, " ", "_")+"_"+d.serial, "../../"+d.name)
		media, wear := f.NVMeMedia[d.name], f.NVMeWear[d.name]
		if wear == 0 {
			wear = 3
		}
		g.cmd([]string{"smartctl", "-j", "-x", "/dev/" + d.name}, smartNVMe(d, wear, media), 0)
	}
	g.pciDev("0000:00:17.0", "0x010601", "0x8086", "0xa182", "ahci", 0, "", "", "", "")
	for i, d := range hdds {
		scsi := fmt.Sprintf("/sys/devices/pci0000:00/0000:00:17.0/ata%d/host%d/target%d:0:0/%d:0:0:0", i+1, i, i, i)
		g.write(scsi+"/model", d.model+"\n")
		g.write(scsi+"/vendor", "ATA     \n")
		g.write(scsi+"/rev", d.fw+"\n")
		g.write(scsi+"/ioerr_cnt", fmt.Sprintf("0x%x\n", f.IOErrCnt[d.name]))
		g.write(scsi+"/wwid", fmt.Sprintf("naa.5000c500e4a1%04x\n", i+1))
		blk := scsi + "/block/" + d.name
		g.write(blk+"/size", fmt.Sprintf("%d\n", d.sizeGB*1000*1000*1000/512))
		g.write(blk+"/ro", "0\n")
		g.write(blk+"/removable", "0\n")
		g.write(blk+"/queue/rotational", "1\n")
		g.symlink(blk+"/device", "../../../"+fmt.Sprintf("%d:0:0:0", i))
		g.write(blk+"/"+d.name+"1/partition", "1\n")
		g.symlink(blk+"/"+d.name+"1/holders/md1", "../../../../../../../../../../virtual/block/md1")
		g.symlink("/sys/block/"+d.name, "../devices/pci0000:00/0000:00:17.0/"+fmt.Sprintf("ata%d/host%d/target%d:0:0/%d:0:0:0/block/%s", i+1, i, i, i, d.name))
		g.symlink("/dev/disk/by-id/ata-"+d.model+"_"+d.serial, "../../"+d.name)
		g.cmd([]string{"smartctl", "-j", "-x", "/dev/" + d.name}, smartATA(d, f.Realloc[d.name], f.Pending[d.name]), 0)
	}
	if f.ISCSI {
		g.iscsiLUN()
	}
	// virtual md block devices (skipped as disks, used for holders)
	g.write("/sys/devices/virtual/block/md0/size", "3749707776\n")
	g.write("/sys/devices/virtual/block/md1/size", "23441313792\n")
	g.symlink("/sys/block/md0", "../devices/virtual/block/md0")
	g.symlink("/sys/block/md1", "../devices/virtual/block/md1")
	g.symlink("/sys/block/loop0", "../devices/virtual/block/loop0")
	g.write("/sys/devices/virtual/block/loop0/size", "0\n")

	md1 := "md1 : active raid5 sdd1[3] sdc1[2] sdb1[1] sda1[0]\n      11720656896 blocks super 1.2 level 5, 512k chunk, algorithm 2 [4/4] [UUUU]\n      bitmap: 0/30 pages [0KB], 65536KB chunk\n"
	if f.MDFailed != "" {
		md1 = "md1 : active raid5 sdd1[3](F) sdc1[2] sdb1[1] sda1[0]\n      11720656896 blocks super 1.2 level 5, 512k chunk, algorithm 2 [4/3] [UUU_]\n      bitmap: 12/30 pages [48KB], 65536KB chunk\n"
	}
	g.write("/proc/mdstat", "Personalities : [raid1] [raid6] [raid5] [raid4] [linear] [multipath] [raid0] [raid10]\n"+
		"md0 : active raid1 nvme1n1p2[1] nvme0n1p2[0]\n      1874853888 blocks super 1.2 [2/2] [UU]\n      bitmap: 2/14 pages [8KB], 65536KB chunk\n\n"+
		md1+"\nunused devices: <none>\n")

	// mounts + df
	dataOpts := "rw,noatime"
	if f.DataReadOnly {
		dataOpts = "ro,noatime"
	}
	mi := []string{
		"22 1 9:0 / / rw,relatime shared:1 - ext4 /dev/md0 rw,errors=remount-ro",
		"23 22 259:1 / /boot/efi rw,relatime shared:2 - vfat /dev/nvme0n1p1 rw,fmask=0077,dmask=0077",
		"24 22 9:1 / /data " + dataOpts + " shared:3 - xfs /dev/md1 " + strings.Split(dataOpts, ",")[0] + ",attr2,inode64",
		"25 22 0:5 / /dev rw,nosuid shared:4 - devtmpfs udev rw,size=263852012k",
		"26 22 0:22 / /sys rw,nosuid,nodev,noexec,relatime shared:5 - sysfs sysfs rw",
		"27 22 0:23 / /proc rw,nosuid,nodev,noexec,relatime shared:6 - proc proc rw",
		"28 22 0:24 / /run rw,nosuid,nodev,noexec,relatime shared:7 - tmpfs tmpfs rw,size=52770404k,mode=755",
		"29 22 7:0 / /snap/core22/1663 ro,nodev,relatime shared:8 - squashfs /dev/loop0 ro",
		"30 22 9:0 /var/lib/docker /var/lib/docker rw,relatime shared:1 - ext4 /dev/md0 rw",
	}
	if f.MegaRAID {
		mi = append(mi, "31 22 8:64 / /archive rw,relatime shared:9 - ext4 /dev/sde rw")
	}
	g.write("/proc/self/mountinfo", strings.Join(mi, "\n")+"\n")
	dataUsed := uint64(41)
	if f.DataFullPct > 0 {
		dataUsed = uint64(f.DataFullPct)
	}
	dataSize := uint64(12_000_000_000_000)
	df := "Mounted on 1B-blocks Used Avail Inodes IUsed\n" +
		fmt.Sprintf("/ 1886720000000 %d %d 117178368 2203411\n", 1886720000000*22/100, 1886720000000*78/100) +
		"/boot/efi 1124999168 6684672 1118314496 0 0\n" +
		fmt.Sprintf("/data %d %d %d 1171872000 12345678\n", dataSize, dataSize*dataUsed/100, dataSize*(100-dataUsed)/100) +
		"/run 54036893696 4775936 54032117760 65970000 2507\n"
	if f.MegaRAID {
		df += "/archive 5999999565824 1199999913164 4799999652659 366210048 120000\n"
	}
	g.cmd([]string{"df", "-B1", "--output=target,size,used,avail,itotal,iused"}, df, 0)

	if f.MegaRAID {
		dev := g.pciDev("0000:5e:00.0", "0x010400", "0x1000", "0x005d", "megaraid_sas", 0, "8.0 GT/s PCIe", "8", "8.0 GT/s PCIe", "8")
		scsi := dev + "/host6/target6:2:0/6:2:0:0"
		g.write(scsi+"/model", "MR9361-8i       \n")
		g.write(scsi+"/vendor", "AVAGO   \n")
		g.write(scsi+"/rev", "4.68\n")
		g.write(scsi+"/ioerr_cnt", "0x0\n")
		blk := scsi + "/block/sde"
		g.write(blk+"/size", "11718749184\n")
		g.write(blk+"/ro", "0\n")
		g.write(blk+"/removable", "0\n")
		g.write(blk+"/queue/rotational", "1\n")
		g.symlink(blk+"/device", "../../../6:2:0:0")
		g.symlink("/sys/block/sde", "../devices/pci0000:00/0000:5e:00.0/host6/target6:2:0/6:2:0:0/block/sde")
		g.cmd([]string{"smartctl", "-j", "-x", "/dev/sde"}, `{"json_format_version":[1,0],"smartctl":{"version":[7,4],"exit_status":2,"messages":[{"string":"Smartctl open device: /dev/sde failed: DELL or MegaRaid controller, please try adding '-d megaraid,N'","severity":"error"}]},"device":{"name":"/dev/sde","info_name":"/dev/sde","type":"scsi","protocol":"SCSI"}}`+"\n", 2)
		vdState, pd2 := "Optl", "Onln"
		if f.MegaDegraded {
			vdState, pd2 = "Dgrd", "Failed"
		}
		g.cmd([]string{"storcli64", "/call", "show", "all", "J"}, fmt.Sprintf(`{
"Controllers":[
{
	"Command Status" : {"CLI Version" : "007.2612.0000.0000 Jun 13, 2023","Operating system" : "Linux 6.8.0-49-generic","Controller" : 0,"Status" : "Success","Description" : "None"},
	"Response Data" : {
		"Basics" : {"Controller" : 0,"Model" : "AVAGO MegaRAID SAS 9361-8i","Serial Number" : "SK94012345","PCI Address" : "00:5e:00:00"},
		"Version" : {"Firmware Package Build" : "24.21.0-0151","Firmware Version" : "4.680.00-8527","Driver Name" : "megaraid_sas","Driver Version" : "07.727.03.00-rc1"},
		"Status" : {"Controller Status" : "Optimal","Memory Correctable Errors" : 0,"Memory Uncorrectable Errors" : 0,"ECC Bucket Count" : 0,"Any Offline VD Cache Preserved" : "No","BBU Status" : 0},
		"VD LIST" : [{"DG/VD" : "0/0","TYPE" : "RAID5","State" : "%s","Access" : "RW","Consist" : "Yes","Cache" : "RWBD","Cac" : "-","sCC" : "ON","Size" : "5.457 TB","Name" : "archive"}],
		"PD LIST" : [
			{"EID:Slt" : "252:0","DID" : 8,"State" : "Onln","DG" : 0,"Size" : "1.818 TB","Intf" : "SATA","Med" : "HDD","SED" : "N","PI" : "N","SeSz" : "512B","Model" : "ST2000NM0055-1V4104","Sp" : "U","Type" : "-"},
			{"EID:Slt" : "252:1","DID" : 9,"State" : "Onln","DG" : 0,"Size" : "1.818 TB","Intf" : "SATA","Med" : "HDD","SED" : "N","PI" : "N","SeSz" : "512B","Model" : "ST2000NM0055-1V4104","Sp" : "U","Type" : "-"},
			{"EID:Slt" : "252:2","DID" : 10,"State" : "%s","DG" : 0,"Size" : "1.818 TB","Intf" : "SATA","Med" : "HDD","SED" : "N","PI" : "N","SeSz" : "512B","Model" : "ST2000NM0055-1V4104","Sp" : "U","Type" : "-"},
			{"EID:Slt" : "252:3","DID" : 11,"State" : "Onln","DG" : 0,"Size" : "1.818 TB","Intf" : "SATA","Med" : "HDD","SED" : "N","PI" : "N","SeSz" : "512B","Model" : "ST2000NM0055-1V4104","Sp" : "U","Type" : "-"}
		],
		"Cachevault_Info" : [{"Model" : "CVPM02","State" : "Optimal","Temp" : "28C","Mode" : "-","MfgDate" : "2021/03/11"}]
	}
}
]
}
`, vdState, pd2), 0)
		var pd strings.Builder
		pd.WriteString(`{"Controllers":[{"Command Status":{"Controller":0,"Status":"Success","Description":"Show Drive Information Succeeded."},"Response Data":{`)
		for s := 0; s < 4; s++ {
			if s > 0 {
				pd.WriteString(",")
			}
			fmt.Fprintf(&pd, `"Drive /c0/e252/s%[1]d":[{"EID:Slt":"252:%[1]d","DID":%[2]d,"State":"Onln"}],"Drive /c0/e252/s%[1]d - Detailed Information":{"Drive /c0/e252/s%[1]d State":{"Shield Counter":0,"Media Error Count":0},"Drive /c0/e252/s%[1]d Device attributes":{"SN":"ZFL4A%03[1]d","Manufacturer Id":"ATA     ","Model Number":"ST2000NM0055-1V4104","NAND Vendor":"NA","WWN":"5000C500B2C3D%03[1]d","Firmware Revision":"SN05    ","Raw size":"1.819 TB [0xe8e088b0 Sectors]","Device Speed":"6.0Gb/s","Link Speed":"12.0Gb/s"}}`, s, 8+s)
		}
		pd.WriteString("}}]}\n")
		g.cmd([]string{"storcli64", "/call/eall/sall", "show", "all", "J"}, pd.String(), 0)
	}
}

func smartATA(d disk, realloc, pending int) string {
	passed, exit := "true", 0
	return fmt.Sprintf(`{
  "json_format_version": [1, 0],
  "smartctl": {"version": [7, 4], "argv": ["smartctl", "-j", "-x", "/dev/%[1]s"], "exit_status": %[9]d},
  "device": {"name": "/dev/%[1]s", "info_name": "/dev/%[1]s [SAT]", "type": "sat", "protocol": "ATA"},
  "model_family": "Seagate Exos 7E8",
  "model_name": "%[2]s",
  "serial_number": "%[3]s",
  "firmware_version": "%[4]s",
  "user_capacity": {"blocks": %[5]d, "bytes": %[6]d},
  "rotation_rate": %[7]d,
  "interface_speed": {"max": {"string": "6.0 Gb/s"}, "current": {"string": "6.0 Gb/s"}},
  "smart_status": {"passed": %[10]s},
  "ata_smart_attributes": {"revision": 10, "table": [
    {"id": 1, "name": "Raw_Read_Error_Rate", "value": 83, "worst": 64, "thresh": 44, "when_failed": "", "raw": {"value": 215098744, "string": "215098744"}},
    {"id": 5, "name": "Reallocated_Sector_Ct", "value": 100, "worst": 100, "thresh": 10, "when_failed": "", "raw": {"value": %[11]d, "string": "%[11]d"}},
    {"id": 9, "name": "Power_On_Hours", "value": 78, "worst": 78, "thresh": 0, "when_failed": "", "raw": {"value": 19832, "string": "19832"}},
    {"id": 187, "name": "Reported_Uncorrect", "value": 100, "worst": 100, "thresh": 0, "when_failed": "", "raw": {"value": 0, "string": "0"}},
    {"id": 194, "name": "Temperature_Celsius", "value": 64, "worst": 50, "thresh": 0, "when_failed": "", "raw": {"value": %[8]d, "string": "%[8]d (0 19 0 0 0)"}},
    {"id": 197, "name": "Current_Pending_Sector", "value": 100, "worst": 100, "thresh": 0, "when_failed": "", "raw": {"value": %[12]d, "string": "%[12]d"}},
    {"id": 198, "name": "Offline_Uncorrectable", "value": 100, "worst": 100, "thresh": 0, "when_failed": "", "raw": {"value": 0, "string": "0"}},
    {"id": 199, "name": "UDMA_CRC_Error_Count", "value": 200, "worst": 200, "thresh": 0, "when_failed": "", "raw": {"value": 0, "string": "0"}}
  ]},
  "power_on_time": {"hours": 19832},
  "power_cycle_count": 41,
  "temperature": {"current": %[8]d}
}
`, d.name, d.model, d.serial, d.fw, d.sizeGB*1000*1000*1000/512, d.sizeGB*1000*1000*1000, d.rpm, d.temp, exit, passed, realloc, pending)
}

func smartNVMe(d disk, wear, media int) string {
	return fmt.Sprintf(`{
  "json_format_version": [1, 0],
  "smartctl": {"version": [7, 4], "argv": ["smartctl", "-j", "-x", "/dev/%[1]s"], "exit_status": 0},
  "device": {"name": "/dev/%[1]s", "info_name": "/dev/%[1]s", "type": "nvme", "protocol": "NVMe"},
  "model_name": "%[2]s",
  "serial_number": "%[3]s",
  "firmware_version": "%[4]s",
  "user_capacity": {"blocks": %[5]d, "bytes": %[6]d},
  "smart_status": {"passed": true, "nvme": {"value": 0}},
  "nvme_smart_health_information_log": {
    "critical_warning": 0, "temperature": %[7]d, "available_spare": 100, "available_spare_threshold": 10,
    "percentage_used": %[8]d, "data_units_read": 412398123, "data_units_written": 801234567, "host_reads": 9123456789,
    "host_writes": 12345678901, "controller_busy_time": 4567, "power_cycles": 41, "power_on_hours": 19830,
    "unsafe_shutdowns": 7, "media_errors": %[9]d, "num_err_log_entries": %[9]d,
    "warning_temp_time": 0, "critical_comp_time": 0
  },
  "temperature": {"current": %[7]d},
  "power_cycle_count": 41,
  "power_on_time": {"hours": 19830}
}
`, d.name, d.model, d.serial, d.fw, d.sizeGB*1000*1000*1000/512, d.sizeGB*1000*1000*1000, d.temp, wear, media)
}

// --- network ---------------------------------------------------------------------

type nic struct {
	name, addr, driver, mac string
	pciDevice               string
	speed                   int
	maxSpeed                int
	port                    string
	adminUp, carrier        bool
	master                  string
	ip                      string
	numa                    int
}

func (g *gen) network(f faults) {
	mgmtSpeed, duplex := 1000, "full"
	if f.MgmtSpeed > 0 {
		mgmtSpeed = f.MgmtSpeed
	}
	if f.MgmtHalf {
		duplex = "half"
	}
	nics := []nic{
		{"eno1", "0000:03:00.0", "igb", "3c:ec:ef:10:00:01", "0x1521", mgmtSpeed, 1000, "Twisted Pair", true, true, "", "10.10.0.21/24", 0},
		{"eno2", "0000:03:00.1", "igb", "3c:ec:ef:10:00:02", "0x1521", 0, 1000, "Twisted Pair", false, false, "", "", 0},
		{"ens1f0", "0000:4b:00.0", "ice", "b4:96:91:a0:00:10", "0x159b", 25000, 25000, "FIBRE", true, true, "bond0", "", 0},
		{"ens1f1", "0000:4b:00.1", "ice", "b4:96:91:a0:00:11", "0x159b", 25000, 25000, "FIBRE", true, f.BondMemberDown != "ens1f1", "bond0", "", 0},
	}
	addrs := map[string]string{}
	for _, n := range nics {
		dev := g.pciDev(n.addr, "0x020000", "0x8086", n.pciDevice, n.driver, n.numa, "8.0 GT/s PCIe", "8", "8.0 GT/s PCIe", "8")
		if strings.HasSuffix(n.addr, ".0") {
			name, pn, sn := "Intel(R) Ethernet Network Adapter E810-XXV-2", "K91258-006", "B49691A00010"
			if n.driver == "igb" {
				name, pn, sn = "Intel(R) Ethernet Controller I350 (onboard)", "AOC-I350", "3CECEF100001"
			}
			g.write(dev+"/vpd", vpd(name, pn, sn))
		}
		dir := dev + "/net/" + n.name
		g.symlink("/sys/class/net/"+n.name, "../../devices/pci0000:00/"+n.addr+"/net/"+n.name)
		g.symlink(dir+"/device", "../../../"+n.addr)
		flags := "0x1003"
		if !n.adminUp {
			flags = "0x1002"
		}
		if n.master != "" {
			flags = "0x1803"
			g.symlink(dir+"/master", "../../../../virtual/net/"+n.master)
		}
		oper, carrier, speed, dup := "down", "0", "-1", "unknown"
		if n.carrier {
			oper, carrier, speed, dup = "up", "1", fmt.Sprint(n.speed), duplex
			if n.name != "eno1" {
				dup = "full"
			}
		}
		chg := 2 + f.CarrierChanges[n.name]
		for k, v := range map[string]string{"address": n.mac, "operstate": oper, "carrier": carrier, "speed": speed, "duplex": dup,
			"mtu": "1500", "type": "1", "flags": flags, "carrier_changes": fmt.Sprint(chg), "uevent": "INTERFACE=" + n.name + "\nIFINDEX=2\n"} {
			if !n.adminUp && (k == "carrier" || k == "speed" || k == "duplex") {
				continue // these attributes are unreadable while admin down
			}
			g.write(dir+"/"+k, v+"\n")
		}
		if n.master != "" {
			g.write(dir+"/mtu", "9000\n")
		}
		crc := f.CRCErrors[n.name]
		stats := map[string]uint64{"rx_bytes": 912345678901, "tx_bytes": 812345678901, "rx_packets": 912345678, "tx_packets": 812345678,
			"rx_errors": uint64(crc), "tx_errors": 0, "rx_dropped": 12, "tx_dropped": 0, "rx_crc_errors": uint64(crc),
			"rx_frame_errors": 0, "rx_missed_errors": 0, "collisions": 0}
		if !n.adminUp {
			for k := range stats {
				stats[k] = 0
			}
		}
		for k, v := range stats {
			g.write(dir+"/statistics/"+k, fmt.Sprintf("%d\n", v))
		}
		if n.ip != "" {
			addrs[n.name] = n.ip
		}
		// ethtool outputs
		g.cmd([]string{"ethtool", "-i", n.name}, fmt.Sprintf("driver: %s\nversion: %s\nfirmware-version: %s\nexpansion-rom-version: \nbus-info: %s\nsupports-statistics: yes\nsupports-test: yes\nsupports-eeprom-access: yes\nsupports-register-dump: yes\nsupports-priv-flags: yes\n",
			n.driver, map[string]string{"igb": "6.8.0-49-generic", "ice": "6.8.0-49-generic"}[n.driver],
			map[string]string{"igb": "1.63, 0x80000f73, 1.3534.0", "ice": "4.40 0x8001c967 1.3534.0"}[n.driver], n.addr), 0)
		modes := "1000baseT/Full"
		if n.driver == "igb" {
			modes = "10baseT/Half 10baseT/Full\n\t                        100baseT/Half 100baseT/Full\n\t                        1000baseT/Full"
		} else {
			modes = "1000baseX/Full\n\t                        10000baseSR/Full\n\t                        25000baseSR/Full"
		}
		sp, dp := fmt.Sprintf("%dMb/s", n.speed), "Full"
		if !n.carrier {
			sp, dp = "Unknown!", "Unknown! (255)"
		} else if n.name == "eno1" && f.MgmtHalf {
			dp = "Half"
		}
		an := "on"
		if n.driver == "ice" {
			an = "off"
		}
		link := "yes"
		if !n.carrier {
			link = "no"
		}
		g.cmd([]string{"ethtool", n.name}, fmt.Sprintf("Settings for %s:\n\tSupported ports: [ %s ]\n\tSupported link modes:   %s\n\tSupported pause frame use: Symmetric\n\tSupports auto-negotiation: Yes\n\tAdvertised link modes:  %s\n\tAdvertised pause frame use: Symmetric\n\tAdvertised auto-negotiation: Yes\n\tSpeed: %s\n\tDuplex: %s\n\tAuto-negotiation: %s\n\tPort: %s\n\tPHYAD: 0\n\tTransceiver: internal\n\tLink detected: %s\n",
			n.name, map[string]string{"Twisted Pair": "TP", "FIBRE": "FIBRE"}[n.port], modes, modes, sp, dp, an, n.port, link), 0)
		g.cmd([]string{"ethtool", "-S", n.name}, fmt.Sprintf("NIC statistics:\n     rx_packets: 912345678\n     tx_packets: 812345678\n     rx_bytes: 912345678901\n     tx_bytes: 812345678901\n     rx_crc_errors: %d\n     rx_dropped: 12\n     tx_errors: 0\n     rx_length_errors: 0\n     tx_timeout_count: 0\n", crc), 0)
		if n.port == "FIBRE" {
			rx := -2.41
			if v, ok := f.RxPowerDBm[n.name]; ok {
				rx = v
			}
			if !n.carrier {
				rx = -40
			}
			g.cmd([]string{"ethtool", "-m", n.name}, sfp28(rx, "X7A0B1"+n.addr[len(n.addr)-1:]), 0)
		}
		if n.driver == "ice" && n.addr == "0000:4b:00.0" {
			hw := dev + "/hwmon/hwmon20"
			g.write(hw+"/name", "ice\n")
			g.write(hw+"/temp1_input", "52000\n")
			g.write(hw+"/temp1_max", "96000\n")
			g.write(hw+"/temp1_crit", "105000\n")
			g.symlink(hw+"/device", "../../../"+n.addr)
			g.symlink("/sys/class/hwmon/hwmon20", "../../devices/pci0000:00/"+n.addr+"/hwmon/hwmon20")
		}
	}
	// bond0 + vlan + docker bridge (virtual)
	bond := "/sys/devices/virtual/net/bond0"
	g.symlink("/sys/class/net/bond0", "../../devices/virtual/net/bond0")
	for k, v := range map[string]string{"address": "b4:96:91:a0:00:10", "operstate": "up", "carrier": "1", "speed": "50000", "duplex": "full",
		"mtu": "9000", "type": "1", "flags": "0x1403", "carrier_changes": "2", "uevent": "DEVTYPE=bond\nINTERFACE=bond0\n", "bonding/mode": "802.3ad 4"} {
		g.write(bond+"/"+k, v+"\n")
	}
	if f.BondMemberDown != "" {
		g.write(bond+"/speed", "25000\n")
	}
	g.symlink(bond+"/lower_ens1f0", "../../../pci0000:00/0000:4b:00.0/net/ens1f0")
	g.symlink(bond+"/lower_ens1f1", "../../../pci0000:00/0000:4b:00.1/net/ens1f1")
	for _, k := range []string{"rx_bytes", "tx_bytes", "rx_packets", "tx_packets", "rx_errors", "tx_errors", "rx_dropped", "tx_dropped", "rx_crc_errors", "rx_frame_errors", "rx_missed_errors", "collisions"} {
		g.write(bond+"/statistics/"+k, "0\n")
	}
	slave := func(name, status string, fails int) string {
		return fmt.Sprintf("\nSlave Interface: %s\nMII Status: %s\nSpeed: %s\nDuplex: full\nLink Failure Count: %d\nPermanent HW addr: b4:96:91:a0:00:1%s\nSlave queue ID: 0\nAggregator ID: 1\nActor Churn State: none\nPartner Churn State: none\n",
			name, status, map[string]string{"up": "25000 Mbps", "down": "Unknown"}[status], fails, name[len(name)-1:])
	}
	s1 := "up"
	if f.BondMemberDown == "ens1f1" {
		s1 = "down"
	}
	g.write("/proc/net/bonding/bond0", "Ethernet Channel Bonding Driver: v6.8.0-49-generic\n\nBonding Mode: IEEE 802.3ad Dynamic link aggregation\nTransmit Hash Policy: layer3+4 (1)\nMII Status: up\nMII Polling Interval (ms): 100\nUp Delay (ms): 0\nDown Delay (ms): 0\nPeer Notification Delay (ms): 0\n\n802.3ad info\nLACP active: on\nLACP rate: fast\nMin links: 0\nAggregator selection policy (ad_select): stable\n"+
		slave("ens1f0", "up", 0)+slave("ens1f1", s1, map[bool]int{true: 3, false: 0}[s1 == "down"]))
	vlan := "/sys/devices/virtual/net/bond0.100"
	g.symlink("/sys/class/net/bond0.100", "../../devices/virtual/net/bond0.100")
	for k, v := range map[string]string{"address": "b4:96:91:a0:00:10", "operstate": "up", "carrier": "1", "mtu": "9000", "type": "1",
		"flags": "0x1003", "carrier_changes": "1", "uevent": "DEVTYPE=vlan\nINTERFACE=bond0.100\n"} {
		g.write(vlan+"/"+k, v+"\n")
	}
	g.symlink(vlan+"/lower_bond0", "../bond0")
	g.write("/proc/net/vlan/config", "VLAN Dev name    | VLAN ID\nName-Type: VLAN_NAME_TYPE_RAW_PLUS_VID_NO_PAD\nbond0.100      | 100  | bond0\n")
	g.write("/proc/net/vlan/bond0.100", "bond0.100  VID: 100\t REORDER_HDR: 1  dev->priv_flags: 1021\n")
	addrs["bond0.100"] = "10.20.0.21/24"
	dock := "/sys/devices/virtual/net/docker0"
	g.symlink("/sys/class/net/docker0", "../../devices/virtual/net/docker0")
	for k, v := range map[string]string{"address": "02:42:ac:11:00:01", "operstate": "down", "carrier": "0", "mtu": "1500", "flags": "0x1003",
		"carrier_changes": "1", "uevent": "DEVTYPE=bridge\nINTERFACE=docker0\n", "bridge/bridge_id": "8000.0242ac110001"} {
		g.write(dock+"/"+k, v+"\n")
	}
	addrs["docker0"] = "172.17.0.1/16"
	g.symlink("/sys/class/net/lo", "../../devices/virtual/net/lo")
	g.write("/sys/devices/virtual/net/lo/address", "00:00:00:00:00:00\n")

	var ipj []string
	for _, n := range []string{"eno1", "eno2", "ens1f0", "ens1f1", "bond0", "bond0.100", "docker0"} {
		info := ""
		if a := addrs[n]; a != "" {
			p := strings.Split(a, "/")
			info = fmt.Sprintf(`{"family":"inet","local":"%s","prefixlen":%s,"scope":"global"}`, p[0], p[1])
		}
		ipj = append(ipj, fmt.Sprintf(`{"ifname":"%s","addr_info":[%s]}`, n, info))
	}
	g.cmd([]string{"ip", "-j", "addr", "show"}, "["+strings.Join(ipj, ",")+"]\n", 0)
	g.cmd([]string{"lldpctl", "-f", "json"}, `{"lldp": {"interface": [
  {"ens1f0": {"via": "LLDP", "rid": "1", "age": "8 days, 23:59:01", "chassis": {"leaf-a01.dc1": {"id": {"type": "mac", "value": "0c:29:ef:00:00:01"}, "descr": "Arista Networks EOS version 4.31.2F", "mgmt-ip": "10.0.0.11"}}, "port": {"id": {"type": "ifname", "value": "Ethernet12/1"}, "descr": "srv-01 ens1f0", "ttl": "120"}, "vlan": {"vlan-id": "100", "pvid": false}}},
  {"ens1f1": {"via": "LLDP", "rid": "2", "age": "8 days, 23:59:01", "chassis": {"leaf-a02.dc1": {"id": {"type": "mac", "value": "0c:29:ef:00:00:02"}, "descr": "Arista Networks EOS version 4.31.2F", "mgmt-ip": "10.0.0.12"}}, "port": {"id": {"type": "ifname", "value": "Ethernet12/1"}, "descr": "srv-01 ens1f1", "ttl": "120"}}},
  {"eno1": {"via": "LLDP", "rid": "3", "age": "8 days, 23:59:01", "chassis": {"mgmt-sw01": {"id": {"type": "mac", "value": "00:1b:21:00:00:03"}, "descr": "Cisco IOS Software, C2960X"}}, "port": {"id": {"type": "ifname", "value": "Gi1/0/21"}, "descr": "GigabitEthernet1/0/21", "ttl": "120"}}}
]}}
`, 0)
	if g.s.IPMITool == "ipmitool" {
		g.cmd([]string{"ipmitool", "mc", "info"}, "Device ID                 : 32\nDevice Revision           : 1\nFirmware Revision         : 1.40\nIPMI Version              : 2.0\nManufacturer ID           : 10876\nManufacturer Name         : Supermicro\nProduct ID                : 6929 (0x1b11)\nProduct Name              : Unknown (0x1B11)\nDevice Available          : yes\n", 0)
		g.cmd([]string{"ipmitool", "fru", "print"}, "FRU Device Description : Builtin FRU Device (ID 0)\n Chassis Type          : Rack Mount Chassis\n Chassis Part Number   : CSE-119UH4TS-R1K22P-T\n Chassis Serial        : C1160LL12AB0123\n Board Mfg Date        : Mon Mar  1 00:00:00 2021 UTC\n Board Mfg             : Supermicro\n Board Product         : X12DPU-6\n Board Serial          : OM21AS012345\n Board Part Number     : X12DPU-6\n Product Manufacturer  : Supermicro\n Product Name          : SYS-120U-TNR\n Product Part Number   : SYS-120U-TNR\n Product Serial        : S412345X3A01234\n\nFRU Device Description : PS1 (ID 1)\n Board Mfg             : SUPERMICRO\n Board Product         : PWS-1K22A-1R\n Board Serial          : P1K22CL21123451\n Board Part Number     : PWS-1K22A-1R\n\nFRU Device Description : PS2 (ID 2)\n Board Mfg             : SUPERMICRO\n Board Product         : PWS-1K22A-1R\n Board Serial          : P1K22CL21123452\n Board Part Number     : PWS-1K22A-1R\n", 0)
		g.cmd([]string{"ipmitool", "lan", "print"}, "Set in Progress         : Set Complete\nAuth Type Support       : NONE MD2 MD5 PASSWORD \nIP Address Source       : Static Address\nIP Address              : 10.10.0.121\nSubnet Mask             : 255.255.255.0\nMAC Address             : 3c:ec:ef:10:00:99\nSNMP Community String   : public\nDefault Gateway IP      : 10.10.0.1\nDefault Gateway MAC     : 00:00:00:00:00:00\n802.1q VLAN ID          : Disabled\nRMCP+ Cipher Suites     : 1,2,3,6,7,8,11,12,15,16,17\n", 0)
	}
}

func sfp28(rx float64, sn string) string {
	mw := 0.0
	if rx > -40 {
		mw = pow10(rx / 10)
	}
	return fmt.Sprintf(`	Identifier                                : 0x03 (SFP)
	Extended identifier                       : 0x04 (GBIC/SFP defined by 2-wire interface ID)
	Connector                                 : 0x07 (LC)
	Transceiver codes                         : 0x10 0x00 0x00 0x00 0x00 0x00 0x00 0x00 0x00
	Transceiver type                          : 25G Ethernet: 25G Base-SR
	Encoding                                  : 0x06 (64B/66B)
	BR, Nominal                               : 25500MBd
	Length (OM3 50um)                         : 70m
	Laser wavelength                          : 850nm
	Vendor name                               : FINISAR CORP.
	Vendor OUI                                : 00:90:65
	Vendor PN                                 : FTLF8536P4BCL
	Vendor rev                                : A
	Vendor SN                                 : %s
	Date code                                 : 210314
	Optical diagnostics support               : Yes
	Laser bias current                        : 7.510 mA
	Laser output power                        : 0.6810 mW / -1.67 dBm
	Receiver signal average optical power     : %.4f mW / %.2f dBm
	Module temperature                        : 36.12 degrees C / 97.02 degrees F
	Module voltage                            : 3.3053 V
	Alarm/warning flags implemented           : Yes
	Laser bias current high alarm             : Off
	Laser output power high alarm             : Off
	Laser output power low alarm              : Off
	Module temperature high alarm             : Off
	Laser rx power low alarm                  : Off
	Laser bias current high alarm threshold   : 13.000 mA
	Laser bias current low alarm threshold    : 2.000 mA
	Laser output power high alarm threshold   : 1.7783 mW / 2.50 dBm
	Laser output power low alarm threshold    : 0.0891 mW / -10.50 dBm
	Laser output power high warning threshold : 1.4125 mW / 1.50 dBm
	Laser output power low warning threshold  : 0.1122 mW / -9.50 dBm
	Module temperature high alarm threshold   : 85.00 degrees C / 185.00 degrees F
	Module temperature low alarm threshold    : -10.00 degrees C / 14.00 degrees F
	Module temperature high warning threshold : 75.00 degrees C / 167.00 degrees F
	Module temperature low warning threshold  : -5.00 degrees C / 23.00 degrees F
	Module voltage high alarm threshold       : 3.6300 V
	Module voltage low alarm threshold        : 2.9700 V
	Module voltage high warning threshold     : 3.4650 V
	Module voltage low warning threshold      : 3.1350 V
	Laser rx power high alarm threshold       : 1.7783 mW / 2.50 dBm
	Laser rx power low alarm threshold        : 0.0355 mW / -14.50 dBm
	Laser rx power high warning threshold     : 1.4125 mW / 1.50 dBm
	Laser rx power low warning threshold      : 0.0447 mW / -13.50 dBm
`, sn, mw, rx)
}

func pow10(x float64) float64 {
	r := 1.0
	for x >= 1 {
		r *= 10
		x--
	}
	for x < 0 {
		r /= 10
		x++
	}
	// 10^x for x in [0,1): series via exp(x*ln10)
	y, term := 1.0, 1.0
	for i := 1; i < 20; i++ {
		term *= x * 2.302585092994046 / float64(i)
		y += term
	}
	return r * y
}

// --- GPUs ---------------------------------------------------------------------------

func (g *gen) gpus(f faults) {
	// BMC VGA (ast)
	ast := g.pciDev("0000:02:00.0", "0x030000", "0x1a03", "0x2000", "ast", 0, "2.5 GT/s PCIe", "1", "2.5 GT/s PCIe", "1")
	g.symlink("/sys/class/drm/card0", "../../devices/pci0000:00/0000:02:00.0/drm/card0")
	g.symlink(ast+"/drm/card0/device", "../../../0000:02:00.0")
	type gp struct {
		addr, uuid string
		card       int
		numa       int
	}
	gs := []gp{{"0000:17:00.0", "GPU-5c1a3f0e-0000-4000-8000-000000000001", 1, 0}, {"0000:ca:00.0", "GPU-5c1a3f0e-0000-4000-8000-000000000002", 2, 1}}
	var xml strings.Builder
	xml.WriteString("<?xml version=\"1.0\" ?>\n<!DOCTYPE nvidia_smi_log SYSTEM \"nvsmi_device_v12.dtd\">\n<nvidia_smi_log>\n\t<timestamp>Mon Nov 16 09:00:00 2026</timestamp>\n\t<driver_version>550.127.05</driver_version>\n\t<cuda_version>12.4</cuda_version>\n\t<attached_gpus>2</attached_gpus>\n")
	for i, x := range gs {
		bad := i == 1
		width := "16"
		if bad && f.GPUWidth8 {
			width = "8"
		}
		dev := g.pciDev(x.addr, "0x030200", "0x10de", "0x20b5", "nvidia", x.numa, "16.0 GT/s PCIe", width, "16.0 GT/s PCIe", "16")
		g.symlink(fmt.Sprintf("/sys/class/drm/card%d", x.card), fmt.Sprintf("../../devices/pci0000:00/%s/drm/card%d", x.addr, x.card))
		g.symlink(fmt.Sprintf("%s/drm/card%d/device", dev, x.card), "../../../"+x.addr)
		temp, ue, pend := 41+i*3, 0, "No"
		if bad {
			if f.GPUTemp > 0 {
				temp = f.GPUTemp
			}
			ue = f.GPUUE
			if f.GPURemapPend {
				pend = "Yes"
			}
		}
		fmt.Fprintf(&xml, `	<gpu id="00000000:%s">
		<product_name>NVIDIA A100 80GB PCIe</product_name>
		<product_brand>NVIDIA</product_brand>
		<serial>13244200%05d</serial>
		<uuid>%s</uuid>
		<board_part_number>900-21001-0020-100</board_part_number>
		<vbios_version>92.00.68.00.01</vbios_version>
		<pci>
			<pci_bus_id>00000000:%s</pci_bus_id>
			<pci_gpu_link_info>
				<pcie_gen>
					<max_link_gen>4</max_link_gen>
					<current_link_gen>4</current_link_gen>
					<device_current_link_gen>4</device_current_link_gen>
					<max_device_link_gen>4</max_device_link_gen>
					<max_host_link_gen>4</max_host_link_gen>
				</pcie_gen>
				<link_widths>
					<max_link_width>16x</max_link_width>
					<current_link_width>%sx</current_link_width>
				</link_widths>
			</pci_gpu_link_info>
		</pci>
		<fan_speed>N/A</fan_speed>
		<performance_state>P0</performance_state>
		<clocks_event_reasons>
			<clocks_event_reason_gpu_idle>Active</clocks_event_reason_gpu_idle>
			<clocks_event_reason_applications_clocks_setting>Not Active</clocks_event_reason_applications_clocks_setting>
			<clocks_event_reason_sw_power_cap>Not Active</clocks_event_reason_sw_power_cap>
			<clocks_event_reason_hw_slowdown>Not Active</clocks_event_reason_hw_slowdown>
			<clocks_event_reason_hw_thermal_slowdown>Not Active</clocks_event_reason_hw_thermal_slowdown>
			<clocks_event_reason_hw_power_brake_slowdown>Not Active</clocks_event_reason_hw_power_brake_slowdown>
			<clocks_event_reason_sync_boost>Not Active</clocks_event_reason_sync_boost>
			<clocks_event_reason_sw_thermal_slowdown>Not Active</clocks_event_reason_sw_thermal_slowdown>
		</clocks_event_reasons>
		<fb_memory_usage>
			<total>81920 MiB</total>
			<reserved>567 MiB</reserved>
			<used>4 MiB</used>
			<free>81349 MiB</free>
		</fb_memory_usage>
		<ecc_mode>
			<current_ecc>Enabled</current_ecc>
			<pending_ecc>Enabled</pending_ecc>
		</ecc_mode>
		<ecc_errors>
			<volatile>
				<sram_correctable>0</sram_correctable>
				<sram_uncorrectable>0</sram_uncorrectable>
				<dram_correctable>0</dram_correctable>
				<dram_uncorrectable>%d</dram_uncorrectable>
			</volatile>
			<aggregate>
				<sram_correctable>0</sram_correctable>
				<sram_uncorrectable>0</sram_uncorrectable>
				<dram_correctable>0</dram_correctable>
				<dram_uncorrectable>%d</dram_uncorrectable>
			</aggregate>
		</ecc_errors>
		<retired_pages>
			<multiple_single_bit_retirement>
				<retired_count>N/A</retired_count>
			</multiple_single_bit_retirement>
			<double_bit_retirement>
				<retired_count>N/A</retired_count>
			</double_bit_retirement>
			<pending_blacklist>N/A</pending_blacklist>
			<pending_retirement>N/A</pending_retirement>
		</retired_pages>
		<remapped_rows>
			<remapped_row_corr>0</remapped_row_corr>
			<remapped_row_unc>%d</remapped_row_unc>
			<remapped_row_pending>%s</remapped_row_pending>
			<remapped_row_failure>No</remapped_row_failure>
		</remapped_rows>
		<temperature>
			<gpu_temp>%d C</gpu_temp>
			<gpu_temp_tlimit>N/A</gpu_temp_tlimit>
			<gpu_temp_max_threshold>92 C</gpu_temp_max_threshold>
			<gpu_temp_slow_threshold>87 C</gpu_temp_slow_threshold>
			<gpu_temp_max_mem_threshold>95 C</gpu_temp_max_mem_threshold>
			<gpu_temp_max_gpu_threshold>85 C</gpu_temp_max_gpu_threshold>
			<memory_temp>%d C</memory_temp>
		</temperature>
		<gpu_power_readings>
			<power_state>P0</power_state>
			<power_draw>62.41 W</power_draw>
			<current_power_limit>300.00 W</current_power_limit>
			<default_power_limit>300.00 W</default_power_limit>
		</gpu_power_readings>
		<clocks>
			<graphics_clock>1410 MHz</graphics_clock>
			<mem_clock>1512 MHz</mem_clock>
		</clocks>
		<utilization>
			<gpu_util>0 %%</gpu_util>
			<memory_util>0 %%</memory_util>
		</utilization>
	</gpu>
`, strings.TrimPrefix(x.addr, "0000:"), 12345+i, x.uuid, strings.TrimPrefix(x.addr, "0000:"), width, ue, ue, ue, pend, temp, temp+4)
	}
	xml.WriteString("</nvidia_smi_log>\n")
	g.cmd([]string{"nvidia-smi", "-q", "-x"}, xml.String(), 0)
}

// vpd encodes a PCI VPD image: identifier string, VPD-R keywords, end tag.
func vpd(name, pn, sn string) string {
	var r []byte
	kw := func(k, v string) { r = append(r, k[0], k[1], byte(len(v))); r = append(r, v...) }
	kw("PN", pn)
	kw("EC", "A01")
	kw("SN", sn)
	kw("V0", "PCIe 4.0 x8")
	kw("RV", "\x00")
	var b []byte
	b = append(b, 0x82, byte(len(name)), byte(len(name)>>8))
	b = append(b, name...)
	b = append(b, 0x90, byte(len(r)), byte(len(r)>>8))
	b = append(b, r...)
	b = append(b, 0x78)
	return string(b)
}

// iscsiLUN adds sdf: a 43 GB volume of an iSCSI target whose session
// failed (kernel log write errors), with SCSI command errors since boot.
func (g *gen) iscsiLUN() {
	scsi := "/sys/devices/platform/host9/session1/target9:0:0/9:0:0:1"
	g.write(scsi+"/vendor", "IET     \n")
	g.write(scsi+"/model", "VIRTUAL-DISK    \n")
	g.write(scsi+"/rev", "0001\n")
	g.write(scsi+"/ioerr_cnt", "0x1d1\n")
	g.write(scsi+"/wwid", "t10.IET     VIRTUAL-DISK    00020001\n")
	blk := scsi + "/block/sdf"
	g.write(blk+"/size", fmt.Sprintf("%d\n", 43*1000*1000*1000/512))
	g.write(blk+"/ro", "0\n")
	g.write(blk+"/removable", "0\n")
	g.write(blk+"/queue/rotational", "1\n")
	g.symlink(blk+"/device", "../../../9:0:0:1")
	g.symlink("/sys/block/sdf", "../devices/platform/host9/session1/target9:0:0/9:0:0:1/block/sdf")
	g.write("/sys/class/iscsi_session/session1/targetname", "iqn.2024-01.net.example:backup.vol2\n")
	g.write("/sys/class/iscsi_session/session1/state", "FAILED\n")
	g.write("/sys/class/iscsi_connection/connection1:0/persistent_address", "10.16.40.20\n")
	g.write("/sys/class/iscsi_connection/connection1:0/persistent_port", "3260\n")
}
