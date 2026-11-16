package smart

import (
	"encoding/binary"
	"os"
	"testing"
)

const fx = "../../../testdata/fixtures/server-degraded/cmd/"

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fx + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSmartctlATA(t *testing.T) {
	in, err := ParseSmartctl(read(t, "smartctl_-j_-x__dev_sdc.txt"))
	if err != nil {
		t.Fatal(err)
	}
	s := in.SMART
	if in.Model != "ST4000NM000A-2HZ100" || in.Serial != "WJG0A003" || *in.RotationRPM != 7200 || *in.TempC != 35 || in.SizeBytes != 4e12 {
		t.Errorf("identity %+v", in)
	}
	if !*s.Passed || *s.Reallocated != 24 || *s.Pending != 8 || *s.CRCErrors != 0 || *s.PowerOnHours != 19832 || s.Source != "smartctl" {
		t.Errorf("smart %+v", s)
	}
}

func TestSmartctlNVMe(t *testing.T) {
	in, err := ParseSmartctl(read(t, "smartctl_-j_-x__dev_nvme1n1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	s := in.SMART
	if *s.WearPercentUsed != 93 || *s.MediaErrors != 3 || *s.NVMeCriticalWarn != 0 || *s.AvailableSpare != 100 || *s.DataWrittenBytes != 801234567*512000 {
		t.Errorf("nvme %+v", s)
	}
}

func TestSmartctlOpenFailed(t *testing.T) {
	if _, err := ParseSmartctl(read(t, "smartctl_-j_-x__dev_sde.txt")); err == nil || err.Error() == "" {
		t.Error("MegaRAID virtual drive must report open failure")
	}
	if _, err := ParseSmartctl([]byte("not json")); err == nil {
		t.Error("bad json")
	}
	// failing disk: exit status bit 3 overrides smart_status
	in, err := ParseSmartctl([]byte(`{"smartctl":{"exit_status":8},"device":{"protocol":"ATA"},"smart_status":{"passed":true},
		"ata_smart_attributes":{"table":[{"id":5,"value":5,"thresh":10,"when_failed":"now","raw":{"value":3000}},{"id":231,"value":12,"raw":{"value":12}}]}}`))
	if err != nil || *in.SMART.Passed || *in.SMART.Reallocated != 3000 || *in.SMART.WearPercentUsed != 88 {
		t.Errorf("failing disk %+v %v", in, err)
	}
}

func TestNVMeLog(t *testing.T) {
	b := make([]byte, 512)
	b[0] = 0x04 // reliability degraded
	binary.LittleEndian.PutUint16(b[1:], 273+47)
	b[3], b[4], b[5] = 7, 10, 101
	binary.LittleEndian.PutUint64(b[48:], 1000)
	binary.LittleEndian.PutUint64(b[128:], 12345)
	binary.LittleEndian.PutUint64(b[160:], 9)
	in, err := ParseNVMeLog(b)
	if err != nil {
		t.Fatal(err)
	}
	s := in.SMART
	if *in.TempC != 47 || *s.NVMeCriticalWarn != 4 || *s.AvailableSpare != 7 || *s.AvailableSpareThr != 10 || *s.WearPercentUsed != 101 ||
		*s.PowerOnHours != 12345 || *s.MediaErrors != 9 || *s.DataWrittenBytes != 512000000 {
		t.Errorf("%+v %+v", in, s)
	}
	if _, err := ParseNVMeLog(b[:100]); err == nil {
		t.Error("short log must fail")
	}
}

func TestNVMeCLI(t *testing.T) {
	// nvme-cli 2.x prints large counters as strings and temperature in Kelvin
	in, err := ParseNVMeCLI([]byte(`{"critical_warning":0,"temperature":310,"avail_spare":100,"spare_thresh":10,"percent_used":4,
		"data_units_read":"12,345","data_units_written":678,"power_on_hours":9000,"unsafe_shutdowns":12,"media_errors":0,"num_err_log_entries":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if *in.TempC != 37 || *in.SMART.WearPercentUsed != 4 || *in.SMART.DataReadBytes != 12345*512000 || *in.SMART.ErrorLogEntries != 2 {
		t.Errorf("%+v %+v", in, in.SMART)
	}
	if _, err := ParseNVMeCLI([]byte(`{}`)); err == nil {
		t.Error("missing critical_warning must fail")
	}
}
