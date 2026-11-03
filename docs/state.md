# Where things live

| What                | Where                                                         |
| ------------------- | ------------------------------------------------------------- |
| Saved tabs          | `~/.local/state/pitwall/state.json` (`$XDG_STATE_HOME`)       |
| Saved tab backups   | `state.json.prev`, `state.json.bad-<time>` and `state.json.restored-<time>` next to it |
| Saved tab snapshots | `~/.local/state/pitwall/snapshots/state-<time>.json`, the newest 48 (`pitwall snapshots`) |
| Logs                | `~/.local/state/pitwall/gui.log`, `daemon.log` and `crash.log` (`pitwall logs`) |
| Decisions log       | `~/.local/state/pitwall/decisions.jsonl` (`pitwall jev report`) |
| Agent errors log    | `~/.local/state/pitwall/errors.jsonl` (Settings, Agent errors) |
| Timing log          | `~/.local/state/pitwall/timing.jsonl` (Settings, Timing)      |
| Socket              | `$XDG_RUNTIME_DIR/pitwall/pitwall.sock`, else `/tmp/pitwall-<uid>/` |
| Config and themes   | `~/.config/pitwall/` (`$XDG_CONFIG_HOME`)                     |
| Decision model key  | `~/.config/pitwall/credentials` (mode 0600), or `$TYPESAFE_API_KEY` |
| Paired phones       | `~/.local/state/pitwall/remote/` (token hashes, mode 0600)    |
| Web client token    | `~/.local/state/pitwall/web.json` (mode 0600, `pitwall web token --rotate`) |
| Web client open code | `~/.local/state/pitwall/web-open.json` (a one-time code's hash, `pitwall web open`) |
| Window state        | `~/.local/state/pitwall/gui.json` (sidebar shown or hidden)   |

macOS uses the same paths. On Windows, config and themes live in
`%APPDATA%\pitwall`, and saved tabs, the logs, window state and the
socket in `%LOCALAPPDATA%\pitwall`. The `XDG_*` variables win when set.

After a reboot, run `pitwall`: tabs, groups and panes come back in their
folders. State saved by an older version opens with each of its nested tabs
as a tab of its own, in the same place and group. Agent panes resume with `claude --resume <id>`,
`codex resume <id>`, `pi --session <id>` or `omp --resume <id>`, run as the agent's name on PATH or as the binary from the
pane's original command, with that command's options. Prompts, session selectors, print mode and
options the agent does not document are dropped, so a resume never sends the first prompt again. A
resume also keeps the permission mode the
agent's hooks last reported: Claude gets `--dangerously-skip-permissions` or `--permission-mode
<mode>`, and Codex gets `--dangerously-bypass-approvals-and-sandbox` when it ran with the full
bypass; Codex's other modes are left to its config. Other flags of an agent started in a shell, such
as `--model`, are not restored. If a resume fails within 3 seconds, the pane falls back to a shell
in the same folder. A restored agent in a pane that was a shell also drops to a shell in the same
folder whenever it exits. Running processes do not survive a reboot.

## Pane history

Pane history is on by default. A checkpoint saves the most recent 2000
rows of each pane's main screen and scrollback, including held commands. Restored panes show that
text with its SGR colors, then a dim `Restored from 14:32, Nov 3` divider
before the new process's output. Alternate-screen contents, images, links
and terminal modes are not restored. State files without history still load.

Saved text does not retain OSC 133 command blocks. Prompt jumps and the
command browser use marks from the new process's output.

Set `[terminal] persist_scrollback` to a whole number from 0 to 200000 to
change the row limit; 0 disables capture and restore. The daemon reads this
setting at each save and restore. A byte limit of 4 MiB per pane can reduce
the number of saved rows when lines contain long text or many colors.
History uses gzip inside `state.json`, so rolling snapshots carry the same
history as the state they copy. The daemon captures history every 30 seconds
and before shutdown or sleep. The one-second saves after tab changes reuse
the last checkpoint. A daemon crash can lose output since that checkpoint.
Capture and compression run outside the daemon's lock for client requests.

A resumed agent that returns to a shell keeps its history and final output.
Held commands keep their output without accumulating restore dividers or
exit notices in the saved text. A command that clears scrollback with
`CSI 3 J`, including `clear` in some shells, clears restored rows too.

Guarded tabs save and restore no history, including their shell splits.
To exclude an individual pane, add `env = ["PITWALL_NO_HISTORY=1"]` to its
agent preset in `config.toml`. Existing snapshots and backup files retain
the text saved when capture was enabled. These files can contain private
terminal output, including any secrets a command prints, and have the same
owner-only permissions as `state.json`.

Agent launches queue 100 milliseconds apart, with the most recently used
session first. Shells and other non-agent panes start immediately. Set
`[agents] startup_per_agent_delay_ms` to change the gap, or 0 to start all
agents at once. Closing a queued pane cancels its launch. Stopping the daemon
cancels the queue and saves the remaining panes for the next restart.

Self-reported agents can supply an exact resume command with
[`pitwall report-agent`](agent-report.md). pitwall stores that argv in
`state.json` format 10 and clears it when the agent releases the pane or
the pane returns to an idle shell.

pitwall never deletes saved tabs it cannot read. Before it upgrades
`state.json` to a newer format, it copies the old file to `state.json.prev`,
so you can copy it back for an older pitwall. A damaged `state.json`, or one a
newer pitwall saved, moves to `state.json.bad-<time>`, and pitwall starts
without those tabs and says so at the top of the window until you close the
notice. When a later pitwall can read a `state.json.bad-<time>` file, as
after you update past the version that saved it, it adds that file's tabs to
the ones you have and renames it to `state.json.restored-<time>`. A damaged
file stays where it is for you to fix or delete.

## Shutdown

The daemon saves your tabs a second after each change, and again when it
stops on SIGTERM, SIGINT or SIGHUP, which is how a login session ends.
From the moment it gets one of them, a pane whose program exits keeps its
tab, so an agent the shutdown stops first still resumes after the reboot.

On Linux with systemd-logind, the daemon also takes a delay lock on
shutdown and sleep over the system bus, the one `systemd-inhibit --list`
shows as pitwall's "Save open tabs". Before the system shuts down, logind
waits for the daemon to save and stop closing tabs, up to its
`InhibitDelayMaxSec`, 5 seconds by default; before it sleeps, logind waits
for a save. Without a system bus or logind, as in a container or a minimal
server, `daemon.log` says so once and the signals above still save.
macOS and Windows save on the signals only.

## Snapshots

The `snapshots` folder next to `state.json` keeps up to 48 copies of your
saved tabs, named by the time they were taken, as
`state-20261102-153012.250.json`, and the oldest goes when a 49th comes.
pitwall takes one:

- every 15 minutes while it saves a state with tabs, so the folder holds
  about 12 hours of them,
- each time the daemon starts with saved tabs, before it restores them,
- before a save with no tabs replaces a file with tabs, as after a failed
  restore or closing your last tab.

A snapshot without tabs, or one that does not load, never replaces your saved tabs. To go back to one:

```sh
pitwall snapshots                  # lists them, oldest first, with their tab counts
pitwall snapshots restore state-20261102-153012.250.json
pitwall
```

`restore` stops the daemon first, as Restart now does: programs in panes
stop, and agents resume when pitwall starts again with the restored tabs.
It copies the state it replaces to the folder before it writes, so you can
restore that one in turn. By hand, stop the daemon and copy a snapshot over
`state.json`.

## Upgrades

The window and the daemon speak a protocol with a major version and a
feature level. A new window works with a daemon of the same major version,
whatever either one's level, so after most upgrades the running daemon keeps
serving new windows and every program in a pane keeps running. The daemon
runs the old binary until it restarts, so features that need the new daemon
arrive then.

A release that changes the protocol's major version cannot use the running
daemon. Its first window asks: "pitwall <version> needs to restart its
background service; programs running in panes will stop." Nothing stops
until you choose:

- **Restart now** has the old daemon save its state and stop, then starts
  the new one. Tabs come back as after a reboot, with agents resumed.
- **Later** closes the window and leaves the old daemon and its programs
  running. Windows still open from before the upgrade keep working. Run
  `pitwall` again when you are ready to restart; it asks again.

`pitwall update` and the Update button skip that question on Linux and
macOS: the new daemon takes over the old one's panes without stopping them,
whatever the protocol versions. Only when that fails does pitwall fall back
to the restart above ([Updates](install.md#updates)).

A window from before the upgrade whose daemon was restarted by a newer one
reopens itself on the installed binary. CLI commands of the other major
version get an error until the restart. Agent hooks keep working across
major versions; see [Hooks after an upgrade](troubleshooting.md#hooks-after-an-upgrade).

When a window loses its daemon connection, for example because the daemon
crashed, it keeps showing the last screen under "Disconnected from pitwall's
background service." It reconnects on its own, starting a daemon when none
runs: at once, then 0.5 seconds after a failed try and twice as long after
each further one, up to 30 seconds. The Reconnect button tries at once. A
daemon started this way restores tabs as after a reboot.

Scrollback editor panes keep their temporary file's path in the saved state.
A daemon restart removes those panes and any remaining files instead of
relaunching the editor on a missing file. A live daemon handoff keeps the
editor process and transfers file cleanup to the new daemon. Closed tab
history skips scrollback editor panes.
