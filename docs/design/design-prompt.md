# Prompt for the design tool: Tyr TUI redesign

Paste everything below the line into the design tool. Attach the screenshots in
`docs/design/current/` (they show the TUI as it is today, rendered from the
`server-degraded` fixture at 120×40). The braille graphs in those images look
misaligned only because the screenshot tool's browser font draws braille wider
than one cell; in a real terminal they line up.

---

## Who you are designing for and what Tyr is

You are designing the terminal user interface (TUI) of **Tyr**, a hardware
inventory, health-check and stress-test tool for Linux servers and
workstations (x86_64 and arm64). It runs as a single static binary over SSH or
on a local console, often on a datacenter server that is misbehaving.

**Users:** sysadmins, datacenter technicians and hardware engineers. They open
Tyr to answer, in this order:

1. **Is this machine healthy?** (seconds, at a glance)
2. **If not, which component is broken, and where is it physically?** (CPU core,
   DIMM slot, disk bay, NIC port, PSU)
3. **What are its identifiers** (serial number, part number, firmware) so I can
   order a replacement or open a vendor case?
4. **Does it hold up under load?** (stress test with live graphs and a verdict)

Speed of reading the health status matters more than decoration. The UI must
work on an 80×24 SSH session and use the space well on a 200×60 screen.

## The toolkit you must design for (hard constraints)

Everything you design must be buildable with exactly this stack. Do not
propose features these libraries cannot draw.

| Library | Version | What we use it for |
|---|---|---|
| **Bubble Tea v2** (`charm.land/bubbletea/v2`) | v2.1.1 | App framework (Elm architecture: model → update → view). Full-screen alt-screen. Key events, optional mouse (click, wheel; off by default because it blocks terminal text selection), focus/blur events, window title, terminal background detection (light/dark). |
| **Lip Gloss v2** (`charm.land/lipgloss/v2`) | v2.1.1 | All styling: foreground/background colors, bold/faint/underline/reverse, padding, margins, borders (normal, rounded, thick, double, hidden; per-side; colored), fixed width/height, alignment, horizontal/vertical joining of blocks. `lipgloss/table` (static tables with per-cell styles), `lipgloss/tree` (trees), `lipgloss/list`. Color helpers `Blend1D`/`Blend2D` (gradients), `Lighten`/`Darken`, `LightDark` (pick a color by terminal background). **Compositor/Layers**: draw boxes on top of the screen (pop-ups, overlays) with x/y/z positions. |
| **Bubbles v2** (`charm.land/bubbles/v2`) | v2.2.1 | `help` (key hints in the footer), `key` (key bindings), `spinner`, `viewport` (scrollable pages with search highlights), `progress` (gauges, can color by value), `tree`. We do **not** use `textinput` (it would run clipboard helpers); text fields are our own minimal single-line input. |
| **Tone / CharmTone** (`charm.land/tone`) | v0.2.0 | The color palette: 79 named colors (e.g. Guac, Mustard, Coral, Pom, Malibu, Charple, Pepper, Salt, Squid, Steam, Oyster, BBQ, Sash, Iron, Steep, Oceania). Use these names in your color tokens. |
| **x/ansi** (`github.com/charmbracelet/x/ansi`) | v0.11.9 | Width-aware truncation and stripping of styled text. |

Not used: **Gum** (it is a shell-script CLI, not a library for a Bubble Tea app).

What the terminal medium means for your design:

- **A grid of monospace character cells.** No images, no pixel positioning, no
  variable font sizes, no icons except Unicode characters. One font, one size.
  Bold and faint are the only weights.
- **Graphics come from characters:** box-drawing (`─ │ ╭ ╮ ┏ ┃ ├ └ ┊ ┄`), blocks
  (`▁▂▃▄▅▆▇█ ░ ■ □`), braille dots (2×4 dots per cell, used for line graphs and
  sparklines), arrows and geometric symbols (`● ▲ ✖ ? ▸ ▾ ◂ ▸ ◆ ↑`). Every
  symbol must be exactly one cell wide; avoid emoji and East-Asian-width
  characters.
- **Color is optional.** With `NO_COLOR` or a monochrome terminal, status must
  still be readable from shape and text. Today status is double-encoded: color
  plus glyph (`●` OK, `▲` WARN, `✖` CRIT, `?` UNKNOWN). Keep that principle.
