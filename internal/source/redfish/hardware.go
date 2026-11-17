package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/noahzmr/tyr/internal/model"
)

// Hardware is what the BMC knows about the parts that the host cannot see
// for itself: the drives behind a RAID controller (HPE Smart Array without
// ssacli, any controller without its vendor tool), part and serial numbers
// of cards, controllers and batteries, and the firmware of every component.
type Hardware struct {
	Controllers []model.RAIDController
	Devices     []model.Device
	Firmware    []model.FirmwareItem
}

// ReadHardware reads storage, the device inventory and the firmware
// inventory. Missing resources are normal (not every BMC has them); errs
// lists the reads that failed for another reason.
func ReadHardware(ctx context.Context, g Getter) (Hardware, []string) {
	var hw Hardware
	var errs []string
	note := func(err error) {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err.Error())
		}
	}
	systems, err := members(ctx, g, "/redfish/v1/Systems")
	note(err)
	for _, sys := range systems {
		ctrls, err := readStorage(ctx, g, strings.TrimSuffix(sys, "/"))
		note(err)
		hw.Controllers = append(hw.Controllers, ctrls...)
	}
	for i := range hw.Controllers {
		hw.Controllers[i].Index = i
	}
	chassis, err := members(ctx, g, "/redfish/v1/Chassis")
	note(err)
	for _, ch := range chassis {
		devs, err := readDevices(ctx, g, strings.TrimSuffix(ch, "/"))
		note(err)
		hw.Devices = append(hw.Devices, devs...)
	}
	hw.Firmware, err = readFirmware(ctx, g)
	note(err)
	return hw, errs
}

// --- JSON helpers -------------------------------------------------------------

// obj is one decoded Redfish resource; the shapes differ between vendors
// and firmware versions, so fields are looked up by path with fallbacks.
type obj map[string]any

func getObj(ctx context.Context, g Getter, path string) (obj, error) {
	b, err := g.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var o obj
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return o, nil
}

