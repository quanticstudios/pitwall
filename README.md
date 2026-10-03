# pitwall
<img src="packaging/pitwall.svg" alt="pitwall logo: a P whose stem is three status lights and whose bowl is a terminal pane" width="96" height="96">

pitwall is a terminal multiplexer for running coding agents side by side. It
opens straight into a shell like tmux, but it is a native window: a sidebar
lists every tab and what it is doing right now, whether that is a command
running in a terminal or a Claude Code or Codex agent working, waiting for
your answer, asking for approval, done, or failed. Terminals are drawn with
real fonts and pixels, not character cells.

A background daemon owns every terminal. Closing the window leaves tabs
running, and after a reboot they come back in the same folders with agents
resumed where they left off.

pitwall is developed on Linux (Hyprland) and works on any Wayland or X11
desktop. macOS and Windows builds are new; see the notes below.

## Install

On Linux or macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.sh | sh
```

On Windows, in PowerShell:

```powershell
irm https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.ps1 | iex
```

The script downloads the latest release for your system, checks it against
the release's `checksums.txt`, and installs `pitwall` in `~/.local/bin`. On
Windows it installs `pitwall.exe` in `%LOCALAPPDATA%\pitwall\bin` and adds that
folder to your user PATH. To pin a release, set `PITWALL_VERSION=v0.0.9`; to
install somewhere else, set `PITWALL_INSTALL_DIR`. In PowerShell, set them
first with `$env:PITWALL_VERSION = 'v0.0.9'`.

| Platform                       | Release builds | Status                     |
| ------------------------------ | -------------- | -------------------------- |
| Linux (glibc 2.35 or newer)    | x86_64, arm64  | alpha, used every day      |
| macOS                          | arm64, x86_64  | alpha, new and not yet run |
| Windows 10 1809 or newer, 11   | x86_64, arm64  | alpha, new and not yet run |

The macOS and Windows builds compile and pass the platform-independent tests
in CI, but nobody has used them yet. Expect rough edges, and please report
what breaks.

Known gaps on macOS:

- Release binaries are not signed. `get.sh` clears the quarantine flag; for an
  archive you downloaded yourself, run `xattr -d com.apple.quarantine pitwall`.
- There is no app bundle or Dock icon yet. Start pitwall from a terminal.
- Notifications come through `osascript`, so macOS shows them under Script
  Editor.

Known gaps on Windows:

- Agent status comes from hooks only. Windows has no foreground process group
  to read, so an agent started without hooks, or a command running in a
  shell, shows nothing in the sidebar.
- A tab's folder does not follow `cd`. It stays the folder the tab opened in.
- There is no Start menu entry. Run `pitwall` from a terminal; started from
  Explorer, a console window flashes before the window opens.
- Claude Code runs hook commands through Git Bash. Other shells get a path
  with forward slashes, quoted only when it contains spaces.
- When an upgrade replaces a running daemon, the old daemon is stopped without
  a final save. It saves within moments of every change, so little is lost.

The Linux release archive holds only the binary. For a desktop entry and
icon, build from source as below.

### Build from source

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
`codex`, a dev server, anything. The tab's row in the sidebar shows what is
happening: the name of a running command, or the agent's state.

Open more tabs with **+** in the sidebar header. Each tab can be split into
panes. Typing `exit` closes a pane; an empty tab closes, and the window closes
with your last tab. Tabs you detached keep running in the background.

## Concepts

- **Tab**: one working context, a set of split panes started in a folder. Its
  title follows the work: the agent's topic or first prompt, the running
  command, or the folder the shell is in now (`~` for home). Naming a tab
  replaces the title. The command line also takes its number in `pitwall ls`.
- **Pane**: one terminal.
- **Group**: tabs you put together after the fact, for example all the tabs
  working on one repo. Tabs start ungrouped.
- **Daemon**: the background process that owns the terminals. The window and
  the `pitwall` commands talk to it.

## Everyday use

### Watching agents

Each sidebar row shows a tab's state:

| State    | Meaning                                           |
| -------- | ------------------------------------------------- |
| Working  | The agent is in a turn                            |
| Input    | The agent asked you a question                    |
| Approval | The agent wants permission to run a tool          |
| Plan     | The agent finished a plan and waits for approval  |
| Done     | The agent finished its turn                       |
| Error    | The turn failed                                   |
| `go`     | A terminal is running that command                |

A tab running Claude or Codex always shows it, idle or busy: the agent's logo
replaces the row icon. The logo goes when the agent exits back to the shell.

When an agent needs you (a question, an approval, a plan, an error, a
finished turn) in a pane you are not looking at, that pane gets a ring in the
state's color and its sidebar row gets an accent bar and a dot. Not looking
means another tab, another pane of the same tab, or the window in the
background. The mark stays until you focus that pane, and a group header
counts its tabs that carry one. pitwall also sends a desktop notification
for it. Ctrl+Shift+U (Alt+U in the aide preset) jumps to the pane that most
recently started waiting; press it again for the next one. Once you have
seen them all, it walks the waiting panes by priority.

Any program can ask for your attention with an OSC notification, no hooks
needed: OSC 9 (`printf '\e]9;tests passed\a'`, iTerm2's form), OSC 777
(`\e]777;notify;title;body\a`, the form urxvt, foot and Ghostty read) and
kitty's OSC 99 in its single-chunk form. The pane then shows as Input with
the message until you focus it or the agent's state changes. ConEmu's
numeric OSC 9 forms, such as `9;4` progress, are ignored.

States are exact when the agent's hooks are installed (`pitwall hooks
install`). Without hooks, pitwall still recognizes `claude` and `codex`
running in a pane and reads their state from the screen, which is a little
less precise.

### Tabs and panes

The keyboard shortcuts are in [Keybindings](#keybindings). With the mouse:
click a tab to switch to it, double-click it to rename it, middle-click to
close it, right-click it for **Rename tab**, **Detach tab**, **Close tab**,
grouping, and more. The "+" in the sidebar header, on a group header, or on a
hovered tab opens a new one; it opens right below the one you were in, in the
same group and folder.

Drag a tab or a group header to reorder it: the other rows slide apart to
show where it will land, and Escape puts it back. Groups and ungrouped tabs
share one order, so a group can sit above loose tabs and a loose tab between
two groups: drop on the top half of a group header to land above the group,
on the bottom half to land first inside it. Rest on a group header for
a moment, or drop on a collapsed one, to move the tab to the end of that
group. Ctrl+click or Shift+click several tabs to drag them together. Drag the
gaps between panes to resize them.
Ctrl+Shift+B (Ctrl+B in the aide preset) hides the sidebar; pitwall
remembers that across restarts.

### Grouping

- **By folder:** right-click a tab and choose **Group tabs in `<folder>`**.
  Every ungrouped tab in that repo or folder joins one group, and new tabs you
  start inside that folder join it automatically.
- **By hand:** Ctrl+click or Shift+click to pick tabs, then right-click
  and choose **New group** or **Move to group**.
- **Ungroup** or **Remove from group** never close anything.

For a Git repo group, **New worktree tab** in the group menu starts a tab in a
fresh worktree under `<repo>/.worktrees/`, so parallel agents on one repo do
not step on each other. Deleting that tab removes the worktree it made. pitwall never deletes a folder it did not create.

### Detaching

**Detach** (right-click a tab, or `pitwall detach`) hides a tab and keeps
everything in it running. The **Detached** list in the sidebar footer brings
it back, as does `pitwall attach <name>`.

### Let agents name their tab

Claude Code and Codex set a terminal title, which pitwall shows as the tab's
title with spinners stripped. An agent can also name its tab explicitly:

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
optional name act on the pane's own tab. Older pitwall versions called tabs
sessions; the commands and flags are the same.

| Command                               | Does                                                          |
| ------------------------------------- | ------------------------------------------------------------- |
| `pitwall`                             | Open the window (starts the daemon if needed)                 |
| `pitwall ls [--json]`                 | List tabs in sidebar order: #, name, state, folder, group     |
| `pitwall new [-n name] [-d] [dir]`    | Open a tab and print its #; `-d` leaves it detached           |
| `pitwall attach [name]`               | Show a tab in the window, opening the window if needed        |
| `pitwall detach [name]`               | Hide a tab; its processes keep running                        |
| `pitwall rename [old] <new>`          | Rename a tab                                                  |
| `pitwall kill [-f] <name>`            | Close a tab and its processes; files are never touched        |
| `pitwall tab new`                     | Open a tab next to this pane's tab, in its folder             |
| `pitwall tab rename [name...]`        | Name this pane's tab; no name goes back to the automatic one  |
| `pitwall tab close`                   | Close this pane's tab                                         |
| `pitwall hooks install` / `uninstall` | Add or remove agent hooks (`--dry-run` to preview)            |
| `pitwall --version`                   | Print the version                                             |

A name is a tab's `#` from `pitwall ls` (`3` or `#3`), else its title. A
title matches exactly first, then by a unique prefix, so
`pitwall attach fix` finds the tab titled `fix login redirects`. Detached
tabs are numbered after the ones the sidebar shows.

