package netbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noahzmr/tyr/internal/accept"
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/collector/all"
	"github.com/noahzmr/tyr/internal/fixture"
	"github.com/noahzmr/tyr/internal/model"
)

// fakeNetBox is an in-memory NetBox: list endpoints with equality filters,
// create, change, delete. Related objects are returned nested ({id, name,
// slug}), choice fields as {value}, like NetBox does.
type fakeNetBox struct {
	mu      sync.Mutex
	version string
	objs    map[string]map[int]obj // endpoint -> id -> object
	next    int
	writes  []string
	token   string
}

func newFake(version string) *fakeNetBox {
	return &fakeNetBox{version: version, objs: map[string]map[int]obj{}, next: 100, token: "secret"}
}

var related = map[string]string{"manufacturer": "dcim/manufacturers", "device_type": "dcim/device-types",
	"role": "", "site": "dcim/sites", "tenant": "tenancy/tenants", "device": "dcim/devices",
	"primary_mac_address": "dcim/mac-addresses", "oob_ip": "ipam/ip-addresses"}

func (f *fakeNetBox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Token "+f.token {
		http.Error(w, `{"detail":"Invalid token"}`, 403)
		return
	}
	p := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	if p == "status" {
		json.NewEncoder(w).Encode(obj{"netbox-version": f.version})
		return
	}
	parts := strings.Split(p, "/")
	ep := parts[0] + "/" + parts[1]
	if f.objs[ep] == nil {
		f.objs[ep] = map[int]obj{}
	}
	oid := 0
	if len(parts) > 2 {
		oid, _ = strconv.Atoi(parts[2])
	}
	switch r.Method {
	case http.MethodGet:
		var res []any
		for i := 0; i < f.next+1; i++ { // id order
			o, ok := f.objs[ep][i]
			if !ok || !f.match(o, r.URL.Query()) {
				continue
			}
			res = append(res, f.render(ep, o))
		}
		lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		total := len(res)
		if lim > 0 && len(res) > lim {
			res = res[:lim]
		}
		if res == nil {
			res = []any{}
		}
		json.NewEncoder(w).Encode(obj{"count": total, "next": nil, "results": res})
	case http.MethodPost, http.MethodPatch:
		var body obj
		json.NewDecoder(r.Body).Decode(&body)
		if r.Method == http.MethodPost {
			if ep == "dcim/devices" {
				for _, o := range f.objs[ep] {
					if o["name"] == body["name"] {
						http.Error(w, `{"name":["Device name must be unique per site."]}`, 400)
						return
					}
				}
			}
			f.next++
			body["id"] = float64(f.next)
			f.objs[ep][f.next] = body
			f.writes = append(f.writes, fmt.Sprintf("POST %s %v", ep, firstNonEmpty(str(body["name"]), str(body["mac_address"]), str(body["address"]), str(body["kind"]))))
			json.NewEncoder(w).Encode(f.render(ep, body))
			return
		}
		o := f.objs[ep][oid]
		if o == nil {
			http.Error(w, "not found", 404)
			return
		}
		keys := make([]string, 0, len(body))
		for k, v := range body {
			o[k] = v
			keys = append(keys, k)
		}
		f.writes = append(f.writes, fmt.Sprintf("PATCH %s %d %s", ep, oid, strings.Join(sortStrings(keys), ",")))
		json.NewEncoder(w).Encode(f.render(ep, o))
	case http.MethodDelete:
		delete(f.objs[ep], oid)
		f.writes = append(f.writes, fmt.Sprintf("DELETE %s %d", ep, oid))
		w.WriteHeader(204)
	}
}

func sortStrings(s []string) []string {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
	return s
}

func (f *fakeNetBox) match(o obj, qs map[string][]string) bool {
	for k, vs := range qs {
		if k == "limit" || k == "offset" {
			continue
		}
		key := strings.TrimSuffix(k, "_id")
		v := o[k]
		if key != k {
			v = o[key]
			if key == "assigned_object" {
				v = o["assigned_object_id"]
			}
		}
		got := fmt.Sprint(v)
		if fv, ok := v.(float64); ok {
			got = strconv.Itoa(int(fv))
		}
		if k == "address" {
			got, _, _ = strings.Cut(got, "/")
		}
		if !strings.EqualFold(got, vs[0]) {
			return false
		}
	}
	return true
}

