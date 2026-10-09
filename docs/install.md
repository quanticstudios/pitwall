# Install

The install commands are in the [README](../README.md#install).

| Platform                       | Release builds | Status                     |
| ------------------------------ | -------------- | -------------------------- |
| Linux (glibc 2.35 or newer)    | x86_64, arm64  | beta, used every day       |
| macOS                          | arm64, x86_64  | beta, new and not yet run  |
| Windows 10 1809 or newer, 11   | x86_64, arm64  | beta, new and not yet run  |

pitwall is developed on Linux (Hyprland) and works on any Wayland or X11
desktop. Releases are tagged `v0.1.0-beta.N`. Config and saved state carry
over between releases, and a release that cannot keep them says so in the
changelog.

The script downloads the latest release for your system, checks it against
the release's `checksums.txt`, and installs `pitwall` in `~/.local/bin`. On
Windows it installs `pitwall.exe` in `%LOCALAPPDATA%\pitwall\bin`, adds that
folder to your user PATH, and adds pitwall to the Start menu. To pin a release, set `PITWALL_VERSION=v0.1.0-beta.1`; to
install somewhere else, set `PITWALL_INSTALL_DIR`. In PowerShell, set them
first with `$env:PITWALL_VERSION = 'v0.1.0-beta.1'`.

## Platform notes

The macOS build compiles and passes the platform-independent tests in CI;
the Windows build passes the whole test suite there. Nobody has used either
much yet. Expect rough edges, and please report what breaks.

Known gaps on macOS:

- Release binaries are not signed. Homebrew sets no quarantine flag and
  `get.sh` clears it; for an archive you downloaded yourself, run
  `xattr -dr com.apple.quarantine pitwall pitwall.app`.
- The app bundle comes from `get.sh`, or from the release archive: copy its
  `pitwall.app` to `~/Applications`. Homebrew installs only the `pitwall`
  command. Started from the Dock, pitwall reads PATH from your login shell
  (`$SHELL -ilc`) once, so it finds `claude` and `codex` as a terminal
  would, and opens its first session in your home folder.
- Notifications come through `osascript`, so macOS shows them under Script
  Editor.

Known gaps on Windows:

- Agent status comes from hooks only. Windows has no foreground process group
  to read, so an agent started without hooks, or a command running in a
  shell, shows nothing in the sidebar.
- A tab's folder follows `cd` in PowerShell and cmd, whose prompt pitwall
  sets up to report it (OSC 7). Git Bash, WSL and other shells report
  nothing unless your own prompt prints OSC 7, so their tabs keep the folder
  they opened in.
- `pitwall.exe` is a GUI program, so the Start menu entry (from `get.ps1` or
  Scoop) opens a window with no console behind it. Run from cmd or
  PowerShell, a command such as `pitwall ls` prints to that terminal, but
  the prompt comes back before it finishes, and `pitwall kill`'s question
  races the shell for what you type. Pipe the output (`pitwall ls |
  Out-Host`) to wait for it, and pass `-f` to kill without the question.
  Git Bash waits as usual.
- Notifications are Windows PowerShell toasts, so Windows lists them under
  Windows PowerShell. pitwall starts that PowerShell when the window opens,
  and the first toast waits for it, up to about ten seconds. A later toast
  for the same tab replaces the one before it.
- Claude Code runs hook commands through Git Bash. Other shells get a path
  with forward slashes, quoted only when it contains spaces.
- When an upgrade replaces a running daemon, the old daemon is stopped without
  a final save. It saves within moments of every change, so little is lost.

The Linux release archive
holds them as `pitwall.desktop` and `pitwall.svg` next to the binary, and
`scripts/install.sh` installs them when you build from source as below.

## Updates

A release build checks GitHub for a newer release when a
window opens, every hour after, and when the window regains focus half an hour
or more after the last check. When there is one, an Update button
shows at the bottom of the sidebar. It downloads the release's archive for
your system, checks it against `checksums.txt`, and replaces the `pitwall`
binary the window runs; a symlink to it keeps pointing at the new one. When
the download or the checksum fails, nothing is installed, the button reads
"Update failed", and a click tries again. After an install, "Restart to
finish" opens a new window on the same tab and closes this one.

Windows will not replace a running program, so there the update renames
`pitwall.exe` to `pitwall.exe.old` and puts the new one in its place. The
next window deletes `pitwall.exe.old` once nothing runs it; while the daemon
still does, the next update uses `pitwall.exe.old1`, and so on.

The update leaves the daemon running, so tabs keep their processes. Most
releases work with the daemon already running; when one cannot, the new
window asks before restarting it, as after any upgrade (see
[Upgrades](state.md#upgrades)).

A build from source (`git describe` past a tag, or `-dirty`) never checks.
To stop the check, set `check = false` under `[updates]` in config.toml or use the switch
under About in the settings page.

A Homebrew, Scoop or distro package install has no button: pitwall sees that
the package manager owns its binary, so update it with `brew upgrade`,
`scoop update pitwall` or the distro's package manager.

## Build from source

You need Git, a C compiler, pkg-config, and [mise](https://mise.jdx.dev). Gio,
the UI toolkit, needs the development headers for EGL, Wayland, X11,
xkbcommon, Xcursor and Xfixes (`egl`, `wayland-egl`, `wayland-client`,
`wayland-cursor`, `x11`, `x11-xcb`, `xkbcommon`, `xkbcommon-x11`, `xcursor`,
`xfixes` in pkg-config).

```sh
git clone https://github.com/quanticstudios/pitwall.git
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
