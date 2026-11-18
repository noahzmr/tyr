package storage

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/crypt"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

// sedMaxAge: a drive's TCG state changes only when someone sets locking up.
const sedMaxAge = 10 * time.Minute

type sedCache struct {
	sed *model.SED
	at  time.Time
}

type zfsCache struct {
	at  time.Time
	enc map[string][2]string // dataset → encryption, keystatus
}

// Partition type GUIDs that hold no data worth encrypting.
var bootPartTypes = map[string]bool{
	"c12a7328-f81f-11d2-ba4b-00a0c93ec93b": true, // EFI system
	"21686148-6449-6e6f-744e-656564454649": true, // BIOS boot
	"bc13c2ff-59e6-4262-a352-b275fd6f7172": true, // XBOOTLDR (/boot)
}

// bootMount: mountpoints that stay unencrypted for the firmware or the
// boot loader to read.
func bootMount(mp string) bool {
	return mp == "/boot" || mp == "/boot/efi" || mp == "/efi" || strings.HasPrefix(mp, "/boot/")
}

// encryption works out, for every disk, filesystem, swap area and Longhorn
// volume, whether its data is encrypted at rest and by which layer.
func (c *Collector) encryption(ctx context.Context, env *collector.Env, out *model.Storage, h *model.Header,
	mounts []sysfs.Mount, devs []sysfs.BlockDev) {
	f := env.FS
	root := env.Priv.Root && f.IsHost()
	now := env.Now()

	// dm-crypt mappings
	dms := crypt.ReadDM(f)
	cryptOf := map[string]*model.CryptDevice{}
	isDM := map[string]crypt.DM{}
	for _, dm := range dms {
		isDM[dm.Dev] = dm
		if dm.Kind != "crypt" {
			continue
		}
		cd := model.CryptDevice{Name: dm.Name, DM: dm.Dev, Type: dm.Type, Backing: dm.Slaves}
		if strings.HasPrefix(dm.Type, "LUKS") && len(dm.Slaves) > 0 && root {
			c.mu.Lock()
			l, ok := c.luks[dm.Slaves[0]]
			c.mu.Unlock()
			if !ok {
				l, _ = crypt.ReadLUKS(f, "/dev/"+dm.Slaves[0])
				c.mu.Lock()
				c.luks[dm.Slaves[0]] = l
				c.mu.Unlock()
			}
			if l != nil {
				cd.Cipher, cd.KeyBits = l.Cipher, l.KeyBits
			}
		}
		out.Crypt = append(out.Crypt, cd)
	}
	for i := range out.Crypt {
		cryptOf[out.Crypt[i].DM] = &out.Crypt[i]
	}

	// self-encrypting drives
	disks := map[string]*model.Disk{}
	for i := range out.Disks {
		disks[out.Disks[i].Name] = &out.Disks[i]
	}
	if root {
		for _, b := range devs {
			d := disks[b.Name]
			if d == nil || d.Remote != nil || d.Kind == "virtual" {
				continue
			}
			c.mu.Lock()
			sc, ok := c.sed[b.Name]
			c.mu.Unlock()
			if !ok || now.Sub(sc.at) > sedMaxAge {
				sc = sedCache{at: now}
				if raw, src, err := crypt.ReadDiscovery("/dev/"+b.Name, b.NVMeCtrl); err == nil {
					if s, err := crypt.ParseDiscovery(raw); err == nil {
						s.Source = src
						sc.sed = s
					}
				}
				c.mu.Lock()
				c.sed[b.Name] = sc
				c.mu.Unlock()
			}
			if s := sc.sed; s != nil {
				cp := *s
				if d.SED != nil {
					cp.ATASecurity = d.SED.ATASecurity
				}
				d.SED = &cp
			}
		}
	}

	// controller encryption: every logical drive of the only controller of
	// a driver reports it (HPE Secure Encryption, Redfish Volume.Encrypted)
	ctrlEnc := map[string]string{} // driver → controller model
	for _, rc := range out.Controllers {
		all := len(rc.VDs) > 0
		for _, v := range rc.VDs {
			all = all && v.Encrypted != nil && *v.Encrypted
		}
		if !all {
			continue
		}
		for _, drv := range ctrlDrivers(rc) {
			if _, dup := ctrlEnc[drv]; dup {
				ctrlEnc[drv] = "" // two such controllers: no telling which disk is which
			} else {
				ctrlEnc[drv] = rc.Model
			}
		}
	}

	var down func(dev string, depth int) *model.Encryption
	down = func(dev string, depth int) *model.Encryption {
		if depth > 8 || dev == "" {
			return nil
		}
		if cd := cryptOf[dev]; cd != nil {
			return &model.Encryption{Protected: true, Method: cryptDesc(cd), Via: dev + " (" + cd.Name + ")"}
		}
		if sl := crypt.Slaves(f, dev); len(sl) > 0 { // LVM, multipath, md
			var first *model.Encryption
			var plain []string
			for _, s := range sl {
				e := down(s, depth+1)
				if e != nil && e.Protected {
					if first == nil {
						first = e
					}
				} else {
					plain = append(plain, s)
				}
			}
			switch {
			case len(plain) == 0:
				return first
			case first != nil:
				return &model.Encryption{Method: first.Method, Via: first.Via, Note: "only partly encrypted: " + strings.Join(plain, ", ") + " not"}
			}
			return &model.Encryption{}
		}
		disk := dev
		if p := crypt.Parent(f, dev); p != "" {
			disk = p
		}
		if d := disks[disk]; d != nil {
			if e := sedEncryption(d); e != nil {
				return e
			}
			if m, ok := ctrlEnc[diskDriver(d)]; ok && m != "" && d.Remote == nil {
				return &model.Encryption{Protected: true, Method: "RAID controller encryption", Via: m}
			}
		}
		e := &model.Encryption{}
		if !root {
			e.Note = "self-encrypting drive state needs root"
		}
		return e
	}

	// covered: everything stacked on dev ends in an encryption layer
	var covered func(dev string, depth int) (bool, string)
	covered = func(dev string, depth int) (bool, string) {
		if depth > 8 {
			return false, ""
		}
		if dm, ok := isDM[dev]; ok && dm.Kind == "crypt" {
			return true, dev
		}
		hs := crypt.Holders(f, dev)
		if len(hs) == 0 {
			return false, ""
		}
		via := ""
		for _, hd := range hs {
			ok, v := covered(hd, depth+1)
			if !ok {
				return false, ""
			}
			if via == "" {
				via = v
			}
		}
		return true, via
	}

	mountedAt := map[string][]string{}
	for _, m := range mounts {
		if n := crypt.DevName(f, m.Source); n != "" {
			mountedAt[n] = append(mountedAt[n], m.Mountpoint)
		}
	}
	for _, cd := range out.Crypt {
		cd := cryptOf[cd.DM]
		cd.UsedBy = append(cd.UsedBy, mountedAt[cd.DM]...)
		for _, hd := range crypt.Holders(f, cd.DM) {
			if dm, ok := isDM[hd]; ok && dm.Kind == "lvm" {
				cd.UsedBy = append(cd.UsedBy, "LVM "+dm.Name)
			}
		}
	}

	// disks: SED with locking, or every data partition under dm-crypt
	for i := range out.Disks {
		d := &out.Disks[i]
		if e := sedEncryption(d); e != nil && e.Protected {
			d.Encryption = e
			continue
		}
		if m, ok := ctrlEnc[diskDriver(d)]; ok && m != "" && d.Remote == nil {
			d.Encryption = &model.Encryption{Protected: true, Method: "RAID controller encryption", Via: m}
			continue
		}
		parts := crypt.Partitions(f, d.Name)
		if len(parts) == 0 {
			parts = []string{d.Name}
		}
		var enc, plain []string
		via, closed := "", ""
		for _, p := range parts {
			props := sysfs.UdevProps(f, p)
			if bootPartTypes[strings.ToLower(props["ID_PART_ENTRY_TYPE"])] || slices.ContainsFunc(mountedAt[p], bootMount) {
				continue
			}
			ok, v := covered(p, 0)
			switch {
			case !ok && (props["ID_FS_TYPE"] == "crypto_LUKS" || props["ID_FS_TYPE"] == "BitLocker"):
				// encrypted but not opened (the live ISO, a data disk not in use)
				enc = append(enc, p)
				if closed == "" {
					closed = props["ID_FS_TYPE"]
					if root {
						if l, err := crypt.ReadLUKS(f, "/dev/"+p); err == nil {
							closed = fmt.Sprintf("LUKS%d %s %d-bit", l.Version, l.Cipher, l.KeyBits)
						}
					}
				}
			case ok:
				enc = append(enc, p)
				if via == "" {
					via = v
				}
			case props["ID_FS_TYPE"] == "" && len(crypt.Holders(f, p)) == 0 && len(mountedAt[p]) == 0 && p != d.Name:
				// an empty or unknown partition: nothing on it to judge
			case p == d.Name && len(crypt.Holders(f, p)) == 0 && len(mountedAt[p]) == 0 && props["ID_FS_TYPE"] == "":
				// an unused disk
			default:
				plain = append(plain, p)
			}
		}
		e := sedEncryption(d) // media encryption without locking, as a note
		if e == nil {
			e = &model.Encryption{}
		}
		method, at := cryptDesc(cryptOf[via]), strings.Join(enc, ", ")+" → "+via
		if via == "" {
			method, at = closed, strings.Join(enc, ", ")+" (not opened)"
		}
		switch {
		case len(enc) > 0 && len(plain) == 0:
			e = &model.Encryption{Protected: true, Method: method, Via: at}
		case len(enc) > 0:
			e = &model.Encryption{Method: method, Via: at, Note: "only partly encrypted: " + strings.Join(plain, ", ") + " not"}
		case len(plain) == 0 && e.Method == "":
			continue // nothing on it: neither encrypted nor not
		}
		if !root && !e.Protected && e.Note == "" {
			e.Note = "self-encrypting drive state needs root"
		}
		d.Encryption = e
	}

	// ZFS datasets carry their own encryption
	zfsEnc := c.zfsEncryption(ctx, env, out.Filesystems, now)

	// filesystems
	majmin := map[string]string{}
	for _, m := range mounts {
		if _, ok := majmin[m.Mountpoint]; !ok {
			majmin[m.Mountpoint] = m.MajMin
		}
	}
	for i := range out.Filesystems {
		fs := &out.Filesystems[i]
		if fs.FSType == "zfs" {
			if z, ok := zfsEnc[fs.Source]; ok {
				if z[0] != "" && z[0] != "off" && z[0] != "-" {
					fs.Encryption = &model.Encryption{Protected: true, Method: "ZFS " + z[0], Via: fs.Source}
					if z[1] == "unavailable" {
						fs.Encryption.Note = "key not loaded"
					}
				} else {
					fs.Encryption = &model.Encryption{Via: fs.Source}
				}
			}
			continue
		}
		dev := crypt.DevName(f, fs.Source)
		if dev == "" {
			dev = crypt.NameByMajMin(f, majmin[fs.Mountpoint])
		}
		if dev != "" {
			fs.Encryption = down(dev, 0)
		}
	}

	// swap
	if b, err := f.ReadFile("/proc/swaps"); err == nil {
		for _, line := range strings.Split(string(b), "\n")[1:] {
			x := strings.Fields(line)
			if len(x) < 3 {
				continue
			}
			sw := model.SwapArea{Path: unescapeOctal(x[0]), Type: x[1]}
			if kb, err := strconv.ParseUint(x[2], 10, 64); err == nil {
				sw.SizeBytes = kb << 10
			}
			if sw.Type == "file" {
				if fs := containingFS(out.Filesystems, sw.Path); fs != nil {
					sw.Encryption = fs.Encryption
				}
			} else if dev := crypt.DevName(f, sw.Path); dev != "" {
				sw.Encryption = down(dev, 0)
			}
			out.Swap = append(out.Swap, sw)
		}
	}

	// Longhorn encrypted volumes: dm-crypt on the volume's LUN
	if lh := out.Longhorn; lh != nil {
		for i := range lh.Volumes {
			v := &lh.Volumes[i]
			if v.Device == "" {
				continue
			}
			for _, hd := range crypt.Holders(f, v.Device) {
				if dm, ok := isDM[hd]; ok && dm.Kind == "crypt" {
					v.Encrypted = true
				}
			}
		}
	}

	if env.Opts.RequireEncryption {
		for _, fs := range out.Filesystems {
			if bootMount(fs.Mountpoint) || fs.Encryption == nil || fs.Encryption.Protected {
				continue
			}
			why := "not encrypted"
			if n := fs.Encryption.Note; n != "" {
				why += " (" + n + ")"
			}
			h.Find(model.StatusWarn, "not_encrypted", fs.Mountpoint, "encryption",
				fmt.Sprintf("%s (%s on %s): %s, but encryption is required", fs.Mountpoint, fs.FSType, fs.Source, why))
		}
		// disks with data but no mounted filesystem (the live ISO, unused
		// data disks): judged as a whole
		for _, d := range out.Disks {
			if len(d.Mounted) > 0 || d.Encryption == nil || d.Encryption.Protected || d.Removable || d.Kind == "usb" || d.Remote != nil {
				continue
			}
			h.Find(model.StatusWarn, "not_encrypted", d.Name, "encryption", fmt.Sprintf("%s (%s %s): data on it is not encrypted, but encryption is required",
				d.Name, d.Model, fmt.Sprintf("%.0f GB", float64(d.SizeBytes)/1e9)))
		}
		for _, sw := range out.Swap {
			if sw.Encryption == nil || sw.Encryption.Protected {
				continue
			}
			h.Find(model.StatusWarn, "swap_not_encrypted", sw.Path, "encryption",
				fmt.Sprintf("swap %s is not encrypted, but encryption is required: memory pages reach the disk in clear", sw.Path))
		}
	}
	if len(out.Crypt) > 0 || anySED(out.Disks) {
		h.SetSource("encryption", model.AvailOK, "")
	}
}

