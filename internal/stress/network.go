package stress

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/netproto"
)

// peerAddr normalizes host[:port].
func peerAddr(target string, defPort int) string {
	if target == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(target); err == nil {
		return target
	}
	return net.JoinHostPort(target, strconv.Itoa(defPort))
}

// localNIC finds the interface traffic to addr leaves through (a UDP
// "connect" sends no packet) and its link speed.
func localNIC(env *Env, addr string) (model.NIC, bool) {
	c, err := net.Dial("udp", addr)
	if err != nil {
		return model.NIC{}, false
	}
	ip := c.LocalAddr().(*net.UDPAddr).IP.String()
	c.Close()
	if env.Baseline.Network == nil {
		return model.NIC{}, false
	}
	for _, n := range env.Baseline.Network.Interfaces {
		for _, a := range n.Addresses {
			if strings.SplitN(a, "/", 2)[0] == ip {
				return n, true
			}
		}
	}
	return model.NIC{}, false
}

func netPlan(cfg Config, env *Env, engine string) *Plan {
	p := &Plan{}
	if engine == EngStressNG {
		p.Scope = []string{"kernel network stack on this host (loopback)"}
		p.Impact = []string{"Loads the TCP/UDP stack with socket traffic to this host's own addresses for " + fmtDur(cfg.Duration) + "."}
		p.Warnings = []string{"This does not send traffic through a network card: it tests the kernel network stack only. Use the built-in engine with a `tyr net-peer` (or iperf3) on another host to test a NIC, cable and switch port."}
		return p
	}
	def := netproto.DefaultPort
	if engine == EngIperf3 {
		def = 5201
	}
	addr := peerAddr(cfg.Target, def)
	if addr == "" {
		if engine == EngIperf3 {
			p.Blocked = "enter the address of a host running `iperf3 -s`"
		} else {
			p.Blocked = "start `tyr net-peer` on another host and enter its address and token"
		}
		return p
	}
	if engine == EngBuiltin && cfg.Token == "" {
		p.Blocked = "enter the token `tyr net-peer` printed when it started"
		return p
	}
	streams := max(1, cfg.Streams)
	dir := "this host → peer"
	if cfg.Reverse {
		dir = "peer → this host"
	}
	scope := fmt.Sprintf("%s, %d TCP streams, %s", addr, streams, dir)
	if n, ok := localNIC(env, addr); ok {
		scope += fmt.Sprintf(" via %s", n.Name)
		if n.SpeedMbps != nil {
			scope += fmt.Sprintf(" (link %d Mb/s)", *n.SpeedMbps)
		}
	} else {
		p.Warnings = append(p.Warnings, "could not tell which interface reaches "+addr)
	}
	p.Scope = []string{scope}
	p.Impact = []string{"Saturates the link to the peer for " + fmtDur(cfg.Duration) + "; other traffic on this interface and the switch path suffers.",
		"Watches CRC/drop counters, link flaps and transceiver levels during the run; throughput below 90% of the link speed is a WARN."}
	return p
}

// --- built-in (tyr net-peer) --------------------------------------------------------

type netBuiltin struct{}

func (netBuiltin) Plan(cfg Config, env *Env) *Plan { return netPlan(cfg, env, EngBuiltin) }

func tcpRetrans(c net.Conn) uint64 {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return 0
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		return 0
	}
	var n uint64
	_ = raw.Control(func(fd uintptr) {
		if info, err := unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO); err == nil {
			n = uint64(info.Total_retrans)
		}
	})
	return n
}