- **Light and dark terminals.** Tyr detects the background and switches
  palettes. Design both. Tone has no green or yellow dark enough for text on
  white (contrast below 3.5:1); propose how to handle that (current light theme
  uses #1a7f37 green and #9a6700 yellow instead).
- **Color depth** is downsampled automatically (truecolor → 256 → 16), so
  colors must stay distinguishable after downsampling.
- **Sizes:** the minimum supported size is 80×24 (below 40×10 Tyr shows "terminal
  too small"). Design every screen at **80×24, 120×40 and 160×50** and say what
  collapses, wraps or truncates at each size.
- **Refresh:** data updates every 5 s (every 1 s during a stress test). Layout
  must not jump around when values change; selection must stay on the same
  device across refreshes.

## Status model (use it consistently everywhere)

- **OK / WARN / CRIT / UNKNOWN** for every component, rolled up: a module's
  status is the worst of its findings; the host status is the worst module.
- **Availability** is separate from health: a data source can be
  `unavailable` (e.g. "dmidecode requires root", "no BMC") or `not applicable`.
  That must look different from a failure (it is not red).
- **Findings** are one-line problems ("sdd: 2 I/O errors in kernel log",
  "1 uncorrectable ECC error on P2-DIMMB1"). Each belongs to a module and names
  a component.
- **Session markers:** `NEW` (finding appeared since Tyr started), "resolved
  this session", "rising counters" (error counters that grew, e.g. ECC, CRC,
  machine checks), "+N this session".
- **Thresholds:** sensors have warn/crit limits (from hardware or defaults).
  Show readings relative to their limits, not just raw numbers.

## Global layout and actions (today)

Top to bottom:

1. **Header bar** (1 line): app name, hostname, product, system serial, uptime;
   right side: `not root` warning, `fixture` (demo mode), `⏸ paused`, spinner while
   collecting, "updated 3s ago" (turns yellow if collection stalls), overall
   status badge (`OK`/`WARN`/`CRIT`).
2. **Tab bar** (1 line): `1 Overview  2 CPU  3 Memory  4 Storage  5 Power
   6 Network  7 GPU/NPU  8 Events  9 Stress  0 Inventory`. Each tab shows its
   module's status glyph and, when it fits, its problem count. The active tab
   is a filled pill. Tabs without data (Power, Events today) are dimmed.
3. **Content area** of the active tab.
4. **Footer** (1 line): key hints for the current context, or a short message
   ("exported …", "refreshing…").

Global actions (available everywhere unless a text field has focus):

| Key | Action |
|---|---|
| `1`–`0`, `tab`, `shift+tab` | switch tabs |
| `↑↓` / `j k`, `←→` / `h l`, `pgup pgdn`, `home end` (`g G`) | move selection |
| `enter` | open the full-screen detail page of the selected row (or the action of the row) |
| `esc` | back / close / clear |
| `!` | show only rows with problems (all tables) |
| `o` | toggle order: problems first ↔ physical device order |
| `n` | jump to the next row with a problem |
| `v` | switch the tab's view (see tabs) |
| `/` | search (Inventory list, detail pages) |
| `r` refresh now, `p` pause/resume live refresh, `e` export snapshot as JSON | |
| `s` go to the Stress tab | |
| `?` help overlay (all keys, status legend), any key closes it | |
| `q` quit (blocked while a stress test runs), `ctrl+c` stop everything and quit | |
| mouse (only with `--mouse`) | click tabs, wheel scrolls |

The window title shows status and counts, e.g. `CRIT srv-db-01 (14 crit, 22 warn) – Tyr`.

## Screens: what must be visible, and what the user can do

For every screen below: keep all listed information reachable (on the screen,
in the detail page, or one key away), and keep every listed action.

### 1 Overview
**Purpose:** health at a glance, then jump to the problem.
**Shows:** one card per module (CPU, Memory, Storage, Power, Network, GPU/NPU,
Events, System) with status badge, issue count, one-line summary
("63/64 cores online, max 96°C"), and its top finding. Cards with WARN/CRIT have
colored borders. Below: a selectable list of **all findings**, most severe first,
each labelled with its module and a `NEW` badge if new; then "Rising counters"
(counter name, value, +growth) and "Resolved this session". In short terminals
the cards collapse to one line per module.
**Actions:** arrows move between cards; `enter` on a card opens that module's tab;
`↓` past the last card row (or `n`) moves into the findings list; `enter` on a
finding opens the module tab **with that exact component selected**.

### 2 CPU (views: Grid / Cores / Details, `v`)
**Grid:** model, microcode, topology (sockets/cores/threads, online list),
frequency (base/max, governor, driver), package temperatures, socket serials or
Intel PPIN, core-test result; a grid of every logical CPU showing load % (or
temperature as a green→red heat map with `t`), offline CPUs marked; selected
CPU details; findings.
**Cores:** table, one row per logical CPU: CPU, socket, core, NUMA node, SMT
siblings, core type (P/E or big/little), AMD preferred-core rank, state, load,
MHz, max MHz, temperature, throttle count, machine-check count, core-test
result, deepest idle-state share; selected-core details with idle-state
residency.
**Details (scrollable page):** identification (vendor, model, family/model/
stepping, microcode same on all CPUs or mixed), sockets with serial/PPIN,
topology (present/online/offline lists, SMT, isolated CPUs), frequency and power
(driver, governor, energy preference, boost, idle states), temperatures with
limits, caches (L1d/L1i/L2/L3 size × instances, ways, sharing), CPU security
vulnerabilities (OK vs vulnerable), all instruction-set flags with notable ones
highlighted.
**Actions:** arrows/hjkl move in the grid; `t` load ↔ temperature; `v` view;
`enter` opens the per-core page (load/frequency/temperature with graphs, idle
states table, health counters, findings).

### 3 Memory (views: Table / Slot map, `v`)
**Shows:** usage bar (used/total/available/swap), installed vs OS-visible
memory and slots used, ECC totals (correctable/uncorrectable, ECC type), NUMA
nodes, kernel-poisoned memory. DIMM table: slot, size, type, speed
(configured/rated), part number, CE, UE, temperature, serial. **Slot map:** a
physical grid per CPU socket and memory channel showing every slot (filled or
empty) colored by status, so "the 3rd DIMM on CPU2 channel B" is obvious.
**Actions:** `a` show/hide empty slots; `v` table ↔ slot map; `enter` opens the
DIMM page (identity incl. serial/asset tag, temperature gauge, EDAC controller
counts, counter growth, findings).

### 4 Storage (views: Disks / RAID / Filesystems, `v`)
**Disks:** name, type (NVMe/SSD/HDD), model, serial, size, temperature, wear %,
SMART verdict, error counts. **RAID:** software RAID arrays (level, state,
members, sync progress) and hardware RAID controllers with virtual drives,
physical drives (slot, state, model, serial) and battery/CacheVault.
**Filesystems:** mountpoint, type, size, usage bar, inodes, read-only flag.
**Actions:** `v` switch view; `enter` opens the page for the disk (identity,
temperature gauge, SMART health, full SMART attribute table, counters, findings),
array, RAID drive or filesystem.

### 5 Power (to be designed, collector coming)
Planned data: PSUs (location, model, serial, status, input/output watts, rails),
redundancy state, total power draw, CPU package power (RAPL), fans. Design it.

### 6 Network
**Shows:** interfaces as a tree (bond/bridge → member ports, VLANs under their
parent), kind, link (speed/duplex or "no link"/"admin down"), addresses and LLDP
neighbor (switch + port), rx/tx rates and a traffic sparkline; hidden virtual
interfaces count; BMC LAN address.
**Actions:** `enter` opens the interface page: adapter, serial/part number,
driver/firmware, PCI/NUMA, MAC/MTU, link details, LLDP, traffic graphs, bond
members, transceiver (vendor/part/serial, temperature, voltage, tx/rx optical
power per lane against limits), statistics, non-zero driver error counters,
counter growth, findings.

### 7 GPU/NPU
**Shows:** GPUs and NPUs: device, model, driver, load (or idle/on for NPUs),
memory used/total, power, temperature, PCIe link (gen, width vs max); BMC display
adapters listed as "not counted".
**Actions:** `enter` opens the device page: identity (serial, part number, UUID,
PCI, NUMA, driver, firmware), PCIe and power state, sensors (temperatures,
fans, power vs cap; NPU shows the CPU die temperature marked "shared"), utilization
and memory, NPU power mode / AI engine columns / running contexts, reliability
(ECC, retired pages, row remapping, throttle reasons, driver errors).

### 8 Events (to be designed, collector coming)
Planned data: one timeline merging the BMC event log (SEL) and kernel hardware
messages (machine checks, EDAC, PCIe AER, I/O errors, thermal, link up/down, GPU
Xid), with severity, source, component and time; SEL fill level. Design it with
filtering by severity/source and jump-to-component.

### 9 Stress
Four states:
1. **Form:** test (CPU, Memory, Storage, Network, Full test), engine (builtin,
   stress-ng, mprime, memtester, iperf3 — unavailable ones shown with the reason,
   e.g. "not installed"), duration (30 s … 24 h), per-test options (CPU list;
   memory %; storage mode read-only scan vs file write+verify and target disk/
   filesystem; network peer address, token, streams, direction). Below: recent
   runs with verdict badges.
2. **Confirmation:** per component: scope, impact, warnings, notes (e.g. how to
   boot memtest86+), or why it cannot run. The user types `yes` + `enter`;
   `esc` cancels.
3. **Live view:** title (component · engine · n/m for a full test), "no errors" /
   "N wrong results" / guard trip, progress bar, elapsed/remaining, current
   stage; braille line graphs: CPU load %, hottest CPU °C with the critical line,
   the test's own throughput (verified kernels/s, verified GB/s, MB/s read,
   Gb/s), and frequency / disk write / NIC tx / memory used; per-core load strip
   (CPU test); list of wrong results; scrolling log. Other tabs keep updating
   live during the test.
