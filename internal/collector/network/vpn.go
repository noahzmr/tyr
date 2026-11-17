package network

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/netinfo"
)

const (
	vpnJournalLines = 20
	vpnKernelLines  = 20
	// A WireGuard session lives 180 s after the last handshake; with a
	// keepalive the peers handshake every 2 minutes, so older means the
	// tunnel is not up.
	wgSessionAge = 180 * time.Second
)

// vpnServices are the syslog identifiers of the software behind a VPN
// type; generic ones (network managers) only count when the message names
// the interface.
var vpnServices = map[string][]string{
	"WireGuard":   {"wg-quick"},
	"Tailscale":   {"tailscaled"},
	"ZeroTier":    {"zerotier-one"},
	"Nebula":      {"nebula"},
	"OpenVPN/TUN": {"openvpn", "nm-openvpn"},
	"TAP":         {"openvpn", "nm-openvpn"},
	"IPsec":       {"charon", "charon-systemd", "ipsec", "pluto", "strongswan"},
	"PPP":         {"pppd"},
}

var genericServices = []string{"NetworkManager", "systemd-networkd"}

// vpnDetails fills peers, the service journal and kernel lines of every
// VPN interface.
func (c *Collector) vpnDetails(ctx context.Context, env *collector.Env, out *model.Network, h *model.Header) {
	var vpns []*model.NIC
	wireguard := false
	for i := range out.Interfaces {
		if n := &out.Interfaces[i]; n.Kind == "vpn" && n.VPN != nil {
			vpns = append(vpns, n)
			wireguard = wireguard || n.VPN.Type == "WireGuard"
		}
	}
	if len(vpns) == 0 {
		return
	}
	// WireGuard peers: wg needs CAP_NET_ADMIN
	var wg map[string]*netinfo.WGIface
	wgNote := ""
	if wireguard {
		switch _, err := env.Exec.LookPath("wg"); {
		case err != nil:
			wgNote = "peer status needs wg (wireguard-tools)"
		default:
			b, err := env.Exec.Run(ctx, "wg", "show", "all", "dump")
			if err != nil {
				wgNote = "peer status needs root (wg show)"
				if len(b) > 0 {
					wgNote += ": " + firstLine(string(b))
				}
			} else {
				wg = netinfo.ParseWGDump(b)
				h.SetSource("wg", model.AvailOK, "")
			}
		}
	}
	klog, _ := env.KernelLog(ctx)
	_, noJournal := env.Exec.LookPath("journalctl")
	for _, n := range vpns {
		v := n.VPN
		if v.Type == "WireGuard" {
			if w := wg[n.Name]; w != nil {
				v.PublicKey, v.ListenPort, v.Peers = w.PublicKey, w.ListenPort, w.Peers
			} else if wgNote != "" {
				v.Note = wgNote
			}
		}
		if noJournal == nil {
			v.Services, v.Journal = vpnJournal(ctx, env, n.Name, v.Type)
		}
		if klog != nil {
			re := regexp.MustCompile(`(?:^|[^\w])` + regexp.QuoteMeta(n.Name) + `(?:[^\w]|$)`)
			for _, e := range klog.Recent {
				msg := strings.TrimSpace(e.Message)
				if !re.MatchString(msg) && !(v.Type == "WireGuard" && strings.HasPrefix(msg, "wireguard:")) {
					continue
				}
				if !e.Time.IsZero() {
					msg = e.Time.UTC().Format("2006-01-02 15:04:05") + " " + msg
				}
				v.KernelLines = append(v.KernelLines, msg)
			}
			if len(v.KernelLines) > vpnKernelLines {
				v.KernelLines = v.KernelLines[len(v.KernelLines)-vpnKernelLines:]
			}
		}
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// vpnJournal reads the last 24 hours of the VPN's services and of the
// network managers, the latter only where a message names the interface.
func vpnJournal(ctx context.Context, env *collector.Env, name, typ string) ([]string, []model.VPNLogLine) {
	own := vpnServices[typ]
	args := []string{"-o", "json", "--no-pager", "-q", "--since=-24h", "-n", "2000"}
	ids := append(append([]string{}, own...), genericServices...)
	for i, id := range ids {
		if i > 0 {
			args = append(args, "+")
		}
		args = append(args, "SYSLOG_IDENTIFIER="+id)
	}
	if typ == "WireGuard" { // systemd's start/stop lines of wg-quick@<if>
		args = append(args, "+", "UNIT=wg-quick@"+name+".service")
	}
	b, err := env.Exec.Run(ctx, "journalctl", args...)
	if err != nil && len(b) == 0 {
		return own, nil
	}
	nameRE := regexp.MustCompile(`(?:^|[^\w])` + regexp.QuoteMeta(name) + `(?:[^\w]|$)`)
	var out []model.VPNLogLine
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var e map[string]any
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		id, _ := e["SYSLOG_IDENTIFIER"].(string)
		msg := journalMessage(e["MESSAGE"])
		if msg == "" {
			continue
		}
		mine := false
		for _, o := range own {
			if id == o {
				mine = true
			}
		}
		// wg-quick logs for every interface; network managers for all of them
		if (!mine || id == "wg-quick") && !nameRE.MatchString(msg) {
			if unit, _ := e["UNIT"].(string); unit != "wg-quick@"+name+".service" {
				continue
			}
		}
		l := model.VPNLogLine{Service: firstNonEmpty(id, str(e["_SYSTEMD_UNIT"])), Message: msg, Priority: 6}
		if p, err := strconv.Atoi(str(e["PRIORITY"])); err == nil {
			l.Priority = p
		}
		if us, err := strconv.ParseInt(str(e["__REALTIME_TIMESTAMP"]), 10, 64); err == nil {
			l.Time = time.UnixMicro(us).UTC()
		}
		out = append(out, l)
	}
	if len(out) > vpnJournalLines {
		out = out[len(out)-vpnJournalLines:]
	}
	seen := append([]string{}, own...) // the VPN's own services, and every one that logged
	for _, l := range out {
		if !slices.Contains(seen, l.Service) {
			seen = append(seen, l.Service)
		}
	}
	return seen, out
}

// journalMessage: MESSAGE is a string, or an array of bytes when it is not
// valid UTF-8.
func journalMessage(v any) string {
	switch m := v.(type) {
	case string:
		return strings.TrimSpace(m)
	case []any:
		b := make([]byte, 0, len(m))
		for _, x := range m {
			if f, ok := x.(float64); ok {
				b = append(b, byte(f))
			}
		}
		return strings.TrimSpace(strings.ToValidUTF8(string(b), "?"))
	}
	return ""
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// vpnFindings judges a VPN interface: down while configured, WireGuard
// peers without a current session, errors of its service.
func vpnFindings(h *model.Header, n *model.NIC, now time.Time) model.Status {
	st := model.StatusOK
	add := func(s model.Status, code, msg string) {
		h.Find(s, code, n.Name, "vpn", msg)
		st = model.Worst(st, s)
	}
	v := n.VPN
	if v == nil {
		v = &model.VPN{Type: "VPN"}
	}
	linked := n.Carrier == nil || *n.Carrier // WireGuard has no carrier concept: up when admin up
	if n.AdminUp && !linked && len(n.Addresses) > 0 {
		add(model.StatusWarn, "vpn_down", fmt.Sprintf("%s (%s) is down: no tunnel (is the service running?)", n.Name, v.Type))
	}
	for i := range v.Peers {
		p := &v.Peers[i]
		who := shortKey(p.PublicKey)
		if p.Endpoint != "" {
			who += " (" + p.Endpoint + ")"
		}
		switch {
		case p.LatestHandshake.IsZero() && p.Endpoint != "":
			p.Status = model.StatusWarn
			add(model.StatusWarn, "vpn_peer", fmt.Sprintf("%s: peer %s has never completed a handshake", n.Name, who))
		case p.LatestHandshake.IsZero():
			// no endpoint: a peer that dials in; it has not yet
		case p.KeepaliveS > 0 && now.Sub(p.LatestHandshake) > wgSessionAge+time.Duration(p.KeepaliveS)*time.Second:
			p.Status = model.StatusWarn
			add(model.StatusWarn, "vpn_peer", fmt.Sprintf("%s: peer %s - last handshake %s ago, keepalive %d s: the tunnel is not up",
				n.Name, who, now.Sub(p.LatestHandshake).Round(time.Second), p.KeepaliveS))
		}
	}
	errs, last := 0, ""
	for _, l := range v.Journal {
		if l.Priority <= 3 {
			errs++
			last = l.Message
		}
	}
	if errs > 0 {
		add(model.StatusWarn, "vpn_service", fmt.Sprintf("%s: %d error(s) from %s in the last 24 h, last: %s", n.Name, errs, v.Type, last))
	}
	return st
}

// shortKey shortens a WireGuard public key for messages.
func shortKey(k string) string {
	if len(k) > 10 {
		return k[:8] + "…"
	}
	return k
}
