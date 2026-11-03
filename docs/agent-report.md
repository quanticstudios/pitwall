# Add pitwall support to your agent

An agent can report its name, state and exact resume command without a
built-in integration. Run `pitwall report-agent` from the agent's process
inside a pane. `PITWALL_PANE` identifies the pane and `PITWALL_SOCKET`
identifies its daemon. The command uses the same hook path as
`pitwall hook`.

Skip reports when `PITWALL_PANE` is unset. Send reports in order and ignore
delivery failures. The CLI prints nothing on success and waits at most
500 milliseconds for the daemon. It does nothing outside a pane or when
the daemon is unavailable. Invalid arguments return an error.

## State

Use the same `--name` on every report. The label appears in the sidebar
and can contain spaces.

```sh
pitwall report-agent --name 'My agent' --state working
pitwall report-agent --name 'My agent' --state blocked --message 'Choose a file'
pitwall report-agent --name 'My agent' --state done --message 'Tests pass'
pitwall report-agent --name 'My agent' --state idle
```

| State     | When to report it                                               |
| --------- | --------------------------------------------------------------- |
| `working` | A turn starts or continues after the user answers               |
| `blocked` | The agent needs a decision, permission or answer from the user  |
| `done`    | A turn finishes                                                 |
| `idle`    | The agent waits for a new prompt; this clears its last activity |

`blocked` and `done` use the same attention and notification rules as
built-in hooks. `--message` needs a state. The sidebar shows a shortened
message for an active state. `idle` clears the message along with the
activity, and keeps the agent's identity and resume command.

The name must be 1 to 64 bytes with no surrounding whitespace. A message
can be at most 1024 bytes. Both must be valid UTF-8 without control
characters. Changing the name clears the old activity and resume command.

## Resume

Report the argv that reopens the current session after `--`. Include every
option the session needs, such as the model or permission mode.

```sh
pitwall report-agent --name 'My agent' --state idle -- my-agent --resume "$session_id"
```

State is optional, so a session switch can update only the resume command:

```sh
pitwall report-agent --name 'My agent' -- my-agent --resume "$new_session_id"
pitwall report-agent --name 'My agent' --no-resume
```

Omitting the command keeps the stored argv for that name. `--no-resume`
clears it. Report the new argv whenever the agent switches sessions.
pitwall saves the argv in its state file and runs it in the pane's saved
directory after a daemon restart or reboot. The agent must report again
after resuming.

Self-reported agents use the same [startup queue and gap](state.md) as
built-in agents.

The argv can have at most 64 arguments and 8 KiB of argument text. Arguments
must be valid UTF-8 without control characters. The executable can be on
`PATH` or a path. pitwall executes the argv directly, so quotes inside an
argument stay literal and shell operators and variables do not expand.
Avoid credentials in argv because pitwall stores the command on disk.
The whole report can be at most 16 KiB of JSON.

Without a resume command, a shell pane returns to its shell after restart.
A command pane keeps pitwall's existing restore rules: a held command
without a resume command stays exited with its exit code unknown.

## Release

Release when the user quits the agent, not when a turn ends:

```sh
pitwall report-agent --name 'My agent' --release
```

Release clears the name, activity and resume command. It applies only if
the name matches the pane's current self-reported agent. It cannot include
a state, message or resume command.

If the agent exits without releasing, pitwall clears its identity and
resume command when the pane returns to an idle shell. This check usually
runs once a second. A tool taking the foreground does not release the
agent. A command pane that exits also loses its resume command.

## Built-in agents

Built-in hooks and process detection keep authority over Claude, Codex,
pi, Oh My Pi, Gemini, OpenCode, Cursor, Amp and Aider. pitwall ignores a
self-report when the pane has a built-in agent identity or its foreground
process identifies as a built-in agent. A built-in hook takes authority
from a self-reported agent and clears its resume command.

Custom labels use the `agent:` prefix internally. A label such as `codex`
therefore cannot select Codex's built-in resume or detection rules.
No hooks, config changes or plugin installation are needed.

## Programs and scripts

A build, a deploy script or any other program can report what it does
without being an agent. pitwall reads the Program Status Protocol,
OSC 7501 revision 0.2, which tuios, Rex and libghostty-vt also read. The
specification is at
[superlogical.com/rex/docs/build/program-status](https://www.superlogical.com/rex/docs/build/program-status).
A program writes the sequence to its terminal, or a script runs
`pitwall status` with a state:

```sh
pitwall status working --app cargo --progress 40 --message 'Compiling'
pitwall status blocked --app deploy --message 'Approve the deploy?'
pitwall status done --app cargo --message 'Built 12 crates'
pitwall status error --app cargo --message 'Linker failed'
pitwall status idle
```

```sh
# The same report as a raw sequence; msg is base64 of "Compiling".
printf '\e]7501;state=working:app=cargo:progress=40:msg=Q29tcGlsaW5n\e\\'
```

`pitwall status <state>` uses the same hook path as `report-agent`, with the
same rules: it does nothing outside a pane or without a daemon, prints
nothing and waits at most 500 milliseconds. An invalid argument returns an
error. `--app` is 1 to 32 bytes of letters, digits and `_ . + -`,
`--progress` 0 to 100 with `working` or `blocked`, and `--message` at most
2048 bytes without control characters. The sequence also takes `id` for
records under the pane's root record, `title`, and `kind` (`permission`,
`question` or `auth`) for a blocked program. pitwall answers the protocol's
query, `OSC 7501 ; ? ST`. A report that breaks a rule of the specification
is dropped whole.

`pitwall status` without a state, or with `--` and text, still sets the
line of text under the tab's row, as before.

### What the sidebar shows

The row gets a line for the program: its name in the state's color, its
message and a progress bar when it reports progress. A tab with several
agents shows each agent's program on its sub-row instead, as `cargo 40%`.

| Report    | The pane's state, where no agent owns it                |
| --------- | ------------------------------------------------------- |
| `working` | Working, in the inbox's Working section                 |
| `blocked` | Input, or Approval with `kind=permission`; Active       |
| `error`   | Error, Active; it waits on you                          |
| `done`    | Done, like a finished turn                              |
| `idle`    | Nothing; the line goes                                  |

When a pane has several records, the most urgent one shows: blocked, then
error, done, working and idle. `blocked` and `error` use the same attention
and notification rules as an agent's.

### An agent's state wins

A built-in or self-reported agent in the pane keeps its own state on the
row, its pill and the inbox. A build that the agent runs, which calls
`pitwall status` from the agent's shell tool, shows on the program line
under the agent's state, so the row says both: Claude is working, and
`make` is at 65%. Once the agent leaves the pane, the program's state
shows on the row again.

### How long a report stays

There is no heartbeat, and a program does not have to report again.

- `working`, `blocked` and `idle` go once no process is left in the
  process group that reported them, within about a second: the program
  exited, finished or crashed. They also go when the pane's command exits.
- `done` and `error` stay until you type into the pane.
- A report sent while the shell itself holds the terminal, such as
  `pitwall status working` typed at a prompt, has no group to watch. It
  stays until it is replaced or cleared, or until the pane exits.

On Windows, programs have no process groups, so `working` stays until the
program reports again or the pane exits.

Reports are not saved: after a daemon restart, a program reports again.

