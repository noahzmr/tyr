package crypt

import (
	"encoding/binary"
	"errors"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/smart"
)

// TCG Storage Level 0 Discovery (TCG Storage Architecture Core Spec,
// 3.3.6): a header and feature descriptors. It is the read-only first
// step every Opal tool takes; it needs no password and changes nothing.

// ParseDiscovery decodes a Level 0 Discovery response.
func ParseDiscovery(b []byte) (*model.SED, error) {
	if len(b) < 48 {
		return nil, errors.New("tcg discovery: short response")
	}
	end := int(binary.BigEndian.Uint32(b[0:])) + 4 // the length field does not count itself
	if end < 48 {
		return nil, errors.New("tcg discovery: no features")
	}
	if end > len(b) {
		end = len(b)
	}
	s := &model.SED{}
	seen := false
	for off := 48; off+4 <= end; {
		code := binary.BigEndian.Uint16(b[off:])
		n := int(b[off+3])
		data := b[off+4 : min(off+4+n, end)]
		switch code {
		case 0x0002: // Locking
			if len(data) > 0 {
				x := data[0]
				s.LockingSupported = x&0x01 != 0
				s.LockingEnabled = x&0x02 != 0
				s.Locked = x&0x04 != 0
				s.MediaEncryption = x&0x08 != 0
				s.MBREnabled = x&0x10 != 0
			}
			seen = true
		case 0x0100:
			s.SSC = "Enterprise"
		case 0x0200:
			if s.SSC == "" {
				s.SSC = "Opal 1"
			}
		case 0x0203:
			s.SSC = "Opal 2"
		case 0x0301:
			s.SSC = "Opalite"
		case 0x0302:
			s.SSC = "Pyrite 1"
		case 0x0303:
			s.SSC = "Pyrite 2"
		case 0x0304:
			s.SSC = "Ruby"
		}
		off += 4 + n
	}
	if !seen && s.SSC == "" {
		return nil, errors.New("tcg discovery: no locking feature")
	}
	return s, nil
}

// opalDiscovery mirrors struct opal_discovery (linux/sed-opal.h).
type opalDiscovery struct {
	Data uint64
	Size uint64
}

// IOC_OPAL_DISCOVERY = _IOW('p', 239, struct opal_discovery), Linux 6.4+.
const iocOpalDiscovery = 0x401070EF

// ReadDiscovery asks a drive for its Level 0 Discovery: through the
// kernel's sed-opal ioctl on the block device (NVMe and SCSI/SATA, the
// kernel already probed the drive), else with an NVMe Security Receive on
// the controller. Needs root. It sends no SCSI pass-through, so a drive
// without TCG support counts no rejected command (ioerr_cnt).
func ReadDiscovery(blockDev, nvmeCtrl string) ([]byte, string, error) {
	buf := make([]byte, 2048)
	fd, err := unix.Open(blockDev, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err == nil {
		d := opalDiscovery{Data: uint64(uintptr(unsafe.Pointer(&buf[0]))), Size: uint64(len(buf))}
		r, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), iocOpalDiscovery, uintptr(unsafe.Pointer(&d)))
		unix.Close(fd)
		if errno == 0 { // the ioctl returns the response length (0 on some kernels)
			if r > 0 && int(r) < len(buf) {
				return buf[:r], "sed-opal ioctl", nil
			}
			return buf, "sed-opal ioctl", nil
		}
		if errno != 0 {
			err = errno
		}
	}
	if nvmeCtrl != "" {
		// Security Receive: SECP 0x01, ComID (SPSP) 0x0001, allocation length
		b, nerr := smart.NVMeAdminRead("/dev/"+nvmeCtrl, 0x82, 0, 0x01<<24|0x0001<<8, uint32(len(buf)), len(buf))
		if nerr == nil {
			return b, "nvme security receive", nil
		}
		err = nerr
	}
	if err == nil {
		err = errors.New("no TCG discovery")
	}
	return nil, "", err
}
