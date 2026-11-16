package ipmi

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/execx"
	"github.com/noahzmr/tyr/internal/model"
)

const fx = "../../../testdata/fixtures/"

func read(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(fx + p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func byName(ss []Sensor, n string) *Sensor {
	for i := range ss {
		if ss[i].Name == n {
			return &ss[i]
		}
	}
	return nil
}

func TestParseIPMIToolSensors(t *testing.T) {
	ss, err := ParseIPMIToolSensors(read(t, "server-degraded/cmd/ipmitool_sensor.txt"))
	if err != nil {
		t.Fatal(err)
	}
	cpu := byName(ss, "CPU2 Temp")
	if cpu == nil || *cpu.Value != 93 || cpu.Status != model.StatusWarn || cpu.Kind != model.KindTemperature ||
		*cpu.Thresholds.UpperWarn != 91 || *cpu.Thresholds.UpperCrit != 96 || cpu.Thresholds.Origin != "ipmi" {
		t.Errorf("CPU2 Temp %+v", cpu)
	}
	v := byName(ss, "12V")
	if v == nil || *v.Value != 12.192 || v.Unit != model.UnitVolts || *v.Thresholds.LowerCrit != 10.272 || *v.Thresholds.LowerWarn != 10.784 {
		t.Errorf("12V %+v %+v", v, v.Thresholds)
	}
	if f := byName(ss, "FANA"); f == nil || f.Value != nil || f.Status != model.StatusUnknown || f.Kind != model.KindFan {
		t.Errorf("FANA %+v", f)
	}
	if p := byName(ss, "PS1 Status"); p == nil || p.Discrete != "0x1" || p.Unit != "" {
		t.Errorf("PS1 %+v", p)
	}
	if d := byName(ss, "P2-DIMMC1 Temp"); d == nil || d.Status != model.StatusWarn {
		t.Errorf("dimm %+v", d)
	}
	// lower thresholds of 0.000 on temperatures mean "unused"
	in := []byte("Inlet Temp | 22.000 | degrees C | ok | 0.000 | 0.000 | 0.000 | 42.000 | 47.000 | 50.000\n")
	ss, _ = ParseIPMIToolSensors(in)
	if ss[0].Thresholds.LowerCrit != nil || *ss[0].Thresholds.UpperCrit != 47 {
		t.Errorf("zero lower thresholds kept: %+v", ss[0].Thresholds)
	}
}

func TestParseIPMIUtilSensors(t *testing.T) {
	ss, err := ParseIPMIUtilSensors(read(t, "server-healthy/cmd/ipmiutil_sensor_-c.txt"))
	if err != nil {
		t.Fatal(err)
	}
	cpu := byName(ss, "CPU1 Temp")
	if cpu == nil || *cpu.Value != 52 || cpu.Unit != model.UnitCelsius || cpu.Status != model.StatusOK || cpu.Thresholds != nil {
		t.Errorf("CPU1 %+v", cpu)
	}
	if f := byName(ss, "FAN3"); f == nil || f.Kind != model.KindFan || *f.Value != 6620 {
		t.Errorf("FAN3 %+v", f)
	}
	if p := byName(ss, "PS2 Status"); p == nil || p.Discrete != "Presence detected" || p.Status != model.StatusOK {
		t.Errorf("PS2 %+v", p)
	}
	if f := byName(ss, "FANA"); f == nil || f.Status != model.StatusUnknown {
		t.Errorf("FANA %+v", f)
	}
	for _, c := range []struct {
		in   string
		want model.Status
	}{{"BelowNonCrit", model.StatusWarn}, {"AboveCrit", model.StatusCrit}, {"Warn", model.StatusWarn}, {"Absent", model.StatusUnknown}, {"NonRec", model.StatusCrit}} {
		if got := ipmiutilStatus(c.in); got != c.want {
			t.Errorf("ipmiutilStatus(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseIPMIToolSEL(t *testing.T) {
	sel, err := ParseIPMIToolSEL(read(t, "server-degraded/cmd/ipmitool_sel_elist.txt"))
	if err != nil {
		t.Fatal(err)
	}
	sel.Used, sel.Capacity = parseIPMIToolSELInfo(read(t, "server-degraded/cmd/ipmitool_sel_info.txt"))
	if len(sel.Entries) != 7 || sel.Used != 7 || sel.Capacity != 1023 {
		t.Fatalf("entries=%d used=%d cap=%d", len(sel.Entries), sel.Used, sel.Capacity)
	}
	want := []struct {
		typ, sensor, cat string
		sev              model.Status
		asserted         bool
	}{
		{"Event Logging Disabled", "#0x07", "system", model.StatusOK, true},
		{"Memory", "#0x87", "memory", model.StatusWarn, true},
		{"Memory", "#0x87", "memory", model.StatusCrit, true},
		{"Power Supply", "PS2 Status", "psu", model.StatusCrit, true},
		{"Power Unit", "#0xc9", "psu", model.StatusCrit, true},
		{"Temperature", "CPU2 Temp", "temperature", model.StatusWarn, true},
		{"Temperature", "CPU2 Temp", "temperature", model.StatusOK, false},
	}
	for i, w := range want {
		e := sel.Entries[i]
		if e.SensorType != w.typ || e.Sensor != w.sensor || e.Category != w.cat || e.Severity != w.sev || e.Asserted != w.asserted {
			t.Errorf("entry %d: %+v, want %+v", i, e, w)
		}
	}
	if !sel.Entries[1].Time.Equal(time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("time %v", sel.Entries[1].Time)
	}
	// Pre-init timestamps and Dell-style records
	in := []byte("  1a | Pre-Init  |0000000000| System Event #0xff | Timestamp Clock Sync | Asserted\n" +
		"  1b | 03/02/2025 | 11:41:07 | Drive Slot / Bay Drive 2 | Drive Fault | Asserted\n" +
		"  1c | 03/02/2025 | 11:45:00 | Processor CPU1 Status | IERR | Asserted\n")
	sel, _ = ParseIPMIToolSEL(in)
	if !sel.Entries[0].Time.IsZero() || sel.Entries[0].Severity != model.StatusOK {
		t.Errorf("pre-init %+v", sel.Entries[0])
	}
	if e := sel.Entries[1]; e.SensorType != "Drive Slot / Bay" || e.Category != "drive" || e.Severity != model.StatusCrit {
		t.Errorf("drive %+v", e)
	}
	if e := sel.Entries[2]; e.Category != "processor" || e.Severity != model.StatusCrit {
		t.Errorf("ierr %+v", e)
	}
}

func TestParseIPMIUtilSEL(t *testing.T) {
	sel, err := ParseIPMIUtilSEL(read(t, "server-healthy/cmd/ipmiutil_sel.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Entries) != 2 || sel.Capacity != 1023 || sel.Used != 2 || *sel.FillPercent() > 1 {
		t.Fatalf("%+v", sel)
	}
	in := []byte("SEL Ver 51 Support 0f, Size = 512 records (Used=3, Free=509)\n" +
		"0002 12/03/21 14:01:52 MAJ BMC  Power Supply #51 AC Lost asserted 6f [03 ff ff]\n" +
		"0003 12/03/21 15:20:45 MIN BMC  Memory #08 Correctable ECC, DIMM2 asserted 6f [00 ff 01]\n" +
		"0004 12/03/21 15:30:00 MAJ BMC  Power Supply #51 AC Lost deasserted 6f [03 ff ff]\n")
	sel, err = ParseIPMIUtilSEL(in)
	if err != nil || len(sel.Entries) != 3 {
		t.Fatalf("%+v %v", sel, err)
	}
	if e := sel.Entries[0]; e.SensorType != "Power Supply" || e.Event != "AC Lost" || e.Severity != model.StatusCrit || e.Category != "psu" {
		t.Errorf("ac lost %+v", e)
	}
	if e := sel.Entries[1]; e.Category != "memory" || e.Severity != model.StatusWarn {
		t.Errorf("ce %+v", e)
	}
	if e := sel.Entries[2]; e.Asserted || e.Severity != model.StatusOK {
		t.Errorf("deasserted %+v", e)
	}
}

func TestAllowlist(t *testing.T) {
	rp := &execx.Replay{Tools: map[string]string{"ipmiutil": "/x"}, Commands: map[string]execx.Recorded{}}
	c, err := Find(rp)
	if err != nil || c.Tool != IPMIUtil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"power", "-d"}, {"sel", "-d"}, {"sensor", "-u", "1"}, {"raw", "0x00", "0x02"}} {
		if _, err := c.run(context.Background(), args...); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%v must be refused, got %v", args, err)
		}
	}
	if _, err := Find(&execx.Replay{}); err == nil {
		t.Error("no tools must fail")
	}
}

func TestFRUAndMCInfo(t *testing.T) {
	frus := ParseFRU(read(t, "server-degraded/cmd/ipmitool_fru_print.txt"))
	if len(frus) != 3 || frus[0].Name != "Builtin FRU Device" || frus[0].Serial != "S412345X3A01234" || frus[0].ChassisSerial != "C1160LL12AB0123" ||
		frus[2].Name != "PS2" || frus[2].Serial != "P1K22CL21123452" || frus[2].Manufacturer != "SUPERMICRO" {
		t.Errorf("%+v", frus)
	}
	rp := &execx.Replay{Tools: map[string]string{"ipmitool": "/x"}, Commands: map[string]execx.Recorded{
		"ipmitool mc info": {Stdout: read(t, "server-degraded/cmd/ipmitool_mc_info.txt")}}}
	c, _ := Find(rp)
	v, fw, err := c.MCInfo(context.Background())
	if err != nil || v != "Supermicro" || fw != "1.40" {
		t.Errorf("%q %q %v", v, fw, err)
	}
}
