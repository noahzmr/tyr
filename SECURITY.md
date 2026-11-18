# Security policy

## Reporting a vulnerability

Please report security problems **privately** through GitHub's security
advisories - not in a public issue, pull request or discussion:

**<https://github.com/noahzmr/tyr/security/advisories/new>**
(the repository's *Security* tab → *Report a vulnerability*)

Only the maintainers see the report. Please include:

- the Tyr version (`tyr version`) and how it runs: TUI, `tyr report`,
  `tyr accept`, the live ISO;
- what an attacker can do, and what they need for it (local user, root,
  access to the network, to the BMC, to NetBox, to a webhook receiver);
- the steps or a proof of concept, and anything you know about a fix.

## Security contacts

| Who | Role | Contact |
|---|---|---|
| Noah Zeumer ([noahzmr](https://github.com/noahzmr)) | maintainer | GitHub security advisory (above) |

## What happens next

The maintainer aims to acknowledge a report within 7 days, agrees on a
disclosure date with you, fixes the problem in a patch release and
publishes the advisory with it (GitHub can assign a CVE). You are credited
in the advisory unless you prefer not to be.

## Supported versions

| Version | Security fixes |
|---|---|
| the latest 1.x release | yes |
| older releases | no - update to the latest release |

## Scope

Tyr runs as root on servers and talks to their BMC, so these matter most:

- anything that lets a user or a remote party run code or write files as
  the user running Tyr;
- the commands that change a machine: filesystem repair, the stress
  tests, the offline-core test, package installs, writing the ISO to a USB
  stick;
- credentials Tyr handles: the Redfish account, the NetBox token, webhook
  tokens and secrets - they must never reach snapshots, reports, logs or
  result files;
- the live ISO, and the release archives and their checksums.

Problems in the tools Tyr runs (smartctl, storcli, stress-ng …) belong to
their projects; tell us too if Tyr uses them unsafely.
