// Package netproto is the wire protocol between `tyr stress --component
// network` and `tyr net-peer`.
//
// Every TCP connection starts with one JSON line from the client (Hello)
// and one JSON line reply (Reply). A session has one control connection and
// N data connections. Data flows as fixed-size frames:
//
//	[8 bytes sequence][4 bytes CRC32C of the payload][payload]
//
// The receiver checks the sequence (lost or duplicated frames) and the CRC
// (corruption that slipped past Ethernet FCS and TCP checksums). At the end
// the client asks the peer for its counters over the control connection.
package netproto

import (
	"bufio"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"time"
)

// Version of the protocol.
const Version = 1

// DefaultPort of net-peer.
const DefaultPort = 7300

// FrameSize is the size of one data frame.
const FrameSize = 64 << 10

const headerSize = 12

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Hello is the first line of every connection.
type Hello struct {
	Version  int    `json:"v"`
	Token    string `json:"token"`
	Session  string `json:"session"`
	Role     string `json:"role"` // control, data
	Stream   int    `json:"stream"`
	Streams  int    `json:"streams"`
	Reverse  bool   `json:"reverse"` // peer sends, client receives
	Duration int64  `json:"duration_ms"`
}

// Reply answers a Hello or a control request.
type Reply struct {
	OK    bool          `json:"ok"`
	Error string        `json:"error,omitempty"`
	Stats []StreamStats `json:"stats,omitempty"`
}

// StreamStats are a receiver's counters for one stream.
type StreamStats struct {
	Stream    int    `json:"stream"`
	Bytes     uint64 `json:"bytes"`
	Frames    uint64 `json:"frames"`
	CRCErrors uint64 `json:"crc_errors"`
	SeqErrors uint64 `json:"seq_errors"`
}

// Request is a control-channel request ("stats").
type Request struct {
	Op string `json:"op"`
}

// TokenOK compares tokens in constant time.
func TokenOK(want, got string) bool {
	return len(want) > 0 && subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

// WriteJSON writes v as one line.
func WriteJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// ReadJSON reads one line (at most 64 KiB) into v.
func ReadJSON(r *bufio.Reader, v any) error {
	line, err := r.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return errors.New("netproto: line too long")
		}
		return err
	}
	return json.Unmarshal(line, v)
}

// Sender writes frames with increasing sequence numbers.
type Sender struct {
	buf []byte
	seq uint64
}

// NewSender prepares a frame buffer with a stream-specific payload.
func NewSender(stream int) *Sender {
	b := make([]byte, FrameSize)
	x := uint64(stream)*0x9E3779B97F4A7C15 | 1
	for i := headerSize; i+8 <= len(b); i += 8 {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		binary.LittleEndian.PutUint64(b[i:], x)
	}
	binary.LittleEndian.PutUint32(b[8:], crc32.Checksum(b[headerSize:], castagnoli))
	return &Sender{buf: b}
}

// Send writes one frame.
func (s *Sender) Send(w io.Writer) error {
	binary.LittleEndian.PutUint64(s.buf[0:], s.seq)
	s.seq++
	_, err := w.Write(s.buf)
	return err
}

// Receiver checks incoming frames.
type Receiver struct {
	buf   []byte
	next  uint64
	Stats StreamStats
}

// NewReceiver creates a receiver for one stream.
func NewReceiver(stream int) *Receiver {
	return &Receiver{buf: make([]byte, FrameSize), Stats: StreamStats{Stream: stream}}
}

// Recv reads and checks one frame. It returns an error only for I/O
// errors; corruption is counted in Stats.
func (r *Receiver) Recv(rd io.Reader) error {
	if _, err := io.ReadFull(rd, r.buf); err != nil {
		return err
	}
	seq := binary.LittleEndian.Uint64(r.buf[0:])
	if seq != r.next {
		r.Stats.SeqErrors++
	}
	r.next = seq + 1
	if crc32.Checksum(r.buf[headerSize:], castagnoli) != binary.LittleEndian.Uint32(r.buf[8:]) {
		r.Stats.CRCErrors++
	}
	r.Stats.Frames++
	r.Stats.Bytes += FrameSize
	return nil
}

// Dial connects and performs the hello exchange.
func Dial(addr string, h Hello, timeout time.Duration) (net.Conn, *bufio.Reader, error) {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, nil, err
	}
	h.Version = Version
	_ = c.SetDeadline(time.Now().Add(timeout))
	if err := WriteJSON(c, h); err != nil {
		c.Close()
		return nil, nil, err
	}
	br := bufio.NewReaderSize(c, 64<<10)
	var rep Reply
	if err := ReadJSON(br, &rep); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("peer did not answer: %w", err)
	}
	_ = c.SetDeadline(time.Time{})
	if !rep.OK {
		c.Close()
		return nil, nil, fmt.Errorf("peer refused: %s", rep.Error)
	}
	return c, br, nil
}
