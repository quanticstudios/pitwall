# Changelog

pitwall is in alpha. Releases are `v0.1.0-alpha.1`, `v0.1.0-alpha.2`, ...:
each release bumps the alpha number and becomes the latest release on
GitHub, which is what the installers take. The `v0.0.N` tags before that
were the same alpha under patch numbers. A release is a git tag on `main`; `scripts/install.sh` stamps the
binary with `git describe`, and `pitwall --version` prints it.

To cut a release: add a section below and merge it, then
`git tag -a v0.1.0-alpha.N -m "pitwall v0.1.0-alpha.N"` on that commit and
`git push origin v0.1.0-alpha.N` (the tag alone, so the release build runs).

## v0.1.0-alpha.22

- The first group at the top of the sidebar gets the same 6dp above its
  header as the others, so its hover box no longer touches the sidebar's
  header.

## v0.1.0-alpha.21

- The daemon logs each decision to `decisions.jsonl` in the state folder:
  latency, answer, probability, input tokens, and what you did next (how
  you answered a permission prompt and how fast, whether you prompted
  again after a turn check, when you focused a triaged pane). It holds ids
  and numbers, never prompts, commands, paths or screen text.
- `holdout` under `[decisions.approvals]`, 0.5 by default, hides Jev's
  recommendation on that share of permission prompts, at random, so the
  two can be compared. Risk flags always show, alone as "Risk: sudo" when
  the recommendation is hidden or Jev did not answer. `holdout = 0` shows
  every recommendation.
- `pitwall jev report [--days N]` prints calls, failures, latency, an
  estimated cost, answer times with and without the recommendation, how
  often Jev agreed with you, the turn check's follow-up rate and triage's
  time to focus, with a one-line verdict once there is enough data.
  `pitwall jev status` shows how many events the log holds.
- An older daemon logs a newer client's protocol version at most once a
  minute per version, with a count, instead of once per hook.

## v0.1.0-alpha.20

- Alt+1-9 and Ctrl+T then 1-9 count only the tabs the sidebar shows: a
  collapsed group's tabs no longer take a number. Hold Alt for a moment and
  the first nine rows show their number in place of their icon. Rebind
  `goto_tab_1`-`9` to another modifier, Ctrl+1-9 say, and holding that one
  shows them instead.

## v0.1.0-alpha.19

- Hovering a tab shows "…" then "×" in place of its time. The ×
  closes the tab, like the menu's Close and a middle-click on the row.
- An Update button shows at the bottom of the sidebar when GitHub has a
  newer release. A click downloads the archive for your system, checks it
  against the release's `checksums.txt`, and replaces the binary the window
  runs; "Restart to finish" then reopens the window on the new version.
  Release builds on Linux and macOS check when a window opens and every 6
  hours; builds from source and Windows never do. `[updates]` gains `check`
  to turn it off, also under About in the settings page.
- About in the settings page shows a release archive's version instead of
  `dev-` and its commit.

## v0.1.0-alpha.18

- Shift+Enter adds a new line in Claude Code and Codex. Under the kitty
  keyboard protocol, which both turn on, pitwall sent Shift+Enter as a plain
  Enter. Enter, Tab and Backspace with any modifier, Shift included, now get
  their own codes, as in kitty and Ghostty; unmodified they still send the
  bytes a shell expects.

## v0.1.0-alpha.17

- In tab mode, Ctrl+T then N opens a tab outside every group, like the
  sidebar's top "+", and Ctrl+T then G opens one in this tab's group, right
  after it. The global new-tab chord (aide Alt+Shift+T, conventional
  Ctrl+Shift+T) still opens it in the group. `[keys.tab]` gains
  `new_in_group`.

## v0.1.0-alpha.16

- The "+" next to the session name always opens a tab outside every group,
  in the folder of the tab you are on, even when that folder belongs to a
  group. A group's own "+" still opens one inside it.
