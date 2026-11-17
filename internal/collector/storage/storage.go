// Package storage collects disks (inventory + SMART), software and hardware
// RAID, filesystem usage, and block I/O errors.
package storage

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/noahzmr/tyr/internal/check"
	"github.com/noahzmr/tyr/internal/collector"
	"github.com/noahzmr/tyr/internal/execx"
	"github.com/noahzmr/tyr/internal/hostfs"
	"github.com/noahzmr/tyr/internal/model"
	"github.com/noahzmr/tyr/internal/source/kernellog"
	"github.com/noahzmr/tyr/internal/source/raid"
	"github.com/noahzmr/tyr/internal/source/smart"
	"github.com/noahzmr/tyr/internal/source/sysfs"
)

// Thresholds for filesystem usage (percent).
const (
	fsWarn = 85.0
	fsCrit = 95.0
)

// Collector is the storage collector. It keeps SMART results between
// collections (see collector.Options.SMARTInterval) and the SCSI command
// errors its own SMART queries caused, so they are not reported as disk
// errors.
type Collector struct {
	mu    sync.Mutex
	smart map[string]smartCache
	ioerr map[string]*ioState
}

// smartCache is one disk's last SMART read (also a failed one).
type smartCache struct {
	info     *smart.Info
	src, why string
	serial   string
	at       time.Time
}

// ioState tracks a disk's ioerr_cnt over this process: the value when Tyr
// first saw it, and how much of the growth Tyr's own queries caused.
type ioState struct {
	first, tyr uint64
	// errors that rose on every disk of a controller at once (rejected
	// commands of a monitoring agent), and growth not yet matched
	shared, pend  uint64
	pendSince     time.Time
	last, lastTyr uint64
	lastKernel    int
	seen          bool
}

// sharedHold is how long growth on one disk waits for the same growth on
// the other disks of its controller before it counts as the disk's own.
const sharedHold = 90 * time.Second

// New returns a storage collector.
func New() *Collector {
	return &Collector{smart: map[string]smartCache{}, ioerr: map[string]*ioState{}}
}

func (*Collector) ID() model.ModuleID { return model.ModStorage }