func (f *fakeNetBox) render(ep string, o obj) obj {
	out := obj{}
	for k, v := range o {
		out[k] = v
	}
	for k := range related {
		if n, ok := o[k].(float64); ok {
			out[k] = obj{"id": n}
			if k == "primary_mac_address" {
				out[k] = obj{"id": n, "mac_address": f.objs["dcim/mac-addresses"][int(n)]["mac_address"]}
			}
		}
	}
	if n, ok := o["role"].(float64); ok {
		roles := "dcim/device-roles"
		if ep == "dcim/inventory-items" {
			roles = "dcim/inventory-item-roles"
		}
		out["role"] = obj{"id": n, "name": f.objs[roles][int(n)]["name"]}
	}
	if s, ok := o["status"].(string); ok {
		out["status"] = obj{"value": s}
	}
	if ts, ok := o["tags"].([]any); ok {
		var tags []any
		for _, t := range ts {
			n := t.(float64)
			tags = append(tags, obj{"id": n, "slug": f.objs["extras/tags"][int(n)]["slug"]})
		}
		out["tags"] = tags
	}
	return out
}

func (f *fakeNetBox) all(ep string) []obj {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []obj
	for i := 0; i <= f.next; i++ {
		if o, ok := f.objs[ep][i]; ok {
			out = append(out, o)
		}
	}
	return out
}

