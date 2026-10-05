# Driving pitwall from an agent

pitwall is a terminal multiplexer that knows which tabs run Claude Code or
Codex and what each agent is doing. These commands let you open tabs, type
into them and wait for them. Every tab you open shows up in the user's
sidebar, where they can watch it, scroll it and type into it themselves.

## Naming a tab

A tab is named by its number in `pitwall ls` (`3` or `#3`), its `id` from
`pitwall ls --json`, its exact title, or a unique prefix of its title.
Numbers shift when tabs close or move; the `id` does not. Inside a pitwall
pane, commands act on the pane's session; `-s <session>` picks another.

## Commands

`pitwall new [-n name] [-d] [dir] -- <cmd> [args...]` opens a tab in dir (the
current directory by default) running cmd instead of a shell, and prints its
number, as `#4`. `-d` keeps it out of the sidebar until attached; leave it off
when the user should see the work. When cmd exits, its pane stays with the
output on screen and the exit code recorded, until someone closes it.

`pitwall send [-f] [--no-enter] <tab> <text...>` pastes the text into the
tab's agent pane (a tab without an agent: its first pane), then presses
Enter. Words after the tab join with spaces; put `--` before text that starts
with `-`. The paste is bracketed when the program asked for it, so newlines
stay part of one prompt. For an agent, send returns once the agent takes up
the prompt (at most 10 s), so a `wait --until done` right after it waits for
this turn. `--no-enter` pastes without pressing Enter. send refuses:

- while the agent is working, unless `-f`. Text sent into a busy agent queues
  or interrupts its input;
- always, `-f` or not, while the agent waits on a permission prompt, a
  question or a plan. pitwall never answers those for the user: tell the user
  the tab needs them, and let them answer in the tab;
- when the tab's process has exited.

A tab without an agent (a shell) takes text in any state.

`pitwall wait <tab> --until done|idle|blocked|exit [--timeout 10m]` blocks
until the tab's agent reaches the state, and prints the state it saw. There
is no timeout unless you give one.

| State     | Means                                                                   |
| --------- | ----------------------------------------------------------------------- |
| `working` | a turn runs; for a tab without an agent, a command runs                 |
| `blocked` | the agent waits on a permission prompt, a question or a plan approval   |
| `done`    | the agent finished its turn, or ended it with an error                  |
| `idle`    | the agent sits at its prompt with nothing new; a shell at its prompt    |
| `exited`  | the tab's process ended (`exit_code` has its status)                    |
| `""`      | an agent the tab was started with has not been seen running yet         |

`--until done` waits for `done`, or for `idle` after a turn it saw (an
interrupted turn ends idle). `--until idle` takes `idle` or `done`: the agent
is ready for input. `--until blocked` waits for a prompt or question.
`--until exit` waits for the process to end. On a tab without an agent,
`done` means `exit`.

| Exit code | Means                                                              |
| --------- | ------------------------------------------------------------------ |
| 0         | the state was reached                                              |
| 2         | the agent became blocked while waiting for something else; prints `blocked: <question>` |
| 3         | the process exited while waiting for something else; prints `exit <code>` |
| 124       | `--timeout` passed                                                 |
| 1         | an error: no such tab, no daemon, the tab was closed               |

With `--until exit`, and `done` on a tab without an agent, wait exits with
the process's own exit code instead (1 when a signal killed it).

`pitwall ls --json` prints one object per tab of the session, in sidebar
order, detached tabs last. Every key is always present; unknown values are
`""`.

```json
{"n":2,"id":"8c1f3a90d2e4b7a1","title":"review auth","group":"api","cwd":"/work/api","branch":"auth-review","agent":"codex","state":"blocked","question":"Run go test ./...?","exit_code":0,"panes":1,"detached":false}
```

`agent` is `claude`, `codex` or `""`. `state` is one from the table above, for
the tab's agent pane, else its first pane. `question` is set while blocked.
`exit_code` counts only when `state` is `exited`.

## Recipes

Hand a review to Codex in a tab the user can watch, then read the verdict:

```sh
tab=$(pitwall new -n "review auth" -- codex "review origin/main..HEAD, end with SHIP or HOLD")
pitwall wait "$tab" --until done --timeout 20m
case $? in
  0) pitwall send "$tab" "list only the HOLD findings, one per line"
     pitwall wait "$tab" --until done --timeout 5m ;;
  2) echo "the review tab is waiting on a permission prompt; answer it there" ;;
esac
```

Run the tests in a visible tab and continue when they pass:

```sh
tab=$(pitwall new -n tests -- go test ./...)
pitwall wait "$tab" --until exit --timeout 15m   # exits with go test's status
```

The tab stays open with the output after the command ends, so the user can
read a failure. Close it with `pitwall kill -f "$tab"`.

## What this allows

Any process running as the user can already reach pitwall's socket, so these
commands add no access. They can start programs, type into agents that are
idle and read their state. They cannot answer a permission prompt or a
question.
