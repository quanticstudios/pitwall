# pitwall

A native (Gio, pixel-drawn) terminal multiplexer for running coding agents. A
daemon owns PTYs and state; the GUI client draws an aide-style sidebar with
agent status next to split terminal panes. Sessions survive the window closing
(daemon) and a reboot (store + agent resume).

## Layout

| Package                | Owns                                                           |
| ---------------------- | -------------------------------------------------------------- |
| `internal/model`       | Shared types and aide's activity priority/tier logic           |
| `internal/layout`      | Split tree types and ops                                       |
| `internal/vt`          | `Emulator` contract and the emulator behind `New`              |
| `internal/pane`        | One process in a PTY, feeding a `vt.Emulator`                  |
| `internal/proto`       | Wire messages, gob framing, socket path                        |
| `internal/daemon`      | State owner, socket server, wiring of pane/agent/gitstat/store |
| `internal/agent`       | Claude/Codex hook payload to `model.Activity`                  |
| `internal/gitstat`     | Worktrees and branch stats via `git`                           |
| `internal/store`       | Persist state, resume commands                                 |
| `internal/logs`        | Log files, a non-blocking log writer, a line rate limiter      |
| `internal/decisionlog` | decisions.jsonl and `pitwall jev report`                       |
| `internal/update`      | Find a newer GitHub release, install it over the binary        |
| `internal/remote`      | The phone page: pairing, device tokens, its HTTP server        |
| `internal/qr`          | QR codes for pairing                                           |
| `internal/input`       | Gio events to PTY bytes                                        |
| `internal/ui/theme`    | Colors, derived tokens, type and spacing scales, from aide     |
| `internal/ui/anim`     | Eased values, the durations, reduce_motion                     |
| `internal/ui/kit`      | The button, popover placement and elevation                    |
| `internal/ui/sidebar`  | The sidebar                                                    |
| `internal/ui/term`     | Grid renderer and pane input                                   |
| `internal/ui/app`      | Window, splits, Alt navigation, switcher                       |
| `cmd/pitwall`          | Entry point (orchestrator only)                                |

Exported signatures in each package are the contract other packages build
against. Implement them as written; if one is wrong, say so in your report
instead of changing it.

## References

- aide (look and behavior to match): `../aide` (a sibling checkout), especially
  `src/renderer/src/components/SidebarTree.tsx`, `src/shared/workspace-activity.ts`,
  `src/main/git-service.ts`, the CSS tokens under `src/renderer/src`.
- tuios (MIT, Go, read-only, git-excluded): `.ref/tuios`.
  Copy whatever code we need from it. Put `// Adapted from tuios (MIT): <path>`
  above copied code. Take the part, not the framework around it.

## Rules

- Read `CONTRIBUTING.md` before preparing a contribution. Send every change to
  `main` through a pull request; GitHub blocks direct pushes for everyone.
- Go 1.27.1 via `mise.toml`. `gofmt`, `go vet ./...` and `go test ./...` pass
  before every commit.
- Stdlib first. A new dependency needs a reason in the commit message.
- One runnable test per non-trivial piece of logic. No test frameworks.
- GUI windows only through `agent-ws` (skill `agent-desktop`): never launch a
  window on the user's desktop. `agent-ws down <name>` when done.
- Never list processes with arguments (`ps aux`, `ps -ef`, `pgrep -a`,
  `/proc/*/cmdline`) and never run bare `env`/`printenv`: the shell carries
  secrets. `pgrep -c -f <pattern>` is fine.
- Never touch `~/.claude/settings.json` or `~/.codex/config.toml`.
