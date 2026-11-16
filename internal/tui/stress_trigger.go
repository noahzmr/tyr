package tui

import (
	"fmt"
	"slices"
	"strconv"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/stress"
)

// Starting a stress test from any tab: S stresses the selected component,
// F runs the default full test. Both open the usual confirmation on the
// Stress tab; nothing starts without "yes".
var (
	keyStressThis = key.NewBinding(key.WithKeys("S", "shift+s"), key.WithHelp("S", "stress this"))
	keyFullTest   = key.NewBinding(key.WithKeys("F", "shift+f"), key.WithHelp("F", "full test"))
)

// stressReq is what a tab asks the Stress tab to test.
type stressReq struct {
	comp   int    // index into stressComps; 4 = full test
	cpus   string // CPU list for the CPU test ("" = all online)
	stMode int    // storage: 0 read scan, 1 file write
	target string // storage: a disk path or a mountpoint
	engine string // "" = keep the form's engine (offline-core resets to builtin)
	why    string // set: this selection cannot be stressed, say why
}

// stressTargeter is implemented by tabs whose selection maps to a test.
type stressTargeter interface {
	StressTarget() stressReq
}

func compIndex(c stress.Component) int {
	return max(0, slices.Index(stress.Components, c))
}

// startStress fills the Stress form from the request and opens the
// confirmation.
func (m *Model) startStress(this bool) tea.Cmd {
	st := m.stressTab()
	if st == nil {
		return nil
	}
	if st.state != stForm {
		if st.Running() {
			m.flash("a stress test is already running")
		} else {
			m.flash("finish the open stress dialog first")
		}
		m.active = slices.Index(m.tabs, tab(st))
		return nil
	}
	req := stressReq{comp: 4}
	if this {
		switch tb := m.tabs[m.active].(type) {
		case *stressTab:
			req.comp = st.comp // the form as it is
			req.cpus, req.stMode = st.cpus, st.stMode
		case stressTargeter:
			req = tb.StressTarget()
		}
	}
	if req.why != "" {
		m.flash(req.why)
		return nil
	}
	st.comp, st.cpus, st.stMode, st.stTarget, st.field = req.comp, req.cpus, req.stMode, 0, 0
	if c := st.component(); c != "" {
		es := st.engines()
		cur := ""
		if i := st.engine[c]; i < len(es) {
			cur = es[i].Name
		}
		want := req.engine
		if want == "" && cur == stress.EngOfflineCore {
			want = stress.EngBuiltin
		}
		for i, e := range es {
			if e.Name == want {
				st.engine[c] = i
			}
		}
	}
	if req.target != "" {
		i := slices.Index(st.targets(), req.target)
		if i < 0 {
			m.flash(req.target + " is not a stress target")
			return nil
		}
		st.stTarget = i
	}
	m.active = slices.Index(m.tabs, tab(st))
	return st.prepare()
}

// Overview: the focused card or finding's module; the full test otherwise.
func (o *overviewTab) StressTarget() stressReq {
	var mod model.ModuleID
	if o.inFindings {
		if it, ok := o.finds.current(); ok {
			mod = o.findMod[it.key]
		}
	} else if o.sel < len(cardModules) {
		mod = cardModules[o.sel]
	}
	if i := slices.Index(stress.Components, stress.Component(mod)); i >= 0 {
		return stressReq{comp: i}
	}
	return stressReq{comp: 4}
}

// CPU: the selected CPU and its SMT siblings (the physical core).
func (c *cpuTab) StressTarget() stressReq {
	if c.cpu == nil || c.sel >= len(c.cpu.Logical) {
		return stressReq{why: "no CPU data to select a CPU from"}
	}
	l := c.cpu.Logical[c.sel]
	if !l.Online {
		// the guarded offline-core test: its own confirmation names the risk
		return stressReq{comp: compIndex(stress.CompCPU), cpus: strconv.Itoa(l.ID), engine: stress.EngOfflineCore}
	}
	if off, bad := c.cpu.Suspects()[l.ID]; bad {
		return stressReq{why: fmt.Sprintf("%s shares its core with offline cpu%d: select cpu%d and press S to test that core isolated", cpuKey(l.ID), off, off)}
	}
	cpus := l.Siblings
	if cpus == "" {
		cpus = strconv.Itoa(l.ID)
	}
	return stressReq{comp: compIndex(stress.CompCPU), cpus: cpus}
}

func (m *memoryTab) StressTarget() stressReq { return stressReq{comp: compIndex(stress.CompMemory)} }

// Storage: a read scan of the selected disk, a file test on the selected
// filesystem, all disks from the RAID view.
func (s *storageTab) StressTarget() stressReq {
	req := stressReq{comp: compIndex(stress.CompStorage)}
	if s.st == nil {
		return req
	}
	it, ok := s.lists[s.view].current()
	if !ok {
		return req
	}
	switch s.view {
	case 0:
		d, _ := s.disk(it.key)
		req.target = d.Path
	case 2:
		for _, f := range s.st.Filesystems {
			if f.Mountpoint == it.key && f.ReadOnly {
				return stressReq{why: f.Mountpoint + " is read-only: the file test needs a writable filesystem"}
			}
		}
		req.stMode, req.target = 1, it.key
	}
	return req
}

// Network: the test runs against a peer, not a selected port.
func (n *networkTab) StressTarget() stressReq { return stressReq{comp: compIndex(stress.CompNetwork)} }

func (a *accelTab) StressTarget() stressReq {
	return stressReq{why: "no GPU/NPU stress engine yet: F runs the full test of CPU, memory, storage and network"}
}
