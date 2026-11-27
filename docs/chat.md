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

### A New task chat's terminal

A chat New task started runs its agent headless, so its tab has no TUI to
flip to. Terminal in the chat's header, or Alt+T, opens one on the same
conversation: Claude Code's chats, Codex's and Antigravity CLI's.

- Going to the terminal stops a running turn first (the window asks), closes
  the chat's agent, and starts the TUI on the conversation in the tab:
  `claude --resume`, `codex resume <thread> --no-daemon` or
  `agy --conversation <id>`. The TUI keeps the chat's model, effort and
  mode. agy's TUI starts in Auto unless told otherwise, so a chat in agy's
  Default mode passes `--settings '{"agentMode":""}'`.
- Back to chat (or Alt+T again) quits the TUI: two Ctrl+C for Claude Code,
  Esc and two Ctrl+C for Codex and agy, and a closed pane after 3 seconds
  if it is still running. Quitting the TUI yourself does the same.
- Coming back, the chat adds the turns you ran in the TUI, read from the
  agent's own log (Codex's rollout, agy's transcript), and the agent picks
  the conversation up again, those turns included. A Claude chat does not
  show its TUI turns yet, though Claude still remembers them.
- One process holds the conversation at a time. The chat refuses messages
  while the terminal is open.
- Codex and agy write a conversation to disk with its first turn, so their
  terminal opens once the chat has one. A chat you rewound or forked waits
  for its next turn too.

Codex's TUI runs its turns in a background server it shares across every
Codex window by default, and that server keeps the conversation locked
after the TUI quits, so the chat could not take it back. `--no-daemon`
keeps the turns in the TUI's own process, which ends with it. agy 1.5.15's
TUI flags a conversation `agy acp` started with "Invalid Project"; the
turns still go to the same conversation, and the chat picks them up.

## The composer

- **Enter** sends; **Shift+Enter** adds a line.
- While the agent works, **Enter** queues the message above the box. It
  shows there at once, under a line that says it goes when the turn ends.
  When the turn ends, everything queued goes as one prompt, a blank line
  between messages. A slash command, a `!` command, and a message you marked
  **Send separately** each go in a turn of their own, in their place. A line
  above the queue says when the next messages go as one.
- **Ctrl+Enter** (Cmd+Enter on macOS) steers instead: the message goes into
  the running turn now, as typing it in the agent's terminal does, with
  every queued message ahead of it, in order, as one message, and the queue
  empties. A queued slash or `!` command keeps its place. On an empty box
  Ctrl+Enter steers the queue alone; while the agent is idle it sends with
  the queue first. Claude Code takes a steer once the running tool returns,
  Codex at once, Antigravity CLI through its Alt+M, in pane chats and New
  task chats. An agent that takes nothing mid-turn, as a New task
  Antigravity chat, queues on Ctrl+Enter too and says so; nothing stops its
  turn. `[chat] follow_up = "steer"` swaps the two keys. The box's
  placeholder names them. In a New task chat whose agent takes nothing
  mid-turn, Send now on a queued row stops the turn and sends it first, and
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
  In Claude and Antigravity chats it opens for a /word in the middle of
  the draft too, with skills and custom commands only.
- **$** in a Codex chat lists Codex's skills, anywhere in the draft, and a
  pick writes `$name`, which Codex reads as the skill. Codex reads a
  /word as text, so / opens only at the draft's start, where it lists the
  built-ins and the skills, and a skill picked there goes in as `$name` too.
- **1**, **2** and **3** answer an approval card; a question's options take
  their number keys and Enter.
- **Ctrl+Backspace** deletes a word, or a whole chip.

## Suggested next prompt

Turn on **Suggest next prompt** in Settings, or `[chat] suggest_next =
true`. After a turn ends, an empty composer shows a predicted next message
in muted text with a **Tab** key cap. **Tab** fills it in to edit or send,
typing replaces it, and **Escape** dismisses it.

Each suggestion is one headless run of the chat's own agent, under the
pane's or chat's account, so it uses some of that agent's plan:

| Agent       | Run                                                                   |
| ----------- | --------------------------------------------------------------------- |
| Claude Code | `claude -p` on haiku                                                  |
| Codex       | `codex exec --ephemeral` on its cheapest listed model, low effort     |
| Antigravity | `agy -p` on a flash model; pitwall deletes the conversation agy saves |

The run sees the last six prompts and replies, at most 8,000 characters,
not the tool calls. It saves no session and runs no tools, hooks or MCP
servers. pitwall asks once per turn and only while the composer is empty,
never while the agent works and never in a guarded tab. A turn that starts
stops a run still going. T3 Code asks the live session instead (Claude's
`--prompt-suggestions`, Codex's `thread/sample`), which reuses its prompt
cache; a pane chat has no such session to ask, so pitwall pays for a short
context on a cheap model instead.

## Since you left

Come back to a tab that no window had on screen and no phone had open for
5 minutes or more, while its agent worked, and pitwall says what happened:
how long you were away, the agent's state now (Working, Needs you or
Done), two summary lines (see below), how many messages and turns came,
the files it edited, how many commands it ran, the last one that failed,
and the last test run's result.

A window counts as having the tab on screen while it is focused and shows
that tab. The phone counts while it has the chat open in the foreground:
the Android app and the page tell the computer every 30 seconds that they
show it, and say so when they go to the background or the screen goes
off. A phone that stops telling, because the system froze the app or the
connection dropped, stops counting 75 seconds after it last did. Typing
in the agent's terminal counts as being there too.

Where it shows depends on how you look at the agent:

