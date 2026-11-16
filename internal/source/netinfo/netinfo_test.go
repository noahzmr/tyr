package netinfo

import (
	"os"
	"testing"

	"github.com/noahzmr/tyr/internal/hostfs"
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

func TestReadIfaces(t *testing.T) {
	ifs := ReadIfaces(hostfs.New(fx + "server-degraded/root"))
	by := map[string]Iface{}
	for _, x := range ifs {
		by[x.Name] = x
	}
	if _, ok := by["lo"]; ok {
		t.Error("lo must be skipped")
	}
	e := by["ens1f1"]
	if e.Kind != "physical" || e.Master != "bond0" || e.Driver != "ice" || *e.Carrier || !e.AdminUp {
		t.Errorf("ens1f1 %+v", e)
	}
	if b := by["bond0"]; b.Kind != "bond" || len(b.Lowers) != 2 {
		t.Errorf("bond0 %+v", b)
	}
	if v := by["bond0.100"]; v.Kind != "vlan" || v.Lowers[0] != "bond0" {
		t.Errorf("vlan %+v", v)
	}
	if d := by["docker0"]; d.Kind != "bridge" {
		t.Errorf("docker0 %+v", d)
	}
	if e := by["eno2"]; e.AdminUp || e.Carrier != nil {
		t.Errorf("eno2 admin down %+v", e)
	}
	if by["ens1f0"].Stats.RxCRC != 1532 || *by["ens1f0"].CarrierChanges != 37 {
		t.Errorf("stats %+v", by["ens1f0"].Stats)
	}
	if VLANIDs(hostfs.New(fx + "server-degraded/root"))["bond0.100"] != 100 {
		t.Error("vlan id")
	}
	// real laptop: wireless + virtual interfaces
	for _, x := range ReadIfaces(hostfs.New(fx + "laptop-amd-nonroot/root")) {
		if x.Name == "wlp194s0" && x.Kind != "wireless" {
			t.Errorf("wlan kind %q", x.Kind)
		}
		if x.Name == "docker0" && x.Kind != "bridge" {
			t.Errorf("docker0 kind %q", x.Kind)
		}
	}
}

func TestBonding(t *testing.T) {
	b := ParseBonding(read(t, "server-degraded/root/proc/net/bonding/bond0"))
	if b.Mode != "IEEE 802.3ad Dynamic link aggregation" || b.MIIStatus != "up" || len(b.Slaves) != 2 {
		t.Fatalf("%+v", b)
	}
	if s := b.Slaves[1]; s.Name != "ens1f1" || s.MIIStatus != "down" || s.LinkFailures != 3 || s.AggregatorID != "1" {
		t.Errorf("%+v", s)
	}
	ab := ParseBonding([]byte("Bonding Mode: fault-tolerance (active-backup)\nPrimary Slave: None\nCurrently Active Slave: eno1\nMII Status: up\n\nSlave Interface: eno1\nMII Status: up\nSpeed: 1000 Mbps\nLink Failure Count: 0\n\nSlave Interface: eno2\nMII Status: up\nLink Failure Count: 1\n"))
	if ab.ActiveSlave != "eno1" || ab.Slaves[1].LinkFailures != 1 {
		t.Errorf("active-backup %+v", ab)
	}
}

func TestEthtool(t *testing.T) {
	di := ParseDrvInfo(read(t, "server-degraded/cmd/ethtool_-i_ens1f0.txt"))
	if di.Driver != "ice" || di.Firmware != "4.40 0x8001c967 1.3534.0" || di.BusInfo != "0000:4b:00.0" {
		t.Errorf("%+v", di)
	}
	li := ParseLinkInfo(read(t, "server-degraded/cmd/ethtool_eno1.txt"))
	if *li.MaxSpeedMbps != 1000 || li.Port != "Twisted Pair" || li.Autoneg != "on" {
		t.Errorf("%+v", li)
	}
	if li := ParseLinkInfo(read(t, "server-degraded/cmd/ethtool_ens1f0.txt")); *li.MaxSpeedMbps != 25000 || li.Port != "FIBRE" {
		t.Errorf("%+v", li)
	}
	st := ParseStats(read(t, "server-degraded/cmd/ethtool_-S_ens1f0.txt"))
	if st["rx_crc_errors"] != 1532 || st["rx_dropped"] != 12 || len(st) != 2 {
		t.Errorf("%v", st)
	}
}

func TestModuleSFP(t *testing.T) {
	m := ParseModule(read(t, "server-degraded/cmd/ethtool_-m_ens1f0.txt"))
	if m == nil || m.Identifier != "SFP" || m.Vendor != "FINISAR CORP." || m.PartNumber != "FTLF8536P4BCL" || m.Type != "25G Ethernet: 25G Base-SR" {
		t.Fatalf("%+v", m)
	}
	by := map[string]model.Sensor{}
	for _, s := range m.Sensors {
		by[s.Name] = s
	}
	rx := by["Rx power"]
	if *rx.Value != -13.9 || *rx.Thresholds.LowerWarn != -13.5 || *rx.Thresholds.LowerCrit != -14.5 || rx.Unit != model.UnitDBm {
		t.Errorf("rx %+v %+v", rx, rx.Thresholds)
	}
	if tmp := by["Module temperature"]; *tmp.Value != 36.12 || *tmp.Thresholds.UpperCrit != 85 {
		t.Errorf("temp %+v", tmp)
	}
	if v := by["Module voltage"]; *v.Value != 3.3053 || *v.Thresholds.LowerWarn != 3.135 {
		t.Errorf("volt %+v", v)
	}
}

func TestModuleQSFP(t *testing.T) {
	in := `	Identifier                                : 0x11 (QSFP28)
	Transceiver type                          : 100G Ethernet: 100G Base-SR4 or 25GBase-SR
	Vendor name                               : Mellanox
	Vendor PN                                 : MMA1B00-C100D
	Module temperature                        : 41.25 degrees C / 106.25 degrees F
	Module voltage                            : 3.2860 V
	Transmit avg optical power (Channel 1)    : 0.8012 mW / -0.96 dBm
	Transmit avg optical power (Channel 2)    : 0.7950 mW / -1.00 dBm
	Rcvr signal avg optical power(Channel 1)  : 0.7350 mW / -1.34 dBm
	Rcvr signal avg optical power(Channel 2)  : 0.0000 mW / -inf dBm
	Laser rx power low alarm threshold        : 0.0490 mW / -13.10 dBm
	Laser rx power low warning threshold      : 0.0980 mW / -10.09 dBm
`
	m := ParseModule([]byte(in))
	if m == nil || m.Identifier != "QSFP28" || len(m.Sensors) != 6 {
		t.Fatalf("%+v", m)
	}
	var lane2 model.Sensor
	for _, s := range m.Sensors {
		if s.Name == "Rx power lane 2" {
			lane2 = s
		}
	}
	if lane2.Value == nil || *lane2.Value != -40 || *lane2.Thresholds.LowerCrit != -13.1 {
		t.Errorf("dark lane %+v", lane2)
	}
	if ParseModule([]byte("Cannot get module EEPROM information: Operation not supported\n")) != nil {
		t.Error("no module must return nil")
	}
}

func TestIPAndLLDPAndLan(t *testing.T) {
	addrs, err := ParseIPAddr(read(t, "server-degraded/cmd/ip_-j_addr_show.txt"))
	if err != nil || addrs["bond0.100"][0] != "10.20.0.21/24" || len(addrs["ens1f0"]) != 0 {
		t.Errorf("%v %v", addrs, err)
	}
	n, err := ParseLLDP(read(t, "server-degraded/cmd/lldpctl_-f_json.txt"))
	if err != nil || n["ens1f0"].System != "leaf-a01.dc1" || n["ens1f0"].Port != "Ethernet12/1" || n["ens1f0"].VLAN != "100" || n["eno1"].Port != "Gi1/0/21" {
		t.Errorf("%+v %v", n, err)
	}
	one, err := ParseLLDP([]byte(`{"lldp":{"interface":{"eth0":{"chassis":{"sw1":{"id":{"value":"x"}}},"port":{"id":{"value":"Gi0/1"}}}}}}`))
	if err != nil || one["eth0"].System != "sw1" || one["eth0"].Port != "Gi0/1" {
		t.Errorf("single %+v %v", one, err)
	}
	if empty, err := ParseLLDP([]byte(`{"lldp":{}}`)); err != nil || len(empty) != 0 {
		t.Errorf("empty %v %v", empty, err)
	}
	lan := ParseIPMILan(read(t, "server-degraded/cmd/ipmitool_lan_print.txt"))
	if lan == nil || lan.IP != "10.10.0.121" || lan.IPSource != "Static Address" || lan.Gateway != "10.10.0.1" || lan.VLAN != "Disabled" {
		t.Errorf("%+v", lan)
	}
}