func fixtureSnap(t *testing.T, name string) *model.Snapshot {
	t.Helper()
	env, _, err := fixture.Load("../../testdata/fixtures/"+name, collector.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s := collector.Collect(ctx, env, all.Collectors(), nil)
	s.Inventory = model.BuildInventory(s)
	return s
}

func TestSync(t *testing.T) {
	snap := fixtureSnap(t, "server-healthy")
	if snap.System == nil || snap.System.Serial == "" {
		t.Fatal("fixture without serial number")
	}
	fake := newFake("4.3.2")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := Defaults()
	cfg.URL, cfg.Token, cfg.Site, cfg.Role = srv.URL, "secret", "Lab", "Server"
	cfg.CustomFields = map[string]string{"memory": "{memory_gb} GB"}
	tags := Tags{Rules: []TagRule{{"Big Memory", "memory_gb >= 1"}, {"gpu", "gpus > 100"}, {"tested", `verdict == "PASS"`}}}
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	ctx := context.Background()

	// 1: a new device
	rep, err := Sync(ctx, cfg, tags, Input{Snap: snap}, false, logf)
	if err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(logs, "\n"))
	}
	if !rep.Created || rep.DeviceID == 0 || len(rep.Problems) > 0 {
		t.Fatalf("report %+v\n%s", rep, strings.Join(logs, "\n"))
	}
	devs := fake.all("dcim/devices")
	if len(devs) != 1 || devs[0]["serial"] != snap.System.Serial || devs[0]["role"] == nil {
		t.Fatalf("devices %v", devs)
	}
	if cf := devs[0]["custom_fields"].(obj); !strings.HasSuffix(str(cf["memory"]), " GB") {
		t.Errorf("custom fields %v", cf)
	}
	if got := strings.Join(rep.Tags, ","); got != "big-memory" {
		t.Errorf("tags %q, want big-memory", got)
	}
	items := fake.all("dcim/inventory-items")
	if len(items) < 5 {
		t.Fatalf("only %d inventory items", len(items))
	}
	names := map[string]bool{}
	for _, it := range items {
		if names[str(it["name"])] {
			t.Errorf("duplicate item name %s", it["name"])
		}
		names[str(it["name"])] = true
		if it["discovered"] != true || it["role"] == nil {
			t.Errorf("item %v", it)
		}
	}
	ifs := fake.all("dcim/interfaces")
	if len(ifs) == 0 || len(fake.all("dcim/mac-addresses")) == 0 {
		t.Errorf("interfaces %d, MACs %d", len(ifs), len(fake.all("dcim/mac-addresses")))
	}
	if j := fake.all("extras/journal-entries"); len(j) != 1 || !strings.Contains(str(j[0]["comments"]), "Tyr health check") {
		t.Errorf("journal %v", j)
	}

	// 2: again, nothing changed: only a new journal entry
	fake.writes = nil
	logs = nil
	if _, err := Sync(ctx, cfg, tags, Input{Snap: snap}, false, logf); err != nil {
		t.Fatal(err)
	}
	for _, w := range fake.writes {
		if !strings.HasPrefix(w, "POST extras/journal-entries") {
			t.Errorf("second run wrote %s", w)
		}
	}

	// 3: an acceptance test that failed: device failed, tag "tested" stays off, journal danger
	r := &accept.Result{Verdict: accept.Fail, Profile: accept.Profile{Name: "quick"}, Failures: []string{"DIMM P1-A1: uncorrectable error"},
		StartedAt: time.Now().Add(-time.Hour), EndedAt: time.Now()}
	fake.writes = nil
	rep, err = Sync(ctx, cfg, tags, Input{Snap: snap, Result: r, ResultDir: "/mnt/results/tyr/x"}, false, logf)
	if err != nil {
		t.Fatal(err)
	}
	dev := fake.all("dcim/devices")[0]
	if dev["status"] != "failed" {
		t.Errorf("status %v, want failed", dev["status"])
	}
	j := fake.all("extras/journal-entries")
	last := j[len(j)-1]
	if last["kind"] != "danger" || !strings.Contains(str(last["comments"]), "uncorrectable") || strings.Contains(str(last["comments"]), "](runs/") {
		t.Errorf("journal %v", last)
	}

	// 4: passed: back to active, tag tested set; another rule's tag stays
	r.Verdict, r.Failures = accept.Pass, nil
	if _, err = Sync(ctx, cfg, tags, Input{Snap: snap, Result: r}, false, logf); err != nil {
		t.Fatal(err)
	}
	dev = fake.all("dcim/devices")[0]
	if dev["status"] != "active" {
		t.Errorf("status %v, want active", dev["status"])
	}
	var slugs []string
	for _, tid := range dev["tags"].([]any) {
		slugs = append(slugs, str(fake.objs["extras/tags"][int(tid.(float64))]["slug"]))
	}
	if got := strings.Join(sortStrings(slugs), ","); got != "big-memory,tested" {
		t.Errorf("tags %s", got)
	}

	// 5: an operator's own tag is kept; a rule removed from the file no longer manages its tag
	fake.mu.Lock()
	fake.next++
	fake.objs["extras/tags"][fake.next] = obj{"id": float64(fake.next), "name": "rack-7", "slug": "rack-7"}
	dev["tags"] = append(dev["tags"].([]any), float64(fake.next))
	fake.mu.Unlock()
	tags.Rules[0].When = "memory_gb > 100000" // big-memory comes off
	if rep, err = Sync(ctx, cfg, tags, Input{Snap: snap, Result: r}, false, logf); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rep.Tags, ","); got != "rack-7,tested" {
		t.Errorf("tags %s, want rack-7,tested", got)
	}
}

