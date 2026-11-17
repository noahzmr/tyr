package netinfo

import (
	"encoding/binary"
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Native ethtool: the same SIOCETHTOOL requests the ethtool tool sends, so
// Tyr needs no ethtool binary on the host. Every reader returns an error
// the caller can fall back on (the driver does not support the request).

const (
	ethtoolGRingParam     = 0x10
	ethtoolGStrings       = 0x1b
	ethtoolGStats         = 0x1d
	ethtoolGChannels      = 0x3c
	ethtoolGModuleInfo    = 0x42
	ethtoolGModuleEEPROM  = 0x43
	ethtoolGLinkSettings  = 0x4c
	ethSSStats            = 1
	ethGStringLen         = 32
	linkModeMaxNWords     = 127
	linkSettingsHeaderLen = 48
)

// Ethtool is an open control socket.
type Ethtool struct{ fd int }

// OpenEthtool opens the socket the requests go through.
func OpenEthtool() (*Ethtool, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &Ethtool{fd: fd}, nil
}

// Close closes the socket.
func (e *Ethtool) Close() error { return unix.Close(e.fd) }

// ifreq is struct ifreq with ifr_data (40 bytes on 64-bit).
type ifreq struct {
	name [unix.IFNAMSIZ]byte
	data unsafe.Pointer
	_    [16]byte
}

func (e *Ethtool) ioctl(name string, buf []byte) error {
	if len(name) >= unix.IFNAMSIZ {
		return fmt.Errorf("interface name too long: %s", name)
	}
	var ifr ifreq
	copy(ifr.name[:], name)
	ifr.data = unsafe.Pointer(&buf[0])
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(e.fd), unix.SIOCETHTOOL, uintptr(unsafe.Pointer(&ifr)))
	runtime.KeepAlive(buf)
	if errno != 0 {
		return errno
	}
	return nil
}

var le = binary.LittleEndian // the kernel's byte order on amd64 and arm64

func cmdBuf(cmd uint32, n int) []byte {
	b := make([]byte, n)
	le.PutUint32(b, cmd)
	return b
}

// LinkInfo reads the port, autonegotiation and the fastest advertised (else
// supported) link mode, like ParseLinkInfo does from `ethtool <if>`.
func (e *Ethtool) LinkInfo(name string) (LinkInfo, error) {
	var li LinkInfo
	b := cmdBuf(ethtoolGLinkSettings, linkSettingsHeaderLen+3*linkModeMaxNWords*4)
	if err := e.ioctl(name, b); err != nil { // handshake: the kernel answers −nwords
		return li, err
	}
	n := -int(int8(b[15]))
	if n <= 0 || n > linkModeMaxNWords {
		return li, fmt.Errorf("link settings: %d mask words", n)
	}
	b = cmdBuf(ethtoolGLinkSettings, linkSettingsHeaderLen+3*n*4)
	b[15] = byte(int8(n))
	if err := e.ioctl(name, b); err != nil {
		return li, err
	}
	li.Port = portName(b[9])
	li.Autoneg = map[byte]string{0: "off", 1: "on"}[b[11]]
	li.Duplex = map[byte]string{0: "Half", 1: "Full", 0xff: "Unknown"}[b[8]]
	mask := func(k int) []uint32 {
		out := make([]uint32, n)
		for i := range out {
			out[i] = le.Uint32(b[linkSettingsHeaderLen+(k*n+i)*4:])
		}
		return out
	}
	best := func(m []uint32) int {
		top := 0
		for bit, speed := range linkModeSpeeds {
			if bit/32 < len(m) && m[bit/32]&(1<<(bit%32)) != 0 && speed > top {
				top = speed
			}
		}
		return top
	}
	if v := best(mask(1)); v > 0 { // advertising
		li.MaxSpeedMbps = &v
	} else if v := best(mask(0)); v > 0 { // supported
		li.MaxSpeedMbps = &v
	}
	return li, nil
}

func portName(p byte) string {
	return map[byte]string{0x00: "Twisted Pair", 0x01: "AUI", 0x02: "BNC", 0x03: "MII", 0x04: "FIBRE", 0x05: "Direct Attach Copper", 0xef: "None", 0xff: "Other"}[p]
}