- Protocol 16: the new `pitwall` restarts an older daemon.

## v0.1.0-alpha.15

- The sidebar's tab rows and group headers sit 8dp in from both edges, so a
  highlighted row no longer runs into the sidebar's right border. Inside a
  row, content is 12dp from each side again.

## v0.1.0-alpha.14

- A Claude Code session started with bypass (`claude
  --dangerously-skip-permissions`, a `cdang` alias) resumes with it, even
  after shift+tab, `/clear` or compaction moved it to another mode. pitwall
  tells agent processes apart by Claude's SessionStart hook: a plain
  `claude` started afterwards, in the same pane or the same script, resumes
  in its own mode. Run `pitwall hooks install` once to add that hook; until
  then the mode follows each report, as before.
- Claude Code's `auto` and `manual` permission modes are kept on resume.

## v0.1.0-alpha.13

- A group header shows "+" and "…" only on hover, so a long group name
  uses the whole row; on hover the name fades out under the two buttons.
  The buttons get a hover highlight of their own.
- A tab row's pill, time and "…" sit 16dp from its right edge, up from 12dp,
  so the pill's filled box no longer crowds the border.
- Every tab row highlights on hover, not only rows without an agent; a
  state's tint (approval, error, plan ready) shows through faintly.

## v0.1.0-alpha.12

- The agent panel opens with Ctrl+Shift+L, so Ctrl+L reaches the shell
  again to clear the screen. A `toggle_panel` set in config.toml still wins.

## v0.1.0-alpha.11

- On Wayland, a window on another workspace no longer hangs and gets
  "Application Not Responding". pitwall carries a patched copy of Gio
  (`third_party/gioui.org`, see its PATCH.md) that presents a frame only
  after the compositor showed the last one.
## v0.1.0-alpha.10

- The sidebar (Ctrl+B) slides in and out with the panes moving along,
  instead of the panes jumping to their new place. The agent panel
  (Ctrl+L) slides in from the right edge the same way, over 200ms. The
  terminals still resize once.

## v0.1.0-alpha.9

The `v0.1.0-alpha.7` and `v0.1.0-alpha.8` tags have no release: the first
build stopped before this section existed, the second never started, and
tags cannot be moved. alpha.9 is the same code.

- An agent side panel on the right, Ctrl+L (`toggle_panel`, rebindable).
  It follows the focused pane and reads the agent's own session file:
  Claude Code's transcript and its subagents, Codex's rollout, or pi's
  session. Its tabs:
  - Flow: the current turn as a graph from prompt to outcome, with a
    pending approval and Jev's suggestion, then tool calls, errors, turn
    time, changes and earlier turns.
  - Subagents: Active and Done. Opening one shows what it was asked, its
    latest message or result, and its tool calls.
  - Plan: Claude's todo list or Task tools, or Codex's `update_plan`.
  - Changes: files changed from the default branch, with +/-.
  - Timeline: prompts, tool calls, subagents and suggestions with their times.

  Claude gets every tab, and Codex gets Plan and Subagents when it uses them.
  pi has neither. The panel reads the files only while it is open, keeps
  their text in memory, and never logs or saves it. Ctrl+L no longer reaches
  the terminal; set `toggle_panel = []` to give it back.
- Run `pitwall hooks install` once: the pi extension now reports its session
  file.
- Branch +/- in the sidebar comes from one diff from the merge base to the
  work tree. A line changed both in a commit and in uncommitted work now
  counts once, so the numbers can drop slightly.
- Protocol 15: the new `pitwall` restarts an older daemon.

## v0.1.0-alpha.6

- pitwall logs. Windows write events to `gui.log` and the daemon to
  `daemon.log` in the state directory: starts, connections, panes starting
  and exiting, sizes, errors, slow requests, slow frames and stalls, one line
  each, never pane contents, typed input, prompts, hook payloads or command
  arguments. Go's standard crash traces go to `crash.log` instead: the panic
  value and the crashing goroutine's stack, verbatim. A file over 5 MB at startup moves to `.1`.
  `pitwall logs` prints the three paths and `pitwall logs -f` follows them;
  README's Troubleshooting says what to attach to a bug.
