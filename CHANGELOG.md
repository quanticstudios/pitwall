# Changelog

pitwall uses patch versioning while it is pre-1.0: every release bumps the last
number (`v0.0.1`, `v0.0.2`, ...). A release is a git tag on `main`;
`scripts/install.sh` stamps the binary with `git describe`, and
`pitwall --version` prints it.

To cut a release: add a section below, commit it, then
`git tag -a v0.0.N -m "pitwall v0.0.N"` and `git push origin main v0.0.N`.

## v0.0.6

- Groups and ungrouped tabs share one order: drag a group above loose tabs,
  or a tab between two groups.
- Tabs no longer get generated names. A tab shows its title (the agent's
  topic, the command, or the folder) until you name it. `pitwall ls` numbers
  tabs in sidebar order, and `attach`, `detach`, `kill` and `rename` take that
  number, a title, or a unique title prefix.
- A tab running Claude Code or Codex always shows the agent's mark and name,
  idle or working, in the sidebar and the switcher.
- Settings is a page instead of a dialog. Open it with the gear or `Ctrl+,`;
  it takes the pane area and the sidebar stays. Pick themes from previews,
  choose fonts and sizes, record shortcuts with conflict checks and swap,
  check whether the agent hooks are installed. Every change writes the one
  key to `config.toml` and keeps your comments.
- Protocol version 7, store format 6.

## v0.0.5

- A tab's branch, `+/-` stats and folder follow its shell's working
  directory, like its title, instead of the folder it started in.

## v0.0.4

- One level less: the sidebar lists **tabs**, optionally in groups. A tab
  holds its split panes; sessions are gone from the UI, the keys and the CLI.
  Saved state splits each old session's tabs into separate rows.
- Next/previous tab moves between rows (aide: Alt+J/K; conventional:
  Ctrl+Tab, Ctrl+PageDown/PageUp, Ctrl+Shift+PageDown/PageUp for groups).
  Alt+1-9 jumps to a tab. New tab opens right below the current one.
- A shell's tab title follows its working directory (`~`, `pitwall`).
- Drag and drop rebuilt: the row lifts, the others slide apart to open a gap,
  hovering a group header drops into it, Escape animates back.
- Config action names say tab (`next_tab`, `new_tab`, `goto_tab_N`); the old
  session names still load, with a note from `pitwall config check`.
- `pitwall ls` lists tabs with a label and a handle; commands accept either.
- Protocol version 6, store format 5.

## v0.0.3

- `~/.config/pitwall/config.toml` for keybindings, theme, fonts and pane
  spacing, reloaded live. `pitwall config init / check / default / path /
  schema`, with a generated JSON Schema for editor completion and checks.
- Two keybinding presets: **conventional** (the new default, Linux terminal
  conventions such as Ctrl+Shift+T, Ctrl+Tab, Ctrl+Shift+O/E) and **aide**
  (the previous Alt-key bindings). Single actions can be rebound or unbound.
- Themes: aide-dark, aide-light, tokyo-night, catppuccin-mocha, custom theme
  files, and per-color overrides. The daemon answers terminal color queries
  with the theme's palette.
- Configurable UI and terminal fonts, sizes, line height and fallback fonts.
- Tabs are listed under their session in the sidebar instead of a strip
  above the panes, with a hover "+" for a new tab.
- Sessions show the agent's topic as their name until you name them.
- Drag sessions and groups to reorder them; drop a session on a group to
  move it there.
- Hide the sidebar with Ctrl+Shift+B (Ctrl+B in the aide preset).
- Panes sit 4dp in from the window edges and 4dp apart.
- The sidebar shows the new pitwall logo.

## v0.0.2

- Claude Code's terminal titles reach the tab. Its `✳` title prefix contains
  byte 0x9C, which the terminal parser read as a string terminator, cutting
  the title to one byte and printing the rest on screen.
- Tabs are named after the agent's work: Claude's topic title, else the
  first prompt in the tab (Codex titles itself after the folder), else the
  running command. Slash commands are skipped.
- Sessions you never named carry a label that follows their busiest tab;
  the daemon keeps it current for the sidebar.
- New logo: a P whose stem is three status lights and whose bowl is a pane.
- The daemon stores session and group order and accepts reorder requests,
  ready for drag and drop in the sidebar.
- Protocol version 5: the next `pitwall` restarts an older running daemon.

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
