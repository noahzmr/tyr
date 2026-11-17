package cpu

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/cpuset"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/kernellog"
)

// Offline CPUs: which physical core they belong to, whether its other
// thread still runs, when and how they went offline, and whether that
// survives a reboot. Nothing here changes the system.

var (
	offlineMsg = regexp.MustCompile(`(?:smpboot: CPU (\d+) is now offline|psci: CPU(\d+) killed|CPU(\d+): shutdown)`)
	onlineMsg  = regexp.MustCompile(`(?:smpboot: Booting Node \d+ Processor (\d+)|CPU(\d+) is up)`)
	bootLimit  = regexp.MustCompile(`\b(maxcpus|nr_cpus|possible_cpus)=\d+`)
)

// siblingOffset is the distance between the two threads of a core, from
// the complete cores ("6,38" → 32, "0-1" → 1); 0 if unknown.
func siblingOffset(logical []model.LogicalCPU) int {
	count := map[int]int{}
	for _, l := range logical {
		if !l.Online || l.Siblings == "" {
			continue
		}
		set, err := cpuset.Parse(l.Siblings)
		if err != nil || len(set) != 2 {
			continue
		}
		count[set[1]-set[0]]++
	}
	best, n := 0, 0
	for off, c := range count {
		if c > n || (c == n && off < best) {
			best, n = off, c
		}
	}
	return best
}

func smtSiblingOf(id, off int) int {
	if id%(2*off) < off {
		return id + off
	}
	return id - off
}

func (c *Collector) offlineInfo(env *collector.Env, out *model.CPU, klog *kernellog.Log) {
	byID := map[int]*model.LogicalCPU{}
	for i := range out.Logical {
		byID[out.Logical[i].ID] = &out.Logical[i]
	}
	var offline []*model.LogicalCPU
	for i := range out.Logical {
		if l := &out.Logical[i]; l.Present && !l.Online {
			offline = append(offline, l)
		}
	}
	if len(offline) == 0 {
		return
	}
	since, ups := map[int]time.Time{}, map[int]time.Time{}
	if klog != nil {
		for _, e := range klog.Recent {
			for _, pair := range []struct {
				re *regexp.Regexp
				to map[int]time.Time
			}{{offlineMsg, since}, {onlineMsg, ups}} {
				if m := pair.re.FindStringSubmatch(e.Message); m != nil {
					for _, g := range m[1:] {
						if id, err := strconv.Atoi(g); err == nil {
							pair.to[id] = e.Time
						}
					}
				}
			}
		}
	}
	cmdline := hostfs.StringOr(env.FS, "/proc/cmdline", "")
	smtOff := out.SMTControl == "off" || out.SMTControl == "forceoff" || out.SMTControl == "notsupported"
	off := siblingOffset(out.Logical)
	for _, l := range offline {
		o := &model.OfflineInfo{}
		l.Offline = o
		if t, ok := since[l.ID]; ok && !ups[l.ID].After(t) {
			o.Since, o.How = t, "runtime"
		} else if bootLimit.MatchString(cmdline) {
			o.How = "boot"
		}
		if !o.Since.IsZero() {
			for _, r := range out.MCEHistory {
				if r.CPU == l.ID && !r.Time.After(o.Since) && o.Since.Sub(r.Time) < 30*time.Minute {
					o.AfterMCE = true
				}
			}
		}
		o.Persistent = persistentOffline(env, l.ID)
		if smtOff {
			continue
		}
		// sysfs may still list the offline thread in its sibling's list
		sib := -1
		for _, x := range out.Logical {
			if x.Online && x.ID != l.ID && x.Siblings != "" {
				if set, err := cpuset.Parse(x.Siblings); err == nil && set.Contains(l.ID) {
					sib = x.ID
				}
			}
		}
		if sib < 0 && off > 0 {
			if cand := smtSiblingOf(l.ID, off); byID[cand] != nil {
				x := byID[cand]
				// an online candidate must be alone in its core
				if !x.Online || x.Siblings == "" || x.Siblings == strconv.Itoa(x.ID) {
					sib = cand
					o.SiblingInferred = true
				}
			}
		}
		if l.SocketID < 0 {
			l.SocketID = socketOfNode(out.Logical, l.NUMANode)
		}
		if sib < 0 {
			continue
		}
		o.Sibling = &sib
		if x := byID[sib]; x != nil && x.Online {
			o.SiblingOnline = true
			if l.CoreID < 0 {
				l.CoreID, l.SocketID, l.NUMANode = x.CoreID, x.SocketID, x.NUMANode
			}
			o.L3SharedWith = l3Shared(env.FS, sib)
		}
	}
}

// socketOfNode is the socket of a NUMA node when all its online CPUs sit
// on one socket (the usual one node per socket); -1 otherwise.
func socketOfNode(logical []model.LogicalCPU, node int) int {
	if node < 0 {
		return -1
	}
	sock := -1
	for _, x := range logical {
		if !x.Online || x.NUMANode != node || x.SocketID < 0 {
			continue
		}
		if sock >= 0 && x.SocketID != sock {
			return -1
		}
		sock = x.SocketID
	}
	return sock
}

// l3Shared is the shared_cpu_list of a CPU's level-3 cache.
func l3Shared(f hostfs.FS, cpu int) string {
	dirs, _ := f.Glob(fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cache/index*", cpu))
	for _, d := range dirs {
		if strings.TrimSpace(hostfs.StringOr(f, d+"/level", "")) == "3" {
			return strings.TrimSpace(hostfs.StringOr(f, d+"/shared_cpu_list", ""))
		}
	}
	return ""
}

// persistentOffline finds a boot-time config that offlines the CPU again
// (systemd unit, tmpfiles, rc.local, udev rule); "" if there is none.
func persistentOffline(env *collector.Env, id int) string {
	re := regexp.MustCompile(fmt.Sprintf(`cpu%d/online\b`, id))
	for _, g := range []string{"/etc/systemd/system/*.service", "/etc/tmpfiles.d/*.conf", "/etc/rc.local", "/etc/udev/rules.d/*.rules"} {
		files, _ := env.FS.Glob(g)
		for _, f := range files {
			if b, err := env.FS.ReadFile(f); err == nil && re.Match(b) {
				return f
			}
		}
	}
	return ""
}

// offlineFindings: a suspect core whose other thread still runs work.
func offlineFindings(h *model.Header, out *model.CPU) {
	for i := range out.Logical {
		l := &out.Logical[i]
		o := l.Offline
		if o == nil || o.Sibling == nil || !o.SiblingOnline {
			continue
		}
		h.Find(model.StatusCrit, "core_half_offline", "cpu"+strconv.Itoa(l.ID), "sysfs",
			fmt.Sprintf("cpu%d is offline, but cpu%d - the other thread of the same physical core - still runs work: the core is not isolated", l.ID, *o.Sibling))
	}
}

// offlineNote is the short "how" for the core_offline message.
func offlineNote(out *model.CPU) string {
	runtime, kept := 0, 0
	for _, l := range out.Logical {
		if o := l.Offline; o != nil {
			if o.How == "runtime" {
				runtime++
			}
			if o.Persistent != "" {
				kept++
			}
		}
	}
	switch {
	case runtime > 0 && kept == 0:
		return "; taken offline at runtime - back online after a reboot"
	case runtime > 0 && kept < runtime:
		return "; some come back online after a reboot"
	}
	return ""
}
