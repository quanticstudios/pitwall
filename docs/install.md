# Install

The install commands are in the [README](../README.md#install).

| Platform                       | Release builds | Status                     |
| ------------------------------ | -------------- | -------------------------- |
| Linux (glibc 2.35 or newer)    | x86_64, arm64  | used every day             |
| macOS                          | arm64, x86_64  | new and not yet run        |
| Windows 10 1809 or newer, 11   | x86_64, arm64  | new and not yet run        |
| Linux, `pitwall-core`          | x86_64, arm64  | new; no window             |

pitwall is developed on Linux (Hyprland) and works on any Wayland or X11
desktop. Releases are tagged `v0.2.N`: `v0.2.0` starts the line and each later one bumps the patch number. Config and saved state carry
over between releases, and a release that cannot keep them says so in the
changelog.

The script downloads the latest release for your system, checks it against
the release's `checksums.txt`, and installs `pitwall` in `~/.local/bin`. On
Windows it installs `pitwall.exe` in `%LOCALAPPDATA%\pitwall\bin`, adds that
folder to your user PATH, and adds pitwall to the Start menu. To pin a release, set `PITWALL_VERSION=v0.2.0`; to
install somewhere else, set `PITWALL_INSTALL_DIR`. In PowerShell, set them
first with `$env:PITWALL_VERSION = 'v0.2.0'`.

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

## Servers and containers

`pitwall-core` is the daemon and every command but the window. It is a
static binary with no cgo, so it needs no GUI or C library, and runs on a
server, in a container or on an arm64 board. Releases include it for Linux
x86_64 and arm64. Install it with:

```sh
curl -fsSL https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.sh | sh -s -- --core
```

or with `PITWALL_CORE=1` set. `PITWALL_VERSION` and `PITWALL_INSTALL_DIR`
work as above. The script installs `pitwall-core` and, when that folder has
no `pitwall`, links `pitwall` to it, so hooks, agents and `pitwall --host`
from a desktop find it by that name. It never replaces another `pitwall`;
it prints the `ln -sf` command that would. On macOS `--core` stops with an
error: there is no macOS core.

Then run `pitwall-core hooks install`. `pitwall-core daemon` runs the
daemon in the foreground, as a container's entry point would, and
`pitwall-core remote-start` starts it in the background unless one runs.
`pitwall-core new -d -- claude` opens a tab without a window, and `ls`,
`read`, `send` and `wait` drive it. The commands that open a window refuse
before they change anything: `session new` needs `-d`, and `attach` only
focuses a window that already shows the session. From a desktop,
`pitwall --host box` opens a window on the host's daemon
([remote.md](remote.md)).

`pitwall-core update` installs the latest `pitwall-core` archive, never the
desktop one, and hands the daemon over as `pitwall update` does below.

## Updates

A release build checks GitHub for a newer release when a
window opens, every hour after, and when the window regains focus half an hour
or more after the last check. When there is one, an Update button
shows at the bottom of the sidebar. It downloads the release's archive for
your system, checks it against `checksums.txt`, and replaces the `pitwall`
binary the window runs; a symlink to it keeps pointing at the new one. When
the download or the checksum fails, nothing is installed, the button reads
"Update failed", and a click tries again. After an install, the new
binary's daemon takes over the running one's panes, as `pitwall update`
does below. "Restart to finish" then opens a new window on the same tab and
closes this one.

From a terminal, `pitwall update` does the same: it installs the latest
release over the binary it runs from, then starts that binary's daemon in
handoff mode. The new daemon takes over every pane's terminal from the old
one ([handoff.md](handoff.md)), so agents, dev servers and test loops keep
running with their scrollback, and it prints which happened:

```
Installed pitwall v0.2.1 over v0.2.0.
The new daemon took over; every program in a pane kept running.
```

If the handoff fails, the old daemon keeps serving and every program in a
pane keeps running on the old build. `pitwall update` prints why the handoff
failed and exits 1. `pitwall update --restart` tries the handoff again and,
when it fails, restarts the daemon as **Restart now** does: agents resume,
other programs in panes stop. Until then, a release that changed the
protocol's major version cannot drive the old daemon from the command line.
Daemons started from this release refuse the handoff when systemd runs
them as a service
([remote.md](remote.md#keep-the-daemon-running-across-reboots)). The Update
button does not restart on its own either: it shows why in the notice at
the top of the window, and "Restart to finish" asks, then tries the handoff
again before it restarts. On a build from
source, or a binary a package manager owns, `pitwall update` installs
nothing and only moves the daemon onto the binary, so run it after
`brew upgrade` or `scripts/install.sh`. With no daemon running it stops
after the install.

`pitwall update --version vX` installs release `vX` instead of the latest,
older or newer, such as `pitwall update --version v0.2.13` to go back one
release. It checks that the release has this binary's archive and that it
matches `checksums.txt` before it writes anything, then hands the daemon
over as above. When the handoff fails, the old daemon keeps serving and the
command to force it is `pitwall update --restart --version vX`, since a
plain `pitwall update --restart` would install the latest release again.
The installed older release may not understand `--version` or `--restart`.
If it rejects that retry, reinstall a newer updater that supports these
flags at the same path without touching the daemon, then run
`pitwall update --restart --version vX` in one invocation. The
[install script](../README.md#install) leaves the daemon alone; use the same
install directory and, for pitwall-core, `--core`. This also applies after a
refused downgrade across protocol levels. Avoid running the older release's
plain `update`, which installs the latest release.
A package manager's binary installs nothing here either. On Windows the
release installs and the daemon keeps running on the old build until
an updater that supports these flags runs
`pitwall update --restart --version vX` in one invocation.
`PITWALL_VERSION=vX` with the [install script](../README.md#install)
installs a version without touching the daemon.

Windows will not replace a running program, so there the update renames
`pitwall.exe` to `pitwall.exe.old` and puts the new one in its place. The
next window deletes `pitwall.exe.old` once nothing runs it; while the daemon
still does, the next update uses `pitwall.exe.old1`, and so on.

`pitwall update` from v0.2.13 or earlier runs that release's own code,
which restarts the daemon when the handoff fails. To keep the panes running
through that update, install the new release with the
[install script](../README.md#install), which leaves the daemon alone, then
run the new `pitwall update`, which hands the daemon over or keeps it.
This migration path applies to daemons running outside systemd services.
A daemon from v0.2.13 or earlier has no systemd refusal guard: even the new
updater can hand it over, after which systemd kills its panes. If systemd
runs that old daemon, install the new binary without running `update`, then
run `systemctl --user restart pitwall` when you can stop the programs in
panes. Agents resume. Use `systemctl restart <unit>` for a system service.

Windows has no handoff. There the update leaves the daemon running, so
tabs keep their processes, and `pitwall update` exits 1 naming
`pitwall update --restart`, which restarts it. Most releases
work with the daemon already running; when one cannot, the new window asks
before restarting it, as after any upgrade (see
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
