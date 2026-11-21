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
- While the agent works, **Enter** queues the message above the box. When
  the turn ends, everything queued goes as one prompt, a blank line between
  messages. A slash command, a `!` command, and a message you marked **Send
  separately** each go in a turn of their own, in their place. A line above
  the queue says when the next messages go as one.
- **Ctrl+Enter** (Cmd+Enter on macOS) sends it now instead, into the running
  turn: Claude Code takes it once the running tool returns, Codex at once,
  Antigravity CLI through its Alt+M. In a New task chat whose agent takes
  nothing mid-turn, Send now stops the turn and sends the message first, and
  its tooltip says so.
- Point at a queued message for **Send now**, **Edit** (back into the box),
  **Move up**, **Move down**, **Send separately** and **Remove**. More than
  two fold into a count that opens on a click. A message that could not be
  sent stays with **Retry**.
- The queue never goes into a question, an approval, a plan or a menu: it
  waits, and the line above it says why ("Waiting for your answer"). After
  a failed turn it waits for **Send**; at a usage limit, until the limit
  resets, and it says when.
- **Esc** stops the turn and holds the queue until you send again or press
  **Resume**.
- The queue survives a daemon restart, an update and a reboot. After a
  restart it waits for the resumed agent's input box. A message pitwall was
  typing when it stopped comes back marked as maybe sent, with **Retry**,
  and never goes twice on its own.
- A tab with queued messages shows the count on its sidebar row and board
  card, "2 queued", or "2 held" when the queue waits for you. The phone page
  lists the queue and why it waits, with Send now and Remove.
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
model, effort and mode chips list agy's own models, efforts and modes and
set its options, approvals are cards
with Allow and Deny (agy offers no Always over ACP), images go with the
prompt, Esc stops the turn, and the chat resumes its conversation after a
restart. agy asks its
questions as a message there, so answer one by replying. "Antigravity
(terminal)" in New task starts the TUI instead.

A pane running agy's TUI shows as the chat too, once agy's hooks name its
conversation (`pitwall hooks install`); agy names it at your first prompt.
A pane pitwall resumed with `--conversation` shows as the chat at once.
pitwall never guesses from the newest folder under
`~/.gemini/antigravity-cli/brain`. The chat reads agy's transcript: your
prompts, replies, thinking, commands with their output and exit code,
edits as diffs, writes and file reads, and agy's to-do list (the `task.md`
in its conversation folder) as the task card. agy writes a call once it is
done, so a running command shows when it ends. The composer types into
the TUI, Esc stops the turn, and an approval is a card whose Allow, Always
and Deny press agy's own keys. The mode, model and effort chips read agy's
footer and set them the way you would: Shift+Tab, `/model` and `/effort`.
Answer agy's questions in the TUI (Alt+T).

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
