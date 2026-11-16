package stress

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaskWithout(t *testing.T) {
	for _, c := range []struct {
		in   string
		cpu  int
		want string
	}{
		{"ffffffff,ffffffff", 7, "ffffffff,ffffff7f"},
		{"ffffffff,ffffffff", 39, "ffffff7f,ffffffff"},
		{"ff", 3, "f7"},
		{"ff", 40, "ff"},
	} {
		if got, ok := maskWithout(c.in, c.cpu); !ok || got != c.want {
			t.Errorf("maskWithout(%s, %d) = %s, want %s", c.in, c.cpu, got, c.want)
		}
	}
}

// fakeSys builds a sysfs/cgroupfs/procfs with cpu7 offline.
func fakeSys(t *testing.T) string {
	dir := t.TempDir()
	sysCPU, cgroupRoot, procIRQ = dir+"/cpu", dir+"/cgroup", dir+"/irq"
	isoState = dir + "/state.json"
	removeCgDir = os.RemoveAll
	t.Cleanup(func() {
		sysCPU, cgroupRoot, procIRQ = "/sys/devices/system/cpu", "/sys/fs/cgroup", "/proc/irq"
		isoState, removeCgDir = "/run/tyr-offline-core.json", os.Remove
	})
	for p, v := range map[string]string{
		"cpu/cpu7/online":               "0",
		"cgroup/cgroup.controllers":     "cpuset cpu io memory pids",
		"cgroup/cgroup.subtree_control": "cpu io memory pids",
		"irq/default_smp_affinity":      "ffffffff,ffffffff",
		"irq/24/smp_affinity_list":      "0-63",
		"irq/25/smp_affinity_list":      "3",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestIsolateRestore(t *testing.T) {
	dir := fakeSys(t)
	read := func(p string) string { return readTrim(filepath.Join(dir, p)) }
	var log []string
	logf := func(f string, a ...any) { log = append(log, f) }
	s, err := isolate(7, logf)
	if err != nil {
		t.Fatal(err)
	}
	if read("cpu/cpu7/online") != "1" || s.Partition != "isolated" || read("cgroup/tyr-offline-core/cpuset.cpus") != "7" ||
		read("irq/24/smp_affinity_list") != "0-6,8-63" || read("irq/25/smp_affinity_list") != "3" ||
		read("irq/default_smp_affinity") != "ffffffff,ffffff7f" || !strings.Contains(read("cgroup/cgroup.subtree_control"), "+cpuset") {
		t.Fatalf("isolated state wrong: %+v", s)
	}
	if _, err := os.Stat(isoState); err != nil {
		t.Fatal("no state file while isolated")
	}
	// a second test refuses while the first is not cleaned up
	if why := isolationCheck(7); !strings.Contains(why, "--restore") {
		t.Errorf("second run: %q", why)
	}
	// Tyr killed: the state file alone restores everything
	did, err := RestoreOfflineCore(logf)
	if !did || err != nil {
		t.Fatalf("restore: %v %v", did, err)
	}
	if read("cpu/cpu7/online") != "0" || read("irq/24/smp_affinity_list") != "0-63" || read("irq/default_smp_affinity") != "ffffffff,ffffffff" ||
		read("cgroup/cgroup.subtree_control") != "-cpuset" {
		t.Errorf("not restored: online=%s irq=%s def=%s", read("cpu/cpu7/online"), read("irq/24/smp_affinity_list"), read("irq/default_smp_affinity"))
	}
	if _, err := os.Stat(filepath.Join(dir, "cgroup/tyr-offline-core")); !os.IsNotExist(err) {
		t.Error("cgroup left behind")
	}
	if _, err := os.Stat(isoState); !os.IsNotExist(err) {
		t.Error("state file left behind")
	}
}

func TestIsolateRefusesWithoutCpuset(t *testing.T) {
	dir := fakeSys(t)
	_ = os.WriteFile(dir+"/cgroup/cgroup.controllers", []byte("cpu io memory\n"), 0o644)
	if _, err := isolate(7, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "cpuset") {
		t.Fatalf("err %v", err)
	}
	if readTrim(dir+"/cpu/cpu7/online") != "0" {
		t.Error("cpu7 was brought online without isolation")
	}
}
