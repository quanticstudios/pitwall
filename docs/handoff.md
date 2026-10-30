# Daemon handoff

Design for [ENG-2722](https://linear.app/quantic/issue/ENG-2722), part of
[pitwall#152](https://github.com/quanticstudios/pitwall/issues/152). A new
daemon takes over a running daemon's panes without their processes noticing:
the PTY masters move between the two processes, and every child keeps its
terminal. `pitwall update` does not use it yet; that is ENG-2723.

Linux and macOS only. On Windows `daemon.Takeover` fails at once and the
caller keeps today's restart path.

## Pieces

- The new daemon calls `daemon.Takeover(path, opts)`. It dials the old
  daemon's socket and sends `Hello{Kind: "handoff"}`. The old daemon serves
  that connection with `serveHandoff`, before its protocol version check,
  because the two builds may speak different `proto.Version`s. Handoff
  messages carry their own `handoffVersion`; a mismatch refuses the handoff.
- `pane.Pause`, `Resume`, `Save` and `Master` freeze a pane on the old side
  and export it. `pane.Prepare` builds the pane on the new side with its exit
  watcher registered and its emulator restored, but reads nothing until
  `Start`. `Release` gives a prepared pane up without signalling its process.
- Emulator state travels as `vt.Save` snapshots (ENG-2720). An unknown
  `vt.SnapshotVersion` refuses the handoff. Exits of processes the new daemon
  did not start are watched as [process-exit.md](process-exit.md) describes.

## States

```
old                                   new
serving
  <- Hello{handoff}, request ------- dial
freeze: hold d.mu, stop accepting,
  pause readers and input writers
send: state, panes, input, fds ----> receive
                                      prepare: restore emulators, register
                                        exit watchers, build the listener
wait for Ready (handoffTimeout) <--- Ready
Commit ----------------------------> wait for Commit (handoffTimeout)
                                      start readers and writers
wait for Started (no deadline) <---- Started
exit without closing panes            serve
```

Commit is the confirm point. The old daemon decides it, after the new one
says it is ready, so exactly one daemon ever reads the PTYs.

- Before Commit, any failure on either side (an error, a fault, a timeout, a
  closed connection, a `handoffAbort` from the new side) makes the old
  daemon resume: it gives every writer its unwritten input back, resumes
  every reader, accepts again and releases `d.mu`. The new daemon releases
  what it prepared and returns an error. Its copies of the PTY masters and
  the listener close; the old daemon still holds its own.
- After Commit the old daemon waits for `Started` with no deadline. The new
  daemon cannot fail between Commit and Started short of crashing: it only
  starts goroutines. A connection that closes without `Started` means the new
  daemon died before reading, so the old one resumes. A new daemon that hangs
  there leaves panes frozen, never split between two readers; ending it
  resumes the old one.
- The old daemon's own shutdown (SIGTERM) does not close the handoff
  connection, which would read as the new daemon dying; it waits for the
  handoff to end. While accepting is paused, closing the listener keeps the
  socket path, which the new daemon may be about to serve.
- On `Started` the old daemon forgets its panes, so nothing it still runs
  can close or signal them, and its `Serve` returns without saving state.
  Its listener closes without unlinking the socket path the new one serves.

## What moves

| Item | How |
| --- | --- |
| PTY master, one per running pane | fd over `SCM_RIGHTS` |
| listening socket | fd over `SCM_RIGHTS` |
| pane id, PID, size | `handoffPane` |
| screen, scrollback, modes, parser state | `vt.Save` snapshot |
| input a client sent that is not written yet | `handoffPane.Input`, in order |
| layout, tabs, sessions, held panes, activities, tasks | `model.State` |
| managed sessions' stdio pipes | `handoffBundle.Chats`, the seam for M4; empty, and a non-empty one is refused |

fds go 64 to a message, each with one data byte, after the gob messages.
Package net marks received fds close-on-exec, so a pane the new daemon
starts later cannot inherit another pane's master. A truncated control
message refuses the handoff. The sender reads each fd through `SyscallConn`:
`File.Fd` would switch the file description, shared with the old daemon's
own copy, to blocking mode.

Freezing a pane sets its read and write deadlines in the past. The reader
writes what it already read into the emulator and parks, so the snapshot
holds every byte read and the kernel buffer holds the rest. A write blocked
on a program that does not read stdin returns with the bytes it wrote; the
input writer keeps the rest and hands it over with its queue.

A pane whose process exited before the freeze but that the daemon has not
handled yet refuses the handoff: its exit belongs to the old daemon. A held
pane that already exited moves as a snapshot with its exit code. If a process
exits during the freeze, `pane.Prepare` cannot watch it and the handoff
refuses; the old daemon then handles the exit as usual.

## What does not move

The same things a restart loses today: each pane's last agent reply for
`pitwall read`, messages `pitwall send` queued for a busy agent, OSC
notifications, liveness timers, the running turn's timing and the conflict
radar's last run. Activities do move, so an agent mid-turn still shows
working.

A hook that fires during the freeze waits in the socket's backlog; one that
waits longer than its own 500 ms limit is dropped. A GUI's connection closes
when the old daemon exits and reconnects to the new one, as after a restart.
A message a client sent that the old daemon had not read by Commit, such as
a keystroke typed in that moment, is lost with it, as during a restart. An
emulator reply (a cursor position report, say) in flight at the freeze can
fail to reach its program.

## Fault injection

`handoffFault`, nil outside tests, runs at each step with its name, and an
error there fails the step: `old:freeze`, `old:send`, `old:ready` (after
Ready, before Commit), `new:receive`, `new:prepare`, `new:ready` (after
sending Ready) and `new:commit` (after Commit, before reading).

`TestHandoff` in `internal/daemon/handoff_test.go` runs the old and new
daemons as two processes of the test binary, with a socket and fifos in
their own temp directory. The old one serves ten panes: four shells, two
echo loops, a stream that never stops, a burst written only during the
freeze and larger than the PTY buffer, a program not yet reading 160 KB of
queued input, and a TUI on the alternate screen stopped inside an escape
sequence. A held tab that exited before the start and one that exits while
the old daemon runs come along. Each case fails one step, kills the new
daemon at `new:ready` or `new:commit`, or shuts the old daemon down at
`old:ready`. It then checks on whichever daemon serves: the state and held
exit codes, every burst line once and in order, the stream with no gap, the
escape sequence finishing as bold text, shells and echo loops answering
input sent before and after, and every queued input byte reaching the
program in order.

## For ENG-2723

`Takeover` returns a daemon and its listener; the caller runs `Serve`. The
lock next to the socket is held by the old daemon until it exits, so the new
one takes it with a blocking `flock` after `Takeover` returns, then writes
its PID there. On any `Takeover` error the old daemon is still serving.