4. **Result:** verdict (PASS/WARN/FAIL/ABORTED) with reasons, metrics
   (min/avg/max), wrong results, result-file path; for a full test, one line per
   component plus the overall verdict.
**Actions:** form `↑↓` field, `←→` change value, `enter` edit text field or start;
confirmation type `yes` + `enter`, `esc` back; live `esc` aborts (cleanly stops
everything); result `enter`/`esc` back to the form.

### 0 Inventory (views: List / Tree, `v`)
**List:** every component (system, chassis, board, BMC, PSU, FRU, CPU, DIMM,
disk, RAID controller/drive, NIC, transceiver, GPU, NPU) with type, location/slot,
vendor, model, part number, **serial** (always visible), firmware; a hint when
serials need root.
**Tree:** topology with status rolled up into each branch: system → platform
(board, BIOS, BMC, PSUs, fans) → processors → sockets → cores → threads; memory
→ sockets → channels → DIMMs; storage → RAID controller → virtual drives →
physical drives, md arrays → members, disks; network → bonds → ports →
transceivers; GPU/NPU. Problem branches open automatically; closed branches show
"(N problems inside)".
**Actions:** list: `/` search all fields (e.g. type a serial), `f` cycle category
filter, `enter` component page; tree: `→`/`space` open, `←` close or go to
parent, `enter` opens the component in its module tab.

