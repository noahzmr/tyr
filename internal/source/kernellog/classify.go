package kernellog

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/noahzmr/tyr/internal/model"
)

// Category groups hardware events.
type Category string

const (
	CatMCE     Category = "mce"
	CatEDAC    Category = "edac"
	CatAER     Category = "aer"
	CatIO      Category = "io"
	CatThermal Category = "thermal"
	CatLink    Category = "link"
	CatHW      Category = "hw"  // APEI/GHES generic hardware errors
	CatGPU     Category = "gpu" // GPU/NPU driver errors (subject: PCI address)
)

// Class is the classification of a message. Subject identifies the affected
// component ("cpu7", "sda", "0000:3b:00.0", "eth0", a DIMM label).
type Class struct {
	Category Category
	Severity model.Status
	Subject  string
	// LinkUp marks "link is up" messages (OK severity) used for flap counting.
	LinkUp bool
	// Count is the error count stated in the message (EDAC "3 CE"), default 1.
	Count int
}

type rule struct {
	re *regexp.Regexp
	fn func(m []string) Class
}

// xidSeverity classifies NVIDIA Xid codes (NVIDIA "Xid Errors" catalog):
// hardware/ECC/bus problems are CRIT, application-caused faults WARN.
func xidSeverity(code int) model.Status {
	switch code {
	case 48, 62, 64, 74, 79, 95, 109, 110, 119, 120, 121, 140, 143, 145, 146:
		return model.StatusCrit
	case 13, 31, 43, 45, 68, 69, 94:
		return model.StatusWarn
	}
	return model.StatusWarn
}

// pciAddr normalizes "0000:3b:00" / "3b:00.0" style addresses to 0000:3b:00.0.
func pciAddr(s string) string {
	s = strings.ToLower(strings.Trim(s, "() "))
	if strings.Count(s, ":") == 1 {
		s = "0000:" + s
	}
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	if len(s) > 12 && strings.HasPrefix(s, "0000") && strings.Count(s, ":") == 2 && len(strings.SplitN(s, ":", 2)[0]) > 4 {
		s = s[len(s)-12:]
	}
	return s
}

