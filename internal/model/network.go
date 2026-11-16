package model

// Network is the network module section.
type Network struct {
	Header
	Interfaces []NIC   `json:"interfaces"`
	BMC        *BMCLan `json:"bmc_lan,omitempty"`
	Hidden     int     `json:"hidden_virtual"` // virtual interfaces not shown (use --nic-all)
}

// NIC is a physical or logical network interface.
type NIC struct {
	Name           string            `json:"name"`
	Kind           string            `json:"kind"` // physical, wireless, bond, bridge, vlan, virtual
	MAC            string            `json:"mac,omitempty"`
	PCIAddress     string            `json:"pci_address,omitempty"`
	Vendor         string            `json:"vendor,omitempty"`
	Model          string            `json:"model,omitempty"`
	Driver         string            `json:"driver,omitempty"`
	DriverVersion  string            `json:"driver_version,omitempty"`
	Firmware       string            `json:"firmware,omitempty"`
	Serial         string            `json:"serial,omitempty"`      // PCI VPD [SN]
	PartNumber     string            `json:"part_number,omitempty"` // PCI VPD [PN]
	NUMANode       int               `json:"numa_node"`
	PortType       string            `json:"port_type,omitempty"`
	MaxSpeedMbps   *int              `json:"max_speed_mbps"`
	AdminUp        bool              `json:"admin_up"`
	OperState      string            `json:"oper_state"`
	Carrier        *bool             `json:"carrier"`
	SpeedMbps      *int              `json:"speed_mbps"`
	Duplex         string            `json:"duplex,omitempty"`
	Autoneg        string            `json:"autoneg,omitempty"`
	MTU            int               `json:"mtu"`
	CarrierChanges *uint64           `json:"carrier_changes"`
	Addresses      []string          `json:"addresses"`
	Master         string            `json:"master,omitempty"`  // bond/bridge this NIC belongs to
	Members        []string          `json:"members,omitempty"` // for bond/bridge/vlan
	Bond           *Bond             `json:"bond,omitempty"`
	VLANID         *int              `json:"vlan_id"`
	Counters       NICCounters       `json:"counters"`
	RxBps          *float64          `json:"rx_bps"` // bits per second since last refresh
	TxBps          *float64          `json:"tx_bps"`
	ErrorCounters  map[string]uint64 `json:"error_counters,omitempty"` // non-zero ethtool -S error counters
	Module         *Transceiver      `json:"transceiver,omitempty"`
	TempCelsius    *float64          `json:"temp_celsius"`
	LLDP           *LLDPNeighbor     `json:"lldp,omitempty"`
	Status         Status            `json:"status"`
}

// NICCounters are /sys/class/net/*/statistics values.
type NICCounters struct {
	RxBytes    uint64 `json:"rx_bytes"`
	TxBytes    uint64 `json:"tx_bytes"`
	RxPackets  uint64 `json:"rx_packets"`
	TxPackets  uint64 `json:"tx_packets"`
	RxErrors   uint64 `json:"rx_errors"`
	TxErrors   uint64 `json:"tx_errors"`
	RxDropped  uint64 `json:"rx_dropped"`
	TxDropped  uint64 `json:"tx_dropped"`
	RxCRC      uint64 `json:"rx_crc_errors"`
	RxFrame    uint64 `json:"rx_frame_errors"`
	RxMissed   uint64 `json:"rx_missed_errors"`
	Collisions uint64 `json:"collisions"`
}

// Bond is the state of a bonding master.
type Bond struct {
	Mode        string      `json:"mode"`
	ActiveSlave string      `json:"active_slave,omitempty"`
	MIIStatus   string      `json:"mii_status"`
	Slaves      []BondSlave `json:"slaves"`
}

// BondSlave is one bond member.
type BondSlave struct {
	Name         string `json:"name"`
	MIIStatus    string `json:"mii_status"`
	Speed        string `json:"speed,omitempty"`
	Duplex       string `json:"duplex,omitempty"`
	LinkFailures uint64 `json:"link_failures"`
	AggregatorID string `json:"aggregator_id,omitempty"`
}

// Transceiver is an SFP/QSFP module with digital optical monitoring.
type Transceiver struct {
	Identifier string   `json:"identifier,omitempty"` // SFP, QSFP28
	Type       string   `json:"type,omitempty"`       // 25G Base-SR, ...
	Vendor     string   `json:"vendor,omitempty"`
	PartNumber string   `json:"part_number,omitempty"`
	Serial     string   `json:"serial,omitempty"`
	Wavelength string   `json:"wavelength,omitempty"`
	DateCode   string   `json:"date_code,omitempty"` // manufacturing date, YYYY-MM-DD
	Sensors    []Sensor `json:"sensors"`             // temperature, voltage, tx/rx power (dBm) per lane
	Status     Status   `json:"status"`
}

// LLDPNeighbor is the switch port seen via LLDP.
type LLDPNeighbor struct {
	System    string `json:"system"`
	Port      string `json:"port"`
	PortDescr string `json:"port_description,omitempty"`
	VLAN      string `json:"vlan,omitempty"`
}

// BMCLan is the BMC network configuration (read-only).
type BMCLan struct {
	IPSource string `json:"ip_source,omitempty"`
	IP       string `json:"ip,omitempty"`
	Netmask  string `json:"netmask,omitempty"`
	Gateway  string `json:"gateway,omitempty"`
	MAC      string `json:"mac,omitempty"`
	VLAN     string `json:"vlan,omitempty"`
}

func (n *Network) counters(c map[string]uint64) {
	for _, x := range n.Interfaces {
		p := "net." + x.Name + "."
		c[p+"rx_errors"] = x.Counters.RxErrors
		c[p+"tx_errors"] = x.Counters.TxErrors
		c[p+"rx_crc_errors"] = x.Counters.RxCRC
		c[p+"rx_dropped"] = x.Counters.RxDropped
		c[p+"rx_missed"] = x.Counters.RxMissed
		if x.CarrierChanges != nil {
			c[p+"carrier_changes"] = *x.CarrierChanges
		}
		if x.Bond != nil {
			for _, s := range x.Bond.Slaves {
				c[p+"slave."+s.Name+".link_failures"] = s.LinkFailures
			}
		}
	}
}
