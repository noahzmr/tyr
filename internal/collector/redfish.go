package collector

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/redfish"
)

// Redfish data is cached across rounds: the logs for 2 minutes, power for
// 15 seconds; a BMC answers slowly and the TUI refreshes every few seconds.
const (
	redfishLogTTL   = 2 * time.Minute
	redfishPowerTTL = 15 * time.Second
	// storage, device and firmware inventory change rarely and take many
	// requests to read
	redfishHardwareTTL = 10 * time.Minute
	redfishMaxLog      = 500
)

type rfCache struct {
	once  sync.Once
	g     redfish.Getter
	st    model.SourceState
	mu    sync.Mutex
	logs  rfJob[rfLogs]
	power rfJob[*model.Power]
	hw    rfJob[redfish.Hardware]
}

type rfLogs struct {
	logs   []model.EventLog
	events []model.Event
}

// rfJob is one cached Redfish read that refreshes in the background, so a
// slow BMC never holds up a collection round.
type rfJob[T any] struct {
	at   time.Time
	val  T
	st   model.SourceState
	busy bool
	done chan struct{}
}

// fetch returns the cached value, starting a background refresh when it
// is older than ttl. Without any value yet it waits for the first read
// until ctx ends.
func fetch[T any](e *Env, ctx context.Context, j *rfJob[T], ttl time.Duration, load func(context.Context) (T, model.SourceState)) (T, model.SourceState) {
	e.rf.mu.Lock()
	if !j.busy && (j.at.IsZero() || e.Now().Sub(j.at) >= ttl) {
		j.busy, j.done = true, make(chan struct{})
		done := j.done
		go func() {
			bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			v, st := load(bg)
			cancel()
			e.rf.mu.Lock()
			j.val, j.st, j.at, j.busy = v, st, e.Now(), false
			e.rf.mu.Unlock()
			close(done)
		}()
	}
	have, done := !j.at.IsZero(), j.done
	v, st := j.val, j.st
	e.rf.mu.Unlock()
	if have {
		return v, st
	}
	select {
	case <-done:
		e.rf.mu.Lock()
		defer e.rf.mu.Unlock()
		return j.val, j.st
	case <-ctx.Done():
		var zero T
		return zero, model.SourceState{Name: "redfish", State: model.AvailUnavailable, Reason: "still reading the BMC (first read): the data appears with the next refresh"}
	}
}

// RedfishClient finds the BMC's Redfish service once: the host interface
// (SMBIOS type 42) first, then the configured address.
func (e *Env) RedfishClient(ctx context.Context) (redfish.Getter, model.SourceState) {
	e.rf.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		st := model.SourceState{Name: "redfish", State: model.AvailOK}
		switch {
		case e.Redfish != nil:
			e.rf.g, st.Reason = e.Redfish, "recorded"
		case e.Simulated:
			st.State, st.Reason = model.AvailNotApplicable, "fixture without a Redfish recording"
		case e.Opts.NoRedfish:
			st.State, st.Reason = model.AvailNotApplicable, "--no-redfish"
		case e.IsVM():
			st.State, st.Reason = model.AvailNotApplicable, "virtual machine ("+e.Virt+")"
		default:
			e.rf.g, st = e.dialRedfish(ctx)
		}
		e.rf.st = st
	})
	return e.rf.g, e.rf.st
}

func (e *Env) dialRedfish(ctx context.Context) (redfish.Getter, model.SourceState) {
	st := model.SourceState{Name: "redfish", State: model.AvailUnavailable}
	cfg, err := redfish.LoadConfig(e.Opts.RedfishConfig)
	if err != nil {
		st.Reason = err.Error()
		return nil, st
	}
	if e.Opts.Redfish != "" {
		cfg.Host = e.Opts.Redfish
	}
	var cands [][2]string
	if tables, _ := e.DMI(ctx); tables != nil {
		for _, r := range tables.ByType(42) {
			var lines []string
			for _, l := range r.Lists {
				lines = append(lines, l...)
			}
			for k, v := range r.Fields {
				lines = append(lines, k+": "+v)
			}
			if a := redfish.HostInterface(lines); a != "" {
				cands = append(cands, [2]string{a, "host interface"})
			}
		}
	}
	if cfg.Host != "" {
		cands = append(cands, [2]string{cfg.Host, "address"})
	}
	if len(cands) == 0 {
		st.Reason = "no BMC host interface (enable iLO's Virtual NIC) and no address: --redfish HOST, TYR_REDFISH or host= in " + cfg.Path
		return nil, st
	}
	if cfg.User == "" || cfg.Password == "" {
		st.Reason = "no Redfish credentials: TYR_REDFISH_USER and TYR_REDFISH_PASSWORD, or user= and password= in " + cfg.Path
		return nil, st
	}
	var errs []string
	for _, c := range cands {
		dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		cl, err := redfish.Dial(dctx, &cfg, c[0], c[1])
		cancel()
		if err == nil {
			st.State, st.Reason = model.AvailOK, "via "+c[1]+" "+cl.Addr
			if cl.Note != "" {
				st.Reason += "; " + cl.Note
			}
			return cl, st
		}
		if errors.Is(err, redfish.ErrPinMismatch) {
			st.Reason = c[0] + ": " + err.Error()
			return nil, st
		}
		errs = append(errs, c[1]+" "+c[0]+": "+err.Error())
	}
	st.Reason = strings.Join(errs, "; ")
	return nil, st
}

// RedfishLogs are the BMC's event logs (cached, refreshed in the background).
func (e *Env) RedfishLogs(ctx context.Context) ([]model.EventLog, []model.Event, model.SourceState) {
	v, st := fetch(e, ctx, &e.rf.logs, redfishLogTTL, func(bg context.Context) (rfLogs, model.SourceState) {
		g, st := e.RedfishClient(bg)
		if g == nil {
			return rfLogs{}, st
		}
		logs, events, errs := redfish.Logs(bg, g, redfishMaxLog)
		switch {
		case len(logs) == 0 && len(errs) > 0:
			st.State, st.Reason = model.AvailUnavailable, strings.Join(errs, "; ")
		case len(errs) > 0:
			st.Reason += "; some logs unreadable: " + strings.Join(errs, "; ")
		}
		return rfLogs{logs, events}, st
	})
	return v.logs, v.events, st
}

// RedfishPower is the chassis power data (cached, refreshed in the background).
func (e *Env) RedfishPower(ctx context.Context) (*model.Power, model.SourceState) {
	return fetch(e, ctx, &e.rf.power, redfishPowerTTL, func(bg context.Context) (*model.Power, model.SourceState) {
		g, st := e.RedfishClient(bg)
		if g == nil {
			return nil, st
		}
		p, err := redfish.ReadPower(bg, g)
		if err != nil {
			st.State, st.Reason = model.AvailUnavailable, err.Error()
		}
		return p, st
	})
}

// RedfishHardware is what the BMC knows about drives behind RAID
// controllers, the device inventory and the firmware inventory (cached for
// 10 minutes, refreshed in the background).
func (e *Env) RedfishHardware(ctx context.Context) (redfish.Hardware, model.SourceState) {
	return fetch(e, ctx, &e.rf.hw, redfishHardwareTTL, func(bg context.Context) (redfish.Hardware, model.SourceState) {
		g, st := e.RedfishClient(bg)
		if g == nil {
			return redfish.Hardware{}, st
		}
		hw, errs := redfish.ReadHardware(bg, g)
		if len(errs) > 0 {
			st.Reason += "; some resources unreadable: " + strings.Join(errs, "; ")
		}
		return hw, st
	})
}