func (c *Collector) Collect(ctx context.Context, env *collector.Env, prevSec model.Section) (model.Section, error) {
	f := env.FS
	out := &model.Storage{Disks: []model.Disk{}, MDArrays: []model.MDArray{}, Controllers: []model.RAIDController{}, Filesystems: []model.Filesystem{}}
	h := &out.Header
	if !hostfs.Exists(f, "/sys/block") {
		return nil, errors.New("/sys/block not available")
	}
	h.SetSource("sysfs", model.AvailOK, "")

	var mounts []sysfs.Mount
	if b, err := f.ReadFile("/proc/self/mountinfo"); err == nil {
		mounts = sysfs.ParseMountinfo(b)
	}

	// --- disks ----------------------------------------------------------
	devs := sysfs.ReadBlockDevs(f)
	klog, kst := env.KernelLog(ctx)
	h.Sources = append(h.Sources, kst)
	ioErrs := kernelIOErrors(klog, devs)
	c.mu.Lock()
	for _, b := range devs {
		if b.IOErrors == nil {
			continue
		}
		st := c.ioerr[b.Name]
		if st == nil || *b.IOErrors < st.first+st.tyr { // first sight, or the counter was reset (reboot, driver reload)
			c.ioerr[b.Name] = &ioState{first: *b.IOErrors}
		}
	}
	c.mu.Unlock()
	for _, b := range devs {
		d := model.Disk{
			Name: b.Name, Path: "/dev/" + b.Name, Model: b.Model, Vendor: b.Vendor, Serial: b.Serial, WWN: b.WWN, Firmware: b.Firmware,
			SizeBytes: b.SizeBytes, Interface: b.Interface, Removable: b.Removable, ReadOnly: b.ReadOnly,
			IOErrors: b.IOErrors, KernelErrors: ioErrs[b.Name].n, KernelLast: ioErrs[b.Name].last, Holders: b.Holders,
		}
		d.Kind = diskKind(b)
		if r := b.Remote; r != nil {
			d.Remote = &model.RemoteDisk{Transport: r.Transport, Target: r.Target, Portal: r.Portal, Session: r.Session, State: r.State}
		}
		d.Location = b.Location.String()
		d.KernelLines = kernelLines(klog, b)
		for _, m := range mounts {
			src := strings.TrimPrefix(m.Source, "/dev/")
			if src == b.Name || slices.Contains(b.Partitions, src) {
				d.Mounted = append(d.Mounted, m.Mountpoint)
			}
		}
		nvmeTemp(f, b, &d)
		out.Disks = append(out.Disks, d)
	}
	c.smartAll(ctx, env, out, devs)
	c.ioErrorAccounting(f, out, env.Now())
	for i := range out.Disks {
		diskFindings(h, &out.Disks[i], env.Opts.Thresholds)
	}

	// --- software RAID ----------------------------------------------------------
	if b, err := f.ReadFile("/proc/mdstat"); err == nil {
		arrays, err := raid.ParseMdstat(b)
		if err != nil {
			h.SetSource("mdstat", model.AvailUnavailable, err.Error())
		}
		for i := range arrays {
			a := &arrays[i]
			if a.Members == nil {
				a.Members = []model.MDMember{}
			}
			mdFindings(h, a)
		}
		out.MDArrays = append(out.MDArrays, arrays...)
	}

	// --- hardware RAID --------------------------------------------------------
	c.hwRAID(ctx, env, out)

	// --- filesystems --------------------------------------------------------------
	out.Filesystems = filesystems(env, mounts)
	for _, fs := range out.Filesystems {
		switch {
		case fs.ReadOnly:
			h.Find(model.StatusCrit, "fs_read_only", fs.Mountpoint, "mountinfo",
				fmt.Sprintf("%s (%s) is mounted read-only", fs.Mountpoint, fs.FSType))
		}
		if fs.UsedPercent != nil && *fs.UsedPercent >= fsWarn {
			st := model.StatusWarn
			if *fs.UsedPercent >= fsCrit {
				st = model.StatusCrit
			}
			h.Find(st, "fs_full", fs.Mountpoint, "statfs", fmt.Sprintf("%s %.0f%% full", fs.Mountpoint, *fs.UsedPercent))
		}
		if fs.InodesUsedPercent != nil && *fs.InodesUsedPercent >= fsWarn {
			st := model.StatusWarn
			if *fs.InodesUsedPercent >= fsCrit {
				st = model.StatusCrit
			}
			h.Find(st, "fs_inodes", fs.Mountpoint, "statfs", fmt.Sprintf("%s inodes %.0f%% used", fs.Mountpoint, *fs.InodesUsedPercent))
		}
	}

	h.Summary = summary(out)
	h.Rollup()
	return out, nil
}

func diskKind(b sysfs.BlockDev) string {
	switch {
	case b.Remote != nil:
		return b.Remote.Transport
	case strings.HasPrefix(b.Interface, "nvme"):
		return "nvme"
	case b.Interface == "virtio" || b.Interface == "xen":
		return "virtual"
	case b.Interface == "usb":
		return "usb"
	case b.Interface == "mmc":
		return "mmc"
	case b.Rotational:
		return "hdd"
	}
	return "ssd"
}

// nvmeTemp reads the NVMe hwmon sensor ("Composite") with the controller's
// own warning/critical composite temperature thresholds (WCTEMP/CCTEMP).
func nvmeTemp(f hostfs.FS, b sysfs.BlockDev, d *model.Disk) {
	if b.NVMeCtrl == "" || b.SysPath == "" {
		return
	}
	for _, chip := range sysfs.ReadHwmon(f) {
		if chip.Name != "nvme" || !strings.HasPrefix(chip.Device+"/", b.SysPath+"/") && chip.Device != b.SysPath {
			continue
		}
		for _, s := range chip.Sensors {
			if s.Kind == model.KindTemperature && s.Input != nil && (s.Index == 1 || s.Label == "Composite") {
				d.TempCelsius = s.Input
				d.TempThreshold = s.Thresholds()
				return
			}
		}
	}
}

// kernelIOErrors counts kernel log I/O errors per disk (ataN is mapped to
// the disk whose sysfs path contains it; partitions map to their disk).
// kernelIO is a disk's I/O errors in the kernel log: how many, and the
// latest line (with its time) for troubleshooting.
type kernelIO struct {
	n    int
	last string
}