// linkModeSpeeds maps ethtool link mode bits (enum ethtool_link_mode_bit_indices)
// to their speed in Mb/s; bits that are not speeds are left out.
var linkModeSpeeds = map[int]int{
	0: 10, 1: 10, 2: 100, 3: 100, 4: 1000, 5: 1000, 12: 10000, 15: 2500, 17: 1000, 18: 10000, 19: 10000, 20: 10000,
	21: 20000, 22: 20000, 23: 40000, 24: 40000, 25: 40000, 26: 40000, 27: 56000, 28: 56000, 29: 56000, 30: 56000,
	31: 25000, 32: 25000, 33: 25000, 34: 50000, 35: 50000, 36: 100000, 37: 100000, 38: 100000, 39: 100000, 40: 50000,
	41: 1000, 42: 10000, 43: 10000, 44: 10000, 45: 10000, 46: 10000, 47: 2500, 48: 5000,
	52: 50000, 53: 50000, 54: 50000, 55: 50000, 56: 50000, 57: 100000, 58: 100000, 59: 100000, 60: 100000, 61: 100000,
	62: 200000, 63: 200000, 64: 200000, 65: 200000, 66: 200000, 67: 100, 68: 1000,
	69: 400000, 70: 400000, 71: 400000, 72: 400000, 73: 400000,
	75: 100000, 76: 100000, 77: 100000, 78: 100000, 79: 100000, 80: 200000, 81: 200000, 82: 200000, 83: 200000, 84: 200000,
	85: 400000, 86: 400000, 87: 400000, 88: 400000, 89: 400000, 90: 100, 91: 100, 92: 10,
	93: 800000, 94: 800000, 95: 800000, 96: 800000, 97: 800000, 98: 800000, 99: 10, 100: 10, 101: 10, 102: 10,
}

// Stats reads every driver statistic (`ethtool -S`), names and values.
func (e *Ethtool) Stats(name string) (map[string]uint64, error) {
	di, err := unix.IoctlGetEthtoolDrvinfo(e.fd, name)
	if err != nil {
		return nil, err
	}
	n := int(di.N_stats)
	if n == 0 {
		return map[string]uint64{}, nil
	}
	sb := cmdBuf(ethtoolGStrings, 12+n*ethGStringLen)
	le.PutUint32(sb[4:], ethSSStats)
	le.PutUint32(sb[8:], uint32(n))
	if err := e.ioctl(name, sb); err != nil {
		return nil, err
	}
	vb := cmdBuf(ethtoolGStats, 8+n*8)
	le.PutUint32(vb[4:], uint32(n))
	if err := e.ioctl(name, vb); err != nil {
		return nil, err
	}
	out := make(map[string]uint64, n)
	for i := 0; i < n; i++ {
		k := strings.TrimRight(string(sb[12+i*ethGStringLen:12+(i+1)*ethGStringLen]), "\x00")
		out[strings.TrimSpace(k)] = le.Uint64(vb[8+i*8:])
	}
	return out, nil
}

// RingParams reads the receive ring (`ethtool -g`).
func (e *Ethtool) RingParams(name string) (Ring, error) {
	b := cmdBuf(ethtoolGRingParam, 36)
	if err := e.ioctl(name, b); err != nil {
		return Ring{}, err
	}
	return Ring{RXMax: int(le.Uint32(b[4:])), RX: int(le.Uint32(b[20:]))}, nil
}

// RxQueues reads the receive queues in use (`ethtool -l`: combined, else RX).
func (e *Ethtool) RxQueues(name string) (int, error) {
	b := cmdBuf(ethtoolGChannels, 36)
	if err := e.ioctl(name, b); err != nil {
		return 0, err
	}
	rx, combined := int(le.Uint32(b[20:])), int(le.Uint32(b[32:]))
	return max(rx, combined), nil
}

// ModuleEEPROM reads the transceiver's EEPROM: its SFF type and bytes.
func (e *Ethtool) ModuleEEPROM(name string) (int, []byte, error) {
	info := cmdBuf(ethtoolGModuleInfo, 44)
	if err := e.ioctl(name, info); err != nil {
		return 0, nil, err
	}
	typ, n := int(le.Uint32(info[4:])), int(le.Uint32(info[8:]))
	if n <= 0 || n > 4096 {
		return 0, nil, fmt.Errorf("module EEPROM length %d", n)
	}
	b := cmdBuf(ethtoolGModuleEEPROM, 16+n)
	le.PutUint32(b[12:], uint32(n))
	if err := e.ioctl(name, b); err != nil {
		return 0, nil, err
	}
	return typ, b[16 : 16+n], nil
}

// FilterStats keeps the non-zero counters whose names indicate errors,
// drops or discards - what ParseStats keeps from `ethtool -S`.
func FilterStats(all map[string]uint64) map[string]uint64 {
	out := map[string]uint64{}
	for k, v := range all {
		if v == 0 || !statsRe.MatchString(strings.ToLower(k)) || strings.Contains(k, "pkts_ok") {
			continue
		}
		out[k] = v
		if len(out) >= 50 {
			break
		}
	}
	return out
}
