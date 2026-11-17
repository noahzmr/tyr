# Vendor tools for the ISO

Packages in this folder are installed into the ISO when it is built:

- `.deb` files through apt (dependencies from Debian),
- `.rpm` files unpacked into the root filesystem (no scripts run).

For example StorCLI (Broadcom MegaRAID), PERCCLI (Dell PERC) or ssacli (HPE
Smart Array). Where to download them: [docs/tools.md](../../docs/tools.md).
Their licences usually forbid passing them on: keep ISOs built with them
inside your organisation. The packages themselves are not committed.
