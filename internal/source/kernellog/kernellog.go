// Package kernellog reads the kernel log and extracts hardware events.
// Sources in priority order: /dev/kmsg, `journalctl -k -b -o json`, `dmesg -r`.
package kernellog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/noahzmr/tyr/internal/execx"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
)

// Entry is one classified hardware-related kernel log message.
type Entry struct {
	Time     time.Time
	Priority int // syslog level 0..7
	Message  string
	Class
}

// Log is the result of reading the kernel log.
type Log struct {
	Source  string // "kmsg", "journalctl", "dmesg"
	Entries []Entry
	Total   int // number of messages scanned
	// Recent are the last messages of any kind (up to maxRecent), so a
	// component's surroundings - a RAID member failing, a session dropping -
	// can be shown next to its classified errors.
	Recent []Entry
}

const maxRecent = 5000

// ByCategory returns entries of one category.
func (l *Log) ByCategory(c Category) []Entry {
	var out []Entry
	for _, e := range l.Entries {
		if e.Category == c {
			out = append(out, e)
		}
	}
	return out
}

// maxMessages bounds memory use for very long-running hosts.
const maxMessages = 200000

// Read reads the kernel log of the current boot. bootTime converts the
// kernel's monotonic timestamps (from /proc/stat btime).
func Read(ctx context.Context, f hostfs.FS, r execx.Runner, bootTime time.Time) (*Log, error) {
	var errs []error
	if KmsgReadable(f) {
		l, err := readKmsg(f, bootTime)
		if err == nil {
			return l, nil
		}
		errs = append(errs, fmt.Errorf("/dev/kmsg: %w", err))
	} else {
		errs = append(errs, errors.New("/dev/kmsg: permission denied (dmesg_restrict)"))
	}
	if _, err := r.LookPath("journalctl"); err == nil {
		out, err := r.Run(ctx, "journalctl", "-k", "-b", "-o", "json", "--no-pager",
			"--output-fields=MESSAGE,PRIORITY,__REALTIME_TIMESTAMP")
		if err == nil {
			l, perr := ParseJournal(out)
			if perr == nil && l.Total > 0 {
				return l, nil
			}
			if perr != nil {
				errs = append(errs, perr)
			} else {
				errs = append(errs, errors.New("journalctl: no kernel messages visible (need root or adm/systemd-journal group)"))
			}
		} else {
			errs = append(errs, err)
		}
	}
	if _, err := r.LookPath("dmesg"); err == nil {
		out, err := r.Run(ctx, "dmesg", "-r")
		if err == nil {
			return ParseDmesgRaw(out, bootTime), nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// KmsgReadable reports whether /dev/kmsg can actually be read. access(2) is
// not enough: kernel.dmesg_restrict denies the open even when the mode allows it.
func KmsgReadable(f hostfs.FS) bool {
	if !f.IsHost() {
		return f.Readable("/dev/kmsg")
	}
	fd, err := unix.Open("/dev/kmsg", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	unix.Close(fd)
	return true
}

func readKmsg(f hostfs.FS, bootTime time.Time) (*Log, error) {
	real := f.Real("/dev/kmsg")
	if f.IsHost() {
		fd, err := unix.Open(real, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		defer unix.Close(fd)
		l := &Log{Source: "kmsg"}
		buf := make([]byte, 16<<10)
		for l.Total < maxMessages {
			n, err := unix.Read(fd, buf)
			if err == unix.EAGAIN {
				break
			}
			if err == unix.EPIPE || err == unix.EINTR {
				continue // record overwritten while reading, or interrupted
			}
			if err != nil {
				return nil, err
			}
			if n <= 0 {
				break
			}
			l.add(parseKmsgRecord(buf[:n], bootTime))
		}
		return l, nil
	}
	// Fixture: a plain file with one record per line.
	fh, err := f.Open("/dev/kmsg")
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	return ParseKmsg(fh, bootTime)
}

// ParseKmsg parses /dev/kmsg records stored as lines (fixture format).
// Continuation lines (starting with a space) are ignored.
func ParseKmsg(rd io.Reader, bootTime time.Time) (*Log, error) {
	l := &Log{Source: "kmsg"}
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() && l.Total < maxMessages {
		line := sc.Bytes()
		if len(line) == 0 || line[0] == ' ' {
			continue
		}
		l.add(parseKmsgRecord(line, bootTime))
	}
	return l, sc.Err()
}

type rawMsg struct {
	t   time.Time
	pri int
	msg string
	ok  bool
}

// parseKmsgRecord parses "pri,seq,usec,flags[,...];message[\n SUBSYSTEM=...]".
func parseKmsgRecord(rec []byte, bootTime time.Time) rawMsg {
	hdr, msg, ok := bytes.Cut(rec, []byte{';'})
	if !ok {
		return rawMsg{}
	}
	if i := bytes.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	f := strings.Split(string(hdr), ",")
	if len(f) < 3 {
		return rawMsg{}
	}
	pri, _ := strconv.Atoi(f[0])
	usec, _ := strconv.ParseInt(f[2], 10, 64)
	return rawMsg{t: bootTime.Add(time.Duration(usec) * time.Microsecond), pri: pri & 7, msg: unescape(string(msg)), ok: true}
}

// unescape decodes kmsg's \xNN escapes.
func unescape(s string) string {
	if !strings.Contains(s, `\x`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func (l *Log) add(m rawMsg) {
	if !m.ok {
		return
	}
	l.Total++
	e := Entry{Time: m.t, Priority: m.pri, Message: m.msg}
	if c := Classify(m.msg); c.Category != "" {
		e.Class = c
		l.Entries = append(l.Entries, e)
	}
	l.Recent = append(l.Recent, e)
	if len(l.Recent) > 2*maxRecent { // trim in batches
		l.Recent = append([]Entry(nil), l.Recent[len(l.Recent)-maxRecent:]...)
	}
}

// ParseJournal parses `journalctl -o json` output (one object per line).
func ParseJournal(data []byte) (*Log, error) {
	l := &Log{Source: "journalctl"}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() && l.Total < maxMessages {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec struct {
			Message  json.RawMessage `json:"MESSAGE"`
			Priority string          `json:"PRIORITY"`
			RealTime string          `json:"__REALTIME_TIMESTAMP"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("journalctl: %w", err)
		}
		msg := journalString(rec.Message)
		usec, _ := strconv.ParseInt(rec.RealTime, 10, 64)
		pri, err := strconv.Atoi(rec.Priority)
		if err != nil {
			pri = 6
		}
		l.add(rawMsg{t: time.UnixMicro(usec).UTC(), pri: pri, msg: msg, ok: true})
	}
	return l, sc.Err()
}

// journalString decodes MESSAGE, which is a string or (for non-UTF-8 data)
// an array of byte values.
func journalString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var bs []byte
	var ints []int
	if json.Unmarshal(raw, &ints) == nil {
		for _, v := range ints {
			bs = append(bs, byte(v))
		}
		return string(bs)
	}
	return ""
}

// ParseDmesgRaw parses `dmesg -r` output: "<6>[    1.234567] message".
func ParseDmesgRaw(data []byte, bootTime time.Time) *Log {
	l := &Log{Source: "dmesg"}
	for _, line := range strings.Split(string(data), "\n") {
		if l.Total >= maxMessages {
			break
		}
		if !strings.HasPrefix(line, "<") {
			continue
		}
		end := strings.IndexByte(line, '>')
		if end < 0 {
			continue
		}
		pri, _ := strconv.Atoi(line[1:end])
		rest := line[end+1:]
		var t time.Time
		if strings.HasPrefix(rest, "[") {
			if cl := strings.IndexByte(rest, ']'); cl > 0 {
				if secs, err := strconv.ParseFloat(strings.TrimSpace(rest[1:cl]), 64); err == nil {
					t = bootTime.Add(time.Duration(secs * float64(time.Second)))
				}
				rest = strings.TrimPrefix(rest[cl+1:], " ")
			}
		}
		l.add(rawMsg{t: t, pri: pri & 7, msg: rest, ok: true})
	}
	return l
}

// Severity maps an entry to a model status.
func (e Entry) Severity() model.Status { return e.Class.Severity }
