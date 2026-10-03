# pitwall
<img src="packaging/pitwall.svg" alt="pitwall logo: a P whose stem is three status lights and whose bowl is a terminal pane" width="96" height="96">

pitwall is a terminal multiplexer for running coding agents side by side. It
opens straight into a shell like tmux, but it is a native window: a sidebar
lists every session and what it is doing right now, whether that is a command
running in a terminal or a Claude Code or Codex agent working, waiting for
your answer, asking for approval, done, or failed. Terminals are drawn with
real fonts and pixels, not character cells.

A background daemon owns every terminal. Closing the window leaves sessions
running, and after a reboot they come back in the same folders with agents
resumed where they left off.

pitwall runs on Linux. It is developed on Hyprland and works on any Wayland or
X11 desktop.

## Install

You need Git, a C compiler, pkg-config, and [mise](https://mise.jdx.dev). Gio,
the UI toolkit, needs the development headers for EGL, Wayland, X11,
xkbcommon, Xcursor and Xfixes (`egl`, `wayland-egl`, `wayland-client`,
`wayland-cursor`, `x11`, `x11-xcb`, `xkbcommon`, `xkbcommon-x11`, `xcursor`,
`xfixes` in pkg-config).

```sh
git clone git@github.com:quanticstudios/pitwall.git
cd pitwall
mise install go
./scripts/install.sh
```

The installer builds `~/.local/bin/pitwall` and adds a desktop entry and icon
under `~/.local/share`, so pitwall shows up in your app launcher. Set `PREFIX`
to install elsewhere. It never edits Claude Code or Codex configuration.

Check the install:

```sh
pitwall --version
```

## Quick start

```sh
pitwall hooks install --dry-run   # see what would change
pitwall hooks install             # let Claude Code and Codex report their state
pitwall                           # open the window
```

The window opens on a shell in the folder you launched it from. Run `claude`,
`codex`, a dev server, anything. The session's row in the sidebar shows what
is happening: the name of a running command, or the agent's state.

Open more sessions with **+** in the sidebar header. Each session can hold
tabs, and each tab can be split into panes. Typing `exit` closes a pane; an
empty tab closes, an empty session closes, and the window closes with your
last session. Sessions you detached keep running in the background.

## Concepts

- **Session**: one working context, started in a folder. It gets a memorable
  generated name such as `swift-otter`, which you can rename.
- **Tab**: a set of split panes inside a session. A tab's title follows the
  agent running in it, or you can name it.
- **Pane**: one terminal.
- **Group**: sessions you put together after the fact, for example all the
  sessions working on one repo. Sessions start ungrouped.
- **Daemon**: the background process that owns the terminals. The window and
  the `pitwall` commands talk to it.

## Everyday use

### Watching agents

Each sidebar row shows a session's state:

| State    | Meaning                                           |
| -------- | ------------------------------------------------- |
| Working  | The agent is in a turn                            |
| Input    | The agent asked you a question                    |
| Approval | The agent wants permission to run a tool          |
| Plan     | The agent finished a plan and waits for approval  |
| Done     | The agent finished its turn                       |
| Error    | The turn failed                                   |
| `go`     | A terminal is running that command                |

A group header shows how many of its sessions need you. When a session needs
you and you are not looking at it, pitwall sends a desktop notification.

States are exact when the agent's hooks are installed (`pitwall hooks
install`). Without hooks, pitwall still recognizes `claude` and `codex`
running in a pane and reads their state from the screen, which is a little
less precise.

### Sessions, tabs and panes

The keyboard shortcuts are in [Keybindings](#keybindings). With the mouse:
click a session to switch to it, right-click it for **Rename**, **Detach**,
grouping, and more. Tabs appear above the panes once a session has more than
one: click to switch, double-click to rename, middle-click to close. Drag the
gaps between panes to resize them.

### Grouping

- **By folder:** right-click a session and choose **Group sessions in
  `<folder>`**. Every ungrouped session in that repo or folder joins one
  group, and new sessions you start inside that folder join it automatically.
- **By hand:** Ctrl+click or Shift+click to pick sessions, then right-click
  and choose **New group** or **Move to group**.
- **Ungroup** or **Remove from group** never close anything.

For a Git repo group, **New worktree session** in the group menu starts a
session in a fresh worktree under `<repo>/.worktrees/`, so parallel agents on
one repo do not step on each other. Deleting that session removes the
worktree it made. pitwall never deletes a folder it did not create.

### Detaching

**Detach** (right-click a session, or `pitwall detach`) hides a session and
keeps everything in it running. The **Detached sessions** list in the sidebar
footer brings it back, as does `pitwall attach <name>`.

### Let agents name their tab

Claude Code and Codex set a terminal title, which pitwall shows on the tab
with spinners stripped. An agent can also name its tab explicitly:

```sh
pitwall tab rename "fix login redirects"
```

To have agents do this on their own, add to your `CLAUDE.md` or `AGENTS.md`:

```markdown
At the start of a task inside a pitwall pane, run
`pitwall tab rename "<short description of the task>"`.
```

## Command line

Run these from any terminal. Inside a pitwall pane, commands that take an
optional name act on the pane's own session.

| Command                               | Does                                                          |
| ------------------------------------- | ------------------------------------------------------------- |
| `pitwall`                             | Open the window (starts the daemon if needed)                 |
| `pitwall ls [--json]`                 | List sessions: name, state, tabs, folder, group               |
| `pitwall new [-n name] [-d] [dir]`    | Create a session; `-d` leaves it detached                     |
| `pitwall attach [name]`               | Show a session in the window, opening the window if needed    |
| `pitwall detach [name]`               | Hide a session; its processes keep running                    |
| `pitwall rename [old] <new>`          | Rename a session                                              |
| `pitwall kill [-f] <name>`            | Close a session and its processes; files are never touched    |
| `pitwall tab new`                     | Open a tab in this pane's session                             |
| `pitwall tab rename [name...]`        | Name this pane's tab; no name goes back to the automatic one  |
| `pitwall tab close`                   | Close this pane's tab                                         |
| `pitwall hooks install` / `uninstall` | Add or remove agent hooks (`--dry-run` to preview)            |
| `pitwall --version`                   | Print the version                                             |

Names match exactly first, then by a unique prefix, so `pitwall attach swi`
finds `swift-otter`.

```sh
pitwall new -n auth -d ~/src/service   # start a background session
pitwall ls
pitwall attach auth
```

## Keybindings

| Keys                              | Action                                |
| --------------------------------- | ------------------------------------- |
| Alt+J / Alt+K                     | Next / previous session in the group  |
| Alt+H / Alt+L                     | Previous / next pane                  |
| Alt+Arrows                        | Same as J / K / H / L                 |
| Hold Alt                          | Show the session switcher             |
| Alt+Space                         | Pin the switcher open                 |
| Alt+1-9                           | Jump to session                       |
| Alt+N                             | Split the pane to the right           |
| Alt+Shift+N                       | Split the pane below                  |
| Alt+Shift+W                       | Close the pane                        |
| Alt+Shift+T                       | New session in this folder            |
| Ctrl+T then n                     | New tab                               |
| Ctrl+T then x                     | Close the tab                         |
| Ctrl+T then r                     | Rename the tab                        |
| Ctrl+T then h / l or Left / Right | Previous / next tab                   |
| Ctrl+T then 1-9                   | Jump to tab                           |
| Ctrl+T twice                      | Send Ctrl+T to the terminal           |
| Ctrl+Shift+C / Ctrl+Shift+V       | Copy selection / paste                |
| Shift+PageUp / Shift+PageDown     | Scroll back / forward one page        |
| Escape                            | Close the switcher or a dialog        |

The settings button in the sidebar footer shows the bindings in effect.

## Hooks

Hooks are how Claude Code and Codex tell pitwall exactly what they are doing.
`pitwall hooks install` merges pitwall's entries into
`~/.claude/settings.json` and `~/.codex/hooks.json`:

- It keeps every existing setting and hook and never adds a duplicate.
- It backs each file up first as `<file>.pitwall-backup-<unix time>` and
  writes atomically. A symlinked config stays a symlink.
- Add `--dry-run` to print the result without writing.

Inside Codex, run `/hooks` once to trust the new hooks, and restart agent
sessions that were already running. `pitwall hooks uninstall` removes only
the exact entries pitwall added. `pitwall hooks` prints the blocks if you
prefer to edit the files yourself.

The hooks do nothing outside a pitwall pane, so they are safe to keep
installed globally.

## Where things live

| What                | Where                                                         |
| ------------------- | ------------------------------------------------------------- |
| Saved sessions      | `~/.local/state/pitwall/state.json` (`$XDG_STATE_HOME`)       |
| Daemon log          | `~/.local/state/pitwall/daemon.log`                           |
| Socket              | `$XDG_RUNTIME_DIR/pitwall/pitwall.sock`                       |

After a reboot, run `pitwall`: sessions, tabs, groups and panes come back in
their folders. Agent panes resume with `claude --resume <id>` or
`codex resume <id>`; if a resume fails, the pane falls back to a shell in the
same folder. Running processes and scrollback do not survive a reboot.

When you upgrade pitwall while an older daemon is running, the next `pitwall`
detects it, has it save its state and stop, and starts the new one. Programs
running in panes at that moment stop.

## Troubleshooting

- **The window does not open from the launcher.** Errors are sent as a
  desktop notification; run `pitwall` in a terminal to see them.
- **An agent's state is missing or late.** Check `pitwall hooks install` was
  run with the installed binary, and in Codex run `/hooks` once.
- **Something else.** Look at `~/.local/state/pitwall/daemon.log`, which
  starts with the daemon's version.

## Development

`mise.toml` sets `GOFLAGS=-tags=novulkan` so Gio builds with OpenGL and no
Vulkan headers. Use mise so every command gets it:

```sh
mise exec -- go build ./cmd/pitwall
mise exec -- go vet ./...
mise exec -- go test -race ./...
```

Releases use patch versioning; see [CHANGELOG.md](CHANGELOG.md).

## Credits

pitwall stands on other people's open source work. The full list, with every
license, is in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). In short:

- [tuios](https://github.com/Gaurav-Gosain/tuios) (MIT, Gaurav Gosain):
  pitwall adapted its PTY spawning, agent detection, resume commands and
  screen patterns, and its test fixtures.
- [charmbracelet/x/vt](https://github.com/charmbracelet/x) (MIT,
  Charmbracelet): the terminal emulator, vendored with a one-line patch.
- [Gio](https://gioui.org) (MIT / Unlicense): the UI toolkit.
- [creack/pty](https://github.com/creack/pty) (MIT): pseudo-terminals.
- [go-text/typesetting](https://github.com/go-text/typesetting) (BSD / Unlicense): text shaping.
- [Lucide](https://lucide.dev) (ISC) and Feather (MIT): the icons.
- [Geist](https://vercel.com/font) (OFL 1.1): the UI font.
- aide (Quantic Studios): the sidebar design and agent states pitwall ports.
- [zj-radar](https://github.com/marktoda/zj-radar), [zellij](https://zellij.dev),
  [tmux](https://github.com/tmux/tmux) and [Ghostty](https://ghostty.org):
  ideas for hook handling, tab mode, session naming, detach and keymaps.
