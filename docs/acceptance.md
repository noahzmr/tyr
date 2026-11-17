# Acceptance test and the Tyr ISO

Is this machine fit for production? `tyr accept` answers that with one
verdict, and the Tyr ISO runs it by itself: boot the ISO (BMC virtual media
or a USB stick), wait, read the verdict.

## What runs

1. **Health check**: every module, plus the quick per-core test. Dead parts,
   degraded links and logged hardware errors show up here.
2. **CPU**: the torture engine (Tyr's own Prime95-style test, blend of FFT
   sizes) on all CPUs; every result is checked.
3. **Memory**: the built-in pattern test over 90 % of the available memory,
   locked in RAM.
4. **Storage**: a read-only surface scan of all disks at once (never a
   write). USB disks are left out: the boot stick, the results stick and BMC
   virtual media.
5. **Network**: only with a peer (`tyr net-peer` on another host).
6. **Final check**: every module again, after the load.

Each stress test has its safety guard and its own verdict (see
[stress-tests.md](stress-tests.md)). The acceptance test then judges
everything against the limits of the profile.

| Profile | CPU | Memory | Storage | Network | About |
|---|---|---|---|---|---|
| `quick` | 10 min | 10 min | 5 min | 2 min | 30 min: dead parts, not marginal ones |
| `burnin` (default) | 90 min | 90 min | 45 min | 10 min | 4 h: marginal CPUs, DIMMs and disks show up |
| `extended` | 8 h | 8 h | 6 h | 30 min | 24 h: before a new server goes into production |

The storage scan stops after its time and reports how far it got; a full pass
of a large hard disk takes many hours.

## Verdict

| Verdict | Exit code | Means |
|---|---|---|
| **PASS** | 0 | fit for production |
| **REMARKS** | 1 | passed; read the remarks (cooling, a slow link, an uneven NUMA layout …) |
| **FAIL** | 2 | not fit: see the failures |
| **INCOMPLETE** | 3 | a test could not run or was stopped |

The machine **fails** when

- any finding is critical (before or after the load),
- a finding from the profile's `fail_on_warn` list is a warning: worn or
  failing hardware rather than a setting, such as pending or reallocated
  sectors, correctable ECC errors, an uncorrected machine check, a missing
  CPU, a transceiver out of range, a GPU on fewer PCIe lanes, a failed power
  supply or lost PSU redundancy, a failed fan,
- a stress test fails: a wrong result, a guard stop, a new critical finding,
- an error counter grew during the test: machine checks, uncorrectable ECC,
  SMART, I/O and driver errors always; correctable ECC, CRC and link
  failures by default (`fail_counters`),
- a temperature under load passed a limit you set (`max_temp_c`),
- the machine has less than a minimum you set (`min`).

Other warnings are **remarks**. Findings in `ignore` do not count.

## Limits file

Every profile can be changed with a JSON file; fields in it replace the
profile's, durations and temperatures per key. Print the full profile as a
template:

```bash
tyr accept --profile burnin --print-profile > accept.json
```

A file that requires 512 GB of memory, 64 logical CPUs and 8 disks, lets the
CPU run 2 hours and allows at most 85 °C on the CPU and 55 °C on the disks
under load:

```json
{
  "durations": { "cpu": "2h" },
  "max_temp_c": { "cpu": 85, "disk": 55 },
  "min": { "memory_gb": 512, "logical_cpus": 64, "disks": 8 }
}
```

| Field | Meaning |
|---|---|
| `durations` | `cpu`, `memory`, `storage`, `network`: `"90m"`, `"2h"`; `"0s"` skips a part |
| `cpu_engine` | `torture` (default) or `builtin` |
| `fft` | torture: `blend` (default), `small` (most heat), `large` (memory) |
| `memory_percent` | memory test size, percent of available memory (default 90) |
| `storage_include_usb` | also scan USB disks |
| `fail_on_warn` | finding codes whose warnings fail the test (`pending_sectors` or `storage/pending_sectors`); the codes are in the alert catalog (`A` in the TUI) |
| `ignore` | finding codes that do not count |
| `fail_counters` | counter names (part of the name, e.g. `.ce`) whose growth fails the test |
| `max_temp_c` | `cpu`, `disk`, `dimm`: highest temperature under load |
| `min` | `memory_gb` (installed), `logical_cpus` (online), `disks` (physical, not USB) |

Unknown fields are rejected, so a typo cannot silently weaken a limit.

## Results

`tyr accept` writes a directory per run, named after the serial number and
the start time (UTC):