func kernelIOErrors(l *kernellog.Log, devs []sysfs.BlockDev) map[string]kernelIO {
	out := map[string]kernelIO{}
	if l == nil {
		return out
	}
	for _, e := range l.ByCategory(kernellog.CatIO) {
		if e.Severity() < model.StatusWarn {
			continue
		}
		for _, b := range devs {
			if e.Subject == b.Name || slices.Contains(b.Partitions, e.Subject) || e.Subject == b.NVMeCtrl ||
				(strings.HasPrefix(e.Subject, "ata") && strings.Contains(b.SysPath, "/"+e.Subject+"/")) {
				k := out[b.Name]
				k.n++
				k.last = strings.TrimSpace(e.Message)
				if !e.Time.IsZero() {
					k.last = e.Time.UTC().Format("2006-01-02 15:04:05") + " " + k.last
				}
				out[b.Name] = k
			}
		}
	}
	return out
}

func (c *Collector) smartAll(ctx context.Context, env *collector.Env, out *model.Storage, devs []sysfs.BlockDev) {
	h := &out.Header
	switch {
	case env.Opts.NoSMART:
		h.SetSource("smart", model.AvailSkipped, "disabled by --no-smart")
		return
	}
	_, ctlErr := env.Exec.LookPath("smartctl")
	_, nvmeErr := env.Exec.LookPath("nvme")
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	reasons := map[string]int{}
	used := map[string]bool{}
	maxAge := collector.SMARTMaxAge(ctx, env.Opts.SMARTInterval)
	cached := 0
	for i := range out.Disks {
		d := &out.Disks[i]
		b := devs[i]
		if d.Kind == "virtual" || d.Kind == "mmc" || !d.Local() {
			continue // a volume's SMART belongs to the storage system
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			info, src, why, at, fromCache := c.smartFor(ctx, env, b, maxAge, ctlErr == nil, nvmeErr == nil)
			mu.Lock()
			defer mu.Unlock()
			if fromCache {
				cached++
			}
			d.SMARTAt = at
			if info == nil {
				reasons[why]++
				return
			}
			used[src] = true
			d.SMART = &info.SMART
			if d.Model == "" {
				d.Model = info.Model
			}
			if d.Serial == "" {
				d.Serial = info.Serial
			}
			if d.Firmware == "" {
				d.Firmware = info.Firmware
			}
			if info.RotationRPM != nil {
				d.RotationRPM = info.RotationRPM
			}
			if d.TempCelsius == nil {
				d.TempCelsius = info.TempC
			}
		}()
	}
	wg.Wait()
	switch {
	case len(used) > 0:
		var srcs []string
		for s := range used {
			srcs = append(srcs, s)
		}
		sort.Strings(srcs)
		reason := "via " + strings.Join(srcs, ", ")
		for why, n := range reasons {
			reason += fmt.Sprintf("; %d disks: %s", n, why)
		}
		if maxAge > 0 {
			reason += fmt.Sprintf("; read every %s", maxAge)
			if cached > 0 {
				reason += fmt.Sprintf(" (%d disks from the last read)", cached)
			}
		}
		h.SetSource("smart", model.AvailOK, reason)
	case len(reasons) > 0:
		var rs []string
		for why := range reasons {
			rs = append(rs, why)
		}
		sort.Strings(rs)
		h.SetSource("smart", model.AvailUnavailable, strings.Join(rs, "; "))
	case env.IsVM():
		h.SetSource("smart", model.AvailNotApplicable, "virtual disks")
	}
}

// smartFor returns a disk's SMART data: the cached read if it is younger
// than maxAge and the disk is the same (serial), else a new read. Around a
// new read it samples ioerr_cnt, so the SCSI errors the query itself
// causes (commands the drive or controller rejects) are known.
func (c *Collector) smartFor(ctx context.Context, env *collector.Env, b sysfs.BlockDev, maxAge time.Duration, haveCtl, haveNVMe bool) (*smart.Info, string, string, time.Time, bool) {
	c.mu.Lock()
	e, ok := c.smart[b.Name]
	c.mu.Unlock()
	if ok && maxAge > 0 && e.serial == b.Serial && env.Now().Sub(e.at) < maxAge {
		return e.info, e.src, e.why, e.at, true
	}
	before, okB := sysfs.ReadIOErrCount(env.FS, b.Name)
	info, src, why := readSMART(ctx, env, b, haveCtl, haveNVMe)
	after, okA := sysfs.ReadIOErrCount(env.FS, b.Name)
	at := env.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if okB && okA && after > before {
		if st := c.ioerr[b.Name]; st != nil {
			st.tyr += after - before
		}
	}
	c.smart[b.Name] = smartCache{info: info, src: src, why: why, serial: b.Serial, at: at}
	return info, src, why, at, false
}

