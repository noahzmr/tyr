// Package crypt finds out whether block devices are encrypted: dm-crypt
// mappings and their LUKS headers, the block device stack between a disk
// and a filesystem, and what self-encrypting drives report in their TCG
// Storage discovery. Everything here only reads.
package crypt

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/noahzmr/tyr/internal/hostfs"
)

// DM is one device-mapper device.
type DM struct {
	Dev    string // dm-0
	Name   string // luks-3f2…, vg0-root, mpatha
	UUID   string
	Kind   string // crypt, lvm, multipath, other
	Type   string // crypt: LUKS1, LUKS2, PLAIN, BITLK, TCRYPT, FVAULT2
	Slaves []string
}

// ReadDM lists the device-mapper devices from /sys/block/dm-*.
func ReadDM(f hostfs.FS) []DM {
	dirs, _ := f.Glob("/sys/block/dm-*")
	var out []DM
	for _, d := range dirs {
		dm := DM{Dev: path.Base(d), Name: hostfs.StringOr(f, d+"/dm/name", ""), UUID: hostfs.StringOr(f, d+"/dm/uuid", "")}
		dm.Kind, dm.Type = dmKind(dm.UUID)
		dm.Slaves = Slaves(f, dm.Dev)
		out = append(out, dm)
	}
	sort.Slice(out, func(i, j int) bool { return natLess(out[i].Dev, out[j].Dev) })
	return out
}

// dmKind reads the subsystem prefix of a dm uuid: CRYPT-LUKS2-<uuid>-<name>,
// LVM-<vg><lv>, mpath-<wwid>.
func dmKind(uuid string) (kind, typ string) {
	switch {
	case strings.HasPrefix(uuid, "CRYPT-"):
		t := strings.TrimPrefix(uuid, "CRYPT-")
		if i := strings.IndexByte(t, '-'); i > 0 {
			t = t[:i]
		}
		if t == "SUBDEV" || t == "VERITY" || t == "INTEGRITY" { // helper devices, not encryption
			return "other", t
		}
		return "crypt", t
	case strings.HasPrefix(uuid, "LVM-"):
		return "lvm", ""
	case strings.HasPrefix(uuid, "mpath-"):
		return "multipath", ""
	}
	return "other", ""
}

// Slaves are the devices a stacked device (dm, md) is built on.
func Slaves(f hostfs.FS, dev string) []string { return names(f, "/sys/class/block/"+dev+"/slaves") }

// Holders are the stacked devices built on dev.
func Holders(f hostfs.FS, dev string) []string { return names(f, "/sys/class/block/"+dev+"/holders") }

func names(f hostfs.FS, dir string) []string {
	es, _ := f.ReadDir(dir)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	sort.Slice(out, func(i, j int) bool { return natLess(out[i], out[j]) })
	return out
}

// Parent returns the disk a partition belongs to ("" for a whole device).
func Parent(f hostfs.FS, dev string) string {
	if !hostfs.Exists(f, "/sys/class/block/"+dev+"/partition") {
		return ""
	}
	if real, err := f.EvalSymlinks("/sys/class/block/" + dev); err == nil {
		return path.Base(path.Dir(real))
	}
	return ""
}

// Partitions lists the partitions of a disk.
func Partitions(f hostfs.FS, disk string) []string {
	ps, _ := f.Glob("/sys/class/block/" + disk + "/" + disk + "*/partition")
	var out []string
	for _, p := range ps {
		out = append(out, path.Base(path.Dir(p)))
	}
	sort.Slice(out, func(i, j int) bool { return natLess(out[i], out[j]) })
	return out
}

// NameByMajMin resolves "253:0" to "dm-0".
func NameByMajMin(f hostfs.FS, mm string) string {
	if mm == "" || mm == "0:0" {
		return ""
	}
	if real, err := f.EvalSymlinks("/sys/dev/block/" + mm); err == nil {
		return path.Base(real)
	}
	return ""
}

