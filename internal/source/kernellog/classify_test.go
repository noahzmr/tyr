package kernellog

import (
	"strings"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/model"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		msg  string
		cat  Category
		sev  model.Status
		subj string
	}{
		{"mce: [Hardware Error]: CPU 12: Machine Check: 0 Bank 7: cc00008000010090", CatMCE, model.StatusWarn, "cpu12"},
		{"mce: [Hardware Error]: CPU 3: Machine Check Exception: 5 Bank 4: b200000000070f0f", CatMCE, model.StatusCrit, "cpu3"},
		{"mce: [Hardware Error]: CPU 9: Machine Check: 0 Bank 5: be00000000800400", CatMCE, model.StatusCrit, "cpu9"}, // UC bit set
		{"mce: [Hardware Error]: Machine check events logged", CatMCE, model.StatusWarn, ""},
		{"[Hardware Error]: Corrected error, no action required.", CatMCE, model.StatusWarn, ""},
		{"mce: CPU20: Core temperature above threshold, cpu clock throttled (total events = 1)", CatThermal, model.StatusWarn, "cpu20"},
		{"thermal thermal_zone2: critical temperature reached, shutting down", CatThermal, model.StatusCrit, "thermal_zone2"},
		{"EDAC MC0: 1 CE memory read error on CPU_SrcID#0_MC#0_Chan#1_DIMM#0 (channel:1 slot:0 page:0x3a2f40 offset:0xa18 grain:32 syndrome:0x0 - err_code:0x0000:0x009f)", CatEDAC, model.StatusWarn, "CPU_SrcID#0_MC#0_Chan#1_DIMM#0"},
		{"EDAC skx MC1: 2 UE memory read error on DIMM_B1 (channel:0 slot:0)", CatEDAC, model.StatusCrit, "DIMM_B1"},
		{"pcieport 0000:00:03.0: AER: Corrected error received: 0000:3b:00.0", CatAER, model.StatusWarn, "0000:3b:00.0"},
		{"pcieport 0000:00:1c.0: AER: Correctable error message received from 0000:02:00.0", CatAER, model.StatusWarn, "0000:02:00.0"},
		{"pcieport 0000:00:01.0: AER: Uncorrected (Fatal) error received: 0000:01:00.0", CatAER, model.StatusCrit, "0000:01:00.0"},
		{"pcieport 0000:00:01.0: AER: Uncorrected (Non-Fatal) error received: 0000:01:00.0", CatAER, model.StatusWarn, "0000:01:00.0"},
		{"{1}[Hardware Error]: event severity: fatal", CatHW, model.StatusCrit, ""},
		{"I/O error, dev sdb, sector 1953525160 op 0x0:(READ) flags 0x80700 phys_seg 1 prio class 0", CatIO, model.StatusCrit, "sdb"},
		{"blk_update_request: critical medium error, dev sda, sector 123456", CatIO, model.StatusCrit, "sda"},
		{"Buffer I/O error on dev sdc1, logical block 0, async page read", CatIO, model.StatusCrit, "sdc"},
		{"sd 2:0:0:0: [sdc] tag#5 Sense Key : Medium Error [current]", CatIO, model.StatusCrit, "sdc"},
		{"ata3.00: exception Emask 0x0 SAct 0x0 SErr 0x0 action 0x6 frozen", CatIO, model.StatusWarn, "ata3"},
		{"nvme nvme1: I/O 412 QID 7 timeout, aborting", CatIO, model.StatusWarn, "nvme1"},
		{"nvme nvme1: controller is down; will reset: CSTS=0xffffffff", CatIO, model.StatusCrit, "nvme1"},
		{"ixgbe 0000:3b:00.1 ens1f1: NIC Link is Down", CatLink, model.StatusWarn, "ens1f1"},
		{"ixgbe 0000:3b:00.1 ens1f1: NIC Link is Up 10 Gbps, Flow Control: RX/TX", CatLink, model.StatusOK, "ens1f1"},
		{"mlx5_core 0000:5e:00.0 ens2f0np0: Link down", CatLink, model.StatusWarn, "ens2f0np0"},
		{"tg3 0000:02:00.0 eno1: Link is down", CatLink, model.StatusWarn, "eno1"},
		{"bond0: (slave eno2): link status definitely down, disabling slave", "", 0, ""},
		{"NETDEV WATCHDOG: eno1 (igb): transmit queue 2 timed out 5104 ms", CatLink, model.StatusWarn, "eno1"},
		{"e1000e 0000:00:1f.6 eno1: Detected Hardware Unit Hang:", CatLink, model.StatusWarn, "eno1"},
		{"NVRM: Xid (PCI:0000:3b:00): 79, pid='<unknown>', name=<unknown>, GPU has fallen off the bus.", CatGPU, model.StatusCrit, "0000:3b:00.0"},
		{"NVRM: Xid (PCI:0000:5e:00): 13, pid=1234, name=python, Graphics Exception", CatGPU, model.StatusWarn, "0000:5e:00.0"},
		{"NVRM: Xid (PCI:0000:5e:00): 48, pid=1234, An uncorrectable double bit error (DBE) has been detected", CatGPU, model.StatusCrit, "0000:5e:00.0"},
		{"amdgpu 0000:03:00.0: amdgpu: GPU reset begin!", CatGPU, model.StatusWarn, "0000:03:00.0"},
		{"amdgpu 0000:c4:00.0: [drm:amdgpu_job_timedout [amdgpu]] *ERROR* ring gfx_0.0.0 timeout, signaled seq=1, emitted seq=2", CatGPU, model.StatusWarn, "0000:c4:00.0"},
		{"i915 0000:00:02.0: [drm] GPU HANG: ecode 12:1:85dffffb, in Xorg [1234]", CatGPU, model.StatusWarn, "0000:00:02.0"},
		{"amdxdna 0000:c5:00.1: aie2_mgmt_fw_init: Failed to boot firmware, ret -110 timeout", CatGPU, model.StatusWarn, "0000:c5:00.1"},
		{"usb 3-4: reset full-speed USB device number 4 using xhci_hcd", "", 0, ""},
		{"Linux version 6.8.0", "", 0, ""},
	}
	for _, c := range cases {
		got := Classify(c.msg)
		if got.Category != c.cat || got.Severity != c.sev || got.Subject != c.subj {
			t.Errorf("Classify(%q) = {%q %v %q}, want {%q %v %q}", c.msg, got.Category, got.Severity, got.Subject, c.cat, c.sev, c.subj)
		}
	}
}

