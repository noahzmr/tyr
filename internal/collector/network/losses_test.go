package network

import (
	"strings"
	"testing"

	"github.com/noahzmr/tyr/internal/model"
)

// The server's case: bond cpe (active-backup, eno5np0 active, eno6np1
// backup) and vlan_customer on it. The NIC loses packets (rx_missed); the
// bond repeats the members' counters; the backup drops by design.
func TestLossDiagnosis(t *testing.T) {
	ring, max, q := 512, 4096, 24
	mk := func(missed, dropB, dropV, pkts uint64) *model.Network {
		return &model.Network{Interfaces: []model.NIC{
			{Name: "cpe", Kind: "bond", Members: []string{"eno5np0", "eno6np1"}, Bond: &model.Bond{Mode: "fault-tolerance (active-backup)", ActiveSlave: "eno5np0"},
				Counters: model.NICCounters{RxPackets: pkts, RxMissed: missed, RxDropped: dropB}},
			{Name: "eno5np0", Kind: "physical", Master: "cpe", RingRX: &ring, RingRXMax: &max, RxQueues: &q, Counters: model.NICCounters{RxPackets: pkts, RxMissed: missed}},
			{Name: "eno6np1", Kind: "physical", Master: "cpe", Counters: model.NICCounters{RxPackets: dropB, RxDropped: dropB}},
			{Name: "vlan_customer", Kind: "vlan", Counters: model.NICCounters{RxPackets: pkts / 2, RxDropped: dropV}},
		}}
	}
	prev := mk(201319, 1166699, 58080, 10_000_000)
	cur := mk(201527, 1168072, 58148, 11_000_000)
	by := map[string]*model.NIC{}
	for i := range prev.Interfaces {
		by[prev.Interfaces[i].Name] = &prev.Interfaces[i]
	}
	var h model.Header
	losses(&h, cur, prev, by, 120)
	get := func(name string) model.NICLoss {
		for _, l := range cur.Interface(name).Losses {
			return l
		}
		t.Fatalf("%s: no loss", name)
		return model.NICLoss{}
	}
	if l := get("eno5np0"); l.Counter != "rx_missed" || l.Delta != 208 || l.Status != model.StatusWarn || l.Where != "in the NIC" || !strings.Contains(l.Cause, "(512 of 4096 entries, 24 queues)") {
		t.Errorf("eno5np0 %+v", l)
	}
	for _, l := range cur.Interface("cpe").Losses {
		if l.Status != model.StatusOK || l.Where != "on the members" {
			t.Errorf("cpe %+v", l)
		}
	}
	if l := get("eno6np1"); l.Status != model.StatusOK || !strings.Contains(l.Where, "bond backup") {
		t.Errorf("eno6np1 %+v", l)
	}
	if l := get("vlan_customer"); l.Status != model.StatusOK || l.Delta != 68 || l.Where != "in the kernel" {
		t.Errorf("vlan_customer %+v", l)
	}
	warn := 0
	for _, f := range h.Findings {
		if f.Status == model.StatusWarn {
			warn++
			if f.Subject != "eno5np0" || !strings.Contains(f.Message, "208 packets lost in the NIC") {
				t.Errorf("finding %+v", f)
			}
		}
	}
	if warn != 1 {
		t.Errorf("%d warnings: %+v", warn, h.Findings)
	}
}
