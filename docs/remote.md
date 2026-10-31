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
- The sidebar's repo icons come from the repo on the host, which the
  daemon there reads. With an older pitwall on the host, each repo gets its
  letter tile.
- Ctrl+V in a Claude Code or Codex pane pastes text or an image from the
  clipboard here. The host keeps a pasted image in a file only you can
  read, under its state directory, until the tab closes, and the agent gets that
  file's path. With an older pitwall on the host, text still pastes, and
  an image paste says under the pane that the host's pitwall is too old.
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

## Web client

pitwall's background service can serve a built aide web client, the
bundle, to a browser on this machine and to paired phones. Point it at
the bundle's folder:

```toml
[web]
enabled = true
bundle = "~/src/aide/out/web"
```

Then `pitwall web open` opens it in your default browser. The local
listener serves it at `/` on `127.0.0.1`, next to the web protocol at
`/ws` ([web-protocol.md](web-protocol.md)). With `[remote]` on too, the
phone page shows Open the full client to a paired phone, which serves the
same bundle at `/app/` and the web protocol at `/ws` on the phone page's
address, such as the one `tailscale serve` gives. The phone page's own
approvals stay at `/`.

pitwall serves the folder's files and nothing else: a folder's
`index.html` but no listing, no dotfile, nothing outside the folder, a
symlink out of it included. The bundle loads its files with relative
URLs (Vite's `base: './'`), since it sits at `/app/` on the phone.

### Sessions and the bootstrap

No token goes in a URL, a log line or the browser's history:

1. `pitwall web open` writes a one-time code's SHA-256 to `web-open.json`
   in the state folder (mode 0600) and opens
   `http://127.0.0.1:<port>/_pitwall/open?code=<code>`. The code is not the
   token. It works once, within a minute.
2. The listener deletes the code, sets a session cookie (HttpOnly,
   SameSite=Strict, named for the listener's port) and answers 303 to `/`,
   so the page the browser shows has no code in its URL. A history entry
   for the open link holds only a used code.
3. On a phone, the phone page sends its device token in the
   `Authorization` header to `POST /api/web`, which sets the cookie the
   same way. The device token stays in the phone page.
4. The bundle fetches `/_pitwall/bootstrap` with the cookie and gets
   `{"url": "/ws", "token": "<64 hex digits>", "protocol": 1}`. It keeps
   the token in memory and sends it in `hello`. The token is the
   session's own: neither `web.json`'s token nor a device token reaches the
   browser. `url` is relative, so it is right behind `tailscale serve`.

Every file the bundle loads needs the cookie, and so does the bootstrap.
A session lasts 24 hours at most and ends with the listener, a daemon
restart, `pitwall web token --rotate` (the local listener's sessions) or
`pitwall remote revoke` (that phone's sessions): run `pitwall web open`
again, or tap Open the full client again. `daemon.log` says when a
session starts, never the code or a token.

The bundle's pages get this Content-Security-Policy: scripts, styles,
images, fonts and workers from the page's own origin only, no inline
script or style, no `eval`, connections only to its own origin and its
WebSocket, no frames, forms or `<base>`. The local listener still refuses
a request whose `Host` or `Origin` is not its own; the phone page refuses
a `/ws` upgrade whose `Origin` is another site's.
