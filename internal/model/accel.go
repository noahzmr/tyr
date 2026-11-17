package model

// Accel is the GPU/NPU module section.
type Accel struct {
	Header
	GPUs        []Accelerator `json:"gpus"`
	NPUs        []Accelerator `json:"npus"`
	BMCDisplays []PCIDevice   `json:"bmc_displays,omitempty"`
}

// PCIDevice is a minimal PCI device description.
type PCIDevice struct {
	Address string `json:"pci_address"`
	Vendor  string `json:"vendor"`
	Model   string `json:"model"`
	Driver  string `json:"driver,omitempty"`
}

// Accelerator is a GPU or NPU.
type Accelerator struct {
	Kind          string `json:"kind"` // gpu, npu
	Index         int    `json:"index"`
	Name          string `json:"name"` // card0, accel0, nvidia0
	PCIAddress    string `json:"pci_address"`
	Vendor        string `json:"vendor"`
	Model         string `json:"model"`
	Driver        string `json:"driver,omitempty"`
	DriverVersion string `json:"driver_version,omitempty"`
	Firmware      string `json:"firmware,omitempty"` // VBIOS or NPU firmware
	UUID          string `json:"uuid,omitempty"`
	Serial        string `json:"serial,omitempty"`
	PartNumber    string `json:"part_number,omitempty"`
	NUMANode      int    `json:"numa_node"`
	Integrated    bool   `json:"integrated"`
	// Interconnect names a CPU link other than PCIe ("NVLink-C2C" on Grace
	// superchips and DGX Spark's GB10); the PCIe function is then only a
	// stub and its link width means nothing.
	Interconnect string `json:"interconnect,omitempty"`
	// SharedMemory: the accelerator uses system RAM (unified memory) and
	// has no memory of its own; SharedMemBytes is the system's total.
	SharedMemory       bool     `json:"shared_memory,omitempty"`
	SharedMemBytes     *uint64  `json:"shared_memory_bytes,omitempty"`
	LinkSpeed          string   `json:"pcie_link_speed,omitempty"`
	LinkWidth          *int     `json:"pcie_link_width"`
	MaxLinkSpeed       string   `json:"pcie_max_link_speed,omitempty"`
	MaxLinkWidth       *int     `json:"pcie_max_link_width"`
	PowerState         string   `json:"power_state,omitempty"` // D0, D3hot
	UtilizationPercent *float64 `json:"utilization_percent"`
	MemTotalBytes      *uint64  `json:"memory_total_bytes"`
	MemUsedBytes       *uint64  `json:"memory_used_bytes"`
	PowerWatts         *float64 `json:"power_watts"`
	PowerCapWatts      *float64 `json:"power_cap_watts"`
	ClockMHz           *float64 `json:"clock_mhz"`
	Temps              []Sensor `json:"temps"`
	Fans               []Sensor `json:"fans"`
	ECCCorrectable     *uint64  `json:"ecc_correctable"`
	ECCUncorrectable   *uint64  `json:"ecc_uncorrectable"`
	RetiredPages       *uint64  `json:"retired_pages"`
	RemapPending       *bool    `json:"row_remap_pending"`
	RemapFailed        *bool    `json:"row_remap_failed"`
	ThrottleReasons    []string `json:"throttle_reasons"`
	KernelErrors       int      `json:"kernel_log_errors"`
	// Processes holding GPU memory, largest first (nvidia-smi).
	Processes     []GPUProcess `json:"processes,omitempty"`
	BusyTimeUs    *uint64      `json:"busy_time_us,omitempty"` // NPU cumulative busy time
	RuntimeStatus string       `json:"runtime_status,omitempty"`
	// Runtime power management: cumulative time the device was powered up,
	// and the share of the last refresh interval it was awake.
	RuntimeActiveMs *uint64  `json:"runtime_active_ms,omitempty"`
	AwakePercent    *float64 `json:"awake_percent,omitempty"`
	// AMD XDNA NPU (xrt-smi examine).
	PowerMode  string `json:"power_mode,omitempty"`
	AIEColumns *int   `json:"aie_columns,omitempty"`
	HWContexts *int   `json:"hw_contexts,omitempty"`
	// SensorNote explains missing or shared sensors.
	SensorNote string `json:"sensor_note,omitempty"`
	Status     Status `json:"status"`
}

func (a *Accel) counters(c map[string]uint64) {
	for _, list := range [][]Accelerator{a.GPUs, a.NPUs} {
		for _, x := range list {
			p := "accel." + x.PCIAddress + "."
			if x.ECCCorrectable != nil {
				c[p+"ecc.ce"] = *x.ECCCorrectable
			}
			if x.ECCUncorrectable != nil {
				c[p+"ecc.ue"] = *x.ECCUncorrectable
			}
			if x.RetiredPages != nil {
				c[p+"retired_pages"] = *x.RetiredPages
			}
			c[p+"kernel_errors"] = uint64(x.KernelErrors)
		}
	}
}

// GPUProcess is a process using an accelerator.
type GPUProcess struct {
	PID      int     `json:"pid"`
	Type     string  `json:"type,omitempty"` // C compute, G graphics
	Name     string  `json:"name"`
	MemBytes *uint64 `json:"memory_bytes"`
}
