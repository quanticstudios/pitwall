# The desktop chat

pitwall shows Claude Code as a desktop chat: your prompts on the right,
Claude's replies as rendered markdown, each tool call as a row built for it,
and a composer at the bottom. The chat and Claude Code's own TUI are two
views of the same running claude, and Alt+T flips between them.

## Where it shows

Every pane that runs Claude Code's TUI shows as the chat, however claude
started:

- typed in a shell: `claude`, `claude --dangerously-skip-permissions`, or an
  alias that wraps either
- `pitwall new -- claude`, a worktree worker or an agent preset
- New task, its presets and the welcome card
- a tab restored after a restart

pitwall reads the session's transcript as Claude Code writes it, and the
pane's hooks tell it which transcript that is, so run `pitwall hooks install`
once. Nothing is started or restarted for the chat. Claude keeps the flags it
started with, bypass permissions included. What you type in the composer goes
into the TUI as keys, so the TUI's history and the chat's stay the same
conversation.

Each pane of a split decides for itself. When claude exits, the pane shows
the shell again.

## Alt+T: the chat or the TUI

Alt+T (Cmd+Option+T in the mac preset) flips the focused pane between the
chat and the TUI, mid-turn too. Use the TUI for the screens the chat does not
draw, such as `/config`, `/login` and plugin menus, then press Alt+T to go
back. Claude Code's own Alt+T toggle never reaches its TUI while pitwall
holds the key; rebind `chat_terminal` in [keys](keys.md) to give it back.

## The composer

- **Enter** sends; **Shift+Enter** adds a line.
- While Claude works, **Enter** queues the message above the box. pitwall
  holds it and types it when the turn ends, one per turn. **Ctrl+Enter**
  (Cmd+Enter on macOS) sends it now instead: Claude Code takes it into the
  running turn when it can.
- Point at a queued message for **Send now**, **Edit** (back into the box),
  **Move up**, **Move down** and **Remove**. More than two fold into a count
  that opens on a click. A message that could not be sent stays with
  **Retry**.
- **Esc** stops the turn and holds the queue until you send again or press
  **Resume**.
- **@** and part of a name lists the project's files and folders: git's
  tracked and untracked ones, not ignored. Up and Down pick, Enter or Tab
  puts `@path` in as a chip, and a folder lists what is in it. Claude Code
  reads the file, as an `@` typed in its TUI does.
- The **branch** chip shows the repo's branch, switched anywhere, and copies
  it on a click.
- **Shift+Tab** cycles the permission mode, and the mode chip opens a picker:
  Default, Accept edits, Plan, and Bypass permissions in a chat that runs in
  bypass. Bypass shows in red with a shield.
- The **model** and **effort** chips show what the TUI uses and pick another.
- The **context meter** shows how full the context window is and turns yellow
  at 75%.
- **Paste or drop an image** and it shows as a thumbnail, with an
  `[Image #N]` chip in your text where it goes.
- **/** lists the slash commands, with the ones that need the TUI marked.
- **1**, **2** and **3** answer an approval card; a question's options take
  their number keys and Enter.
- **Ctrl+Backspace** deletes a word, or a whole image chip.

## The transcript

- It follows the newest line while Claude writes and jumps to the end when
  you send.
- Each turn's tool calls fold into one work log, such as "Worked for 48s ·
  4 tools". Click it to open it.
- Replies render as markdown: tables, lists, quotes, code blocks and diffs,
  with Copy on each block and Copy reply under the reply.
- Each tool call has its own row. An edit shows its file, +N −M and the diff
  with line numbers. A command shows `$ command`, its output in color, how
  long it ran and its exit code, and a background shell reads Running in
  background until it ends. Read, Grep and Glob show their paths, a web
  search its sources, an MCP call its server, tool, arguments and result.
- A todo list is a checklist card, such as "3 of 7"; while the turn runs, a
  strip above the composer shows the task in progress.
- A subagent is a card with its description, type, time and tool count, its
  calls folded inside it.
- Thinking is a quiet "Thought for 12s" row. Your slash commands are chips,
  a compaction is a "Conversation compacted" divider, and API errors, retries
  and plan limits are cards.
- A finished turn's footer shows its time, tokens and cost.
- Long or old chats open at once: pitwall reads the transcript from its end
  and loads older turns as you scroll up.

## Codex panes

A pane running Codex's TUI shows as the chat too, once Codex's hooks name
its session. pitwall never guesses the session from the newest log in
`~/.codex/sessions`: until a hook names one, the pane stays the TUI. Run
`pitwall hooks install`, then trust the hooks once with `/hooks` in Codex.
A fresh Codex names its session with its first prompt, so a new pane turns
into the chat after you send one.

The chat reads Codex's rollout: your prompts, replies, reasoning summaries,
commands with their output and exit code, file changes as diffs, web
searches, MCP calls, the plan from `update_plan`, and the context meter.
Codex logs a call when it finishes, so a running command shows once it
ends. The composer types into the TUI, Esc stops the turn, and an approval
shows as a card whose Allow and Deny press Codex's own keys. The model and
effort show under the box; change them, the mode, and answer Codex's
questions in the TUI (Alt+T).

## Antigravity

New task starts Antigravity CLI (`agy`) as a desktop chat. pitwall runs
`agy acp` and drives it with its ACP driver ([ACP agents](acp.md)): the
model, effort and mode chips set agy's own options, approvals are cards
with Allow and Deny (agy offers no Always over ACP), images go with the
prompt, Esc stops the turn, and the chat resumes its conversation after a
restart. agy asks its
questions as a message there, so answer one by replying. "Antigravity
(terminal)" in New task starts the TUI instead.

A pane running agy's TUI shows as the chat too, once agy's hooks name its
conversation (`pitwall hooks install`); agy names it at your first prompt.
pitwall never guesses from the newest folder under
`~/.gemini/antigravity-cli/brain`. The chat reads agy's transcript: your
prompts, replies, thinking, commands with their output and exit code,
edits, writes and file reads. agy writes a call once it is done, so a
running command shows when it ends. The composer types into the TUI, Esc
stops the turn, and an approval is a card whose Allow, Always and Deny
press agy's own keys. Change the model, effort and mode, and answer agy's
questions, in the TUI (Alt+T).

## Settings

```toml
[chat]
claude = "chat"     # or "terminal": show Claude Code panes as the TUI, Alt+T away from the chat
codex = "chat"      # or "terminal": the same for Codex panes
antigravity = "chat" # or "terminal": New task starts agy's TUI, and agy panes show as the TUI
width = "standard"  # or "comfortable" (736 dp), "full": the transcript's column
```

`standard` is 960 dp, enough for a 120-column diff. Reply prose stops near 96
characters at any width. [Configuration](config.md) has the rest.

The chat is for Claude Code. A chat tab that pitwall drives over an agent's
own protocol, such as a Codex chat from `pitwall chat new`, draws the same
way; [the command line](cli.md#chats) covers those.