// ioErrorAccounting re-reads ioerr_cnt after the SMART queries and splits
// it into what Tyr caused and what is new since Tyr started.
func (c *Collector) ioErrorAccounting(f hostfs.FS, out *model.Storage, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	groups := map[string][]*ioState{}
	for i := range out.Disks {
		d := &out.Disks[i]
		st := c.ioerr[d.Name]
		if d.IOErrors == nil || st == nil {
			continue
		}
		if v, ok := sysfs.ReadIOErrCount(f, d.Name); ok {
			d.IOErrors = &v
		}
		// growth since the last round that Tyr did not cause
		if st.seen && *d.IOErrors >= st.last {
			grow := *d.IOErrors - st.last
			grow -= min(grow, st.tyr-st.lastTyr)
			if grow > 0 && d.KernelErrors == st.lastKernel {
				if st.pend == 0 {
					st.pendSince = now
				}
				st.pend += grow
			}
		}
		st.last, st.lastTyr, st.lastKernel, st.seen = *d.IOErrors, st.tyr, d.KernelErrors, true
		if ctrl, _, _ := strings.Cut(d.Location, " ·"); ctrl != "" && d.Local() {
			groups[ctrl] = append(groups[ctrl], st)
		}
	}
	// the same growth on every disk of a controller (3 or more): rejected
	// commands of a monitoring agent, not errors of the disks
	grouped := map[*ioState]bool{}
	for _, g := range groups {
		if len(g) < 3 {
			continue
		}
		for _, st := range g {
			grouped[st] = true
		}
		if g[0].pend == 0 {
			continue
		}
		same := true
		for _, st := range g[1:] {
			same = same && st.pend == g[0].pend
		}
		if same {
			for _, st := range g {
				st.shared += st.pend
				st.pend = 0
			}
		}
	}
	for i := range out.Disks {
		d := &out.Disks[i]
		st := c.ioerr[d.Name]
		if d.IOErrors == nil || st == nil {
			continue
		}
		tyr, shared := st.tyr, st.shared
		d.IOErrorsTyr = &tyr
		if shared > 0 {
			d.IOErrorsShared = &shared
		}
		held := uint64(0)
		if st.pend > 0 && grouped[st] && now.Sub(st.pendSince) < sharedHold {
			held = st.pend // may still turn out to be controller-wide
		}
		var fresh uint64
		if base := st.first + st.tyr + st.shared + held; *d.IOErrors > base {
			fresh = *d.IOErrors - base
		}
		d.IOErrorsNew = &fresh
	}
}

// readSMART tries, in order: native NVMe ioctl (root), smartctl, nvme-cli.
func readSMART(ctx context.Context, env *collector.Env, b sysfs.BlockDev, haveCtl, haveNVMe bool) (*smart.Info, string, string) {
	var errs []string
	if b.NVMeCtrl != "" && env.FS.IsHost() {
		if !env.Priv.Root {
			errs = append(errs, "NVMe health log needs root")
		} else if raw, err := smart.ReadNVMeLog("/dev/" + b.NVMeCtrl); err == nil {
			if in, err := smart.ParseNVMeLog(raw); err == nil {
				return in, "nvme ioctl", ""
			}
		} else {
			errs = append(errs, "nvme ioctl: "+err.Error())
		}
	}
	if haveCtl {
		out, err := env.Exec.Run(ctx, "smartctl", "-j", "-x", "/dev/"+b.Name)
		var ee *execx.ExitError
		if err == nil || errors.As(err, &ee) && len(out) > 0 {
			in, perr := smart.ParseSmartctl(out)
			if perr == nil {
				return in, "smartctl", ""
			}
			errs = append(errs, perr.Error())
		} else {
			errs = append(errs, err.Error())
		}
	}
	if haveNVMe && b.NVMeCtrl != "" {
		out, err := env.Exec.Run(ctx, "nvme", "smart-log", "/dev/"+b.NVMeCtrl, "-o", "json")
		if err == nil {
			if in, err := smart.ParseNVMeCLI(out); err == nil {
				return in, "nvme-cli", ""
			}
		}
		errs = append(errs, "nvme-cli failed")
	}
	if len(errs) == 0 {
		if b.NVMeCtrl != "" {
			return nil, "", "smartctl/nvme-cli not installed"
		}
		return nil, "", "smartctl not installed"
	}
	return nil, "", errs[0]
}

