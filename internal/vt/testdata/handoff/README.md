# Handoff PTY fixtures

Recorded on Linux at 80 columns by 24 rows with the adjacent Go recorder:

```sh
go run ./internal/vt/testdata/handoff/record.go /absolute/path/to/claude
```

- `vim.raw`: vim with no vimrc, swap or viminfo, a generated 52-line file,
  movement to the end and back, then quit.
- `htop.raw`: htop restricted to one disposable `sleep` process, a selection,
  then quit. It cannot expose other processes or their arguments.
- `claude.raw`: Claude Code's initial theme picker and login-method TUI,
  followed by Ctrl-C. No authenticated conversation or API request is needed.
- `log.raw`: 300 generated colored log lines, enough to scroll the screen.

The recorder passes an explicit environment and an empty temporary home to
children. It substitutes the temporary directory and local account name before
saving. No existing agent settings or credentials are loaded. The recordings
were inspected for local paths, account names and secrets. htop's process ID,
uptime, CPU and memory numbers are incidental and need not reproduce.

`TestHandoffRecordings` feeds the recorded bytes in 257-byte chunks, compares
JSON-encoded grids and ANSI-rendered frame bytes, restores after every chunk,
and continues the stream. It also compares every compact history row, leaves
the alternate screen and resizes. `TestHandoffEveryStreamBoundary` covers every
split inside a synthetic stream with UTF-8, OSC, CSI, DCS and charset changes.
The 10,000-line size test and benchmark generate a separate 120-column log.