// sedEncryption judges a drive's TCG state: protected only with locking set up.
func sedEncryption(d *model.Disk) *model.Encryption {
	s := d.SED
	if s == nil || s.SSC == "" {
		return nil
	}
	e := &model.Encryption{Method: s.SSC + " self-encrypting drive", Via: d.Name}
	switch {
	case s.LockingEnabled && s.MediaEncryption:
		e.Protected = true
		if s.Locked {
			e.Note = "locked"
		}
	case s.LockingEnabled:
		e.Note = "locking without media encryption (" + s.SSC + "): access control, not encryption"
	case s.MediaEncryption:
		e.Note = "encrypts, but locking is not set up: the data is readable by anyone who has the drive"
	default:
		return nil
	}
	return e
}

func anySED(ds []model.Disk) bool {
	for _, d := range ds {
		if d.SED != nil && d.SED.SSC != "" {
			return true
		}
	}
	return false
}

func cryptDesc(cd *model.CryptDevice) string {
	if cd == nil {
		return "dm-crypt"
	}
	s := cd.Type
	if s == "PLAIN" {
		s = "dm-crypt plain"
	}
	if cd.Cipher != "" {
		s += " " + cd.Cipher
	}
	if cd.KeyBits > 0 {
		s += fmt.Sprintf(" %d-bit", cd.KeyBits)
	}
	return s
}

