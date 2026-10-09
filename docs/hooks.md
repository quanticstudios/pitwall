# Hooks

Hooks are how Claude Code, Codex, pi, Gemini CLI, OpenCode and Cursor CLI
tell pitwall exactly what they are doing. Amp and Aider have no hooks;
pitwall recognizes them and reads what it can from their screens (see
[Watching agents](usage.md#watching-agents)).

| Agent      | What `pitwall hooks install` writes               | Installed when                                |
| ---------- | ------------------------------------------------- | --------------------------------------------- |
| Claude     | hooks in `~/.claude/settings.json`                | always                                        |
| Codex      | hooks in `~/.codex/hooks.json`                    | always                                        |
| Gemini CLI | hooks in `~/.gemini/settings.json`                | `gemini` is on `PATH` or the dir exists       |
| Cursor CLI | hooks in `~/.cursor/hooks.json`                   | `cursor-agent` is on `PATH` or the dir exists |
| pi         | an extension, `~/.pi/agent/extensions/pitwall.ts` | `pi` is on `PATH` or the dir exists           |
| OpenCode   | a plugin, `~/.config/opencode/plugins/pitwall.js` | `opencode` is on `PATH` or the dir exists     |
| Amp, Aider | nothing: they have no hooks                       |                                               |

For the JSON configs (Gemini's is in `$GEMINI_CLI_HOME` when set):

- It keeps every existing setting and hook and never adds a duplicate.
- It backs each file up first as `<file>.pitwall-backup-<unix time>` and
  writes atomically. A symlinked config stays a symlink.
- Add `--dry-run` to print each change and the files it makes without
  writing.
- Gemini reads its settings with comments, which the merge cannot keep, so
  a `settings.json` that has them is skipped with a warning; add the block
  `pitwall hooks` prints by hand.

pi has no shell hooks, so for pi the same command writes a small extension,
`~/.pi/agent/extensions/pitwall.ts` (under `$PI_CODING_AGENT_DIR` when set).
Inside a pitwall pane it runs `pitwall hook pi` in the background, one at a
time, on each prompt, tool call, finished run and session end, and sends the
tool's name but never its arguments. pi waits for it only when a session
ends (exit, `/new`, `/resume`, `/reload`), and then at most a second; a hook
still running after 5 seconds is killed. A missing binary or a stopped
daemon never fails pi. pitwall skips pi when `pi` is not on your `PATH` and
its agent directory does not exist. It replaces the extension only when
nobody edited it; an edited one is left alone with a warning, and the other
agents' hooks are installed as usual.

OpenCode takes plugins rather than shell hooks, so for OpenCode the command
writes `~/.config/opencode/plugins/pitwall.js` (under `$XDG_CONFIG_HOME`
when set), when `opencode` is on your `PATH` or that directory exists. The
plugin works like pi's extension: inside a pitwall pane it runs `pitwall
hook opencode` in the background, one at a time, on each prompt, tool call,
permission prompt, question, and finished, failed or aborted run of the main
session, never a subagent's. It sends the tool's name but never its
arguments, and replaces or removes only a file nobody edited.

Cursor's `hooks.json` lists commands per event. pitwall adds its command
to the events that only report (`afterAgentThought`, `postToolUse`,
`afterShellExecution`, `afterFileEdit`, `stop`, `sessionEnd` and a few
more) and sets `"version": 1` when the file has none. It leaves out the
`before*` events: Cursor reads their hooks' output as a decision, and a
hook that printed nothing could block the action. Cursor's docs do not say
which events its CLI fires, as opposed to the editor; the editor reads the
same file, and the hooks do nothing there unless it runs inside a pitwall
pane.

Inside Codex, run `/hooks` once to trust the new hooks, and restart agent
sessions that were already running (`/reload` in pi). `pitwall hooks
uninstall` removes only the exact entries pitwall added, and pi's extension
only when it is unedited. `pitwall hooks` prints the blocks and the
extension if you prefer to edit the files yourself.

The hooks do nothing outside a pitwall pane, so they are safe to keep
installed globally.

## Plan limits

Claude Code reports its 5-hour and weekly plan limits only to its status
line, not to hooks or its transcript. `pitwall hooks install --statusline`,
or the "Also show Claude Code's plan limits" box in the install dialog (off
by default), sets the `statusLine` command in `~/.claude/settings.json` to
`pitwall statusline`. If you already have a status line command, pitwall
keeps it as an argument: `pitwall statusline '<your command>'`. Each time
Claude Code redraws the status line, pitwall saves the `rate_limits` from
its input to `claude-limits.json` in pitwall's state directory, then runs
your command on the same input and passes its output and exit code through
unchanged. Saving never fails the status line. `pitwall hooks uninstall`
puts your command back, or removes the `statusLine` pitwall added. Codex
logs its limits in its session files, so it needs nothing.