```sh
pitwall new -n auth -d ~/src/service   # start a background tab
pitwall ls
pitwall attach auth
```

## Keybindings

Two presets ship. **conventional** is the default and follows Linux terminal
defaults (Ghostty, kitty, GNOME Terminal); it leaves plain Ctrl+letters and
readline's Alt+B/F/D/. to the shell. **aide** is the Alt-key layout pitwall
started with. Pick one with `preset` in [config.toml](#configuration) and
override single actions there. The settings button in the sidebar footer
shows the bindings in effect.

conventional:

| Keys                                    | Action                                                    |
| --------------------------------------- | --------------------------------------------------------- |
| Ctrl+Shift+T                            | New tab below this one, in its folder                     |
| Ctrl+Shift+W                            | Close the pane (the tab with its last one)                |
| Ctrl+Tab / Ctrl+Shift+Tab               | Next / previous tab, across groups                        |
| Ctrl+PageDown / Ctrl+PageUp             | Next / previous tab, across groups                        |
| Ctrl+Shift+PageDown / Ctrl+Shift+PageUp | First tab of the next / previous group                    |
| Alt+1-9                                 | Go to the Nth tab in the sidebar                          |
| Ctrl+Shift+O                            | Split the pane to the right                               |
| Ctrl+Shift+E                            | Split the pane below                                      |
| Ctrl+Alt+Right/Down, Ctrl+Alt+Left/Up   | Next / previous pane                                      |
| Ctrl+Shift+B                            | Show or hide the sidebar                                  |
| Ctrl+Shift+U                            | Go to the tab that needs you, newest first                |
| Ctrl+Shift+C / Ctrl+Shift+V             | Copy selection / paste                                    |
| Shift+PageUp / Shift+PageDown           | Scroll back / forward one page                            |
| Escape                                  | Close a dialog or settings, cancel a drag                 |

aide:

| Keys                              | Action                                         |
| --------------------------------- | ---------------------------------------------- |
| Alt+J / Alt+K                     | Next / previous tab, across groups             |
| Alt+H / Alt+L                     | Previous / next pane                           |
| Alt+Arrows                        | Same as J / K / H / L                          |
| Alt+1-9                           | Go to the Nth tab in the sidebar               |
| Alt+Shift+T                       | New tab below this one, in its folder          |
| Alt+N                             | Split the pane to the right                    |
| Alt+Shift+N                       | Split the pane below                           |
| Alt+Shift+W                       | Close the pane                                 |
| Ctrl+B                            | Show or hide the sidebar (the shell no longer gets Ctrl+B) |
| Alt+U                             | Go to the tab that needs you, newest first     |
| Ctrl+T then n                     | New tab                                        |
| Ctrl+T then x                     | Close the tab                                  |
| Ctrl+T then r                     | Rename the tab                                 |
| Ctrl+T then h / l or Left / Right | Previous / next tab in the group               |
| Ctrl+T then 1-9                   | Go to the Nth tab                              |
| Ctrl+T then u                     | Go to the tab that needs you, newest first     |
| Ctrl+T twice                      | Send Ctrl+T to the terminal                    |
| Ctrl+Shift+C / Ctrl+Shift+V       | Copy selection / paste                         |
| Shift+PageUp / Shift+PageDown     | Scroll back / forward one page                 |
| Escape                            | Close a dialog or settings, cancel a drag      |

Every action, with its config name, is listed by `pitwall config default`.
The session-era names `next_session`, `prev_session`, `new_session` and
`jump_session_1`-`9` still work as `next_tab`, `prev_tab`, `new_tab` and
`goto_tab_1`-`9`; `pitwall config check` notes each one to rename.

## Configuration

pitwall reads `~/.config/pitwall/config.toml` (`$XDG_CONFIG_HOME`). Without
the file everything has its default. An open window rereads the file within
a second of a change and applies keys, theme, fonts and spacing at once.
Mistakes show as a desktop notification; the window keeps running, and only
the broken entries fall back to their defaults.

| Command                  | Does                                                          |
| ------------------------ | ------------------------------------------------------------- |
| `pitwall config init`    | Write a commented config listing every option, and its schema |
| `pitwall config check`   | Print problems as `config.toml:LINE: message`; exit 1 if any  |
| `pitwall config default` | Print the commented config                                    |
| `pitwall config path`    | Print the config file's path                                  |
| `pitwall config schema`  | Print the JSON Schema (`schema theme` for theme files)        |

The file `init` writes starts with `#:schema ~/.config/pitwall/schema.json`,
so editors with taplo or Even Better TOML complete action names and flag a
bad chord, color or key as you type. The window refreshes the schema files
when a new pitwall knows more keys.

```toml
[keys]
preset = "conventional"
new_tab = ["Ctrl+Shift+T", "Super+T"]  # a chord or a list of chords
toggle_sidebar = "Ctrl+B"
tab_prefix = []                         # [] unbinds

[keys.tab]                              # tab mode, after tab_prefix
rename = "F2"

[theme]
name = "tokyo-night"

[theme.colors]
primary = "#ff9e64"

[font]
mono_family = "Iosevka"
mono_size = 14
line_height = 1.1
mono_fallback = ["Noto Sans Mono CJK SC"]

[layout]
pane_gap = 4
pane_margin = 4
```

Chords are modifiers (`Ctrl`, `Alt`, `Shift`, `Super`) and a key joined by
`+`, in any case. Keys are a printable character, `Space`, `Tab`, `Enter`,
`Esc`, `Backspace`, `Delete`, `Home`, `End`, `PageUp`, `PageDown`, `Up`,
`Down`, `Left`, `Right` or `F1`-`F12`. Two actions on one chord is an error
naming both.

### Themes

Built in: `aide-dark` (the default), `aide-light`, `tokyo-night`,
`catppuccin-mocha`. A custom theme is `~/.config/pitwall/themes/<name>.toml`
with the same keys as `[theme]`; its `name` picks the built-in it starts
from, so it only lists what differs. Add `#:schema
~/.config/pitwall/theme.schema.json` as its first line for completion.

| `[theme.colors]`    | Used for                                    |
| ------------------- | ------------------------------------------- |
| `bg`                | Window background                           |
| `sidebar`           | Sidebar background                          |
| `surface`           | Pane canvas, dialogs, cards                 |
| `surface_secondary` | Selected rows, active tab, fields           |
| `surface_elevated`  | Hovered and floating surfaces, badges       |
| `border`            | Hairlines                                   |
| `fg`                | Text                                        |
| `muted`             | Secondary text                              |
| `primary`           | Accent: buttons, focus, the tab-mode chip   |
| `on_primary`        | Text on primary buttons                     |
| `red`               | Errors                                      |
| `yellow`            | Waiting for you                             |
| `green`             | Done, idle                                  |
| `blue`              | Working                                     |
| `purple`            | Plan ready                                  |

`[theme.terminal]` has `foreground`, `background`, `cursor` and `ansi`, an
array of the 16 ANSI colors (black, red, green, yellow, blue, magenta, cyan,
white, then the bright ones). Colors are `#rrggbb` or `#rrggbbaa`. The
daemon answers programs' color queries (OSC 10, 11 and 4) from the terminal
colors; a running daemon picks a change up the next time it starts.

### Fonts and spacing

`[font]` takes `ui_family` (default the bundled Geist), `ui_size` (13),
`mono_family` (`JetBrainsMono Nerd Font`), `mono_size` (13), `line_height`
(a multiple of the font's, 1.0) and `mono_fallback`, families tried for
characters the terminal font lacks before any monospace font and color
emoji. Families are any installed font (`fc-list : family`); a missing one is
reported and the default is used. `[layout]` sets `pane_gap` and
`pane_margin` in dp (both 4).

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
| Saved tabs          | `~/.local/state/pitwall/state.json` (`$XDG_STATE_HOME`)       |
| Daemon log          | `~/.local/state/pitwall/daemon.log`                           |
| Socket              | `$XDG_RUNTIME_DIR/pitwall/pitwall.sock`, else `/tmp/pitwall-<uid>/` |
| Config and themes   | `~/.config/pitwall/` (`$XDG_CONFIG_HOME`)                     |
| Window state        | `~/.local/state/pitwall/gui.json` (sidebar shown or hidden)   |

macOS uses the same paths. On Windows, config and themes live in
`%APPDATA%\pitwall`, and saved tabs, the daemon log, window state and the
socket in `%LOCALAPPDATA%\pitwall`. The `XDG_*` variables win when set.

After a reboot, run `pitwall`: tabs, groups and panes come back in their
folders. State saved by an older version opens with each of its nested tabs
as a tab of its own, in the same place and group. Agent panes resume with `claude --resume <id>` or
`codex resume <id>`; if a resume fails, the pane falls back to a shell in the
same folder. Running processes and scrollback do not survive a reboot.

When you upgrade pitwall while an older daemon is running, the next `pitwall`
detects it, has it save its state and stop, and starts the new one. Programs
running in panes at that moment stop.

## Troubleshooting

- **The window does not open from the launcher.** Errors are sent as a
  desktop notification (an alert on macOS, a message box on Windows); run
  `pitwall` in a terminal to see them.
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
- [BurntSushi/toml](https://github.com/BurntSushi/toml) (MIT): the config parser.
- [Tokyo Night](https://github.com/folke/tokyonight.nvim), [Catppuccin](https://github.com/catppuccin/palette)
  and GitHub's [Primer](https://github.com/primer/primitives) (all MIT): theme colors.
- aide (Quantic Studios): the sidebar design and agent states pitwall ports.
- [zj-radar](https://github.com/marktoda/zj-radar), [zellij](https://zellij.dev),
  [tmux](https://github.com/tmux/tmux) and [Ghostty](https://ghostty.org):
  ideas for hook handling, tab mode, session naming, detach and keymaps.

## License

MIT, see [LICENSE](LICENSE).
