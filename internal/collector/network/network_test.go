package network

import (
	"context"
	"encoding/json"
	"strings"
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
	if n.Status != model.StatusOK || n.Summary != "3/4 ports up, bond0 OK, wg0 WireGuard 2/2 peers" || n.Hidden != 1 {
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
	if n.Status != model.StatusCrit || n.Summary != "2/4 ports up, bond0 CRIT, wg0 WireGuard 0/2 peers" {
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

func TestVPN(t *testing.T) {
	n := collect(t, "server-healthy")
	w := nic(n, "wg0")
	if w == nil || w.Kind != "vpn" || w.VPN == nil || w.VPN.Type != "WireGuard" || w.Status != model.StatusOK {
		t.Fatalf("wg0 %+v", w)
	}
	v := w.VPN
	if *v.ListenPort != 51820 || len(v.Peers) != 2 || v.Peers[0].Endpoint != "198.51.100.20:51820" || len(v.Peers[0].AllowedIPs) != 2 ||
		v.Peers[0].KeepaliveS != 25 || v.Peers[1].Endpoint != "" || v.Peers[0].RxBytes != 1234567890 {
		t.Errorf("peers %+v", v.Peers)
	}
	if len(w.Addresses) != 1 || w.Addresses[0] != "10.99.0.1/24" {
		t.Errorf("addresses %v", w.Addresses)
	}
	// journal: wg-quick and systemd's lines for wg0; NetworkManager's line about a veth is not
	if len(v.Journal) != 5 || v.Journal[0].Service != "systemd" || !strings.Contains(v.Journal[1].Message, "ip link add wg0") {
		t.Errorf("journal %+v", v.Journal)
	}
	if len(v.KernelLines) != 1 || !strings.Contains(v.KernelLines[0], "WireGuard 1.0.0 loaded") {
		t.Errorf("kernel %v", v.KernelLines)
	}
	// no key but the public ones ever reaches the snapshot
	b, _ := json.Marshal(n)
	if strings.Contains(string(b), "FIXTUREPRIVATEKEY") || strings.Contains(string(b), "FIXTUREPRESHAREDKEY") {
		t.Error("a private or preshared key is in the snapshot")
	}

	d := nic(collect(t, "server-degraded"), "wg0")
	if d == nil || d.Status != model.StatusWarn || d.VPN.Peers[0].Status != model.StatusWarn || d.VPN.Peers[1].Status != model.StatusWarn {
		t.Fatalf("degraded wg0 %+v", d)
	}
	if len(d.VPN.KernelLines) != 2 || !strings.Contains(d.VPN.KernelLines[1], "did not complete") {
		t.Errorf("degraded kernel %v", d.VPN.KernelLines)
	}
}

func TestVPNFindings(t *testing.T) {
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	up, down := true, false
	cases := []struct {
		name  string
		nic   model.NIC
		codes string
	}{
		{"tunnel without its daemon", model.NIC{Name: "tun0", Kind: "vpn", AdminUp: true, Carrier: &down, Addresses: []string{"10.8.0.2/24"},
			VPN: &model.VPN{Type: "OpenVPN/TUN"}}, "vpn_down"},
		{"admin down is not a finding", model.NIC{Name: "tun0", Kind: "vpn", AdminUp: false, Carrier: &down, Addresses: []string{"10.8.0.2/24"},
			VPN: &model.VPN{Type: "OpenVPN/TUN"}}, ""},
		{"idle peer without keepalive", model.NIC{Name: "wg0", Kind: "vpn", AdminUp: true, Carrier: &up,
			VPN: &model.VPN{Type: "WireGuard", Peers: []model.VPNPeer{{PublicKey: "k", LatestHandshake: now.Add(-5 * time.Hour)}}}}, ""},
		{"peer that dials in, not yet", model.NIC{Name: "wg0", Kind: "vpn", AdminUp: true, Carrier: &up,
			VPN: &model.VPN{Type: "WireGuard", Peers: []model.VPNPeer{{PublicKey: "k"}}}}, ""},
		{"stale with keepalive", model.NIC{Name: "wg0", Kind: "vpn", AdminUp: true, Carrier: &up,
			VPN: &model.VPN{Type: "WireGuard", Peers: []model.VPNPeer{{PublicKey: "k", Endpoint: "e:1", KeepaliveS: 25, LatestHandshake: now.Add(-4 * time.Minute)}}}}, "vpn_peer"},
		{"fresh with keepalive", model.NIC{Name: "wg0", Kind: "vpn", AdminUp: true, Carrier: &up,
			VPN: &model.VPN{Type: "WireGuard", Peers: []model.VPNPeer{{PublicKey: "k", Endpoint: "e:1", KeepaliveS: 25, LatestHandshake: now.Add(-90 * time.Second)}}}}, ""},
		{"service warning only", model.NIC{Name: "wg0", Kind: "vpn", AdminUp: true, Carrier: &up,
			VPN: &model.VPN{Type: "WireGuard", Journal: []model.VPNLogLine{{Priority: 4, Message: "w"}}}}, ""},
		{"service error", model.NIC{Name: "wg0", Kind: "vpn", AdminUp: true, Carrier: &up,
			VPN: &model.VPN{Type: "WireGuard", Journal: []model.VPNLogLine{{Priority: 3, Message: "e"}}}}, "vpn_service"},
	}
	for _, c := range cases {
		var h model.Header
		vpnFindings(&h, &c.nic, now)
		var codes []string
		for _, f := range h.Findings {
			codes = append(codes, f.Code)
		}
		if got := strings.Join(codes, ","); got != c.codes {
			t.Errorf("%s: %q, want %q", c.name, got, c.codes)
		}
	}
}
