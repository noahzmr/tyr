# External tools

Tyr is one static binary and runs without any of the tools below. Each one
adds data or a test where Linux has no interface Tyr can read itself. What a
missing tool costs is listed per module under the data sources ("○
unavailable" with the reason), so nothing is silently missing.

The Tyr ISO ([acceptance.md](acceptance.md)) has every distribution tool
below built in; vendor tools go into `iso/vendor/` before building it.

Tyr looks for tools in `PATH` and in `/usr/local/sbin`, `/usr/local/bin`,
`/usr/sbin`, `/usr/bin`, `/sbin`, `/bin`, `/opt/MegaRAID/storcli`,
`/opt/MegaRAID/perccli`, `/opt/dell/srvadmin/bin` and `/opt/xilinx/xrt/bin`.

## Not needed: Tyr reads these itself

| Tool | Tyr reads instead | The tool is |
|---|---|---|
| `dmidecode` | the SMBIOS table in `/sys/firmware/dmi/tables` | a fallback when the table cannot be read or decoded |
| `ethtool` | the NIC driver through `SIOCETHTOOL`: link modes, statistics, rings, queues, transceiver EEPROM | a fallback for drivers that refuse a request and for externally calibrated transceivers |
| `ipmitool` / `ipmiutil` | the BMC through `/dev/ipmi0`: sensors, event log, FRU, LAN | a fallback when the device cannot be used |
| `nvme` (nvme-cli) | the NVMe health log through an ioctl | a fallback for SMART on NVMe drives |
| `df` | `statfs` | used only in fixtures |
| `journalctl` | `/dev/kmsg` for this boot | part of systemd; Tyr uses it for earlier boots and the boot history |

`tyr check-native` (as root) compares the native readers with these tools on
a host and lists every difference.

## Recommended: from the distribution

| Tool | Package | Tyr uses it for | Without it |
|---|---|---|---|
| `smartctl` | `smartmontools` | SMART of SATA/SAS drives (and NVMe as fallback) | no SMART for SATA/SAS: reallocated and pending sectors, wear, CRC errors, power-on hours |
| `ras-mc-ctl` | `rasdaemon` (enable the service) | machine checks rasdaemon kept across boots | the history comes from the journal and pstore only |
| `lldpctl` | `lldpd` (enable the service) | the switch and port each NIC is connected to | no LLDP neighbours |
| `docker`, `go` | Docker, Go | building the Tyr ISO (`make iso`, `I` in the TUI) | no ISO; everything else works |
| `wg` | `wireguard-tools` | WireGuard peers: endpoint, last handshake, traffic (as root; keys other than public ones are never kept) | WireGuard interfaces without peer status |
| `ipmitool` | `ipmitool` | BMC fallback, and `tyr check-native` | nothing while `/dev/ipmi0` works |
| `wl-copy` / `xclip` / `xsel` | `wl-clipboard` / `xclip` / `xsel` | copying in terminals without OSC 52 (GNOME Terminal, Ptyxis) on a desktop | copy works only through OSC 52 |

| Distribution | Install |
|---|---|
| Debian, Ubuntu | `sudo apt-get install smartmontools rasdaemon lldpd ipmitool` |
| RHEL, Rocky, Alma, Fedora | `sudo dnf install smartmontools rasdaemon ipmitool`; `lldpd` comes from [EPEL](https://docs.fedoraproject.org/en-US/epel/) on RHEL and its rebuilds |
| SUSE | `sudo zypper install smartmontools rasdaemon lldpd ipmitool` |

```bash
sudo systemctl enable --now rasdaemon lldpd
```

Official documentation: [smartmontools](https://www.smartmontools.org/),
[rasdaemon](https://github.com/mchehab/rasdaemon),
[lldpd](https://lldpd.github.io/),
[ipmitool](https://github.com/ipmitool/ipmitool),
[wl-clipboard](https://github.com/bugaevc/wl-clipboard),
[xclip](https://github.com/astrand/xclip).

## Stress engines: from the distribution, or from the TUI

Tyr's own engines (`builtin`, `torture`, the storage scan, the network test
against `tyr net-peer`) need nothing. These add more:

| Engine | Package | Official documentation |
|---|---|---|
| `stress-ng` | `stress-ng` (EPEL on RHEL) | [github.com/ColinIanKing/stress-ng](https://github.com/ColinIanKing/stress-ng) |
| `memtester` | `memtester` (EPEL on RHEL) | [pyropus.ca/software/memtester](https://pyropus.ca/software/memtester/) |
| `iperf3` | `iperf3` | [software.es.net/iperf](https://software.es.net/iperf/) |

`i` on the Stress tab installs the missing ones with the host's package
manager (`apt-get`, `dnf`, `yum`, `zypper` or `pacman`, through `sudo` when
not root) after showing the exact command.

## Manual installation: vendor tools

These are not in the distributions' repositories. Install them only on the
machines that have the hardware.

### StorCLI / PERCCLI - MegaRAID and Dell PERC controllers

Tyr reads the controller, its virtual and physical drives and the cache
battery with `storcli64` (Broadcom MegaRAID) or `perccli64` (Dell PERC).
Without them, drives behind such a controller show no RAID state.

| Vendor | Where | Notes |
|---|---|---|
| Broadcom | [Broadcom support: download search](https://www.broadcom.com/support/download-search), search "StorCLI" | RPM/DEB; installs to `/opt/MegaRAID/storcli/` (found there) |
| Dell | [Dell support](https://www.dell.com/support/home), your server's drivers, category "SAS RAID", "PERC CLI" | installs to `/opt/MegaRAID/perccli/` (found there) |

```bash
sudo dnf install ./storcli-*.noarch.rpm      # or: sudo apt-get install ./storcli_*_all.deb
```

### NVIDIA GPUs: `nvidia-smi`

Comes with the NVIDIA driver; there is nothing to install separately. Use the
driver of your distribution or NVIDIA's:
[NVIDIA Driver Installation Guide](https://docs.nvidia.com/datacenter/tesla/driver-installation-guide/).
Without it, Tyr shows the GPU's PCIe link only.

### AMD NPUs: `xrt-smi`

Part of AMD's XRT and XDNA driver:
[github.com/amd/xdna-driver](https://github.com/amd/xdna-driver) (build and
install instructions in its README). Tyr finds `xrt-smi` in
`/opt/xilinx/xrt/bin`.

### Prime95 / mprime: optional

Not needed: Tyr's own `torture` engine runs the same kind of test (Lucas-Lehmer
FFTs on the vector units with FMA, results checked). The `mprime` engine runs
Prime95's own code when you want exactly that - for example to compare with
results from other machines. It is not packaged and has its own license terms
(see the download page).

1. Download the Linux 64-bit tarball from
   [mersenne.org/download](https://www.mersenne.org/download/).
2. Check its checksum against the page, then:

```bash
mkdir -p /tmp/mprime && tar -xzf p95v*.linux64.tar.gz -C /tmp/mprime
sudo install -m 755 /tmp/mprime/mprime /usr/local/bin/mprime
```

Tyr runs `mprime -t` in a temporary directory and stops it with the test.

### memtest86+: the pre-OS memory test

Tyr never installs it: its package changes the boot loader. The in-OS memory
test cannot check memory the kernel and running programs use; for that, boot
memtest86+ ([memtest.org](https://www.memtest.org/)) from the boot menu or a
USB stick. When it is installed, the memory test's plan says where.

## Not run by Tyr: for troubleshooting

The alert explanations (`i`, `A`) name these tools as next steps; Tyr does not
run them.

| Tool | For | Official documentation |
|---|---|---|
| `ssacli` | HPE Smart Array configuration: empty bays, disabled drives, cache | [HPE Software Delivery Repository](https://downloads.linux.hpe.com/SDR/), project "mcp" |
| `ilorest` | HPE iLO from the OS: IML, AHS log | [github.com/HewlettPackard/python-redfish-utility](https://github.com/HewlettPackard/python-redfish-utility) |
| `iscsiadm` | iSCSI sessions | package `open-iscsi` (Debian, Ubuntu, SUSE) / `iscsi-initiator-utils` (RHEL) |