func (netBuiltin) Run(ctx context.Context, cfg Config, _ *Plan, rt Runtime) error {
	env := runEnv(ctx)
	addr := peerAddr(cfg.Target, netproto.DefaultPort)
	streams := max(1, cfg.Streams)
	sb := make([]byte, 8)
	_, _ = rand.Read(sb)
	session := hex.EncodeToString(sb)
	hello := netproto.Hello{Token: cfg.Token, Session: session, Streams: streams, Reverse: cfg.Reverse, Duration: cfg.Duration.Milliseconds()}
	hello.Role = "control"
	ctl, ctlR, err := netproto.Dial(addr, hello, 10*time.Second)
	if err != nil {
		return fmt.Errorf("net-peer %s: %w", addr, err)
	}
	defer ctl.Close()
	rt.Track(ctl)

	var speed float64
	if n, ok := localNIC(env, addr); ok && n.SpeedMbps != nil {
		speed = float64(*n.SpeedMbps) * 1e6
	}
	var bytes atomic.Uint64
	var mu sync.Mutex
	var conns []net.Conn
	var recvs []*netproto.Receiver
	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		h := hello
		h.Role, h.Stream = "data", i
		c, br, err := netproto.Dial(addr, h, 10*time.Second)
		if err != nil {
			return fmt.Errorf("data stream %d: %w", i, err)
		}
		rt.Track(c)
		mu.Lock()
		conns = append(conns, c)
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer c.Close()
			go func() { <-ctx.Done(); c.Close() }()
			if cfg.Reverse {
				r := netproto.NewReceiver(i)
				mu.Lock()
				recvs = append(recvs, r)
				mu.Unlock()
				for ctx.Err() == nil {
					if r.Recv(br) != nil {
						return
					}
					bytes.Add(netproto.FrameSize)
				}
				return
			}
			s := netproto.NewSender(i)
			for ctx.Err() == nil {
				if s.Send(c) != nil {
					return
				}
				bytes.Add(netproto.FrameSize)
			}
		}()
	}
	rt.Progress(-1, fmt.Sprintf("%d streams to %s", streams, addr))
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last, lastAt := uint64(0), time.Now()
	loop := true
	for loop {
		select {
		case <-ctx.Done():
			loop = false
		case now := <-t.C:
			n := bytes.Load()
			bps := float64(n-last) * 8 / now.Sub(lastAt).Seconds()
			rt.Metric("net_test_bits_per_s", "bits_per_second", bps)
			if speed > 0 {
				rt.Metric("net_link_ratio", "percent", bps/speed*100)
			}
			var retr uint64
			mu.Lock()
			for _, c := range conns {
				retr += tcpRetrans(c)
			}
			mu.Unlock()
			if !cfg.Reverse {
				rt.Metric("tcp_retransmits", "count", float64(retr))
			}
			last, lastAt = n, now
		}
	}
	wg.Wait()
	// Collect the receiver's view of the data (corruption, lost frames).
	var stats []netproto.StreamStats
	if cfg.Reverse {
		for _, r := range recvs {
			stats = append(stats, r.Stats)
		}
	} else {
		_ = ctl.SetDeadline(time.Now().Add(5 * time.Second))
		if err := netproto.WriteJSON(ctl, netproto.Request{Op: "stats"}); err == nil {
			var rep netproto.Reply
			if err := netproto.ReadJSON(ctlR, &rep); err == nil {
				stats = rep.Stats
			} else {
				rt.Logf("peer stats: %v", err)
			}
		}
	}
	var total uint64
	for _, s := range stats {
		total += s.Bytes
		if s.CRCErrors > 0 || s.SeqErrors > 0 {
			rt.VerifyError(VerifyError{Subject: fmt.Sprintf("stream %d", s.Stream), Workload: "tcp payload",
				Detail: fmt.Sprintf("%d corrupted frames (CRC32C), %d lost/duplicated frames of %d", s.CRCErrors, s.SeqErrors, s.Frames)})
		}
	}
	rt.Logf("receiver verified %s in %d streams", humanBytes(total), len(stats))
	return nil
}

// --- iperf3 ---------------------------------------------------------------------

type netIperf3 struct{}

func (netIperf3) Plan(cfg Config, env *Env) *Plan { return netPlan(cfg, env, EngIperf3) }

var iperfRate = regexp.MustCompile(`([\d.]+)\s+([KMG]?)bits/sec`)

func (netIperf3) Run(ctx context.Context, cfg Config, _ *Plan, rt Runtime) error {
	env := runEnv(ctx)
	addr := peerAddr(cfg.Target, 5201)
	host, port, _ := net.SplitHostPort(addr)
	streams := max(1, cfg.Streams)
	var speed float64
	if n, ok := localNIC(env, addr); ok && n.SpeedMbps != nil {
		speed = float64(*n.SpeedMbps) * 1e6
	}
	args := []string{"-c", host, "-p", port, "-t", strconv.Itoa(int(cfg.Duration.Seconds()) + 2), "-P", strconv.Itoa(streams), "-i", "1", "--forceflush"}
	if cfg.Reverse {
		args = append(args, "-R")
	}
	rt.Progress(-1, fmt.Sprintf("iperf3 to %s, %d streams", addr, streams))
	err := runExternal(ctx, rt, EngIperf3, args, func(_, line string) {
		if streams > 1 && !strings.Contains(line, "[SUM]") {
			return
		}
		if strings.Contains(line, "sender") || strings.Contains(line, "receiver") {
			return
		}
		m := iperfRate.FindStringSubmatch(line)
		if m == nil {
			return
		}
		v, _ := strconv.ParseFloat(m[1], 64)
		v *= map[string]float64{"": 1, "K": 1e3, "M": 1e6, "G": 1e9}[m[2]]
		rt.Metric("net_test_bits_per_s", "bits_per_second", v)
		if speed > 0 {
			rt.Metric("net_link_ratio", "percent", v/speed*100)
		}
	})
	if err != nil {
		return fmt.Errorf("iperf3: %w", err)
	}
	return nil
}

// --- stress-ng (kernel stack only) -------------------------------------------------

type netStressNG struct{}

func (netStressNG) Plan(cfg Config, env *Env) *Plan { return netPlan(cfg, env, EngStressNG) }

func (netStressNG) Run(ctx context.Context, cfg Config, _ *Plan, rt Runtime) error {
	rt.Progress(-1, "stress-ng --sock/--udp (kernel stack, loopback)")
	return stressNG(ctx, cfg, rt, "network stack", "--sock", "2", "--udp", "2")
}
