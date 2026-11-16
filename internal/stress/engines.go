package stress

import (
	"fmt"
	"os"
	"slices"
)

// EngineInfo describes one engine for a component.
type EngineInfo struct {
	Name      string
	Desc      string
	Available bool
	Reason    string // why not available
}

// engineDescs lists the engines per component, preferred first.
var engineDescs = map[Component][]EngineInfo{
	CompCPU: {
		{Name: EngBuiltin, Desc: "Tyr kernels on every core, each result checked against a golden value"},
		{Name: EngStressNG, Desc: "stress-ng --cpu, all methods, --verify"},
		{Name: EngMprime, Desc: "Prime95 torture test (mprime), self-checking FFTs"},
		{Name: EngOfflineCore, Desc: "an offline (suspect) core: online only for the test, isolated, offline again after"},
	},
	CompMemory: {
		{Name: EngBuiltin, Desc: "Tyr patterns: walking bits, random, moving inversions, address-in-address"},
		{Name: EngStressNG, Desc: "stress-ng --vm, all methods, --verify"},
		{Name: EngMemtester, Desc: "memtester pattern suite"},
	},
	CompStorage: {
		{Name: EngBuiltin, Desc: "read: full-surface O_DIRECT read scan; file: write + CRC verify on a filesystem"},
		{Name: EngStressNG, Desc: "stress-ng --hdd on a filesystem, --verify"},
	},
	CompNetwork: {
		{Name: EngBuiltin, Desc: "TCP streams to a `tyr net-peer`, every block CRC-checked"},
		{Name: EngIperf3, Desc: "iperf3 client to an iperf3 server"},
		{Name: EngStressNG, Desc: "stress-ng --sock/--udp: kernel network stack only (loopback, does not touch the NIC)"},
	},
}

// Engines lists the engines of a component and whether they can run here.
func Engines(c Component, env *Env) []EngineInfo {
	var out []EngineInfo
	for _, e := range engineDescs[c] {
		e.Available = true
		switch {
		case e.Name == EngOfflineCore:
			if env == nil || len(offlineCPUs(env)) == 0 {
				e.Available, e.Reason = false, "no CPU is offline"
			} else if !env.Simulated() && os.Geteuid() != 0 {
				e.Available, e.Reason = false, "needs root"
			}
		case env != nil && env.Simulated():
			if e.Name != EngBuiltin {
				e.Available, e.Reason = false, "fixtures mode: external engines are not run"
			}
		case e.Name != EngBuiltin:
			if _, err := env.Collector.Exec.LookPath(e.Name); err != nil {
				e.Available, e.Reason = false, e.Name+" is not installed"
			}
		}
		out = append(out, e)
	}
	return out
}

// testFor returns the implementation for a config.
func testFor(cfg Config, env *Env) (Test, error) {
	if !slices.Contains(Components, cfg.Component) {
		return nil, fmt.Errorf("unknown component %q (cpu, memory, storage, network)", cfg.Component)
	}
	if cfg.Engine == "" {
		cfg.Engine = EngBuiltin
	}
	var info *EngineInfo
	for _, e := range Engines(cfg.Component, env) {
		if e.Name == cfg.Engine {
			info = &e
		}
	}
	if info == nil {
		return nil, fmt.Errorf("engine %q does not exist for %s", cfg.Engine, cfg.Component)
	}
	if !info.Available {
		return nil, fmt.Errorf("engine %s: %s", cfg.Engine, info.Reason)
	}
	if env.Simulated() {
		return simTest{}, nil
	}
	if cfg.Engine == EngOfflineCore {
		return offlineCore{}, nil
	}
	switch cfg.Component {
	case CompCPU:
		switch cfg.Engine {
		case EngStressNG:
			return cpuStressNG{}, nil
		case EngMprime:
			return cpuMprime{}, nil
		}
		return cpuBuiltin{}, nil
	case CompMemory:
		switch cfg.Engine {
		case EngStressNG:
			return memStressNG{}, nil
		case EngMemtester:
			return memMemtester{}, nil
		}
		return memBuiltin{}, nil
	case CompStorage:
		if cfg.Engine == EngStressNG {
			return storageStressNG{}, nil
		}
		return storageBuiltin{}, nil
	case CompNetwork:
		switch cfg.Engine {
		case EngIperf3:
			return netIperf3{}, nil
		case EngStressNG:
			return netStressNG{}, nil
		}
		return netBuiltin{}, nil
	}
	return nil, fmt.Errorf("no test for %s", cfg.Component)
}
