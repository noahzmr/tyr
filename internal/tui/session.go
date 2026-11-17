package tui

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/model"
)

// session is what the TUI remembers across refreshes: the first snapshot
// as a baseline (NEW / resolved findings, counter growth), the previous
// counters (growth in the last interval), sensor history for sparklines,
// and list preferences shared by every tab.
type session struct {
	start        time.Time
	baseFindings map[string]bool
	seen         map[string]model.Finding // every non-OK finding seen this session
	current      map[string]bool
	baseCounters map[string]uint64
	prevCounters map[string]uint64
	curCounters  map[string]uint64
	hist         map[string][]float64
	colWidth     map[string]int // auto table columns: widest value seen
	firstSeen    map[string]time.Time
	resolvedAt   map[string]time.Time
	prefs        prefs
}

// prefs are list preferences shared by all tabs.
type prefs struct {
	problemsOnly bool // "!" shows only WARN/CRIT/UNKNOWN rows
	naturalOrder bool // "o" keeps device order instead of problems first
	mouse        bool
	gen          int    // snapshot generation, bumped on every refresh
	lost         string // a selected row that vanished with the last refresh
}

const sessionHistLen = 120

func newSession() *session {
	return &session{seen: map[string]model.Finding{}, hist: map[string][]float64{}, colWidth: map[string]int{},
		firstSeen: map[string]time.Time{}, resolvedAt: map[string]time.Time{}}
}

// update records a new snapshot.
func (s *session) update(snap *model.Snapshot) {
	fs := snap.Findings()
	if s.baseFindings == nil {
		s.start = snap.Timestamp
		s.baseFindings = map[string]bool{}
		for _, f := range fs {
			s.baseFindings[f.Key()] = true
		}
	}
	s.current = map[string]bool{}
	for _, f := range fs {
		s.current[f.Key()] = true
		if f.Status != model.StatusOK {
			s.seen[f.Key()] = f
			if _, ok := s.firstSeen[f.Key()]; !ok {
				s.firstSeen[f.Key()] = snap.Timestamp
			}
			delete(s.resolvedAt, f.Key())
		}
	}
	for k := range s.seen {
		if _, gone := s.resolvedAt[k]; !s.current[k] && !gone {
			s.resolvedAt[k] = snap.Timestamp
		}
	}
	c := snap.Counters()
	if s.baseCounters == nil {
		s.baseCounters = c
	}
	s.prevCounters, s.curCounters = s.curCounters, c
	s.recordSensors(snap)
}

// isNew reports whether a finding appeared after the first snapshot.
func (s *session) isNew(f model.Finding) bool {
	return s != nil && s.baseFindings != nil && !s.baseFindings[f.Key()]
}

