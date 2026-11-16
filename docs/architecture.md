# Architecture

![Tyr architecture](images/architecture.svg)

Tyr is one Go binary without cgo. Everything it knows about a host goes into a
single `model.Snapshot`; the TUI, the CLI, the stress engine and the reports all
read that snapshot or its JSON.

## Layers

| Layer | Packages | Job |
|---|---|---|
| Host access | `hostfs`, `execx` | read-only `/proc`, `/sys`, `/dev`; tools without a shell, with deadlines |
| Sources | `source/sysfs`, `dmi`, `smart`, `ipmi`, `raid`, `pci`, `gpu`, `kernellog`, `netinfo` | read and parse one kind of data |
| Collectors | `collector/system`, `cpu`, `memory`, `storage`, `network`, `accel` | combine sources into a section with findings |
| Checks | `check` | thresholds → `OK` / `WARN` / `CRIT` / `UNKNOWN` |
| Model | `model` | `Snapshot`, sections, sensors, findings, inventory; JSON schema 1.0 |
| Consumers | `tui`, `cmd/tyr`, `stress`, `report` | show, export, load, report |

```mermaid
flowchart LR
    subgraph host["Host"]
        sys["sysfs · hwmon · procfs · /dev/kmsg"]
        tools["dmidecode · smartctl · nvme · storcli · nvidia-smi · ethtool · lldpctl"]
        bmc["BMC (IPMI, read-only)"]
    end
    subgraph tyr["tyr"]
        src["source/*"] --> col["collector/* (parallel)"]
        col --> chk["check"] --> snap["model.Snapshot"]
        snap --> tui["TUI"]
        snap --> cli["CLI: report · history · view"]
        snap --> stress["stress engine"]
        stress --> res[("result JSON")]
        res --> rep["report"]
        cli --> rep
    end
    sys --> src
    tools --> src
    bmc -. fallback .-> src
```

## Collection

`collector.Collect` starts one goroutine per module, each with a timeout
(`--timeout`, 10 s). A module returns its section: readings, the findings it
raised, and the state of every source it tried (`ok`, `unavailable` with a
reason, `not_applicable`, `skipped`). The previous snapshot is passed in, so
counters can be turned into rates and slow data (caches, flags, firmware) can
be carried over.

Power and Events have no collector yet; the TUI shows them as ○ unavailable
with that reason, and their data (PSU inventory, fans) comes from the System
module.

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
  "network": { "…": "…" },
  "accel":   { "…": "…" },
  "inventory": [ { "category": "dimm", "location": "P2-DIMMB1", "serial": "…", "status": "CRIT" } ]
}
```

`tyr report --format json` writes it; the TUI's `e` exports it; every stress
result embeds the snapshot before and after the load. The report package reads
any of them back (see [reports.md](reports.md)).

## Design rules

- **Read-only.** Collectors never write; IPMI runs an allowlist of read-only
  commands; the boot configuration is never touched. The only writes are the
  storage file test's temporary file, result and export files, and - after a
  confirmation - package installs started from the Stress tab.
- **Local first.** Linux interfaces before tools, tools before the BMC.
- **Nothing silent.** Unusable sources are listed with their reason.
- **Limits of the part first.** A sensor's own threshold wins; defaults fill gaps.
- **Status without colour.** Glyph and word for every status.