// ctrlDrivers are the kernel drivers of a RAID controller's disks.
func ctrlDrivers(rc model.RAIDController) []string {
	m := strings.ToLower(rc.Model + " " + rc.Tool)
	switch {
	case strings.Contains(m, "smart array") || strings.Contains(m, "smartraid") || strings.Contains(m, "hpe") || strings.Contains(m, " p4") || strings.Contains(m, " e2"):
		return []string{"smartpqi", "hpsa"}
	case strings.Contains(m, "storcli") || strings.Contains(m, "perccli") || strings.Contains(m, "megaraid") || strings.Contains(m, "perc"):
		return []string{"megaraid_sas"}
	}
	return nil
}

// diskDriver is the controller driver from the disk's location text
// ("0000:5c:00.0 (smartpqi) · port 1I").
func diskDriver(d *model.Disk) string {
	if i := strings.Index(d.Location, "("); i >= 0 {
		if j := strings.Index(d.Location[i:], ")"); j > 0 {
			return d.Location[i+1 : i+j]
		}
	}
	return ""
}

func containingFS(fss []model.Filesystem, p string) *model.Filesystem {
	var best *model.Filesystem
	for i := range fss {
		mp := fss[i].Mountpoint
		if (p == mp || strings.HasPrefix(p, strings.TrimSuffix(mp, "/")+"/")) && (best == nil || len(mp) > len(best.Mountpoint)) {
			best = &fss[i]
		}
	}
	return best
}