### Detail pages (any tab, `enter`)
Full-screen scrollable page with sections: identity, sensors (value + gauge
against warn/crit limits + session sparkline), module-specific tables, counters
(value, "+N this session", "↑ +N last refresh"), findings.
**Actions:** `↑↓ pgup pgdn g G` scroll, `/` search with highlights, `n`/`N`
next/previous match, `esc` back, `q` quit.

### Help overlay (`?`)
Box drawn over the current screen (Lip Gloss compositor) listing navigation,
health, data and this-tab keys plus the status legend. Any key closes it.

## What is wrong today (please fix)

- Information density is uneven: some tabs leave half the screen empty, others
  truncate important text (card summaries, long findings).
- Hierarchy within a tab is weak: summary, table, detail strip and findings
  look alike; it is not obvious what to read first.
- Tables are wide and flat; status is only a glyph plus row tint.
- Detail strips under tables are cramped two-line "· separated" text.
- The tab bar gets crowded and loses problem counts below ~130 columns.
- The Stress live view needs a stronger "is it passing?" signal and better use of
  space; graphs need clearer axes and limits.
- Power and Events tabs need a design.

## What we want from you (deliverables)

1. **Design principles** (short): hierarchy, density, how status is shown,
   how selection/focus is shown, how empty/unavailable/"needs root" states look.
2. **Color tokens** for dark and light terminals, named by role (ok, warn, crit,
   unknown, accent, fg, muted, subtle, border, bar background, selection
   background, on-accent, new badge, graph series), each mapped to a **CharmTone
   name** (or an explicit hex where Tone has no usable color, with the reason),
   with contrast notes. Graph colors must stay distinguishable in 256 colors.
3. **Component specs** buildable with Lip Gloss/Bubbles: header, tab bar
   (incl. overflow behaviour), status badge, card, section heading, table (header,
   row, selected row, problem row, dimmed row, scroll indicator), key/value block,
   gauge with thresholds, sparkline, braille graph (axes, labels, limit line,
   legend), tree rows, detail page, overlay/pop-up, form fields (choice, text,
   button, disabled with reason), confirmation, toast/message, footer key hints.
   For each: characters used, colors (tokens), spacing, states.
4. **Screen mockups as monospace text** (exact characters, one per cell, annotated
   with color tokens) for: Overview, CPU (grid + cores), Memory (table + slot
   map), Storage, Network, GPU/NPU, Inventory tree, a detail page, Stress form,
   confirmation, live view and result, help overlay, and the new **Power** and
   **Events** tabs. Each at 120×40; Overview, a table tab and Stress live also at
   80×24 and 160×50.
5. **Interaction notes:** what `enter`, `esc`, `v`, `!`, `n` do on each screen if
   you change anything; how focus moves between regions (e.g. cards ↔ findings).
   Keep every action listed above available.
6. A short list of **what you changed and why**, ordered by impact.

Constraints recap: one-cell characters only, works without color, 80×24
minimum, light and dark themes, CharmTone palette, nothing that Bubble Tea v2 /
Lip Gloss v2 / Bubbles v2 cannot render, and status must be readable within two
seconds of opening any screen.
