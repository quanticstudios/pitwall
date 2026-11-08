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

## Ship a screen-pattern fix

1. Edit [screen_patterns.json](../internal/agent/screen_patterns.json).
   Increase `patterns_version`, a positive integer, for every fix. Keep
   `schema_version` at `1` unless the loader and format change together.
2. Add a screen fixture under `internal/agent/testdata` and an entry in
   `screen_checks.json` with the provider and expected state. The optional
   `screen` value checks the common classifier: `0` means none, `1` busy,
   and `2` form. Keep existing fixtures and expectations passing.
3. Run `go test ./internal/agent` with the build environment described in
   [CONTRIBUTING.md](../CONTRIBUTING.md), then merge through a pull request.

Running daemons fetch the raw file on `main` at start and every 24 hours.
Using `main` lets a merged fix reach users without a binary release or a
release asset. Each daemon checks the downloaded file against fixtures
bundled in its binary, then saves and activates a newer version. Old binaries
cannot run fixtures added after their release. A fix that needs new matching
logic or a new schema needs a binary release.

The file holds only substring lists and Go regular expressions. Substrings
are lowercase and match against lowercase screen text. `common.busy_regex`
matches original text. Common forms outrank busy matches; within a form,
the bottommost plan or input line wins. Provider rules try plan, input,
approval, then busy across the last 12 non-empty rows.

The loader accepts at most 256 matchers, 4096 bytes per matcher, and 256 KiB
per file. It rejects unknown fields, unsupported schemas, invalid regexes
and failed fixtures. Versions must increase above both the embedded and
active rules. To undo a bad fix, restore the correct patterns with another
version increase. See [configuration](config.md#screen-detection-updates)
for disabling fetches and [troubleshooting](troubleshooting.md#screen-detection-patterns)
for checking the version in use.

## Publish a release

`scripts/release.sh` runs steps 1 and 3: it opens the release pull request
from the `## Unreleased` section, named for the next patch of the newest
release or for the version you pass, `v0.2.0` to start the minor, and `scripts/release.sh --tag` tags the
merged release commit and pushes the tag once. The steps below are what it
does, and what to check around it.

1. Merge the release changes and the matching `## v0.2.N` section in
   [CHANGELOG.md](../CHANGELOG.md) through a pull request.
2. Check that all required CI jobs passed for the commit you intend to tag.
   Run platform checks relevant to the release. The full macOS suite
   remains advisory; a green build does not prove it passed.
3. Tag that commit on `main` with `v0.2.N` and push the tag on its
   own. The release workflow verifies that the tagged commit belongs to `main`
   before building and publishes the release as the latest one.
4. Review the six platform archives, `checksums.txt`, notices and release notes
   on the GitHub release before announcing it.
5. Push the AUR package by hand, as below. The workflow updates Homebrew and
   Scoop itself.

The **release tags: immutable** ruleset blocks updates and deletion of `v*`
tags with no bypass actors. Correct a published release with a new version.
The release workflow grants `contents: write` only to the job that publishes
the release and to the packages job, which attaches the AUR files to it;
build and verification jobs use read access.

## Package managers

After the release is out, the workflow's `packages` job runs
`scripts/pkgrender` on the release's `checksums.txt`. It pushes
`Formula/pitwall.rb` to `quanticstudios/homebrew-tap` and
`bucket/pitwall.json` to `quanticstudios/scoop-bucket` as Quantic Studios,
each with its own deploy key, held in the `HOMEBREW_TAP_DEPLOY_KEY` and
`SCOOP_BUCKET_DEPLOY_KEY` repository secrets. It skips a repository whose
secret is missing. A failure there leaves the release published; rerun the
job once it is fixed.

The job also attaches `pitwall-bin.aur.tar.gz`, the AUR `PKGBUILD` and
`.SRCINFO`. Pushing to the AUR takes the maintainer's AUR account, so it is
a hand step, from a clone of `ssh://aur@aur.archlinux.org/pitwall-bin.git`:

```sh
curl -fsSL https://github.com/quanticstudios/pitwall/releases/download/v0.2.N/pitwall-bin.aur.tar.gz | tar -xz
git add PKGBUILD .SRCINFO
git commit -m "pitwall v0.2.N"
git push origin HEAD:master
```

The AUR accepts only a `master` branch, whatever git's default is.

## Before announcing pitwall

- Review open security alerts and resolve confirmed vulnerabilities.
- Scan reachable Git history for credentials as well as the current files.
  Rotate any exposed credential; deleting a file does not revoke a credential.
- Keep the alpha status in [README.md](../README.md) and the platform
  limitations in [install.md](install.md#platform-notes) accurate. Check the
  install commands against the release you announce.
- Check that media, fonts, copied code and dependency notices have the licenses
  needed for redistribution. Promo media must have redistribution rights.