func (o obj) get(path ...string) any {
	var v any = map[string]any(o)
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// str is a trimmed string field; iLO's "N/A" placeholder counts as empty.
func (o obj) str(path ...string) string {
	switch v := o.get(path...).(type) {
	case string:
		s := strings.TrimSpace(v)
		if strings.EqualFold(s, "N/A") || strings.EqualFold(s, "Unknown") {
			return ""
		}
		return s
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func (o obj) num(path ...string) (float64, bool) {
	v, ok := o.get(path...).(float64)
	return v, ok
}

func (o obj) link(path ...string) string {
	if m, ok := o.get(path...).(map[string]any); ok {
		if s, ok := m["@odata.id"].(string); ok {
			return s
		}
	}
	return ""
}

// links reads an array of {"@odata.id": …}.
func (o obj) links(path ...string) []string {
	arr, _ := o.get(path...).([]any)
	var out []string
	for _, a := range arr {
		if m, ok := a.(map[string]any); ok {
			if s, ok := m["@odata.id"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func first(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// fetchAll reads the resources at paths, three at a time (a BMC answers
// slowly and serves few connections); unreadable ones are skipped.
func fetchAll(ctx context.Context, g Getter, paths []string) []obj {
	out := make([]obj, len(paths))
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			out[i], _ = getObj(ctx, g, p)
		}()
	}
	wg.Wait()
	var res []obj
	for _, o := range out {
		if o != nil {
			res = append(res, o)
		}
	}
	return res
}

// health maps Redfish Status.Health to a Tyr status.
func health(h string) model.Status {
	switch strings.ToLower(h) {
	case "ok":
		return model.StatusOK
	case "warning":
		return model.StatusWarn
	case "critical":
		return model.StatusCrit
	}
	return model.StatusUnknown
}

// --- storage ------------------------------------------------------------------

// readStorage reads the HPE Smart Storage views (array controllers and host
// bus adapters) and the standard Storage collection of one system and joins
// them: a controller on its serial, a drive on its serial. The HPE view comes
// first because only it reports the bay, endurance, power-on hours and
// temperature; the standard one adds what it lacks (exact size, predicted
// failure, the conditions of a failed drive).
func readStorage(ctx context.Context, g Getter, sys string) ([]model.RAIDController, error) {
	var ctrls []model.RAIDController
	bySerial := map[string]int{}  // controller serial → index
	drives := map[string][2]int{} // drive serial → controller, drive
	add := func(c model.RAIDController) int {
		if c.Serial != "" {
			if i, ok := bySerial[c.Serial]; ok {
				d := &ctrls[i]
				d.Model, d.Firmware, d.Location = first(d.Model, c.Model), first(d.Firmware, c.Firmware), first(d.Location, c.Location)
				d.Status = model.Worst(d.Status, c.Status)
				return i
			}
			bySerial[c.Serial] = len(ctrls)
		}
		ctrls = append(ctrls, c)
		return len(ctrls) - 1
	}
	addDrive := func(ci int, p model.PhysicalDrive) {
		if p.Serial != "" {
			if at, ok := drives[p.Serial]; ok {
				mergeDrive(&ctrls[at[0]].PDs[at[1]], p)
				return
			}
		} else if p.Status < model.StatusWarn {
			// No serial on a healthy drive: discovery has not finished, the
			// data is not trustworthy. A failed drive often reports nothing
			// but its bay and condition, and is kept for exactly that.
			return
		}
		ctrls[ci].PDs = append(ctrls[ci].PDs, p)
		if p.Serial != "" {
			drives[p.Serial] = [2]int{ci, len(ctrls[ci].PDs) - 1}
		}
	}
	var firstErr error
	keep := func(err error) {
		if err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}

	for _, coll := range []string{sys + "/SmartStorage/ArrayControllers", sys + "/SmartStorage/HostBusAdapters"} {
		paths, err := members(ctx, g, coll)
		keep(err)
		for _, c := range fetchAll(ctx, g, paths) {
			rc := model.RAIDController{
				Model:    first(c.str("Model"), c.str("Name")),
				Serial:   c.str("SerialNumber"),
				Firmware: c.str("FirmwareVersion", "Current", "VersionString"),
				Location: c.str("Location"),
				State:    c.str("Status", "Health"),
				Tool:     "redfish",
				Status:   health(c.str("Status", "Health")),
			}
			if bp := c.str("BackupPowerSourceStatus"); bp != "" {
				st := model.StatusOK
				if !strings.EqualFold(bp, "Present") {
					st = model.StatusWarn
				}
				rc.Battery = &model.RAIDBattery{Type: "Smart Storage Battery", State: bp, Status: st}
			}
			ci := add(rc)
			// array controllers link PhysicalDrives, host bus adapters Drives
			drvColl := first(c.link("Links", "PhysicalDrives"), c.link("Links", "Drives"))
			if drvColl == "" && c.link() != "" {
				drvColl = strings.TrimSuffix(c.link(), "/") + "/DiskDrives"
			}
			if drvColl != "" {
				p, err := members(ctx, g, drvColl)
				keep(err)
				for _, d := range fetchAll(ctx, g, p) {
					addDrive(ci, smartDrive(d))
				}
			}
			if ldColl := c.link("Links", "LogicalDrives"); ldColl != "" {
				if p, err := members(ctx, g, ldColl); err == nil {
					for _, l := range fetchAll(ctx, g, p) {
						ctrls[ci].VDs = append(ctrls[ci].VDs, logicalDrive(l))
					}
				}
			}
		}
	}

	paths, err := members(ctx, g, sys+"/Storage")
	keep(err)
	for _, st := range fetchAll(ctx, g, paths) {
		rc := model.RAIDController{Tool: "redfish", Model: st.str("Name"), State: st.str("Status", "Health"), Status: health(st.str("Status", "Health"))}
		if sc, ok := st.get("StorageControllers").([]any); ok && len(sc) > 0 {
			if m, ok := sc[0].(map[string]any); ok {
				c := obj(m)
				rc.Model = first(c.str("Model"), c.str("Name"), rc.Model)
				rc.Serial = c.str("SerialNumber")
				rc.Firmware = c.str("FirmwareVersion")
				rc.Location = c.str("Location", "PartLocation", "ServiceLabel")
				if h := c.str("Status", "Health"); h != "" {
					rc.State, rc.Status = h, model.Worst(rc.Status, health(h))
				}
			}
		}
		dl := st.links("Drives")
		if len(dl) == 0 {
			dl = st.links("Links", "Drives")
		}
		// Join on a drive already seen through the HPE view before adding
		// a controller: the same Smart Array often reports no serial here.
		ci := -1
		for _, d := range fetchAll(ctx, g, dl) {
			p := standardDrive(d)
			if at, ok := drives[p.Serial]; ok && p.Serial != "" {
				mergeDrive(&ctrls[at[0]].PDs[at[1]], p)
				continue
			}
			if p.Serial == "" && p.Slot != "" && mergeBySlot(ctrls, p) {
				continue // a failed drive without a serial, seen in the HPE view by its bay
			}
			if ci < 0 {
				ci = add(rc)
			}
			addDrive(ci, p)
		}
	}
	return ctrls, firstErr
}

// smartDrive reads an HpeSmartStorageDiskDrive: the only view with the bay,
// endurance, power-on hours and temperature the Smart Storage Administrator
// shows.
func smartDrive(d obj) model.PhysicalDrive {
	p := model.PhysicalDrive{
		Slot:      d.str("Location"),
		Model:     d.str("Model"),
		Serial:    d.str("SerialNumber"),
		Firmware:  d.str("FirmwareVersion", "Current", "VersionString"),
		Interface: d.str("InterfaceType"),
		Media:     d.str("MediaType"),
		State:     d.str("Status", "Health"),
		Status:    health(d.str("Status", "Health")),
	}
	if v, ok := d.num("CapacityMiB"); ok && v > 0 {
		p.SizeBytes = uint64(v) << 20
	} else if v, ok := d.num("CapacityGB"); ok && v > 0 {
		p.SizeBytes = uint64(v * 1e9)
	}
	if v, ok := d.num("SSDEnduranceUtilizationPercentage"); ok {
		left := 100 - v
		p.LifeLeftPercent = &left
	}
	if v, ok := d.num("PowerOnHours"); ok && v > 0 {
		h := uint64(v)
		p.PowerOnHours = &h
	}
	if v, ok := d.num("CurrentTemperatureCelsius"); ok && v > 0 {
		p.TempCelsius = &v
	}
	if arr, ok := d.get("DiskDriveStatusReasons").([]any); ok {
		for _, r := range arr {
			if s, ok := r.(string); ok && s != "" && !strings.EqualFold(s, "None") && !strings.EqualFold(s, "Ok") {
				p.Reasons = append(p.Reasons, s)
			}
		}
	}
	finishDrive(&p)
	return p
}

// standardDrive reads a Redfish Drive resource.
func standardDrive(d obj) model.PhysicalDrive {
	p := model.PhysicalDrive{
		Slot:             driveBay(first(d.str("PhysicalLocation", "PartLocation", "ServiceLabel"), d.str("Name"), d.str("Id"))),
		Model:            d.str("Model"),
		Serial:           d.str("SerialNumber"),
		Firmware:         first(d.str("FirmwareVersion"), d.str("Revision")),
		Interface:        d.str("Protocol"),
		Media:            d.str("MediaType"),
		State:            d.str("Status", "Health"),
		Status:           health(d.str("Status", "Health")),
		FailurePredicted: d.get("FailurePredicted") == true,
	}
	if v, ok := d.num("CapacityBytes"); ok && v > 0 {
		p.SizeBytes = uint64(v)
	}
	if v, ok := d.num("PredictedMediaLifeLeftPercent"); ok {
		p.LifeLeftPercent = &v
	}
	if arr, ok := d.get("Status", "Conditions").([]any); ok {
		for _, c := range arr {
			if m, ok := c.(map[string]any); ok {
				if id := obj(m).str("MessageId"); id != "" {
					p.Reasons = append(p.Reasons, id)
				}
			}
		}
	}
	finishDrive(&p)
	return p
}

// finishDrive derives the size text and judges the drive: the controller's
// health, a predicted failure, and worn-out flash (the same limits as SMART
// wear: 90 % used is a warning, 100 % critical).
func finishDrive(p *model.PhysicalDrive) {
	if p.SizeBytes > 0 {
		p.SizeText = sizeText(p.SizeBytes)
	}
	if p.FailurePredicted {
		p.Status = model.Worst(p.Status, model.StatusWarn)
	}
	if l := p.LifeLeftPercent; l != nil {
		switch {
		case *l <= 0:
			p.Status = model.Worst(p.Status, model.StatusCrit)
		case *l <= 10:
			p.Status = model.Worst(p.Status, model.StatusWarn)
		}
	}
}

// mergeDrive fills the gaps of a drive from a second view of the same disk;
// the first view's values win.
func mergeDrive(dst *model.PhysicalDrive, src model.PhysicalDrive) {
	fill := func(d *string, s string) {
		if *d == "" {
			*d = s
		}
	}
	fill(&dst.Slot, src.Slot)
	fill(&dst.Model, src.Model)
	fill(&dst.Firmware, src.Firmware)
	fill(&dst.Interface, src.Interface)
	fill(&dst.Media, src.Media)
	fill(&dst.State, src.State)
	if dst.SizeBytes == 0 {
		dst.SizeBytes, dst.SizeText = src.SizeBytes, src.SizeText
	}
	if dst.LifeLeftPercent == nil {
		dst.LifeLeftPercent = src.LifeLeftPercent
	}
	if dst.PowerOnHours == nil {
		dst.PowerOnHours = src.PowerOnHours
	}
	if dst.TempCelsius == nil {
		dst.TempCelsius = src.TempCelsius
	}
	dst.FailurePredicted = dst.FailurePredicted || src.FailurePredicted
	for _, r := range src.Reasons {
		if !contains(dst.Reasons, r) {
			dst.Reasons = append(dst.Reasons, r)
		}
	}
	dst.Status = model.Worst(dst.Status, src.Status)
}

func mergeBySlot(ctrls []model.RAIDController, p model.PhysicalDrive) bool {
	for ci := range ctrls {
		for di := range ctrls[ci].PDs {
			if d := &ctrls[ci].PDs[di]; d.Serial == "" && d.Slot == p.Slot {
				mergeDrive(d, p)
				return true
			}
		}
	}
	return false
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// logicalDrive reads an HpeSmartStorageLogicalDrive.
func logicalDrive(l obj) model.VirtualDrive {
	v := model.VirtualDrive{
		ID:     first(l.str("LogicalDriveNumber"), l.str("Id")),
		Name:   l.str("LogicalDriveName"),
		State:  l.str("Status", "Health"),
		Status: health(l.str("Status", "Health")),
	}
	if r := l.str("Raid"); r != "" {
		v.Level = "RAID " + r
	}
	if c, ok := l.num("CapacityMiB"); ok && c > 0 {
		v.SizeText = sizeText(uint64(c) << 20)
	}
	if v.Status == model.StatusUnknown {
		v.Status = model.StatusOK
	}
	return v
}

// driveBay turns the standard ServiceLabel ("Slot=0:Port=1I:Box=1:Bay=3")
// into the Port:Box:Bay form of the HPE view and ssacli ("1I:1:3").
func driveBay(label string) string {
	f := map[string]string{}
	for _, part := range strings.Split(label, ":") {
		if k, v, ok := strings.Cut(part, "="); ok {
			f[k] = v
		}
	}
	if f["Port"] == "" || f["Box"] == "" || f["Bay"] == "" {
		return strings.TrimSpace(label)
	}
	return f["Port"] + ":" + f["Box"] + ":" + f["Bay"]
}

func sizeText(b uint64) string {
	switch {
	case b >= 1e12:
		return strconv.FormatFloat(float64(b)/1e12, 'f', 2, 64) + " TB"
	default:
		return strconv.FormatFloat(float64(b)/1e9, 'f', 0, 64) + " GB"
	}
}

// --- device inventory ---------------------------------------------------------

// readDevices reads a chassis' device inventory: HPE's Devices collection
// (the only one with part and serial numbers of every card, controller and
// battery), else the standard PCIeDevices.
func readDevices(ctx context.Context, g Getter, chassis string) ([]model.Device, error) {
	paths, err := members(ctx, g, chassis+"/Devices")
	if err != nil {
		var err2 error
		paths, err2 = members(ctx, g, chassis+"/PCIeDevices")
		if err2 != nil {
			if errors.Is(err, os.ErrNotExist) {
				err = err2
			}
			return nil, err
		}
	}
	var out []model.Device
	seen := map[string]bool{}
	for _, d := range fetchAll(ctx, g, paths) {
		if strings.EqualFold(d.str("Status", "State"), "Absent") {
			continue // an empty slot
		}
		dev := model.Device{
			Name:         first(d.str("Name"), d.str("DeviceName"), d.str("Id")),
			Location:     first(d.str("Location"), d.str("Oem", "Hpe", "Location"), d.str("Slot", "Location", "PartLocation", "ServiceLabel"), d.str("Location", "PartLocation", "ServiceLabel")),
			Type:         first(d.str("DeviceType"), d.str("PCIeInterface", "DeviceType")),
			Manufacturer: first(d.str("Manufacturer"), d.str("Oem", "Hpe", "Manufacturer")),
			Model:        first(d.str("Model"), d.str("Oem", "Hpe", "ProductId")),
			PartNumber:   d.str("PartNumber"),
			ProductPart:  d.str("ProductPartNumber"),
			Serial:       first(d.str("SerialNumber"), d.str("Oem", "Hpe", "SerialNumber")),
			Firmware:     first(d.str("FirmwareVersion", "Current", "VersionString"), d.str("FirmwareVersion"), d.str("Oem", "Hpe", "Firmware", "Current", "VersionString")),
			Status:       health(d.str("Status", "Health")),
		}
		if dev.Name == "" {
			continue
		}
		if dev.Status == model.StatusUnknown {
			dev.Status = model.StatusOK
		}
		k := dev.Serial
		if k == "" {
			k = dev.Name + "|" + dev.Location
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, dev)
	}
	return out, nil
}

// --- firmware inventory -------------------------------------------------------

// readFirmware reads UpdateService/FirmwareInventory: what is installed.
// Dell lists previous and available versions too ("Previous-…",
// "Available-…"); only the installed ones are kept.
func readFirmware(ctx context.Context, g Getter) ([]model.FirmwareItem, error) {
	coll := "/redfish/v1/UpdateService/FirmwareInventory"
	if us, err := getObj(ctx, g, "/redfish/v1/UpdateService"); err == nil {
		if l := us.link("FirmwareInventory"); l != "" {
			coll = strings.TrimSuffix(l, "/")
		}
	}
	paths, err := members(ctx, g, coll)
	if err != nil {
		return nil, err
	}
	var out []model.FirmwareItem
	for _, f := range fetchAll(ctx, g, paths) {
		id := f.str("Id")
		if strings.HasPrefix(id, "Previous-") || strings.HasPrefix(id, "Available-") {
			continue
		}
		it := model.FirmwareItem{
			Name:     first(f.str("Name"), id),
			Version:  f.str("Version"),
			Location: first(f.str("Oem", "Hpe", "DeviceContext"), f.str("Oem", "Dell", "DeviceContext")),
			ID:       id,
			Status:   health(f.str("Status", "Health")),
		}
		if it.Name == "" || it.Version == "" {
			continue
		}
		if it.Status == model.StatusUnknown {
			it.Status = model.StatusOK
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
