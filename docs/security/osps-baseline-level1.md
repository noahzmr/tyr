# OpenSSF Security Baseline Level 1 self-assessment

Tyr is assessed against the [OpenSSF Open Source Project Security Baseline](https://baseline.openssf.org/)
Level 1, catalog [2026-08-28](https://baseline.openssf.org/versions/2026-08-28.html):
24 requirements for "any code or non-code project with any number of
maintainers or users".

## Statement

As of 2026-11-18, Tyr meets **all 24** OpenSSF Security Baseline Level 1
requirements. The repository is public, `main` is protected by an active
ruleset, v1.0.1 ships with the MPL-2.0 license, private vulnerability
reporting and push protection are on, and `SECURITY.md`, `CONTRIBUTING.md`
and the bug report template are in place.

The build pipeline is in good shape: CI runs on `pull_request` only, with
read-only permissions and no secrets, and passes untrusted pull request text
through environment variables instead of into the script.

## Per-control status

Status: **Met**, **Gap** (open), **Verify** (a setting in GitHub that cannot be
read from outside; check it and record the date).

| Control | Requirement (short) | Status | Evidence / what is missing |
|---|---|---|---|
| OSPS-AC-01.01 | MFA for sensitive actions on the repository | Met | Two-factor authentication is on for the owner's account (confirmed by the owner, 2026-11-18). Every collaborator added later needs it too: *Settings → Password and authentication* |
| OSPS-AC-02.01 | New collaborators get manually assigned, least privileges | Met | A personal-account repository has no default role: the owner adds each collaborator by invitation (*Settings → Collaborators*), which is the manual assignment. Collaborator list reviewed by the owner, 2026-11-18. Keep it to people who merge; contributions come as pull requests from forks and need no access. Should finer roles (read, triage) be needed, move the repository to an organisation with base permission *No permission* |
| OSPS-AC-03.01 | Direct commits to the primary branch are prevented | Met | Ruleset *protect main* (active since 2026-11-18, target: the default branch, empty bypass list): pull request required, status check `test` must pass. Releases go through a pull request too (`make release-prepare`, then `make publish` pushes only the tag) |
| OSPS-AC-03.02 | Deleting the primary branch needs explicit confirmation | Met | The same ruleset restricts deletions and blocks force pushes of `main` |
| OSPS-BR-01.01 | CI sanitises untrusted metadata | Met | `.github/workflows/ci.yml`: triggers `push` (main) and `pull_request`; the pull request title and body reach the script only as environment variables (`PR_TITLE`, `PR_BODY`), never as `${{ }}` inside `run:` |
| OSPS-BR-01.03 | CI on untrusted code has no privileged credentials | Met | `pull_request` (not `pull_request_target`): fork pull requests get no secrets; `permissions: contents: read`; the workflow uses no secrets. Releases are built locally (`make publish`), not in CI |
| OSPS-BR-03.01 | Official project channels use encryption | Met | The only official channel is `https://github.com/noahzmr/tyr` (issues, releases); every project link in the documentation is `https` |
| OSPS-BR-03.02 | Distribution channels are authenticated against tampering | Met | GitHub Releases over HTTPS with `SHA256SUMS`; the README's install steps download over `https` and check the sums (becomes reachable once the repository is public) |
| OSPS-BR-07.01 | No unencrypted secrets stored in version control | Met | GitHub secret scanning with push protection is on (confirmed by the owner, 2026-11-18): a push with a recognised secret is rejected. Also gitleaks in the pre-commit hook (staged changes) and in CI (the whole history; no findings), `.gitignore` for `iso/netbox.json`, `iso/webhooks.json`, `iso/ca/` and exports, and credentials never written into snapshots |
| OSPS-DO-01.01 | User guides for all basic functionality (after a release) | Met | `README.md` (install, quick start, CLI reference, keys) and `docs/` (TUI, health checks, stress tests, acceptance and ISO, reports, NetBox, webhooks, tools) |
| OSPS-DO-02.01 | A guide for reporting defects (after a release) | Met | `CONTRIBUTING.md` ("Reporting a bug") and the bug report template `.github/ISSUE_TEMPLATE/bug_report.yml` (version, system, command, what happened, `tyr report --format json`); the README's "Report an issue" opens it |
| OSPS-GV-02.01 | Public mechanism for discussing changes and problems | Met | Public GitHub Issues with a bug report template, and pull requests |
| OSPS-GV-03.01 | Documented contribution process (or "not accepted") | Met | `CONTRIBUTING.md`: issues first for larger changes, fork and pull request against `main`, hooks, tests and documentation, commit messages, maintainer review, MPL-2.0 for contributions |
| OSPS-LE-02.01 | Source code license is OSI / FSF approved | Met | Mozilla Public License 2.0 (OSI-approved), the same as the owner's other projects; brand-free, third-party names and libraries in `NOTICE` |
| OSPS-LE-02.02 | Released assets' license is OSI / FSF approved | Met | From v1.0.1 on the release archives are MPL-2.0 (v1.0.0 was published before the license) |
| OSPS-LE-03.01 | License in `LICENSE` / `COPYING` / `LICENSES/` | Met | `LICENSE` (plain MPL-2.0 text, recognised by license scanners) |
| OSPS-LE-03.02 | License included with the released assets | Met | `LICENSE` and `NOTICE` are in every archive, checked in `tyr_1.0.1_linux_arm64.tar.gz` |
| OSPS-QA-01.01 | Repository publicly readable at a static URL | Met | <https://github.com/noahzmr/tyr>, public since 2026-11-18; GitHub recognises the license as MPL-2.0 |
| OSPS-QA-01.02 | Public record of every change: what, who, when | Met | The public Git history records author and date of every commit; changes reach `main` as pull requests |
| OSPS-QA-02.01 | Dependency list for direct language dependencies | Met | `go.mod` / `go.sum` (Go modules); release tooling is pinned by version in `scripts/release.sh` and the `Makefile` |
| OSPS-QA-04.01 | Multi-repository projects list their codebases | Met | Tyr is a single repository; the README's directory structure covers it |
| OSPS-QA-05.01 | No generated executable artifacts in version control | Met | The committed `tyr-linux-amd64` is removed (2026-11-18) and `/tyr-linux-*` ignored; `scripts/check-binaries.sh` fails on executables and packaged binaries in the pre-commit hook and in CI. The old file remains in the Git history |
| OSPS-QA-05.02 | No unreviewable binary artifacts in version control | Met | Apart from the binary above, the only non-text files are about 120 tiny sysfs fixture files (`uevent`, `vpd`, `cmdline`, about 1 KB in all), generated from source by `tools/genfixture`, and the documentation's screenshots and diagrams (`docs/images/`) |
| OSPS-VM-02.01 | Security contacts in the documentation | Met | `SECURITY.md`: the maintainer as security contact, reporting through a private GitHub security advisory, what to include, supported versions, scope. Private vulnerability reporting is enabled (2026-11-18), so the advisory form is open to reporters; no email address is needed - the Baseline accepts the forge's own reporting tool |

## Plan to reach Level 1

In this order; each step names the requirements it closes.

1. ~~Choose and add a license~~ (LE-02.01, LE-03.01): done 2026-11-18,
   MPL-2.0 with `NOTICE` and `COPYRIGHT_HEADER.txt`.
2. ~~Ship the license with the releases~~ (done: v1.0.1, 2026-11-18) (LE-02.02, LE-03.02): `LICENSE`
   and `NOTICE` are in `archives.files`; publish the next release (v1.0.1
   or later). v1.0.0 stays as published, without a license; its CHANGELOG
   entry says so.
3. ~~Remove the committed binary~~ (QA-05.01): done 2026-11-18, with a
   check in the hook and CI. The file stays in the history; removing it
   from there means rewriting history and force-pushing, which is optional.
4. ~~Add the project documents~~ (VM-02.01, GV-03.01, DO-02.01): done
   2026-11-18 - `SECURITY.md` (private GitHub security advisories),
   `CONTRIBUTING.md`, the bug report template.
5. ~~Make the repository public~~ (done 2026-11-18) (QA-01.01, QA-01.02, GV-02.01) with Issues
   on. Before that: review the history for anything not meant to be public
   (hosts, addresses, customer names in fixtures or documentation examples,
   tokens) - the history becomes public with it.
6. ~~Turn on GitHub's protections~~ (done 2026-11-18) (BR-07.01, VM-02.01): *Settings → Advanced
   Security*: private vulnerability reporting, secret scanning, push
   protection, Dependabot alerts.
7. ~~Protect `main`~~ (done 2026-11-18, ruleset *protect main*) (AC-03.01, AC-03.02): *Settings → Rules → Rulesets →
   New branch ruleset*, target the default branch, enforcement *Active*, no
   bypass: *Restrict deletions*, *Block force pushes*, *Require a pull request
   before merging*, *Require status checks to pass* (the `test` job). The
   release flow already works with it (`make release-prepare`, then `make
   publish` after the merge; DEVELOPER.md "Releasing").
8. ~~Confirm the account settings~~ (AC-01.01, AC-02.01): two-factor
   authentication and the collaborator list confirmed by the owner,
   2026-11-18. Review the collaborators again when someone is added.
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
