package gpu

import (
	"regexp"
	"strconv"
	"strings"
)

// XRTInfo is what `xrt-smi examine -r platform -r aie-partitions` reports
// for an AMD XDNA NPU (amdxdna exposes no hwmon sensors).
type XRTInfo struct {
	PowerWatts *float64 // estimated NPU power; nil when "N/A"
	PowerMode  string   // Default, Powersaver, Balanced, Performance, Turbo
	Columns    *int     // total AIE columns
	Contexts   *int     // hardware contexts (running workloads); nil if unknown
}

var (
	xrtKV  = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z ]*?)\s*:\s*(.*?)\s*$`)
	xrtRow = regexp.MustCompile(`^\s*\|\s*\d+\s*\|`)
)

// ParseXRTSMI parses the text report (stdout; the JSON format requires an
// output file). Unknown lines are ignored.
func ParseXRTSMI(out []byte) XRTInfo {
	var x XRTInfo
	rows, inPart := 0, false
	for _, line := range strings.Split(string(out), "\n") {
		trim := strings.TrimSpace(line)
		switch {
		case trim == "AIE Partitions":
			inPart = true
			continue
		case strings.Contains(trim, "No hardware contexts running"):
			x.Contexts = new(int)
			continue
		case inPart && xrtRow.MatchString(line):
			rows++
			continue
		}
		m := xrtKV.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		k, v := m[1], m[2]
		switch k {
		case "Power Mode":
			x.PowerMode = v
		case "Total Columns":
			if n, err := strconv.Atoi(v); err == nil {
				x.Columns = &n
			}
		case "Estimated Power":
			if f := strings.Fields(v); len(f) >= 1 {
				if w, err := strconv.ParseFloat(f[0], 64); err == nil && w >= 0 && w < 1000 {
					x.PowerWatts = &w
				}
			}
		}
	}
	if x.Contexts == nil && rows > 0 {
		x.Contexts = &rows
	}
	return x
}
