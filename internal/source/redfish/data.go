package redfish

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/model"
)

type link struct {
	ID string `json:"@odata.id"`
}

type collection struct {
	Members  []link `json:"Members"`
	NextLink string `json:"Members@odata.nextLink"`
}

func getJSON(ctx context.Context, g Getter, path string, v any) error {
	b, err := g.Get(ctx, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// members lists a collection's member paths.
func members(ctx context.Context, g Getter, path string) ([]string, error) {
	var c collection
	if err := getJSON(ctx, g, path, &c); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(c.Members))
	for _, m := range c.Members {
		out = append(out, m.ID)
	}
	return out, nil
}

// --- event logs ---------------------------------------------------------------

type rawEntry struct {
	ID         string `json:"Id"`
	Created    string `json:"Created"`
	Severity   string `json:"Severity"`
	Message    string `json:"Message"`
	MessageID  string `json:"MessageId"`
	SensorType string `json:"SensorType"`
	Oem        struct {
		Hpe struct {
			ClassDescription string `json:"ClassDescription"`
			Count            int    `json:"Count"`
			Updated          string `json:"Updated"`
			Repaired         *bool  `json:"Repaired"`
		} `json:"Hpe"`
	} `json:"Oem"`
}

const maxPages = 25

// Logs reads every log service of every system and manager (HPE: IML and
// the iLO event log; others: SEL, Lclog), at most max entries per log,
// newest kept. Logs that cannot be read are reported in errs.
func Logs(ctx context.Context, g Getter, max int) ([]model.EventLog, []model.Event, []string) {
	var logs []model.EventLog
	var out []model.Event
	var errs []string
	for _, root := range []string{"/redfish/v1/Systems", "/redfish/v1/Managers"} {
		parents, err := members(ctx, g, root)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for _, p := range parents {
			services, err := members(ctx, g, strings.TrimSuffix(p, "/")+"/LogServices")
			if err != nil {
				continue // no logs on this resource
			}
			for _, sp := range services {
				var svc struct {
					ID      string `json:"Id"`
					Name    string `json:"Name"`
					Entries link   `json:"Entries"`
				}
				if err := getJSON(ctx, g, sp, &svc); err != nil || svc.Entries.ID == "" {
					if err != nil {
						errs = append(errs, err.Error())
					}
					continue
				}
				name := logName(svc.ID, svc.Name)
				es, err := entries(ctx, g, svc.Entries.ID, name, max)
				if err != nil {
					errs = append(errs, name+": "+err.Error())
				}
				logs = append(logs, model.EventLog{Name: name, Source: "redfish", Entries: len(es)})
				out = append(out, es...)
			}
		}
	}
	return logs, out, errs
}

func logName(id, name string) string {
	switch strings.ToUpper(id) {
	case "IML":
		return "IML (Integrated Management Log)"
	case "IEL":
		return "iLO event log"
	case "SEL":
		return "BMC SEL"
	}
	if name != "" {
		return name
	}
	return id
}

func entries(ctx context.Context, g Getter, path, log string, max int) ([]model.Event, error) {
	var raw []rawEntry
	for page := 0; path != "" && page < maxPages; page++ {
		var c struct {
			Members  []rawEntry `json:"Members"`
			NextLink string     `json:"Members@odata.nextLink"`
		}
		if err := getJSON(ctx, g, path, &c); err != nil {
			return toEvents(raw, log, max), err
		}
		raw = append(raw, c.Members...)
		path = c.NextLink
	}
	return toEvents(raw, log, max), nil
}

func toEvents(raw []rawEntry, log string, max int) []model.Event {
	out := make([]model.Event, 0, len(raw))
	for _, r := range raw {
		e := model.Event{Log: log, Source: "redfish", ID: r.ID, Message: strings.TrimSpace(r.Message), Severity: severity(r.Severity),
			Class: r.Oem.Hpe.ClassDescription, Component: r.SensorType, Count: r.Oem.Hpe.Count, Repaired: r.Oem.Hpe.Repaired}
		e.Time = parseTime(r.Created)
		e.Updated = parseTime(r.Oem.Hpe.Updated)
		if e.Class == "" && r.MessageID != "" {
			e.Class = r.MessageID
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	if max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, l := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func severity(s string) model.Status {
	switch strings.ToLower(s) {
	case "critical":
		return model.StatusCrit
	case "warning":
		return model.StatusWarn
	case "ok", "informational":
		return model.StatusOK
	}
	return model.StatusUnknown
}

// --- power ---------------------------------------------------------------------

type status struct {
	State  string `json:"State"`
	Health string `json:"Health"`
}

type rawPower struct {
	PowerControl []struct {
		PowerConsumedWatts *float64 `json:"PowerConsumedWatts"`
		PowerCapacityWatts *float64 `json:"PowerCapacityWatts"`
		PowerLimit         struct {
			LimitInWatts *float64 `json:"LimitInWatts"`
		} `json:"PowerLimit"`
		PowerMetrics struct {
			Avg      *float64 `json:"AverageConsumedWatts"`
			Min      *float64 `json:"MinConsumedWatts"`
			Max      *float64 `json:"MaxConsumedWatts"`
			Interval int      `json:"IntervalInMin"`
		} `json:"PowerMetrics"`
	} `json:"PowerControl"`
	PowerSupplies []rawSupply `json:"PowerSupplies"`
	Redundancy    []struct {
		Name   string `json:"Name"`
		Mode   string `json:"Mode"`
		Status status `json:"Status"`
	} `json:"Redundancy"`
}

type rawSupply struct {
	MemberID        string   `json:"MemberId"`
	Name            string   `json:"Name"`
	Model           string   `json:"Model"`
	Manufacturer    string   `json:"Manufacturer"`
	SerialNumber    string   `json:"SerialNumber"`
	PartNumber      string   `json:"PartNumber"`
	SparePartNumber string   `json:"SparePartNumber"`
	FirmwareVersion string   `json:"FirmwareVersion"`
	PowerSupplyType string   `json:"PowerSupplyType"`
	Capacity        *float64 `json:"PowerCapacityWatts"`
	LastOutput      *float64 `json:"LastPowerOutputWatts"`
	InputVoltage    *float64 `json:"LineInputVoltage"`
	InputType       string   `json:"LineInputVoltageType"`
	Status          status   `json:"Status"`
	Oem             struct {
		Hpe struct {
			BayNumber         *int `json:"BayNumber"`
			PowerSupplyStatus struct {
				State string `json:"State"`
			} `json:"PowerSupplyStatus"`
		} `json:"Hpe"`
	} `json:"Oem"`
}

// ReadPower reads the first chassis that has power data.
func ReadPower(ctx context.Context, g Getter) (*model.Power, error) {
	chassis, err := members(ctx, g, "/redfish/v1/Chassis")
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, ch := range chassis {
		var c struct {
			Power link `json:"Power"`
		}
		if err := getJSON(ctx, g, ch, &c); err != nil {
			lastErr = err
			continue
		}
		if c.Power.ID == "" {
			continue
		}
		var rp rawPower
		if err := getJSON(ctx, g, c.Power.ID, &rp); err != nil {
			lastErr = err
			continue
		}
		return toPower(rp), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no chassis with power data")
	}
	return nil, lastErr
}

func toPower(rp rawPower) *model.Power {
	p := &model.Power{Supplies: []model.PowerSupply{}}
	if len(rp.PowerControl) > 0 {
		pc := rp.PowerControl[0]
		p.ConsumedWatts, p.CapacityWatts, p.LimitWatts = pc.PowerConsumedWatts, pc.PowerCapacityWatts, pc.PowerLimit.LimitInWatts
		p.AvgWatts, p.MinWatts, p.MaxWatts, p.IntervalMin = pc.PowerMetrics.Avg, pc.PowerMetrics.Min, pc.PowerMetrics.Max, pc.PowerMetrics.Interval
	}
	for i, s := range rp.PowerSupplies {
		ps := model.PowerSupply{Name: s.Name, Model: s.Model, Manufacturer: s.Manufacturer, Serial: s.SerialNumber, PartNumber: s.PartNumber,
			SparePart: s.SparePartNumber, Firmware: s.FirmwareVersion, Type: s.PowerSupplyType, CapacityWatts: s.Capacity,
			OutputWatts: s.LastOutput, InputVolts: s.InputVoltage, InputType: s.InputType, State: s.Status.State, Health: s.Status.Health,
			Detail: s.Oem.Hpe.PowerSupplyStatus.State}
		switch {
		case s.Oem.Hpe.BayNumber != nil:
			ps.Bay = strconv.Itoa(*s.Oem.Hpe.BayNumber)
		case s.MemberID != "":
			ps.Bay = s.MemberID
		default:
			ps.Bay = strconv.Itoa(i + 1)
		}
		if ps.Name == "" || ps.Name == "HpeServerPowerSupply" {
			ps.Name = "Power Supply " + ps.Bay
		}
		ps.Present = !strings.EqualFold(s.Status.State, "Absent")
		ps.Status = supplyStatus(ps)
		p.Supplies = append(p.Supplies, ps)
	}
	for _, r := range rp.Redundancy {
		p.Redundancy = append(p.Redundancy, model.Redundancy{Name: r.Name, Mode: r.Mode, Health: r.Status.Health, Status: severity(r.Status.Health)})
	}
	return p
}

// supplyStatus: Redfish health, plus no AC input on an installed supply.
func supplyStatus(s model.PowerSupply) model.Status {
	if !s.Present {
		return model.StatusUnknown
	}
	st := severity(s.Health)
	if st == model.StatusUnknown && s.Health == "" {
		st = model.StatusOK
	}
	if s.InputVolts != nil && *s.InputVolts == 0 {
		st = model.Worst(st, model.StatusCrit)
	}
	return st
}
