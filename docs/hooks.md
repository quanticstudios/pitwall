# Hooks

Hooks are how Claude Code, Codex, pi, Gemini CLI and OpenCode tell pitwall
exactly what they are doing. `pitwall hooks install` merges pitwall's
entries into `~/.claude/settings.json`, `~/.codex/hooks.json` and, when
Gemini CLI is installed, `~/.gemini/settings.json` (in `$GEMINI_CLI_HOME`
when set):

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

Inside Codex, run `/hooks` once to trust the new hooks, and restart agent
sessions that were already running (`/reload` in pi). `pitwall hooks
uninstall` removes only the exact entries pitwall added, and pi's extension
only when it is unedited. `pitwall hooks` prints the blocks and the
extension if you prefer to edit the files yourself.

The hooks do nothing outside a pitwall pane, so they are safe to keep
installed globally.
