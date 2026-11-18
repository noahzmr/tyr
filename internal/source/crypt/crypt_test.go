package crypt

import (
	"encoding/binary"
	"testing"
)

func TestDMKind(t *testing.T) {
	for uuid, want := range map[string][2]string{
		"CRYPT-LUKS2-3f2a9c0d8e7b4a1f9c2d3e4f5a6b7c8d-luks-3f2a": {"crypt", "LUKS2"},
		"CRYPT-PLAIN-cryptswap":                                  {"crypt", "PLAIN"},
		"CRYPT-SUBDEV-abc-temporary":                             {"other", "SUBDEV"},
		"LVM-abcdef":                                             {"lvm", ""},
		"mpath-3600508b1001c":                                    {"multipath", ""},
		"":                                                       {"other", ""},
	} {
		k, ty := dmKind(uuid)
		if k != want[0] || ty != want[1] {
			t.Errorf("%q: %s/%s, want %v", uuid, k, ty, want)
		}
	}
}

func luks1() []byte {
	b := make([]byte, 1024)
	copy(b, luksMagic)
	binary.BigEndian.PutUint16(b[6:], 1)
	copy(b[8:], "aes")
	copy(b[40:], "xts-plain64")
	binary.BigEndian.PutUint32(b[108:], 64)
	copy(b[168:], "0b1c2d3e-aaaa-bbbb-cccc-0123456789ab")
	return b
}

func TestParseLUKS(t *testing.T) {
	l, err := ParseLUKS(luks1())
	if err != nil || l.Version != 1 || l.Cipher != "aes-xts-plain64" || l.KeyBits != 512 || l.UUID != "0b1c2d3e-aaaa-bbbb-cccc-0123456789ab" {
		t.Fatalf("luks1: %+v %v", l, err)
	}
	b := make([]byte, 4096, 16384)
	copy(b, luksMagic)
	binary.BigEndian.PutUint16(b[6:], 2)
	binary.BigEndian.PutUint64(b[8:], 16384)
	js := `{"keyslots":{"0":{"type":"luks2","key_size":64}},"segments":{"0":{"type":"crypt","encryption":"aes-xts-plain64","sector_size":512}}}`
	b = append(b, js...)
	b = append(b, make([]byte, 16384-len(b))...)
	l, err = ParseLUKS(b)
	if err != nil || l.Version != 2 || l.Cipher != "aes-xts-plain64" || l.KeyBits != 512 {
		t.Fatalf("luks2: %+v %v", l, err)
	}
	if _, err := ParseLUKS(make([]byte, 4096)); err == nil {
		t.Error("zeros parsed as LUKS")
	}
}

// discovery builds a Level 0 response with the given feature descriptors.
func discovery(feats ...[]byte) []byte {
	b := make([]byte, 48)
	for _, f := range feats {
		b = append(b, f...)
	}
	binary.BigEndian.PutUint32(b, uint32(len(b)-4))
	return append(b, make([]byte, 64)...) // trailing junk beyond the length
}

func feat(code uint16, data ...byte) []byte {
	b := make([]byte, 4, 4+len(data))
	binary.BigEndian.PutUint16(b, code)
	b[2] = 0x10
	b[3] = byte(len(data))
	return append(b, data...)
}

func TestParseDiscovery(t *testing.T) {
	// Opal 2 drive, locking set up and currently unlocked, media encrypted
	s, err := ParseDiscovery(discovery(feat(0x0001, 0x11, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
		feat(0x0002, 0x0b, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0), feat(0x0203, make([]byte, 16)...)))
	if err != nil || s.SSC != "Opal 2" || !s.LockingSupported || !s.LockingEnabled || s.Locked || !s.MediaEncryption {
		t.Fatalf("opal: %+v %v", s, err)
	}
	// Pyrite: locking without encryption
	s, err = ParseDiscovery(discovery(feat(0x0002, 0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0), feat(0x0303, make([]byte, 16)...)))
	if err != nil || s.SSC != "Pyrite 2" || s.MediaEncryption || s.LockingEnabled {
		t.Fatalf("pyrite: %+v %v", s, err)
	}
	if _, err := ParseDiscovery(discovery()); err == nil {
		t.Error("empty discovery accepted")
	}
}
