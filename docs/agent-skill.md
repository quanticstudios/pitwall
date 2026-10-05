# Driving pitwall from an agent

pitwall is a terminal multiplexer that knows which tabs run coding agents
(Claude Code, Codex, pi, and the ones it reads from the screen) and what each is
doing. These commands let you start an agent or a command in a tab and wait
for it. A tab you open without `-d` shows up in the user's sidebar, where
they can watch it, scroll it and type into it themselves; a detached one
stays hidden until someone attaches it.

Start an agent with its whole task as the prompt argument, then wait.
Typing into a running agent is not offered, because pitwall can't reliably
tell when an agent is ready to take input.

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
output on screen and the exit code recorded, until someone closes it or the
daemon restarts. A restarted daemon forgets that the pane stays: it closes
when the command ends, and its exit code is gone. A relative command path
(`./tool`) is the caller's, made absolute before the tab opens.

`pitwall wait <tab> --until done|idle|blocked|exit [--timeout 10m]` blocks
until the tab's agent reaches the state, and prints the state it saw. There
is no timeout unless you give one. It watches the tab's main pane: the first
live pane running an agent, else the first live pane, else the first exited
pane that ran an agent, else the first pane.

| State     | Means                                                                       |
| --------- | --------------------------------------------------------------------------- |
| `working` | a turn runs; for a tab without an agent, a command runs in its shell        |
| `blocked` | the agent waits on a permission prompt, a question or a plan approval       |
| `done`    | the agent finished its turn, or ended it with an error                      |
| `idle`    | the agent sits at its prompt with nothing new                               |
| `running` | no agent is known and the process runs: a shell at its prompt, or a command |
| `exited`  | the tab's process ended (`exit_code` has its status)                        |
| `""`      | an agent the tab was started with has not been seen running yet             |

`--until done` waits for `done`, or for `idle` after a turn it saw (an
interrupted turn ends idle). `--until idle` takes `idle` or `done`. Only an
agent is ever idle, so `--until idle` on a shell tab waits until the
timeout. `--until blocked` waits for a prompt or question. `--until exit`
waits for the process to end. On a tab that never showed an agent, `done`
means `exit`; once a tab ran an agent, its exit ends `--until done` with 3.

| Exit code | Means                                                                                   |
| --------- | --------------------------------------------------------------------------------------- |
| 0         | the state was reached                                                                   |
| 2         | the agent became blocked while waiting for something else; prints `blocked: <question>` |
| 3         | the process exited while waiting for something else; prints `exit <code>`               |
| 124       | `--timeout` passed                                                                      |
| 1         | an error: no such tab, no daemon, the tab was closed                                    |

With `--until exit`, and `done` on a tab that never showed an agent, wait
exits with the process's own exit code instead (1 when a signal killed it).
The daemon pushes state in snapshots, so changes close together arrive as
one; wait judges the latest. An agent pitwall reads from its screen (no
hooks) is polled, so its state can lag or be misread.

`pitwall ls --json` prints one object per tab of the session, in sidebar
order, detached tabs last. Every key is always present; unknown strings are
`""`.

```json
{"n":2,"id":"8c1f3a90d2e4b7a1","title":"review auth","group":"api","cwd":"/work/api","branch":"auth-review","agent":"codex","state":"blocked","question":"Run go test ./...?","exit_code":null,"panes":1,"detached":false}
```

`agent` is the agent's name (`claude`, `codex`, `pi`, or one pitwall reads
from the screen, such as `gemini`) or `""`. `state` is one from the table above,
for the tab's main pane. `question` is the agent's question or approval
detail while blocked. `exit_code` is `null` until the process exits, then
its exit code.

## Recipes

Hand a review to Codex in a tab the user can watch, and read the verdict
from a file it writes:

```sh
tab=$(pitwall new -d -n "review auth" -- codex "review origin/main..HEAD; write SHIP or HOLD and the findings to /tmp/review.txt")
pitwall wait "$tab" --until done --timeout 20m
case $? in
  0) cat /tmp/review.txt ;;
  2) pitwall ls --json | jq -r --arg id "$tab" '.[] | select("#\(.n)" == $id) | .question'
     echo "the review tab is waiting on you; answer it there" ;;
esac
```

A blocked tab needs the user: report its `question` from `ls --json` and let
them answer in the tab. pitwall never answers for an agent.

Run the tests in a visible tab and continue when they pass:

```sh
tab=$(pitwall new -n tests -- go test ./...)
pitwall wait "$tab" --until exit --timeout 15m   # exits with go test's status
```

The tab stays open with the output after the command ends, so the user can
read a failure, until it is closed or the daemon restarts. Close it with
`pitwall kill -f "$tab"`.

## What this allows

Any process running as the user can already reach pitwall's socket, so these
commands add no access. They can start programs in tabs and read every
tab's state. They do not type into running agents or answer their prompts.
