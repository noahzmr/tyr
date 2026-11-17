package redfish

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noahzmr/tyr/internal/model"
)

type mapGetter map[string]string

func (m mapGetter) Get(_ context.Context, p string) ([]byte, error) {
	if s, ok := m[p]; ok {
		return []byte(s), nil
	}
	return nil, os.ErrNotExist
}

// An HPE iLO 5: IML with Oem.Hpe fields, the iLO event log, Power with bays.
var ilo = mapGetter{
	"/redfish/v1/Systems":                                   `{"Members":[{"@odata.id":"/redfish/v1/Systems/1/"}]}`,
	"/redfish/v1/Systems/1/LogServices":                     `{"Members":[{"@odata.id":"/redfish/v1/Systems/1/LogServices/IML/"}]}`,
	"/redfish/v1/Systems/1/LogServices/IML/":                `{"Id":"IML","Name":"Integrated Management Log","Entries":{"@odata.id":"/redfish/v1/Systems/1/LogServices/IML/Entries/"}}`,
	"/redfish/v1/Systems/1/LogServices/IML/Entries/":        `{"Members":[{"Id":"1","Created":"2026-11-13T16:05:11Z","Severity":"Critical","Message":"Uncorrectable Machine Check Exception (Board 0, Processor 2, APIC ID 0x0000002B, Bank 0x00000001, Status 0xBE000000'00800400)","Oem":{"Hpe":{"ClassDescription":"System Error","Count":3,"Updated":"2026-11-13T17:00:00Z","Repaired":false}}}],"Members@odata.nextLink":"/redfish/v1/Systems/1/LogServices/IML/Entries/?page=2"}`,
	"/redfish/v1/Systems/1/LogServices/IML/Entries/?page=2": `{"Members":[{"Id":"2","Created":"2026-11-14T08:00:00Z","Severity":"Warning","Message":"Power Supply Failure (Power Supply 2)","Oem":{"Hpe":{"ClassDescription":"Power","Count":1,"Repaired":true}}}]}`,
	"/redfish/v1/Managers":                                  `{"Members":[{"@odata.id":"/redfish/v1/Managers/1/"}]}`,
	"/redfish/v1/Managers/1/LogServices":                    `{"Members":[{"@odata.id":"/redfish/v1/Managers/1/LogServices/IEL/"}]}`,
	"/redfish/v1/Managers/1/LogServices/IEL/":               `{"Id":"IEL","Name":"iLO Event Log","Entries":{"@odata.id":"/redfish/v1/Managers/1/LogServices/IEL/Entries/"}}`,
	"/redfish/v1/Managers/1/LogServices/IEL/Entries/":       `{"Members":[{"Id":"7","Created":"2026-11-16T10:00:00Z","Severity":"OK","Message":"Browser login: admin"}]}`,
	"/redfish/v1/Chassis":                                   `{"Members":[{"@odata.id":"/redfish/v1/Chassis/1/"}]}`,
	"/redfish/v1/Chassis/1/":                                `{"Power":{"@odata.id":"/redfish/v1/Chassis/1/Power/"}}`,
	"/redfish/v1/Chassis/1/Power/": `{"PowerControl":[{"PowerCapacityWatts":1600,"PowerConsumedWatts":286,"PowerMetrics":{"AverageConsumedWatts":281,"IntervalInMin":20,"MaxConsumedWatts":340,"MinConsumedWatts":270}}],
		"PowerSupplies":[{"FirmwareVersion":"1.00","LastPowerOutputWatts":286,"LineInputVoltage":232,"LineInputVoltageType":"ACHighLine","MemberId":"0","Model":"865408-B21","Name":"HpeServerPowerSupply","PowerCapacityWatts":800,"PowerSupplyType":"AC","SerialNumber":"5WBXU0DLLCQ744","SparePartNumber":"866729-001","Status":{"Health":"OK","State":"Enabled"},"Oem":{"Hpe":{"BayNumber":1,"PowerSupplyStatus":{"State":"Ok"}}}},
		                 {"FirmwareVersion":"1.00","LastPowerOutputWatts":0,"LineInputVoltage":0,"MemberId":"1","Model":"865408-B21","Name":"HpeServerPowerSupply","PowerCapacityWatts":800,"SerialNumber":"5WBXT0D4DBV14E","Status":{"Health":"Critical","State":"Enabled"},"Oem":{"Hpe":{"BayNumber":2,"PowerSupplyStatus":{"State":"Unknown"}}}}],
		"Redundancy":[{"Name":"PowerSupply Redundancy Group 1","Mode":"Failover","Status":{"Health":"Critical","State":"Enabled"}}]}`,
}

