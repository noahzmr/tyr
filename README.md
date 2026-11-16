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

- **Hardware report:** status, alerts by severity, component health (every kind of component — CPU cores, packages, DIMMs, disks, RAID, filesystems, ports, transceivers, GPUs/NPUs, PSUs, fans — with OK/WARN/CRIT counts and which ones have problems), each logical CPU (state, clock, load, temperature, MCEs, throttling, core test), system, CPU, memory (DIMMs with serials and ECC counts), storage (disks with SMART, RAID, filesystems), network, GPU/NPU, the full inventory, and the data sources that could not be used and why.
- **Stress result:** verdict and reasons, scope, charts of the metrics over time (Mermaid `xychart-beta` line charts: load, CPU temperature against its limit, verified throughput, clock …; the result JSON keeps up to 120 points per metric under `series`), metrics (min/avg/max), wrong results, counters that changed, new alerts, component health after the test (and each core after a CPU test), the log.
- **History** of each host across any number of snapshots and stress results (a stress result adds its before/after snapshots): status over time, when each alert appeared and was resolved, counters that grew (with resets), sensor min/max/last against their limits, parts added, removed or replaced (serial changed) and firmware/BIOS/kernel/microcode updates, and every stress run (also as a Mermaid gantt timeline).

Mermaid renders in GitLab, GitHub, Obsidian or mermaid.live. Terminal views
(`tyr view`, the TUI, glow) cannot draw it, so there the charts are
sparklines; `--charts text` gives the sparkline form in `tyr report --from`
and `tyr history` output too, e.g. for `| glow -`.

Tyr does not keep history on its own: collect the JSON you want (`tyr report
--format json`, the TUI's `e` export, the stress result files) and pass files or
directories; other JSON files are listed as skipped.

In the TUI, `R` opens the Reports dialog: choose the host report, a stress
result (this session's or an earlier run) or the history of files and
directories, Markdown or JSON, and view it (scroll, `/` search), save it, copy
it to the clipboard, or a combination. On any report page (and in `tyr view`)
`c` copies the report: Markdown with the Mermaid charts, or JSON. Locally Tyr
puts it on the desktop clipboard with `wl-copy`, `xclip` or `xsel` (install
one of them; GNOME Terminal and Ptyxis cannot copy any other way) and also sends
OSC 52. Over SSH only OSC 52 reaches your clipboard: it works in kitty,
WezTerm, foot, iTerm2, Windows Terminal and tmux (`set -g set-clipboard on`),
not in GNOME Terminal or Ptyxis, and some terminals cut very large copies;
save the report there instead. `e` writes the current snapshot as JSON and Markdown.

## Contributing

Commit messages, pull requests, comments and files describe the work and
nothing else: no trailers or notes that credit the tool used to write them.
Turn the hooks on once per clone; CI checks the same (`.github/workflows/ci.yml`):

```bash
git config core.hooksPath scripts/hooks   # commit-msg + pre-commit (also gofmt)
sh scripts/check-attribution.sh tree      # the checker the hooks and CI share
```

## Fixtures

`make demo FIXTURE=server-degraded` runs the TUI against recorded data in
`testdata/fixtures`; `scripts/capture-fixtures.sh` records a new host.

See `docs/DESIGN.md` for the design.