// resolved returns findings seen earlier in the session that are gone now.
func (s *session) resolved() []model.Finding {
	if s == nil {
		return nil
	}
	var out []model.Finding
	for k, f := range s.seen {
		if !s.current[k] {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// counterChange is a monotonic counter that grew during the session.
type counterChange struct {
	Key              string
	Value            uint64
	SinceStart, Last uint64 // growth since the first snapshot / last refresh
}

// rising returns counters that grew since the session started and match
// the filter (nil = all), largest recent growth first.
func (s *session) rising(match func(string) bool) []counterChange {
	if s == nil {
		return nil
	}
	var out []counterChange
	for k, v := range s.curCounters {
		if match != nil && !match(k) {
			continue
		}
		b, ok := s.baseCounters[k]
		if !ok || v <= b {
			continue
		}
		ch := counterChange{Key: k, Value: v, SinceStart: v - b}
		if p, ok := s.prevCounters[k]; ok && v > p {
			ch.Last = v - p
		}
		out = append(out, ch)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Last != out[j].Last {
			return out[i].Last > out[j].Last
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// counters returns all current counters matching the filter with their
// growth (zero growth included), sorted by key.
func (s *session) counters(match func(string) bool) []counterChange {
	if s == nil {
		return nil
	}
	var out []counterChange
	for k, v := range s.curCounters {
		if !match(k) {
			continue
		}
		ch := counterChange{Key: k, Value: v}
		if b, ok := s.baseCounters[k]; ok && v > b {
			ch.SinceStart = v - b
		}
		if p, ok := s.prevCounters[k]; ok && v > p {
			ch.Last = v - p
		}
		out = append(out, ch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (s *session) record(key string, v *float64) {
	if v == nil {
		return
	}
	h := append(s.hist[key], *v)
	if len(h) > sessionHistLen {
		h = h[len(h)-sessionHistLen:]
	}
	s.hist[key] = h
}

// history returns the recorded values of a sensor key.
func (s *session) history(key string) []float64 {
	if s == nil {
		return nil
	}
	return s.hist[key]
}

// recordSensors keeps a short history of the readings shown in detail
// pages. Keys are "<module>/<component>/<sensor>".
func (s *session) recordSensors(snap *model.Snapshot) {
	if pw := snap.Power; pw != nil {
		s.record("power/draw", pw.ConsumedWatts)
	}
	if c := snap.CPU; c != nil {
		var sum, n float64
		for _, l := range c.Logical {
			if l.UtilizationPercent != nil {
				sum += *l.UtilizationPercent
				n++
			}
		}
		if n > 0 {
			avg := sum / n
			s.record("cpu/load", &avg)
		}
		s.record("cpu/maxtemp", c.MaxTempC)
		for _, p := range c.PackageTemps {
			s.record("cpu/"+p.Name+"/temp", p.Value)
		}
		for _, l := range c.Logical {
			id := cpuKey(l.ID)
			s.record("cpu/"+id+"/load", l.UtilizationPercent)
			s.record("cpu/"+id+"/temp", l.TempCelsius)
			s.record("cpu/"+id+"/mhz", l.FreqMHz)
		}
	}
	if m := snap.Memory; m != nil {
		used := float64(m.UsedBytes)
		s.record("memory/used", &used)
		for _, d := range m.DIMMs {
			s.record("memory/"+d.Locator+"/temp", d.TempCelsius)
		}
	}
	if st := snap.Storage; st != nil {
		for _, d := range st.Disks {
			s.record("storage/"+d.Name+"/temp", d.TempCelsius)
		}
	}
	if n := snap.Network; n != nil {
		var tot float64
		have := false
		for _, x := range n.Interfaces {
			if x.Kind == "physical" && x.RxBps != nil && x.TxBps != nil {
				tot += *x.RxBps + *x.TxBps
				have = true
			}
		}
		if have {
			s.record("network/total", &tot)
		}
		for _, x := range n.Interfaces {
			s.record("network/"+x.Name+"/rx", x.RxBps)
			s.record("network/"+x.Name+"/tx", x.TxBps)
		}
	}
	if a := snap.Accel; a != nil {
		for _, list := range [][]model.Accelerator{a.GPUs, a.NPUs} {
			for _, x := range list {
				for _, t := range x.Temps {
					s.record("accel/"+x.PCIAddress+"/"+t.Name, t.Value)
				}
				s.record("accel/"+x.PCIAddress+"/power", x.PowerWatts)
				s.record("accel/"+x.PCIAddress+"/load", x.UtilizationPercent)
			}
		}
	}
	if sys := snap.System; sys != nil {
		for _, t := range sys.Temps {
			s.record("system/"+t.Name, t.Value)
		}
		for _, f := range sys.Fans {
			s.record("system/"+f.Name, f.Value)
		}
	}
}

func cpuKey(id int) string { return "cpu" + strconv.Itoa(id) }

// subjectMatches reports whether a finding subject refers to a component
// key: exact, one element of a comma list ("cpu7,cpu9"), or the key
// followed by a sensor name ("P2-DIMMC1 Temp").
func subjectMatches(subject, key string) bool {
	if key == "" {
		return false
	}
	for _, p := range strings.Split(subject, ",") {
		p = strings.TrimSpace(p)
		if strings.EqualFold(p, key) || strings.HasPrefix(strings.ToLower(p), strings.ToLower(key)+" ") {
			return true
		}
	}
	return false
}

// relatedFindings returns the findings of a module that refer to key.
func relatedFindings(fs []model.Finding, keys ...string) []model.Finding {
	var out []model.Finding
	for _, f := range fs {
		for _, k := range keys {
			if subjectMatches(f.Subject, k) || (k != "" && strings.Contains(f.Message, k+":")) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}