func TestSyncDryRunAndErrors(t *testing.T) {
	snap := fixtureSnap(t, "server-healthy")
	fake := newFake("4.1.0")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := Defaults()
	cfg.URL, cfg.Token = srv.URL, "secret"
	ctx := context.Background()

	// not in NetBox, no site/role: nothing written
	if _, err := Sync(ctx, cfg, Tags{}, Input{Snap: snap}, false, nil); err == nil || !strings.Contains(err.Error(), "site and role") {
		t.Errorf("err %v", err)
	}
	// dry run: no writes at all
	cfg.Site, cfg.Role = "Lab", "Server"
	rep, err := Sync(ctx, cfg, Tags{Rules: []TagRule{{"x", "cores > 0"}}}, Input{Snap: snap}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.writes) != 0 {
		t.Errorf("dry run wrote %v", fake.writes)
	}
	// a new device: no lookups by its placeholder id, the whole plan
	if len(rep.Problems) > 0 || rep.DeviceURL != "" {
		t.Errorf("dry run of a new device: problems %v, url %q", rep.Problems, rep.DeviceURL)
	}
	kinds := map[string]int{}
	for _, c := range rep.Changes {
		kinds[c.Object+" "+c.Action]++
	}
	if kinds["device create"] != 1 || kinds["interface create"] == 0 || kinds["inventory item create"] < 5 || kinds["tag set"] != 1 || kinds["journal entry create"] != 1 {
		t.Errorf("plan %v", kinds)
	}
	if !strings.HasPrefix(rep.Summary(), "create ") {
		t.Errorf("summary %q", rep.Summary())
	}
	// the live ISO: no serial, host name tyr-live - never matched by name
	live := *snap
	ys := *snap.System
	ys.Serial = "To Be Filled By O.E.M."
	live.System, live.Hostname = &ys, "tyr-live"
	if _, err := Sync(ctx, cfg, Tags{}, Input{Snap: &live}, true, nil); err == nil || !strings.Contains(err.Error(), "cannot tell this machine apart") {
		t.Errorf("live ISO without serial: %v", err)
	}
	// wrong token
	cfg.Token = "wrong"
	if _, err := Sync(ctx, cfg, Tags{}, Input{Snap: snap}, false, nil); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err %v", err)
	}
	// NetBox before 4.2: the MAC is a field of the interface
	cfg.Token = "secret"
	if _, err := Sync(ctx, cfg, Tags{}, Input{Snap: snap}, false, nil); err != nil {
		t.Fatal(err)
	}
	if len(fake.all("dcim/mac-addresses")) != 0 {
		t.Error("MAC objects on NetBox 4.1")
	}
	withMAC := 0
	for _, i := range fake.all("dcim/interfaces") {
		if str(i["mac_address"]) != "" {
			withMAC++
		}
	}
	if withMAC == 0 {
		t.Error("no interface has a MAC")
	}
}

func TestFacts(t *testing.T) {
	snap := fixtureSnap(t, "server-degraded")
	f := ComputeFacts(snap, nil)
	for _, d := range FactDefs {
		if _, ok := f[d.Name]; !ok {
			t.Errorf("fact %s missing", d.Name)
		}
		if f[d.Name].K != d.Kind {
			t.Errorf("fact %s is %s, defined as %s", d.Name, f[d.Name].K, d.Kind)
		}
	}
	if f["cores"].N == 0 || f["memory_gb"].N == 0 || f["serial"].S == "" || f["status"].S != "CRIT" || f["critical"].N == 0 || f["findings"].S == "" {
		t.Errorf("facts %v", f)
	}
	if f["verdict"].S != "" {
		t.Error("verdict without a test")
	}
	r := &accept.Result{Verdict: accept.Remarks, Profile: accept.Profile{Name: "burnin"}}
	if f := ComputeFacts(snap, r); f["verdict"].S != "REMARKS" || f["profile"].S != "burnin" {
		t.Errorf("verdict %v profile %v", f["verdict"], f["profile"])
	}
}

