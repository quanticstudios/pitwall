# Demo data for the chat images

The README's hero and the desktop chat images come from a throwaway pitwall
with made-up tabs. Nothing here is a real repo, person or session.

- `chat.py` writes two Claude Code transcripts in Claude Code's own JSONL
  format, so the pane chat reads them through the same code as a real
  session: `webhook-retry.jsonl`, a finished turn (thinking, a todo list,
  Read, Grep, Write, Edit, a test run with colored output, a reply with a
  table and code), and `failed-page.jsonl`, a turn still running (`/effort`,
  two Explore subagents, one with its own transcript under `subagents/`, and
  a dev server in the background). It also writes the demo repo's files, so
  the sidebar's line counts match the diff.
- `claude` stands in for Claude Code in a pane. It sends the hooks a real
  session sends, naming its transcript, and draws a small TUI for Alt+T.
  `DEMO_STATE` is `done`, `working`, `approval` or `input`.
- `run.sh <pitwall>` builds the demo repo and its worktrees under
  `/tmp/pwmedia`, starts a daemon there with its own HOME, XDG folders and
  `CLAUDE_CONFIG_DIR`, and opens five tabs: the two chats, an approval, a
  question and `npm run dev`. It prints the command that opens the window.

To take the images:

```sh
go build -o /tmp/pwbin/pitwall ./cmd/pitwall
go test -c -o /tmp/pwbin/pitwall.test ./cmd/pitwall   # takes PITWALL_TEST_SIZE
env -u PITWALL_SOCKET -u PITWALL_PANE -u PITWALL_ATTACH sh docs/media/demo/run.sh /tmp/pwbin/pitwall
```

Then run the `agent-ws run pwmedia -- env ...` line it prints, from a shell
with your own environment: every temp variable goes after `--`, never into
agent-ws itself. `agent-ws shot pwmedia` takes the display; crop with
ImageMagick and save as WebP at the size and quality the image it replaces
uses. When done:

```sh
kill "$(cat /tmp/pwmedia/daemon.pid)"; agent-ws down pwmedia
```
