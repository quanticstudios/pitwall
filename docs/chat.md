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
- **Send now** on a queued message sends that one alone. The rest keep their
  places, and a held queue stays held.
- Queued messages stay with the agent they were written for. If it exits or
  another agent starts in the pane, they wait until you press **Send**, which
  gives them to the agent there now.
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
- **Paste or drop an image** and it shows as a thumbnail on the shelf above
  the text, with an `[Image #N]` chip in your text where it goes. Deleting
  the chip leaves the image on the shelf and in the message. The thumbnail's
  x removes it; while your text still names it, the composer asks first,
  then removes both.
- **Quote a reply**: select text within one reply and choose **Cite in
  composer** on the toolbar under the selection (**Copy** is there too). A
  `[Quote #N]` chip goes in at the cursor and its card on the shelf; the
  card's pencil adds a comment (Enter keeps it, Esc drops the edit), and a
  click on the card scrolls back to the passage and flashes it. The agent
  gets a Markdown quote with your comment after it. A quote takes up to
  8,000 characters.
- **Terminal output into the chat**: select text in a terminal pane and
  press **Ctrl+Alt+A** (Cmd+Option+A in mac, `add_to_chat`), or right-click
  and choose **Add to chat**. A `[Terminal #N]` chip goes into the composer
  of the tab's agent chat, else of the chat you last typed in. The agent
  gets the lines as a fenced block under the command that printed them,
  when shell integration (OSC 133) knows it, or the pane's own command.
- **Big pastes become files**: a paste of 32 KiB or more, or one that would
  take the message past 120,000 characters, becomes a `[Pasted text #N]`
  chip with a card that shows its name, size and first line.
  **Ctrl+Shift+V** keeps it inline. pitwall saves the text to a file in the
  tab's paste folder, which goes when the tab closes, and the agent gets
  the file's path where the chip stood, so it reads what it needs instead
  of the whole paste filling its context. That holds for Claude Code,
  Codex and Antigravity alike, in panes and New task chats: each reads the
  file with its own tools, and in Default mode an agent may ask first. A
  message takes 8 attachments, images and pasted texts together, and 8 MiB
  of pasted text.
- **/** lists the slash commands, with the ones that need the TUI marked.
- **1**, **2** and **3** answer an approval card; a question's options take
  their number keys and Enter.
- **Ctrl+Backspace** deletes a word, or a whole chip.

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
- The header's Tools button picks which tool calls show, as `[chat]
  tool_calls` does: Tools (all of them), Failed only (a call shows while it
  runs and stays only if it fails), No tools (thinking stays) and Zen (your
  messages and each turn's final reply, once the turn ends). Errors,
  approvals, questions and plans always show. In Zen, the find bar shows the
  words between calls while it is open.
- A work log's line names at most two kinds of call, commands and edits
  before reads and searches, then counts the rest, "Ran 2 commands, read 1
  file, and performed 3 other actions". A failed call is not counted as
  done: the line ends "and 1 command failed", and its icon turns red.
- A strip at the transcript's right edge has a mark for each of your
  messages, each error in red and each question in yellow. Hover one to see
  "Jump to message: …" with its first line; click to scroll to it. It hides
  when the chat is too narrow to leave room beside the column.
- When the agent stops on its plan's usage limit, the notice says when it
  resets and offers Continue at, which queues `continue` for a minute after
  the reset, and Snooze until then, which hides the tab in the inbox until
  that time. A queued continue, or pitwall's own resume when
  `[agents] resume_after_limit` is on, reads "Continues at 15:01" with
  Cancel. The queue survives a restart. Past the reset, Continue now sends
  it. This works for Claude Code and Codex, in panes and in New task chats;
  Antigravity CLI prints no usage limit message pitwall knows, so its chats
  show no notice.
- Long or old chats open at once: pitwall reads the transcript from its end
  and loads older turns as you scroll up.
- Ctrl+F (Cmd+F in mac) finds in the whole chat: your prompts, the replies,
  thinking and plans, older turns not loaded yet and long messages shown cut
  too: the background service searches the whole transcript, the window
  only what it shows. The bar counts "3 of 12"; Enter goes to the match
  above, Shift+Enter to the one below. A match in older turns loads them,
  and one in a folded work log, thought, plan or hidden turn opens it. Past
  5,000 matches the bar keeps the newest, and its count ends in +. An older
  background service cannot search: the bar then finds in what the window
  has loaded, and reads the older turns in while it is open.
- With the pointer on the Changes panel, Ctrl+F finds in every file of the
  turn's diff instead. Enter goes to the next match, Shift+Enter to the
  one before, and Escape closes the bar.

## Codex panes

A pane running Codex's TUI shows as the chat too, as soon as pitwall sees
Codex in it. The chat stays empty until Codex's hooks name its session,
which a fresh Codex does with its first prompt: send that from the chat's
composer, and the chat fills in. A pane pitwall resumed with `codex resume`
shows its history at once, from the rollout whose file name carries the
session id. pitwall never guesses the session from the newest log in
`~/.codex/sessions`. Run `pitwall hooks install`, then trust the hooks once
with `/hooks` in Codex.

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

A pane running agy's TUI shows as the chat too, as soon as pitwall sees agy
in it. The chat stays empty until agy's hooks name its conversation
(`pitwall hooks install`), which agy does at your first prompt. A pane
pitwall resumed with `--conversation` shows its history at once.
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
width = "standard"  # or "comfortable" (736 dp), "full": the transcript's and composer's column
tool_calls = "all"  # or "failed", "none", "zen": which tool calls show

[font]
chat_code_size = 0  # code in the chat; 0 follows chat_size
```

`standard` is 960 dp, enough for a 120-column diff. Reply prose stops near 96
characters at any width. [Configuration](config.md) has the rest.

The chat is for Claude Code. A chat tab that pitwall drives over an agent's
own protocol, such as a Codex chat from `pitwall chat new`, draws the same
way; [the command line](cli.md#chats) covers those.
