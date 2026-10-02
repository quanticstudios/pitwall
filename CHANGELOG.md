# Changelog

pitwall uses patch versioning while it is pre-1.0: every release bumps the last
number (`v0.0.1`, `v0.0.2`, ...). A release is a git tag on `main`;
`scripts/install.sh` stamps the binary with `git describe`, and
`pitwall --version` prints it.

To cut a release: add a section below, commit it, then
`git tag -a v0.0.N -m "pitwall v0.0.N"` and `git push origin main v0.0.N`.

## v0.0.1

The first tagged build.

- Native Gio window with an aide-style sidebar beside split terminal panes.
- Starts like tmux: straight into a shell in the folder you launch it from.
- Sessions with generated names (`swift-otter`), tabs, and splits. Exiting a
  pane closes it; the tab and session close when empty; the window closes with
  the last session.
- Ctrl+T tab mode, Alt+J/K/H/L navigation and the Alt-hold session switcher.
- Live state for every session: the command a terminal runs, and Claude Code
  or Codex working, waiting for input, asking for approval, plan ready, done,
  or failed. Exact through hooks, read from the screen without them.
- Tab titles follow the agent's terminal title; agents can name their tab with
  `pitwall tab rename`.
- Groups made after the fact, one-click grouping by folder, and git worktree
  sessions.
- Detach and attach, and `pitwall ls / new / attach / detach / kill / rename`.
- The daemon owns the PTYs: closing the window keeps sessions running, and
  after a reboot sessions come back with agents resumed.
- Desktop notifications when a session needs you.
- `pitwall hooks install` merges the hooks into Claude Code and Codex config.
