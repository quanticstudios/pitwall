# Security policy

## Supported versions

Security fixes target the latest release. Upgrade to the latest release
before checking whether a reported problem still occurs. pitwall is alpha
software; older releases do not receive backported fixes.

## Report a vulnerability privately

Use [Report a vulnerability](https://github.com/quanticstudios/pitwall/security/advisories/new)
to contact @quanticstudios through GitHub's private vulnerability reporting.
Do not open a public issue or pull request with an exploit or leaked secret.

Include the affected version and operating system, reproduction steps,
impact, and a minimal proof of concept using invented credentials and data.
The maintainer coordinates a fix and disclosure through the private report.
There is no guaranteed response deadline or bug bounty.

## Trust boundaries

pitwall starts shells and coding agents with your user account's permissions.
It does not sandbox their commands. Agent hooks report activity; they do not
enforce agent approvals or restrict access to files.

The daemon, saved sessions and local control connection belong to your user
account. Keep them local. Terminal output, logs and saved sessions can reveal
project paths, prompts and other private data. Review them before sharing.

Release installers compare downloads with `checksums.txt` from the same
GitHub release. This detects corruption but does not provide an independent
signature. macOS releases are not signed or notarized.
