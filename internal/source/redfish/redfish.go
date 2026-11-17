// Package redfish reads the BMC's Redfish service: event logs (HPE IML,
// iLO event log, SEL), power (supplies, draw, cap), the drives behind RAID
// controllers (HPE Smart Storage, Redfish Storage), the device inventory
// and the firmware inventory. Read-only: the client only issues GET
// requests.
//
// Access: the BMC's host interface (SMBIOS type 42, HPE iLO "Virtual
// NIC") when it answers, else an address from --redfish, TYR_REDFISH or
// the config file. Credentials come from TYR_REDFISH_USER /
// TYR_REDFISH_PASSWORD or the root-only config file and are never written
// to snapshots. The BMC's certificate is accepted on first use and its
// SHA-256 fingerprint pinned in the config file; a changed certificate is
// refused.
package redfish

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultConfig is the config file: credentials, address and pins.
const DefaultConfig = "/etc/tyr/redfish.conf"

// Getter reads one Redfish resource ("/redfish/v1/Chassis/1/Power").
type Getter interface {
	Get(ctx context.Context, path string) ([]byte, error)
}

// Config is the config file plus environment overrides.
type Config struct {
	Path     string
	Host     string // host[:port] when the host interface is not used
	User     string
	Password string
	Pins     map[string]string // host:port -> "sha256:ab12…"
}

// LoadConfig reads the config file (missing is fine) and applies
// TYR_REDFISH, TYR_REDFISH_USER and TYR_REDFISH_PASSWORD.
//
//	host = 10.16.5.200
//	user = tyr-read
//	password = …
//	pin 16.1.15.1:443 = sha256:…
func LoadConfig(path string) (Config, error) {
	c := Config{Path: path, Pins: map[string]string{}}
	f, err := os.Open(path)
	switch {
	case err == nil:
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			switch {
			case k == "host":
				c.Host = v
			case k == "user":
				c.User = v
			case k == "password":
				c.Password = v
			case strings.HasPrefix(k, "pin "):
				c.Pins[strings.TrimSpace(strings.TrimPrefix(k, "pin "))] = v
			}
		}
	case !errors.Is(err, os.ErrNotExist):
		return c, err
	}
	if v := os.Getenv("TYR_REDFISH"); v != "" {
		c.Host = v
	}
	if v := os.Getenv("TYR_REDFISH_USER"); v != "" {
		c.User = v
	}
	if v := os.Getenv("TYR_REDFISH_PASSWORD"); v != "" {
		c.Password = v
	}
	return c, nil
}

// savePin adds "pin host = fp" to the config file (root-only, 0600).
func (c *Config) savePin(host, fp string) error {
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(c.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "pin %s = %s\n", host, fp)
	if err == nil {
		c.Pins[host] = fp
	}
	return err
}

// Fingerprint is "sha256:" and the hex SHA-256 of a DER certificate.
func Fingerprint(der []byte) string {
	s := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(s[:])
}

// Client talks to one Redfish service.
type Client struct {
	Addr string // host:port
	Via  string // "host interface" or "address"
	Note string // e.g. certificate pinned on first use
	user string
	pass string
	hc   *http.Client
}

// ErrPinMismatch: the BMC presented a different certificate.
var ErrPinMismatch = errors.New("the BMC's certificate changed since it was pinned")

// Dial connects to addr, checks the certificate against the pin (pinning
// it on first use) and reads the service root.
func Dial(ctx context.Context, cfg *Config, addr, via string) (*Client, error) {
	if !strings.Contains(addr, ":") || strings.Count(addr, ":") > 1 && !strings.HasPrefix(addr, "[") {
		addr = net.JoinHostPort(addr, "443")
	}
	c := &Client{Addr: addr, Via: via, user: cfg.User, pass: cfg.Password}
	var seen string
	var mu sync.Mutex
	verify := func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("no certificate")
		}
		fp := Fingerprint(cs.PeerCertificates[0].Raw)
		mu.Lock()
		seen = fp
		mu.Unlock()
		if want, ok := cfg.Pins[addr]; ok && !strings.EqualFold(want, fp) {
			return fmt.Errorf("%w (pinned %s, got %s; remove the pin line in %s if the BMC got a new certificate)", ErrPinMismatch, want, fp, cfg.Path)
		}
		return nil
	}
	c.hc = &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			// verification is the pin check above (BMCs use self-signed certificates)
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true, VerifyConnection: verify, MinVersion: tls.VersionTLS12}, //nolint:gosec
			DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			MaxIdleConnsPerHost:   2,
		},
	}
	if _, err := c.Get(ctx, "/redfish/v1/"); err != nil {
		return nil, err
	}
	if _, pinned := cfg.Pins[addr]; !pinned && seen != "" {
		if err := cfg.savePin(addr, seen); err != nil {
			c.Note = "certificate " + seen[:23] + "… not pinned (" + err.Error() + ")"
		} else {
			c.Note = "certificate pinned on first use in " + cfg.Path
		}
	}
	return c, nil
}

// Get issues one GET; Redfish is only ever read.
func (c *Client) Get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+c.Addr+path, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OData-Version", "4.0")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%s: login refused (%s): check the user and password", path, resp.Status)
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s: %w", path, os.ErrNotExist)
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("%s: %s", path, resp.Status)
	}
	return b, nil
}

// FileGetter serves a recorded Redfish tree (fixtures): path + ".json"
// under Root ("/redfish/v1/" → Root/redfish/v1/index.json).
type FileGetter struct{ Root string }

func (f FileGetter) Get(_ context.Context, path string) ([]byte, error) {
	p := strings.TrimSuffix(path, "/")
	if strings.HasSuffix(path, "/") {
		p += "/index"
	}
	return os.ReadFile(filepath.Join(f.Root, filepath.FromSlash(p)+".json"))
}

// HostInterface is the Redfish service address SMBIOS type 42 announces
// (HPE iLO Virtual NIC: 16.1.15.1:443); "" when there is none.
func HostInterface(lines []string) string {
	var ip, port string
	for _, l := range lines {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch {
		case k == "IPv4 Redfish Service Address" || k == "Redfish Service IP Address" || k == "IPv6 Redfish Service Address":
			if ip == "" && v != "" && v != "0.0.0.0" {
				ip = v
			}
		case k == "Redfish Service Port":
			port = v
		}
	}
	if ip == "" {
		return ""
	}
	if port == "" || port == "0" {
		port = "443"
	}
	return net.JoinHostPort(ip, port)
}