func diskFindings(h *model.Header, d *model.Disk, def check.Defaults) {
	st := model.StatusOK
	add := func(s model.Status, code, msg string) {
		h.Find(s, code, d.Name, "smart", d.Name+": "+msg)
		st = model.Worst(st, s)
	}
	if s := d.SMART; s != nil {
		if s.Passed != nil && !*s.Passed {
			add(model.StatusCrit, "smart_failed", "SMART overall health FAILED")
		}
		if s.NVMeCriticalWarn != nil && *s.NVMeCriticalWarn != 0 {
			add(model.StatusCrit, "nvme_critical_warning", "NVMe critical warning "+nvmeWarnText(*s.NVMeCriticalWarn))
		}
		if s.AvailableSpare != nil && s.AvailableSpareThr != nil && *s.AvailableSpareThr > 0 && *s.AvailableSpare <= *s.AvailableSpareThr {
			add(model.StatusCrit, "spare_low", fmt.Sprintf("available spare %.0f%% at/below threshold %.0f%%", *s.AvailableSpare, *s.AvailableSpareThr))
		}
		if w := s.WearPercentUsed; w != nil {
			switch {
			case *w >= 100:
				add(model.StatusCrit, "wear", fmt.Sprintf("rated endurance used up (%.0f%%)", *w))
			case *w >= 90:
				add(model.StatusWarn, "wear", fmt.Sprintf("%.0f%% of rated endurance used", *w))
			}
		}
		cnt := func(v *uint64, s model.Status, code, what string) {
			if v != nil && *v > 0 {
				add(s, code, fmt.Sprintf("%d %s", *v, what))
			}
		}
		cnt(s.OfflineUncorrect, model.StatusCrit, "offline_uncorrectable", "offline uncorrectable sectors")
		cnt(s.Pending, model.StatusWarn, "pending_sectors", "pending sectors")
		cnt(s.Reallocated, model.StatusWarn, "reallocated_sectors", "reallocated sectors")
		cnt(s.ReportedUncorrect, model.StatusWarn, "reported_uncorrectable", "reported uncorrectable errors")
		cnt(s.CRCErrors, model.StatusWarn, "crc_errors", "interface CRC errors (cable/backplane)")
		cnt(s.MediaErrors, model.StatusWarn, "media_errors", "media/data integrity errors")
		cnt(s.UncorrectedErrors, model.StatusCrit, "scsi_uncorrected", "uncorrected read/write errors")
		if s.SelfTestFailed {
			add(model.StatusWarn, "self_test_failed", "a SMART self-test failed")
		}
		if s.GrownDefects != nil && *s.GrownDefects > 0 {
			add(model.StatusWarn, "grown_defects", fmt.Sprintf("%d grown defects", *s.GrownDefects))
		}
	}
	// ioerr_cnt also counts commands the device rejects - above all SMART
	// queries a controller does not pass through - so the count since boot
	// alone is no finding; growth Tyr did not cause is.
	if d.IOErrorsNew != nil && *d.IOErrorsNew > 0 {
		add(model.StatusWarn, "io_errors", fmt.Sprintf("%d new SCSI command errors since Tyr started (ioerr_cnt, not from its own SMART queries)", *d.IOErrorsNew))
	}
	last := ""
	if d.KernelLast != "" {
		last = ", last: " + clip(d.KernelLast, 140)
	}
	if d.KernelErrors > 0 && d.Local() {
		h.Find(model.StatusCrit, "kernel_io_errors", d.Name, "kernel_log", fmt.Sprintf("%s: %d I/O errors in kernel log%s", d.Name, d.KernelErrors, last))
		st = model.Worst(st, model.StatusCrit)
	}
	if r := d.Remote; r != nil {
		where := strings.ToUpper(r.Transport)
		if r.Transport == "iscsi" {
			where = "iSCSI"
		}
		if r.Portal != "" {
			where += " " + r.Portal
		}
		if d.KernelErrors > 0 {
			h.Find(model.StatusCrit, "remote_io_errors", d.Name, "kernel_log", fmt.Sprintf(
				"%s (%s): %d I/O errors in kernel log - reads or writes to the storage system failed; check the session, the network path and the target%s",
				d.Name, where, d.KernelErrors, last))
			st = model.Worst(st, model.StatusCrit)
		}
		switch {
		case r.Transport == "iscsi" && r.State != "" && r.State != "LOGGED_IN":
			h.Find(model.StatusCrit, "iscsi_session", d.Name, "sysfs", fmt.Sprintf("%s: iSCSI %s to %s is %s", d.Name, orStr(r.Session, "session"), orStr(r.Target, "the target"), r.State))
			st = model.Worst(st, model.StatusCrit)
		case r.Transport == "fc" && r.State != "" && r.State != "Online":
			h.Find(model.StatusCrit, "fc_port", d.Name, "sysfs", fmt.Sprintf("%s: Fibre Channel port %s is %s", d.Name, orStr(r.Target, r.Session), r.State))
			st = model.Worst(st, model.StatusCrit)
		}
	}
	if d.TempCelsius != nil {
		th := d.TempThreshold
		if th == nil || th.UpperCrit == nil {
			dd := check.Defaults{WarnTempC: 70, CritTempC: 80}
			if d.Kind == "hdd" {
				dd = check.Defaults{WarnTempC: 55, CritTempC: 65}
			}
			th = check.TempThresholds(th, dd)
		}
		d.TempThreshold = th
		if ts := check.Eval(d.TempCelsius, th); ts >= model.StatusWarn {
			h.Find(ts, "disk_temp", d.Name, "smart", fmt.Sprintf("%s at %s", d.Name, check.FmtTemp(*d.TempCelsius)))
			st = model.Worst(st, ts)
		}
	}
	d.Status = st
}

