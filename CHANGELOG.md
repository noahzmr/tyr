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

## [1.0.1] - 2026-11-18

- **License**: Tyr is now licensed under the Mozilla Public License 2.0.
  The archives carry `LICENSE` and `NOTICE` (third-party names and the
  libraries Tyr is built with). v1.0.0 was published without a license.

### Changes

#### Legal

- License Tyr under the Mozilla Public License 2.0 ([`145c1e5`](https://github.com/noahzmr/tyr/commit/145c1e575f7d8b45be4a851d07c4879da58fe554))

#### Documentation

- OpenSSF Security Baseline Level 1 self-assessment and plan ([`c81a089`](https://github.com/noahzmr/tyr/commit/c81a089abc2090c74e9bc4a2dbbc3922230642ec))
- Contributing guide and a bug report template ([`89b9881`](https://github.com/noahzmr/tyr/commit/89b98810fda96caa553b10541c7eded79f903974))
- Release flow through a pull request; Baseline status after the fixes ([`076c864`](https://github.com/noahzmr/tyr/commit/076c86428506d1ee536e7220a35ee3592e569ec0))
- Baseline AC-02.01 confirmed (collaborator list reviewed) ([`39bfb33`](https://github.com/noahzmr/tyr/commit/39bfb33d5fd37f8dac146d9ff70c3a9f36b79c2b))

#### Build and CI

- The notes reach the GitHub release, unwrapped ([`8a79db8`](https://github.com/noahzmr/tyr/commit/8a79db8e5291e472875b279785d93cfe5b597f1f))
- No binaries or secrets in version control ([`f036c5d`](https://github.com/noahzmr/tyr/commit/f036c5d07b0b7ec459c4800eb1fdd27dd76db84f))
- Release through a pull request, for a protected main ([`1b1fd8a`](https://github.com/noahzmr/tyr/commit/1b1fd8a9bbe6a78141d07aaf815306636b176452))

All changes: [v1.0.0...v1.0.1](https://github.com/noahzmr/tyr/compare/v1.0.0...v1.0.1)

## [1.0.0] - 2026-11-18

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

### Changes

#### Storage

- Network volumes, ioerr_cnt without own queries, cached SMART ([`2575e21`](https://github.com/noahzmr/tyr/commit/2575e21424905fbf2ea1ba6aee6a593626cedbb3))
- iSCSI / NVMe-oF sessions with a boot timeline; Longhorn nodes ([`239220a`](https://github.com/noahzmr/tyr/commit/239220aa3f64de73d172d2bef619c94e296b1257))
- SATA wear, data written and counters from the Device Statistics log ([`0bf8e9b`](https://github.com/noahzmr/tyr/commit/0bf8e9b4f5627d563ca62386f3dac2e29b39abb5))
- A disk's own specified maximum temperature as its limit ([`44eeb37`](https://github.com/noahzmr/tyr/commit/44eeb37318a21dd2b4d63eb7681f5e7a036c3894))
- Encryption state of disks, RAID, filesystems and swap ([`c9e960f`](https://github.com/noahzmr/tyr/commit/c9e960f142e6d73d9d7238fbce58b632fdaa46ce))
- Check and repair filesystems from the TUI (K) and tyr fsck ([`dc28930`](https://github.com/noahzmr/tyr/commit/dc2893072bcc4f9ab43865a0f7b2a097968d3a34))

#### CPU and memory

- FFT on the vector units with FMA, like Prime95 ([`7c3b233`](https://github.com/noahzmr/tyr/commit/7c3b233a044bf27d9b7352a98d9b2fcab402dd7d))

#### Network

- Explain drops and losses per interface ([`6b7cbfb`](https://github.com/noahzmr/tyr/commit/6b7cbfb316dffb2509491dc1647f9a03ba4d7d69))
- Native ethtool requests, the tool as fallback ([`25fc29d`](https://github.com/noahzmr/tyr/commit/25fc29d735596d740a1e07bf628736784fcaa99e))
- VPN interfaces with peers, service journal and kernel lines ([`05facf2`](https://github.com/noahzmr/tyr/commit/05facf22781f2bcf2c0f7d285a0c2c3a9d27a2d3))

#### System, power and BMC

- BMC event logs and server power in the TUI ([`8596bfc`](https://github.com/noahzmr/tyr/commit/8596bfcedc84818ce8683fd0dcf874541080c808))
- Decode the kernel's table natively, dmidecode as fallback ([`f5214da`](https://github.com/noahzmr/tyr/commit/f5214da37813308ece4e3e0aa9e6cd427573dced))
- Talk to the BMC through /dev/ipmi0, tools as fallback ([`2465141`](https://github.com/noahzmr/tyr/commit/2465141e76020c05ccf99be3da10b2c2f6712926))
- NVLink-C2C GPUs (DGX Spark GB10, Grace) without false PCIe alarms ([`31edeca`](https://github.com/noahzmr/tyr/commit/31edeca5cbf2e1c72c90a64ea947fa96d3662209))
- Drives behind HPE Smart Array, device part numbers, firmware ([`a9f3551`](https://github.com/noahzmr/tyr/commit/a9f35512b72bb9e910656568fff7e557360228dc))
- Operating system and kernel; the OS as the NetBox platform ([`11b4a1e`](https://github.com/noahzmr/tyr/commit/11b4a1e3a0b03536599893baeb15fdde5c8d1ffc))

#### Stress, acceptance and ISO

- Run CPUs up to their own limit; AMD Tjmax instead of 90 °C ([`c56d5f1`](https://github.com/noahzmr/tyr/commit/c56d5f19d3d08a1be093c090857550be2223fa47))
- Memory test runs to the limit; swap-out of others is not a trip ([`5abd420`](https://github.com/noahzmr/tyr/commit/5abd420b68f079a47e237d195e3486db148f8e3d))
- Prime95-style torture engine ([`a9a75bd`](https://github.com/noahzmr/tyr/commit/a9a75bd7a8a1b1a2cc8ff5a4c3977ad721e72f35))
- Acceptance test with one verdict, and a live ISO that runs it ([`b2c38cc`](https://github.com/noahzmr/tyr/commit/b2c38ccefcdc58a45828b015fd80f7d8f4d3a3df))
- Results partition on the stick itself, and say why copying fails ([`63422d7`](https://github.com/noahzmr/tyr/commit/63422d75623958426f33ade1d99995fa9abd0e1f))
- Live view on the terminal, plain lines without one, console.log always ([`e9fe8f3`](https://github.com/noahzmr/tyr/commit/e9fe8f3b780306507b4842617eb70300b78337fb))
- Find Go for a build started with sudo, and say what failed ([`cf9492f`](https://github.com/noahzmr/tyr/commit/cf9492f02d6508c9f5672b3e6f6e62ece60401b8))
- No false I/O-error FAIL from Tyr's own SMART queries ([`15b7aef`](https://github.com/noahzmr/tyr/commit/15b7aefc6e34b953b62157e52f804c4e986b7e35))

#### TUI

- Start stress tests from any tab, install missing engines ([`3b47789`](https://github.com/noahzmr/tyr/commit/3b4778996eeb8ea4f7834355f99f3edf92da937a))
- Copy to the desktop clipboard where OSC 52 does not work ([`46a8a0f`](https://github.com/noahzmr/tyr/commit/46a8a0fa77bb5a3c2d2fcd51a9040a6b2d1bbd1b))
- Memory and power sparklines on the cards ([`511087b`](https://github.com/noahzmr/tyr/commit/511087b9aac56d29584912e9cdb15138c4421fa0))
- NetBox write and dry run live, with progress and the objects as they go ([`7a4def9`](https://github.com/noahzmr/tyr/commit/7a4def90e68a7d25b55ec24af6f541f12b9eeb3b))
- Cleaner tables and tabs ([`77a9d5c`](https://github.com/noahzmr/tyr/commit/77a9d5c8aa6cf5b902fb980a8026faa6e736e32e))
- Build the ISO with the current settings and write it to a USB stick (I) ([`ab89de8`](https://github.com/noahzmr/tyr/commit/ab89de89f41371e0a6415b70b755a96955c498ea))
- The System card shows the operating system and the kernel ([`5c17a74`](https://github.com/noahzmr/tyr/commit/5c17a7413e4d21f9c71dbf111708edbf376fb8e9))
- Read-only file browser (B) ([`64b6a1d`](https://github.com/noahzmr/tyr/commit/64b6a1d16aa989e24be19a6f2e97a85462ddce7c))
- Feedback in the file browser ([`bacf2a5`](https://github.com/noahzmr/tyr/commit/bacf2a5d6b5158fe1fbfe736e1902d1895b18a90))

#### Reports

- Markdown reports, history from saved JSON, a glow-style viewer ([`4b1ef93`](https://github.com/noahzmr/tyr/commit/4b1ef93732ee3e52d231d4def39a4984023168d3))
- Generate them from the TUI; component and per-core health ([`d76d07e`](https://github.com/noahzmr/tyr/commit/d76d07eff6f944eb09f3e917f1acddd1f9c62fcc))
- Mermaid charts for stress results, gantt of the runs ([`d01dc99`](https://github.com/noahzmr/tyr/commit/d01dc9958b41cbd5c2883e9ea0759cc818ab8b14))
- Copy to the clipboard from the TUI and tyr view ([`494353a`](https://github.com/noahzmr/tyr/commit/494353a410a5e771becac74d1e8fa4ce8e480445))
- Age and power state of the components ([`048043e`](https://github.com/noahzmr/tyr/commit/048043ef94a38e1fc20e2b98c77c12254bf86112))

#### NetBox

- Write machines and test results to NetBox, tags from rules ([`50cf547`](https://github.com/noahzmr/tyr/commit/50cf5474f7730ce7e8d29cb64b75898ba8784e1d))
- Pick site, role, tenant, fields and tags from NetBox; paste ([`2775d5b`](https://github.com/noahzmr/tyr/commit/2775d5bd354f8d801def396756481a6fb970b34f))
- Dry run of a new device, the plan with values, one name per maker ([`75aa646`](https://github.com/noahzmr/tyr/commit/75aa64617601fd67e31eb9a38fc9ad781a4eb482))
- Machines without a serial number, found by their MAC addresses ([`c2638c2`](https://github.com/noahzmr/tyr/commit/c2638c2cf0d9a48232215e238484f1b7739a0dd7))
- No USB disks or empty drives as inventory items ([`afc3a9b`](https://github.com/noahzmr/tyr/commit/afc3a9b0072d9d6a97c45ac927e563c1e12b2d18))

#### Webhooks

- Webhook notifications from tests, configurable by file, environment and ISO ([`4754f8c`](https://github.com/noahzmr/tyr/commit/4754f8c8f19920900b4c9eb14989b507e95034f6))
- Keep webhook URLs out of logs and errors ([`40b605b`](https://github.com/noahzmr/tyr/commit/40b605b06d489c2586f11831c7559c97422b3e17))

#### Documentation

- Testudo-style README, developer guide, deep dives, diagrams ([`1edc295`](https://github.com/noahzmr/tyr/commit/1edc29510104acb5bc8304f5f599e03e385efba0))
- Bring every document up to date ([`c317e10`](https://github.com/noahzmr/tyr/commit/c317e1011eb10124fca35dc17feda2edb9b7de45))
- External tools - what is needed, how to install, official docs ([`0bba501`](https://github.com/noahzmr/tyr/commit/0bba5013ba0b20087b2faffa861e602e86e7eb84))
- Torture FFT, residue table and FFT paths ([`0cdb938`](https://github.com/noahzmr/tyr/commit/0cdb938ca6b4e5b2f8e091af0110860839098b77))
- Bring the documentation up to date ([`bafb447`](https://github.com/noahzmr/tyr/commit/bafb4471b17937068778248655597b57c4178e46))
- Network storage and Longhorn across the documentation ([`802830f`](https://github.com/noahzmr/tyr/commit/802830fa41dcd84c9a25fa2ed9bcf17a3f3f3b4f))
- Encryption, filesystem check and repair, the file browser ([`62da16d`](https://github.com/noahzmr/tyr/commit/62da16d539741002df0827b2b282ef55fac76404))
- Flags, keys, exit codes and tests of the storage additions ([`cef3447`](https://github.com/noahzmr/tyr/commit/cef3447156247a4afffddb016e22dff36cec84c2))
- Installing from a release, and how releases are made ([`87438d4`](https://github.com/noahzmr/tyr/commit/87438d46c5de1baf1915fbb699737a37258e1dfe))

#### Build and CI

- Workflow and hooks that keep tool attribution out of the repo ([`baaa45c`](https://github.com/noahzmr/tyr/commit/baaa45c63248882d80fe7c5ec2f152ed01ad7154))
- Catch attribution lines that contain non-UTF-8 bytes ([`10062b8`](https://github.com/noahzmr/tyr/commit/10062b8088a22c77e1a54e6134b32601ae13a8fd))
- Staticcheck clean again (HTTP status constants, unused code) ([`229a8bb`](https://github.com/noahzmr/tyr/commit/229a8bb585a32c04124fb953a0462d53849b4687))
- Tooling for semantic-version releases (make publish) ([`609bb2a`](https://github.com/noahzmr/tyr/commit/609bb2ac4f698a0c02ef43f2e73364018dfb0f92))

#### Other changes

- Init ([`5ee3167`](https://github.com/noahzmr/tyr/commit/5ee3167f27e44490f6c31c30740f88601c5c3330))
- README and checksums for copying the binary to other hosts ([`6b7753c`](https://github.com/noahzmr/tyr/commit/6b7753caf140ec852a30837acc25d71da6555b52))
- Explain alerts and show where they happen ([`737179f`](https://github.com/noahzmr/tyr/commit/737179fda8cf35e56a7102ac30754fd9fbbe85b8))
- Analyse offline CPU cores and test them isolated ([`90ccee5`](https://github.com/noahzmr/tyr/commit/90ccee57f6a0924a898968da5b6409515833fc00))
- Start the stress or offline-core test from a detail page with S ([`e822302`](https://github.com/noahzmr/tyr/commit/e8223023349e5b245d80dd035d6c62ae5417790e))
- Offline CPU pages: BMC processor events, socket, commands per CPU ([`ba1ec3d`](https://github.com/noahzmr/tyr/commit/ba1ec3db5dc7ba203b20bae294658888c165cc3a))
- Find cores that make programs crash; controller-wide SCSI errors ([`4f03f99`](https://github.com/noahzmr/tyr/commit/4f03f9986f317e121b043325e1f03978b85a8c37))
- Power supply state, boot history and HPE fans ([`740dcc1`](https://github.com/noahzmr/tyr/commit/740dcc1a8388de021455d6ca7e12d84c3da11998))

The first release: 66 commits.
