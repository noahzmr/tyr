package model

import (
	"fmt"
	"strings"
)

// Network is the network module section.
type Network struct {
	Header
	Interfaces []NIC   `json:"interfaces"`
	BMC        *BMCLan `json:"bmc_lan,omitempty"`
	Hidden     int     `json:"hidden_virtual"` // virtual interfaces not shown (use --nic-all)
	// Softnet: packets the kernel's per-CPU receive backlog dropped
	// (netdev_max_backlog full), and receive softirq budget overruns.
	SoftnetDropped  *uint64 `json:"softnet_dropped,omitempty"`
	SoftnetSqueezed *uint64 `json:"softnet_time_squeeze,omitempty"`
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
	RingRX         *int              `json:"ring_rx,omitempty"` // receive ring entries now (ethtool -g)
	RingRXMax      *int              `json:"ring_rx_max,omitempty"`
	RxQueues       *int              `json:"rx_queues,omitempty"` // ethtool -l
	// Losses explains every drop or loss counter that grew since the last
	// refresh: where the packets were lost and the likely cause here.
	Losses []NICLoss `json:"losses,omitempty"`
	Status Status    `json:"status"`
}

// NICLoss is one growing drop/loss counter of an interface.
type NICLoss struct {
	Counter  string  `json:"counter"` // rx_missed, rx_dropped, tx_dropped
	Total    uint64  `json:"total"`   // since boot
	Delta    uint64  `json:"delta"`   // since the last refresh
	PerSec   float64 `json:"per_second"`
	SharePct float64 `json:"share_percent"` // of the packets in that interval
	Where    string  `json:"where"`         // "in the NIC", "in the kernel", "on the members"
	Cause    string  `json:"cause"`
	Status   Status  `json:"status"`
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
	if n.SoftnetDropped != nil {
		c["net.softnet.dropped"] = *n.SoftnetDropped
	}
	for _, x := range n.Interfaces {
		p := "net." + x.Name + "."
		c[p+"rx_errors"] = x.Counters.RxErrors
		c[p+"tx_errors"] = x.Counters.TxErrors
		c[p+"rx_crc_errors"] = x.Counters.RxCRC
		c[p+"rx_dropped"] = x.Counters.RxDropped
		c[p+"rx_missed"] = x.Counters.RxMissed
		c[p+"tx_dropped"] = x.Counters.TxDropped
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

// Interface returns the interface with the given name.
func (n *Network) Interface(name string) *NIC {
	if n == nil {
		return nil
	}
	for i := range n.Interfaces {
		if n.Interfaces[i].Name == name {
			return &n.Interfaces[i]
		}
	}
	return nil
}

// LossCause says where a drop/loss counter of x counts its packets and
// what usually causes it on this kind of interface. benign: expected by
// design (the backup member of an active-backup bond, a bond's sum).
func (n *Network) LossCause(x *NIC, counter string, softnetRising bool) (where, cause string, benign bool) {
	master := n.Interface(x.Master)
	switch counter {
	case "rx_missed":
		ring := ""
		if x.RingRX != nil {
			ring = fmt.Sprintf(" (%d", *x.RingRX)
			if x.RingRXMax != nil {
				ring += fmt.Sprintf(" of %d", *x.RingRXMax)
			}
			ring += " entries"
			if x.RxQueues != nil {
				ring += fmt.Sprintf(", %d queues", *x.RxQueues)
			}
			ring += ")"
		}
		if x.Kind == "bond" {
			return "on the members", "the sum of its members' rx_missed: the loss happens in a member NIC (see there)", true
		}
		return "in the NIC", "the NIC had no free receive buffer: its receive ring" + ring +
			" filled before the CPUs emptied it - traffic bursts, too small a ring, too few queues, or busy CPUs serving the interrupts", false
	case "rx_dropped":
		if master != nil && master.Bond != nil && strings.Contains(master.Bond.Mode, "active-backup") && master.Bond.ActiveSlave != "" && master.Bond.ActiveSlave != x.Name {
			return "in the kernel (bond backup)", "the backup member of active-backup bond " + master.Name + " drops everything it receives (broadcasts, multicast) by design", true
		}
		if x.Kind == "bond" {
			return "on the members", "the sum of its members' drops: look at the members (a backup member of an active-backup bond drops by design)", true
		}
		if softnetRising {
			return "in the kernel (backlog)", "the per-CPU receive backlog was full (softnet dropped rises too): raise net.core.netdev_max_backlog, spread interrupts (RSS queues, irqbalance)", false
		}
		return "in the kernel", "frames the stack did not deliver: a protocol or VLAN this host does not use (LLDP, CDP, STP from the switch; VLAN tags without an interface), multicast without a listener, or packets for another MAC (bridge/promiscuous)", false
	case "tx_dropped":
		return "in the kernel (send)", "packets the queue could not take: the transmit queue or driver ring was full, or the link was down while sending", false
	}
	return "", "", false
}
