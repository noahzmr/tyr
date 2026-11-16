package stress

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/hostfs"
)

type vmRaw struct {
	pswpout, pswpin, oomKill uint64
	ok                       bool
	swapRate                 float64 // background swap-out pages/s before the test
	swapInRate               float64 // background swap-in pages/s before the test
	at                       time.Time
}

func readVM(env *Env) vmRaw {
	b, err := env.Collector.FS.ReadFile("/proc/vmstat")
	if err != nil {
		return vmRaw{}
	}
	var v vmRaw
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		n, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "pswpout":
			v.pswpout = n
		case "pswpin":
			v.pswpin = n
		case "oom_kill":
			v.oomKill = n
		}
	}
	v.ok = true
	return v
}

// memReserve is the headroom the memory test leaves: max(1 GiB, 5% of RAM).
func memReserve(total uint64) uint64 { return max(1<<30, total/20) }

func vmDelta(env *Env, base vmRaw, cfg Config) VMStat {
	cur := readVM(env)
	out := VMStat{Valid: base.ok && cur.ok}
	if out.Valid {
		out.SwapOutPages = int64(cur.pswpout) - int64(base.pswpout)
		if !base.at.IsZero() {
			out.SwapOutPages -= int64(base.swapRate * time.Since(base.at).Seconds() * 1.5)
		}
		out.SwapInPages = int64(cur.pswpin) - int64(base.pswpin)
		if !base.at.IsZero() {
			out.SwapInPages -= int64(base.swapInRate * time.Since(base.at).Seconds() * 1.5)
		}
		out.OOMKills = int64(cur.oomKill) - int64(base.oomKill)
	}
	if mi, err := env.Collector.FS.ReadFile("/proc/meminfo"); err == nil {
		var total uint64
		for _, l := range strings.Split(string(mi), "\n") {
			f := strings.Fields(l)
			if len(f) < 2 {
				continue
			}
			n, _ := strconv.ParseUint(f[1], 10, 64)
			switch f[0] {
			case "MemAvailable:":
				out.MemAvailable = n << 10
			case "MemTotal:":
				total = n << 10
			}
		}
		if cfg.Component == CompMemory {
			out.ReserveBytes = memReserve(total) / 2
			out.TestSwapBytes = selfSwap(env)
		}
	}
	if p, err := hostfs.String(env.Collector.FS, "/proc/pressure/memory"); err == nil {
		for _, line := range strings.Split(p, "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			if v, ok := strings.CutPrefix(f[1], "avg10="); ok {
				x, _ := strconv.ParseFloat(v, 64)
				switch f[0] {
				case "some":
					out.PSISomeAvg10, out.HavePSI = x, true
				case "full":
					out.PSIFullAvg10 = x
				}
			}
		}
	}
	return out
}

// selfSwap is how much of Tyr's own memory (the built-in memory test's
// buffer) is in swap: VmSwap of /proc/self/status.
func selfSwap(env *Env) uint64 {
	if env.Simulated() {
		return 0
	}
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "VmSwap:"); ok {
			f := strings.Fields(v)
			if len(f) > 0 {
				n, _ := strconv.ParseUint(f[0], 10, 64)
				return n << 10
			}
		}
	}
	return 0
}

// diskRates turns /proc/diskstats sector counters into bytes/s.
type diskRates struct {
	env      *Env
	disks    map[string]bool
	rd, wr   uint64
	at       time.Time
	haveLast bool
}

func newDiskRates(env *Env) *diskRates {
	d := &diskRates{env: env, disks: map[string]bool{}}
	blocks, _ := env.Collector.FS.Glob("/sys/block/*")
	for _, b := range blocks {
		n := path.Base(b)
		if !strings.HasPrefix(n, "loop") && !strings.HasPrefix(n, "ram") && !strings.HasPrefix(n, "zram") {
			d.disks[n] = true
		}
	}
	return d
}

func (d *diskRates) read() (rd, wr uint64, ok bool) {
	b, err := d.env.Collector.FS.ReadFile("/proc/diskstats")
	if err != nil {
		return 0, 0, false
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 10 || !d.disks[f[2]] {
			continue
		}
		r, _ := strconv.ParseUint(f[5], 10, 64)
		w, _ := strconv.ParseUint(f[9], 10, 64)
		rd += r * 512
		wr += w * 512
	}
	return rd, wr, true
}

func (d *diskRates) sample() (rdBps, wrBps float64, ok bool) {
	rd, wr, ok := d.read()
	if !ok {
		return 0, 0, false
	}
	now := time.Now()
	defer func() { d.rd, d.wr, d.at, d.haveLast = rd, wr, now, true }()
	if !d.haveLast || rd < d.rd || wr < d.wr {
		return 0, 0, false
	}
	dt := now.Sub(d.at).Seconds()
	if dt <= 0 {
		return 0, 0, false
	}
	return float64(rd-d.rd) / dt, float64(wr-d.wr) / dt, true
}

// storageDisks returns the disks a storage test touches (for SMART rules).
func storageDisks(cfg Config, env *Env) []string {
	if env.Baseline == nil || env.Baseline.Storage == nil {
		return nil
	}
	t := cfg.Target
	var out []string
	for _, d := range env.Baseline.Storage.Disks {
		switch {
		case t == d.Path || t == d.Name:
			out = append(out, d.Name)
		case cfg.StorageMode != "read":
			for _, m := range d.Mounted {
				if m == t {
					out = append(out, d.Name)
				}
			}
			for _, h := range d.Holders { // md/dm under the mountpoint
				for _, fs := range env.Baseline.Storage.Filesystems {
					if fs.Mountpoint == t && strings.Contains(fs.Source, h) {
						out = append(out, d.Name)
					}
				}
			}
		}
	}
	return out
}

func humanBytes(b uint64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.1f TiB", float64(b)/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(b)/(1<<20))
	}
	return fmt.Sprintf("%d KiB", b>>10)
}

// preferOOMKill sets this process's oom_score_adj to 1000 and returns a
// function that puts the old value back (lowering it needs
// CAP_SYS_RESOURCE; without it Tyr simply stays the preferred victim).
func preferOOMKill() (func(), error) {
	const f = "/proc/self/oom_score_adj"
	old, err := os.ReadFile(f)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(f, []byte("1000"), 0); err != nil {
		return nil, err
	}
	return func() { _ = os.WriteFile(f, old, 0) }, nil
}
