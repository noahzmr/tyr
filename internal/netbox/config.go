// Package netbox writes what Tyr knows about a machine into NetBox: the
// device (found by serial number, created when missing), its interfaces
// and MAC addresses, the management IP, an inventory item per component,
// tags from rules, and a journal entry with the test result.
//
// Two files configure it, both JSON and both editable in the TUI (N):
// netbox.json (connection and what to write) and tags.json (which tag
// when). The token is read from the file or TYR_NETBOX_TOKEN and is never
// written to snapshots, results or logs.
package netbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SystemDir holds the configuration for root (and the live ISO).
const SystemDir = "/etc/tyr"

// Config is netbox.json.
type Config struct {
	URL      string `json:"url"`             // https://netbox.example.com (without /api)
	Token    string `json:"token,omitempty"` // API token; TYR_NETBOX_TOKEN overrides
	Insecure bool   `json:"insecure,omitempty"`

	// For devices that are not in NetBox yet (found by serial number).
	Site   string `json:"site,omitempty"`   // created if missing
	Role   string `json:"role,omitempty"`   // created if missing
	Tenant string `json:"tenant,omitempty"` // must exist
	Name   string `json:"name,omitempty"`   // "hostname" or "serial"
	Status string `json:"status,omitempty"` // status of a new device

	// What to write.
	Interfaces bool `json:"interfaces"` // NICs, MACs, BMC interface and IP
	Inventory  bool `json:"inventory"`  // an inventory item per component
	MarkFailed bool `json:"mark_failed"`
	Journal    bool `json:"journal"`
	Prune      bool `json:"prune"` // delete inventory items Tyr wrote that are gone
	// Categories of the inventory written as inventory items.
	Categories []string `json:"inventory_categories,omitempty"`
	// CustomFields sets device custom fields; values may name facts in
	// braces: {"memory": "{memory_gb} GB"}.
	CustomFields map[string]string `json:"custom_fields,omitempty"`

	// AfterAccept writes the result after every `tyr accept`.
	AfterAccept bool `json:"after_accept"`

	Path string `json:"-"` // the file it was read from (or will be saved to)
}

// DefaultCategories are the inventory categories written as items; the
// machine itself (system, chassis) is the device, volumes are not parts.
var DefaultCategories = []string{"board", "bmc", "cpu", "dimm", "disk", "raid_controller", "raid_drive",
	"nic", "transceiver", "gpu", "npu", "psu", "fru", "device"}

// Defaults is a config before the file is applied.
func Defaults() Config {
	return Config{Role: "Server", Name: "hostname", Status: "active",
		Interfaces: true, Inventory: true, MarkFailed: true, Journal: true}
}

// Enabled reports whether NetBox is configured at all.
func (c Config) Enabled() bool { return c.URL != "" && c.Token != "" }

// Validate checks the settings that would fail every request.
func (c Config) Validate() error {
	if c.URL == "" {
		return errors.New("no NetBox URL")
	}
	if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
		return fmt.Errorf("NetBox URL %q: must start with https:// or http://", c.URL)
	}
	if c.Token == "" {
		return errors.New("no NetBox token (token in the file or TYR_NETBOX_TOKEN)")
	}
	switch c.Name {
	case "", "hostname", "serial":
	default:
		return fmt.Errorf("name %q: hostname or serial", c.Name)
	}
	switch c.Status {
	case "", "active", "planned", "staged", "offline", "inventory", "failed", "decommissioning":
	default:
		return fmt.Errorf("status %q: active, planned, staged, offline, inventory, failed or decommissioning", c.Status)
	}
	return nil
}

// TagRule sets Tag on the device while When is true; the tag comes off
// again when it is false.
type TagRule struct {
	Tag  string `json:"tag"`
	When string `json:"when"`
}

// Tags is tags.json. No rules: Tyr sets no tags.
type Tags struct {
	Rules []TagRule `json:"rules"`
	Path  string    `json:"-"`
}

// Validate parses every rule against the known facts.
func (t Tags) Validate() error {
	seen := map[string]bool{}
	for i, r := range t.Rules {
		if strings.TrimSpace(r.Tag) == "" {
			return fmt.Errorf("rule %d: no tag", i+1)
		}
		if slugify(r.Tag) == "" {
			return fmt.Errorf("rule %d: tag %q has no letters or digits", i+1, r.Tag)
		}
		if seen[slugify(r.Tag)] {
			return fmt.Errorf("rule %d: tag %q twice (combine the conditions with ||)", i+1, r.Tag)
		}
		seen[slugify(r.Tag)] = true
		if _, err := Parse(r.When); err != nil {
			return fmt.Errorf("tag %s: %w", r.Tag, err)
		}
	}
	return nil
}

// Paths returns where the two files are read from: the user's own
// (~/.config/tyr) when it exists and Tyr does not run as root, else
// /etc/tyr. dir overrides both.
func Paths(dir string) (netbox, tags string) {
	if dir != "" {
		return filepath.Join(dir, "netbox.json"), filepath.Join(dir, "tags.json")
	}
	pick := func(name string) string {
		if os.Geteuid() != 0 {
			if u := userDir(); u != "" {
				p := filepath.Join(u, name)
				if _, err := os.Stat(p); err == nil {
					return p
				}
				if !writable(SystemDir) {
					if _, err := os.Stat(filepath.Join(SystemDir, name)); err != nil {
						return p // nothing system-wide to read: save to the user's
					}
				}
			}
		}
		return filepath.Join(SystemDir, name)
	}
	return pick("netbox.json"), pick("tags.json")
}

func userDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "tyr")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "tyr")
	}
	return ""
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".tyr-w-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// LoadConfig reads netbox.json onto the defaults (a missing file is not
// an error) and applies TYR_NETBOX_URL and TYR_NETBOX_TOKEN.
func LoadConfig(path string) (Config, error) {
	c := Defaults()
	c.Path = path
	if err := readJSON(path, &c); err != nil {
		return c, err
	}
	c.Path = path
	if v := os.Getenv("TYR_NETBOX_URL"); v != "" {
		c.URL = v
	}
	if v := os.Getenv("TYR_NETBOX_TOKEN"); v != "" {
		c.Token = v
	}
	c.URL = strings.TrimRight(strings.TrimSuffix(strings.TrimRight(c.URL, "/"), "/api"), "/")
	return c, nil
}

// LoadTags reads tags.json (missing: no rules).
func LoadTags(path string) (Tags, error) {
	t := Tags{Path: path}
	if err := readJSON(path, &t); err != nil {
		return t, err
	}
	t.Path = path
	return t, t.Validate()
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // a typo must not silently drop a setting
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Save writes the config (0600: it holds the token).
func (c Config) Save() error {
	if err := c.Validate(); err != nil && c.URL != "" {
		return err
	}
	return writeJSON(c.Path, c)
}

// Save writes the rules.
func (t Tags) Save() error {
	if err := t.Validate(); err != nil {
		return err
	}
	if t.Rules == nil {
		t.Rules = []TagRule{}
	}
	return writeJSON(t.Path, t)
}

func writeJSON(path string, v any) error {
	if path == "" {
		return errors.New("no file to save to")
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// slugify makes a NetBox slug: lower case letters, digits, - and _.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 100 {
		out = strings.TrimRight(out[:100], "-")
	}
	return out
}