- **In its terminal** (the TUI in a pane), a card floats at the bottom of
  the pane, up to 560 dp wide. It never takes the keys: they still go to
  the agent, and typing closes the card. **Escape** or the **x** closes it
  without reaching the agent. **Open chat** shows the pane as pitwall's
  chat, for a Claude Code, Codex or Antigravity pane. A chat tab switched
  to its agent's terminal gets the same card, and Open chat goes back to
  the chat.
- **In pitwall's chat**, a marker in the log sits above the first message
  you missed, "Since you left · 47m". When what you missed is taller than
  the log's view, a card with the summary and the facts sits above the
  box too, with **First missed** to jump there and the **x**; otherwise
  the log already shows it all, and the marker is enough. The card also
  goes once you scroll up to the first missed message and back down. When
  Claude Code writes its own recap since you left, that recap is the card.
- **On the phone**, the Android app and the page follow the chat's rule:
  the marker always, the card when what you missed is taller than the
  screen.
- **From the command line**, `pitwall away [tab]` prints the cards and
  `pitwall away --dismiss <tab>` clears one; `pitwall ls --json` rows carry
  the card as `away`. Reading keeps it. See [Command line](cli.md).

Sending a message clears the card, and so does closing it anywhere: in
the pane, in the chat, on the phone or with `pitwall away --dismiss`.

With **Summaries** on in Settings (`[chat] away_summary = "on"`, the
default), the card adds two lines from the chat's own agent on its cheapest
model, the same headless run as a suggested prompt: where the work stands,
and what it needs from you. pitwall asks once per return, when the card
appears, never after each turn, and never in a guarded tab. The facts show
at once and the two lines when they come; a failed run leaves the facts
alone. **Off** shows the facts and calls no model.

The model gets only this, with secrets such as tokens, keys and passwords
replaced by `logs.Redact`:

- your last message to the agent, its first 1,500 characters;
- the agent's replies since you left, their last 6,000 characters.

No tool calls, command output, file contents or earlier turns go with it.

## Servers

The **Servers** tab of the chat's side panel lists the dev servers the tab
runs. While one runs, a globe button in the chat header opens it, and the
sidebar row shows its lowest port. Each row is `localhost:3000`, then who
started it, **Agent** or **Terminal**, the process name and its pid.

- **Open** opens it in your browser. A tab on another machine has no Open:
  its localhost is not yours.
- **Stop** sends SIGTERM to the server's job: its process group, so `npm run
  dev` and the node it started stop together, unless that group holds the
  agent or a pane's shell, when only the server's process gets it. One still
  running 3 seconds later shows **Force stop**, which asks before SIGKILL.

pitwall looks every 3 seconds at the processes under the tab's panes, in
their terminal sessions, and under its chat's agent process, and at the TCP
ports they listen on, on the wildcard or loopback address: from /proc on
Linux, `lsof` on macOS. It reads each process's parent, group, session and
name, never its arguments or environment. A server keeps its tab after the
shell that started it exits. The agents' own ports are left out. Codex runs
every turn in one app-server that all its TUIs share; a server it starts
goes to the one Codex tab whose folder holds the server's, and to none when
two do. A server that daemonizes into a session of its own leaves the tab.
Windows lists none.

## The transcript

- It follows the newest line while Claude writes and jumps to the end when
  you send.
- Each turn's tool calls fold into one work log, such as "Worked for 48s ·
  4 tools". Click it to open it.
- While a turn runs in a terminal tab, the line under the composer shows
  the agent's own spinner row in place of "Working…", such as
  "Flibbertigibbeting… 11s · ↓ 789 tokens · thinking" for Claude Code,
  "Working… 5s" for Codex or "Running command… 12s" for Antigravity.
  Antigravity's thought keeps its own Thinking row, and the line says
  "Thinking… 12s". That line shows in the Ledger layout and no longer lists
  key hints. A compaction, which runs no turn, shows in the transcript as
  "Compacting conversation… 7s · ↓ 122 tokens" until it ends.
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
  messages, each turn's final reply once the turn ends, and the agent's
  reports between calls: words of three lines or more, 240 characters or
  more, or a list, heading or table, also while the turn runs). Short
  narration such as "Let me check the tests" stays hidden in Zen. Errors,
  approvals, questions and plans always show. In Zen, the find bar shows all
  the words between calls while it is open.
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
(terminal)" in New task starts the TUI instead, and Terminal in the chat's
header opens it on the chat's conversation
([A New task chat's terminal](#a-new-task-chats-terminal)).

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
layout = "column"   # or "ledger": time/agent gutter, unrolled tool rows, plan strip and floating composer
width = "standard"  # or "comfortable" (736 dp), "full": the transcript's and composer's column
tool_calls = "all"  # or "failed", "none", "zen": which tool calls show
suggest_next = false # after a turn, offer a predicted next prompt; uses the agent's plan
away_summary = "on" # or "off": a Since you left card's two lines from the agent's cheapest model

[font]
chat_code_size = 0  # code in the chat; 0 follows chat_size
```

`layout = "ledger"` (Settings, **Chat layout**) switches the desktop chat live
without losing your scroll position or features: agent turns get a left gutter
with the time and agent name beside a 768 dp text column with each tool call as
its own row, your messages sit on the right as bubbles, the plan and changed
files sit in a strip under the header, approvals draw as compact cards, and the
composer floats at the bottom with a quiet status and key-hint line.

`standard` is 960 dp, enough for a 120-column diff. Reply prose stops near 96
characters at any width. [Configuration](config.md) has the rest.

The chat is for Claude Code. A chat tab that pitwall drives over an agent's
own protocol, such as a Codex chat from `pitwall chat new`, draws the same
way; [the command line](cli.md#chats) covers those.
