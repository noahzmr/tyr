package netinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// TestEthtoolLive compares the native requests with the ethtool tool on
// this host's physical NICs (TYR_LIVE=1; read-only).
func TestEthtoolLive(t *testing.T) {
	if os.Getenv("TYR_LIVE") == "" {
		t.Skip("set TYR_LIVE=1")
	}
	if _, err := exec.LookPath("ethtool"); err != nil {
		t.Skip("ethtool not installed")
	}
	e, err := OpenEthtool()
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	devs, _ := filepath.Glob("/sys/class/net/*/device")
	if len(devs) == 0 {
		t.Skip("no physical NIC")
	}
	run := func(args ...string) []byte {
		out, _ := exec.Command("ethtool", args...).Output()
		return out
	}
	for _, d := range devs {
		name := filepath.Base(filepath.Dir(d))
		if _, err := os.Stat("/sys/class/net/" + name + "/wireless"); err == nil {
			continue
		}
		if li, err := e.LinkInfo(name); err == nil {
			want := ParseLinkInfo(run(name))
			if !reflect.DeepEqual(li.MaxSpeedMbps, want.MaxSpeedMbps) || li.Port != want.Port || li.Autoneg != want.Autoneg {
				t.Errorf("%s link: native %+v (max %v), ethtool %+v (max %v)", name, li, deref(li.MaxSpeedMbps), want, deref(want.MaxSpeedMbps))
			}
			t.Logf("%s link: max %v port %q autoneg %q", name, deref(li.MaxSpeedMbps), li.Port, li.Autoneg)
		} else {
			t.Logf("%s link: %v", name, err)
		}
		if all, err := e.Stats(name); err == nil {
			got, want := FilterStats(all), ParseStats(run("-S", name))
			for k, v := range want { // counters move: the native value is read before ethtool's
				if got[k] > v {
					t.Errorf("%s stat %s: native %d > ethtool %d", name, k, got[k], v)
				}
			}
			t.Logf("%s stats: %d counters, %d error counters", name, len(all), len(got))
		}
		if r, err := e.RingParams(name); err == nil {
			if want, ok := ParseRing(run("-g", name)); ok && r != want {
				t.Errorf("%s ring: native %+v, ethtool %+v", name, r, want)
			}
			t.Logf("%s ring: %+v", name, r)
		}
		if q, err := e.RxQueues(name); err == nil {
			if want, ok := ParseChannels(run("-l", name)); ok && q != want {
				t.Errorf("%s queues: native %d, ethtool %d", name, q, want)
			}
			t.Logf("%s queues: %d", name, q)
		}
	}
}

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
