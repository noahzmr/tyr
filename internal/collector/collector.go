// Package collector defines the collector interface, the shared collection
// environment, and the runner that assembles a Snapshot.
package collector

import (
	"context"
	"errors"
	"fmt"
	"github.com/noahzmr/tyr/internal/source/redfish"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/noahzmr/tyr/internal/buildinfo"
	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/execx"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/dmi"
	"github.com/noahzmr/tyr/internal/source/ipmi"
	"github.com/noahzmr/tyr/internal/source/kernellog"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

// Collector produces one module section.
type Collector interface {
	ID() model.ModuleID
	// Collect performs a full collection when prev is nil; otherwise it
	// refreshes live values (sensors, counters, link state) starting from
	// prev, which it must not modify. It always returns a usable section with
	// partial data, findings and source states. A non-nil error means nothing
	// could be collected; the runner then reports the module as UNKNOWN.
	Collect(ctx context.Context, env *Env, prev model.Section) (model.Section, error)
}

// Options are the user-controlled collection settings.
type Options struct {
	Thresholds      check.Defaults
	Timeout         time.Duration // per collector
	NoIPMI          bool
	NoDMI           bool
	NoSMART         bool
	NICAll          bool
	SELAll          bool
	CoreTest        bool
	CoreTestTimeout time.Duration
	// SMARTInterval is how long SMART data is reused before the drives are
	// queried again. Queries are not free: every rejected command counts as
	// a SCSI error on the device (ioerr_cnt), and some controllers log them.
	SMARTInterval time.Duration
	// Redfish: the BMC's address when its host interface is not used
	// (also TYR_REDFISH or "host =" in the config file); RedfishConfig is
	// the config file with credentials and pinned certificates.
	Redfish       string
	RedfishConfig string
	NoRedfish     bool
}

type smartAgeKey struct{}

// WithSMARTMaxAge asks the storage collector for SMART data no older than
// d (0: query the drives now) for collections under ctx.
func WithSMARTMaxAge(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, smartAgeKey{}, d)
}

// SMARTMaxAge is the age limit for ctx, else def.
func SMARTMaxAge(ctx context.Context, def time.Duration) time.Duration {
	if d, ok := ctx.Value(smartAgeKey{}).(time.Duration); ok {
		return d
	}
	return def
}

// DefaultOptions returns the CLI defaults.
func DefaultOptions() Options {
	return Options{Thresholds: check.DefaultDefaults(), Timeout: 10 * time.Second, CoreTestTimeout: 2 * time.Second, SMARTInterval: 5 * time.Minute,
		RedfishConfig: redfish.DefaultConfig}
}

// Privileges describe what the process may access. They are probed with
// access checks rather than uid==0 so that capabilities and groups work.
type Privileges struct {
	Root bool // effective uid 0
	DMI  bool // can read the SMBIOS tables
	Kmsg bool // can read /dev/kmsg
	IPMI bool // /dev/ipmi0 (or ipmi/0, ipmidev/0) readable+writable
}

// Sim configures simulated active checks in --fixtures mode.
type Sim struct {
	CoreTestFail    []int `json:"core_test_fail"`
	CoreTestTimeout []int `json:"core_test_timeout"`
}

// Env is the environment shared by all collectors.
type Env struct {
	FS        hostfs.FS
	Exec      execx.Runner
	Now       func() time.Time
	Opts      Options
	Priv      Privileges
	Virt      string
	Arch      string
	Hostname  string
	Simulated bool // fixture mode: active tests are simulated
	LiveSim   bool // fixture mode with simulated changing counters (demo)
	Sim       Sim
	// Redfish is set for fixtures (a recorded tree); on a host the client
	// is found on first use (RedfishClient).
	Redfish redfish.Getter

	mu      sync.Mutex
	round   *roundCache
	rf      rfCache
	ipmiNat struct { // /dev/ipmi0, opened once per process
		once sync.Once
		cl   *ipmi.Client
		err  error
	}
}

// NewHostEnv builds an Env for the live system.
func NewHostEnv(opts Options) *Env {
	f := hostfs.Host()
	host, _ := os.Hostname()
	e := &Env{
		FS: f, Exec: execx.Host(), Now: time.Now, Opts: opts,
		Virt: sysfs.DetectVirt(f), Arch: runtime.GOARCH, Hostname: host,
	}
	e.Priv = Privileges{
		Root: os.Geteuid() == 0,
		DMI:  f.Readable("/sys/firmware/dmi/tables/DMI") || (os.Geteuid() == 0 && hostfs.Exists(f, "/dev/mem")),
		Kmsg: kernellog.KmsgReadable(f),
		IPMI: ipmiDev(f) != "",
	}
	return e
}

func ipmiDev(f hostfs.FS) string {
	for _, p := range []string{"/dev/ipmi0", "/dev/ipmi/0", "/dev/ipmidev/0"} {
		if hostfs.Exists(f, p) && f.Readable(p) {
			return p
		}
	}
	return ""
}