func TestConfigFiles(t *testing.T) {
	dir := t.TempDir()
	np, tp := Paths(dir)
	c, err := LoadConfig(np)
	if err != nil || c.Enabled() || !c.Inventory || c.Role != "Server" {
		t.Fatalf("defaults %+v %v", c, err)
	}
	c.URL, c.Token, c.Site = "https://netbox.example.com/api/", "tok", "Lab"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYR_NETBOX_TOKEN", "")
	c2, err := LoadConfig(np)
	if err != nil || c2.URL != "https://netbox.example.com" || c2.Token != "tok" || c2.Site != "Lab" {
		t.Fatalf("reloaded %+v %v", c2, err)
	}
	t.Setenv("TYR_NETBOX_TOKEN", "env")
	if c3, _ := LoadConfig(np); c3.Token != "env" {
		t.Error("TYR_NETBOX_TOKEN not applied")
	}
	tg := Tags{Path: tp, Rules: []TagRule{{"hw", "cores >= 16"}}}
	if err := tg.Save(); err != nil {
		t.Fatal(err)
	}
	if tg2, err := LoadTags(tp); err != nil || len(tg2.Rules) != 1 {
		t.Fatalf("%+v %v", tg2, err)
	}
	if err := (Tags{Path: tp, Rules: []TagRule{{"hw", "cores >="}}}).Save(); err == nil {
		t.Error("saved a broken rule")
	}
	if err := writeJSON(np, obj{"url": "x", "tokn": "typo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(np); err == nil {
		t.Error("unknown field accepted")
	}
	if tg3, err := LoadTags(dir + "/missing.json"); err != nil || len(tg3.Rules) != 0 {
		t.Error("missing tags file is not empty rules")
	}
}

func TestChoices(t *testing.T) {
	fake := newFake("4.3.2")
	fake.objs["dcim/sites"] = map[int]obj{1: {"id": 1.0, "name": "Lab", "slug": "lab"}, 2: {"id": 2.0, "name": "DC Frankfurt", "slug": "dc-fra", "description": "main"}}
	fake.objs["extras/custom-fields"] = map[int]obj{
		3: {"id": 3.0, "name": "memory", "label": "Memory", "type": obj{"label": "Text"}, "object_types": []any{"dcim.device"}},
		4: {"id": 4.0, "name": "vlan_note", "object_types": []any{"ipam.vlan"}},
		5: {"id": 5.0, "name": "old_style", "content_types": []any{"dcim.device", "dcim.site"}},
	}
	fake.next = 10
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := NewClient(Config{URL: srv.URL, Token: "secret"}, nil)
	sites, err := c.Choices(context.Background(), "site")
	if err != nil || len(sites) != 2 || sites[0].Name != "DC Frankfurt" || sites[0].Detail != "main" {
		t.Errorf("sites %+v %v", sites, err)
	}
	cfs, err := c.Choices(context.Background(), "custom_field")
	if err != nil || len(cfs) != 2 || cfs[0].Name != "memory" || cfs[0].Detail != "Memory · Text" || cfs[1].Name != "old_style" {
		t.Errorf("custom fields %+v %v", cfs, err)
	}
	if _, err := c.Choices(context.Background(), "nope"); err == nil {
		t.Error("unknown kind accepted")
	}
}

func TestSyncPlatform(t *testing.T) {
	snap := fixtureSnap(t, "server-healthy")
	if snap.System.OS != "Ubuntu 24.04.3 LTS" {
		t.Fatalf("fixture OS %q", snap.System.OS)
	}
	fake := newFake("4.4.0")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := Defaults()
	cfg.URL, cfg.Token, cfg.Site, cfg.Role = srv.URL, "secret", "Lab", "Server"
	ctx := context.Background()
	platform := func() string {
		dev := fake.all("dcim/devices")[0]
		if dev["platform"] == nil {
			return ""
		}
		return str(fake.objs["dcim/platforms"][id(dev["platform"])]["name"])
	}
	if _, err := Sync(ctx, cfg, Tags{}, Input{Snap: snap}, false, nil); err != nil {
		t.Fatal(err)
	}
	if got := platform(); got != "Ubuntu 24.04.3 LTS" {
		t.Errorf("platform %q", got)
	}
	if n := len(fake.all("dcim/platforms")); n != 1 {
		t.Errorf("%d platforms", n)
	}
	// with the kernel
	cfg.Platform = "{os} · {kernel}"
	Sync(ctx, cfg, Tags{}, Input{Snap: snap}, false, nil)
	if got := platform(); got != "Ubuntu 24.04.3 LTS · 6.8.0-49-generic" {
		t.Errorf("platform with kernel %q", got)
	}
	// from the live ISO: left as it is
	live := *snap
	ys := *snap.System
	ys.OS, ys.LiveSystem = "Debian GNU/Linux 13 (trixie)", true
	live.System = &ys
	cfg.Platform = "{os}"
	var logs []string
	Sync(ctx, cfg, Tags{}, Input{Snap: &live}, false, func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	if got := platform(); got != "Ubuntu 24.04.3 LTS · 6.8.0-49-generic" {
		t.Errorf("live system changed the platform to %q", got)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "live system") {
		t.Error("no note about the live system")
	}
	// off
	cfg.Platform = ""
	before := len(fake.writes)
	Sync(ctx, cfg, Tags{}, Input{Snap: snap}, false, nil)
	for _, w := range fake.writes[before:] {
		if strings.Contains(w, "platform") {
			t.Errorf("platform written with platform off: %s", w)
		}
	}
	f := ComputeFacts(snap, nil)
	if f["os_id"].S != "ubuntu" || f["os_version"].S != "24.04" || f["kernel"].S != "6.8.0-49-generic" || f["live_system"].B {
		t.Errorf("facts %v %v %v %v", f["os_id"], f["os_version"], f["kernel"], f["live_system"])
	}
}