func nvmeWarnText(w uint64) string { return model.NVMeWarningText(w) }

func mdFindings(h *model.Header, a *model.MDArray) {
	var failed []string
	for _, m := range a.Members {
		if m.Failed {
			failed = append(failed, m.Device)
		}
	}
	switch {
	case a.State == "inactive":
		h.Find(model.StatusCrit, "md_inactive", a.Name, "mdstat", a.Name+" is inactive")
	case a.Degraded:
		msg := fmt.Sprintf("%s (%s) degraded: %d/%d devices", a.Name, a.Level, a.ActiveDevices, a.Devices)
		if len(failed) > 0 {
			msg += ", failed " + strings.Join(failed, ", ")
		}
		if a.SyncPercent != nil {
			msg += fmt.Sprintf(", %s %.1f%%", a.SyncAction, *a.SyncPercent)
		}
		h.Find(model.StatusCrit, "md_degraded", a.Name, "mdstat", msg)
	case len(failed) > 0:
		h.Find(model.StatusCrit, "md_member_failed", a.Name, "mdstat", fmt.Sprintf("%s: failed member %s", a.Name, strings.Join(failed, ", ")))
	case a.Status == model.StatusWarn:
		pct := ""
		if a.SyncPercent != nil {
			pct = fmt.Sprintf(" %.1f%%", *a.SyncPercent)
		}
		h.Find(model.StatusWarn, "md_sync", a.Name, "mdstat", fmt.Sprintf("%s %s in progress%s", a.Name, a.SyncAction, pct))
	}
}

