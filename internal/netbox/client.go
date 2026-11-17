package netbox

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// obj is a JSON object for requests and replies.
type obj = map[string]any

// Client talks to the NetBox REST API.
type Client struct {
	base    string
	token   string
	http    *http.Client
	DryRun  bool // writes are logged, not sent
	Version string
	log     func(string, ...any)
	fakeID  int
}

// NewClient makes a client for cfg.
func NewClient(cfg Config, log func(string, ...any)) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the user asked for it
	}
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Client{base: strings.TrimRight(cfg.URL, "/"), token: cfg.Token,
		http: &http.Client{Transport: tr, Timeout: 30 * time.Second}, log: log}
}

// APIError is a reply other than 2xx.
type APIError struct {
	Method, Path string
	Code         int
	Body         string
}

func (e *APIError) Error() string {
	b := strings.TrimSpace(e.Body)
	if len(b) > 300 {
		b = b[:300] + " …"
	}
	return fmt.Sprintf("%s %s: HTTP %d %s", e.Method, e.Path, e.Code, b)
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any) (obj, error) {
	if c.DryRun && method != http.MethodGet {
		c.fakeID--
		desc := ""
		if m, ok := body.(obj); ok {
			if n, ok := m["name"].(string); ok {
				desc = " " + n
			}
		}
		c.log("  (dry run) %s %s%s", method, path, desc)
		return obj{"id": float64(c.fakeID)}, nil
	}
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.HasPrefix(c.token, "nbt_") { // NetBox 4.5 v2 tokens
		req.Header.Set("Authorization", "Bearer "+c.token)
	} else {
		req.Header.Set("Authorization", "Token "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &APIError{Method: method, Path: path, Code: resp.StatusCode, Body: string(data)}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return obj{}, nil
	}
	var out obj
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s %s: not JSON (is %s the NetBox URL?)", method, path, c.base)
	}
	return out, nil
}

// Get reads one object or a list page.
func (c *Client) Get(ctx context.Context, path string, q url.Values) (obj, error) {
	return c.do(ctx, http.MethodGet, path, q, nil)
}

// List reads all results of a list endpoint.
func (c *Client) List(ctx context.Context, path string, q url.Values) ([]obj, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("limit", "1000")
	var out []obj
	for off := 0; ; {
		q.Set("offset", strconv.Itoa(off))
		page, err := c.Get(ctx, path, q)
		if err != nil {
			return nil, err
		}
		res := list(page["results"])
		out = append(out, res...)
		if page["next"] == nil || len(res) == 0 {
			return out, nil
		}
		off += len(res)
	}
}

// First returns the first match of a list query, nil when none.
func (c *Client) First(ctx context.Context, path string, q url.Values) (obj, error) {
	q.Set("limit", "1")
	page, err := c.Get(ctx, path, q)
	if err != nil {
		return nil, err
	}
	if res := list(page["results"]); len(res) > 0 {
		return res[0], nil
	}
	return nil, nil
}

// Post creates an object.
func (c *Client) Post(ctx context.Context, path string, body any) (obj, error) {
	return c.do(ctx, http.MethodPost, path, nil, body)
}

// Patch changes an object.
func (c *Client) Patch(ctx context.Context, path string, id int, body any) (obj, error) {
	return c.do(ctx, http.MethodPatch, fmt.Sprintf("%s%d/", path, id), nil, body)
}

// Delete removes an object.
func (c *Client) Delete(ctx context.Context, path string, id int) error {
	_, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("%s%d/", path, id), nil, nil)
	return err
}

// Status reads /api/status/ (the NetBox version) and checks the token.
func (c *Client) Status(ctx context.Context) (string, error) {
	st, err := c.Get(ctx, "/api/status/", nil)
	if err != nil {
		return "", err
	}
	c.Version = str(st["netbox-version"])
	// /api/status/ answers without a valid token on some setups
	if _, err := c.Get(ctx, "/api/dcim/devices/", url.Values{"limit": {"1"}}); err != nil {
		return c.Version, err
	}
	return c.Version, nil
}

// AtLeast reports whether the NetBox version is at least major.minor.
func (c *Client) AtLeast(major, minor int) bool {
	parts := strings.SplitN(strings.TrimPrefix(c.Version, "v"), ".", 3)
	if len(parts) < 2 {
		return true // unknown: assume a current NetBox
	}
	ma, _ := strconv.Atoi(parts[0])
	mi, _ := strconv.Atoi(strings.TrimFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
	return ma > major || (ma == major && mi >= minor)
}

// --- JSON helpers ---------------------------------------------------------

func list(v any) []obj {
	a, _ := v.([]any)
	out := make([]obj, 0, len(a))
	for _, x := range a {
		if m, ok := x.(obj); ok {
			out = append(out, m)
		}
	}
	return out
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func id(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case obj:
		return id(x["id"])
	}
	return 0
}

// nested reads a.b.c from an object.
func nested(o obj, keys ...string) any {
	var v any = o
	for _, k := range keys {
		m, ok := v.(obj)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func q(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	return v
}

// Choice is an existing NetBox object offered in a picker.
type Choice struct {
	Name   string
	Detail string // slug, description, custom field type
}

// choiceKinds maps a picker to its endpoint.
var choiceKinds = map[string]string{
	"site":         "/api/dcim/sites/",
	"role":         "/api/dcim/device-roles/",
	"tenant":       "/api/tenancy/tenants/",
	"tag":          "/api/extras/tags/",
	"custom_field": "/api/extras/custom-fields/",
}

// Choices lists the existing objects of a kind (site, role, tenant, tag,
// custom_field - the custom fields of devices), sorted by name. Read only.
func (c *Client) Choices(ctx context.Context, kind string) ([]Choice, error) {
	path, ok := choiceKinds[kind]
	if !ok {
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
	all, err := c.List(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	out := make([]Choice, 0, len(all))
	for _, o := range all {
		ch := Choice{Name: str(o["name"])}
		switch kind {
		case "custom_field":
			if !forDevices(o) {
				continue
			}
			ch.Detail = str(nested(o, "type", "label"))
			if l := str(o["label"]); l != "" {
				ch.Detail = l + " · " + ch.Detail
			}
		default:
			ch.Detail = firstNonEmpty(str(o["description"]), str(o["slug"]))
		}
		if ch.Name != "" {
			out = append(out, ch)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// forDevices: a custom field that applies to dcim.device (object_types in
// NetBox 4, content_types before).
func forDevices(o obj) bool {
	for _, k := range []string{"object_types", "content_types"} {
		ts, _ := o[k].([]any)
		for _, t := range ts {
			if s, _ := t.(string); s == "dcim.device" {
				return true
			}
		}
	}
	return false
}
