# Architecture

![Tyr architecture](images/architecture.svg)

Tyr is one Go binary without cgo. Everything it knows about a host goes into a
single `model.Snapshot`; the TUI, the CLI, the stress engine and the reports all
read that snapshot or its JSON.

## Layers

| Layer | Packages | Job |
|---|---|---|
| Host access | `hostfs`, `execx` | read-only `/proc`, `/sys`, `/dev`; tools without a shell, with deadlines |
| Sources | `source/sysfs`, `dmi`, `smart`, `ipmi`, `redfish`, `raid`, `pci`, `gpu`, `kernellog`, `netinfo`, `crypt` | read and parse one kind of data; SMBIOS (`/sys/firmware/dmi/tables`), NICs (`SIOCETHTOOL`) and the BMC (`/dev/ipmi0`) natively, their tools only as fallback |
| Collectors | `collector/system`, `cpu`, `memory`, `storage`, `power`, `network`, `accel`, `events` | combine sources into a section with findings |
| Checks | `check` | thresholds → `OK` / `WARN` / `CRIT` / `UNKNOWN` |
| Workloads | `workload` | the verified compute kernels of the core and CPU tests; the torture test's Lucas-Lehmer FFT (AVX-512 / AVX2 FMA or Go) |
| Explanations | `explain` | the alert catalog: meaning, usual causes, commands per finding code |
| Filesystems | `fsck`, `fsbrowse` | check and repair plans with their blockers, streamed runs and logs; the file browser's directory reader and background counter. The only code that changes the host: `umount`, the check tool, `mount`, after a confirmation |
| Model | `model` | `Snapshot`, sections, sensors, findings, inventory; JSON schema 1.0 |
| Acceptance | `accept`, `acceptui` | profiles and limits; one verdict (PASS, REMARKS, FAIL, INCOMPLETE) over a health check, the stress tests and a second check, run with one set of collectors; its live view on a terminal |
| ISO | `isobuild` | the ISO with the current settings from the TUI, USB disks, writing and verifying a stick |
| Settings | `settings` | where configuration files live: `/etc/tyr` for root, `~/.config/tyr` for a user |
| Notifications | `notify` | webhook events (started, failed or warning step, verdict) in five formats, delivered in the background with retries |
| NetBox | `netbox` | facts from a snapshot (and a verdict), tag rules over them, the sync to NetBox's REST API |
| Consumers | `tui`, `cmd/tyr`, `stress`, `accept`, `netbox`, `report` | show, export, load, judge, report, write to NetBox |

```mermaid
flowchart LR
    subgraph host["Host"]
        sys["sysfs · hwmon · procfs · /dev/kmsg · /var/log/pods · /run/udev/data · LUKS headers · TCG discovery"]
        tools["smartctl · storcli · nvidia-smi · lldpctl · journalctl · ip · (fallbacks: dmidecode, ethtool, ipmitool)"]
        bmc["BMC: IPMI and Redfish (read-only)"]
    end
    subgraph tyr["tyr"]
        src["source/*"] --> col["collector/* (parallel)"]
        col --> chk["check"] --> snap["model.Snapshot"]
        snap --> tui["TUI"]
        snap --> cli["CLI: report · history · view"]
        snap --> stress["stress engine"]
        stress --> res[("result JSON")]
        res --> rep["report"]
        snap --> acc["accept: limits → verdict"]
        res --> acc
        acc --> rep
        cli --> rep
    end
    sys --> src
    tools --> src
    bmc -. fallback .-> src
```

The diagram above shows the main paths; the acceptance test (`tyr accept`,
[acceptance.md](acceptance.md)) sits on top of the snapshot and the stress
engine as in the flowchart. The Tyr ISO (`iso/`) is a Debian live system
that runs it at boot. `netbox` ([netbox.md](netbox.md)) takes a snapshot
and, after a test, its result, and writes them to NetBox: the only place
where Tyr writes to another system (the BMC is only read).

## Collection

`collector.Collect` starts one goroutine per module, each with a timeout
(`--timeout`, 10 s). A module returns its section: readings, the findings it
raised, and the state of every source it tried (`ok`, `unavailable` with a
reason, `not_applicable`, `skipped`). The previous snapshot is passed in, so
counters can be turned into rates and slow data (caches, flags, firmware) can
be carried over.

