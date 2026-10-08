# Maintaining the public repository

## Changes to main

Two active GitHub rulesets apply to `main`:

- **main: pull requests only** requires a pull request and squash merge,
  keeps linear history, and blocks force pushes and deletion. It has no
  bypass actors.
- **main: owner approval and CI** requires one approval, a code owner review,
  resolved review discussions, and these GitHub Actions checks against
  current `main`: `linux`, `other (macos-latest)`, and
  `other (windows-latest)`. New commits dismiss stale approvals. Repository
  administrators can bypass this ruleset only when merging a pull request.

[CODEOWNERS](../.github/CODEOWNERS) assigns every file to @quanticstudios.
@quanticstudios is the only repository administrator. Keep other contributors
at write or lower access; granting admin also grants the exception for merges.
The owner can merge their own pull requests, or merge a contribution without
approval or passing CI. The first ruleset still blocks direct pushes for the
owner. Use the exception deliberately and record any skipped checks in the
pull request.

GitHub administrators can edit or remove repository rulesets. These rules
block pushes while they remain active; they cannot prevent an administrator
from changing the repository settings.

Merge with squash and let GitHub delete merged branches. Changes to workflows
and CODEOWNERS need the same owner review as application code. Do not use
`pull_request_target` to build or execute contributor code with write tokens.

## Security and dependencies

Keep secret scanning, push protection, Dependabot alerts, Dependabot security
updates, and private vulnerability reporting enabled. CodeQL default setup
analyzes GitHub Actions, Go and JavaScript/TypeScript. Review alerts under
[Security](https://github.com/quanticstudios/pitwall/security).

[dependabot.yml](../.github/dependabot.yml) requests weekly updates for
Actions and both Go modules. Review dependency changes and license
notices before merging. Pin workflow actions to full commit SHAs.

Keep the default `GITHUB_TOKEN` permissions read-only and leave workflow
approval of pull requests disabled. Require workflow approval for all
external contributors. Inspect their workflow and build-script changes
before approving a run. Use GitHub-hosted runners for public contributions.

## Publish a release

`scripts/release.sh` runs steps 1 and 3: it opens the release pull request
from the `## Unreleased` section, and `scripts/release.sh --tag` tags the
merged release commit and pushes the tag once. The steps below are what it
does, and what to check around it.

1. Merge the release changes and the matching `## v0.1.0-alpha.N` section in
   [CHANGELOG.md](../CHANGELOG.md) through a pull request.
2. Check that all required CI jobs passed for the commit you intend to tag.
   Run platform checks relevant to the release. The full macOS and Windows
   suites remain advisory; a green build does not prove those suites passed.
3. Tag that commit on `main` with `v0.1.0-alpha.N` and push the tag on its
   own. The release workflow verifies that the tagged commit belongs to `main`
   before building and publishes the release as the latest one.
4. Review the six platform archives, `checksums.txt`, notices and release notes
   on the GitHub release before announcing it.

The **release tags: immutable** ruleset blocks updates and deletion of `v*`
tags with no bypass actors. Correct a published release with a new version.
The release workflow grants `contents: write` only to the job that publishes
the release; build and verification jobs use read access.

## Before announcing pitwall

- Review open security alerts and resolve confirmed vulnerabilities.
- Scan reachable Git history for credentials as well as the current files.
  Rotate any exposed credential; deleting a file does not revoke a credential.
- Keep the alpha status and platform limitations in [README.md](../README.md)
  accurate. Check the install commands against the release you announce.
- Check that media, fonts, copied code and dependency notices have the licenses
  needed for redistribution. Promo media must have redistribution rights.