var rules = []rule{
	// --- GPU / NPU ---
	{regexp.MustCompile(`NVRM: Xid \(PCI:([0-9a-fA-F:.]+)\): (\d+)`), func(m []string) Class {
		code, _ := strconv.Atoi(m[2])
		return Class{Category: CatGPU, Severity: xidSeverity(code), Subject: pciAddr(m[1])}
	}},
	{regexp.MustCompile(`amdgpu ([0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]): .*(GPU reset begin|GPU reset\(\d+\) succeeded|ring \S+ timeout|GPU fault detected|uncorrectable|Uncorrectable|poison)`), func(m []string) Class {
		sev := model.StatusWarn
		if strings.Contains(strings.ToLower(m[2]), "uncorrectable") || strings.Contains(m[2], "poison") {
			sev = model.StatusCrit
		}
		return Class{Category: CatGPU, Severity: sev, Subject: m[1]}
	}},
	{regexp.MustCompile(`(?:i915|xe) ([0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]): .*(GPU HANG|[Rr]esetting|wedged|engine reset)`), func(m []string) Class {
		sev := model.StatusWarn
		if strings.Contains(m[2], "wedged") {
			sev = model.StatusCrit
		}
		return Class{Category: CatGPU, Severity: sev, Subject: m[1]}
	}},
	{regexp.MustCompile(`(?i)(?:amdxdna|intel_vpu|habanalabs|qaic) ([0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]): .*(timeout|hang|fatal|failed to boot|firmware.*(?:error|fail)|recovery)`), func(m []string) Class {
		return Class{Category: CatGPU, Severity: model.StatusWarn, Subject: m[1]}
	}},
	// --- MCE (x86) ---
	{regexp.MustCompile(`\[Hardware Error\]: CPU (\d+): Machine Check(?: Exception)?: (\S+) Bank (\d+): ([0-9a-fA-F]+)`), func(m []string) Class {
		sev := model.StatusWarn
		if st, err := strconv.ParseUint(m[4], 16, 64); err == nil && st&(1<<61) != 0 { // MCi_STATUS.UC
			sev = model.StatusCrit
		}
		if strings.Contains(m[0], "Machine Check Exception") {
			sev = model.StatusCrit
		}
		return Class{Category: CatMCE, Severity: sev, Subject: "cpu" + m[1]}
	}},
	{regexp.MustCompile(`\[Hardware Error\]: (Uncorrected|Corrected|Deferred) error`), func(m []string) Class {
		sev := model.StatusWarn
		if m[1] == "Uncorrected" {
			sev = model.StatusCrit
		}
		return Class{Category: CatMCE, Severity: sev}
	}},
	{regexp.MustCompile(`mce: \[Hardware Error\]: Machine check events logged`), func(m []string) Class {
		return Class{Category: CatMCE, Severity: model.StatusWarn}
	}},
	// --- Thermal ---
	{regexp.MustCompile(`CPU(\d+): (Core|Package) temperature above threshold`), func(m []string) Class {
		return Class{Category: CatThermal, Severity: model.StatusWarn, Subject: "cpu" + m[1]}
	}},
	{regexp.MustCompile(`(thermal_zone\d+).*critical temperature reached`), func(m []string) Class {
		return Class{Category: CatThermal, Severity: model.StatusCrit, Subject: m[1]}
	}},
	// --- EDAC ---
	{regexp.MustCompile(`EDAC (?:\S+ )?MC(\d+): (\d+) (CE|UE) [^(]*? on (\S+)`), func(m []string) Class {
		sev := model.StatusWarn
		if m[3] == "UE" {
			sev = model.StatusCrit
		}
		n, _ := strconv.Atoi(m[2])
		subj := m[4]
		if subj == "unknown" || subj == "any" {
			subj = "mc" + m[1]
		}
		return Class{Category: CatEDAC, Severity: sev, Subject: subj, Count: n}
	}},
	// --- PCIe AER ---
	{regexp.MustCompile(`AER: .*?(Corrected|Correctable|Uncorrected|Uncorrectable)[^:]*?(?:received|from)[: ]+([0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7])`), func(m []string) Class {
		sev := model.StatusWarn
		if strings.HasPrefix(m[1], "Uncorr") && strings.Contains(m[0], "Fatal") && !strings.Contains(m[0], "Non-Fatal") {
			sev = model.StatusCrit
		}
		return Class{Category: CatAER, Severity: sev, Subject: m[2]}
	}},
	// --- APEI/GHES ---
	{regexp.MustCompile(`\[Hardware Error\]: event severity: (corrected|recoverable|fatal)`), func(m []string) Class {
		sev := model.StatusCrit
		if m[1] == "corrected" {
			sev = model.StatusWarn
		}
		return Class{Category: CatHW, Severity: sev}
	}},
	// --- Block I/O ---
	{regexp.MustCompile(`(?:I/O|critical medium|critical target) error, dev (\w+), sector (\d+)`), func(m []string) Class {
		return Class{Category: CatIO, Severity: model.StatusCrit, Subject: m[1]}
	}},
	{regexp.MustCompile(`Buffer I/O error on dev (\w+?)(?:p?\d+)?, logical block`), func(m []string) Class {
		return Class{Category: CatIO, Severity: model.StatusCrit, Subject: m[1]}
	}},
	{regexp.MustCompile(`\[(sd[a-z]+)\] (?:tag#\d+ )?(?:Sense Key : Medium Error|Add\. Sense: Unrecovered read error)`), func(m []string) Class {
		return Class{Category: CatIO, Severity: model.StatusCrit, Subject: m[1]}
	}},
	{regexp.MustCompile(`(ata\d+)(?:\.\d+)?: (?:failed command|exception Emask|hard resetting link|SError:)`), func(m []string) Class {
		return Class{Category: CatIO, Severity: model.StatusWarn, Subject: m[1]}
	}},
	{regexp.MustCompile(`nvme (nvme\d+): (controller is down|Removing after probe failure|Device not ready|I/O \d+ (?:\(\S+\) )?QID \d+ timeout)`), func(m []string) Class {
		sev := model.StatusWarn
		if !strings.HasPrefix(m[2], "I/O") {
			sev = model.StatusCrit
		}
		return Class{Category: CatIO, Severity: sev, Subject: m[1]}
	}},
	// --- NIC link ---
	{regexp.MustCompile(`NETDEV WATCHDOG: (\S+) \(`), func(m []string) Class {
		return Class{Category: CatLink, Severity: model.StatusWarn, Subject: m[1]}
	}},
	{regexp.MustCompile(`^\S+ (?:[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7] )?(\S+?):? (?:NIC )?Link (?:is )?[Dd]own`), func(m []string) Class {
		return Class{Category: CatLink, Severity: model.StatusWarn, Subject: m[1]}
	}},
	{regexp.MustCompile(`^\S+ (?:[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7] )?(\S+?):? (?:NIC )?Link (?:is )?[Uu]p`), func(m []string) Class {
		return Class{Category: CatLink, Severity: model.StatusOK, Subject: m[1], LinkUp: true}
	}},
	{regexp.MustCompile(`^\S+ (?:[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7] )?(\S+?):? (?:Reset adapter|Detected Hardware Unit Hang|tx timeout|TX timeout)`), func(m []string) Class {
		return Class{Category: CatLink, Severity: model.StatusWarn, Subject: m[1]}
	}},
}

// Classify returns the hardware classification of a kernel message, or a
// zero Class if the message is not hardware-related.
func Classify(msg string) Class {
	for _, r := range rules {
		if m := r.re.FindStringSubmatch(msg); m != nil {
			c := r.fn(m)
			if c.Count == 0 {
				c.Count = 1
			}
			return c
		}
	}
	return Class{}
}