Some data is expensive and changes slowly, so it is read once or cached:

| Data | Read | Why |
|---|---|---|
| SMART | every `--smart-interval` (5 min), `r` and stress snapshots fresh | every rejected query counts as a SCSI error on the disk |
| Machine checks and program crashes of earlier boots, crash records (pstore), rasdaemon, boot history | once per process | they only change with a reboot |
| Receive ring and queue size of each NIC (`ethtool -g` / `-l`) | once per interface | configuration |
| Redfish event logs / power | every 2 min / 15 s, in the background | a BMC answers slowly; a collection round never waits longer than its timeout, the data arrives with the next refresh |
| Redfish storage (drives behind RAID controllers), device and firmware inventory | every 10 min, in the background | many requests (one per drive, device and firmware), and it changes only with a part or a firmware update |
| iscsid, multipathd and kubelet journal lines (network storage) | every minute | `journalctl -g` over a day is not free; on a host without sessions, Kubernetes or Longhorn neither the journal nor the process list is read |

The Redfish client is found once per process: the BMC's host interface
(SMBIOS type 42) first, then a configured address.

## The refresh loop

```mermaid
sequenceDiagram
    participant T as TUI
    participant C as collector.Collect
    participant M as modules (parallel)
    participant S as session
    loop every --refresh (5 s), unless paused
        T->>C: collect(previous snapshot)
        C->>M: one goroutine per module
        M-->>C: sections, findings, sources
        C-->>T: snapshotMsg
        T->>S: new / resolved findings, rising counters, histories
        T->>T: every tab SetData, selection kept by component id
    end
```

While a stress test runs, the stress monitor's one-second samples feed the TUI
instead, so the other tabs stay live without a second collection.

The session keeps what a single snapshot cannot: when a finding first appeared
(`NEW`), what resolved, which counters grew and by how much, and short value
histories for the sparklines.

## The snapshot

```json
{
  "schema_version": "1.0",
  "tyr_version": "v0.9.0",
  "hostname": "srv-degraded-01",
  "timestamp": "2026-11-16T09:00:00Z",
  "privileged": true,
  "status": "CRIT",
  "summary": "1 core offline (cpu7); …",
  "system":  { "status": "OK",   "findings": [], "sources": [], "…": "…" },
  "cpu":     { "status": "CRIT", "logical": [], "package_temps": [], "…": "…" },
  "memory":  { "…": "…" },
  "storage": { "…": "…" },
  "power":   { "supplies": [], "consumed_watts": 412, "redundancy": [], "…": "…" },
  "network": { "…": "…" },
  "accel":   { "…": "…" },
  "events":  { "logs": [], "entries": [], "…": "…" },
  "inventory": [ { "category": "dimm", "location": "P2-DIMMB1", "serial": "…", "status": "CRIT" } ]
}
```

`tyr report --format json` writes it; the TUI's `e` exports it; every stress
result embeds the snapshot before and after the load. The report package reads
any of them back (see [reports.md](reports.md)).

## Design rules

- **Read-only.** Collectors never write; IPMI runs an allowlist of read-only
  commands and Redfish only GET requests; the boot configuration is never
  touched. The only writes are the storage file test's temporary file, result
  and export files, the Redfish certificate pin appended to its config file,
  and - each after a confirmation - package installs started from the Stress
  tab and the offline-core test, which brings one CPU online in an isolated
  cpuset partition and puts everything back (a state file in `/run` makes
  that recoverable after a kill), and filesystem checks and repairs (`K`,
  `tyr fsck`): unmount, the check tool, mount again; a repair only after the
  device path is typed, never from the live system, never interruptible.
  Reading drives' TCG discovery and LUKS headers changes nothing; key
  material is never read. The file browser only reads.
- **Local first.** Linux interfaces before tools, tools before the BMC. Where
  Tyr reads a kernel interface itself (SMBIOS, ethtool requests, the IPMI
  driver), the tool is only a fallback; native IPMI sends only the read-only
  requests of one allowlist (`source/ipmi/dev.go`).
- **Nothing silent.** Unusable sources are listed with their reason.
- **Limits of the part first.** A sensor's own threshold wins; defaults fill gaps.
- **Status without colour.** Glyph and word for every status.