- A pane whose program printed nothing after a resize kept showing its old
  size, 80x24 for a new pane whose size arrived late, as it can behind a burst
  of typing. A resize now redraws the pane. A size the window failed to send
  is sent again.
- A command tab from `pitwall new -- <cmd>` survives a daemon restart. A
  command that had exited keeps its exit code, and an agent with a known
  session resumes and stays. Any other command that was still running is
  not run again, because that may not be safe: it comes back exited with
  its exit code unknown (`exit_code` null in `pitwall ls --json`, and
  `pitwall wait` prints `exit unknown` and exits 3). The output is not
  saved, so the pane shows a one-line notice instead. Command tabs saved by
  alpha.5 count as such tabs: a pane with a command and no agent session to
  resume is not run again after the upgrade.
- A `pi -p` tab opened with `pitwall new -- pi -p ...` ends showing Done or
  Error. pi's extension used to drop its queued reports at shutdown, the
  result included. Now it drops them and sends one final shutdown report
  that carries the run's result, from a process that outlives pi, so even a
  slow hook delivers it after pi exits. pitwall takes that report as the
  result, then the shutdown. In a command tab, the shutdown and the exit
  after it keep the Done or Error; elsewhere a pi shutdown clears the tab's
  status as before. Once a pi runtime has shut down, pitwall ignores any
  report it sends later. pi also exits up to a second sooner: the extension
  no longer holds it open for its whole shutdown deadline. Run
  `pitwall hooks install` to update the extension. It replaces the alpha.5
  file, which is not counted as edited.
- A resumed Claude pane no longer sends its first prompt again. A pane
  opened as `claude "fix the tests"` came back as
  `claude "fix the tests" --resume <id>`. Resume now keeps only the options
  `claude --help` documents, minus session selectors, print mode and one-off
  actions such as `--worktree`. A value option left without its value, such
  as a trailing `--model`, is dropped too, since it took the `--resume`.
- The daemon protocol is now version 14; the next `pitwall` restarts an
  older daemon. The state file format is now version 8, so an older pitwall
  can't read state this one saved.
- Tab pills say "Working", "Approval" or "Done" without the "Agent " prefix,
  so long titles keep more of their text. `pitwall ls` and desktop
  notifications use the same short labels.
- A tab row's content ends 12dp from the right edge, the same as on the left.
  The "…" menu button shows on hover at the right end of the second line, in
  place of the time, and the pill stays where it is.
- pi's mark is drawn smaller, so it carries the same weight as the Claude and
  Codex marks next to it.
- A window no longer stops taking state from the daemon when `pitwall
  attach` requests pile up faster than it raises itself: the newest request
  replaces an unread one. Killing a session logs the kill and the panes it
  closes in `daemon.log`, and each window's reaction in `gui.log`: the
  session it moved to, or that it closed because nothing was left to show.
  A pane that takes over 3s to close is logged too.

## v0.1.0-alpha.5

