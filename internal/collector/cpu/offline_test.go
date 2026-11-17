package cpu

import (
	"fmt"
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

// both threads offline: the socket comes from the NUMA node
func TestSocketOfNode(t *testing.T) {
	ls := []model.LogicalCPU{{ID: 0, Online: true, NUMANode: 0, SocketID: 0}, {ID: 1, Online: true, NUMANode: 1, SocketID: 1},
		{ID: 22, NUMANode: 1, SocketID: -1}, {ID: 5, Online: true, NUMANode: 2, SocketID: 0}, {ID: 6, Online: true, NUMANode: 2, SocketID: 1}}
	if s := socketOfNode(ls, 1); s != 1 {
		t.Errorf("node 1: socket %d", s)
	}
	if s := socketOfNode(ls, 2); s != -1 { // a node across sockets says nothing
		t.Errorf("node 2: socket %d", s)
	}
}

func TestParseFault(t *testing.T) {
	at := time.Date(2026, 11, 13, 18, 7, 45, 0, time.UTC)
	f, ok := parseFault(at, "kernel log", "unpigz[203267]: segfault at 751f649982f8 ip 0000000000409f71 sp 0000714cf9da5970 error 6 in pigz[401000+1f000] likely on CPU 46 (core 21, socket 1)")
	if !ok || f.CPU != 46 || f.Core != 21 || f.Socket != 1 || f.Kind != "segfault" || f.Process != "unpigz" {
		t.Errorf("segfault %+v", f)
	}
	f, ok = parseFault(at, "kernel log", "traps: pause[212918] trap invalid opcode ip:41a493 sp:7ffc03a833f8 error:0 in pause[401000+86000] likely on CPU 13 (core 3, socket 1)")
	if !ok || f.Kind != "invalid opcode" || f.Process != "pause" || f.CPU != 13 {
		t.Errorf("trap %+v", f)
	}
	if _, ok := parseFault(at, "kernel log", "sh[226979]: segfault at 7ffe7e7ced20 ip 00007ffe7e7ced20 error 15"); ok {
		t.Error("a line without a CPU parsed")
	}
	// a spread of crashes is not a cluster
	c := &model.CPU{}
	for i, cpu := range []int{1, 2, 3, 4, 5} {
		c.ProcessFaults = append(c.ProcessFaults, model.ProcessFault{CPU: cpu, Core: cpu, Process: fmt.Sprint("p", i), Kind: "segfault"})
	}
	var h model.Header
	faultFindings(&h, c)
	if len(h.Findings) != 0 {
		t.Errorf("spread crashes flagged: %+v", h.Findings)
	}
}
