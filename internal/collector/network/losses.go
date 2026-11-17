package network

import (
	"context"
	"fmt"
	"strings"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/netinfo"
)

// Packet losses: for every interface whose drop counters grew since the
// last refresh, how many, how fast, what share of the traffic, where they
// are lost and why that usually happens on this kind of interface.

// rings reads the receive ring and queue count once per interface.
func (c *Collector) rings(ctx context.Context, env *collector.Env, n *model.NIC, et *netinfo.Ethtool, have bool) {
	c.mu.Lock()
	r, ok := c.ring[n.Name]
	c.mu.Unlock()
	if !ok {
		gotRing, gotQ := false, false
		if et != nil {
			if v, err := et.RingParams(n.Name); err == nil {
				r.rx, r.max, gotRing = v.RX, v.RXMax, true
			}
			if q, err := et.RxQueues(n.Name); err == nil {
				r.queues, gotQ = q, true
			}
		}
		if !gotRing && have {
			if b, err := env.Exec.Run(ctx, "ethtool", "-g", n.Name); err == nil {
				if v, ok := netinfo.ParseRing(b); ok {
					r.rx, r.max = v.RX, v.RXMax
				}
			}
		}
		if !gotQ && have {
			if b, err := env.Exec.Run(ctx, "ethtool", "-l", n.Name); err == nil {
				r.queues, _ = netinfo.ParseChannels(b)
			}
		}
		c.mu.Lock()
		c.ring[n.Name] = r
		c.mu.Unlock()
	}
	if r.rx > 0 {
		n.RingRX = &r.rx
		if r.max > 0 {
			n.RingRXMax = &r.max
		}
	}
	if r.queues > 0 {
		n.RxQueues = &r.queues
	}
}

type ringInfo struct{ rx, max, queues int }

func softnet(env *collector.Env, out *model.Network) {
	b, err := env.FS.ReadFile("/proc/net/softnet_stat")
	if err != nil {
		return
	}
	if d, s, ok := netinfo.Softnet(b); ok {
		out.SoftnetDropped, out.SoftnetSqueezed = &d, &s
	}
}

func grew(cur, prev uint64) uint64 {
	if cur > prev {
		return cur - prev
	}
	return 0 // unchanged, or reset (driver reload)
}

// losses fills NIC.Losses and raises the findings.
func losses(h *model.Header, out, prev *model.Network, prevBy map[string]*model.NIC, dt float64) {
	if prev == nil || dt <= 0 {
		return
	}
	backlog := uint64(0)
	if out.SoftnetDropped != nil && prev.SoftnetDropped != nil {
		backlog = grew(*out.SoftnetDropped, *prev.SoftnetDropped)
	}
	if backlog > 0 {
		h.Find(model.StatusWarn, "softnet_drops", "kernel", "procfs", fmt.Sprintf("the kernel's receive backlog dropped %d packets since the last refresh (%.1f/s): net.core.netdev_max_backlog is too small for the traffic, or one CPU gets all the interrupts", backlog, float64(backlog)/dt))
	}
	for i := range out.Interfaces {
		n := &out.Interfaces[i]
		p := prevBy[n.Name]
		if p == nil {
			continue
		}
		rxp := grew(n.Counters.RxPackets, p.Counters.RxPackets)
		txp := grew(n.Counters.TxPackets, p.Counters.TxPackets)
		for _, c := range []struct {
			name      string
			cur, prev uint64
			packets   uint64
		}{
			{"rx_missed", n.Counters.RxMissed, p.Counters.RxMissed, rxp},
			{"rx_dropped", n.Counters.RxDropped, p.Counters.RxDropped, rxp},
			{"tx_dropped", n.Counters.TxDropped, p.Counters.TxDropped, txp},
		} {
			d := grew(c.cur, c.prev)
			if d == 0 {
				continue
			}
			where, cause, benign := out.LossCause(n, c.name, backlog > 0)
			l := model.NICLoss{Counter: c.name, Total: c.cur, Delta: d, PerSec: float64(d) / dt, Where: where, Cause: cause}
			if c.packets+d > 0 {
				l.SharePct = float64(d) / float64(c.packets+d) * 100
			}
			switch {
			case benign:
				l.Status = model.StatusOK
			case c.name == "rx_dropped" && l.SharePct < 1 && backlog == 0:
				l.Status = model.StatusOK // a trickle of frames nobody wants
			default:
				l.Status = model.StatusWarn
			}
			n.Losses = append(n.Losses, l)
			label := map[string]string{"rx_missed": "packets lost in the NIC", "rx_dropped": "received packets dropped", "tx_dropped": "packets not sent"}[c.name]
			msg := fmt.Sprintf("%s: %d %s since the last refresh (%s, %.1f/s, %.2f %% of the traffic) %s: %s", n.Name, d, label, c.name, l.PerSec, l.SharePct, where, short(cause))
			h.Find(l.Status, c.name, n.Name, "sysfs", msg)
			if l.Status > n.Status {
				n.Status = l.Status
			}
		}
	}
}

// short is the cause up to its first " - " or ":" detail.
func short(s string) string {
	for _, sep := range []string{" - ", ": "} {
		if i := strings.Index(s, sep); i > 0 {
			return s[:i]
		}
	}
	return s
}
