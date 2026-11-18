# NetBox

Tyr writes what it knows about a machine into NetBox: the device, its
network ports, an inventory item per component, tags from rules you set, and
the result of a test as a journal entry. From the TUI (`N`), from the command
line (`tyr netbox`), after `tyr accept`, and by itself from the Tyr ISO.

## What Tyr writes

| NetBox | From Tyr | When |
|---|---|---|
| Device | found by its serial number - the system's, else the board's or the chassis', else the SMBIOS UUID unless it is a firmware default - and, for machines without any (a Protectli, a whitebox), by the MAC addresses of its ports; by host name only when it is not a shared one like the ISO's `tyr-live`. Created when missing, with manufacturer, device type, site, role (both created if missing), optional tenant and status; a new device without a serial is named after its model and a MAC (`FW4C-218527`) | always |
| Interfaces | every physical port: name, type from the speed and the port (`10gbase-x-sfpp`, `25gbase-x-sfp28` …), description, speed; the MAC address (NetBox 4.2+: a MAC address object, the interface's primary MAC) | `interfaces` |
| VPN interfaces | WireGuard, OpenVPN, Tailscale … as interfaces of type `virtual` (description: the VPN type, MTU), their addresses as IP addresses assigned to them | `interfaces` |
| BMC interface and IP | an interface for the management controller (`bmc`, or `iLO`/`iDRAC` if it exists) with its MAC; its IP address, assigned to that interface and set as the device's out-of-band IP | `interfaces` |
| Inventory items | one per component: CPUs, DIMMs, disks, RAID controllers and their drives, NICs, transceivers, GPUs, NPUs, power supplies, BMC FRUs, cards from the BMC's device inventory (not USB disks or drives without a medium: sticks come and go). Name, label (slot, bay), manufacturer, part number, serial, a description (model, size, firmware); role per category (`CPU`, `Memory`, `Disk` …, created if missing); marked as discovered | `inventory` |
| Status | `failed` when a component is critical or the acceptance test failed (the device and the inventory item); back to `active` when healthy again - only from `failed`, so `planned`, `staged` or `offline` stay | `mark_failed` |
| Tags | the tags of the rules that match; a rule's tag comes off when it no longer matches. Tags without a rule (set by people or other tools) are never touched | rules in `tags.json` |
| Platform | the operating system (`Ubuntu 24.04.3 LTS`), created if missing; a template, e.g. `{os} · {kernel}`. Not set from the Tyr ISO: its OS is the live system's, not the server's | `platform` |
| Custom fields | values from templates with facts: `"memory": "{memory_gb} GB"` | `custom_fields` |
| Journal entry | the acceptance test's summary (verdict, failures, remarks, steps, counters, limits) as success, warning or danger; without a test the health check's warnings and errors | `journal` |

Manufacturers are written under one name per company, whatever SMBIOS,
PCI, SMART or a transceiver call it (`GenuineIntel` and `Intel Corporation`
are Intel, `SUPERMICRO` is Supermicro, `FINISAR CORP.` is Finisar); a drive
that reports only its model gets its maker from it (`ST4000…` Seagate,
`MZQL…` Samsung). An existing manufacturer is found by slug or by name in
any case before a new one is created. A network card with several ports is
one inventory item, named after its PCI slot, with its ports in the
description.

A dry run (`--dry-run`, *Dry run* in the TUI) lists every object it would
create or change with the values, and the objects that are already right;
the TUI shows it as tables per kind of object.

NetBox 4.3 deprecated inventory items in favour of modules and plans to
remove them in a later release; they still work in 4.7. Tyr (like Logica)
uses them until then.

Tyr deletes nothing unless `prune` is on: then inventory items it marked as
discovered, of a category this run saw, that are no longer there. Drives are
named by their serial number (device names like `sda` change between boots),
so a replaced disk becomes a new item and the old one stays until you prune.

The token needs write access to these objects: `dcim` devices, device types,
device roles, sites, manufacturers, interfaces, MAC addresses, inventory
items and inventory item roles; `ipam` IP addresses; `extras` tags and
journal entries; and read access to `tenancy` tenants if you set one.

## Settings

Two JSON files, both editable in the TUI (`N`). As root (`tyr accept`, the
ISO) Tyr reads `/etc/tyr`; as a user `~/.config/tyr` when that exists or
`/etc/tyr` is not writable. `--netbox-config` and `--tags` name other files.
Unknown fields are rejected, so a typo cannot drop a setting silently.

`netbox.json` (mode 0600, it holds the token):

```json
{
  "url": "https://netbox.example.com",
  "token": "…",
  "site": "Lab",
  "role": "Server",
  "tenant": "",
  "name": "hostname",
  "status": "active",
  "interfaces": true,
  "inventory": true,
  "mark_failed": true,
  "journal": true,
  "prune": false,
  "inventory_categories": [],
  "platform": "{os}",
  "custom_fields": { "memory": "{memory_gb} GB" },
  "after_accept": false
}
```

| Field | Meaning |
|---|---|
| `url` | NetBox without `/api`; `TYR_NETBOX_URL` overrides |
| `token` | API token; `TYR_NETBOX_TOKEN` overrides. v2 tokens (`nbt_…`, NetBox 4.5) are sent as `Bearer` |
| `insecure` | do not check the certificate |
| `site`, `role`, `tenant` | for a device that is not in NetBox yet; without site and role Tyr does not create devices |
| `name` | name of a new device: `hostname` or `serial` (the serial is used when the host name is taken, and on the ISO, where every machine is `tyr-live`) |
| `status` | status of a new device: `active`, `planned`, `staged`, `offline`, `inventory` |
| `interfaces`, `inventory`, `mark_failed`, `journal`, `prune` | what to write (see above) |
| `inventory_categories` | categories written as inventory items; empty: all (`board bmc cpu dimm disk raid_controller raid_drive nic transceiver gpu npu psu fru device`). iSCSI / FC / NVMe-oF volumes (`volume`) are not parts of the machine and are left out unless listed |
| `platform` | the device's platform: a template over the facts, `{os}` by default (`Ubuntu 24.04.3 LTS`), `{os} · {kernel}` for one per kernel; `""` leaves it alone |
| `custom_fields` | device custom fields; `{fact}` is replaced by the fact's value. The fields must exist in NetBox |
| `after_accept` | write after every `tyr accept` (`--netbox` / `--no-netbox` override) |

`tags.json`: one rule per tag. **No rules, no tags**: Tyr sets none by
default.

```json
{
  "rules": [
    { "tag": "hw-memory", "when": "memory_gb >= 192" },
    { "tag": "hw-cpu",    "when": "cores >= 16 && threads >= 32" },
    { "tag": "hw-nics",   "when": "nic_speed_max >= 10000" },
    { "tag": "burnin-ok", "when": "verdict == \"PASS\" || verdict == \"REMARKS\"" },
    { "tag": "defect",    "when": "verdict == \"FAIL\" || failed_parts > 0" }
  ]
}
```

The tag is created in NetBox when missing (slug: lower case, `-` for
spaces).

## Rules

A rule compares facts with values:

| | |
|---|---|
| `==` `!=` | equal, not equal; text without regard to case |
| `<` `<=` `>` `>=` | numbers (text compares alphabetically) |
| `~` `!~` | matches / does not match a regular expression (case-insensitive): `model ~ "DL3[68]0 Gen1[01]"` |
| `&&` `\|\|` `!` `( )` | and, or, not, grouping |
| `"text"` `'text'` `192` `2.5` `true` `false` | values |

A fact alone is true when it is true, a number other than 0, or text that is
not empty: `gpus && !virtual`. A rule that does not parse is refused when the
file is saved or loaded; one that fails on a machine (a regular expression on
a number) leaves its tag as it is and is reported.

## Facts

`tyr netbox --facts` prints them with this machine's values and which rules
match; the TUI shows them under `f` and while editing a rule.

| Fact | Type | Meaning |
|---|---|---|
| `vendor`, `model`, `serial`, `hostname`, `bios` | text | system identity (SMBIOS) |
| `os`, `os_id`, `os_version`, `kernel` | text | operating system (`Ubuntu 24.04.3 LTS`, `ubuntu`, `24.04`) and kernel release |
| `live_system` | true/false | Tyr runs from a live medium (the ISO): `os` is the live system's |
| `arch` | text | `amd64`, `arm64` |
| `virtual` | true/false | a virtual machine |
| `cpu_model`, `cpu_vendor` | text | CPU model, `GenuineIntel` / `AuthenticAMD` |
| `sockets`, `cores`, `threads` | number | sockets, physical cores, logical CPUs |
| `memory_gb` | number | installed memory in GiB (DIMMs; else what the OS sees) |
| `dimms`, `dimm_slots` | number | populated and total DIMM slots |
| `ecc` | true/false | ECC memory |
| `disks`, `disk_tb` | number | physical disks (not USB, not virtual, not iSCSI / FC / NVMe-oF volumes) and their capacity in TB |
| `ssds`, `nvme`, `hdds` | number | by kind |
| `raid_controllers` | number | hardware RAID controllers |
| `iscsi_sessions` | number | iSCSI sessions and NVMe-oF controllers now |
| `longhorn` | true/false | a Longhorn node (`longhorn` → tag `longhorn-node`) |
| `nics`, `nics_10g` | number | physical ports, ports of 10 Gbit/s or more |
| `nic_speed_max` | number | fastest port in Mbit/s (link speed, else supported) |
| `nic_models`, `gpu_model` | text | models, comma-separated |
| `vpns`, `vpn_types` | number, text | VPN interfaces and their types (`WireGuard, Tailscale`) |
| `gpus`, `npus`, `psus` | number | accelerators, power supplies |
| `status` | text | health now: `OK`, `WARN`, `CRIT` |
| `critical`, `warnings` | number | findings now |
| `findings` | text | codes of all findings, space-separated: `findings ~ "\bpending_sectors\b"` (codes: `A` in the TUI) |
| `failed_parts` | number | components with critical status |
| `verdict`, `profile` | text | the acceptance test (`PASS` … / `burnin` …); empty without a test |

## Use

**TUI:** `N` opens the settings. `tab` switches between *Connection* and
*Tag rules*.

- Connection: `↑↓` field, `←→` change, `enter` edit or run. With URL and
  token set, Site, Role, Tenant and the custom fields are chosen from what
  NetBox has: a list that narrows as you type; a site or role NetBox does not
  have yet can be typed (it is created with the first new device), `tab`
  types any value by hand. The inventory categories are a list with check
  boxes (`space`). The buttons:
  *Save*, *Test* (URL and token), *Dry run* (what would be written, nothing
  written) and *Write to NetBox* (this machine, as collected now). Both run
  live: a progress bar over all objects, the time, the counts (new, changed,
  unchanged, problems) and each object as it is written; `esc` cancels.
  At the end `enter` opens every object as tables, with the log.
- Tag rules: every rule shows whether it matches this machine now. `a` adds
  (with NetBox set it starts with NetBox's tags; a new name creates the tag),
  `enter` edits (tag, then condition: the match updates as you type; `ctrl+l`
  lists NetBox's tags), `d` deletes, `J`/`K` move, `f` shows the facts, `s`
  saves.
- Paste with the terminal (`ctrl+shift+v`, middle click) or `ctrl+v` (reads
  the clipboard over OSC 52 where the terminal allows it); pasted onto a URL
  or token field it replaces the value. `ctrl+c` in a field copies it instead
  of quitting.

**Command line:**

```bash
tyr netbox                      # collect now and write
tyr netbox --dry-run            # show what would be written
tyr netbox --facts              # facts and matching rules, no NetBox
tyr netbox --test               # URL and token
tyr netbox --from RESULTS-DIR   # an acceptance result (after.json + accept.json)
tyr netbox --from snapshot.json # a saved snapshot
tyr netbox --validate           # check the two files
```

Exit code: 0 written, 1 written with problems (single interfaces or items),
2 nothing written (no connection, rejected token, device missing without
site/role), 3 bad settings.

**After `tyr accept`:** with `after_accept` (or `--netbox`) the result is
written at the end: the device, its parts, the tags (the rules see the
`verdict`) and the summary as a journal entry. The log goes to `netbox.log`
in the results directory, the outcome to `netbox.json`. A NetBox that cannot
be reached is retried three times (30 s apart) and never changes the verdict.

## The ISO

Put the two files next to the build and build the ISO; they become
`/etc/tyr/netbox.json` and `/etc/tyr/tags.json` (root-only) in the image:

```bash
cp netbox.json tags.json iso/        # ignored by git
cp company-ca.crt iso/ca/            # optional: NetBox with an internal CA
make iso
```

The build checks both files (`tyr netbox --validate`). Instead of a file,
`TYR_NETBOX_URL` and `TYR_NETBOX_TOKEN` given to `make iso` go into
`/etc/tyr/tyr.env` in the image (settings for new devices still need the
file). After the test the ISO
writes the result to the device by itself; DHCP is switched on for that
(DNS through systemd-resolved). A `netbox.json` or `tags.json` on the
`TYR-RESULTS` partition replaces the built-in one; `tyr.netbox=0` on the
kernel command line turns it off.

The token is in the image: anyone with the ISO can read it. Use a token that
can write only what Tyr writes, and keep such an ISO in-house like one with
vendor tools.
