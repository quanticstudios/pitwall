# Where things live

| What                | Where                                                         |
| ------------------- | ------------------------------------------------------------- |
| Saved tabs          | `~/.local/state/pitwall/state.json` (`$XDG_STATE_HOME`)       |
| Saved tab backups   | `state.json.prev`, `state.json.bad-<time>` and `state.json.restored-<time>` next to it |
| Logs                | `~/.local/state/pitwall/gui.log`, `daemon.log` and `crash.log` (`pitwall logs`) |
| Decisions log       | `~/.local/state/pitwall/decisions.jsonl` (`pitwall jev report`) |
| Socket              | `$XDG_RUNTIME_DIR/pitwall/pitwall.sock`, else `/tmp/pitwall-<uid>/` |
| Config and themes   | `~/.config/pitwall/` (`$XDG_CONFIG_HOME`)                     |
| Decision model key  | `~/.config/pitwall/credentials` (mode 0600), or `$TYPESAFE_API_KEY` |
| Paired phones       | `~/.local/state/pitwall/remote/` (token hashes, mode 0600)    |
| Window state        | `~/.local/state/pitwall/gui.json` (sidebar shown or hidden)   |

macOS uses the same paths. On Windows, config and themes live in
`%APPDATA%\pitwall`, and saved tabs, the logs, window state and the
socket in `%LOCALAPPDATA%\pitwall`. The `XDG_*` variables win when set.

After a reboot, run `pitwall`: tabs, groups and panes come back in their
folders. State saved by an older version opens with each of its nested tabs
as a tab of its own, in the same place and group. Agent panes resume with `claude --resume <id>`,
`codex resume <id>` or `pi --session <id>`, run as the agent's name on PATH or as the binary from the
pane's original command, with that command's options. Prompts, session selectors, print mode and
options the agent does not document are dropped, so a resume never sends the first prompt again. A
resume also keeps the permission mode the
agent's hooks last reported: Claude gets `--dangerously-skip-permissions` or `--permission-mode
<mode>`, and Codex gets `--dangerously-bypass-approvals-and-sandbox` when it ran with the full
bypass; Codex's other modes are left to its config. Other flags of an agent started in a shell, such
as `--model`, are not restored. If a resume fails within 3 seconds, the pane falls back to a shell
in the same folder. A restored agent in a pane that was a shell also drops to a shell in the same
folder whenever it exits. Running processes and scrollback do not survive a reboot.

pitwall never deletes saved tabs it cannot read. Before it upgrades
`state.json` to a newer format, it copies the old file to `state.json.prev`,
so you can copy it back for an older pitwall. A damaged `state.json`, or one a
newer pitwall saved, moves to `state.json.bad-<time>`, and pitwall starts
without those tabs and says so at the top of the window until you close the
notice. When a later pitwall can read a `state.json.bad-<time>` file, as
after you update past the version that saved it, it adds that file's tabs to
the ones you have and renames it to `state.json.restored-<time>`. A damaged
file stays where it is for you to fix or delete.

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

A window from before the upgrade whose daemon was restarted by a newer one
reopens itself on the installed binary. CLI commands and agent hooks of the
other major version get an error or, for hooks, nothing, until the restart.

When a window loses its daemon connection, for example because the daemon
crashed, it keeps showing the last screen under "Disconnected from pitwall's
background service." It reconnects on its own, starting a daemon when none
runs: at once, then 0.5 seconds after a failed try and twice as long after
each further one, up to 30 seconds. The Reconnect button tries at once. A
daemon started this way restores tabs as after a reboot.
