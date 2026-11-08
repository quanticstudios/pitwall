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

### Several hosts in one window

To see a host's sessions in every plain `pitwall` window, next to this
machine's, set `window = true` on its entry:

```toml
[[hosts]]
name = "box"
ssh = "me@box.example.com"
window = true
```

The sidebar then lists `local` and `box` above the tabs, each with its
sessions, and a click on one shows it. Below them, Waiting on you lists the
agents on any host that wait on an approval, a question, a plan or an
error; a click goes to the tab. In the inbox view, which already shows the
session the window shows, that list is Waiting elsewhere instead: only the
agents of other sessions and hosts, and nothing when none of them waits.
The tabs below are those of the session the
window shows, and the header tags it with its host. The session switcher,
desktop notifications and session search in the palette cover every host
and name the host of a session on another machine.

Set `[sidebar] sessions = false`, or turn off Sessions in Settings >
Appearance > Sidebar, to hide the host headings, session rows and their
hover previews. Waiting on you or Waiting elsewhere stays, with links to
tabs on every host. The header keeps the session name and host tag. The
session switcher, next/previous session keys and desktop notifications
still cover every host. The setting applies live and defaults to true.

The window opens on this machine at once and connects to each host in the
background, as `pitwall --host` does, retrying with backoff from half a
second up to 30 seconds. A host that is down keeps its last sessions in the
list, dimmed, with the reason in its heading, and never blocks this
machine's sessions or another host's. Only this machine's daemon going
down covers the window, and only while it shows one of its sessions. A host
whose daemon runs another major version stays down: open `pitwall --host
box` once to restart it there.

Every action goes to the daemon that owns the tab, pane or session it
names. A new session or tab opens on the host of the session the window
shows, or in a [shared session](#shared-sessions) on the machine you pick. `pitwall --host box` still opens a window on that host alone, and
`pitwall ls`, `new` and the other commands still talk to the daemon of the
machine they run on.

### Shared sessions

A session can hold tabs from this machine and from the hosts with
`window = true` at once, so a tab on box is one click away and never in
another session. The sidebar's + asks which machine the new tab opens on:
this machine and each host, with the current tab's machine picked, a
latency beside each host and a down host greyed out with why. Ctrl+Shift+T
opens on the current tab's machine without asking, a group's + opens in
that group's folder on its machine, and from a shell:

```sh
pitwall new --host box                    # a shell in box's home folder
pitwall new --host box '~/evals' -- codex # quote ~ so it means box's home
```

With `--host`, box finds the command on its own PATH, so `codex` needs to
be installed there, not here. `--host` refuses `--issue`, which reads the
issue from a checkout here, and refuses `--guard` and presets with
variables while box's daemon is too old to keep them. A task started from
the New task dialog runs on the machine of the folder you pick, in that
machine's part of the session.

Each tab and its splits stay on one machine, and a group holds tabs of one
machine: a project checked out here and on box is two groups, the one on
box with a box chip on its header. Rows of tabs on another host have a
chip with the host's name; this machine's have none. A row of machines
under the sidebar header says how each one is: its latency, or "offline,
retry in 7s" with ssh's error on hover. Click a host there to give it one
of the tab colors, which goes to `[host_colors]` in config.toml; until
then hosts take colors in turn.

A pane on another host has a tag with the host's name hanging from its top
border, and a ring in the host's color when it has focus. Hover the tag
for the ssh destination, the pane's folder and the latency. The tag needs
a row and a half more room above the terminal, so a remote pane is that
much shorter; this machine's panes look as they always did. The window
title names the host of the focused pane, and a tab's hover card gives
its number on its host, such as `tab box:3`: each host numbers its own
tabs, as `pitwall ls` there does. A paste of several lines into a pane
on another host, within 15 seconds of focus moving there from a pane on
another machine, asks first.

When a host goes down its tabs stay where they are with their last
screen, dimmed, and its tag and ring turn red. Their rows and board cards
read Offline and say when they were last seen, and the board says above
its columns that the host is unreachable. Typing into them is off,
never queued: the focused pane says "Typing is off until box reconnects.
Screen from 14:02." with Retry now. Allow and Deny leave their rows and
cards, which say "Answer when box is back". This machine's tabs carry on as
before. You can close a down host's tab: the window forgets it at once,
and the next window to reach the host closes it there, unpinning it first
if you said Unpin and close. Until box's state shows the tab gone, this
machine keeps asking.

This machine keeps the shared session: its name, the order of its tabs
and groups across machines, and for each host the session there that
holds its tabs. That session is an ordinary one on the host, named after
the shared one, so `pitwall --host box`, the phone page and `pitwall ls`
on box show those tabs as a session of their own. A session on another
host becomes shared when you open a tab on this machine from it. A window
must list the host in `[[hosts]]` with `window = true` to show its part.
Renaming the shared session renames each part; killing it from the window
kills them, and waits while a host with a part is down, so nothing keeps
running there unseen. The tabs here close only once each host's state
shows its part gone; until then the window sends the kill again, also
after a reconnect, and the session stays in the sidebar. `pitwall session kill` refuses a shared session,
since it reaches only this machine's daemon. Create PR, Merge PR and Rerun
checks run on the machine of the tab they start from.

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
