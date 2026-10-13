# Driving pitwall from an agent

pitwall is a terminal multiplexer that knows which tabs run coding agents
(Claude Code, Codex, pi, and the ones it reads from the screen) and what each is
doing. These commands let you start an agent or a command in a tab and wait
for it. A tab you open without `-d` shows up in the user's sidebar, where
they can watch it, scroll it and type into it themselves; a detached one
stays hidden until someone attaches it.

Start an agent with its whole task as the prompt argument, then wait. To
work with another agent in turns, split it into a pane of your own tab,
prompt it and read its reply. pitwall types a prompt only into an agent
that reports through hooks and sits at its prompt, since only then can it
tell when the agent is ready and when its turn ends.

## Naming a tab

A tab is named by its number in `pitwall ls` (`3` or `#3`), its `id` from
`pitwall ls --json`, its exact title, or a unique prefix of its title.
Numbers shift when tabs close or move; the `id` does not. Inside a pitwall
pane, commands act on the pane's session; `-s <session>` picks another.

A pane is `TAB.N`, the tab's Nth pane in layout order, counting from 1:
`3.2`, `#3.2` or `review.2`. `wait`, `prompt` and `read` take one; a tab
alone means its main pane (see `wait`). `pane_list` in `pitwall ls --json`
numbers each tab's panes.

## Commands

`pitwall new [-n name] [-d] [dir] -- <cmd> [args...]` opens a tab in dir (the
current directory by default) running cmd instead of a shell, and prints its
number, as `#4`. `-d` keeps it out of the sidebar until attached; leave it off
when the user should see the work. When cmd exits, its pane stays with the
output on screen and the exit code recorded, until someone closes it. The
tab survives a daemon restart (an upgrade or a reboot), but its screen does
not: the pane shows a one-line notice instead. A command that had exited
keeps its exit code. An agent with a known session resumes. Any other
command still running is not run again, since that may not be safe: it comes
back exited with its exit code unknown (`exit_code` null, and `wait` prints
`exit unknown` and exits 3). A relative command path
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
| 3         | a daemon restart stopped the command, so its code is lost; prints `exit unknown`        |
| 124       | `--timeout` passed                                                                      |
| 1         | an error: no such tab, no daemon, the tab was closed                                    |

With `--until exit`, and `done` on a tab that never showed an agent, wait
exits with the process's own exit code instead (1 when a signal killed it,
3 when a daemon restart lost it).
The daemon pushes state in snapshots, so changes close together arrive as
one; wait judges the latest. An agent pitwall reads from its screen (no
hooks) is polled, so its state can lag or be misread.

`pitwall split [--down] [tab] -- <cmd> [args...]` opens a pane running cmd
right of the tab's last pane, or below it with `--down`, and prints its
address, as `#3.2`. Without a tab it splits the calling pane's tab. The
pane closes when cmd exits. A relative command path is the caller's, as
with `new`.

`pitwall prompt <pane> [--wait] [--timeout 10m] -- "text"` types text into
the pane's agent and presses Enter. The text can also be the last
argument, or come on stdin with `-` or no text. prompt first waits until the
agent is idle or done, so prompting an agent still on its last turn waits
for it; if the agent is blocked, it prints `blocked: <question>` and exits
2 without typing. The agent must report through hooks (`agent` in
`ls --json` names it, and `pitwall hooks install` sets them up); an agent
pitwall only reads from the screen is refused. A pane cannot prompt itself.
With `--wait`, prompt waits for the turn it started to end and prints the
agent's reply, its last message, on stdout. Exit codes are `wait`'s: 0 the
reply is printed, 2 the agent became blocked (`blocked: <question>`), 3 its
process exited, 124 `--timeout` passed, 1 an error, such as a turn that
did not start within 30 seconds or one that ended without a reply (an
interrupted turn).

`pitwall read <pane> [--screen]` prints the last message of the latest turn
the pane's agent finished, the same reply `prompt --wait` prints. The
daemon keeps it in memory, with secrets redacted and cut at 16000
characters, until the pane closes or the next prompt. After a daemon
restart, or before the agent finished a turn, there is none: read says so
on stderr and exits 1. `--screen` prints the text on the pane's screen
instead.

`pitwall ls --json` prints one object per tab of the session, in sidebar
order, detached tabs last. Every key is always present; unknown strings are
`""`.

```json
{"n":2,"id":"8c1f3a90d2e4b7a1","title":"review auth","group":"api","cwd":"/work/api","branch":"auth-review","agent":"codex","state":"blocked","question":"Run go test ./...?","exit_code":null,"panes":1,"detached":false,"pane_list":[{"n":1,"id":"5d0e9b2c71a4f836","agent":"codex","state":"blocked","question":"Run go test ./...?","exit_code":null}]}
```

`agent` is the agent's name (`claude`, `codex`, `pi`, `gemini`, `opencode`,
`cursor`, `amp`, `aider`, or another program a decision model reads from the
screen) or `""`. `state` is one from the table above,
for the tab's main pane. `question` is the agent's question or approval
detail while blocked. `exit_code` is `null` until the process exits, then
its exit code. `pane_list` has the same keys for every pane of the tab, in
layout order; its `n` is the N of the pane's address.

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
read a failure, until it is closed. After a daemon restart it keeps the exit
code but not the output. Close it with
`pitwall kill -f "$tab"`.

Discuss a plan with Codex in a pane next to yours, up to three rounds,
while the user watches both:

```sh
pane=$(pitwall split -- codex)
pitwall prompt "$pane" --wait --timeout 20m -- "Critique the plan in plan.md. Answer AGREE if you have nothing to add." > critique.txt
for round in 2 3; do
  grep -q AGREE critique.txt && break
  # revise plan.md with critique.txt, then:
  pitwall prompt "$pane" --wait --timeout 20m -- "I revised plan.md. Critique it again, or answer AGREE." > critique.txt
done
```

Check each prompt's exit code as in the review recipe: 2 means Codex waits
on a permission prompt for the user. You drive the turns. Codex never
needs to prompt you back: it answers each prompt with its reply, and
`--wait` hands that to you. Prompting while it still works on a turn waits
for that turn first. `pitwall read "$pane"` prints the last reply again.

## What this allows

Any process running as the user can already reach pitwall's socket, so these
commands add no access. They can start programs in tabs, read every tab's
state, and read an agent's last reply and screen. `prompt` types into an
agent only while it sits at its prompt with no form on screen. pitwall
never answers a permission prompt or a question for an agent: a blocked
agent waits for the user.
