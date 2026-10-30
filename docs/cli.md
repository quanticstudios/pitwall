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
| `pitwall ls [--json] [--children] [--tree]` | List the session's tabs in sidebar order: #, name, state, since when, folder, group; `--children` lists only the tabs this pane's tab opened; `--tree` puts each tab under the tab that opened it |
| `pitwall new [-n name] [-d] [--issue n] [--preset name] [--model model] [--guard] [dir] [-- cmd...]` | Open a tab and print its #; `-d` leaves it detached; with `-- cmd` the tab runs cmd instead of a shell; `--issue n` adds GitHub issue n to cmd as its last argument, the prompt; `--preset` runs an [agent preset](usage.md#agent-presets) with cmd's args after its own; `--model` selects a model for this task, overriding a preset; `default` clears the model flag; `--guard` turns on [the guard](usage.md#the-guard). Run in a pane, the tab hangs under that pane's tab |
| `pitwall wait <tab> --until done\|idle\|blocked\|exit` | Block until the tab's agent gets there (`--timeout 10m` to give up); `--children` in place of the tab waits for every tab this pane's tab opened, and `--done-line` with it wants each worker's last line to be `DONE` or `BLOCKED: <why>` |
| `pitwall split [--down] [tab] [-- cmd...]` | Open a pane right of the tab's last pane, or below with `--down`, and print its address, as `#3.2` |
| `pitwall prompt <pane> [--wait] -- text` | Type a prompt into an idle agent and press Enter; `--wait` prints its reply when the turn ends |
| `pitwall send <pane> -- text`         | Queue a message for the pane's agent and return; pitwall types it, under a header naming the sender, at the agent's next prompt |
| `pitwall read <pane> [--screen]`      | Print the last reply of the pane's agent, or with `--screen` the text on its screen |
| `pitwall review [tab] [--agent claude\|codex]` | Second opinion: open the other agent read only beside the tab's agent to review its changes, and print its address; its first reply goes to the tab's agent |
| `pitwall attach [name]`               | Show a tab in the window, opening the window if needed        |
| `pitwall detach [name]`               | Hide a tab; its processes keep running                        |
| `pitwall rename [old] <new>`          | Rename a tab                                                  |
| `pitwall kill [-f] <name>`            | Close a tab and its processes; files are never touched        |
| `pitwall tab new`                     | Open a tab next to this pane's tab, in its folder             |
| `pitwall tab rename [name...]`        | Name this pane's tab; no name goes back to the automatic one  |
| `pitwall tab close`                   | Close this pane's tab                                         |
| `pitwall focus left\|right\|up\|down` | Focus the pane next to this one, in the windows that show this pane focused; vim runs it at its edge (see [Moving between vim and panes](keys.md#moving-between-vim-and-panes)) |
| `pitwall worktree ls [--all]`         | List this repo's worktrees: tab, branch, changed files, commits ahead of and behind the default branch; `--all` covers every repo pitwall has a group for |
| `pitwall worktree new <name> [--from <ref> \| --branch <branch> \| --pr <n>] [--preset name \| --lane lane] [--guard] [-- cmd...]` | Open a tab in a new worktree, `<repo>/.worktrees/<name>`, and print its #: a new branch off ref (the default branch without `--from`), an existing local or remote branch, or a GitHub pull request; with `-- cmd` the tab runs cmd. `--preset` and `--guard` work as for `new`. `--lane` (fleet mode) starts the lane's preset with the words after `--` as one brief and the worker contract before it. Run in a pane, the tab hangs under that pane's tab, in its group, guarded when `guard_workers` is on |
| `pitwall worktree lanes` | List the fleet lanes: small, normal and big, each with its preset and timebox (see [Fleet mode](usage.md#fleet-mode)) |
| `pitwall worktree rm <name> [--force]` | Remove a worktree and close its tab; with changes it lists them and stops unless `--force`. The branch stays |
| `pitwall worktree prune`              | Forget worktrees whose folder is gone, and list the ones under `.worktrees` that no tab uses |
| `pitwall completion bash` / `zsh` / `fish` | Print a completion script for pitwall's commands, e.g. `source <(pitwall completion bash)` or `pitwall completion fish \| source` |
| `pitwall queue ls` / `add [--issue n] [--preset name] [--model model] [--at HH:MM] [--every duration] [dir] -- cmd...` / `rm <n>` | List, add to or take from the session's task queue (see [Tasks](usage.md#tasks-and-the-queue)) |
| `pitwall chat new [--provider ID] [--model M] [--effort E] [--mode M] [--dir D \| --worktree NAME [--base B] \| --branch B] [--name N] [-- prompt]` | Open a [chat](#chats) in a tab of its own, send the prompt, and print the chat's id |
| `pitwall chat providers [--json]` | List the daemon's managed providers, including [configured ACP agents](acp.md) |
| `pitwall chat ls [--json]`            | List the session's chats: tab #, id, provider, state, pending answers, title |
| `pitwall chat send <chat> [--steer\|--queue] -- text` | Start a turn, or queue the text behind the running one; `--steer` adds it to the running turn where the agent can, `--queue` always queues |
| `pitwall chat approve <chat> [<request>] allow\|allow-session\|deny [--reason R]` | Answer a pending approval, the only one without a request id; refused inside a pitwall pane |
| `pitwall chat interrupt <chat>`       | Stop the running turn; queued messages wait for the next send |
| `pitwall chat set <chat> [--model M] [--effort E] [--mode M]` | Change the chat's model, effort or permission mode |
| `pitwall chat read <chat> [--json]`   | Print the transcript: prompts, replies, a line per tool call, and pending approvals with their ids; `--json` prints the snapshot |
| `pitwall chat wait <chat> [--until done\|blocked] [--timeout D]` | Block until the chat's turn ends or it waits on an answer; exit codes as `pitwall wait` |
| `pitwall chat archive <chat>`         | Close the chat and its tab, as `pitwall kill` does |
| `pitwall hooks install` / `uninstall` | Add or remove agent hooks (`--dry-run` to preview; `install --statusline`, see [Hooks](hooks.md#plan-limits)) |
| `pitwall doctor`                      | Check that the `pitwall` on PATH is this one, the daemon runs this version, each installed agent's hooks and skill are in place, and the config loads; prints `ok`, `FAIL` or `info` per check, with the fix under each FAIL, and exits 1 on a FAIL. It changes nothing |
| `pitwall statusline [cmd]`            | Claude Code's status line: save its plan limits, then run `cmd` |
| `pitwall jev login` / `status` / `logout` / `report` | Connect TypeSafe's Jev, test the connection, disconnect, report what it did (see [Decisions](decisions.md)) |
| `pitwall logs [-f]`                   | Print the log paths; `-f` follows both logs (see [Logs](troubleshooting.md#logs)) |
| `pitwall remote pair` / `devices` / `revoke` | Pair a phone, list paired phones, unpair one (see [Phone](phone.md)) |
| `pitwall web open`                    | Open the web client (`[web] bundle`) in the default browser, with a link that works once, within a minute (see [Web client](remote.md#web-client)) |
| `pitwall web token --rotate`          | Give local web clients a new token in `web.json`; the old one stops working at once (see [Web protocol](web-protocol.md#connection)) |
| `pitwall --version`                   | Print the version                                             |

`new --model ID` and `queue add --model ID` pass `--model ID` to the agent,
or `-m ID` to Codex. Put the option before `--`. Omit it to keep the agent's
or preset's model; use `--model default` to pass no model flag.

A name is a tab's `#` from `pitwall ls` (`3` or `#3`), its `id` from
`pitwall ls --json`, else its title. A
title matches exactly first, then by a unique prefix, so
`pitwall attach fix` finds the tab titled `fix login redirects`. Numbers
count within the session, and detached tabs come after the ones the sidebar
shows. A session is named in full or by a unique prefix. A pane is
`TAB.N`, the tab's Nth pane in layout order (`3.2`, `#3.2`,
`review.2`), or `@` and its `id` from `pitwall ls --json`, which stays
valid while tabs move; a tab alone is its main pane.

```sh
pitwall session new billing -d ~/src/billing   # a background session
pitwall new -s billing -n api -d               # a tab in it
pitwall ls -s billing
pitwall session attach billing                 # open a window on it
```

## Driving tabs from agents and scripts

`new -- cmd`, `wait` and `ls --json` let an agent or a shell script start an
agent or a command in a tab you can watch and take over, then wait for it.
`split`, `prompt`, `send` and `read` let one agent open another next to
it and discuss a plan, in turns it drives or with messages both ways.
`review` has the other agent review a tab's changes and send its findings
back.
[docs/agent-skill.md](agent-skill.md) is a page to hand an agent: the
states, exit codes and recipes.

```sh
tab=$(pitwall new -n review -d -- codex "review origin/main..HEAD; write SHIP or HOLD to review.txt")
pitwall wait "$tab" --until done --timeout 20m   # 0 done, 2 blocked, 3 exited, 124 timed out
pitwall ls --json | jq '.[] | {n, title, state, question}'
```

Give the agent its whole task as the prompt argument, or prompt it once it
runs: `prompt` types only into an agent that reports through hooks and is
idle or done, and never answers a permission prompt. A command tab's pane, and its exit code, last until you
close it, across daemon restarts too. A restart keeps the exit code but not
the output, resumes an agent with a known session, and does not run any
other unfinished command again: its exit code shows as unknown. Any process running as you can reach the
daemon's socket, so these commands give nothing a local process did not
already have.

## Chats

A chat is an agent that pitwall drives over the agent's own protocol (Claude
Code's stream-json, Codex's app-server) instead of a terminal. Each chat has
a tab of its own with no panes; the window shows a placeholder there until
the transcript view lands. `pitwall ls` lists chat tabs in a section after
the other tabs. A chat is named by its id, a unique prefix of it, or its
tab (`#N` or its title).

```sh
id=$(pitwall chat new --provider codex -- "fix the failing test")
pitwall chat wait "$id" --timeout 20m   # 0 done, 2 waiting on an approval, 124 timed out
pitwall chat read "$id"
pitwall chat approve "$id" allow        # the only pending approval
```

For now chats live in the daemon's memory: a daemon restart closes them and
their tabs. An older window or CLI, from before chats, does not see chat tabs.
[managed-sessions.md](managed-sessions.md) is the design.

