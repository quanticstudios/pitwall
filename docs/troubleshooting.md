# Troubleshooting

- **The window does not open from the launcher.** Errors are sent as a
  desktop notification (an alert on macOS, a message box on Windows); run
  `pitwall` in a terminal to see them.
- **An agent's state is missing or late.** Check `pitwall hooks install` was
  run with the installed binary, and in Codex run `/hooks` once.
- **The window freezes or is reported as not responding.** When one window
  event runs for over 2 seconds, pitwall writes every goroutine's stack to
  `~/.local/state/pitwall/stall-<time>.txt` and keeps the newest 5. Attach
  the newest stall file to the bug report. `gui.log` names it too, and notes
  frames slower than 250 ms, split into layout and rendering time.
- **Something else.** Look at the logs (below).

## Logs

`pitwall logs` prints the paths of the three log files, and `pitwall logs -f`
follows them as they grow. They live in the state directory. On Linux and
macOS the files are mode 0600 in a 0700 directory, so only you can read them;
on Windows they inherit the permissions of your profile folder. A file that
is over 5 MB when a window or the daemon starts moves to `<name>.1`,
replacing the older one; the size is checked at startup only. Two known
limits come with that: two processes starting at the same moment can lose
some lines in the move, and a window started before another one moved
`gui.log` keeps writing to `gui.log.1` until it restarts, which
`pitwall logs -f` does not show.

Every window writes events to `gui.log` and the daemon to `daemon.log`, one
line each, starting with the time, `gui` or `daemon`, the version and the
process id, so the lines of two windows can be told apart. They record
starts and stops, connections, panes starting and exiting (with the
program's name and exit code), sizes sent and applied, errors, slow requests
and frames. They never record what a pane shows, what you type, prompts,
hook payloads, environment variables or a command's arguments. They do hold
folder paths and pane ids.

`crash.log` is different. It holds Go's standard crash trace from windows
and the daemon, and anything the daemon prints before its log opens. A
crash trace is the panic value and the crashing goroutine's stack (more
goroutines if `GOTRACEBACK` asks for them), verbatim, and does not follow the
line format, so the panic value can hold any text pitwall was handling.
Read it before you share it.

For a bug report, attach `gui.log` and `daemon.log` from around the time it
happened, any stall file, and, after checking it, `crash.log`.
