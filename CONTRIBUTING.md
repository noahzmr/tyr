# Contributing to Tyr

Thank you for helping. Bug reports, fixes, new hardware support, fixtures
from real machines and documentation are all welcome.

## Reporting a bug

Open an issue with the **Bug report** template
(<https://github.com/noahzmr/tyr/issues/new?template=bug_report.yml>). It asks
for what makes a report useful:

- `tyr version`, the distribution and `uname -r`;
- the command you ran (`sudo tyr`, `tyr report`, `tyr accept …`) and whether
  it ran as root;
- what happened and what you expected; the error or the screen;
- for wrong or missing hardware data: `sudo tyr report --format json`
  (remove serial numbers, host names and addresses you do not want public),
  and the vendor and model of the part.

Security problems are not reported in public issues: see
[SECURITY.md](SECURITY.md) - they go to a private GitHub security advisory.

## Proposing a change

1. For anything larger than a fix, open an issue first and describe the
   problem and the approach; that saves work on both sides.
2. Fork the repository and work on a branch. `main` only changes through
   pull requests.
3. Set up the hooks once: `git config core.hooksPath scripts/hooks`. They
   reject unformatted Go, binaries and secrets before they are committed.
4. Build and test as described in [DEVELOPER.md](DEVELOPER.md):
   `make lint test` must pass; new behaviour comes with tests (fixtures,
   golden screens or unit tests - DEVELOPER.md shows which fit where).
5. Update the documentation the change affects (`README.md`, `docs/`), and
   add a line to the **Unreleased** section of [CHANGELOG.md](CHANGELOG.md)
   when users will notice the change.
6. Open a pull request against `main`. CI must pass; the maintainer reviews
   every pull request before it is merged.

## Rules for changes

- **Commit messages**: a short subject `area: what changed` (`storage:`,
  `tui:`, `netbox:`, `docs:` …; the area sorts the release notes), then why
  and how in plain sentences. Messages, comments and files describe the work
  only - no notes or trailers crediting the tool that helped write them.
- **Read-only by default**: collectors never write to the host; anything
  that changes a machine needs a confirmation (see the rules in
  DEVELOPER.md).
- **No new dependencies** without discussing them in an issue first; no cgo.
- **No binaries or secrets** in the repository: build artifacts come from
  `make`, credentials from the environment or files that `.gitignore` keeps
  out. CI checks both.
- **License**: Tyr is licensed under the Mozilla Public License 2.0
  ([LICENSE](LICENSE)). By contributing you agree that your contribution is
  licensed under it. New source files start with the header in
  [COPYRIGHT_HEADER.txt](COPYRIGHT_HEADER.txt).
