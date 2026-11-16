package stress

import (
	"strings"
	"testing"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/execx"
)

func replayEnv(root bool, tools ...string) *Env {
	r := &execx.Replay{Tools: map[string]string{}}
	for _, t := range tools {
		r.Tools[t] = "/usr/bin/" + t
	}
	return &Env{Collector: &collector.Env{Exec: r, Priv: collector.Privileges{Root: root}}}
}

func TestPlanInstall(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     *Env
		engines []string
		cmd     string
		blocked string
	}{
		{"apt with sudo", replayEnv(false, "apt-get", "sudo"), []string{"stress-ng", "memtester"}, "sudo apt-get install stress-ng memtester", ""},
		{"dnf as root", replayEnv(true, "dnf", "yum"), []string{"iperf3"}, "dnf install iperf3", ""},
		{"pacman", replayEnv(true, "pacman"), []string{"stress-ng"}, "pacman -S --needed stress-ng", ""},
		{"no sudo", replayEnv(false, "apt-get"), []string{"stress-ng"}, "apt-get install stress-ng", "not root and sudo is not installed"},
		{"no manager", replayEnv(true), []string{"stress-ng"}, "", "no supported package manager"},
		{"mprime only", replayEnv(true, "apt-get"), []string{"mprime"}, "", "nothing Tyr can install"},
	} {
		p := PlanInstall(tc.env, tc.engines)
		if p.Command() != tc.cmd || !strings.Contains(p.Blocked, tc.blocked) || (tc.blocked == "") != (p.Blocked == "") {
			t.Errorf("%s: command %q blocked %q", tc.name, p.Command(), p.Blocked)
		}
	}
	p := PlanInstall(replayEnv(true, "apt-get"), []string{"stress-ng", "mprime"})
	if len(p.Skipped) != 1 || !strings.Contains(p.Skipped[0], "mersenne.org") || p.Command() != "apt-get install stress-ng" {
		t.Errorf("mixed: %+v", p)
	}
	sim := replayEnv(true, "apt-get")
	sim.Collector.Simulated = true
	if p := PlanInstall(sim, []string{"stress-ng"}); !strings.Contains(p.Blocked, "fixtures mode") {
		t.Errorf("fixtures: %+v", p)
	}
	if got := MissingEngines(replayEnv(true, "stress-ng"), CompCPU, CompMemory); strings.Join(got, ",") != "mprime,memtester" {
		t.Errorf("missing engines %v", got)
	}
}