func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// zfsEncryption asks zfs once a minute for the datasets' encryption.
func (c *Collector) zfsEncryption(ctx context.Context, env *collector.Env, fss []model.Filesystem, now time.Time) map[string][2]string {
	var ds []string
	for _, fs := range fss {
		if fs.FSType == "zfs" {
			ds = append(ds, fs.Source)
		}
	}
	if len(ds) == 0 {
		return nil
	}
	c.mu.Lock()
	z := c.zfs
	c.mu.Unlock()
	if z.enc != nil && now.Sub(z.at) < time.Minute {
		return z.enc
	}
	z = zfsCache{at: now, enc: map[string][2]string{}}
	if _, err := env.Exec.LookPath("zfs"); err == nil {
		args := append([]string{"get", "-H", "-o", "name,property,value", "encryption,keystatus"}, ds...)
		if b, err := env.Exec.Run(ctx, "zfs", args...); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				x := strings.Split(line, "\t")
				if len(x) < 3 {
					continue
				}
				e := z.enc[x[0]]
				if x[1] == "encryption" {
					e[0] = x[2]
				} else {
					e[1] = x[2]
				}
				z.enc[x[0]] = e
			}
		}
	}
	c.mu.Lock()
	c.zfs = z
	c.mu.Unlock()
	return z.enc
}

