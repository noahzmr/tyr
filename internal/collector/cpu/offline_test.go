package cpu

import (
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/model"
)

func TestAMDDecoderRecords(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	p := &mceParser{amd: true, source: "kernel log"}
	for _, l := range []string{
		"mce: [Hardware Error]: Machine check events logged",
		"[Hardware Error]: Corrected error, no action required.",
		"[Hardware Error]: CPU:12 (19:1:1) MC1_STATUS[-|CE|MiscV|AddrV|-|-|SyndV|CECC|-|-|-]: 0x9c2040000000011b",
		"[Hardware Error]: Instruction Fetch Unit Ext. Error Code: 1, Instruction Cache data parity error.",
		"[Hardware Error]: CPU:3 (19:1:1) MC16_STATUS[Over|UE|MiscV|AddrV|-|-|SyndV|UECC|-|-|-]: 0xfc00000000000000",
		"[Hardware Error]: Unified Memory Controller Ext. Error Code: 0, DRAM ECC error.",
	} {
		p.line(at, l)
	}
	if len(p.recs) != 2 {
		t.Fatalf("records %+v", p.recs)
	}
	if r := p.recs[0]; r.CPU != 12 || r.Bank != 1 || r.Unit != "Instruction Fetch Unit" || r.Scope != "core" || r.Severity != model.StatusWarn {
		t.Errorf("IF record %+v", r)
	}
	if r := p.recs[1]; r.CPU != 3 || r.Unit != "Unified Memory Controller" || r.Scope != "shared" || r.Severity != model.StatusCrit {
		t.Errorf("UMC record %+v", r)
	}
}

func TestSiblingInference(t *testing.T) {
	mk := func(sib map[int]string, offline ...int) []model.LogicalCPU {
		var out []model.LogicalCPU
		for id := 0; id < 8; id++ {
			l := model.LogicalCPU{ID: id, Present: true, Online: true, Siblings: sib[id], CoreID: -1, SocketID: -1}
			for _, o := range offline {
				if o == id {
					l.Online, l.Siblings = false, ""
				}
			}
			out = append(out, l)
		}
		return out
	}
	// "first threads, then second threads" (offset 4): 1 and 5 share a core
	ls := mk(map[int]string{0: "0,4", 4: "0,4", 2: "2,6", 6: "2,6", 3: "3,7", 7: "3,7", 5: "5"}, 1)
	if off := siblingOffset(ls); off != 4 || smtSiblingOf(1, off) != 5 || smtSiblingOf(5, off) != 1 {
		t.Errorf("offset %d", off)
	}
	// adjacent numbering (offset 1): 2,3 share a core; both offline
	ls = mk(map[int]string{0: "0-1", 1: "0-1", 4: "4-5", 5: "4-5", 6: "6-7", 7: "6-7"}, 2, 3)
	if off := siblingOffset(ls); off != 1 || smtSiblingOf(2, off) != 3 || smtSiblingOf(3, off) != 2 {
		t.Errorf("adjacent offset %d", off)
	}
	c := &model.CPU{Logical: []model.LogicalCPU{{ID: 1, Present: true, Offline: &model.OfflineInfo{Sibling: ptr(5), SiblingOnline: true}},
		{ID: 2, Present: true, Offline: &model.OfflineInfo{Sibling: ptr(3)}}}}
	if s := c.Suspects(); len(s) != 1 || s[5] != 1 {
		t.Errorf("suspects %v", s)
	}
}

func ptr(i int) *int { return &i }

func TestRasdaemonLine(t *testing.T) {
	m := rasLine.FindStringSubmatch("1 2026-10-02 08:11:42 +0200 error: Corrected error, no action required., CPU 7, bank Load Store Unit (bank=0), mcg mcgstatus=0, status '0x9c20400000000017'")
	if m == nil {
		t.Fatal("no match")
	}
	if c := rasCPU.FindStringSubmatch(m[2]); c == nil || c[1] != "7" {
		t.Errorf("cpu %v", c)
	}
	if u := rasUnit.FindStringSubmatch(m[2]); u == nil || u[1] != "Load Store Unit" || amdScope(u[1]) != "core" {
		t.Errorf("unit %v", u)
	}
}