type roundCache struct {
	dmiOnce sync.Once
	dmi     *dmi.Tables
	dmiSt   model.SourceState

	klogOnce sync.Once
	klog     *kernellog.Log
	klogSt   model.SourceState

	bootOnce sync.Once
	boot     time.Time

	ipmiOnce sync.Once
	ipmi     *ipmi.Client
	ipmiSt   model.SourceState

	sensOnce sync.Once
	sens     []ipmi.Sensor
	sensSt   model.SourceState

	selOnce sync.Once
	sel     *ipmi.SEL
	selSt   model.SourceState
}

// NewRound discards cached source data; call it before each collection round.
func (e *Env) NewRound() {
	e.mu.Lock()
	e.round = &roundCache{}
	e.mu.Unlock()
}

func (e *Env) cache() *roundCache {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.round == nil {
		e.round = &roundCache{}
	}
	return e.round
}

// DMI returns the parsed dmidecode output (once per round) and its state.
func (e *Env) DMI(ctx context.Context) (*dmi.Tables, model.SourceState) {
	c := e.cache()
	c.dmiOnce.Do(func() {
		c.dmiSt = model.SourceState{Name: "dmidecode", State: model.AvailOK}
		switch {
		case e.Opts.NoDMI:
			c.dmiSt.State, c.dmiSt.Reason = model.AvailSkipped, "disabled by --no-dmidecode"
			return
		case !e.Priv.DMI:
			c.dmiSt.State, c.dmiSt.Reason = model.AvailUnavailable, "requires root"
			return
		}
		// the kernel's copy of the SMBIOS table, decoded natively; dmidecode
		// only when it cannot be read
		var nativeErr error
		if tab, err := e.FS.ReadFile("/sys/firmware/dmi/tables/DMI"); err == nil && len(tab) > 0 {
			entry, _ := e.FS.ReadFile("/sys/firmware/dmi/tables/smbios_entry_point")
			t, err := dmi.ParseTable(tab, entry)
			if err == nil {
				c.dmi, c.dmiSt.Reason = t, "SMBIOS table, read natively"
				return
			}
			nativeErr = err
		}
		if _, err := e.Exec.LookPath("dmidecode"); err != nil {
			c.dmiSt.State, c.dmiSt.Reason = model.AvailUnavailable, "SMBIOS table not readable and dmidecode not installed"
			if nativeErr != nil {
				c.dmiSt.Reason = nativeErr.Error() + ", and dmidecode not installed"
			}
			return
		}
		t, err := dmi.Read(ctx, e.Exec)
		if err != nil {
			c.dmiSt.State, c.dmiSt.Reason = model.AvailUnavailable, err.Error()
			return
		}
		c.dmi, c.dmiSt.Reason = t, "via dmidecode"
	})
	return c.dmi, c.dmiSt
}

// BootTime returns the boot time from /proc/stat btime.
func (e *Env) BootTime() time.Time {
	c := e.cache()
	c.bootOnce.Do(func() {
		if b, err := e.FS.ReadFile("/proc/stat"); err == nil {
			if ps, err := sysfs.ParseProcStat(b); err == nil && ps.BTime > 0 {
				c.boot = time.Unix(ps.BTime, 0).UTC()
			}
		}
	})
	return c.boot
}

// KernelLog returns the classified kernel log of this boot (once per round).
func (e *Env) KernelLog(ctx context.Context) (*kernellog.Log, model.SourceState) {
	c := e.cache()
	c.klogOnce.Do(func() {
		l, err := kernellog.Read(ctx, e.FS, e.Exec, e.BootTime())
		if err != nil {
			c.klogSt = model.SourceState{Name: "kernel_log", State: model.AvailUnavailable, Reason: err.Error()}
			return
		}
		c.klog = l
		c.klogSt = model.SourceState{Name: "kernel_log", State: model.AvailOK, Reason: "via " + l.Source}
	})
	return c.klog, c.klogSt
}

// IPMI returns a client for the BMC, or nil with the reason it is unusable.
func (e *Env) IPMI() (*ipmi.Client, model.SourceState) {
	c := e.cache()
	c.ipmiOnce.Do(func() {
		st := model.SourceState{Name: "ipmi", State: model.AvailUnavailable}
		switch {
		case e.Opts.NoIPMI:
			st.State, st.Reason = model.AvailSkipped, "disabled by --no-ipmi"
		case e.IsVM():
			st.State, st.Reason = model.AvailNotApplicable, "virtual machine ("+e.Virt+")"
		case !e.Priv.IPMI:
			if ipmiDevExists(e.FS) {
				st.Reason = "requires root (/dev/ipmi0 not accessible)"
			} else {
				st.Reason = "no /dev/ipmi0 (no BMC, or ipmi_si/ipmi_devintf not loaded)"
			}
		default:
			if e.FS.IsHost() { // the BMC through the kernel driver, no tool needed
				e.ipmiNat.once.Do(func() { e.ipmiNat.cl, e.ipmiNat.err = ipmi.OpenNative() })
				if e.ipmiNat.cl != nil {
					c.ipmi = e.ipmiNat.cl
					st.State, st.Reason = model.AvailOK, "via /dev/ipmi0 (native)"
					break
				}
			}
			cl, err := ipmi.Find(e.Exec)
			if err != nil {
				st.Reason = err.Error()
				if e.ipmiNat.err != nil {
					st.Reason = "native: " + e.ipmiNat.err.Error() + "; " + st.Reason
				}
				break
			}
			c.ipmi = cl
			st.State, st.Reason = model.AvailOK, "via "+string(cl.Tool)
		}
		c.ipmiSt = st
	})
	return c.ipmi, c.ipmiSt
}

