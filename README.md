# pitwall

pitwall is a native terminal multiplexer for coding agents on Linux and Hyprland. It opens straight into a shell, like tmux. Each session is a set of split panes, and the sidebar shows what every session is doing: a running command, or a Claude Code or Codex agent working, waiting for input, asking for approval, done, or failed. Sessions start ungrouped; you group the ones that belong together later. Go and Gio draw the window, so it is not limited to terminal cells.

A daemon owns the PTYs, so closing the window leaves sessions running. It saves sessions, groups, pane commands, and agent session IDs to disk, and brings them back after a reboot.

## Install

Install mise, Git, a C compiler, and pkg-config. Gio needs the development libraries for EGL, Wayland, X11, xkbcommon, xkbcommon-x11, Xcursor, and Xfixes. Their pkg-config names are `egl`, `wayland-egl`, `wayland-client`, `wayland-cursor`, `x11`, `x11-xcb`, `xkbcommon`, `xkbcommon-x11`, `xcursor`, and `xfixes`.

From this checkout, install Go 1.27.1 and build pitwall:

```sh
mise install go
./scripts/install.sh
```

The installer writes the binary to `~/.local/bin/pitwall`, the desktop entry to `~/.local/share/applications/pitwall.desktop`, and the icon to `~/.local/share/icons/hicolor/scalable/apps/pitwall.svg`. Add `~/.local/bin` to your shell's `PATH` if needed. The desktop entry uses the installed binary's absolute path.

Set `PREFIX` to an absolute path to change the installation directory:

```sh
PREFIX="$HOME/apps/pitwall" ./scripts/install.sh
```

This puts the binary under `$PREFIX/bin` and the desktop entry and icon under `$PREFIX/share`. Add `$PREFIX/bin` to `PATH`. Desktop launchers need `$PREFIX/share` in `XDG_DATA_DIRS` to discover a custom installation.

The installer does not edit agent configuration.

## First run

```sh
pitwall hooks install
pitwall
```

`pitwall` opens the window, starts the daemon if needed, and drops you into a shell in the folder you launched it from. Run `claude`, `codex`, or anything else there. `Alt+Shift+T` or the `+` in the sidebar header opens another session in the folder your current shell is in.

## Sessions and groups

Sessions start ungrouped, at the top of the sidebar, named after their folder. Each row shows the session's state: the command a terminal is running, or the agent's state and what it is asking. A second line shows `+added -deleted` lines and the branch inside a Git repo, otherwise the folder.

To group sessions, Ctrl+click or Shift+click to pick several, then right-click and choose **New group**, or **Move to group** for an existing one. **Remove from group** and the group's **Ungroup** never close anything. **Open folder as group** in the footer makes a group for a folder; for a Git repo, the group's new-worktree action creates a session in a fresh worktree under `<repo>/.worktrees/`. Deleting such a session removes that worktree; deleting any other session leaves its folder alone.

Agents report their state through hooks (below). Without hooks, pitwall still recognizes `claude` and `codex` as the pane's foreground process and reads their state from the screen, which is less exact. When a session needs you and you are not looking at it, pitwall sends a desktop notification with `notify-send`.

Close the window and run `pitwall` again to reconnect to the same daemon. To run the daemon in the foreground for troubleshooting:

```sh
pitwall daemon
```

## Hooks

Hooks send Claude Code and Codex activity to the sidebar. `pitwall hook <provider>` forwards an event to the pane's daemon. Hooks do nothing outside pitwall panes.

```sh
pitwall hooks install --dry-run
pitwall hooks install
```

Installation merges entries into `~/.claude/settings.json` and `~/.codex/hooks.json`. It creates missing files and keeps unrelated settings and hooks. Repeating installation does not duplicate commands. The command prints each added entry. Existing files get a backup named `<file>.pitwall-backup-<unix time>` before replacement. Replacement is atomic and preserves the existing file's permissions.

