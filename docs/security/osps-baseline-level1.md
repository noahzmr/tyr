# OpenSSF Security Baseline Level 1 self-assessment

Tyr is assessed against the [OpenSSF Open Source Project Security Baseline](https://baseline.openssf.org/)
Level 1, catalog [2026-08-28](https://baseline.openssf.org/versions/2026-08-28.html):
24 requirements for "any code or non-code project with any number of
maintainers or users".

## Statement

As of 2026-11-18, Tyr does **not yet** meet Level 1: 8 requirements are met,
2 depend on account settings that could not be checked from outside, and 14
are open. Most open items come from three causes:

1. **The repository is private.** GitHub answers `404 Not Found` to
   unauthenticated requests for `noahzmr/tyr`, its branches and its releases.
   Level 1 needs a publicly readable repository and history (QA-01), public
   discussion (GV-02), and in practice private vulnerability reporting and
   free secret scanning (VM-02, BR-07), which GitHub offers for public
   repositories.
2. **There is no license.** Four Legal requirements (LE-02, LE-03) fail
   until an OSI-approved license is in the repository and in the release
   archives.
3. **Project documents are missing:** a security contact (`SECURITY.md`), the
   contribution process (`CONTRIBUTING.md`) and a guide for reporting defects.

The build pipeline is in good shape: CI runs on `pull_request` only, with
read-only permissions and no secrets, and passes untrusted pull request text
through environment variables instead of into the script.

## Per-control status

Status: **Met**, **Gap** (open), **Verify** (a setting in GitHub that cannot be
read from outside; check it and record the date).

| Control | Requirement (short) | Status | Evidence / what is missing |
|---|---|---|---|
| OSPS-AC-01.01 | MFA for sensitive actions on the repository | Verify | Personal-account repository: the owner's account (and every collaborator's) must have two-factor authentication on. *Settings → Password and authentication* |
| OSPS-AC-02.01 | New collaborators get manually assigned, least privileges | Verify | On a personal account, collaborators are added only by invitation (manual assignment). If the project moves to an organisation, set the base permission to *No permission* or *Read* |
| OSPS-AC-03.01 | Direct commits to the primary branch are prevented | Gap | `main` takes direct pushes; `make publish` pushes the release commit to `main`. Needs a branch ruleset (pull request required, no bypass) and a release flow through a pull request |
| OSPS-AC-03.02 | Deleting the primary branch needs explicit confirmation | Gap | Same ruleset: *Restrict deletions*, *Block force pushes* |
| OSPS-BR-01.01 | CI sanitises untrusted metadata | Met | `.github/workflows/ci.yml`: triggers `push` (main) and `pull_request`; the pull request title and body reach the script only as environment variables (`PR_TITLE`, `PR_BODY`), never as `${{ }}` inside `run:` |
| OSPS-BR-01.03 | CI on untrusted code has no privileged credentials | Met | `pull_request` (not `pull_request_target`): fork pull requests get no secrets; `permissions: contents: read`; the workflow uses no secrets. Releases are built locally (`make publish`), not in CI |
| OSPS-BR-03.01 | Official project channels use encryption | Met | The only official channel is `https://github.com/noahzmr/tyr` (issues, releases); every project link in the documentation is `https` |
| OSPS-BR-03.02 | Distribution channels are authenticated against tampering | Met | GitHub Releases over HTTPS with `SHA256SUMS`; the README's install steps download over `https` and check the sums (becomes reachable once the repository is public) |
| OSPS-BR-07.01 | No unencrypted secrets stored in version control | Gap | Partly in place: `.gitignore` keeps `iso/netbox.json`, `iso/webhooks.json`, `iso/ca/` and exports out; credentials are never written into snapshots. Missing: an enforced check. Secret scanning with push protection is free for public repositories; until then a secret scanner in the pre-commit hook and CI |
| OSPS-DO-01.01 | User guides for all basic functionality (after a release) | Met | `README.md` (install, quick start, CLI reference, keys) and `docs/` (TUI, health checks, stress tests, acceptance and ISO, reports, NetBox, webhooks, tools) |
| OSPS-DO-02.01 | A guide for reporting defects (after a release) | Gap | The README links "Report an issue" but does not say what a report needs. Add a bug report issue template and a short section: `tyr version`, the distribution and kernel, `tyr report --format json` (or the failing command's output), what was expected |
| OSPS-GV-02.01 | Public mechanism for discussing changes and problems | Gap | GitHub Issues exist, but are not public while the repository is private. Make the repository public with Issues on (Discussions optional) |
| OSPS-GV-03.01 | Documented contribution process (or "not accepted") | Gap | `DEVELOPER.md` explains build, tests and hooks, not how to contribute: no `CONTRIBUTING.md` (pull requests against `main`, the `area: summary` commit style, CI and attribution checks, review). Or state plainly that outside contributions are not accepted |
| OSPS-LE-02.01 | Source code license is OSI / FSF approved | Gap | No license |
| OSPS-LE-02.02 | Released assets' license is OSI / FSF approved | Gap | No license in `tyr_1.0.0_linux_*.tar.gz` |
| OSPS-LE-03.01 | License in `LICENSE` / `COPYING` / `LICENSES/` | Gap | No `LICENSE` file |
| OSPS-LE-03.02 | License included with the released assets | Gap | Add `LICENSE` to the archive files in `.goreleaser.yaml`; releases after that carry it |
| OSPS-QA-01.01 | Repository publicly readable at a static URL | Gap | Private |
| OSPS-QA-01.02 | Public record of every change: what, who, when | Gap | The Git history records author and date for every commit, but it is not public while the repository is private |
| OSPS-QA-02.01 | Dependency list for direct language dependencies | Met | `go.mod` / `go.sum` (Go modules); release tooling is pinned by version in `scripts/release.sh` and the `Makefile` |
| OSPS-QA-04.01 | Multi-repository projects list their codebases | Met | Tyr is a single repository; the README's directory structure covers it |
| OSPS-QA-05.01 | No generated executable artifacts in version control | Gap | `tyr-linux-amd64` (16 MB, a stripped static ELF binary) is committed at the repository root since `e822302` (2026-11-16). Remove it, ignore `/tyr-linux-*`, and let CI fail on tracked executables |
| OSPS-QA-05.02 | No unreviewable binary artifacts in version control | Met | Apart from the binary above, the only non-text files are about 120 tiny sysfs fixture files (`uevent`, `vpd`, `cmdline`, about 1 KB in all), generated from source by `tools/genfixture`, and the documentation's screenshots and diagrams (`docs/images/`) |
| OSPS-VM-02.01 | Security contacts in the documentation | Gap | No `SECURITY.md`. Add one (how to report privately, what to include, response time, supported versions) and enable GitHub private vulnerability reporting, which requires a public repository |

## Plan to reach Level 1

In this order; each step names the requirements it closes.

1. **Choose and add a license** (LE-02.01, LE-03.01): an OSI-approved
   license in `LICENSE`, and the same in the README. Apache-2.0 (with its
   patent grant) and MIT are the common choices for a Go tool; GPL-3.0 if
   changes must stay open. This is the maintainer's decision.
2. **Ship the license with the releases** (LE-02.02, LE-03.02): add `LICENSE`
   to `archives.files` in `.goreleaser.yaml`. v1.0.0 was released without
   it; v1.0.1 or later will carry it. Alternatively re-publish v1.0.0's
   assets with the license, or document that v1.0.0 predates it.
3. **Remove the committed binary** (QA-05.01): `git rm --cached
   tyr-linux-amd64`, add `/tyr-linux-*` to `.gitignore`, and a CI step that
   fails when an ELF or other executable file is tracked. The file stays in
   the history; removing it from there means rewriting history and
   force-pushing, which is optional and needs coordination.
4. **Add the project documents** (VM-02.01, GV-03.01, DO-02.01):
   `SECURITY.md` with a private reporting route (GitHub private vulnerability
   reporting, or a dedicated security address - not a personal one);
   `CONTRIBUTING.md`; `.github/ISSUE_TEMPLATE/bug_report.yml` asking for the
   version, distribution, kernel, the command and its output.
5. **Make the repository public** (QA-01.01, QA-01.02, GV-02.01) with Issues
   on. Before that: review the history for anything not meant to be public
   (hosts, addresses, customer names in fixtures or documentation examples,
   tokens) - the history becomes public with it.
6. **Turn on GitHub's protections** (BR-07.01, VM-02.01): *Settings → Advanced
   Security*: private vulnerability reporting, secret scanning, push
   protection, Dependabot alerts.
7. **Protect `main`** (AC-03.01, AC-03.02): *Settings → Rules → Rulesets →
   New branch ruleset*, target the default branch, enforcement *Active*, no
   bypass: *Restrict deletions*, *Block force pushes*, *Require a pull request
   before merging*, *Require status checks to pass* (the `test` job). Then
   change the release flow: `make publish` cannot push the release commit to
   `main` any more - the CHANGELOG update goes through a pull request, and
   publishing tags the merged commit.
8. **Confirm the account settings** (AC-01.01, AC-02.01): two-factor
   authentication on the owner's and every collaborator's account; record
   the date here.
9. **Check and record**: run the OSPS Baseline scanner (`pvtr` with the
   `osps-baseline` catalog, as other projects do) against `main`, update the
   statement and this table, and optionally publish a machine-readable
   `security-insights.yml` that points to this document.

Worth doing while at it, though not Level 1: pin the GitHub Actions in
`ci.yml` to commit SHAs instead of `@v4` / `@v5`, and sign release tags or
artifacts.

## Keeping this current

Update this document when the repository settings, the branch protection,
the release process or a workflow change, and after each scanner run, with
the date.