func ipmiDevExists(f hostfs.FS) bool {
	for _, p := range []string{"/dev/ipmi0", "/dev/ipmi/0", "/dev/ipmidev/0"} {
		if hostfs.Exists(f, p) {
			return true
		}
	}
	return false
}

// IPMISensors returns all BMC sensors (once per round).
func (e *Env) IPMISensors(ctx context.Context) ([]ipmi.Sensor, model.SourceState) {
	c := e.cache()
	c.sensOnce.Do(func() {
		cl, st := e.IPMI()
		c.sensSt = st
		if cl == nil {
			return
		}
		s, err := cl.Sensors(ctx)
		if err != nil {
			c.sensSt = model.SourceState{Name: "ipmi", State: model.AvailUnavailable, Reason: err.Error()}
			return
		}
		c.sens = s
	})
	return c.sens, c.sensSt
}

// SEL returns the BMC system event log (once per round).
func (e *Env) SEL(ctx context.Context) (*ipmi.SEL, model.SourceState) {
	c := e.cache()
	c.selOnce.Do(func() {
		cl, st := e.IPMI()
		st.Name = "ipmi_sel"
		c.selSt = st
		if cl == nil {
			return
		}
		s, err := cl.SEL(ctx)
		if err != nil {
			c.selSt = model.SourceState{Name: "ipmi_sel", State: model.AvailUnavailable, Reason: err.Error()}
			return
		}
		c.sel = s
	})
	return c.sel, c.selSt
}

// IsVM reports whether the host is virtualized or a container.
func (e *Env) IsVM() bool { return e.Virt != "" && e.Virt != "none" }

// Collect runs the given collectors concurrently and assembles a snapshot.
// prev (may be nil) enables refresh mode for collectors whose section exists.
func Collect(ctx context.Context, env *Env, cols []Collector, prev *model.Snapshot) *model.Snapshot {
	env.NewRound()
	snap := &model.Snapshot{
		SchemaVersion:  model.SchemaVersion,
		TyrVersion:     buildinfo.Version,
		Hostname:       env.Hostname,
		Timestamp:      env.Now().UTC(),
		Privileged:     env.Priv.Root,
		Virtualization: env.Virt,
		Arch:           env.Arch,
	}
	type result struct {
		id  model.ModuleID
		sec model.Section
	}
	results := make(chan result, len(cols))
	for _, c := range cols {
		go func() {
			var p model.Section
			if prev != nil {
				p = prev.Section(c.ID())
			}
			results <- result{c.ID(), runOne(ctx, env, c, p)}
		}()
	}
	for range cols {
		r := <-results
		snap.SetSection(r.id, r.sec)
	}
	var sts []model.Status
	for _, id := range append([]model.ModuleID{model.ModSystem}, model.Modules...) {
		if s := snap.Section(id); s != nil {
			sts = append(sts, s.Head().Status)
		}
	}
	snap.Status = model.Worst(sts...)
	snap.Inventory = model.BuildInventory(snap)
	snap.Summary = check.SummaryLine(snap.Findings(), 200)
	return snap
}

func runOne(ctx context.Context, env *Env, c Collector, prev model.Section) (sec model.Section) {
	start := env.Now()
	timeout := env.Opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if env.Opts.CoreTest && c.ID() == model.ModCPU {
		timeout += env.Opts.CoreTestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			sec = failed(c.ID(), fmt.Errorf("internal error: %v\n%s", r, debug.Stack()))
		}
		h := sec.Head()
		h.CollectedAt = env.Now().UTC()
		h.DurationMS = env.Now().Sub(start).Milliseconds()
	}()
	s, err := c.Collect(ctx, env, prev)
	if err != nil || s == nil {
		if err == nil {
			err = errors.New("collector returned no data")
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("timed out after %v: %w", timeout, err)
		}
		return failed(c.ID(), err)
	}
	return s
}

func failed(id model.ModuleID, err error) model.Section {
	s := model.NewSection(id)
	h := s.Head()
	h.Find(model.StatusUnknown, "collect_failed", string(id), "tyr", fmt.Sprintf("%s: %v", id, err))
	h.Summary = "unavailable: " + err.Error()
	h.Rollup()
	return s
}