func (c *Collector) hwRAID(ctx context.Context, env *collector.Env, out *model.Storage) {
	h := &out.Header
	for _, tool := range []string{"storcli64", "storcli", "perccli64", "perccli"} {
		if _, err := env.Exec.LookPath(tool); err != nil {
			continue
		}
		b, err := env.Exec.Run(ctx, tool, "/call", "show", "all", "J")
		var ee *execx.ExitError
		if err != nil && !(errors.As(err, &ee) && len(b) > 0) {
			h.SetSource("raid_controller", model.AvailUnavailable, err.Error())
			return
		}
		ctrls, err := raid.ParseStorcli(b, strings.TrimSuffix(tool, "64"))
		if err != nil {
			h.SetSource("raid_controller", model.AvailUnavailable, err.Error())
		} else {
			h.SetSource("raid_controller", model.AvailOK, "via "+tool)
		}
		// drive serials, WWNs and firmware need the per-drive detail listing
		if pb, err := env.Exec.Run(ctx, tool, "/call/eall/sall", "show", "all", "J"); err == nil || len(pb) > 0 {
			if det, err := raid.ParseStorcliPDs(pb); err == nil {
				for ci := range ctrls {
					for pi := range ctrls[ci].PDs {
						p := &ctrls[ci].PDs[pi]
						if d, ok := det[ctrls[ci].Index][p.Slot]; ok {
							p.Serial, p.WWN, p.Firmware = d.Serial, d.WWN, d.Firmware
						}
					}
				}
			}
		}
		for _, rc := range ctrls {
			name := fmt.Sprintf("controller %d", rc.Index)
			if rc.State != "" && !strings.EqualFold(rc.State, "Optimal") {
				h.Find(model.StatusWarn, "raid_controller", name, tool, fmt.Sprintf("RAID %s (%s): %s", name, rc.Model, rc.State))
			}
			for _, v := range rc.VDs {
				if v.Status >= model.StatusWarn {
					h.Find(v.Status, "raid_vd", v.ID, tool, fmt.Sprintf("RAID virtual drive %s (%s %s) state %s", v.ID, v.Level, v.Name, v.State))
				}
			}
			for _, p := range rc.PDs {
				if p.Status >= model.StatusWarn {
					h.Find(p.Status, "raid_pd", p.Slot, tool, fmt.Sprintf("RAID physical drive %s (%s) state %s", p.Slot, p.Model, p.State))
				}
			}
			if b := rc.Battery; b != nil && b.Status >= model.StatusWarn {
				h.Find(b.Status, "raid_battery", b.Type, tool, fmt.Sprintf("RAID %s %s: %s", name, b.Type, b.State))
			}
		}
		out.Controllers = append(out.Controllers, ctrls...)
		return
	}
}

// --- filesystems ----------------------------------------------------------

var localFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true,
	"f2fs": true, "vfat": true, "exfat": true, "jfs": true, "reiserfs": true, "bcachefs": true, "ntfs": true, "ntfs3": true}

func filesystems(env *collector.Env, mounts []sysfs.Mount) []model.Filesystem {
	var df map[string]dfRow
	if !env.FS.IsHost() {
		df = readDF(env)
	}
	seen := map[string]int{}
	var out []model.Filesystem
	for _, m := range mounts {
		if !localFS[m.FSType] || strings.HasPrefix(m.Source, "/dev/loop") {
			continue
		}
		// One entry per filesystem: bind mounts and btrfs subvolume repeats
		// keep the shortest mountpoint.
		key := m.MajMin
		if i, ok := seen[key]; ok {
			if len(m.Mountpoint) < len(out[i].Mountpoint) {
				out[i].Mountpoint = m.Mountpoint
			}
			continue
		}
		fs := model.Filesystem{Mountpoint: m.Mountpoint, Source: m.Source, FSType: m.FSType,
			ReadOnly: m.ReadOnly && m.Root == "/" && m.FSType != "vfat" || m.ReadOnly && m.Mountpoint == "/"}
		if env.FS.IsHost() {
			var st unix.Statfs_t
			if err := unix.Statfs(m.Mountpoint, &st); err == nil && st.Blocks > 0 {
				size := st.Blocks * uint64(st.Bsize)
				free := st.Bfree * uint64(st.Bsize)
				avail := st.Bavail * uint64(st.Bsize)
				fs.SizeBytes, fs.UsedBytes = size, size-free
				// like df: used / (used + available to unprivileged users)
				if d := fs.UsedBytes + avail; d > 0 {
					fs.UsedPercent = model.F(float64(fs.UsedBytes) / float64(d) * 100)
				}
				if st.Files > 0 {
					fs.InodesUsedPercent = model.F(float64(st.Files-st.Ffree) / float64(st.Files) * 100)
				}
			}
		} else if r, ok := df[m.Mountpoint]; ok {
			fs.SizeBytes, fs.UsedBytes = r.size, r.used
			if d := r.used + r.avail; d > 0 {
				fs.UsedPercent = model.F(float64(r.used) / float64(d) * 100)
			}
			if r.itotal > 0 {
				fs.InodesUsedPercent = model.F(float64(r.iused) / float64(r.itotal) * 100)
			}
		}
		fs.Status = model.StatusOK
		if fs.ReadOnly {
			fs.Status = model.StatusCrit
		}
		if p := fs.UsedPercent; p != nil && *p >= fsWarn {
			fs.Status = model.Worst(fs.Status, map[bool]model.Status{true: model.StatusCrit, false: model.StatusWarn}[*p >= fsCrit])
		}
		seen[key] = len(out)
		out = append(out, fs)
	}
	if out == nil {
		out = []model.Filesystem{}
	}
	return out
}

