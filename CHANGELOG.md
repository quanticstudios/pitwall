# Changelog

pitwall is in alpha. Releases are `v0.1.0-alpha.1`, `v0.1.0-alpha.2`, ...:
each release bumps the alpha number and becomes the latest release on
GitHub, which is what the installers take. The `v0.0.N` tags before that
were the same alpha under patch numbers. A release is a git tag on `main`; `scripts/install.sh` stamps the
binary with `git describe`, and `pitwall --version` prints it.

To cut a release: add a section below and merge it, then
`git tag -a v0.1.0-alpha.N -m "pitwall v0.1.0-alpha.N"` on that commit and
`git push origin v0.1.0-alpha.N` (the tag alone, so the release build runs).

## v0.1.0-alpha.4

- Ctrl+Backspace deletes the word before the cursor. It sends Ctrl+W, the
  delete-word key in bash, zsh, fish, Claude Code and Codex.

## v0.1.0-alpha.3

- Run `pitwall` as many times as you like: each run opens its own window.
  It reopens the most recent session no window shows, else starts a new
  session in the folder you ran it from. `pitwall -s <name>` still raises
  the window that shows that session.
- Copy on select: a mouse selection goes to the clipboard as soon as you make
  it (drag release or double-clicked word), with a quiet "Copied 42
  characters" notice at the bottom of the panes. The copy shortcut shows the
  notice too. Turn it off with `copy_on_select = false` under the new
  `[terminal]` table, or the switch in Settings, Terminal.
- Links in panes are underlined: web, `file://` and `www.` URLs in the text,
  and OSC 8 hyperlinks that programs print. Hold Ctrl (Cmd on macOS) over
  one to see it in the theme's link color, and click to open it in your
  browser, also inside Claude Code and Codex. Turn it off with `links = false` under
  `[terminal]`, or the switch in Settings, Terminal.

## v0.1.0-alpha.2

- Sessions, like tmux and zellij: many named sessions (`swift-otter`) run at
  once in the daemon, each with its own tabs, groups and order, and all of
  them survive restarts and reboots. Rename them any time.
- The session switcher (Ctrl+Shift+S, aide Alt+S, tab mode `s`) lists every
  session with its agents and live working and needs-you counts, beside a
  live view of the highlighted session's sidebar. Type to filter; `n`, `r`
  and `x` create, rename and kill sessions in place.
- The sidebar header and the window title show the current session.
  Ctrl+Shift+] and Ctrl+Shift+[ (aide Alt+] and Alt+[) step through
  sessions; Ctrl+Shift+N makes one.
- Several windows can be open, one per session. `pitwall` opens the last
  session you used, `pitwall -s <name>` a named one.
- `pitwall session ls / new / attach / rename / kill`. Tab commands act on
  the calling pane's session, else the last one used, or `-s <name>`.
- The jump-to-attention key crosses sessions, and notifications name the
  session.
- Saved state moves into a session called `main`. Protocol version 9, store
  format 7.

## v0.1.0-alpha.1

- Versions say alpha: `v0.1.0-alpha.N`.
- `pitwall notify <text>` rings the pane it runs in, e.g.
  `npm test && pitwall notify "tests passed"`.
- A terminal notification's desktop notification shows just its message.
- Windows: the daemon saves its state again. Syncing the state folder after
  the write is not allowed there and failed every save.
- The README opens with a short loop of the app and shows each feature with
  a short clip.

## v0.0.10

- Pane mode, like zellij's: the pane prefix (Ctrl+P in the aide preset,
  unbound in conventional) then `n` new pane, `d` split down, `r` split
  right, `x` close, `h/j/k/l` or arrows to move, `f` fullscreen, `p` or Tab
  next pane. It stays on until Esc or Enter, so keys chain. Ctrl+P twice
  sends Ctrl+P to the shell. Keys live in `[keys.pane]` and the settings page.
- First release with prebuilt Linux, macOS and Windows archives (the v0.0.9
  tag didn't start the release build).

## v0.0.9

- Install without cloning: `curl -fsSL https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.sh | sh`
  on Linux and macOS, `irm https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.ps1 | iex`
  on Windows. Releases carry prebuilt archives and `checksums.txt`.
- macOS and Windows builds (alpha, untested by us so far). Windows gets agent
  status from hooks only; see the README for the gaps.
- A pane that needs you and isn't in view gets a ring in its state color, and
  its sidebar row a stronger tint, accent bar and dot, until you look at it.
  Desktop notifications follow the same rule.
- `jump_attention` goes to the newest tab that needs you (Ctrl+Shift+U, aide
  Alt+U, `u` in tab mode).
- Any program can ask for attention with OSC 9, OSC 777 or kitty's OSC 99.
- MIT license. Protocol version 8.

## v0.0.8

- The tab switcher is hidden for now: no hold key, no switcher shortcuts,
  and the aide preset's Alt+J/K walk every tab across groups. The code stays
  (`config.SwitcherHidden`) for a later return. A `switcher_modifier`,
  `switcher` or `pin_switcher` line in config still loads and does nothing.
- An agent tab's second line drops the "Claude"/"Codex" name; the logo says it.

## v0.0.7

- Agent tabs show the real Claude and OpenAI logos (from Simple Icons)
  instead of drawn stand-ins. The OpenAI mark and the "Codex" label use the
  theme's text color.

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
