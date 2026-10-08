# Command line

Run these from any terminal. Tab commands act on the current session: the
calling pane's session inside a pitwall pane, else the most recently used
one; `-s <session>` picks another. Inside a pane, commands that take an
optional name act on the pane's own tab.

| Command                               | Does                                                          |
| ------------------------------------- | ------------------------------------------------------------- |
| `pitwall`                             | Open a window on the most recently used session (starts the daemon if needed) |
| `pitwall -s <name>`                   | Open a window on a session, made if missing                   |
| `pitwall session ls [--json]`         | List sessions: tabs, agents working and needing you, windows, last used; `*` marks the current one |
| `pitwall session new [name] [-d] [dir]` | Make a session with a shell in dir and open a window on it; `-d` does not |
| `pitwall session attach <name>`       | Open a window on a session, or raise the one showing it       |
| `pitwall session rename [old] <new>`  | Rename a session, the current one without old                 |
| `pitwall session kill [-f] <name>`    | End a session and close its processes                         |
| `pitwall ls [--json]`                 | List the session's tabs in sidebar order: #, name, state, folder, group |
| `pitwall new [-n name] [-d] [dir] [-- cmd...]` | Open a tab and print its #; `-d` leaves it detached; with `-- cmd` the tab runs cmd instead of a shell |
| `pitwall wait <tab> --until done\|idle\|blocked\|exit` | Block until the tab's agent gets there (`--timeout 10m` to give up) |
| `pitwall attach [name]`               | Show a tab in the window, opening the window if needed        |
| `pitwall detach [name]`               | Hide a tab; its processes keep running                        |
| `pitwall rename [old] <new>`          | Rename a tab                                                  |
| `pitwall kill [-f] <name>`            | Close a tab and its processes; files are never touched        |
| `pitwall tab new`                     | Open a tab next to this pane's tab, in its folder             |
| `pitwall tab rename [name...]`        | Name this pane's tab; no name goes back to the automatic one  |
| `pitwall tab close`                   | Close this pane's tab                                         |
| `pitwall hooks install` / `uninstall` | Add or remove agent hooks (`--dry-run` to preview)            |
| `pitwall jev login` / `status` / `logout` / `report` | Connect TypeSafe's Jev, test the connection, disconnect, report what it did (see [Decisions](decisions.md)) |
| `pitwall logs [-f]`                   | Print the log paths; `-f` follows both logs (see [Logs](troubleshooting.md#logs)) |
| `pitwall remote pair` / `devices` / `revoke` | Pair a phone, list paired phones, unpair one (see [Phone](phone.md)) |
| `pitwall --version`                   | Print the version                                             |

A name is a tab's `#` from `pitwall ls` (`3` or `#3`), its `id` from
`pitwall ls --json`, else its title. A
title matches exactly first, then by a unique prefix, so
`pitwall attach fix` finds the tab titled `fix login redirects`. Numbers
count within the session, and detached tabs come after the ones the sidebar
shows. A session is named in full or by a unique prefix.

```sh
pitwall session new billing -d ~/src/billing   # a background session
pitwall new -s billing -n api -d               # a tab in it
pitwall ls -s billing
pitwall session attach billing                 # open a window on it
```

## Driving tabs from agents and scripts

`new -- cmd`, `wait` and `ls --json` let an agent or a shell script start an
agent or a command in a tab you can watch and take over, then wait for it.
[docs/agent-skill.md](agent-skill.md) is a page to hand an agent: the
states, exit codes and recipes.

```sh
tab=$(pitwall new -n review -d -- codex "review origin/main..HEAD; write SHIP or HOLD to review.txt")
pitwall wait "$tab" --until done --timeout 20m   # 0 done, 2 blocked, 3 exited, 124 timed out
pitwall ls --json | jq '.[] | {n, title, state, question}'
```

Give the agent its whole task as the prompt argument. Typing into a running
agent is not offered, because pitwall can't reliably tell when an agent is
ready to take input. A command tab's pane, and its exit code, last until you
close it, across daemon restarts too. A restart keeps the exit code but not
the output, resumes an agent with a known session, and does not run any
other unfinished command again: its exit code shows as unknown. Any process running as you can reach the
daemon's socket, so these commands give nothing a local process did not
already have.
