package fixture

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/hostfs"
)

// EnableLive makes a fixture look alive for demos and TUI development:
// /proc/stat counters advance with wall-clock time using a deterministic
// per-CPU load pattern, so utilization and sparklines move. Unit tests do not
// call this and keep fully deterministic data.
func EnableLive(env *collector.Env) {
	env.FS = &liveFS{FS: env.FS, start: time.Now()}
	env.LiveSim = true
}

type liveFS struct {
	hostfs.FS
	start time.Time
}

func (l *liveFS) ReadFile(p string) ([]byte, error) {
	b, err := l.FS.ReadFile(p)
	if err != nil || p != "/proc/stat" {
		return b, err
	}
	return l.stat(b), nil
}

// load returns a 0..1 busy fraction for cpu at time t (seconds): a slow wave
// per CPU plus a few CPUs that stay hot.
func load(cpu int, t float64) float64 {
	v := 0.08 + 0.55*math.Pow(math.Sin(t/9+float64(cpu)*0.7), 2)
	if cpu%11 == 3 {
		v = 0.9 + 0.08*math.Sin(t/3)
	}
	return math.Min(1, math.Max(0, v))
}

// busyIntegral is the integral of load over [0, t], approximated in 0.5 s
// steps so counters are monotonic and consistent between reads.
func busyIntegral(cpu int, t float64) float64 {
	sum := 0.0
	const step = 0.5
	for x := 0.0; x < t; x += step {
		sum += load(cpu, x) * math.Min(step, t-x)
	}
	return sum
}

func (l *liveFS) stat(b []byte) []byte {
	t := time.Since(l.start).Seconds()
	const hz = 100.0
	var out strings.Builder
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") || f[0] == "cpu" {
			out.WriteString(line + "\n")
			continue
		}
		var id int
		if _, err := fmt.Sscanf(f[0], "cpu%d", &id); err != nil {
			out.WriteString(line + "\n")
			continue
		}
		var user, idle uint64
		fmt.Sscan(f[1], &user)
		fmt.Sscan(f[4], &idle)
		busy := busyIntegral(id, t) * hz
		user += uint64(busy)
		idle += uint64(t*hz - busy)
		f[1], f[4] = fmt.Sprint(user), fmt.Sprint(idle)
		out.WriteString(strings.Join(f, " ") + "\n")
	}
	return []byte(strings.TrimSuffix(out.String(), "\n") + "\n")
}
