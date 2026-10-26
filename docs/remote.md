# Remote hosts

Agents can run on another machine, such as a build box or a cloud VM, while
the window runs on your laptop. Close the laptop and they keep working; open
it and the window reconnects.

```sh
pitwall --host me@box           # any ssh destination
pitwall --host box -s work      # session work on box
```

pitwall runs your own `ssh` with your `~/.ssh/config`, so the user, port,
key, agent and jump host are the ones `ssh box` already uses. There is no
new login, and no secret goes on a command line. It runs `pitwall
remote-start` on the host, which starts the daemon there unless one runs,
then forwards the daemon's socket to a socket in pitwall's runtime folder
here and opens the window on it. All of it goes through one shared ssh
connection (`ControlMaster`), which closes 10 minutes after the last window
on that host. A new session there opens in your home folder on the host.

To give a host a short name, list it in config.toml:

```toml
[[hosts]]
name = "box"
ssh = "me@box.example.com"
```

`pitwall --host box` then connects to `me@box.example.com`, and the window
title and the sidebar header say `box`. A name that is not listed goes to
ssh as it is.

Set the host up once:

```sh
ssh box 'curl -fsSL https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.sh | sh'
ssh box '~/.local/bin/pitwall hooks install'
```

The hooks belong on the host, where the agents run: they report to the
daemon there. pitwall looks for itself on the host's `PATH` and in
`~/.local/bin`, which a non-interactive ssh login often leaves off the
`PATH`. When pitwall is missing there, too old for `remote-start`, or of
another protocol major version, the window does not open and the error
gives the install command, pinned to this pitwall's release. pitwall never
installs anything on a host by itself. When the daemon running on the host
is of another major version than the pitwall installed there, the window
asks before restarting it, as after an upgrade.

On a Linux host with systemd, run `loginctl enable-linger` there once.
Without it, logging out of the host's last session, which closing the
laptop does, deletes the runtime folder that holds the daemon's socket, and
the next window can no longer reach the daemon your agents run in.
`pitwall doctor`, run on the host over ssh, says whether linger is on.

### Keep the daemon running across reboots

A window starts the host's daemon when none runs, so after the host reboots
your tabs come back only once you connect. To have them back before that,
let systemd start the daemon at boot. pitwall installs no unit itself; write
this one to `~/.config/systemd/user/pitwall.service` on the host:

```ini
[Unit]
Description=pitwall daemon

[Service]
ExecStart=%h/.local/bin/pitwall daemon
Restart=on-failure

[Install]
WantedBy=default.target
```

Then, on the host:

```sh
loginctl enable-linger
systemctl --user daemon-reload
systemctl --user enable --now pitwall
systemctl --user status pitwall   # active (running)
```

Stop any daemon that is not under systemd first: it holds the socket, and
the unit's daemon exits as soon as it sees one running. Change `ExecStart`
if pitwall lives somewhere other than `~/.local/bin`, as with Homebrew.
Panes start with the unit's environment, so a command such as `pitwall new
-- claude` finds only what is on systemd's `PATH`. Add
`Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin` under
`[Service]`, with your own folders, when an agent lives elsewhere.
Crash traces go to `journalctl --user -u pitwall`; the daemon's own log
stays in `daemon.log`.

`systemctl --user stop pitwall` stops the daemon and every program in its
panes. When the window restarts the daemon after an upgrade, the new one runs
outside the unit until the next boot, and `systemctl --user status` shows the
unit inactive until then. To remove the unit:

```sh
systemctl --user disable --now pitwall
rm ~/.config/systemd/user/pitwall.service
```

When the link drops, the window keeps its last screen under "Disconnected
from pitwall's background service." and reconnects as described under
[Upgrades](state.md#upgrades). Each try runs ssh again, which opens a new connection
when the old one died, and forwards the socket again. The daemon on the host
does not notice, and its agents keep running.

Some things stay on the machine they belong to:

- Notifications come from the state the daemon sends, so they show on the
  laptop as usual.
- Ctrl+click opens a web link here. A file link names a file on the host and
  does nothing.
- Open folder as group takes the folder's full path on the host, without
  completion.
- The agent panel shows the agent's state, but not its transcript or the
  changed files, which are on the host.
- `pitwall ls`, `new`, `wait` and the other commands talk to the daemon of
  the machine they run on. Run them on the host, for example in one of its
  panes.
- Windows has no remote windows: its ssh cannot share a connection.
