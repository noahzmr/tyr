package raid

import (
	"os"
	"testing"

	"github.com/noahzmr/tyr/internal/model"
)

func TestMdstatFixture(t *testing.T) {
	b, _ := os.ReadFile("../../../testdata/fixtures/server-degraded/root/proc/mdstat")
	as, err := ParseMdstat(b)
	if err != nil || len(as) != 2 {
		t.Fatalf("%v %+v", err, as)
	}
	md0, md1 := as[0], as[1]
	if md0.Name != "md0" || md0.Level != "raid1" || md0.Degraded || md0.Status != model.StatusOK || len(md0.Members) != 2 || md0.SizeBytes != 1874853888*1024 {
		t.Errorf("md0 %+v", md0)
	}
	if !md1.Degraded || md1.Devices != 4 || md1.ActiveDevices != 3 || !md1.Members[0].Failed || md1.Members[0].Device != "sdd1" || md1.Status != model.StatusCrit {
		t.Errorf("md1 %+v", md1)
	}
}

func TestMdstatStates(t *testing.T) {
	in := `Personalities : [raid1] [raid10]
md2 : active raid10 sdh[3] sdg[2] sdf[1] sde[0]
      1953262592 blocks super 1.2 512K chunks 2 near-copies [4/4] [UUUU]
      [=>...................]  check =  8.3% (162270080/1953262592) finish=152.1min speed=196224K/sec

md3 : active raid1 sdj1[2] sdi1[0]
      976630464 blocks super 1.2 [2/1] [U_]
      [===>.................]  recovery = 17.4% (170287552/976630464) finish=72.3min speed=185786K/sec

md4 : active (auto-read-only) raid1 sdl[1] sdk[0]
      488254464 blocks super 1.2 [2/2] [UU]
      	resync=PENDING

md127 : inactive sdm[0](S)
      976630488 blocks super 1.2

unused devices: <none>
`
	as, err := ParseMdstat([]byte(in))
	if err != nil || len(as) != 4 {
		t.Fatalf("%v %d", err, len(as))
	}
	if a := as[0]; a.SyncAction != "check" || *a.SyncPercent != 8.3 || a.Status != model.StatusOK {
		t.Errorf("check %+v", a)
	}
	if a := as[1]; a.SyncAction != "recovery" || a.SyncFinish != "72.3min" || !a.Degraded || a.Status != model.StatusCrit {
		t.Errorf("recovery %+v", a)
	}
	if a := as[2]; a.Level != "raid1" || a.SyncAction != "resync pending" || a.Status != model.StatusWarn {
		t.Errorf("pending %+v", a)
	}
	if a := as[3]; a.State != "inactive" || !a.Members[0].Spare || a.Status != model.StatusCrit {
		t.Errorf("inactive %+v", a)
	}
}

func TestStorcli(t *testing.T) {
	b, _ := os.ReadFile("../../../testdata/fixtures/server-degraded/cmd/storcli64__call_show_all_J.txt")
	cs, err := ParseStorcli(b, "storcli")
	if err != nil || len(cs) != 1 {
		t.Fatalf("%v %+v", err, cs)
	}
	c := cs[0]
	if c.Model != "AVAGO MegaRAID SAS 9361-8i" || c.Firmware != "4.680.00-8527" || c.State != "Optimal" || c.Status != model.StatusCrit {
		t.Errorf("ctrl %+v", c)
	}
	if len(c.VDs) != 1 || c.VDs[0].State != "Dgrd" || c.VDs[0].Status != model.StatusCrit || len(c.PDs) != 4 || c.PDs[2].Status != model.StatusCrit {
		t.Errorf("vd/pd %+v %+v", c.VDs, c.PDs)
	}
	if c.Battery == nil || c.Battery.Type != "CacheVault" || *c.Battery.TempCelsius != 28 || c.Battery.Status != model.StatusOK {
		t.Errorf("cv %+v", c.Battery)
	}
	none, err := ParseStorcli([]byte(`{"Controllers":[{"Command Status":{"Status":"Failure","Description":"No Controller found"}}]}`), "storcli")
	if err != nil || len(none) != 0 {
		t.Errorf("no controller: %v %v", none, err)
	}
}

func TestStorcliPDs(t *testing.T) {
	b, _ := os.ReadFile("../../../testdata/fixtures/server-degraded/cmd/storcli64__call_eall_sall_show_all_J.txt")
	m, err := ParseStorcliPDs(b)
	if err != nil {
		t.Fatal(err)
	}
	if d := m[0]["252:2"]; d.Serial != "ZFL4A002" || d.WWN != "5000C500B2C3D002" || d.Firmware != "SN05" || len(m[0]) != 4 {
		t.Errorf("%+v", m)
	}
}
