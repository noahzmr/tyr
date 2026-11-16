# Tyr

Hardware inventory, health check and stress test for Linux servers and
workstations, in one static binary. Tyr reads sysfs, procfs, hwmon, SMART,
dmidecode and (where Linux has no other source) the BMC. It shows the result
as a TUI, a text or JSON report, and it can load CPU, memory, storage and
network while it watches temperatures, machine checks and ECC errors.

Tyr needs no installation, no runtime and no files besides the binary: copy
it to a host and run it.

## Build

Go 1.26 or newer; no cgo.

```bash
make cross     # dist/tyr-linux-amd64, dist/tyr-linux-arm64, dist/SHA256SUMS
make build     # dist/tyr for this machine
```

The binaries are statically linked (`CGO_ENABLED=0`, `-trimpath`, stripped)
and carry the version (`tyr version`). They run on any Linux of the same
architecture, independent of the distribution and its libc.

## Run on a remote host

```bash
# copy (pick the architecture: `ssh host uname -m` → x86_64 = amd64, aarch64 = arm64)
scp dist/tyr-linux-amd64 dist/SHA256SUMS admin@host:/tmp/
ssh admin@host 'cd /tmp && sha256sum --ignore-missing -c SHA256SUMS && mv tyr-linux-amd64 tyr && chmod +x tyr'

# interactive TUI on the remote host (-t gives it a terminal; root sees serials, SMART, SEL)
ssh -t admin@host sudo /tmp/tyr

# report without a terminal: text, or the full JSON snapshot
ssh admin@host sudo /tmp/tyr report
ssh admin@host sudo /tmp/tyr report --format json > host.json
```

Without a terminal on stdout, `tyr` prints the text report instead of
starting the TUI, so `ssh host tyr` and cron jobs work unchanged.

Run it as root where you can: dmidecode (serials, DIMM slots), SMART, the
kernel log, PCI VPD and the BMC need it. Without root Tyr still runs and
lists what it skipped and why.

### Results

| What | Where on the remote host | Fetch |
|------|--------------------------|-------|
| Text or JSON report | stdout | redirect, as above |
| TUI snapshot (`e`) | `tyr-snapshot-<host>-<time>.json` in the working directory | `scp host:/tmp/tyr-snapshot-*.json .` |
| Stress results | `/var/lib/tyr/runs/*.json`, else `./tyr-runs/` (or `--run-dir`) | `scp 'host:/var/lib/tyr/runs/*.json' .` |

Exit codes follow the Nagios convention: 0 OK, 1 WARN, 2 CRIT, 3 UNKNOWN or
error. `tyr stress` uses 0 PASS, 1 WARN, 2 FAIL, 3 aborted or not run.

### Stress test without the TUI

```bash
ssh -t admin@host sudo /tmp/tyr stress --component full --duration 10m
ssh admin@host sudo /tmp/tyr stress --component cpu --duration 30m --yes   # no prompt
```

The test asks for confirmation unless `--yes` is given. It stops on its own
at the end, on a critical temperature (a CPU only 5 °C past its own limit),
a new machine check or an uncorrectable ECC error. The network test needs a
second host running `tyr net-peer`. External engines (stress-ng, memtester,
iperf3, mprime) are used when installed; the TUI's Stress tab installs the
packaged ones with `i`.

## Reports and history

Every report can be written as Markdown and read with Tyr itself or with
[glow](https://github.com/charmbracelet/glow), in GitLab or any editor:

```bash
tyr report --format markdown > host.md              # hardware + alerts of this host
tyr report --format json > host.json                # the same as JSON (keep these for history)
tyr report --from host.json --format markdown       # a saved snapshot or stress result as a report
tyr history ./results/                              # history from saved JSON (Markdown)
tyr history --format json --host db-01 ./results/   # the same as JSON
tyr view host.json                                  # view a report in the terminal (scroll, / search)
tyr view ./results/                                 # several files: their history
```

- **Hardware report:** status, alerts by severity, system, CPU, memory (DIMMs with serials and ECC counts), storage (disks with SMART, RAID, filesystems), network, GPU/NPU, the full inventory, and the data sources that could not be used and why.
- **Stress result:** verdict and reasons, scope, metrics (min/avg/max), wrong results, counters that changed, new alerts, the log.
- **History** of each host across any number of snapshots and stress results (a stress result adds its before/after snapshots): status over time, when each alert appeared and was resolved, counters that grew (with resets), sensor min/max/last against their limits, parts added, removed or replaced (serial changed) and firmware/BIOS/kernel/microcode updates, and every stress run.

Tyr does not keep history on its own: collect the JSON you want (`tyr report
--format json`, the TUI's `e` export, the stress result files) and pass files or
directories; other JSON files are listed as skipped. In the TUI, `R` shows the
current host's report (on a stress result: that result), and `e` writes both
JSON and Markdown.

## Fixtures

`make demo FIXTURE=server-degraded` runs the TUI against recorded data in
`testdata/fixtures`; `scripts/capture-fixtures.sh` records a new host.

See `docs/DESIGN.md` for the design.
