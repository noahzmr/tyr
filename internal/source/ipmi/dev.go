package ipmi

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Native IPMI: requests to the BMC through the kernel's IPMI driver
// (/dev/ipmi0), so no ipmitool or ipmiutil is needed. Like the tool path,
// only read-only requests can be sent: readOnly is the single list of what
// may go to the BMC.

// NetFn values.
const (
	netFnSensor    = 0x04
	netFnApp       = 0x06
	netFnStorage   = 0x0A
	netFnTransport = 0x0C
)

// readOnly lists the (NetFn, command) pairs Tyr sends. Never add a Set,
// Clear, Chassis Control, Cold Reset or raw OEM command here. Reserve SDR
// Repository only takes a reservation for reading records in pieces.
var readOnly = map[[2]byte]string{
	{netFnApp, 0x01}:       "Get Device ID",
	{netFnStorage, 0x10}:   "Get FRU Inventory Area Info",
	{netFnStorage, 0x11}:   "Read FRU Data",
	{netFnStorage, 0x20}:   "Get SDR Repository Info",
	{netFnStorage, 0x22}:   "Reserve SDR Repository",
	{netFnStorage, 0x23}:   "Get SDR",
	{netFnStorage, 0x40}:   "Get SEL Info",
	{netFnStorage, 0x43}:   "Get SEL Entry",
	{netFnSensor, 0x2D}:    "Get Sensor Reading",
	{netFnTransport, 0x02}: "Get LAN Configuration Parameters",
}

// transport sends one request and returns the response data after the
// completion code.
type transport interface {
	send(netfn, cmd byte, data []byte) ([]byte, error)
}

// CompletionError is a BMC answer other than "OK".
type CompletionError struct {
	Cmd  string
	Code byte
}

func (e *CompletionError) Error() string {
	return fmt.Sprintf("%s: completion code 0x%02x", e.Cmd, e.Code)
}

// device is /dev/ipmi0.
type device struct {
	mu    sync.Mutex
	f     *os.File
	msgid int64
}

// Device paths the kernel driver creates.
var devPaths = []string{"/dev/ipmi0", "/dev/ipmi/0", "/dev/ipmidev/0"}

func openDevice() (*device, error) {
	var last error
	for _, p := range devPaths {
		f, err := os.OpenFile(p, os.O_RDWR, 0)
		if err == nil {
			return &device{f: f}, nil
		}
		last = err
	}
	return nil, last
}

// ipmi_system_interface_addr: the BMC itself.
type sysAddr struct {
	addrType int32
	channel  int16
	lun      uint8
	_        uint8
}

type ipmiMsg struct {
	netfn   uint8
	cmd     uint8
	dataLen uint16
	_       uint32
	data    unsafe.Pointer
}

type ipmiReq struct {
	addr    unsafe.Pointer
	addrLen uint32
	_       uint32
	msgid   int64
	msg     ipmiMsg
}

type ipmiRecv struct {
	recvType int32
	_        uint32
	addr     unsafe.Pointer
	addrLen  uint32
	_        uint32
	msgid    int64
	msg      ipmiMsg
}

const (
	ipmiSystemInterfaceAddrType = 0x0c
	ipmiBMCChannel              = 0x0f
	// _IOR('i', 13, struct ipmi_req) and _IOWR('i', 11, struct ipmi_recv)
	ipmictlSendCommand    = 2<<30 | uint(unsafe.Sizeof(ipmiReq{}))<<16 | 'i'<<8 | 13
	ipmictlReceiveMsgTrnc = 3<<30 | uint(unsafe.Sizeof(ipmiRecv{}))<<16 | 'i'<<8 | 11
	ipmiTimeout           = 5 * time.Second
)

// guarded lets only the read-only requests through to any transport.
type guarded struct{ t transport }

func (g guarded) send(netfn, cmd byte, data []byte) ([]byte, error) {
	if _, ok := readOnly[[2]byte{netfn, cmd}]; !ok {
		return nil, fmt.Errorf("%w: netfn 0x%02x cmd 0x%02x", ErrNotAllowed, netfn, cmd)
	}
	return g.t.send(netfn, cmd, data)
}

func (d *device) send(netfn, cmd byte, data []byte) ([]byte, error) {
	name, ok := readOnly[[2]byte{netfn, cmd}]
	if !ok { // also checked here: the device never sends anything else
		return nil, fmt.Errorf("%w: netfn 0x%02x cmd 0x%02x", ErrNotAllowed, netfn, cmd)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.msgid++
	addr := sysAddr{addrType: ipmiSystemInterfaceAddrType, channel: ipmiBMCChannel}
	buf := append([]byte{0}, data...) // never empty: a valid pointer
	req := ipmiReq{addr: unsafe.Pointer(&addr), addrLen: uint32(unsafe.Sizeof(addr)), msgid: d.msgid,
		msg: ipmiMsg{netfn: netfn, cmd: cmd, dataLen: uint16(len(data)), data: unsafe.Pointer(&buf[1])}}
	if len(data) == 0 {
		req.msg.data = unsafe.Pointer(&buf[0])
	}
	fd := d.f.Fd()
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, uintptr(ipmictlSendCommand), uintptr(unsafe.Pointer(&req))); errno != 0 {
		return nil, fmt.Errorf("%s: %w", name, errno)
	}
	runtime.KeepAlive(&addr)
	runtime.KeepAlive(buf)
	deadline := time.Now().Add(ipmiTimeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return nil, fmt.Errorf("%s: no answer from the BMC within %v", name, ipmiTimeout)
		}
		pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if n, err := unix.Poll(pfd, int(left.Milliseconds())+1); err != nil && !errors.Is(err, unix.EINTR) {
			return nil, err
		} else if n == 0 {
			continue
		}
		var raddr sysAddr
		rbuf := make([]byte, 1024)
		recv := ipmiRecv{addr: unsafe.Pointer(&raddr), addrLen: uint32(unsafe.Sizeof(raddr)),
			msg: ipmiMsg{dataLen: uint16(len(rbuf)), data: unsafe.Pointer(&rbuf[0])}}
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, uintptr(ipmictlReceiveMsgTrnc), uintptr(unsafe.Pointer(&recv)))
		runtime.KeepAlive(&raddr)
		runtime.KeepAlive(rbuf)
		if errno == unix.EAGAIN || errno == unix.EINTR {
			continue
		}
		if errno != 0 && errno != unix.EMSGSIZE {
			return nil, fmt.Errorf("%s: %w", name, errno)
		}
		if recv.msgid != d.msgid {
			continue // an older answer that timed out
		}
		resp := rbuf[:recv.msg.dataLen]
		if len(resp) == 0 {
			return nil, fmt.Errorf("%s: empty answer", name)
		}
		if resp[0] != 0 {
			return nil, &CompletionError{Cmd: name, Code: resp[0]}
		}
		return append([]byte(nil), resp[1:]...), nil
	}
}

func (d *device) close() error { return d.f.Close() }
