package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/model"
)

// vpnShort is the VPN type in a table cell.
func vpnShort(v *model.VPN) string {
	if v == nil {
		return "VPN"
	}
	switch v.Type {
	case "WireGuard":
		return "WG"
	case "OpenVPN/TUN":
		return "OpenVPN"
	}
	return v.Type
}

// vpnPeersUp counts peers with a current session.
func vpnPeersUp(v *model.VPN) (up, warn int) {
	for _, p := range v.Peers {
		switch {
		case p.Status >= model.StatusWarn:
			warn++
		case !p.LatestHandshake.IsZero():
			up++
		}
	}
	return up, warn
}

// vpnLinkText is the state of a VPN: "WG 2/3 peers", "OpenVPN up".
func vpnLinkText(x model.NIC) string {
	switch {
	case !x.AdminUp:
		return "admin down"
	case x.Carrier != nil && !*x.Carrier:
		return vpnShort(x.VPN) + " down"
	case x.VPN != nil && len(x.VPN.Peers) > 0:
		up, _ := vpnPeersUp(x.VPN)
		return fmt.Sprintf("%s %d/%d peers", vpnShort(x.VPN), up, len(x.VPN.Peers))
	}
	return vpnShort(x.VPN) + " up"
}

// vpnLink is vpnLinkText as the Link column: down or a peer without a
// session in warning colour.
func vpnLink(t *Theme, x model.NIC) string {
	s := vpnLinkText(x)
	if !x.AdminUp {
		return s
	}
	if x.Carrier != nil && !*x.Carrier {
		return t.StatusBold(model.StatusWarn, s)
	}
	if x.VPN != nil {
		if _, warn := vpnPeersUp(x.VPN); warn > 0 {
			return t.StatusBold(model.StatusWarn, s)
		}
	}
	return s
}

// handshakeAge: "12s ago", "3 min ago", "never".
func handshakeAge(at, now time.Time) string {
	if at.IsZero() {
		return "never"
	}
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

func shortKey(k string) string {
	if len(k) > 10 {
		return k[:8] + "…"
	}
	return k
}

// vpnStrip is the VPN's part of the details strip below the list.
func vpnStrip(t *Theme, x model.NIC, now time.Time) (string, string) {
	v := x.VPN
	if v == nil {
		v = &model.VPN{Type: "VPN"}
	}
	port := ""
	if v.ListenPort != nil {
		port = fmt.Sprint(*v.ListenPort)
	}
	c1 := kvBlock(t, []kvPair{{"VPN", v.Type}, {"Addresses", strings.Join(x.Addresses, " ")},
		{"MTU", fmt.Sprint(x.MTU)}, {"Public key", shortKey(v.PublicKey)}, {"Listen port", port}}, "")
	var rows []kvPair
	for _, p := range v.Peers {
		if len(rows) == 4 {
			rows = append(rows, kvPair{"", t.Sub.Render(fmt.Sprintf("+%d more · enter", len(v.Peers)-4))})
			break
		}
		age := handshakeAge(p.LatestHandshake, now)
		if p.Status >= model.StatusWarn {
			age = t.StatusText(p.Status, age)
		}
		rows = append(rows, kvPair{shortKey(p.PublicKey), age + " " + t.Sub.Render(orDash(p.Endpoint))})
	}
	if len(rows) == 0 {
		rows = append(rows, kvPair{"Peers", t.Sub.Render(firstNonEmpty(v.Note, "-"))})
	}
	return c1, kvBlock(t, rows, "")
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// vpnSections adds the VPN sections to an interface's page.
func vpnSections(doc *doc, x model.NIC, now time.Time) {
	t, v := doc.t, x.VPN
	if v == nil {
		return
	}
	port := ""
	if v.ListenPort != nil {
		port = fmt.Sprint(*v.ListenPort)
	}
	doc.section("VPN", t.Dim.Render(v.Type))
	doc.kv([2]string{"Type", v.Type}, [2]string{"Public key", v.PublicKey}, [2]string{"Listen port", port},
		[2]string{"Peer status", v.Note})
	if len(v.Peers) > 0 {
		up, _ := vpnPeersUp(v)
		doc.section("Peers", t.Dim.Render(fmt.Sprintf("%d of %d with a session · wg show %s", up, len(v.Peers), x.Name)))
		for _, p := range v.Peers {
			st := p.Status
			age := handshakeAge(p.LatestHandshake, now)
			doc.add(t.Dot(st) + " " + t.Bold.Render(pad(shortKey(p.PublicKey), 11)) + pad(orDash(p.Endpoint), 24) +
				t.StatusText(st, pad("handshake "+age, 24)) + t.Dim.Render(fmt.Sprintf("rx %s · tx %s", bytesGB(p.RxBytes), bytesGB(p.TxBytes))))
			extra := "allowed " + orDash(strings.Join(p.AllowedIPs, ", "))
			if p.KeepaliveS > 0 {
				extra += fmt.Sprintf(" · keepalive %d s", p.KeepaliveS)
			}
			doc.add("  " + t.Dim.Render(extra))
		}
	}
	if len(v.Journal) > 0 {
		doc.section("Service journal", t.Dim.Render(fmt.Sprintf("last 24 h, newest last · %s · journalctl -t %s", strings.Join(v.Services, ", "), strings.Join(v.Services, " -t "))))
		for _, l := range v.Journal {
			st := model.StatusOK
			switch {
			case l.Priority <= 3:
				st = model.StatusCrit
			case l.Priority == 4:
				st = model.StatusWarn
			}
			ts := ""
			if !l.Time.IsZero() {
				ts = l.Time.Local().Format("Jan 02 15:04:05") + " "
			}
			line := t.Dim.Render(ts+l.Service+": ") + l.Message
			if st != model.StatusOK {
				line = t.Dim.Render(ts+l.Service+": ") + t.StatusText(st, l.Message)
			}
			doc.add(line)
		}
	} else if len(v.Services) > 0 {
		doc.section("Service journal", t.Dim.Render("last 24 h"))
		doc.add(t.Dim.Render("no messages from " + strings.Join(v.Services, ", ")))
	}
	if len(v.KernelLines) > 0 {
		doc.section("Kernel log", t.Dim.Render(fmt.Sprintf("last %d lines naming %s · newest last", len(v.KernelLines), x.Name)))
		for _, l := range v.KernelLines {
			doc.add(t.Dim.Render(l))
		}
	}
}