// DevName resolves a device path (/dev/mapper/x, /dev/sda3,
// /dev/disk/by-uuid/…) to its kernel name.
func DevName(f hostfs.FS, p string) string {
	if !strings.HasPrefix(p, "/dev/") {
		return ""
	}
	if real, err := f.EvalSymlinks(p); err == nil && strings.HasPrefix(real, "/dev/") {
		p = real
	}
	n := strings.TrimPrefix(p, "/dev/")
	if strings.Contains(n, "/") {
		return ""
	}
	if hostfs.Exists(f, "/sys/class/block/"+n) {
		return n
	}
	return ""
}

// --- LUKS -------------------------------------------------------------------

// LUKS is what a LUKS header tells without any key: the version and the
// cipher. Key slots stay encrypted; Tyr never reads or stores key material.
type LUKS struct {
	Version int
	Cipher  string // aes-xts-plain64
	KeyBits int
	UUID    string
}

var luksMagic = []byte{'L', 'U', 'K', 'S', 0xba, 0xbe}

// ReadLUKS reads the LUKS header at the start of a device (root).
func ReadLUKS(f hostfs.FS, dev string) (*LUKS, error) {
	fh, err := f.Open(dev)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	head := make([]byte, 4096)
	if _, err := io.ReadFull(fh, head); err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(head, luksMagic) {
		return nil, errors.New("no LUKS header")
	}
	if binary.BigEndian.Uint16(head[6:]) == 2 {
		size := binary.BigEndian.Uint64(head[8:])
		if size < 4096 || size > 4<<20 {
			return nil, fmt.Errorf("LUKS2 header size %d", size)
		}
		rest := make([]byte, size-4096)
		if _, err := io.ReadFull(fh, rest); err != nil {
			return nil, err
		}
		head = append(head, rest...)
	}
	return ParseLUKS(head)
}

// ParseLUKS decodes a LUKS1 or LUKS2 header.
func ParseLUKS(b []byte) (*LUKS, error) {
	if len(b) < 592 || !bytes.HasPrefix(b, luksMagic) {
		return nil, errors.New("no LUKS header")
	}
	cstr := func(x []byte) string {
		if i := bytes.IndexByte(x, 0); i >= 0 {
			x = x[:i]
		}
		return strings.TrimSpace(string(x))
	}
	switch v := binary.BigEndian.Uint16(b[6:]); v {
	case 1:
		l := &LUKS{Version: 1, UUID: cstr(b[168:208])}
		name, mode := cstr(b[8:40]), cstr(b[40:72])
		l.Cipher = strings.Trim(name+"-"+mode, "-")
		l.KeyBits = int(binary.BigEndian.Uint32(b[108:])) * 8
		return l, nil
	case 2:
		l := &LUKS{Version: 2, UUID: cstr(b[168:208])}
		if len(b) <= 4096 {
			return l, nil
		}
		js := b[4096:]
		if i := bytes.IndexByte(js, 0); i >= 0 {
			js = js[:i]
		}
		var meta struct {
			Keyslots map[string]struct {
				KeySize int `json:"key_size"`
			} `json:"keyslots"`
			Segments map[string]struct {
				Type       string `json:"type"`
				Encryption string `json:"encryption"`
			} `json:"segments"`
		}
		if err := json.Unmarshal(js, &meta); err != nil {
			return l, nil // the binary header alone still says LUKS2
		}
		for _, k := range sortedKeys(meta.Segments) {
			if s := meta.Segments[k]; s.Type == "crypt" {
				l.Cipher = s.Encryption
				break
			}
		}
		for _, k := range sortedKeys(meta.Keyslots) {
			if n := meta.Keyslots[k].KeySize; n > 0 {
				l.KeyBits = n * 8
				break
			}
		}
		return l, nil
	default:
		return nil, fmt.Errorf("LUKS version %d", v)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// natLess orders sda2 before sda10.
func natLess(a, b string) bool {
	ta, tb := strings.TrimRight(a, "0123456789"), strings.TrimRight(b, "0123456789")
	if ta != tb {
		return a < b
	}
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}
