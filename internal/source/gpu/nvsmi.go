// Package gpu parses vendor GPU tools (nvidia-smi) and reads amdgpu/Intel
// GPU state from sysfs.
package gpu

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// node is a generic XML element; nvidia-smi's schema changes between driver
// generations (clocks_throttle_reasons -> clocks_event_reasons,
// power_readings -> gpu_power_readings, single_bit -> dram_correctable), so
// lookups try several paths.
type node struct {
	Name     string
	Attr     map[string]string
	Text     string
	Children []*node
}

func parseXML(data []byte) (*node, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	root := &node{Name: "#root"}
	stack := []*node{root}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{Name: t.Name.Local, Attr: map[string]string{}}
			for _, a := range t.Attr {
				n.Attr[a.Name.Local] = a.Value
			}
			p := stack[len(stack)-1]
			p.Children = append(p.Children, n)
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			stack[len(stack)-1].Text += string(t)
		}
	}
	return root, nil
}

func (n *node) child(name string) *node {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// get follows a slash-separated path; "a/b|c/d" tries alternatives.
func (n *node) get(paths string) string {
	for _, p := range strings.Split(paths, "|") {
		cur := n
		for _, part := range strings.Split(p, "/") {
			cur = cur.child(part)
		}
		if cur != nil {
			if v := strings.TrimSpace(cur.Text); v != "" && v != "N/A" && !strings.HasPrefix(v, "[") {
				return v
			}
		}
	}
	return ""
}

// NvidiaProcess is one process holding GPU memory.
type NvidiaProcess struct {
	PID      int
	Type     string // C compute, G graphics, C+G
	Name     string
	MemBytes *uint64
}

// NvidiaGPU is the subset of `nvidia-smi -q -x` Tyr uses.
type NvidiaGPU struct {
	BusID           string // normalized 0000:3b:00.0
	Product, UUID   string
	Serial          string
	BoardPart       string
	VBIOS           string
	DriverVersion   string
	TempC           *float64
	TempSlowC       *float64
	TempMaxC        *float64
	MemTempC        *float64
	MemTempMaxC     *float64
	PowerW          *float64
	PowerCapW       *float64
	UtilPercent     *float64
	MemTotalBytes   *uint64
	MemUsedBytes    *uint64
	ClockMHz        *float64
	FanPercent      *float64
	LinkGen         int
	MaxLinkGen      int
	LinkWidth       *int
	MaxLinkWidth    *int
	ECCVolatileCE   *uint64
	ECCVolatileUE   *uint64
	ECCAggregateCE  *uint64
	ECCAggregateUE  *uint64
	RetiredPages    *uint64
	RetirePending   bool
	RemapPending    *bool
	RemapFailed     *bool
	ThrottleReasons []string
	// AddressingMode is "ATS" on Grace (NVLink-C2C) systems, "HMM" or
	// empty elsewhere.
	AddressingMode string
	// C2CMode is "Enabled" when the GPU is on NVLink-C2C.
	C2CMode string
	// TempTLimitC is the margin to the slowdown temperature (Blackwell
	// reports this instead of the slowdown threshold).
	TempTLimitC *float64
	Processes   []NvidiaProcess
	// ProcessMemBytes sums the memory the GPU's processes use: the only
	// usage figure when the GPU has no memory of its own (GB10).
	ProcessMemBytes *uint64
}

func num(s string) *float64 {
	f := strings.Fields(s)
	if len(f) == 0 {
		return nil
	}
	v, err := strconv.ParseFloat(strings.TrimSuffix(f[0], "x"), 64)
	if err != nil {
		return nil
	}
	return &v
}

func u64(s string) *uint64 {
	v := num(s)
	if v == nil {
		return nil
	}
	x := uint64(*v)
	return &x
}

func mib(s string) *uint64 {
	v := num(s)
	if v == nil {
		return nil
	}
	x := uint64(*v) << 20
	return &x
}

func yesNo(s string) *bool {
	switch strings.ToLower(s) {
	case "yes":
		b := true
		return &b
	case "no":
		b := false
		return &b
	}
	return nil
}

func sum(a, b *uint64) *uint64 {
	if a == nil && b == nil {
		return nil
	}
	var s uint64
	if a != nil {
		s += *a
	}
	if b != nil {
		s += *b
	}
	return &s
}

// NormalizeBusID turns "00000000:3B:00.0" into "0000:3b:00.0".
func NormalizeBusID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexByte(s, ':'); i > 4 {
		s = s[i-4:]
	}
	return s
}

