// Package netpeer is the `tyr net-peer` server: the far end of the
// network stress test. It needs a token, serves one session at a time and
// exits after a period without sessions.
package netpeer

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/noahzmr/tyr/internal/netproto"
)

// Server configuration.
type Server struct {
	Token       string
	IdleTimeout time.Duration // exit after this long without a session (default 10m)
	MaxSession  time.Duration // hard cap per session (default 25h)
	Logf        func(format string, args ...any)

	mu       sync.Mutex
	session  string
	conns    []net.Conn
	recv     map[int]*netproto.Receiver
	lastSeen time.Time
}

// NewToken returns a random 16-byte hex token.
func NewToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// Serve accepts connections on l until ctx ends or the idle timeout passes.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	if s.Token == "" {
		return errors.New("net-peer: a token is required")
	}
	if s.IdleTimeout <= 0 {
		s.IdleTimeout = 10 * time.Minute
	}
	if s.MaxSession <= 0 {
		s.MaxSession = 25 * time.Hour
	}
	s.lastSeen = time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				l.Close()
				return
			case <-t.C:
				s.mu.Lock()
				idle := s.session == "" && time.Since(s.lastSeen) > s.IdleTimeout
				s.mu.Unlock()
				if idle {
					s.logf("no session for %v, exiting", s.IdleTimeout)
					cancel()
				}
			}
		}
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handle(ctx, c)
	}
}

func (s *Server) handle(ctx context.Context, c net.Conn) {
	defer c.Close()
	br := bufio.NewReaderSize(c, 64<<10)
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var h netproto.Hello
	if err := netproto.ReadJSON(br, &h); err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	refuse := func(msg string) {
		_ = netproto.WriteJSON(c, netproto.Reply{Error: msg})
		s.logf("refused %s: %s", c.RemoteAddr(), msg)
	}
	if h.Version != netproto.Version {
		refuse(fmt.Sprintf("protocol version %d, peer speaks %d", h.Version, netproto.Version))
		return
	}
	if !netproto.TokenOK(s.Token, h.Token) {
		time.Sleep(time.Second) // slow down guessing
		refuse("wrong token")
		return
	}
	s.mu.Lock()
	switch {
	case h.Role == "control" && s.session == "":
		s.session, s.recv, s.lastSeen = h.Session, map[int]*netproto.Receiver{}, time.Now()
	case h.Session != s.session:
		s.mu.Unlock()
		refuse("busy with another session")
		return
	}
	s.conns = append(s.conns, c)
	s.mu.Unlock()
	if err := netproto.WriteJSON(c, netproto.Reply{OK: true}); err != nil {
		return
	}
	switch h.Role {
	case "control":
		s.logf("session from %s: %d streams, %s, %v", c.RemoteAddr(), h.Streams, map[bool]string{true: "peer sends", false: "peer receives"}[h.Reverse], time.Duration(h.Duration)*time.Millisecond)
		s.control(ctx, c, br)
		s.endSession()
	case "data":
		sctx, cancel := context.WithTimeout(ctx, s.MaxSession)
		defer cancel()
		go func() { <-sctx.Done(); c.Close() }()
		if h.Reverse {
			snd := netproto.NewSender(h.Stream)
			for sctx.Err() == nil {
				if err := snd.Send(c); err != nil {
					return
				}
			}
			return
		}
		r := netproto.NewReceiver(h.Stream)
		s.mu.Lock()
		s.recv[h.Stream] = r
		s.mu.Unlock()
		for {
			err := r.Recv(br)
			s.mu.Lock()
			s.lastSeen = time.Now()
			s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (s *Server) control(ctx context.Context, c net.Conn, br *bufio.Reader) {
	for ctx.Err() == nil {
		var req netproto.Request
		if err := netproto.ReadJSON(br, &req); err != nil {
			if !errors.Is(err, io.EOF) {
				s.logf("control: %v", err)
			}
			return
		}
		if req.Op == "stats" {
			s.mu.Lock()
			var st []netproto.StreamStats
			for _, r := range s.recv {
				st = append(st, r.Stats)
			}
			s.mu.Unlock()
			if err := netproto.WriteJSON(c, netproto.Reply{OK: true, Stats: st}); err != nil {
				return
			}
		}
	}
}

func (s *Server) endSession() {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total uint64
	for _, r := range s.recv {
		total += r.Stats.Bytes
	}
	for _, c := range s.conns {
		c.Close()
	}
	s.logf("session ended, received %d MiB", total>>20)
	s.session, s.conns, s.recv, s.lastSeen = "", nil, nil, time.Now()
}
