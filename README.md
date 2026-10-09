# pitwall
<img src="packaging/pitwall.svg" alt="pitwall logo: a P whose stem is three status lights and whose bowl is a terminal pane" width="96" height="96">

<img src="docs/media/pitwall-hero.webp" alt="The pitwall window: a sidebar of Claude Code and Codex tabs with live states, a pane that rings green when its agent finishes, and the jump key switching to the tab whose agent asks for approval" width="800">

pitwall shows every coding agent's live state at a glance, and one key jumps
to the agent that is waiting on you. Sessions survive closing the window and a
reboot, with agents resumed where they left off, and you can approve or answer
an agent from your phone.

It is a terminal multiplexer for running Claude Code, Codex, Gemini CLI,
OpenCode and pi side by side. It is a native window: a sidebar lists every tab
and what it is doing right now, whether that is a command running in a
terminal or an agent working, waiting for your answer, asking for approval,
done, or failed. Terminals are drawn with real fonts and pixels, not character
cells.

**pitwall is in beta.** It is used every day on Linux. Config and saved
state carry over between releases, and a release that cannot keep them says
so in the changelog. It is
developed on Linux (Hyprland) and works on any Wayland or X11 desktop.
macOS and Windows builds are new; see the
[platform notes](docs/install.md#platform-notes). Releases are tagged
`v0.1.0-beta.N`.

## Install

With Homebrew, on macOS or Linux:

```sh
brew install quanticstudios/tap/pitwall
```

With Scoop, on Windows:

```powershell
scoop bucket add quanticstudios https://github.com/quanticstudios/scoop-bucket
scoop install pitwall
```

Or with the install script, on Linux or macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.sh | sh
```

On Windows, in PowerShell:

```powershell
irm https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.ps1 | iex
```

The script checks the download against the release's `checksums.txt` and
installs `pitwall` in `~/.local/bin` (`%LOCALAPPDATA%\pitwall\bin` on
Windows), and `pitwall.app` in `~/Applications` on macOS. [docs/install.md](docs/install.md) covers pinning a version, the
known gaps on macOS and Windows, updates, and building from source.

| Platform                       | Release builds | Status                     |
| ------------------------------ | -------------- | -------------------------- |
| Linux (glibc 2.35 or newer)    | x86_64, arm64  | beta, used every day       |
| macOS                          | arm64, x86_64  | beta, new and not yet run  |
| Windows 10 1809 or newer, 11   | x86_64, arm64  | beta, new and not yet run  |

## Quick start

```sh
pitwall
```

The first window shows a welcome card. It lists the agent CLIs it found on
your `PATH` (Claude Code, Codex, Gemini CLI, OpenCode, pi, Cursor CLI, Amp,
Aider) and whether each one's hooks are installed. Start opens an agent in a tab of its own, and
Install hooks shows what it would change before it changes it. Dismiss the
card, or do anything else, and it never shows again. With no agent on your
`PATH`, you get a shell and a line with where to install one.

Hooks let agents report their state exactly. To install them from a terminal
instead:

```sh
pitwall hooks install --dry-run   # see what would change
pitwall hooks install             # let your agents report their state
```

The window opens on a shell in the folder you launched it from, in a session
with a generated name such as `swift-otter`. Run `claude`, `codex`, `pi`, a dev
server, anything. The tab's row in the sidebar shows what is happening: the
name of a running command, or the agent's state.

Open more tabs with **+** in the sidebar header. Each tab can be split into
panes. Typing `exit` closes a pane; an empty tab closes, and the session ends
with its last tab. Closing the window only detaches it: every session keeps
running, and `pitwall` opens the most recently used one again.

## Keys

The default preset follows Linux terminal conventions and leaves plain
Ctrl+letters to the shell. The aide preset is the Alt-key layout. Every
binding is in [docs/keys.md](docs/keys.md), and Ctrl+Shift+P lists them all.

| conventional                | aide                | Does                                              |
| --------------------------- | ------------------- | ------------------------------------------------- |
| Ctrl+Shift+U                | Alt+U               | Go to the tab that needs you, newest first        |
| Ctrl+Shift+P                | Ctrl+Shift+P        | Command palette: every action and its keys        |
| Ctrl+Shift+T                | Alt+Shift+T         | New tab below this one, in its folder             |
| Ctrl+Tab / Ctrl+Shift+Tab   | Alt+J / Alt+K       | Next / previous tab                               |
| Alt+1-9                     | Alt+1-9             | Go to the Nth tab the sidebar shows               |
| Ctrl+Shift+O / Ctrl+Shift+E | Alt+N / Alt+Shift+N | Split the pane to the right / below               |
| Ctrl+Shift+W                | Alt+Shift+W         | Close the pane                                    |
| Ctrl+Shift+S                | Alt+S               | Session switcher                                  |
| Ctrl+Shift+L                | Ctrl+Shift+L        | Show or hide the agent panel                      |
| Ctrl+Shift+F                | Ctrl+Shift+F        | Find in the pane's scrollback                     |

## Docs

- [Using pitwall](docs/usage.md): concepts, watching agents, sessions, tabs
  and panes, grouping, worktrees, detaching
- [Keybindings](docs/keys.md): both presets, tab mode, pane mode, find
- [Command line](docs/cli.md): every command, and driving tabs from scripts
- [Configuration](docs/config.md): config.toml, themes, fonts
- [Hooks](docs/hooks.md): what `pitwall hooks install` changes for each agent
- [Remote hosts](docs/remote.md): agents on another machine over ssh
- [Phone](docs/phone.md): answer agents from your phone
- [Decisions (Jev)](docs/decisions.md): approval recommendations, triage,
  turn checks
- [Where things live](docs/state.md): files, saved tabs, reboots, upgrades
- [Install](docs/install.md): platform notes, updates, building from source
- [Troubleshooting](docs/troubleshooting.md): what to check, and the logs
- [Agent skill](docs/agent-skill.md): a page to hand an agent that drives
  pitwall
- [Development](docs/development.md) and [credits](docs/credits.md)

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) for setup, checks and the pull request
process. Use the [issue templates](https://github.com/quanticstudios/pitwall/issues/new/choose)
to report bugs or propose features. Report vulnerabilities privately as
described in [SECURITY.md](SECURITY.md).

## License

MIT, see [LICENSE](LICENSE).
