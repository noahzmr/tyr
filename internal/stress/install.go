package stress

import (
	"fmt"
	"slices"
	"strings"
)

// Installable engines: tools every supported distribution packages under
// the engine's own name. mprime and the GPU tools have no distro package
// and are never installed by Tyr; memtest86+ is packaged, but its install
// hooks rewrite the boot loader config, which Tyr never touches.
var installable = []string{EngStressNG, EngMemtester, EngIperf3}

// packageManagers in order of preference: binary and install arguments.
var packageManagers = []struct {
	bin  string
	args []string
}{
	{"apt-get", []string{"install"}},
	{"dnf", []string{"install"}},
	{"yum", []string{"install"}},
	{"zypper", []string{"install"}},
	{"pacman", []string{"-S", "--needed"}},
}

// InstallPlan is how missing engines would be installed. Argv runs in the
// user's terminal (the package manager and sudo may prompt). Blocked says
// why nothing can be installed.
type InstallPlan struct {
	Packages []string
	Manager  string
	Argv     []string
	Sudo     bool
	Blocked  string
	Skipped  []string // missing engines Tyr does not install, with the reason
}

// Command is Argv as one shell-style line for display.
func (p InstallPlan) Command() string { return strings.Join(p.Argv, " ") }

// MissingEngines lists the unavailable engines of the components, without
// duplicates, in component order.
func MissingEngines(env *Env, comps ...Component) []string {
	var out []string
	for _, c := range comps {
		for _, e := range Engines(c, env) {
			if !e.Available && e.Name != EngOfflineCore && !slices.Contains(out, e.Name) { // not a package
				out = append(out, e.Name)
			}
		}
	}
	return out
}

// PlanInstall works out the command that installs the given engines with
// the host's package manager, through sudo when not root.
func PlanInstall(env *Env, engines []string) InstallPlan {
	var p InstallPlan
	for _, e := range engines {
		switch {
		case slices.Contains(installable, e):
			p.Packages = append(p.Packages, e)
		case e == EngMprime:
			p.Skipped = append(p.Skipped, "mprime has no distribution package: download it from mersenne.org")
		default:
			p.Skipped = append(p.Skipped, e+" is not installed by Tyr")
		}
	}
	switch {
	case env == nil || env.Collector == nil:
		p.Blocked = "no host environment"
		return p
	case env.Simulated():
		p.Blocked = "fixtures mode: nothing is installed"
		return p
	case len(p.Packages) == 0:
		p.Blocked = "nothing Tyr can install"
		return p
	}
	lp := env.Collector.Exec.LookPath
	for _, m := range packageManagers {
		if _, err := lp(m.bin); err == nil {
			p.Manager = m.bin
			p.Argv = append(append([]string{m.bin}, m.args...), p.Packages...)
			break
		}
	}
	if p.Manager == "" {
		p.Blocked = "no supported package manager found (apt-get, dnf, yum, zypper, pacman)"
		return p
	}
	if !env.Collector.Priv.Root {
		if _, err := lp("sudo"); err != nil {
			p.Blocked = fmt.Sprintf("not root and sudo is not installed: run %s as root", p.Command())
			return p
		}
		p.Sudo = true
		p.Argv = append([]string{"sudo"}, p.Argv...)
	}
	return p
}
