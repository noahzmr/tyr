package storage

import (
	"context"
	"testing"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/fixture"
	"github.com/noahzmr/tyr/internal/model"
)

func collect(t *testing.T, name string) *model.Storage {
	t.Helper()
	env, _, err := fixture.Load("../../../testdata/fixtures/"+name, collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return collector.Collect(context.Background(), env, []collector.Collector{New()}, nil).Storage
}

func codes(s *model.Storage) map[string][]model.Finding {
	m := map[string][]model.Finding{}
	for _, f := range s.Findings {
		m[f.Code] = append(m[f.Code], f)
	}
	return m
}

func disk(s *model.Storage, n string) *model.Disk {
	for i := range s.Disks {
		if s.Disks[i].Name == n {
			return &s.Disks[i]
		}
	}
	return nil
}

func TestHealthy(t *testing.T) {
	s := collect(t, "server-healthy")
	if s.Status != model.StatusOK || s.Summary != "6/6 disks OK, md 2/2 OK, fullest /data 41%" {
		t.Errorf("%v %q %+v", s.Status, s.Summary, s.Findings)
	}
	n := disk(s, "nvme0n1")
	if n == nil || n.Kind != "nvme" || n.Model != "SAMSUNG MZQL21T9HCJR-00A07" || n.Serial != "S64GNE0R100001" || *n.TempCelsius != 38.85 ||
		*n.TempThreshold.UpperCrit != 84.85 || n.SMART == nil || n.SMART.Source != "smartctl" || len(n.Holders) != 1 || n.Holders[0] != "md0" {
		t.Errorf("nvme0n1 %+v", n)
	}
	h := disk(s, "sda")
	if h == nil || h.Kind != "hdd" || h.Interface != "sata" || *h.RotationRPM != 7200 || h.Serial != "WJG0A001" || *h.IOErrors != 0 {
		t.Errorf("sda %+v", h)
	}
	if disk(s, "loop0") != nil || disk(s, "md0") != nil {
		t.Error("loop/md devices must not be listed as disks")
	}
	if len(s.Filesystems) != 3 || s.Filesystems[0].Mountpoint != "/" || s.Filesystems[2].FSType != "xfs" {
		t.Errorf("filesystems %+v", s.Filesystems)
	}
	if st, _ := s.Source("smart"); st.State != model.AvailOK || st.Reason != "via smartctl" {
		t.Errorf("smart source %+v", st)
	}
}

func TestDegraded(t *testing.T) {
	s := collect(t, "server-degraded")
	if s.Status != model.StatusCrit {
		t.Fatal(s.Status)
	}
	c := codes(s)
	want := map[string]string{
		"md_degraded":      "md1 (raid5) degraded: 3/4 devices, failed sdd1",
		"fs_read_only":     "/data (xfs) is mounted read-only",
		"fs_full":          "/data 96% full",
		"raid_vd":          "RAID virtual drive 0/0 (RAID5 archive) state Dgrd",
		"raid_pd":          "RAID physical drive 252:2 (ST2000NM0055-1V4104) state Failed",
		"kernel_io_errors": "sdd: 2 I/O errors in kernel log",
		"pending_sectors":  "sdc: 8 pending sectors",
		"wear":             "nvme1n1: 93% of rated endurance used",
		"io_errors":        "sdd: 31 I/O errors (ioerr_cnt)",
	}
	for code, msg := range want {
		if len(c[code]) != 1 || c[code][0].Message != msg {
			t.Errorf("%s: %+v, want %q", code, c[code], msg)
		}
	}
	if d := disk(s, "sdd"); d.Status != model.StatusCrit || d.KernelErrors != 2 {
		t.Errorf("sdd %+v", d)
	}
	if d := disk(s, "sde"); d.SMART != nil || d.Model != "MR9361-8i" || d.Vendor != "AVAGO" {
		t.Errorf("sde (RAID VD) %+v", d)
	}
	if st, _ := s.Source("smart"); st.State != model.AvailOK || st.Reason == "via smartctl" {
		t.Errorf("smart source must mention the disk without SMART: %+v", st)
	}
	if len(s.Controllers) != 1 || s.Controllers[0].Status != model.StatusCrit {
		t.Errorf("controllers %+v", s.Controllers)
	}
	snap := &model.Snapshot{Storage: s}
	if cnt := snap.Counters(); cnt["disk.sdc.smart.reallocated"] != 24 || cnt["disk.sdd.kernel_errors"] != 2 {
		t.Errorf("counters %v", cnt)
	}
}

func TestLaptop(t *testing.T) {
	s := collect(t, "laptop-amd-nonroot")
	if len(s.Disks) != 1 || s.Disks[0].Serial != "SERIAL0001" || s.Disks[0].TempCelsius == nil || s.Status != model.StatusOK {
		t.Errorf("%+v %+v", s.Disks, s.Findings)
	}
	if st, _ := s.Source("smart"); st.State != model.AvailUnavailable {
		t.Errorf("smart %+v", st)
	}
	if len(s.Filesystems) < 2 || s.Filesystems[0].UsedPercent == nil {
		t.Errorf("fs %+v", s.Filesystems)
	}
}
