# Tyr Documentation

Deeper documentation that doesn't belong in the top-level `README.md`
(user-facing) or `DEVELOPER.md` (contributor-facing).

## Index

| Document | Audience | Summary |
|---|---|---|
| [architecture.md](architecture.md) | engineers | packages, the snapshot, the refresh loop, design rules |
| [health-checks.md](health-checks.md) | operators | every module: sources, checks, limits, findings |
| [stress-tests.md](stress-tests.md) | operators | lifecycle, engines, guard rules, verdicts, the network peer |
| [acceptance.md](acceptance.md) | operators | the acceptance test (`tyr accept`), limits files, the verdict; building and booting the Tyr ISO |
| [netbox.md](netbox.md) | operators | NetBox: what Tyr writes, settings, tag rules and facts, the ISO |
| [webhooks.md](webhooks.md) | operators | webhook notifications from tests: events, formats, settings, environment, the ISO |
| [reports.md](reports.md) | operators | Markdown reports, Mermaid charts, history, viewing and copying |
| [tui.md](tui.md) | users | every tab, view and key; layouts and themes |
| [tools.md](tools.md) | operators | external tools: what Tyr reads itself, what to install from the distribution or the vendor, official docs |
| [DESIGN.md](DESIGN.md) | engineers | the original design proposal (data model, JSON schema, stress interface) |
| [design/](design/) | designers | the brief for the TUI redesign and the screens before it |

## Diagrams

| Diagram | Kind | Source |
|---|---|---|
| ![](images/architecture.svg) | archify architecture | [diagrams/architecture.architecture.json](diagrams/architecture.architecture.json) |
| ![](images/stress-lifecycle.svg) | archify lifecycle | [diagrams/stress-lifecycle.lifecycle.json](diagrams/stress-lifecycle.lifecycle.json) |
| ![](images/reports.svg) | archify dataflow | [diagrams/reports.dataflow.json](diagrams/reports.dataflow.json) |

The `.html` pages next to the sources are interactive (theme toggle, PNG/SVG
export). Re-render with `make diagrams` - see [DEVELOPER.md](../DEVELOPER.md#6-diagrams).
