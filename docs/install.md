# Install

The install commands and the platform table are in the
[README](../README.md#install).

The script downloads the latest release for your system, checks it against
the release's `checksums.txt`, and installs `pitwall` in `~/.local/bin`. On
Windows it installs `pitwall.exe` in `%LOCALAPPDATA%\pitwall\bin` and adds that
folder to your user PATH. To pin a release, set `PITWALL_VERSION=v0.1.0-alpha.1`; to
install somewhere else, set `PITWALL_INSTALL_DIR`. In PowerShell, set them
first with `$env:PITWALL_VERSION = 'v0.1.0-alpha.1'`.

## Platform notes

The macOS and Windows builds compile and pass the platform-independent tests
in CI, but nobody has used them yet. Expect rough edges, and please report
what breaks.

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
- A tab's folder does not follow `cd`. It stays the folder the tab opened in.
- There is no Start menu entry. Run `pitwall` from a terminal; started from
  Explorer, a console window flashes before the window opens.
- Claude Code runs hook commands through Git Bash. Other shells get a path
  with forward slashes, quoted only when it contains spaces.
- When an upgrade replaces a running daemon, the old daemon is stopped without
  a final save. It saves within moments of every change, so little is lost.

The AUR package installs a desktop entry and icon. The Linux release archive
holds them as `pitwall.desktop` and `pitwall.svg` next to the binary, and
`scripts/install.sh` installs them when you build from source as below.

## Updates

On Linux and macOS, a release build checks GitHub for a newer release when a
window opens, every hour after, and when the window regains focus half an hour
or more after the last check. When there is one, an Update button
shows at the bottom of the sidebar. It downloads the release's archive for
your system, checks it against `checksums.txt`, and replaces the `pitwall`
binary the window runs; a symlink to it keeps pointing at the new one. When
the download or the checksum fails, nothing is installed, the button reads
"Update failed", and a click tries again. After an install, "Restart to
finish" opens a new window on the same tab and closes this one.

The update leaves the daemon running, so tabs keep their processes. Most
releases work with the daemon already running; when one cannot, the new
window asks before restarting it, as after any upgrade (see
[Upgrades](state.md#upgrades)).

A build from source (`git describe` past a tag, or `-dirty`) never checks.
Windows has no button; run the install command again to update. To stop the
check, set `check = false` under `[updates]` in config.toml or use the switch
under About in the settings page.

Update a Homebrew or AUR install with its package manager, and stop
the check there. The button cannot write over an AUR package's
`/usr/bin/pitwall`, and on Homebrew it would replace the binary behind brew's
back.

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