// ParseNvidiaSMI parses `nvidia-smi -q -x`.
func ParseNvidiaSMI(data []byte) ([]NvidiaGPU, error) {
	root, err := parseXML(data)
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi xml: %w", err)
	}
	log := root.child("nvidia_smi_log")
	if log == nil {
		return nil, fmt.Errorf("nvidia-smi xml: no nvidia_smi_log element")
	}
	drv := log.get("driver_version")
	var out []NvidiaGPU
	for _, g := range log.Children {
		if g.Name != "gpu" {
			continue
		}
		x := NvidiaGPU{
			BusID:   NormalizeBusID(firstNonEmpty(g.Attr["id"], g.get("pci/pci_bus_id"))),
			Product: g.get("product_name"), UUID: g.get("uuid"), Serial: g.get("serial"), BoardPart: g.get("board_part_number"), VBIOS: g.get("vbios_version"), DriverVersion: drv,
			TempC: num(g.get("temperature/gpu_temp")), TempSlowC: num(g.get("temperature/gpu_temp_slow_threshold")),
			TempMaxC: num(g.get("temperature/gpu_temp_max_threshold")), MemTempC: num(g.get("temperature/memory_temp")),
			MemTempMaxC:   num(g.get("temperature/gpu_temp_max_mem_threshold")),
			PowerW:        num(g.get("gpu_power_readings/power_draw|gpu_power_readings/average_power_draw|gpu_power_readings/instant_power_draw|power_readings/power_draw")),
			PowerCapW:     num(g.get("gpu_power_readings/current_power_limit|gpu_power_readings/power_limit|power_readings/power_limit")),
			UtilPercent:   num(g.get("utilization/gpu_util")),
			MemTotalBytes: mib(g.get("fb_memory_usage/total")), MemUsedBytes: mib(g.get("fb_memory_usage/used")),
			ClockMHz:   num(g.get("clocks/graphics_clock")),
			FanPercent: num(g.get("fan_speed")),
		}
		if v := num(g.get("pci/pci_gpu_link_info/pcie_gen/current_link_gen")); v != nil {
			x.LinkGen = int(*v)
		}
		if v := num(g.get("pci/pci_gpu_link_info/pcie_gen/max_link_gen|pci/pci_gpu_link_info/pcie_gen/device_current_link_gen")); v != nil {
			x.MaxLinkGen = int(*v)
		}
		if v := num(g.get("pci/pci_gpu_link_info/link_widths/current_link_width")); v != nil {
			w := int(*v)
			x.LinkWidth = &w
		}
		if v := num(g.get("pci/pci_gpu_link_info/link_widths/max_link_width")); v != nil {
			w := int(*v)
			x.MaxLinkWidth = &w
		}
		// ECC: Ampere+ reports sram/dram counters, older drivers single/double bit totals.
		x.ECCVolatileCE = sum(u64(g.get("ecc_errors/volatile/dram_correctable|ecc_errors/volatile/single_bit/total")), u64(g.get("ecc_errors/volatile/sram_correctable")))
		x.ECCVolatileUE = sum(u64(g.get("ecc_errors/volatile/dram_uncorrectable|ecc_errors/volatile/double_bit/total")), u64(g.get("ecc_errors/volatile/sram_uncorrectable")))
		x.ECCAggregateCE = sum(u64(g.get("ecc_errors/aggregate/dram_correctable|ecc_errors/aggregate/single_bit/total")), u64(g.get("ecc_errors/aggregate/sram_correctable")))
		x.ECCAggregateUE = sum(u64(g.get("ecc_errors/aggregate/dram_uncorrectable|ecc_errors/aggregate/double_bit/total")), u64(g.get("ecc_errors/aggregate/sram_uncorrectable")))
		x.RetiredPages = sum(u64(g.get("retired_pages/multiple_single_bit_retirement/retired_count")), u64(g.get("retired_pages/double_bit_retirement/retired_count")))
		x.RetirePending = strings.EqualFold(g.get("retired_pages/pending_retirement|retired_pages/pending_blacklist"), "Yes")
		x.RemapPending = yesNo(g.get("remapped_rows/remapped_row_pending"))
		x.RemapFailed = yesNo(g.get("remapped_rows/remapped_row_failure"))
		x.AddressingMode, x.C2CMode = g.get("addressing_mode"), g.get("c2c_mode")
		x.TempTLimitC = num(g.get("temperature/gpu_temp_tlimit"))
		if ps := g.child("processes"); ps != nil {
			for _, pi := range ps.Children {
				if pi.Name != "process_info" {
					continue
				}
				pr := NvidiaProcess{Type: pi.get("type"), Name: pi.get("process_name"), MemBytes: mib(pi.get("used_memory"))}
				if v := num(pi.get("pid")); v != nil {
					pr.PID = int(*v)
				}
				x.Processes = append(x.Processes, pr)
				x.ProcessMemBytes = sum(x.ProcessMemBytes, pr.MemBytes)
			}
		}
		for _, sec := range []string{"clocks_event_reasons", "clocks_throttle_reasons"} {
			r := g.child(sec)
			if r == nil {
				continue
			}
			for _, c := range r.Children {
				if strings.TrimSpace(c.Text) == "Active" {
					name := strings.TrimPrefix(strings.TrimPrefix(c.Name, "clocks_event_reason_"), "clocks_throttle_reason_")
					if name != "gpu_idle" {
						x.ThrottleReasons = append(x.ThrottleReasons, name)
					}
				}
			}
			break
		}
		out = append(out, x)
	}
	return out, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
