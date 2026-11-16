// Package ipmi reads BMC data through ipmiutil (preferred) or ipmitool.
//
// Only read-only subcommands are ever executed; the allowlist below is the
// single place that decides what may run. Clearing the SEL is a separate,
// explicitly gated function.
package ipmi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/noahzmr/tyr/internal/execx"
)

// Tool identifies the backend.
type Tool string

const (
	IPMIUtil Tool = "ipmiutil"
	IPMITool Tool = "ipmitool"
)

// allowed lists the read-only invocations per tool (argv after the tool).
// Never add `ipmiutil power`, `ipmitool chassis power`, `... raw`, or any
// "set"/"reset" command here.
var allowed = map[Tool][][]string{
	IPMIUtil: {{"sensor", "-c"}, {"sel"}, {"sel", "-c"}, {"fru", "-c"}, {"lan", "-c"}, {"health", "-c"}, {"dcmi", "power"}},
	IPMITool: {{"sensor"}, {"sel", "info"}, {"sel", "elist"}, {"fru", "print"}, {"lan", "print"}, {"mc", "info"},
		{"dcmi", "power", "reading"}, {"sdr", "type", "Power Supply"}},
}

// ErrNotAllowed is returned for any command outside the allowlist.
var ErrNotAllowed = errors.New("ipmi: command not in read-only allowlist")

// Client runs allowlisted IPMI commands.
type Client struct {
	Tool Tool
	r    execx.Runner
}

// Find returns a client for the first installed tool (ipmiutil, then ipmitool).
func Find(r execx.Runner) (*Client, error) {
	for _, t := range []Tool{IPMIUtil, IPMITool} {
		if _, err := r.LookPath(string(t)); err == nil {
			return &Client{Tool: t, r: r}, nil
		}
	}
	return nil, errors.New("neither ipmiutil nor ipmitool installed")
}

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	ok := false
	for _, a := range allowed[c.Tool] {
		if slicesEqual(a, args) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, fmt.Errorf("%w: %s %s", ErrNotAllowed, c.Tool, strings.Join(args, " "))
	}
	return c.r.Run(ctx, string(c.Tool), args...)
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Sensors reads all sensors with their BMC status.
func (c *Client) Sensors(ctx context.Context) ([]Sensor, error) {
	switch c.Tool {
	case IPMIUtil:
		out, err := c.run(ctx, "sensor", "-c")
		if err != nil {
			return nil, err
		}
		return ParseIPMIUtilSensors(out)
	default:
		out, err := c.run(ctx, "sensor")
		if err != nil {
			return nil, err
		}
		return ParseIPMIToolSensors(out)
	}
}

// SEL reads the system event log.
func (c *Client) SEL(ctx context.Context) (*SEL, error) {
	switch c.Tool {
	case IPMIUtil:
		out, err := c.run(ctx, "sel")
		if err != nil {
			return nil, err
		}
		return ParseIPMIUtilSEL(out)
	default:
		out, err := c.run(ctx, "sel", "elist")
		if err != nil {
			return nil, err
		}
		sel, err := ParseIPMIToolSEL(out)
		if err != nil {
			return nil, err
		}
		if info, err := c.run(ctx, "sel", "info"); err == nil {
			sel.Used, sel.Capacity = parseIPMIToolSELInfo(info)
		}
		return sel, nil
	}
}

// LanPrint returns `ipmitool lan print` output. ipmiutil's lan listing uses
// a different layout and is not parsed yet.
func (c *Client) LanPrint(ctx context.Context) ([]byte, error) {
	if c.Tool != IPMITool {
		return nil, errors.New("BMC LAN config only parsed from ipmitool")
	}
	return c.run(ctx, "lan", "print")
}

// FRUs returns the BMC FRU inventory (`ipmitool fru print`).
func (c *Client) FRUs(ctx context.Context) ([]FRU, error) {
	if c.Tool != IPMITool {
		return nil, errors.New("FRU inventory only parsed from ipmitool")
	}
	b, err := c.run(ctx, "fru", "print")
	if err != nil && len(b) == 0 {
		return nil, err
	}
	return ParseFRU(b), nil // fru print exits 1 when one FRU is unreadable
}

// MCInfo returns BMC manufacturer and firmware (`ipmitool mc info`).
func (c *Client) MCInfo(ctx context.Context) (vendor, firmware string, err error) {
	if c.Tool != IPMITool {
		return "", "", errors.New("BMC info only parsed from ipmitool")
	}
	b, err := c.run(ctx, "mc", "info")
	if err != nil {
		return "", "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Manufacturer Name":
			vendor = strings.TrimSpace(v)
		case "Firmware Revision":
			firmware = strings.TrimSpace(v)
		}
	}
	return vendor, firmware, nil
}