- Agents and scripts can drive tabs (issue #2). `pitwall new ... -- <cmd>`
  opens a tab running a command instead of a shell; its pane stays after the
  command exits, showing its output, until you close it or the daemon
  restarts. `pitwall new -d -- codex "<prompt>"` starts an agent on a task.
  `pitwall wait <tab> --until done|idle|blocked|exit` blocks until the
  agent gets there, with exit codes a script can branch on.
  [docs/agent-skill.md](docs/agent-skill.md) documents them for agents.
- **Breaking:** `pitwall ls --json` prints one documented object per tab
  (`n`, `id`, `title`, `group`, `cwd`, `branch`, `agent`, `state`,
  `question`, `exit_code`, `panes`, `detached`) instead of pitwall's internal
  structs, and `[]` when no daemon runs. Tabs can also be named by that `id`.
- A resumed agent keeps the permission mode its hooks last reported. Claude
  comes back with `--dangerously-skip-permissions` for bypassPermissions and
  `--permission-mode <mode>` for its other modes, default included, so a
  bypass set as the default in settings stays off once you left it; Codex comes
  back with `--dangerously-bypass-approvals-and-sandbox` for
  bypassPermissions. A pane whose command already sets permissions keeps its
  own flag. Other flags of an agent started in a shell, such as `--model`,
  are not restored.
- A restored agent in a pane that was a shell drops to a shell in the same
  folder when it exits, instead of closing the tab. A pane opened with a
  command closes as before, except a failed resume within 3 seconds of the
  restart, which also gets a shell.
- The daemon protocol is now version 13; the next `pitwall` restarts an
  older daemon. The state file format is unchanged.

## v0.1.0-alpha.4

- The window no longer freezes while the daemon is busy with a slow request,
  such as a new tab in a repository where `git` is slow. Messages to the
  daemon queue in order instead of blocking the window, and none are dropped
  while the connection is up; a newer resize of a pane replaces its queued
  one.
- When one window event runs for over 2 seconds, pitwall writes every
  goroutine's stack to `stall-<time>.txt` in its state directory and keeps the
  newest 5. Attach the newest one to a report of a frozen window.
- Ctrl+Backspace deletes the word before the cursor. It sends Ctrl+W, the
  delete-word key in bash, zsh, fish, Claude Code and Codex.
- Hovering a tab for half a second opens a card beside the sidebar with its
  full title, group and folder, branch and diff stats, the agent's state and
  question, and the pane count. It follows the pointer to other tabs at once
  and hides on click, scroll, drag or a key press.
- Tabs inside a group are indented under the group header.
- The "+" on a hovered tab row is gone; it covered the state pill. Right-click
  a tab for **New tab below**, or use the "+" on the group or sidebar header.
- Decision models, starting with TypeSafe's Jev. Connect it in Settings,
  Decisions, or with `pitwall jev login`; the key stays in a 0600
  `credentials` file or `TYPESAFE_API_KEY`, never in `config.toml`.
  Nothing is sent until you connect, and connecting keeps your settings.
  With the other `[decisions]` settings left at their defaults:
  - Approvals suggest only: a Claude Code or Codex permission request
    shows the model's recommendation on the tab's pill, in the switcher,
    in the hover card and on the pane, with any risk pitwall reads in the
    call ("Jev: allow 96% · sudo"). The agent's prompt is never delayed
    and pitwall never answers it. Automatic approval was left out because
    a command's text can't show what it will run, so it needs sandboxed
    execution.
  - Attention triage (on by default) sorts what needs you as fyi, later,
    soon or now for the jump-to-attention key and notifications; fyi sends
    no notification.
  - Optional: status for agent CLIs without hooks (Gemini CLI, OpenCode,
    Aider, Amp, Cursor agent, Goose, Crush) read from their screen, and a
    turn check that turns Done into Check when a turn needs review; it
    reads the agent's last message only, never the screen.
  - `provider = "command"` runs your own classifier instead. Every call
    has secrets removed before anything is cut to size, a timeout (1.5 s
    by default, 0.2 to 10 s) and a per-pane limit; any failure leaves
    pitwall as it was. See the README's Decisions section.
- A finished turn's notification shows the start of the agent's last
  message.
- The daemon protocol is now version 11; the next `pitwall` restarts an
  older daemon. The state file format is unchanged.
- pitwall supports the [pi](https://pi.dev) coding agent. A tab running `pi`
  shows pi's logo and its state, and resumes with `pi --session <id>` after
  a reboot. `pitwall hooks install` writes a pi extension,
  `~/.pi/agent/extensions/pitwall.ts`, that reports working, done, error and
  the tool in use; without it pitwall reads pi's state from the screen.

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