Inside Codex, run `/hooks` once to trust the hooks. Restart existing agent sessions if they have not loaded the configuration.

Installation records the running executable's absolute path with symlinks resolved. Install a persistent binary first. Hook installation refuses executables under `/tmp`, the temporary directory, or the Go build cache. Use `scripts/install.sh` rather than installing hooks from `go run`.

Remove hooks for the binary running this command:

```sh
pitwall hooks uninstall --dry-run
pitwall hooks uninstall
```

Uninstall removes this binary's hook commands and keeps other commands, including hooks for another pitwall installation. It backs up files before changing them. Both dry-run commands print the resulting JSON and write nothing.

For manual configuration, `pitwall hooks` prints Claude Code and Codex JSON blocks. It also prints the Codex `notify` alternative for `~/.codex/config.toml`. The `notify` alternative reports finished turns only. The installer does not modify `config.toml`.

## Keybindings

These bindings match the settings dialog and navigation code:

| Keys                          | Action                                |
| ----------------------------- | ------------------------------------- |
| Alt+J / Alt+K                 | Next / previous session in the group  |
| Alt+H / Alt+L                 | Previous / next pane                  |
| Alt+Arrows                    | Same as J / K / H / L                 |
| Hold Alt                      | Show the session switcher             |
| Alt+Space                     | Pin the switcher open                 |
| Alt+1-9                       | Jump to session                       |
| Alt+N                         | Split the pane to the right           |
| Alt+Shift+N                   | Split the pane below                  |
| Alt+Shift+W                   | Close the pane                        |
| Alt+Shift+T                   | New session in this folder            |
| Ctrl+Shift+C / Ctrl+Shift+V   | Copy selection / paste                |
| Escape                        | Close the switcher or dialog          |
| Tab in the folder field       | Complete a folder name                |
| Enter in the folder field     | Open the folder as a group            |
| Shift+PageUp / Shift+PageDown | Scroll backward / forward by one page |

Alt+J/K cycles within the current group when the switcher is hidden; ungrouped sessions count as one group. With the switcher visible, it moves through every session, and releasing Alt switches to the selected one. Alt+1-9 follows the switcher's order. Alt+Down/Up changes session; Alt+Left/Right changes pane. Ctrl+click and Shift+click in the sidebar pick sessions for grouping.

## State and resume

The daemon saves state in `~/.local/state/pitwall/state.json`. If `XDG_STATE_HOME` is set, it uses `$XDG_STATE_HOME/pitwall/state.json`. The detached daemon writes its log to `daemon.log` in the same directory.

The socket is `$XDG_RUNTIME_DIR/pitwall/pitwall.sock`. Without `XDG_RUNTIME_DIR`, the daemon uses `/tmp/pitwall-<uid>/pitwall.sock`. Panes inherit `PITWALL_PANE` and `PITWALL_SOCKET` so hooks reach the correct pane and daemon.

After a reboot, run `pitwall`. The daemon restores saved sessions, groups and panes in their working directories. If hooks recorded a Claude Code session ID, restoration runs `claude --resume <session-id>`. For Codex, it runs `codex resume <session-id>`. A pane started as a shell script that later ran an agent resumes the agent only, never the script. Other panes restart their saved commands. PTY processes and terminal scrollback do not survive a reboot. Keep the agent executables on `PATH` and the project folders available.

## Build and checks

`mise.toml` sets `GOFLAGS=-tags=novulkan`. The installer also sets that value explicitly. Gio uses OpenGL without requiring Vulkan headers. Use mise for development commands so they receive the same build tag:

```sh
mise exec -- go build ./cmd/pitwall
mise exec -- go vet ./...
mise exec -- go test -race ./...
```

## Credits

- aide provides the reference for the sidebar appearance and behavior.
- tuios provides borrowed terminal and session code under the MIT license. Adapted code carries a source-path comment.
- Geist fonts use the SIL Open Font License. The license is in `internal/ui/theme/fonts/OFL.txt`.
- lucide icons use the ISC license.