func TestParseKmsg(t *testing.T) {
	boot := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	in := "6,100,5000000,-;Linux version 6.8.0\n" +
		"4,101,7250000,-;mce: [Hardware Error]: CPU 12: Machine Check: 0 Bank 7: cc00008000010090\n" +
		" SUBSYSTEM=machinecheck\n" +
		"3,102,9000000,-;I/O error, dev sdb, sector 8 op 0x0:(READ)\\x0a\n"
	l, err := ParseKmsg(strings.NewReader(in), boot)
	if err != nil {
		t.Fatal(err)
	}
	if l.Total != 3 || len(l.Entries) != 2 {
		t.Fatalf("total=%d entries=%d", l.Total, len(l.Entries))
	}
	e := l.Entries[0]
	if e.Subject != "cpu12" || e.Priority != 4 || !e.Time.Equal(boot.Add(7250*time.Millisecond)) {
		t.Errorf("entry: %+v", e)
	}
}

func TestParseJournal(t *testing.T) {
	in := `{"MESSAGE":"ixgbe 0000:3b:00.1 ens1f1: NIC Link is Down","PRIORITY":"6","__REALTIME_TIMESTAMP":"1794808302000000"}
{"MESSAGE":[69,68,65,67,32,77,67,48,58,32,49,32,67,69,32,120,32,111,110,32,68,73,77,77,95,65,49,32,40,41],"PRIORITY":"4","__REALTIME_TIMESTAMP":"1794808303000000"}
{"MESSAGE":"hello","PRIORITY":"6","__REALTIME_TIMESTAMP":"1794808304000000"}
`
	l, err := ParseJournal([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if l.Total != 3 || len(l.Entries) != 2 {
		t.Fatalf("total=%d entries=%d %+v", l.Total, len(l.Entries), l.Entries)
	}
	if l.Entries[1].Category != CatEDAC || l.Entries[1].Subject != "DIMM_A1" {
		t.Errorf("byte-array message not decoded: %+v", l.Entries[1])
	}
	if l.Entries[0].Time.Unix() != 1794808302 {
		t.Errorf("time: %v", l.Entries[0].Time)
	}
}

func TestParseDmesgRaw(t *testing.T) {
	boot := time.Unix(1000, 0)
	l := ParseDmesgRaw([]byte("<4>[   12.500000] mce: [Hardware Error]: Machine check events logged\n<6>[   13.000000] hello\n"), boot)
	if l.Total != 2 || len(l.Entries) != 1 || !l.Entries[0].Time.Equal(time.Unix(1012, 500000000)) {
		t.Fatalf("%+v", l)
	}
}
