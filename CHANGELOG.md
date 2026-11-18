# Changelog

All notable changes to Tyr. Versions follow [semantic versioning](https://semver.org):
a major version for incompatible changes to the command line, the JSON
snapshot schema or the configuration files, a minor version for new
features, a patch version for fixes.

Each release has hand-written highlights followed by its commits grouped by
area, generated from the Git history by `make publish`.

## Unreleased

<!-- Highlights and upgrade notes of the next release; `make publish` moves
them into its section and adds the commits grouped by area. -->

The first stable release of Tyr: one static binary for Linux servers (amd64
and arm64) that shows the hardware, judges its health, stresses it and
writes the results where they are needed.

- **Inventory and health** of CPU, memory, storage, network, GPUs/NPUs, power
  and the BMC's event logs: local Linux interfaces first (sysfs, procfs,
  SMBIOS, ethtool and IPMI ioctls), tools where they add data, Redfish where
  Linux has no answer; every reading judged against the part's own limit.
- **Storage**: SMART for ATA, SAS and NVMe (also the drives behind HPE Smart
  Array from the BMC), I/O errors with the kernel lines, md and hardware
  RAID; iSCSI / NVMe-oF sessions with a timeline of the boot, Longhorn
  volumes, replicas and node checks; encryption at rest (LUKS, self-encrypting
  drives, controller and ZFS encryption); filesystem check and repair (`K`,
  `tyr fsck`) and a read-only file browser (`B`).
- **TUI** with a tab per module, detail pages, an alert reference and an
  explanation for every alert.
- **Stress tests**: CPU (a Prime95-style torture test built in), memory,
  storage, network, an isolated test for offline cores; the acceptance test
  `tyr accept` with PASS / REMARKS / FAIL, and a live ISO that runs it at boot.
- **Reports** in Markdown with Mermaid charts and history across runs;
  **NetBox** sync of devices, interfaces, inventory and tags; **webhooks**
  for test events.

Install: download `tyr_1.0.0_linux_<arch>.tar.gz`, check it against
`SHA256SUMS`, copy `tyr` to `/usr/local/bin` and run `sudo tyr`. The JSON
snapshot schema is 1.0.