func TestHPELogs(t *testing.T) {
	logs, evs, errs := Logs(context.Background(), ilo, 500)
	if len(errs) > 0 || len(logs) != 2 || logs[0].Name != "IML (Integrated Management Log)" || logs[0].Entries != 2 || logs[1].Name != "iLO event log" {
		t.Fatalf("logs %+v errs %v", logs, errs)
	}
	e := evs[0]
	if e.Severity != model.StatusCrit || e.Class != "System Error" || e.Count != 3 || e.Repaired == nil || *e.Repaired ||
		e.Updated.IsZero() || !strings.Contains(e.Message, "Processor 2") {
		t.Errorf("IML entry %+v", e)
	}
	if evs[1].Repaired == nil || !*evs[1].Repaired {
		t.Errorf("second page %+v", evs[1])
	}
}

func TestHPEPower(t *testing.T) {
	p, err := ReadPower(context.Background(), ilo)
	if err != nil {
		t.Fatal(err)
	}
	if *p.ConsumedWatts != 286 || *p.CapacityWatts != 1600 || *p.AvgWatts != 281 || p.IntervalMin != 20 || len(p.Redundancy) != 1 || p.Redundancy[0].Status != model.StatusCrit {
		t.Errorf("power %+v", p)
	}
	s1, s2 := p.Supplies[0], p.Supplies[1]
	if s1.Name != "Power Supply 1" || s1.Bay != "1" || s1.Status != model.StatusOK || s1.SparePart != "866729-001" {
		t.Errorf("psu1 %+v", s1)
	}
	if s2.Name != "Power Supply 2" || s2.Status != model.StatusCrit || *s2.InputVolts != 0 || s2.Detail != "Unknown" {
		t.Errorf("psu2 %+v", s2)
	}
}

func TestHostInterface(t *testing.T) {
	lines := []string{"Protocol ID: 04 (Redfish over IP)", "IPv4 Redfish Service Address: 16.1.15.1", "Redfish Service Port: 443"}
	if a := HostInterface(lines); a != "16.1.15.1:443" {
		t.Errorf("host interface %q", a)
	}
	if a := HostInterface([]string{"Host Interface Type: Network"}); a != "" {
		t.Errorf("none: %q", a)
	}
}

// Trust on first use: the certificate is pinned, the same one is accepted,
// a different one is refused; only GET is ever sent.
func TestPinning(t *testing.T) {
	methods := map[string]bool{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods[r.Method] = true
		if u, p, _ := r.BasicAuth(); u != "tyr" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"RedfishVersion":"1.6.0"}`))
	})
	srv := httptest.NewTLSServer(h)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")
	conf := filepath.Join(t.TempDir(), "tyr", "redfish.conf")
	if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte("user = tyr\npassword = secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYR_REDFISH_USER", "")
	t.Setenv("TYR_REDFISH_PASSWORD", "")
	cfg, err := LoadConfig(conf)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Dial(context.Background(), &cfg, addr, "address")
	if err != nil || !strings.Contains(c.Note, "pinned on first use") {
		t.Fatalf("first dial: %v %+v", err, c)
	}
	b, _ := os.ReadFile(conf)
	if !strings.Contains(string(b), "pin "+addr+" = sha256:") {
		t.Fatalf("no pin written:\n%s", b)
	}
	cfg2, _ := LoadConfig(conf)
	if _, err := Dial(context.Background(), &cfg2, addr, "address"); err != nil {
		t.Fatalf("pinned dial: %v", err)
	}
	cfg2.Pins[addr] = "sha256:00"
	if _, err := Dial(context.Background(), &cfg2, addr, "address"); err == nil || !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("changed certificate accepted: %v", err)
	}
	cfg2, _ = LoadConfig(conf)
	cfg2.Password = "wrong"
	if _, err := Dial(context.Background(), &cfg2, addr, "address"); err == nil || !strings.Contains(err.Error(), "login refused") {
		t.Fatalf("bad password: %v", err)
	}
	if len(methods) != 1 || !methods["GET"] {
		t.Errorf("methods used: %v", methods)
	}
}
