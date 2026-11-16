# Tyr: Design Proposal (Step 1)

Status: **draft, awaiting confirmation**. Covers package structure, shared data
model, JSON schema, and the stress-test interface. Nothing is implemented yet.

Module path: `github.com/noahzmr/tyr`

---

## 0. Findings that change the brief

| Topic | Finding | Proposal |
|---|---|---|
| Go version | The latest stable Charm releases moved to the `charm.land` vanity domain and need a newer Go than 1.22: `charm.land/bubbletea/v2` v2.1.1 (go 1.26), `charm.land/lipgloss/v2` v2.1.1 (go 1.26), `charm.land/bubbles/v2` v2.2.1 (go 1.25), `charm.land/huh/v2` v2.0.3 (go 1.25.8). `golang.org/x/sys` v0.50.0 also needs go 1.26. | Set `go 1.26` in `go.mod`. This only affects the **build** toolchain. The output is still a static `CGO_ENABLED=0` binary that runs on any Linux x86_64/arm64 kernel Go supports (≥ 3.2). The alternative, pinning Charm v1 (`github.com/charmbracelet/*`) to keep 1.22, would lock us to a line that's in maintenance. |
| Bubble Tea v2 API | `View()` returns a `tea.View`. Keys arrive as `tea.KeyPressMsg`. Lip Gloss v2 no longer detects the background itself. | The adaptive light/dark theme listens for `tea.BackgroundColorMsg` and uses `lipgloss.LightDark`. `NO_COLOR` and the color profile are handled by Bubble Tea v2's `colorprofile` integration. |
| Sparklines/graphs | Bubbles has no sparkline or chart component. | A small in-house renderer (`▁▂▃▄▅▆▇█` blocks, optional braille) in `internal/tui/components`. That saves a dependency, and it's about 150 LOC. |
| Glamour | Pulls in goldmark and chroma, which grows the binary noticeably. | Leave it out for v1. Help and detail text is plain Lip Gloss. |
| Float determinism | The Go spec allows the compiler to fuse `x*y+z` into FMA on arm64 (not on amd64), so naive float golden values differ by arch. | FP kernels call `math.FMA` explicitly where FMA is wanted and use explicit `float64(...)` conversions everywhere else to block fusion. Golden tables are then identical on both arches, and a test enforces it. |
| `ipmiutil power` | This subcommand is **chassis power control**, not a power reading. | The IPMI source runs only from an **allowlist of read-only subcommands** (`sensor`, `sel`, `fru`, `lan` (read), `health`, `dcmi` reading). `sel -d` (clear) is a separate, explicitly gated call. |
| ethtool | `ethtool -i` data is available as an ioctl in `x/sys/unix` (`IoctlGetEthtoolDrvinfo`). | Native ioctl for driver/firmware info. The CLI is used only for `-S` and `-m` (SFF-8472/8636 decoding isn't worth reimplementing). |
| Kernel log | `/dev/kmsg` is a kernel interface with structured records and needs no external tool. | Priority order: `/dev/kmsg`, then `journalctl -k -o json -b`, then `dmesg`. |
| CPU power w/o BMC | In VMs or on boards without a BMC there's no wall power. | Fall back to RAPL (`/sys/class/powercap/intel-rapl*`), clearly labelled as "CPU package/DRAM power, not wall power". |

---

## 1. Package structure

```
tyr/
├── cmd/tyr/                     main + subcommands (stdlib flag; no cobra)
│   ├── main.go                  dispatch, global flags, TTY detection, exit codes
│   ├── report.go  stress.go  netpeer.go  version.go  tui.go
├── internal/
│   ├── buildinfo/               version/commit/date via -ldflags
│   ├── model/                   shared types: Status, Header, Finding, Reading,
│   │                            Snapshot, per-module structs, Event, schema version
│   ├── check/                   threshold defaults + overrides, evaluators,
│   │                            rollup, Nagios summary-line builder
│   ├── hostfs/                  rooted read-only FS (real "/" or fixture root),
│   │                            symlink-safe resolution inside the root
│   ├── execx/                   Runner interface: real (CommandContext, LookPath,
│   │                            timeout, no shell, LC_ALL=C, output cap) and replay
│   ├── fixture/                 --fixtures loader (manifest, replay runner,
│   │                            live-value simulator for moving counters/temps)
│   ├── cpuset/                  parse/format "0-15,32-47", NUMA node → cpus
│   ├── source/
│   │   ├── sysfs/               topology, cpufreq, hwmon, thermal zones, EDAC, AER,
│   │   │                        thermal_throttle, powercap, /proc/{stat,meminfo,
│   │   │                        mdstat,vmstat,pressure/*,self/mountinfo}, block,
│   │   │                        net statistics, /proc/net/bonding
│   │   ├── dmi/                 dmidecode text parser (types 0,1,2,3,4,17,39)
│   │   │                        + /sys/class/dmi/id fallback (non-root)
│   │   ├── ipmi/                one interface, two backends: ipmiutil, ipmitool
│   │   ├── smart/               smartctl -j (ATA/SCSI/NVMe), nvme-cli -o json
│   │   ├── raid/                mdadm --detail, storcli/perccli (JSON), ssacli (text)
│   │   ├── kernellog/           kmsg/journalctl/dmesg readers + classifier
│   │   │                        (MCE, EDAC, AER, I/O, thermal, link reset)
│   │   ├── ethtool/             drvinfo ioctl, `ethtool -S`, `ethtool -m` parsers
│   │   ├── netlink/             link attrs + addresses (stdlib net + sysfs)
│   │   └── lldp/                lldpctl -f json
│   ├── collector/
│   │   ├── collector.go         Collector interface, Env, registry, parallel runner
│   │   └── system/ cpu/ memory/ storage/ power/ network/ events/
│   ├── workload/                verified compute kernels + golden tables (shared by
│   │                            the CPU quick core test and CPU stress) and memory
│   │                            test patterns (shared by memory stress)
│   ├── stress/
│   │   ├── engine.go            phases, monitor loop, verdict, diff, result store
│   │   ├── guard/               abort rules + resource tracker
│   │   ├── cpu/ memory/ storage/ network/ power/ burnin/
│   │   └── sim/                 simulated tests for --fixtures mode
│   ├── netproto/                wire protocol shared by stress/network and net-peer
│   ├── netpeer/                 `tyr net-peer` server
│   ├── report/text/  report/json/
│   └── tui/
│       ├── app.go keys.go       root model, tab routing, refresh ticker
│       ├── theme/               Lip Gloss styles, status colors, light/dark
│       ├── components/          badge, card, status table, sparkline, core grid,
│       │                        detail pane
│       ├── tabs/                overview, cpu, memory, storage, power, network,
│       │                        events, stress (one sub-model each)
│       └── stressflow/          huh form → confirm → live view → result
├── testdata/fixtures/<scenario>/   (see §6)
├── scripts/capture-fixtures.sh  run on a real server to produce a fixture set
├── docs/  Makefile  README.md
```

Dependency rules, enforced by a test that walks the imports:
`model` imports nothing internal. `source/*` imports only `hostfs`, `execx`, and `model`.
`collector/*` imports `source/*`, `check`, and `model`. `stress/*` imports `collector` and `workload`.
`tui` and `report` import `collector`, `stress`, and `model`, never `source` directly.

Third-party deps: Charm v2 (bubbletea, bubbles, lipgloss, huh) and
`golang.org/x/sys`. That's the whole list.

---

## 2. Shared data model (`internal/model`)

### 2.1 Severity

```go
type Status uint8

const (
    StatusOK Status = iota
    StatusUnknown
    StatusWarn
    StatusCrit
)
// Worst(a, b) = max(a, b). JSON: "OK" | "UNKNOWN" | "WARN" | "CRIT".
```

**Two separate concepts:**

- **Status UNKNOWN**: a thing exists, but its health couldn't be determined.
  Examples: a disk where `smartctl` returns an error, an IPMI sensor reading `na`.
  It takes part in rollup.
- **Availability**: whether a *source* or *item* could be read at all
  (`ok`, `unavailable`, `not_applicable`, `skipped`) plus a reason. This doesn't
  take part in rollup. "No `/dev/ipmi0` in a VM" is `not_applicable`. "smartctl
  not installed" and "requires root" are `unavailable` with a reason. Without
  this split, every VM and every host without smartmontools would exit 3.

### 2.2 Common building blocks

```go
type Header struct {
    Status      Status        `json:"status"`
    Summary     string        `json:"summary"`        // "64/64 cores online, max 71°C"
    Findings    []Finding     `json:"findings"`
    Sources     []SourceState `json:"sources"`
    CollectedAt time.Time     `json:"collected_at"`
    DurationMS  int64         `json:"duration_ms"`
}

type Finding struct {
    Status  Status `json:"status"`
    Code    string `json:"code"`     // stable machine code: "core_offline", "psu_failed"
    Subject string `json:"subject"`  // "cpu7", "/dev/sda", "PSU2", "bond0/eth1"
    Message string `json:"message"`  // human text, used in the summary line
    Source  string `json:"source"`   // "sysfs", "ipmi", "smart", ...
}

type SourceState struct {
    Name   string       `json:"name"`             // "dmidecode", "ipmiutil", ...
    State  Availability `json:"state"`            // ok|unavailable|not_applicable|skipped
    Reason string       `json:"reason,omitempty"` // "requires root", "not installed"
}

// Generic sensor (IPMI, hwmon, transceiver DOM). The unit lives in a field
// here because it varies per row. Typed fields elsewhere carry the unit in the name.
type Sensor struct {
    Name       string      `json:"name"`
    Kind       SensorKind  `json:"kind"`   // temperature|voltage|current|power|fan|...
    Value      *float64    `json:"value"`  // nil = no reading
    Unit       string      `json:"unit"`   // celsius|volts|amps|watts|rpm|dbm|...
    Status     Status      `json:"status"`
    Thresholds *Thresholds `json:"thresholds,omitempty"`
    Source     string      `json:"source"`
}

type Thresholds struct {
    LowerCrit *float64 `json:"lower_crit,omitempty"`
    LowerWarn *float64 `json:"lower_warn,omitempty"`
    UpperWarn *float64 `json:"upper_warn,omitempty"`
    UpperCrit *float64 `json:"upper_crit,omitempty"`
    Origin    string   `json:"origin"` // "ipmi" | "hwmon" | "smart" | "module" | "default"
}
```

Pointers are used for "no value", which shows up as JSON `null`. A reading of 0
is never confused with "not read".

### 2.3 Snapshot and module sections

```go
type Snapshot struct {
    SchemaVersion  string    `json:"schema_version"` // "1.0"
    TyrVersion     string    `json:"tyr_version"`
    Hostname       string    `json:"hostname"`
    Timestamp      time.Time `json:"timestamp"`
    Privileged     bool      `json:"privileged"`     // effective root/caps
    Virtualization string    `json:"virtualization"` // none|kvm|vmware|hyperv|xen|...
    Status         Status    `json:"status"`
    Summary        string    `json:"summary"`        // Nagios line, without the prefix

    System  *System  `json:"system,omitempty"`
    CPU     *CPU     `json:"cpu,omitempty"`
    Memory  *Memory  `json:"memory,omitempty"`
    Storage *Storage `json:"storage,omitempty"`
    Power   *Power   `json:"power,omitempty"`
    Network *Network `json:"network,omitempty"`
    Events  *Events  `json:"events,omitempty"`
}
```

Each section embeds `Header` (which flattens into JSON) and adds typed fields.
Abbreviated:

```go
type CPU struct {
    Header
    Vendor, Model, Microcode string
    Sockets          []CPUSocket   // dmi type 4: designation, status populated/enabled/disabled
    Topology         CPUTopology   // present/possible/online/offline as cpusets
    Logical          []LogicalCPU  // per thread, see below
    Governor         string
    VirtFlags        []string      // vmx/svm, ept/npt, ...
    BaseFreqMHz      *float64
    PackageTemps     []Sensor
    CoreTest         *CoreTestResult // only with --core-test
}
type LogicalCPU struct {
    ID, CoreID, SocketID, NUMANode int
    Online             bool
    UtilizationPercent *float64
    FreqMHz            *float64
    TempCelsius        *float64
    ThrottleCount      *uint64
    MCECount           *uint64
    CoreTest           CoreTestState // PASS|FAIL|TIMEOUT|SKIP|"" (not run)
}

type Memory struct {
    Header
    TotalBytes, AvailableBytes, UsedBytes, SwapTotalBytes, SwapUsedBytes uint64
    InstalledBytes uint64          // sum of DIMMs (dmi)
    DIMMs          []DIMM          // incl. empty slots (Populated=false)
    EDAC           []EDACController
    Temps          []Sensor
}
type DIMM struct {
    Locator, BankLocator, Type, Manufacturer, PartNumber, Serial string
    Populated                      bool
    SizeBytes                      uint64
    Rank                           *int
    SpeedMTs, ConfiguredSpeedMTs   *int
    CorrectableErrors, UncorrectableErrors *uint64 // from EDAC if mappable
    Status                         Status
}

type Storage struct {
    Header
    Disks       []Disk         // model, serial, fw, size_bytes, kind nvme|ssd|hdd,
                               // interface, rotation_rpm, smart{...}, temp_celsius
    MDArrays    []MDArray      // level, state, degraded, members, sync_percent
    Controllers []RAIDController
    Filesystems []Filesystem   // mount, fstype, used_percent, inode_used_percent, read_only
}

type Power struct {
    Header
    PSUs        []PSU          // slot, vendor, model, serial, rated_watts, present,
                               // failed, predictive_failure, ac_lost, input_volts, ...
    Redundancy  string         // fully_redundant|redundancy_lost|non_redundant|unknown
    PowerWatts  *float64
    PowerSource string         // "dcmi" | "ipmi_sensor" | "rapl"
    Rails       []Sensor       // 12V, 5V, 3.3V, VCORE, VBAT
}

type Network struct {
    Header
    Interfaces []NIC   // physical; logical (bond/vlan/bridge) carry Members []string
    BMCLan     *BMCLan
}

type Events struct {
    Header
    SELFillPercent *float64
    Items          []Event   // unified, time-sorted
}
type Event struct {
    Time     time.Time `json:"time"`
    Severity Status    `json:"severity"`
    Source   string    `json:"source"`   // sel|kernel
    Category string    `json:"category"` // mce|edac|aer|io|thermal|link|psu|fan|...
    Subject  string    `json:"subject"`
    Message  string    `json:"message"`
    Raw      string    `json:"raw,omitempty"`
}
```

`System` holds vendor, model, serial, BIOS version/date, board, BMC firmware,
fans (`[]Sensor`), inlet/exhaust temps, and uptime.

### 2.4 Counters for diffing

```go
// Counters flattens every monotonic counter in the snapshot into stable keys,
// e.g. "edac.mc0.dimm3.ce", "mce.cpu7", "aer.0000:3b:00.0.correctable",
// "smart./dev/sda.reallocated", "net.eth0.rx_crc_errors", "throttle.cpu12.core".
func (s *Snapshot) Counters() map[string]uint64
```

The stress engine and guard diff baseline against current through this one
function. No per-module diff code is needed.

---

## 3. Collector interface (`internal/collector`)

```go
type Collector interface {
    ID() model.ModuleID // cpu|memory|storage|power|network|events|system
    // Collect does a full collection when prev == nil. Otherwise it refreshes only
    // live values (sensors, counters, link state, utilization) on a copy of prev.
    // It always returns a usable section: partial data plus findings and
    // SourceState for anything that failed. A non-nil error is reserved for
    // "nothing could be collected"; the runner turns it into an UNKNOWN section.
    Collect(ctx context.Context, env *Env, prev model.Section) (model.Section, error)
}

type Env struct {
    FS     hostfs.FS     // "/" or the fixture root
    Exec   execx.Runner  // real or replay
    Now    func() time.Time
    Opts   Options       // --no-ipmi, --nic-all, --sel-all, thresholds, timeouts
    Priv   Privileges    // probed by access checks, not just uid==0, so CAP_* works
    Virt   string
    cache  *roundCache   // per-round memo: dmidecode, IPMI SDR, kernel log
}
// env.DMI(ctx), env.IPMISensors(ctx), env.KernelLog(ctx): each runs at most
// once per round, even though CPU/Memory/Power all need dmidecode.
```

The runner executes collectors concurrently, each under its own
`context.WithTimeout(--timeout)`, and recovers panics into an UNKNOWN section
with `"internal error: ..."`. `--section` limits which collectors run.

`execx.Runner`:

```go
type Runner interface {
    LookPath(name string) (string, error)
    Run(ctx context.Context, name string, args ...string) (stdout []byte, err error)
}
```

The real implementation uses `exec.CommandContext` with no shell and
`LC_ALL=C`. It caps stdout at 16 MiB, kills the process group on timeout, and
returns `*ExitError` with stderr attached. The IPMI backend wraps the runner in a
subcommand allowlist.

---

## 4. JSON schema

Conventions: snake_case keys, units in typed field names (`temp_celsius`,
`power_watts`, `size_bytes`, `throughput_bps`, `duration_ms`, `freq_mhz`),
RFC 3339 UTC timestamps, `null` for "not read". Additive changes bump the minor
version (`1.1`). Renames or removals bump the major. A hand-written JSON Schema
lives in `docs/schema/report.v1.json` and `docs/schema/stress-result.v1.json`,
and golden-file tests validate fixture output against it.

### 4.1 `tyr report --format json` (abbreviated)

```json
{
  "schema_version": "1.0",
  "tyr_version": "0.1.0",
  "hostname": "srv-db-01",
  "timestamp": "2026-11-16T09:12:03Z",
  "privileged": true,
  "virtualization": "none",
  "status": "CRIT",
  "summary": "PSU2 failed, redundancy lost; 1 core offline (cpu7)",
  "cpu": {
    "status": "CRIT",
    "summary": "63/64 cores online, max 71°C",
    "findings": [
      {"status": "CRIT", "code": "core_offline", "subject": "cpu7",
       "message": "cpu7 present but offline", "source": "sysfs"}
    ],
    "sources": [
      {"name": "sysfs", "state": "ok"},
      {"name": "dmidecode", "state": "ok"},
      {"name": "ipmi", "state": "ok"}
    ],
    "collected_at": "2026-11-16T09:12:02Z",
    "duration_ms": 412,
    "vendor": "GenuineIntel",
    "model": "Intel(R) Xeon(R) Gold 6338 CPU @ 2.00GHz",
    "microcode": "0xd0003a5",
    "topology": {"present": "0-63", "possible": "0-63", "online": "0-6,8-63", "offline": "7"},
    "logical": [
      {"id": 7, "core_id": 7, "socket_id": 0, "numa_node": 0, "online": false,
       "utilization_percent": null, "freq_mhz": null, "temp_celsius": null,
       "throttle_count": 0, "mce_count": 0, "core_test": "SKIP"}
    ]
  },
  "power": {
    "status": "CRIT",
    "summary": "1/2 PSU OK, 412 W",
    "redundancy": "redundancy_lost",
    "power_watts": 412,
    "power_source": "dcmi",
    "psus": [
      {"slot": "PSU2", "vendor": "DELTA", "model": "D1600E-S0", "serial": "…",
       "rated_watts": 1600, "present": true, "failed": true,
       "predictive_failure": false, "ac_lost": true, "status": "CRIT"}
    ],
    "rails": [
      {"name": "12V", "kind": "voltage", "value": 12.06, "unit": "volts",
       "status": "OK", "source": "ipmi",
       "thresholds": {"lower_crit": 10.8, "upper_crit": 13.2, "origin": "ipmi"}}
    ]
  },
  "events": {
    "status": "CRIT",
    "summary": "3 critical, 2 warning in SEL; SEL 41% full",
    "sel_fill_percent": 41,
    "items": [
      {"time": "2026-11-16T03:01:44Z", "severity": "CRIT", "source": "sel",
       "category": "psu", "subject": "PS2 Status", "message": "Power Supply AC lost"}
    ]
  }
}
```

### 4.2 Stress result file (`/var/lib/tyr/runs/<ts>-<component>-<id>.json`, mode 0600)

```json
{
  "schema_version": "1.0",
  "tyr_version": "0.1.0",
  "hostname": "srv-db-01",
  "run_id": "20261116T091500Z-memory-7f3a",
  "component": "memory",
  "config": {"duration_ms": 300000, "memory_percent": 70, "numa_node": null, "intensity": "max"},
  "started_at": "2026-11-16T09:15:00Z",
  "ended_at": "2026-11-16T09:20:00Z",
  "verdict": "FAIL",
  "aborted": null,
  "reasons": ["2 pattern mismatches", "EDAC correctable errors +14 on DIMM_B1"],
  "summary": "memory stress: 2 mismatches at 0x3a2f40000-0x3a2f40fff, DIMM_B1; 5m00s",
  "metrics": [
    {"name": "cpu_package_temp", "unit": "celsius", "min": 41, "avg": 63.2, "max": 71, "samples": 300},
    {"name": "mem_throughput", "unit": "bytes_per_second", "min": 1.1e10, "avg": 1.4e10, "max": 1.5e10, "samples": 300}
  ],
  "verify_errors": [
    {"time": "2026-11-16T09:17:12Z", "subject": "0x3a2f40000-0x3a2f40fff",
     "workload": "moving_inversions", "expected": "0x5555555555555555",
     "got": "0x5555555555555455", "detail": "virt 0x7f12…, phys 0x3a2f40a18, DIMM_B1 (EDAC correlation)"}
  ],
  "counter_deltas": {"edac.mc1.dimm2.ce": 14},
  "new_findings": [ /* Finding objects not present in baseline */ ],
  "new_events": [ /* Event objects with time in [started_at, ended_at] */ ],
  "baseline": { /* full Snapshot */ },
  "after":    { /* full Snapshot */ }
}
```

Burn-in also writes `<run_id>.metrics.jsonl`, one sample line per interval, so
a 24 h run never builds a huge in-memory slice.

---

## 5. Stress-test interface (`internal/stress`)

### 5.1 Test

```go
type Test interface {
    ID() model.ModuleID
    // Plan validates scope against the baseline snapshot and returns everything
    // the confirmation screen needs. It has NO side effects.
    Plan(cfg Config, base *model.Snapshot) (*Plan, error)
    // Run is the load phase. It must verify its own results and report every
    // mismatch through rt.VerifyError. It must return within 2s of ctx.Done()
    // with all goroutines stopped and all resources released.
    Run(ctx context.Context, plan *Plan, rt Runtime) error
}

type Plan struct {
    Test         model.ModuleID
    Scope        []string        // "cpu0-15", "/dev/nvme0n1", "eth0 → 10.0.0.2"
    Duration     time.Duration
    Impact       []string        // "Will saturate 16 cores; services on this host slow down"
    Warnings     []string        // "load average 23.1", "12 running VMs"
    Destructive  bool
    ConfirmToken string          // non-empty → user must type this (device name)
    Rules        []guard.Rule    // test-specific abort rules on top of the defaults
}

type Runtime interface {
    Progress(fraction float64, stage string) // "pass 2/4: moving inversions"
    Metric(name, unit string, v float64)     // throughput, iops, latency_p99, ...
    VerifyError(e VerifyError)               // wrong result / mismatch / read error
    Track(c io.Closer)                       // engine closes these if Run leaks them
    Logf(format string, args ...any)
}
```

### 5.2 Engine and phases

```go
func (e *Engine) Start(ctx context.Context, cfg Config) (*Run, error)

type Run struct{ /* ... */ }
func (r *Run) Events() <-chan Event // PhaseChanged, Sample, Metric, VerifyError, GuardTrip, Finished
func (r *Run) Abort(reason string)
func (r *Run) Wait() *Result
```

1. **Baseline**: a full `collector` snapshot, `Plan()`, and the busy-system
   check (loadavg vs nproc, running VMs from `/sys/fs/cgroup/machine.slice`
   and libvirt/qemu processes, container count from cgroups).
2. **Load**: `Test.Run` under a context that the guard, the user, or the
   duration deadline can cancel. A monitor loop runs alongside on two tiers:
   - fast (1 s): hwmon, thermal zones, EDAC counts, `/proc/{meminfo,vmstat,pressure/memory}`, throttle counters, NIC stats, RAPL
   - slow (10 s): IPMI sensors (BMCs are slow), SEL tail; SMART every 60 s for in-scope disks

   Every sample goes to the guard and to the event channel. The TUI consumes
   the channel with a `tea.Cmd` that reads one event at a time; the CLI prints
   a progress line every N seconds.
3. **After**: a full snapshot, `Counters()` diff, new findings (keyed by
   `code+subject`), new events in the time window, then the verdict and the
   JSON file. If `/var/lib/tyr/runs` isn't writable, it falls back to `./`.

**Verdict:**
- FAIL: any verify error, a guard abort, a new CRIT finding, a new MCE or UE,
  an increased SMART error counter, or a core test FAIL/TIMEOUT.
- WARN: a new WARN finding, CE increase, throttling events, or network
  throughput below 90 % of the negotiated link speed.
- PASS: none of the above.

User abort gives `aborted: {by: "user"}` and exit 3 (incomplete). A guard abort
gives FAIL and exit 2.

### 5.3 Guard (`internal/stress/guard`)

```go
type Rule interface {
    Name() string
    Eval(base, cur *model.Snapshot, d map[string]uint64 /* counter deltas */) (trip bool, reason string)
}
```

Default rules: `TempCritical` (any reading ≥ its source's crit threshold,
else `--crit-temp`), `NewMCE`, `NewUncorrectableECC`, `PSUFault`,
`RedundancyLost`, `NewSMARTErrors` (in-scope disks), and `MemoryPressure`
(the memory test only: swap-out delta > 0, `oom_kill` delta > 0, PSI
`some avg10` > 10 %, or MemAvailable < reserve).

On a trip, the engine cancels the test's context and gives `Run` 2 s to return.
Then it closes everything registered with `Track`. If anything is still
running, that's reported as an internal error in the result.

**Cleanup guarantees:**
- Workers are goroutines (no child processes, except iperf3, which runs with
  `Setpgid` and is killed as a group).
- Memory comes from anonymous `unix.Mmap` and is `Munmap`ed on exit.
- Temp files are created with `O_TMPFILE`. If the filesystem doesn't support
  that, the file is created and unlinked immediately while the fd stays open.
  Either way nothing is left behind even after `kill -9`.
- Sockets are tracked.
- SIGINT/SIGTERM cancel the root context.

### 5.4 Per-component notes

**CPU.** One goroutine per selected CPU, `LockOSThread` plus
`SchedSetaffinity`. Kernels rotate every ~10 s: int (xorshift/mul chains), FP
with explicit `math.FMA`, a vector-friendly loop, SHA-256, CRC32C, and a
64×64 matmul. Each kernel is run with fixed seeds and checked against the
embedded golden value **every iteration**, never against a value computed on
the same core. A mismatch causes FAIL for that core, with the core ID,
kernel, and seed.

**Memory.**
- Size = min(pct × MemAvailable, MemAvailable − max(1 GiB, 5 % of RAM)).
- Pages are touched one by one from threads pinned to the target NUMA node's
  CPUs. Linux first-touch policy then places them locally, with no `mbind`
  and no CGO; placement is verified via `/proc/self/numa_maps`.
- Patterns: walking 1/0, random + verify (seeded PRNG regenerated, not
  stored), moving inversions, and address-in-address.
- Mismatches are reported as a virtual range plus the physical address (via
  `/proc/self/pagemap`, root only). A DIMM is named only if EDAC logged an
  event for that DIMM during the run (correlation, clearly labelled). Exact
  phys→DIMM decoding isn't exposed to userspace.

**Storage.**
- Read path: `O_RDONLY|O_DIRECT` with page-aligned mmap buffers. Sequential
  full surface, then random 4k reads at QD 1/8/32 (goroutines per queue slot).
  Results: p50/p99/max latency via an HDR-style log-bucket histogram, and slow
  sectors over a configurable threshold.
- File write test: `O_TMPFILE` plus a CRC32C per block, then read back with
  `O_DIRECT` and compared.
- Raw write requires all of: `--destructive`, the typed device name, the device
  not appearing in mountinfo/swaps, empty `/sys/block/X/holders`, the device
  not an md/LVM member, and `open(O_EXCL)` succeeding (the kernel refuses this
  for in-use devices).

**Network.**
- `netproto` uses a TCP control channel (JSON hello: version, token, test
  params) plus N data streams, TCP and UDP.
- Payload blocks carry a sequence number and CRC32C, so end-to-end corruption
  is detected, not just throughput.
- Retransmits come from `unix.GetsockoptTCPInfo`. UDP results give
  loss/reorder/jitter.
- Bond tests pin streams per member by source IP or interface binding
  (`SO_BINDTODEVICE`).
- The optional failover check sets one member down only with explicit opt-in.
- iperf3 is available as an alternative engine (`-J` output parsed).

**Power.** A composite test runs CPU + memory + storage file-write
concurrently under one guard. It monitors watts, rail sag (min vs baseline),
PSU temps/fans, and redundancy. The guided failover is a step list ("unplug
PSU 2 now" → wait for redundancy_lost → "re-plug" → wait for
fully_redundant); each transition is recorded with a timestamp.

**Burn-in.** A composite of the selected tests with a long duration, periodic
JSONL metrics, and a snapshot every N minutes.

### 5.5 Testability

- Every workload kernel has a hook (`corrupt func(iter int, v *uint64)`, nil in
  production) so tests can inject a wrong result and assert it's detected.
- Every guard rule has table tests with synthetic snapshots.
- `stress/sim` provides fake tests for `--fixtures` mode that emit realistic
  metrics, optionally inject an error, and exercise the full TUI flow.

---

## 6. Fixtures

```
testdata/fixtures/<scenario>/
├── manifest.json     {"description", "hostname", "privileged", "virtualization",
│                      "lookpath": ["ipmiutil","smartctl",...],
│                      "commands": {"ipmiutil sensor -c": "cmd/ipmiutil-sensor.txt", ...},
│                      "live": {"jitter": {...}}}   // simulator for moving values
├── root/             proc/, sys/ (symlinks preserved), dev/ipmi0 marker, ...
└── cmd/              recorded command outputs (+ optional .exit / .stderr)
```

Planned scenarios:
- `server-healthy`
- `server-degraded` (offline core, ECC errors, degraded md, degraded bond, failed PSU, low optical power)
- `vm-kvm`
- `nonroot`
- `arm64-server`

Parser unit tests use the files in `cmd/` and `root/` directly.
`scripts/capture-fixtures.sh` collects a set from a real machine (read-only
commands only) and lists the serial/MAC fields to redact.

---

## 7. Exit codes and summary line

`report`: worst Status → 0 OK, 1 WARN, 2 CRIT, 3 UNKNOWN.
`stress`: PASS 0, WARN 1, FAIL 2, aborted by user or could not run 3.

The last line of text output is `<OK|WARNING|CRITICAL|UNKNOWN> - <findings>`
(or `PASS|WARN|FAIL - ...`). It's built from the CRIT findings first, then
WARN, and is truncated to about 200 characters with "(+N more)".

---

## 8. Decisions (confirmed 2026-11-16)

1. Build floor is **Go 1.26** with Charm v2 (`charm.land/*`).
2. **Local Linux interfaces first, IPMI only where Linux has no answer.**
   Per data point:

   | Data | Primary (local) | IPMI only as fallback / sole source |
   |---|---|---|
   | CPU temps | hwmon coretemp/k10temp, thermal zones | IPMI temp sensors if no hwmon |
   | DIMM temps | hwmon jc42 / spd5118 | IPMI DIMM sensors |
   | Disk/NIC temps | SMART, hwmon (nvme, NIC drivers) | n/a |
   | Fans, voltage rails | hwmon (fan*_input, in*_input) | IPMI sensors (BMC-managed fans are usually invisible to Linux) |
   | Power draw | none for wall power (RAPL = CPU/DRAM only, shown as such) | DCMI / IPMI power sensors |
   | PSU inventory | dmidecode type 39 | `fru` if type 39 is empty |
   | PSU status, redundancy | none | IPMI (sole source) |
   | SEL, SEL fill | none | IPMI (sole source) |
   | BMC firmware / LAN | none | IPMI (sole source) |

   Fixtures: real captures from the dev machine where possible. IPMI, RAID
   CLI and `ethtool -m` fixtures are authored from documented output formats.
3. `tyr net-peer` requires a token (printed at startup or `--token`), serves one
   client at a time, exits after 10 min idle.
4. Missing optional tools and not-applicable sources do not affect the exit code.

---

## 9. GPU and NPU support (proposal, 2026-11-16)

### 9.1 Scope

A new health module `accel` (tab **GPU/NPU**, between Network and Events)
with one collector, one JSON section and one overview card, e.g.
"4/4 GPUs OK, max 71°C, 0 ECC, 1 NPU OK".

Device discovery is vendor-neutral and local-first:

| Device | Discovery | Driver examples |
|---|---|---|
| GPU | `/sys/class/drm/card*` + PCI class `0x03xxxx` | amdgpu, i915, xe, nvidia(-drm), nouveau |
| NPU / accelerator | `/sys/class/accel/accel*` + PCI class `0x12xxxx` / `0x1180` | amdxdna (Ryzen AI), intel_vpu (Core Ultra), habanalabs (Gaudi), qaic (Cloud AI 100) |
| Board VGA | PCI vendor `1a03` ASPEED, `102b` Matrox, ast/mgag200 | listed as "BMC display", hidden by default, never a GPU |

### 9.2 Data sources (local first, vendor tool only where sysfs is empty)

| Data | Local (sysfs/hwmon/kernel log) | Vendor tool (fallback / sole source) |
|---|---|---|
| CPU per core: limits, SMT siblings, P/E or big/little type, AMD preferred-core rank, microcode, C-state residency | `cpuN/cpufreq/cpuinfo_{min,max}_freq`, `topology/core_cpus_list`, `/sys/devices/cpu_{core,atom}/cpus`, `cpu_capacity`, `amd_pstate_prefcore_ranking`, `/proc/cpuinfo`, `cpuidle/state*` | – (P/E without a PMU list: exactly two max frequencies more than 15% apart, e.g. Zen 5/5c) |
| CPU: caches, flags, family/model/stepping, vulnerabilities, SMT, isolation, boost, EPP | `cache/index*`, `/proc/cpuinfo`, `vulnerabilities/*`, `smt/*`, `isolated`, `nohz_full`, `cpufreq/boost` | – (vulnerabilities are shown, not raised as hardware findings; mixed microcode is WARN) |
| Inventory: model, PCI address, NUMA node, driver, VBIOS/firmware | PCI sysfs, `vbios_version`, `fw_version`, `vbnv` | `nvidia-smi -q -x` (proprietary NVIDIA exposes almost nothing in sysfs) |
| PCIe link: current vs max speed/width (degraded link = WARN) | `current_link_*`, `max_link_*` | – |
| Utilization, VRAM used/total | amdgpu `gpu_busy_percent`, `mem_info_vram_*`; xe/i915 engine busyness via fdinfo | nvidia-smi, amd-smi, xpu-smi |
| Temperatures (edge/junction/mem), power, fan, clocks | hwmon of the GPU (amdgpu, i915/xe power+energy) | nvidia-smi |
| ECC / RAS: correctable/uncorrectable, retired pages / row remapping | amdgpu `ras/*_err_count`, `ras/gpu_vram_bad_pages` | nvidia-smi (volatile/aggregate SBE/DBE, remapped rows), amd-smi |
| Throttle reasons | amdgpu `gpu_metrics` (binary, versioned) | nvidia-smi `clocks_event_reasons` |
| GPU/NPU errors in kernel log | NVRM Xid, amdgpu ring timeout / GPU reset, i915/xe GPU HANG, ivpu/amdxdna errors, AER on the device | – |
| NPU: firmware, power state, busy time | `/sys/class/accel`, ivpu `npu_busy_time_us`, PCI sysfs | `xrt-smi examine` (AMD), `hl-smi` (Gaudi), `qaic-util` |
| NPU: awake share | runtime PM `power/runtime_active_time` delta per refresh | – |
| NPU: temperature (no own sensor in amdxdna/ivpu) | CPU die sensor (k10temp Tdie/Tctl, coretemp package), marked `shared`; findings stay in the CPU module | – |
| NPU: power, power mode, AIE columns, running contexts (AMD) | – | `xrt-smi --batch examine -r platform -r aie-partitions` (text; JSON needs an output file). Run on full collect, and on refresh only while the NPU is runtime-active, because opening the device wakes it |

Severity rules (source thresholds first, as everywhere): uncorrectable ECC,
Xid 48/63/64/74/79/94/95 (DBE, row-remap failure, NVLink, fell off the bus,
contained/uncontained ECC), GPU fell off the bus, pending row remap = CRIT;
correctable ECC increase, link below max, thermal/power throttling active,
retired pages near the limit, GPU reset in log = WARN.

### 9.3 Stress

Tyr can't run GPU/NPU compute natively: with CGO_ENABLED=0 there's no
CUDA/ROCm/Level Zero/OpenCL. Proposal: Tyr runs **verified external
engines** under its own baseline → live monitoring → guard → diff → verdict,
exactly like the other components:

| Hardware | Engine (only if installed) | Verifies results |
|---|---|---|
| NVIDIA | `dcgmi diag -r 2/3` (preferred), `gpu_burn` | yes |
| AMD Instinct/Radeon | `rvs` (ROCm Validation Suite: gst, mem, pebb), `amd-smi` | yes |
| Intel GPU | `xpu-smi diag` | yes |
| AMD NPU | `xrt-smi validate` | yes |
| Gaudi | `hl_qual` | yes |

GPU guard additions: GPU temp ≥ its slowdown/shutdown threshold, new Xid or
UE ECC, GPU fell off the bus, power above cap. If no engine is installed,
the GPU/NPU stress option is shown as unavailable with the reason
("install dcgm or gpu-burn"). Full burn-in includes GPUs when an engine is present.

### 9.4 Model sketch

```go
type Accel struct {
    Header
    GPUs []Accelerator `json:"gpus"`
    NPUs []Accelerator `json:"npus"`
    BMCDisplays []PCIDevice `json:"bmc_displays,omitempty"`
}
type Accelerator struct {
    Kind, Vendor, Model, PCIAddress, Driver, DriverVersion, Firmware string
    NUMANode int
    LinkGen/LinkWidth vs MaxLinkGen/MaxLinkWidth
    UtilizationPercent, VRAMTotalBytes, VRAMUsedBytes, PowerWatts, PowerCapWatts *…
    Temps, Fans []Sensor
    ECCCorrectable, ECCUncorrectable, RetiredPages *uint64
    ThrottleReasons []string
    Status Status
}
```

Counters for diffs: `accel.<pci>.ecc.ce|ue`, `accel.<pci>.xid`,
`accel.<pci>.retired_pages`, `accel.<pci>.resets`.

### 9.5 Decisions (confirmed 2026-11-16)

1. One module `accel`, tab **GPU/NPU** (tabs 1-9: Overview, CPU, Memory,
   Storage, Power, Network, GPU/NPU, Events, Stress).
2. Stress via external verified engines (dcgmi diag / gpu_burn, rvs,
   xpu-smi diag, xrt-smi validate) under Tyr's guard/monitoring/verdict.
3. Hardware priority: NVIDIA datacenter, AMD GPU/Instinct, Intel GPU, client
   NPUs (amdxdna, intel_vpu). All four get parsers and fixtures.
4. Order: Memory, Storage, Power, Network, **GPU/NPU**, Events, then report.
   CLI: `--section accel`, `--component gpu|npu` for stress, `--gpu <pci|index>` scope.

---

## 10. Known gaps / verification wanted

- **ipmiutil output formats** (`sensor -c`, `sel`) are parsed from the
  documented layout; the fixtures for them are authored, not captured.
  `ipmitool sensor` / `sel elist` / `sel info` are well-established formats.
  A real capture (`scripts/capture-fixtures.sh --redact`) from one Supermicro,
  Dell and HPE host would replace the synthetic BMC fixtures.
- `ipmiutil sensor -c` carries the BMC status but no numeric thresholds; Tyr
  uses the BMC status plus its configurable defaults for those sensors.
- BMC sensor reads take 1-30 s on some BMCs. They are read once per round and
  shared by all collectors; a slower refresh tier for IPMI (stress design §5.2)
  will also be used by the TUI.
- SEL events persist until the SEL is cleared, so old memory/PSU events keep
  the module at WARN/CRIT; messages carry the date of the latest event.
- Workload golden values are verified on amd64 only (arm64 checked via
  assembly inspection; needs a run on real arm64 hardware or in CI).
- Storage: NVMe health is read natively (NVMe Get Log Page ioctl, root);
  smartctl covers SATA/SAS and is the NVMe fallback, nvme-cli the last
  resort. HPE `ssacli` text output is not parsed yet (storcli/perccli are).
  Disks behind a MegaRAID controller need `smartctl -d megaraid,N`, not yet
  enumerated; their health comes from storcli's PD states instead.
- Network: BMC LAN settings are parsed from `ipmitool lan print` only;
  ipmiutil's `lan` layout is not parsed. Speed-below-max uses the advertised
  link modes, which can over-report when the transceiver is slower than the NIC.
- GPU/NPU: amdgpu, i915/xe and NPUs are read from sysfs/hwmon (no amd-smi or
  xpu-smi needed); NVIDIA requires `nvidia-smi` because the proprietary driver
  exposes no health data in sysfs. Habana/Qualcomm NPUs get inventory, PCIe
  and kernel-log errors only.
- Fixture captures: `/proc/self` and `/proc/net` are copied explicitly
  (they are per-process links); disk serials are redacted everywhere
  including `/dev/disk/by-id` link names.

## 11. Stress tests as built (2026-11-16)

### 11.1 Decisions (confirmed 2026-11-16, change §5.3)

1. **Engines per run.** Tyr's own self-checking workloads are always available
   (static binary, nothing to install). stress-ng, mprime (Prime95), memtester and
   iperf3 appear as extra engines when installed. Every engine runs under the same
   baseline → monitor/guard → after → diff → verdict lifecycle. External engines run
   in their own process group in a private temp directory and are killed as a group
   (SIGTERM, then SIGKILL after 3 s); nothing survives a stop.
2. **memtest86+** only runs before the OS. Tyr detects it in `/boot` and explains
   how to boot it; it never changes the boot configuration.

### 11.2 Engines

| Component | builtin | external |
|---|---|---|
| CPU | pinned worker per CPU, 7 kernels with golden values, per-core attribution | `stress-ng --cpu all methods --verify`, `mprime -t` (FATAL ERROR / hardware failure lines) |
| Memory | anonymous mmap, size = pct × MemAvailable leaving max(1 GiB, 5 %); random, walking ones, moving inversions, address-in-address; physical address via pagemap (root) | `stress-ng --vm all --verify`, `memtester` (FAILURE lines) |
| Storage | `read`: O_DIRECT full-surface scan (wraps), latency p99/max, slow reads, read errors with offset — never writes. `file`: O_TMPFILE, CRC32C blocks, fsync, read back via a separate O_DIRECT fd | `stress-ng --hdd --verify` in a temp dir (file mode only) |
| Network | `tyr net-peer` (token, one session, 10 min idle exit); N TCP streams, frames with sequence + CRC32C, TCP_INFO retransmits, ratio to link speed | `iperf3` (rate lines), `stress-ng --sock/--udp` (labelled: kernel stack only, no NIC) |

`--component full` runs CPU → memory → storage (read scan of all disks) → network
(only with a peer), a fresh baseline for each, one confirmation.

### 11.3 Guard as built

Default: any temperature at CRIT (plan refuses to start if one is CRIT already),
new MCE, new uncorrectable ECC / poisoned memory. Memory test: OOM kill, swap-out
above the pre-test background rate (> 16 MiB), MemAvailable below half the
reserve, PSI some avg10 > 25 %. Storage: new SMART errors on in-scope disks.

### 11.4 Not done yet

- GPU/NPU stress engines (§9.3), burn-in composite, power test, raw-device writes
  (`--destructive`) — not implemented.
- stress-ng, mprime, memtester and iperf3 output handling is tested with fake
  scripts only; none was installed on the development machine. mprime's command
  line and torture settings (`prime.txt`/`local.txt`) follow its documentation.
- Memory pages are not placed per NUMA node yet; burn-in JSONL metrics not written.