```text
CZ2D1234AB-20261117T090000Z/
├── VERDICT          PASS, REMARKS, FAIL or INCOMPLETE
├── accept.md        the report: verdict, failures, remarks, steps, limits, the health report
├── accept.json      the same as data
├── before.json      snapshot before the load
├── after.json       snapshot after the load
├── console.log      the console output (ISO)
└── runs/            one result per stress test (.json and .md)
```

`tyr view accept.md` shows the report in a terminal; `tyr history DIR`
reads a results directory (its snapshots and stress runs), or many of them.

## The Tyr ISO

A Debian 13 live system with Tyr and everything it uses: smartmontools,
nvme-cli, ipmitool, rasdaemon, lldpd, ethtool, stress-ng, memtester, iperf3,
CPU microcode and the firmware for common server NICs and controllers.
amd64 boots with BIOS and UEFI, arm64 with UEFI. It starts through Debian's
signed shim and GRUB, so Secure Boot can stay on.

Tested so far: amd64 under UEFI in QEMU, from boot to the results on the
stick. Not yet tested: Secure Boot switched on, BIOS boot, the arm64 image,
real servers.

### Use

1. Optional: a USB stick with a partition labelled **`TYR-RESULTS`** (FAT32,
   exFAT or ext4) for the results; an `accept.json` on it replaces the
   limits. Without it the results stay in memory until you plug one in and
   press `c`.

   ```bash
   sudo mkfs.vfat -n TYR-RESULTS /dev/sdX1
   ```

2. Boot the ISO: as BMC virtual media (iLO: Virtual Media → CD/DVD, iDRAC:
   Virtual Console → Virtual Media) or from a USB stick
   (`sudo dd if=tyr-live-….iso of=/dev/sdX bs=4M conv=fsync`).
3. The boot menu starts the burn-in after 15 seconds. It also offers quick,
   extended, an interactive mode without a test, memtest86+ (amd64; with
   Secure Boot off) and the UEFI settings.
4. Tyr waits 15 more seconds (any key opens a menu), then runs the test on
   the first console. At the end the verdict fills the screen with the
   failures and remarks, and a menu: view the report, copy the results to a
   stick, the TUI, a shell, run again, reboot, power off.

| Console | Shows |
|---|---|
| Alt+F1 | the acceptance test and the verdict |
| Alt+F2 | a root shell |
| Alt+F3 | Tyr's TUI, live (without SMART queries, which could count as disk errors in the test on some controllers) |
| serial ttyS0 (115200) | kernel messages and a login (iLO VSP, iDRAC SOL) |

The system runs from memory (`toram`): the virtual media can disconnect
during a long run. It never writes to the server's disks: no automounting,
no mdadm or LVM, the disk test only reads; the one partition it writes is
`TYR-RESULTS`.

Kernel command line (`e` in the boot menu):

| Parameter | Effect |
|---|---|
| `tyr.profile=burnin\|quick\|extended\|none` | the profile; `none` opens the menu |
| `tyr.peer=HOST[:PORT] tyr.token=TOKEN` | network test against `tyr net-peer` (turns on DHCP) |
| `tyr.dhcp=1` | DHCP on all wired NICs without a peer |
| `tyr.wait=SECONDS` | the countdown before the test (default 15) |

The NICs are up without an address otherwise, so Tyr sees link, speed and
LLDP neighbours.

### Build

Needs Docker; the first build downloads about 1 GB of Debian packages.

```bash
make iso          # dist/tyr-live-<version>-amd64.iso
make iso-arm64    # dist/tyr-live-<version>-arm64.iso
```

arm64 builds natively on an arm64 host (Ampere, Grace, DGX Spark). On an
amd64 host it runs under emulation (much slower) and needs qemu-user
binfmt once: `sudo apt install qemu-user-binfmt`.

- **Limits for every machine**: `iso/accept.json` becomes
  `/etc/tyr/accept.json` in the image (a `TYR-RESULTS` stick's
  `accept.json` still wins).
- **Vendor tools** (StorCLI, PERCCLI, ssacli): put their `.deb` or `.rpm`
  into `iso/vendor/`; see [tools.md](tools.md) for the downloads. Their
  licences usually forbid passing them on: keep such ISOs inside your
  organisation.
- **NVIDIA GPUs**: the ISO has no NVIDIA driver; Tyr checks those GPUs
  through PCI only (link, errors in the kernel log).
- `MIRROR=http://…/debian make iso` uses another Debian mirror.

What goes into the image: `iso/builder.sh` (packages), `iso/overlay/`
(services, network, the `tyr-live` script), `iso/grub.cfg` (boot menu).
