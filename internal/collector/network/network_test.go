package network

import (
	"context"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/fixture"
	"github.com/noahzmr/tyr/internal/model"
)

func load(t *testing.T, name string, nicAll bool) *collector.Env {
	t.Helper()
	opts := collector.DefaultOptions()
	opts.NICAll = nicAll
	env, _, err := fixture.Load("../../../testdata/fixtures/"+name, opts)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func collect(t *testing.T, name string) *model.Network {
	return collector.Collect(context.Background(), load(t, name, false), []collector.Collector{New()}, nil).Network
}

func nic(n *model.Network, name string) *model.NIC {
	for i := range n.Interfaces {
		if n.Interfaces[i].Name == name {
			return &n.Interfaces[i]
		}
	}
	return nil
}

func TestHealthy(t *testing.T) {
	n := collect(t, "server-healthy")
	if n.Status != model.StatusOK || n.Summary != "3/4 ports up, bond0 OK" || n.Hidden != 1 {
		t.Errorf("%v %q hidden=%d %+v", n.Status, n.Summary, n.Hidden, n.Findings)
	}
	e := nic(n, "ens1f0")
	if e == nil || e.Vendor != "Intel" || e.Model != "Ethernet Controller E810-XXV for SFP" || e.Firmware != "4.40 0x8001c967 1.3534.0" ||
		*e.MaxSpeedMbps != 25000 || e.PortType != "FIBRE" || e.Module == nil || e.Module.Vendor != "FINISAR CORP." || e.LLDP.System != "leaf-a01.dc1" ||
		*e.TempCelsius != 52 || e.Master != "bond0" {
		t.Errorf("ens1f0 %+v", e)
	}
	b := nic(n, "bond0")
	if b == nil || b.Bond == nil || len(b.Members) != 2 || b.Status != model.StatusOK {
		t.Errorf("bond0 %+v", b)
	}
	if v := nic(n, "bond0.100"); v == nil || *v.VLANID != 100 || v.Addresses[0] != "10.20.0.21/24" {
		t.Errorf("vlan %+v", v)
	}
	if nic(n, "docker0") != nil {
		t.Error("docker0 must be hidden without --nic-all")
	}
	all := collector.Collect(context.Background(), load(t, "server-healthy", true), []collector.Collector{New()}, nil).Network
	if nic(all, "docker0") == nil || all.Hidden != 0 {
		t.Error("--nic-all must show docker0")
	}
}

func TestDegraded(t *testing.T) {
	n := collect(t, "server-degraded")
	if n.Status != model.StatusCrit || n.Summary != "2/4 ports up, bond0 CRIT" {
		t.Errorf("%v %q", n.Status, n.Summary)
	}
	got := map[string]string{}
	for _, f := range n.Findings {
		got[f.Code] = f.Message
	}
	for code, msg := range map[string]string{
		"bond_degraded":   "bond0 degraded: ens1f1 down (1/2 members up)",
		"link_down":       "ens1f1 link down",
		"half_duplex":     "eno1 running half duplex",
		"speed_below_max": "eno1 at 100M, port supports 1G",
		"crc_errors":      "ens1f0: 1532 CRC errors since boot (cable/optics)",
		"transceiver":     "ens1f0 transceiver rx power -13.90 dBm out of range",
	} {
		if got[code] != msg {
			t.Errorf("%s: %q, want %q", code, got[code], msg)
		}
	}
	if n.BMC == nil || n.BMC.IP != "10.10.0.121" {
		t.Errorf("bmc %+v", n.BMC)
	}
	if e := nic(n, "eno2"); e.Status != model.StatusOK {
		t.Error("admin-down unused port must be OK")
	}
}

func TestRefreshRatesFlapsAndErrors(t *testing.T) {
	env := load(t, "server-degraded", false)
	first := collector.Collect(context.Background(), env, []collector.Collector{New()}, nil)
	prev := *first.Network
	prev.CollectedAt = env.Now().Add(-5 * time.Second)
	prev.Interfaces = append([]model.NIC(nil), first.Network.Interfaces...)
	for i := range prev.Interfaces {
		p := &prev.Interfaces[i]
		if p.Name == "ens1f0" {
			p.Counters.RxBytes -= 125_000_000 // 1 Gbit in 5 s = 200 Mbit/s
			p.Counters.RxCRC -= 10
			p.Counters.RxErrors -= 10
			p.CarrierChanges = model.U(*p.CarrierChanges - 2)
		}
	}
	env.NewRound()
	sec, err := New().Collect(context.Background(), env, &prev)
	if err != nil {
		t.Fatal(err)
	}
	r := sec.(*model.Network)
	e := nic(r, "ens1f0")
	if e.RxBps == nil || *e.RxBps < 190e6 || *e.RxBps > 210e6 {
		t.Errorf("rx rate %v", e.RxBps)
	}
	got := map[string]string{}
	for _, f := range r.Findings {
		got[f.Code] = f.Message
	}
	if got["errors_rising"] != "ens1f0: 20 new rx/tx/CRC errors since last refresh" || got["link_flap"] != "ens1f0 link flapped (2 carrier changes since last refresh)" {
		t.Errorf("refresh findings %v", got)
	}
}

func TestLaptop(t *testing.T) {
	n := collect(t, "laptop-amd-nonroot")
	w := nic(n, "wlp194s0")
	if w == nil || w.Kind != "wireless" || w.Vendor == "" || n.Status != model.StatusOK {
		t.Errorf("%+v %+v", w, n.Findings)
	}
}
