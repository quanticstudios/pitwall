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
