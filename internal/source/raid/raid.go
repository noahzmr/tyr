// Package raid parses Linux software RAID (/proc/mdstat) and hardware RAID
// controllers (storcli/perccli JSON).
package raid

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/model"
)

var (
	mdHead   = regexp.MustCompile(`^(md\w+)\s*:\s*(\w+)\s*(?:\((?:auto-)?read-only\)\s*)?(\S+)?\s*(.*)$`)
	mdMember = regexp.MustCompile(`^(\S+)\[(\d+)\]((?:\([A-Z]\))*)$`)
	mdCount  = regexp.MustCompile(`\[(\d+)/(\d+)\]\s*\[([U_]+)\]`)
	mdBlocks = regexp.MustCompile(`^\s*(\d+) blocks`)
	mdSync   = regexp.MustCompile(`(resync|recovery|check|reshape|repair)\s*=\s*([\d.]+)%`)
	mdFinish = regexp.MustCompile(`finish=(\S+)`)
	mdSyncP  = regexp.MustCompile(`(resync|recovery)\s*=\s*(PENDING|DELAYED)`)
)

// ParseMdstat parses /proc/mdstat.
func ParseMdstat(data []byte) ([]model.MDArray, error) {
	var out []model.MDArray
	var cur *model.MDArray
	for _, line := range strings.Split(string(data), "\n") {
		if m := mdHead.FindStringSubmatch(line); m != nil && strings.HasPrefix(line, "md") {
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &model.MDArray{Name: m[1], State: m[2]}
			rest := m[4]
			if m[2] == "active" || m[2] == "inactive" {
				if m[3] != "" && !strings.Contains(m[3], "[") {
					cur.Level = m[3]
				} else {
					rest = m[3] + " " + rest
				}
			}
			for _, tok := range strings.Fields(rest) {
				if mm := mdMember.FindStringSubmatch(tok); mm != nil {
					role, _ := strconv.Atoi(mm[2])
					cur.Members = append(cur.Members, model.MDMember{Device: mm[1], Role: role,
						Failed: strings.Contains(mm[3], "(F)"), Spare: strings.Contains(mm[3], "(S)")})
				}
			}
			continue
		}
		if cur == nil {
			continue
		}
		if m := mdBlocks.FindStringSubmatch(line); m != nil {
			n, _ := strconv.ParseUint(m[1], 10, 64)
			cur.SizeBytes = n * 1024
		}
		if m := mdCount.FindStringSubmatch(line); m != nil {
			cur.Devices, _ = strconv.Atoi(m[1])
			cur.ActiveDevices, _ = strconv.Atoi(m[2])
			cur.Degraded = strings.Contains(m[3], "_")
		}
		if m := mdSync.FindStringSubmatch(line); m != nil {
			cur.SyncAction = m[1]
			if v, err := strconv.ParseFloat(m[2], 64); err == nil {
				cur.SyncPercent = &v
			}
			if f := mdFinish.FindStringSubmatch(line); f != nil {
				cur.SyncFinish = f[1]
			}
		} else if m := mdSyncP.FindStringSubmatch(line); m != nil {
			cur.SyncAction = m[1] + " " + strings.ToLower(m[2])
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	if !strings.Contains(string(data), "Personalities") && len(out) == 0 {
		return nil, fmt.Errorf("mdstat: unrecognized format")
	}
	for i := range out {
		out[i].Status = MDStatus(&out[i])
	}
	return out, nil
}

// MDStatus derives the severity of an array.
func MDStatus(a *model.MDArray) model.Status {
	st := model.StatusOK
	for _, m := range a.Members {
		if m.Failed {
			st = model.StatusCrit
		}
	}
	switch {
	case a.State == "inactive":
		return model.StatusCrit
	case a.Degraded:
		return model.StatusCrit
	case strings.HasPrefix(a.SyncAction, "recovery"), strings.HasPrefix(a.SyncAction, "resync"), a.SyncAction == "reshape":
		st = model.Worst(st, model.StatusWarn)
	}
	return st
}

// --- storcli / perccli ----------------------------------------------------------

type storcliOut struct {
	Controllers []struct {
		CommandStatus struct {
			Controller  any    `json:"Controller"`
			Status      string `json:"Status"`
			Description string `json:"Description"`
		} `json:"Command Status"`
		Response map[string]json.RawMessage `json:"Response Data"`
	} `json:"Controllers"`
}

// ParseStorcli parses `storcli /call show all J` (perccli is identical).
func ParseStorcli(data []byte, tool string) ([]model.RAIDController, error) {
	var s storcliOut
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s json: %w", tool, err)
	}
	var out []model.RAIDController
	for i, c := range s.Controllers {
		if c.CommandStatus.Status != "Success" {
			if strings.Contains(c.CommandStatus.Description, "No Controller") {
				continue
			}
			return out, fmt.Errorf("%s: %s", tool, c.CommandStatus.Description)
		}
		rc := model.RAIDController{Index: i, Tool: tool, VDs: []model.VirtualDrive{}, PDs: []model.PhysicalDrive{}}
		var basics, version, status map[string]any
		_ = json.Unmarshal(c.Response["Basics"], &basics)
		_ = json.Unmarshal(c.Response["Version"], &version)
		_ = json.Unmarshal(c.Response["Status"], &status)
		rc.Model, rc.Serial = str(basics["Model"]), str(basics["Serial Number"])
		rc.Firmware = str(version["Firmware Version"])
		rc.State = str(status["Controller Status"])
		if n, ok := basics["Controller"].(float64); ok {
			rc.Index = int(n)
		}
		var vds []map[string]any
		_ = json.Unmarshal(c.Response["VD LIST"], &vds)
		for _, v := range vds {
			vd := model.VirtualDrive{ID: str(v["DG/VD"]), Name: str(v["Name"]), Level: str(v["TYPE"]), State: str(v["State"]), SizeText: str(v["Size"])}
			vd.Status = vdStatus(vd.State)
			rc.VDs = append(rc.VDs, vd)
		}
		var pds []map[string]any
		_ = json.Unmarshal(c.Response["PD LIST"], &pds)
		for _, p := range pds {
			pd := model.PhysicalDrive{Slot: str(p["EID:Slt"]), State: strings.TrimSpace(str(p["State"])), Group: str(p["DG"]),
				SizeText: str(p["Size"]), Interface: str(p["Intf"]), Media: str(p["Med"]), Model: strings.TrimSpace(str(p["Model"]))}
			if sed := str(p["SED"]); sed == "Y" || sed == "F" { // F: FIPS SED
				pd.SED = map[string]string{"Y": "self-encrypting", "F": "self-encrypting (FIPS)"}[sed]
			}
			pd.Status = pdStatus(pd.State)
			rc.PDs = append(rc.PDs, pd)
		}
		for _, k := range []string{"Cachevault_Info", "BBU_Info"} {
			var bats []map[string]any
			if json.Unmarshal(c.Response[k], &bats) == nil && len(bats) > 0 {
				b := bats[0]
				bt := &model.RAIDBattery{Type: map[string]string{"Cachevault_Info": "CacheVault", "BBU_Info": "BBU"}[k],
					Model: str(b["Model"]), State: str(b["State"])}
				if t := strings.TrimSuffix(str(b["Temp"]), "C"); t != "" {
					if v, err := strconv.ParseFloat(t, 64); err == nil {
						bt.TempCelsius = &v
					}
				}
				switch strings.ToLower(bt.State) {
				case "optimal", "learning", "charging", "":
					bt.Status = model.StatusOK
				case "failed", "missing":
					bt.Status = model.StatusCrit
				default:
					bt.Status = model.StatusWarn
				}
				rc.Battery = bt
			}
		}
		st := model.StatusOK
		if rc.State != "" && !strings.EqualFold(rc.State, "Optimal") {
			st = model.StatusWarn
		}
		for _, v := range rc.VDs {
			st = model.Worst(st, v.Status)
		}
		for _, p := range rc.PDs {
			st = model.Worst(st, p.Status)
		}
		if rc.Battery != nil {
			st = model.Worst(st, rc.Battery.Status)
		}
		rc.Status = st
		out = append(out, rc)
	}
	return out, nil
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func vdStatus(s string) model.Status {
	switch s {
	case "Optl":
		return model.StatusOK
	case "Pdgd", "Rec", "Cac":
		return model.StatusWarn
	case "Dgrd", "OfLn", "Msng":
		return model.StatusCrit
	}
	return model.StatusUnknown
}

func pdStatus(s string) model.Status {
	switch s {
	case "Onln", "UGood", "GHS", "DHS", "JBOD", "Cpybck":
		return model.StatusOK
	case "Rbld", "UGUnsp", "UGShld":
		return model.StatusWarn
	case "Offln", "UBad", "Failed", "Msng", "UBUnsp":
		return model.StatusCrit
	}
	return model.StatusUnknown
}

// PDDetail is the identity of one physical drive behind a RAID controller.
type PDDetail struct{ Serial, WWN, Firmware string }

var pdDetailKey = regexp.MustCompile(`^Drive /c(\d+)/e(\d+)/s(\d+) - Detailed Information$`)

// ParseStorcliPDs parses `storcli /call/eall/sall show all J` into
// controller index -> "EID:Slt" -> serial/WWN/firmware.
func ParseStorcliPDs(data []byte) (map[int]map[string]PDDetail, error) {
	var s storcliOut
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("storcli pd json: %w", err)
	}
	out := map[int]map[string]PDDetail{}
	for _, c := range s.Controllers {
		for k, raw := range c.Response {
			m := pdDetailKey.FindStringSubmatch(k)
			if m == nil {
				continue
			}
			ctrl, _ := strconv.Atoi(m[1])
			var det map[string]json.RawMessage
			if json.Unmarshal(raw, &det) != nil {
				continue
			}
			var attrs map[string]any
			for dk, dv := range det {
				if strings.HasSuffix(dk, "Device attributes") {
					_ = json.Unmarshal(dv, &attrs)
				}
			}
			if attrs == nil {
				continue
			}
			if out[ctrl] == nil {
				out[ctrl] = map[string]PDDetail{}
			}
			out[ctrl][m[2]+":"+m[3]] = PDDetail{Serial: str(attrs["SN"]), WWN: str(attrs["WWN"]), Firmware: str(attrs["Firmware Revision"])}
		}
	}
	return out, nil
}
