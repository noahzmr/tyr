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

// An HPE-branded SATA SSD behind smartpqi: the firmware's attribute table
// has no wear or host-writes attribute, the Device Statistics log has both.
func TestSmartctlATADeviceStatistics(t *testing.T) {
	in, err := ParseSmartctl([]byte(`{
	  "smartctl": {"exit_status": 0},
	  "device": {"protocol": "ATA"},
	  "model_name": "VK000480GWEZC", "serial_number": "18311DBE479E", "firmware_version": "HPGG",
	  "logical_block_size": 512,
	  "smart_status": {"passed": true},
	  "power_on_time": {"hours": 55078}, "power_cycle_count": 90,
	  "ata_smart_attributes": {"table": [
	    {"id": 5, "name": "Reallocated_Sector_Ct", "value": 100, "worst": 100, "thresh": 10, "raw": {"value": 0, "string": "0"}},
	    {"id": 9, "name": "Power_On_Hours", "value": 100, "worst": 100, "thresh": 0, "raw": {"value": 55078, "string": "55078"}},
	    {"id": 173, "name": "Unknown_Attribute", "value": 99, "worst": 99, "thresh": 10, "raw": {"value": 23, "string": "23"}},
	    {"id": 180, "name": "Unused_Rsvd_Blk_Cnt_Tot", "value": 100, "worst": 100, "thresh": 1, "raw": {"value": 0, "string": "0"}}
	  ]},
	  "ata_smart_error_log": {"extended": {"revision": 1, "sectors": 1, "count": 0}},
	  "ata_device_statistics": {"pages": [
	    {"number": 1, "name": "General Statistics", "table": [
	      {"offset": 8, "name": "Lifetime Power-On Resets", "size": 4, "value": 90, "flags": {"valid": true}},
	      {"offset": 24, "name": "Logical Sectors Written", "size": 6, "value": 85937500000, "flags": {"valid": true}},
	      {"offset": 40, "name": "Logical Sectors Read", "size": 6, "value": 39062500000, "flags": {"valid": true}}
	    ]},
	    {"number": 4, "name": "General Errors Statistics", "table": [
	      {"offset": 8, "name": "Number of Reported Uncorrectable Errors", "size": 4, "value": 0, "flags": {"valid": true}}
	    ]},
	    {"number": 5, "name": "Temperature Statistics", "table": [
	      {"offset": 8, "name": "Current Temperature", "size": 1, "value": 23, "flags": {"valid": true}},
	      {"offset": 32, "name": "Lowest Temperature", "size": 1, "value": -2, "flags": {"valid": true}},
	      {"offset": 48, "name": "Lowest Average Short Term Temperature", "size": 1, "flags": {"valid": false}},
	      {"offset": 88, "name": "Specified Maximum Operating Temperature", "size": 1, "value": 60, "flags": {"valid": true}}
	    ]},
	    {"number": 6, "name": "Transport Statistics", "table": [
	      {"offset": 24, "name": "Number of Interface CRC Errors", "size": 4, "value": 3, "flags": {"valid": true}}
	    ]},
	    {"number": 7, "name": "Solid State Device Statistics", "table": [
	      {"offset": 8, "name": "Percentage Used Endurance Indicator", "size": 1, "value": 2, "flags": {"valid": true}}
	    ]}
	  ]}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	s := in.SMART
	eq := func(name string, got *uint64, want uint64) {
		t.Helper()
		if got == nil || *got != want {
			t.Errorf("%s = %v, want %d", name, got, want)
		}
	}
	if s.WearPercentUsed == nil || *s.WearPercentUsed != 2 {
		t.Errorf("wear = %v, want 2 (device statistics, not attribute 173)", s.WearPercentUsed)
	}
	eq("written", s.DataWrittenBytes, 85937500000*512)
	eq("read", s.DataReadBytes, 39062500000*512)
	eq("uncorrectable", s.ReportedUncorrect, 0)
	eq("crc", s.CRCErrors, 3)
	eq("error log", s.ErrorLogEntries, 0)
	eq("power cycles", s.PowerCycles, 90)
	if in.TempMaxC == nil || *in.TempMaxC != 60 {
		t.Errorf("max temperature = %v, want 60", in.TempMaxC)
	}
	if s.AvailableSpare == nil || *s.AvailableSpare != 100 || s.AvailableSpareThr == nil || *s.AvailableSpareThr != 1 {
		t.Errorf("spare = %v / %v, want 100 / 1 from attribute 180", s.AvailableSpare, s.AvailableSpareThr)
	}
}

// Without the statistics log, the vendor attributes are the fallback.
func TestSmartctlATAWearFallback(t *testing.T) {
	in, err := ParseSmartctl([]byte(`{
	  "smartctl": {"exit_status": 0}, "device": {"protocol": "ATA"},
	  "ata_smart_attributes": {"table": [
	    {"id": 173, "name": "Wear_Leveling_Count", "value": 97, "worst": 97, "thresh": 10, "raw": {"value": 90}},
	    {"id": 233, "name": "Media_Wearout_Indicator", "value": 95, "worst": 95, "thresh": 0, "raw": {"value": 0}},
	    {"id": 241, "name": "Host_Writes_32MiB", "value": 100, "worst": 100, "thresh": 0, "raw": {"value": 1000}}
	  ]}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	s := in.SMART
	if s.WearPercentUsed == nil || *s.WearPercentUsed != 5 {
		t.Errorf("wear = %v, want 5 (233 before 173)", s.WearPercentUsed)
	}
	if s.DataWrittenBytes == nil || *s.DataWrittenBytes != 1000<<25 {
		t.Errorf("written = %v, want %d", s.DataWrittenBytes, uint64(1000<<25))
	}
}

func TestSmartctlSASWritten(t *testing.T) {
	in, err := ParseSmartctl([]byte(`{
	  "smartctl": {"exit_status": 0}, "device": {"protocol": "SCSI"},
	  "scsi_percentage_used_endurance_indicator": 4,
	  "scsi_start_stop_cycle_counter": {"accumulated_start_stop_cycles": 31},
	  "scsi_error_counter_log": {
	    "read": {"total_uncorrected_errors": 0, "gigabytes_processed": "1234.500"},
	    "write": {"total_uncorrected_errors": 0, "gigabytes_processed": "800.000"}
	  }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	s := in.SMART
	if s.DataWrittenBytes == nil || *s.DataWrittenBytes != 800e9 || s.DataReadBytes == nil || *s.DataReadBytes != 1234.5e9 {
		t.Errorf("written/read = %v/%v", s.DataWrittenBytes, s.DataReadBytes)
	}
	if s.PowerCycles == nil || *s.PowerCycles != 31 || s.WearPercentUsed == nil || *s.WearPercentUsed != 4 {
		t.Errorf("cycles/wear = %v/%v", s.PowerCycles, s.WearPercentUsed)
	}
}