// fsHealth reads what mounted filesystems recorded about their own errors:
// ext4's error counter and btrfs' per-device error counters (sysfs, no root).
func fsHealth(f hostfs.FS, out *model.Storage, h *model.Header, mounts []sysfs.Mount) {
	majmin := map[string]string{}
	for _, m := range mounts {
		if _, ok := majmin[m.Mountpoint]; !ok {
			majmin[m.Mountpoint] = m.MajMin
		}
	}
	for i := range out.Filesystems {
		fs := &out.Filesystems[i]
		dev := crypt.DevName(f, fs.Source)
		if dev == "" {
			dev = crypt.NameByMajMin(f, majmin[fs.Mountpoint])
		}
		var hl *model.FSHealth
		switch {
		case strings.HasPrefix(fs.FSType, "ext") && dev != "":
			d := "/sys/fs/ext4/" + dev
			n, err := hostfs.Uint(f, d+"/errors_count")
			if err != nil {
				continue
			}
			hl = &model.FSHealth{Errors: &n, Source: "ext4 sysfs"}
			if t, err := hostfs.Uint(f, d+"/first_error_time"); err == nil && t > 0 {
				hl.FirstError = time.Unix(int64(t), 0).UTC().Format("2006-01-02 15:04")
			}
			if t, err := hostfs.Uint(f, d+"/last_error_time"); err == nil && t > 0 {
				hl.LastError = time.Unix(int64(t), 0).UTC().Format("2006-01-02 15:04")
			}
		case fs.FSType == "btrfs" && dev != "":
			ids, _ := f.Glob("/sys/fs/btrfs/*/devices/" + dev)
			if len(ids) == 0 {
				continue
			}
			base := path.Dir(path.Dir(ids[0]))
			stats, _ := f.Glob(base + "/devinfo/*/error_stats")
			if len(stats) == 0 {
				continue
			}
			var total uint64
			var kinds []string
			for _, s := range stats {
				b, _ := f.ReadFile(s)
				for _, line := range strings.Split(string(b), "\n") {
					x := strings.Fields(line)
					if len(x) == 2 {
						if v, err := strconv.ParseUint(x[1], 10, 64); err == nil && v > 0 {
							total += v
							kinds = append(kinds, fmt.Sprintf("%s %d", strings.TrimSuffix(x[0], "_errs"), v))
						}
					}
				}
			}
			hl = &model.FSHealth{Errors: &total, Source: "btrfs device stats"}
			if len(kinds) > 0 {
				hl.LastError = strings.Join(kinds, ", ")
			}
		default:
			continue
		}
		fs.Health = hl
		if hl.Errors != nil && *hl.Errors > 0 {
			last := ""
			if hl.LastError != "" {
				last = ", last: " + hl.LastError
			}
			h.Find(model.StatusWarn, "fs_errors", fs.Mountpoint, "sysfs", fmt.Sprintf(
				"%s (%s on %s): the filesystem recorded %d errors%s - check it (K in the TUI, tyr fsck)", fs.Mountpoint, fs.FSType, fs.Source, *hl.Errors, last))
			fs.Status = model.Worst(fs.Status, model.StatusWarn)
		}
	}
}