type dfRow struct{ size, used, avail, itotal, iused uint64 }

// readDF parses a recorded `df -B1 --output=target,size,used,avail,itotal,iused`
// (fixture mode only; the live host uses statfs).
func readDF(env *collector.Env) map[string]dfRow {
	b, err := env.Exec.Run(context.Background(), "df", "-B1", "--output=target,size,used,avail,itotal,iused")
	out := map[string]dfRow{}
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		n := len(f)
		var v [5]uint64
		ok := true
		for i := 0; i < 5; i++ {
			x, err := strconv.ParseUint(f[n-5+i], 10, 64)
			if err != nil {
				ok = false
				break
			}
			v[i] = x
		}
		if ok {
			out[strings.Join(f[:n-5], " ")] = dfRow{v[0], v[1], v[2], v[3], v[4]}
		}
	}
	return out
}

func summary(s *model.Storage) string {
	ok, n, rok, rn := 0, 0, 0, 0
	for _, d := range s.Disks {
		if !d.Local() {
			rn++
			if d.Status == model.StatusOK {
				rok++
			}
			continue
		}
		n++
		if d.Status == model.StatusOK {
			ok++
		}
	}
	parts := []string{fmt.Sprintf("%d/%d disks OK", ok, n)}
	if rn > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d network volumes OK", rok, rn))
	}
	if n := len(s.MDArrays); n > 0 {
		good := 0
		for _, a := range s.MDArrays {
			if a.Status == model.StatusOK {
				good++
			}
		}
		parts = append(parts, fmt.Sprintf("md %d/%d OK", good, n))
	}
	for _, c := range s.Controllers {
		parts = append(parts, fmt.Sprintf("RAID %s", c.Status))
	}
	var worst *model.Filesystem
	for i := range s.Filesystems {
		fs := &s.Filesystems[i]
		if fs.UsedPercent != nil && (worst == nil || *fs.UsedPercent > *worst.UsedPercent) {
			worst = fs
		}
	}
	if worst != nil {
		parts = append(parts, fmt.Sprintf("fullest %s %.0f%%", worst.Mountpoint, *worst.UsedPercent))
	}
	return strings.Join(parts, ", ")
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// maxKernelLines is how many of a disk's latest kernel log lines are kept.
const maxKernelLines = 30

// kernelLines returns the latest kernel log lines that name the disk, its
// partitions, its SATA port, its NVMe controller or (iSCSI) its session's
// connection - where to look when a disk reports errors.
func kernelLines(l *kernellog.Log, b sysfs.BlockDev) []string {
	if l == nil {
		return nil
	}
	alts := []string{regexp.QuoteMeta(b.Name) + `p?\d*`}
	if b.NVMeCtrl != "" {
		alts = append(alts, regexp.QuoteMeta(b.NVMeCtrl))
	}
	for _, e := range strings.Split(b.SysPath, "/") {
		if strings.HasPrefix(e, "ata") && e[3:] != "" && strings.Trim(e[3:], "0123456789") == "" {
			alts = append(alts, e+`(?:\.\d+)?`)
		}
	}
	if r := b.Remote; r != nil && strings.HasPrefix(r.Session, "session") {
		alts = append(alts, "connection"+strings.TrimPrefix(r.Session, "session")+`:\d+`)
	}
	re := regexp.MustCompile(`(?:^|[^\w])(?:` + strings.Join(alts, "|") + `)(?:[^\w]|$)`)
	var out []string
	for _, e := range l.Recent {
		if !re.MatchString(e.Message) {
			continue
		}
		line := strings.TrimSpace(e.Message)
		if !e.Time.IsZero() {
			line = e.Time.UTC().Format("2006-01-02 15:04:05") + " " + line
		}
		out = append(out, line)
	}
	if len(out) > maxKernelLines {
		out = out[len(out)-maxKernelLines:]
	}
	return out
}
