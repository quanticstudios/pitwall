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
ssh as it is, except from a pitwall pane: there `pitwall new --host` takes
only a `[[hosts]]` name, so an agent starts processes only on machines you
listed. `local` means this machine, so a `[[hosts]]` entry with that name
is reported as a config problem and ignored.

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

[The monitor](usage.md#the-monitor) of a host's tab, or its Monitor tab, shows that host, sampled
by its own daemon over the same connection, and dims with "No data since"
while the host is down.

Every action goes to the daemon that owns the tab, pane or session it
names. A new session or tab opens on the host of the session the window
shows, or in a [shared session](#shared-sessions) on the machine you pick. `pitwall --host box` still opens a window on that host alone, and
`pitwall ls`, `new` and the other commands still talk to the daemon of the
machine they run on, except for `--host` and an agent's
[workers on another machine](#workers-on-another-machine).

### Shared sessions

A session can hold tabs from this machine and from the hosts with
`window = true` at once, so a tab on box is one click away and never in
another session. The sidebar header's + asks which machine the new tab opens on:
this machine and each host, with the current tab's machine picked, a
latency beside each host and a down host greyed out with why. Ctrl+Shift+T
opens on the current tab's machine without asking, a group's + opens in
that group's folder on its machine, and from a shell:

```sh
pitwall new --host box                    # a shell in box's home folder
pitwall new --host box '~/evals' -- codex # quote ~ so it means box's home
pitwall new --host box -d -- codex        # detached on box, as new -d here
```

With `--host`, box finds the command on its own PATH, so `codex` needs to
be installed there, not here. `--host` refuses `--issue`, which reads the
issue from a checkout here, and refuses `--guard` and presets with
variables while box's daemon is too old to keep them or to check the
guard's hooks. Box's daemon starts a guarded tab only when pitwall's hooks
are installed in the agent's config folder there (see
[the guard](usage.md#the-guard)).

Run in an agent's pane, `pitwall new --host box` opens a worker of that
agent's tab, as `pitwall new` does here. This machine records the worker
before box starts it. In a window that shows both machines, the worker
hangs under the agent's tab, with box's name on its row, and box closes it
when it is done, as `[agents] close_done_workers` in box's config says.
When the connection drops during the start, the command retries 3 times;
if box stays out of reach it prints the tab's id. Run the same command
with `--id <id>` to finish that start: box opens no second tab for an id it
already has. From a pane, ssh never asks for a password or passphrase
(`BatchMode=yes`): open the connection once from a shell or a window
first, or let your ssh agent answer. When `box` has no `window = true`,
the command says that the sidebar will not show the worker. When the window
will not open a tab or task on a host, or kill, group or move a shared
session's tabs, such as while the host is down, it says why in the notice
at the top of the window. A task started from
the New task dialog runs on the machine of the folder you pick, in that
machine's part of the session.

Each tab and its splits stay on one machine, and a group holds tabs of one
machine: a project checked out here and on box is two groups. The sidebar's
tree lists each machine's tabs and groups together, this machine first,
then the hosts in `[[hosts]]` order, each below a line that names the
machine and says how it is: "This machine", a host's latency, or "offline,
retry in 7s" in red with ssh's error on hover. Rows and group headers carry
no host mark, since the line above them says where they run. A drag
reorders tabs and groups within their machine and never moves one to
another machine. Alt navigation and the tab digits count the tabs in the
same order. Hover a line for a + that opens a tab on that machine without
asking. Click a host's line, or right-click it, to give the host one of the
tab colors, which goes to `[host_colors]` in config.toml; until then hosts
take colors in turn. A session with tabs on one machine only shows no
lines.

The line has two styles, set with `[appearance] host_sections` or
Settings, Appearance, Machines. `divider`, the default, is a thin labelled
rule above each machine's tabs. `sections` gives each machine a header with
a dot, its health and its tab count, and a rule in the host's color down
the left of a host's tabs. A click on a header folds its tabs away, and a
folded header counts the tabs that need you. Each window remembers the
folds per session until it closes, and a section opens again when the
active tab moves into it. While the sidebar has the keyboard the cursor
stops on the headers too: Right or Enter opens one, Left or Enter folds it.

In the inbox, Waiting on you and on the board, where tabs of several
machines mix, a tab still names its host. The inbox has no machine lines,
so there a host's health shows on its panes' tags.

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
on box show those tabs as a session of their own. The host records which
machine's session each part was made for. A session there that already has
the name and belongs to another machine's session is never joined: the new
part gets the name with `-2`, `-3` added. When you open the first tab on
box from the window or a shell, a session of the same name that belongs to
no other machine joins; `pitwall new --host` run in a pane never joins a
session by name and makes a part of its own, so it refuses a daemon on box
from before parts recorded their session. A part joined before hosts
recorded this takes the session of the first window or `pitwall new --host`
that reaches it, and
leaves the shared sessions of every other machine. A session on another
host becomes shared when you open a tab on this machine from it, unless it
is already another machine's part. A window
must list the host in `[[hosts]]` with `window = true` to show its part.
Renaming the shared session renames each part; killing it from the window
kills them, and waits while a host with a part is down, so nothing keeps
running there unseen. The tabs here close only once each host's state
shows its part gone; until then the window sends the kill again, also
after a reconnect or from the next window, and the session stays in the
sidebar. `pitwall session kill` refuses a shared session,
since it reaches only this machine's daemon. Create PR, Merge PR and Rerun
checks run on the machine of the tab they start from.

### Workers on another machine

An agent in a pane here can start workers on box, so their builds and
tests use box's CPU, memory and disk:

```sh
pitwall worktree new login-fix --host box -- claude "Fix the login redirect."
pitwall new --host box -n evals '~/evals' -- codex "Run the eval suite."
```

Each prints the worker as `#box:3`. `worktree new --host` resolves
`--from`, else the default branch, to a commit here and pushes it to the
repository on box as `refs/pitwall/base/<id>` over the shared ssh
connection, so commits not pushed anywhere reach the worker. Uncommitted
changes stay here. The repository on box is at the same path under box's
home folder, `~/Work/app` for `~/Work/app` here, or at `--repo`. Box makes
the worktree from that commit. Removing the worktree while its tab is
still open, with `worktree rm` on box or the window's close-and-remove,
also deletes the ref. Closing the tab alone keeps its worktree, branch
and base ref. `--host` refuses `--branch`, since the branch can differ
on the two machines; `--from <branch>` starts a new branch off it.
In [fleet mode](usage.md#fleet-mode), `host = "box"` under
`[fleet.small]`, `[fleet.normal]` or `[fleet.big]` sends that lane's
workers to box, and `--host local` keeps one here. The worker carries this
machine's timebox and nudge for its lane, and box nudges it past that
timebox whether fleet mode is on there or not.

Before anything is pushed or started, box's daemon checks the start with
its own PATH and environment:

| Check                                              | When                    | A problem               |
| -------------------------------------------------- | ----------------------- | ----------------------- |
| the command is on the daemon's PATH                | always                  | stops the start         |
| a guarded agent has pitwall's hooks                | `--guard`               | stops the start         |
| the agent has pitwall's hooks and a login          | Claude Code or Codex    | warns                   |
| the repository is a git work tree                  | `worktree new`          | stops, prints `git clone` |
| `git ls-remote origin HEAD`                        | `worktree new`          | stops the start         |
| `git push --dry-run` to origin                     | `worktree new`          | stops the start         |
| `gh api repos/<repo>` grants push                  | a GitHub origin         | stops the start         |

git runs with `GIT_TERMINAL_PROMPT=0` and ssh's `BatchMode`, 10 seconds
per call. A problem prints under `box cannot start this worker; nothing
was started:` with fixes in the problem text or under `fix on box:`.
A dry run that succeeds still warns that origin may refuse the real push.
A host whose daemon predates
the check gets none, and retries of an existing `--id` skip it. A command
or config folder supplied by the new worktree can be checked only after
setup; guarded agents still need their hooks before they start.

A worker on box hangs under the agent's tab, as described above, and the
agent reaches it from its own pane:

```sh
pitwall ls --children --json        # box's rows: "host": "box", "addr": "box:3", "n": 0
pitwall read box:3                  # or @<id>, a tab or pane id on any machine of the session
pitwall send box:3 -- "rebase on main"
pitwall prompt box:3 --wait -- "status?"
pitwall wait box:3 --until done
pitwall wait --children --until done
pitwall kill -f box:3
```

`#3` is always this machine's tab 3, and `box:3`, or `#box:3` as `new
--host` prints it, box's own tab 3, as `pitwall ls` on box numbers it. A title names a tab here only. Each call
dials box over the shared ssh connection and drops it when it ends.

`wait --children` counts a worker on box only while it watches box's
daemon on a live connection. When box drops, it prints `box unreachable:`
and the reason, dials again with the window's backoff, and does not exit
0 until box is back, even for a worker box showed as done before. After 5
minutes out of reach it exits 1 naming the host. A worker box has no tab
for 2 minutes after its start prints `box:@<id> lost:`, and one that
closed before it finished, or whose result box lost in a restart, prints
`result lost` with its branch; either makes the exit 1.

When the agent's tab is gone for good, a new agent takes its workers over:
`pitwall ls --orphans` lists them, here and in parts on `[[hosts]]`
machines made for a session this machine no longer has, and `pitwall
adopt @<id>` (or `--all`) makes the calling tab their parent. A part made
by another machine never shows.

pitwall's fleet notes about a worker on box, such as one past twice its
lane's timebox or one whose CI failed again, reach no pane here, so the
agent watches its `state` in `pitwall ls --children --json`. Box's own
config decides whether CI failures go to the worker and when it closes.
Results come back through git: a worker pushes its branch and opens its
pull request from box, and you review, merge and release here as usual.
The [agent skill](agent-skill.md#workers-on-another-machine) has the same
in an agent's words.

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
  daemon there reads, also for a host's tabs in a shared session. With an
  older pitwall on the host, each repo gets its letter tile.
- Ctrl+V in a Claude Code or Codex pane pastes text or an image from the
  clipboard here. The host keeps a pasted image in a file only you can
  read, under its state directory, until the tab closes, and the agent gets that
  file's path. With an older pitwall on the host, text still pastes, and
  an image paste says under the pane that the host's pitwall is too old.
- Ctrl+click opens a web link here. A file link names a file on the host and
  does nothing.
- A program on the host can set the clipboard here with OSC 52, as Neovim
  and tmux do, but only after you allow it, so a program there cannot
  silently change what you paste here. The first write from a host
  opens a dialog, "box wants to set your clipboard", with the text,
  Allow, Deny and Remember for box. Allow puts the text on the
  clipboard; Deny, Escape or a click outside drops it. Enter does
  neither, so typing into a pane as the dialog opens answers nothing.
  Remember keeps the answer in config.toml, and later writes from that
  host follow it without asking:

  ```toml
  [host_clipboard]
  box = "allow"
  "gpu.example.com" = "deny"
  ```

  Settings, Terminal, Machines has Ask, Allow and Deny for each host in
  `[[hosts]]` and each host with an answer. Ask removes the host's line.
  Writes from this machine's panes never ask, and `[terminal] osc52 =
  "off"` ignores every write.
- Open folder as group takes the folder's full path on the host, without
  completion.
- The agent panel shows the agent's state, but not its transcript or the
  changed files, which are on the host.
- `pitwall ls`, `new`, `wait` and the other commands talk to the daemon of
  the machine they run on. Run them on the host, for example in one of its
  panes, unless you start a tab there with `--host` or reach an agent's
  [workers on another machine](#workers-on-another-machine).
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
