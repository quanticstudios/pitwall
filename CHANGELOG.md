# Changelog

Releases are `v0.1.1`, `v0.1.2`, ...: each release bumps the patch number
and becomes the latest release on GitHub, which is what the installers take.
Before them came `v0.1.0-beta.1` to `v0.1.0-beta.18`, `v0.1.0-alpha.1` to
`v0.1.0-alpha.24`, and the `v0.0.N` tags. A release is a git tag on `main`;
`scripts/install.sh` stamps the binary with `git describe`, and
`pitwall --version` prints it.

Changes land with their notes under `## Unreleased` at the top. To cut a
release from an up-to-date, clean `main`, run `sh scripts/release.sh`. It
renames `## Unreleased` to the next `## v0.1.N` (or the version you
pass) and opens a `Release v0.1.N` pull request. Once that merges,
`git pull --ff-only` and run `sh scripts/release.sh --tag`. It tags the
release commit on `main` and pushes the tag once, which starts the release
build. Either step takes `--dry-run`.

## Unreleased

- The daemon has a Claude Code driver for managed chats: it runs `claude -p` in stream-json mode, sends turns and maps streamed text, tool calls, usage and limits to chat events. Nothing starts it yet.
- `docs/web-protocol.md` specifies the protocol aide will use to drive chats over a localhost WebSocket, with a JSON Schema (`docs/web-protocol.schema.json`) tested against aide's delta fixtures. Nothing serves it yet.
- A new daemon can take over a running daemon's panes, PTYs and socket on Linux and macOS without the processes noticing, with output, queued input, scrollback and held tabs intact. A failure at any step leaves the old daemon serving. `pitwall update` does not use it yet.
- The phone page retries a port that is in use every 2 seconds. It waited for the settings to change.
- Pane exit tracking can follow transferred processes on Linux and macOS, including processes started by the previous daemon. Older Linux kernels report an unknown exit code for these processes.
- A group you made by grouping tabs shows their repo's icon in place of the folder when all its tabs are in one repo, its worktrees included. An icon you picked still wins, and the first tile in **Icon and color** brings back the repo's icon.
- A group header counts its tabs that need you, are working, or finished a turn you have not seen, each with its row's state glyph. It showed only a count of tabs with something unseen.
- Settings, Agents can send CI failures once per head commit and requested review changes once per review. Both are off by default, with one message held while an agent works. The existing `auto_send_failures` config key still works; ordinary tabs can now receive more than two failing commits, while fleet escalation keeps its limit.
- Terminal state can be saved and restored for a future daemon handoff, including scrollback and partially read escape sequences.
- New task has an editable model picker per agent, with Claude aliases and Codex cache suggestions, and remembers each agent's choice. `pitwall new` and `pitwall queue add` accept `--model`, including `default` to leave the model unset.
- Search all sessions from the command palette, or bind `search_all` to a key. Results group matching scrollback and agent transcript excerpts by tab, including detached tabs. Enter opens the tab at the scrollback line or shows the matching transcript excerpt.
- Choose named Claude Code and Codex account profiles in New task and New tab. Tabs keep their account after a restart, show its label and color, and track usage limits and limit resumes per profile.
- Usage-limit resumes defer to Claude Code's own auto-continue setting to avoid sending continue twice. The tab's hover card explains when Claude handles the wait. Codex resumes are unchanged.
- A Codex tab stopped at its folder trust dialog shows as needing you. It showed as idle.
- Drop image files onto an agent pane to paste their quoted paths. Clipboard images now use the same private save-and-paste path locally and over `--host`, with images kept under the state directory until their tab closes. A shell receives dropped paths and ignores clipboard images.

## v0.1.6

- Ctrl+V in a Claude Code or Codex pane of a `pitwall --host` window pastes an image from this machine's clipboard. The window sends it to the host, which saves it to a private file for a day, and pastes that file's path, which the agent attaches. It used to send Ctrl+V, and the agent found no clipboard on the host. A host whose pitwall predates this says so under the pane.

## v0.1.5

- Settings, Agents adds, edits and deletes presets: name, agent, model, effort, extra arguments and the guard. pitwall writes the model and effort as the agent's own flags in `config.toml`. Renaming a preset renames it in the fleet lanes; deleting one turns those lanes off.
- Settings, Fleet shows the agent, model and effort each lane runs, and has a New preset button.
- The phone page and its push notifications show PR open for a finished turn whose branch has an open pull request. They showed Done.
- Timing no longer charges a Claude Code or Codex call that sent no end, such as one another hook blocked, to its command until the turn ends. It counts for nothing once the agent moves on, and its time goes to the model, so a blocked `gh pr create` no longer shows 79 minutes. Old turns in `timing.jsonl` keep their numbers.
- A `pitwall --host` window shows each repo's favicon or logo, or its letter tile, in the sidebar, read from the repo on the host. Its rows used to show only the agent's mark. A host whose pitwall predates this gets letter tiles.
- Splitting a pane opens the new one in that pane's folder, so a `cd` in the shell carries over. It used to open in the session's folder.

## v0.1.4

- Fleet mode, off by default, in Settings, Fleet and under `[fleet]` in `config.toml`. Three lanes, small, normal and big, each name one of your presets; `pitwall worktree new <name> --lane small -- "<brief>"` starts the lane's preset on the brief, and `pitwall worktree lanes` lists them. The pitwall skill tells orchestrator agents to route each task to a lane by size.
- Fleet mode puts the contents of a worker contract file, set with `contract`, before every `--lane` brief.
- Fleet mode nudges a lane worker once when it runs past its lane's timebox with no pull request open, and tells the tab that started it once past twice the timebox.
- Fleet mode escalates a lane worker whose checks fail again after `auto_send_failures` sent them twice: it tells the parent tab, or with `escalate = "respawn"` starts the big lane in the same worktree with the diff stat, the failure and the old agent's last screen.
- A finished turn whose branch has an open pull request reads PR open, with a pull request icon, in the sidebar, `pitwall ls`, `ls --json` (`pr-open`) and the phone page. `pitwall wait --until done` and `wait --children` wait until the pull request merges or closes.

- In Claude Code and Codex panes, Ctrl+V and Ctrl+Shift+V both paste text or an image: text goes in as a paste, and with an image on the clipboard the agent gets Ctrl+V and attaches it. Codex used to answer Ctrl+V with text on the clipboard by saying it found no image.
- Pasting with only an image on the clipboard no longer stops every later paste in the window from working on Wayland.

## v0.1.3

- Settings, Agent errors shows a likely fix for known causes, such as unquoted globs in zsh or a missing command, and Copy report includes it. Ask an agent to fix these opens New task with the report as the prompt.
- Settings, Agent errors has charts: tiles for the errors, the tokens they took, errors per active day and the top cause's share, a bar per day stacked by kind, and a split by cause whose hover outlines the cause's group.
- `pitwall doctor` checks the install: that the `pitwall` on PATH is the one running, that the daemon runs the same version, each installed agent's hooks and skill, the config, and linger on a Linux host over ssh. It prints the command that fixes each failure and changes nothing.
- `pitwall wait --children --done-line` waits for each worker's last line to read `DONE` or `BLOCKED: <why>`. A worker that stops without one gets one nudge; a second miss prints `no-done-line`, and `BLOCKED` ends the wait like a blocked tab. The pitwall skill tells orchestrators to end every brief with that line.
- After a daemon restart, a resumed agent whose turn had finished still shows Done, so `pitwall wait --children` no longer waits forever on a worker that finished before the restart. One that was mid-turn is told to continue once it is back.
- `pitwall wait --children` no longer counts a worker done while a message from `pitwall send` waits to be typed into it.
- `pitwall ls` has a SINCE column and `--tree`, which puts each tab under the tab that opened it. `ls --json` adds `parent` and `since`.
- The pitwall skill checks for running workers before starting any, tells you the tab numbers it opened, asks workers for a report and a done line, and nudges workers that time out. A copy you have not edited updates on its own.
- Second opinion, in a tab's menu, the review view, the command palette and `pitwall review`, has the other agent review the tab's changes: Codex for Claude Code, Claude Code for Codex, your pick for any other agent. The reviewer opens read only in a split, and its reply goes to the tab's agent, which checks each finding before it changes anything.
- The hover card of a tab with an open pull request says why it cannot merge yet: conflicts, failed checks, changes requested (or awaiting re-review), checks still running, or no approval. A pull request ready to merge shows Ready on its chip, and `pitwall ls --json` has the reason in `pr_state`.
- A branch pushed to GitLab, Gitea, Forgejo or another host that is not GitHub has Open compare page in its tab menu, which opens the host's page for starting a pull request.
- Set `auto_send_failures = true` under `[git]` to send a pull request's failed checks to the tab's agent when CI fails, at most twice per branch and never while the agent works or GitHub still shows an older commit.
- A working agent whose screen has not changed for 20 minutes shows "quiet 20m" on its row in place of the time, and `pitwall ls --json` gives `quiet_since`. Set the minutes with `quiet_after` under `[agents]`. With `continue_when_quiet = true`, pitwall types `continue` into it once.
- `pitwall status [pane] -- text` puts a line of text, such as "CI 3/7", under a tab's row and in its hover card; without text it clears it. `pitwall ls --json` gives it as `status`.
- A Plan row has Request changes…: type what should change, and pitwall types it as your reply the next time the agent waits for input. It never answers the plan dialog for you.
- Agent presets: an `[[agents.preset]]` table in `config.toml` names an agent with its arguments, such as its model and effort, and environment. The New task dialog lists presets after the plain agents, and `pitwall new`, `pitwall worktree new` and `pitwall queue add` take `--preset NAME`. A resumed agent keeps them. Settings, Agents lists them.
- The guard: `--guard` on `pitwall new` and `pitwall worktree new`, or `guard = true` in a preset, makes Claude Code, Codex, pi, omp and OpenCode 1 refuse `git push`, `gh pr merge`, `terraform apply` and similar commands with a reason the agent reads. `guard_deny` under `[agents]` adds patterns, and `guard_workers` turns it on for every worker an agent opens with `pitwall worktree new`. It reads command text only: a seatbelt, not a sandbox. Run `pitwall hooks install` again for pi, omp and OpenCode.
- OpenCode 2 loads pitwall's plugin again, so its tabs show Working, Approval, Input, Done and Error and report failed commands. Run `pitwall hooks install` again: it adds `plugins/pitwall/tui.js` next to `plugins/pitwall.js`. OpenCode 1 needs version 1.4 or later.
- The update check reads the latest release from github.com's redirect instead of GitHub's API, so the Update button no longer goes missing when other tools on the machine use up the API's 60 requests an hour.

## v0.1.2

- Settings has a Timing category: how your agents' turns split between the model, tests, builds, CI, other tools and waiting on you, per day and per task from prompt to merge, with the slowest commands and a Copy report button. The daemon keeps it in `timing.jsonl`, redacted, with no prompts or output.
- An orchestrator's workers start folded into its row, so a fleet takes one row until you click its bar to list them.
- Settings has an Agent errors category: every failed tool call and API error from Claude Code, Codex, Cursor, Gemini CLI, OpenCode, pi and omp, kept redacted in `errors.jsonl` and grouped by cause over 7 days, 30 days or all time, by count or by tokens, with a Copy report button to hand the list to an agent to fix. Run `pitwall hooks install` again to update the OpenCode plugin and the pi and omp extension.
- After an update, pitwall offers to restart its background service whenever that service still runs the older version, not only when the protocol changed.
- The conflict radar marks a tab's row, and puts up its notice, only for an overlap with a tab whose pull request is open. Sibling worktrees still at work, like a fleet of agents, no longer mark each other's rows; the hover card still lists every overlap.
- omp tabs show Oh My Pi's own gradient π instead of a coral letter badge.
- A character the sidebar's font lacks, such as the π omp puts in its tab title, no longer draws as a box in bold and medium titles; it comes from the terminal font, as it already did in regular text.

## v0.1.1

- Oh My Pi (`omp`) works like pi: its tab shows Working, Done and Error, the side panel follows its session, and it resumes with `omp --resume <id>` after a restart. `pitwall hooks install` writes pitwall's extension to `~/.omp/agent/extensions/pitwall.ts` when omp is installed; with `$PI_CODING_AGENT_DIR` set, omp loads pi's copy, which reports as omp too.

## v0.1.0-beta.18

- A tab's cost reads `~$3.20` and says it is an estimate at API prices. The price table is checked again: Claude Sonnet 5.5's cache reads drop to $0.10, and Claude's fast mode, Opus 4.5, Sonnet 4.5 and GPT-5.6 Terra get prices.
- A worktree tab whose port block has a dev server listening shows its port as a chip, `:3010`, that opens `http://localhost:3010` in the browser. The hover card and the agent panel's Changes view list every open port of the block.
- New task can start from an open GitHub issue: **From issue** fills the prompt with the issue and names the worktree's branch `issue-<n>-<title>`. `pitwall new --issue <n>` and `pitwall queue add --issue <n>` do the same for the prompt. Ctrl+click in **Agent** picks several agents, which start one task each in their own worktree, side by side.
- The review view has a This turn scope: what changed on disk since the agent's latest turn started, from a snapshot the daemon takes when an agent with hooks gets a prompt. It opens there after a finished turn, and Undo turn puts the turn's files back, with Redo turn to bring them again.
- An agent that stops on its plan's usage limit gets `continue` typed into it a minute after the limit resets. The tab's pill shows when, and Cancel resume in the tab's menu or typing in the pane stops it. Turn it off with `resume_after_limit` under `[agents]` or in Settings.
- Send failures to agent and Send review comments to agent, in the tab menu, the command palette and the PR hover card, send a pull request's failed checks with the end of their logs, or its unresolved review comments, to the tab's agent as one prompt.
- Pinning a tab moves it to the top, after the tabs already pinned, as in a browser; unpinning puts it right after them. A grouped tab moves within its group.
- A tab an agent opens with `pitwall new` or `pitwall worktree new` joins the agent's group, so it hangs under the agent's tab even when the agent's tab is ungrouped and the repo has a group. `pitwall worktree new` no longer makes a group for the repo when run from an agent's pane.

## v0.1.0-beta.17

- Decisions get a measured rework. Triage asks two yes/no questions (blocked on you: now; nothing to do: fyi) in place of a rounded score that turned most split answers into "later", and holds out 20% of panes so `pitwall jev report` can tell whether it helps. Approvals with a risk pitwall sees show it without asking the model; the rest ask whether you asked for the call and whether it is hard to undo. New: check runs (`[decisions.check]`) catch a check that exits 0 but still shows a failure, skip or bypass, and mark the turn Check; stuck agents (`[decisions.stuck]`) flag an agent working 10 minutes on a loop as Working · stuck. The turn check is now measured by whether your next prompt reads as a correction, against a plain rule. Reading the screens of agents without hooks (`[decisions.agents]`) is removed: a config that sets it gets a note.
- An orchestrator whose turn ended while its workers still run shows Watching, a blue outline with an eye, instead of a Working pill that looked like its workers'.
- An SVG icon whose viewBox doesn't start at 0,0 draws in place, where oksvg drew it shifted and clipped. pitwall's own icon is its mark without the dark tile, which read as a black square on the sidebar.

## v0.1.0-beta.16

- A shell tab in a repo shows the repo's icon too, and a tab's icon follows the folder its first pane is in now, so `cd` into a repo picks up its icon. SVG icons that use `scale()` with one number draw again; oksvg read them as flattened to nothing. pitwall's own repo has `.pitwall/icon.svg`.

## v0.1.0-beta.15

- A tab in a repo shows the repo's icon, its favicon or logo (SVG, PNG or ICO, or `.pitwall/icon.*` to pick one), with the agent's logo as a badge on its corner, and the repo's group shows it in place of the folder. A repo with no icon gets a tile with its name's first letter.

## v0.1.0-beta.14

- The bar under an orchestrator's tab counts its stopped workers, the grey segments, in a muted chip with a power icon. A stopped worker's tab has no agent running: the agent exited or never started.
- The pitwall skill tells agents to close a finished worker with `pitwall kill`. They used to type `/exit` into it, which pitwall pastes and the agent may not run.
- Next and previous tab (Alt+J and Alt+K in the aide preset) skip the workers of an orchestrator whose workers are folded, and go to the next row you can see.
- `pitwall worktree new` works from a worktree of a bare repo, as in a `repo.git` beside `main/` layout. pitwall used to take the bare repo for the main checkout and refuse to group it.
- An orchestrator whose own turn ended shows Working while any of its workers work, instead of a green Done above blue workers.
- The working count under an orchestrator's tab spins its loader, so it no longer looks stuck. It stays still with reduce_motion on.
- The pitwall skill now covers requests for subagents: Claude Code and Codex start them as worker tabs. It also tells them never to open a window or session for a worker. The daemon updates an unedited beta.13 skill when it restarts.

## v0.1.0-beta.13

- Tabs an agent opens hang under the agent's tab in the sidebar. An
  orchestrator that starts ten workers with `pitwall worktree new` or
  `pitwall new` gets one row with a bar of their states and a count of how
  many need you, failed, work and are done; click it to fold the workers
  away. `pitwall ls --children` lists them and `pitwall wait --children`
  waits for all of them, stopping at the first one that needs you. The
  pitwall skill for Claude Code and Codex now tells them how, and the
  daemon updates a skill an earlier release installed.

- Ctrl+H, J, K and L can move between vim's windows and pitwall's panes, as
  vim-tmux-navigator does in tmux. Bind `navigate_left`, `navigate_down`,
  `navigate_up` and `navigate_right`, and add the few lines for Neovim or
  Vim from docs/keys.md, which call the new `pitwall focus` at vim's edge.
  The keys still reach vim, and a pane with no neighbour that way.

## v0.1.0-beta.12

- On macOS, the window opens. pitwall never started AppKit's event loop on
  the main thread, so the window never appeared (#122).
- On Wayland, a pane types the active keyboard layout's letters with three
  layouts or more, and on GNOME with two: GNOME adds the locale's layout to
  the keymap, so us and ru typed Latin letters after switching to Russian
  (#127).

## v0.1.0-beta.11

- After an update, when the daemon still runs the older pitwall, the
  sidebar's update button reads Restart to finish and restarts it for
  you, instead of the new features quietly doing nothing until you find
  and kill the old process.

## v0.1.0-beta.10

- Pin a tab from its menu so it cannot close by accident: closing it asks
  you to unpin it first, and `pitwall kill` refuses it.
- Color a tab from its menu: a row of eight colors tints its row.

## v0.1.0-beta.9

- A soft green mark under a tab's agent icon shows when Claude Code's
  `/remote-control` is on for it. Turn it off under Appearance in
  Settings, or with `remote_control = false` under `[appearance]`.

## v0.1.0-beta.8

- On Windows, a second message `pitwall send` queued for an agent no
  longer waits 30 seconds when the agent's turn starts within the same
  clock tick as the typing.
- The Subagents tab shows Claude Code workflows: a row for the run and one
  for each agent it starts, with their calls and replies. Claude Code
  2.1.299 writes workflow agents' transcripts a folder below the others,
  so pitwall did not see them.

## v0.1.0-beta.7

- Agents in pitwall panes can message each other in both directions.
  `pitwall send #3.2 -- "critique plan.md"` returns at once; pitwall types
  the message into that pane's agent the next time it sits at its prompt
  and you are not typing there, one message per turn, under a line that
  names the sender and the command to reply with, such as `pitwall send
  @8c1f3a90d2e4b7a1 "..."`. An agent that waited on another with
  `prompt --wait` while that one prompted it back would wait forever;
  messages wait in a queue instead. After 20 messages from agents into
  one pane, pitwall refuses more until you type in that pane, so two
  agents cannot talk on your quota forever. A pane address can now be
  `@` and the pane's id, which stays valid while tabs move, and
  `ls --json` counts each pane's `queued` messages. Restart the daemon to
  use send.
- `pitwall hooks install` adds a pitwall skill for Claude Code and Codex,
  `skills/pitwall/SKILL.md`, that tells them how to ask or message another
  agent in a pane, and that a message from one carries none of your
  authority. Run `pitwall hooks install` again, or press Install in
  Settings, to get it.

## v0.1.0-beta.6

- One agent can talk with another in a split pane of its tab while you
  watch. `pitwall split -- codex` opens Codex next to the calling pane and
  prints its address, as `#3.2`. `pitwall prompt #3.2 --wait -- "critique
  plan.md"` waits until Codex is idle, types the prompt, waits for that
  turn and prints Codex's reply; `pitwall read #3.2` prints it again, and
  `--screen` prints the pane's screen. pitwall types only into an agent
  that reports through hooks and sits at its prompt, and still never
  answers a permission prompt. `wait` takes a pane address too, and
  `ls --json` lists each tab's panes in `pane_list`. Restart the daemon
  to use prompt and read.
- A slash command such as /compact or /model no longer leaves the tab on
  Working. Claude Code ends those without a Stop, so pitwall now waits for
  the first tool call before it shows a command as working.
- The README and the site cover features they left out: an agent starting
  and waiting on another agent's tab with `pitwall new` and `pitwall wait`,
  the sidebar's keyboard focus, shortcut search by keys, and the Update
  button.

## v0.1.0-beta.5

- The sidebar works from the keyboard. Ctrl+Shift+H (Ctrl+Shift+B in
  aide, Cmd+Shift+B in mac, or "Focus sidebar" in the command palette)
  puts a focus ring on the shown tab's row. Arrows or j and k move through
  group headers, tabs and their agents, Left and Right open and close
  groups, Enter shows a tab or focuses an agent's pane, Space shows a tab
  and stays, Shift+F10 or the Menu key opens the row's menu, F2 renames,
  and Delete opens the Delete dialog for the tab. Esc, or the same chord,
  goes back to the pane. No key reaches a pane meanwhile, a hidden sidebar
  shows until you leave, and the footer lists the keys. Rebind it with
  `focus_sidebar`.
- You can search keyboard shortcuts by their keys. Typing `ctrl+shift+d`,
  `shift ctrl d` or `⌃⇧D` in the command palette or the settings search
  lists only what Ctrl+Shift+D runs; before, it listed every action with a
  D in it, and `cmd+shift+r` found nothing. Modifiers go in any order, and
  cmd, ⌘, win, meta, option, ⌥, ⇧, return, esc, del, pgup and the arrows
  all work. A chord nothing uses says so.
- The Keyboard shortcuts page has its own search field, which keeps its
  Shortcuts, Sessions, Tab mode and Pane mode sections, and a Record keys
  button: press a chord and see every action bound to it, in tab and pane
  mode too.
- Token counts that miss the start of a long session file end in "+" (`2M+`) instead of a "≥" the UI font cannot draw, and counts of a billion or more read `1.1B` instead of `1076M`. The review view's folded lines start with "…".
- The README and the site lead with what pitwall does for you, one short
  section per outcome with a new screenshot: the sidebar, answering from
  your phone, the review view, worktree conflict warnings, New task, plan
  limits, the session switcher and the command palette. The first-run
  walkthrough moved to `docs/usage.md` and the platform table to
  `docs/install.md`.

## v0.1.0-beta.4

- On Windows, closing a pane or stopping the daemon ends the pane's
  programs within 2 seconds, children included. A program that held off
  the console's close event, or a paste stuck in a program that stopped
  reading, could stall the daemon's shutdown for 10 seconds or more.
- The command palette fits its results instead of keeping a fixed height,
  lists the actions you ran last first, and shows the letters you typed in
  bold. "Go to tab 1–9" with Alt+1…9 replaces nine Go to tab rows, and
  Split the pane below and the other pane mode actions that repeat a window
  action are listed once with all their keys. When nothing matches, it says
  what you can search by.
- The find bar has up, down and close buttons, and its border turns red
  when nothing matches.
- The session switcher's preview shows the session's tabs and groups
  without the sidebar's header and footer buttons.
- A tab running two or more agents lists them under its row: each agent
  pane gets a small row with its logo, its own title and its state, and
  the asking one gets Allow and Deny there. Clicking one focuses that pane.
  The tab row keeps the most urgent state and counts them, "3 agents · 1
  approval"; clicking the count folds the list. The hover card lists every
  agent too.
- The open tab's row is a plain primary tint with a thin outline, without
  the yellow or red of its state, which stays in its pill and accent bar.
  Group headers have a chevron that turns as they open. Rows drop the dot
  beside the pill, show "Merge conflicts" as a red mark (the hover card
  says it in words), and skip a second line that repeats the title, such
  as "~" under "~". Row heights follow `[font] ui_size`, and keyboard
  focus on a row or a menu entry shows a ring.

- Settings fits a narrow window. Below 760dp the category list becomes a
  dropdown next to the search field, and a row whose label would get
  narrower than 220dp puts its control under the label instead of
  squeezing the label to a few letters a line.
- Settings polish: the Close button shows Esc as its hint instead of a
  separate keycap, the theme cards sit four to a row, or two by two in a
  narrow window, switches have a white knob, Clipboard from programs and
  Bell read "Allow" and "Mark the tab" instead of their config values, and
  rows, switches, segments and buttons show a focus ring under Tab.
- The side panel says "Reading git…" with a pulsing dot until git
  answers, then either lists the changes or says the folder is not a git
  repository, where it used to show one line for both. Every tab's empty
  state, and the review view's, has an icon and one short line.
- Decision stats hides its 7 days, 30 days and All time picker until the
  log has something in it, and offers Set up decisions while no provider
  is connected. Usage says it is reading your agents' session files while
  it scans.
- Open folder as group keeps the path as you typed it after an error,
  turns the field's border red and gives a one-line reason with `~` for
  your home folder. The Delete dialog shows the worktree folder with `~`
  too, wrapped after a `/` rather than inside a name.
- The daemon sends a window only the panes it draws, and of those only
  the rows that changed. Agents in other tabs and sessions keep running,
  and a pane shows its current screen as soon as you switch to it. With 32
  busy panes and four on screen, a window receives 0.2 MB/s instead of
  34.
- Secondary text reads at 4.5:1 or better in every built-in theme:
  tokyo-night, aide-light and catppuccin-mocha get a slightly stronger
  muted color, and status chips darken or lighten their text where it
  was faint, most visibly in aide-light.
- `[appearance] reduce_motion`, also a switch under Settings, Appearance,
  shows dialogs, menus, hover and focus changes at once instead of easing
  them in.
- The interface text size in Settings now scales the sidebar, menus,
  hover cards, the command palette, the side panel and dialogs too, on one
  scale of five sizes. The smallest labels, such as status chips and diff
  counts, are 11px instead of 10px.
- Dialogs fade and scale in over 150ms on one backdrop, the same as the
  command palette's, and none of the window's shortcuts run behind an open
  dialog: Ctrl+Shift+L no longer opens the agent panel under it.
- The first run asks to install hooks once, on the welcome card, which now
  dims the panes behind it. The pane notice shows after the card is gone.
- A tab's menu near the bottom of the sidebar opens above its "…" button
  instead of running off the window, and every menu, submenu, hover card
  and the group icon picker stays inside the window. Menus fade in over
  120ms.
- The pane focus border crossfades over 120ms, a sidebar row's fill eases
  over 80ms on hover and 160ms when its state changes, and a group's tabs
  fade in as it opens. A focused pane that also wants attention keeps a
  thin accent line inside the attention ring.

## v0.1.0-beta.3

- On Windows, `pitwall.exe` is a GUI program with pitwall's icon: it opens
  no console window, and `get.ps1` and Scoop put it in the Start menu.
  Commands run from cmd or PowerShell still print to that terminal, and
  hooks run by an agent stay silent.
- On Windows, the Update button works. It renames the running
  `pitwall.exe` to `pitwall.exe.old`, puts the new one in its place, and a
  later window deletes the old one.
- On Windows, a toast shows within moments rather than after PowerShell's
  ten-second start, and a tab's next toast replaces its last one.
- On Windows, a tab's folder follows `cd` in PowerShell and cmd.
- On Windows, git and gh run without a console window, a project's
  folders resolve against the paths git prints, the folder dialog
  completes paths typed with `\`, a tab no longer takes the program's path
  that Windows titles a new console with as its title, and a hook still
  reaches a daemon of an older version when Windows drops that daemon's
  refusal.

- A sidebar row whose Claude Code or Codex asks permission has Allow and
  Deny buttons, and Ctrl+Shift+Y and Ctrl+Shift+D press them for the
  focused pane. They take the phone page's path: the key goes only to the
  prompt the row showed, while the pane still shows it.
- On Linux, clicking a desktop notification raises pitwall on its pane, and
  an approval's notification has Allow and Deny. It needs notify-send from
  libnotify 0.7.10 or later.
- `[notifications]` in config.toml, and Settings > Notifications, turn
  notifications off per state or per agent, add a sound, and set quiet
  hours (`quiet_hours = "22:00-08:00"`) that keep only approvals, errors
  and urgent questions, silently.
- The phone page can push notifications to your phone when an agent
  needs you, with the page closed or the phone locked. Tap Turn on in
  the page; on an iPhone, add it to the Home Screen first. pitwall pushes
  what the desktop notifies, by the same rules, even with every window
  closed, and tapping one opens the page on that tab. Settings, Phone
  shows which devices get pushes and sends a test. Push needs a
  certificate the phone trusts, such as `tailscale serve` gives.
- The phone page can be added to the Home Screen as an app, and pairs by
  a typed code as well as by the QR code.
- Settings, Usage starts with Limits: how much of Claude Code's and
  Codex's 5-hour and weekly plan limits you have used, when each window
  resets, how old the report is, and when you hit 100% at the current pace.
  Codex's come from its session files. Claude Code reports them only to its
  status line, so `pitwall hooks install --statusline`, or a box in the
  install dialog, runs your status line through `pitwall statusline`, which
  saves them and prints your own status line unchanged. A window at 90%
  shows a notice once, and `limits_in_sidebar = true` under `[usage]` adds
  a meter to the bottom of the sidebar that turns yellow at 80% and red at
  95%. pitwall never asks Anthropic or OpenAI for them.
- A tab whose branch has a pull request shows it on its row: the number in
  the PR's state color, a CI dot and the review, with each check by name
  in the hover card. Click it to open the PR. The tab's menu and the
  command palette gain Open PR, Merge PR (asks first, and again when checks
  failed; `[git] merge_method` picks squash, merge or rebase) and Re-run
  failed checks. A merged tab says Merged and offers Archive, which closes
  it and removes its worktree and branch; `[git] archive_on_merge = true`
  does that on its own. The daemon asks `gh` about each branch every minute
  or so, slower while no window has focus, and shows nothing without `gh`
  or a login.
- pitwall knows more agents. Cursor CLI, Amp and Aider get a logo in the
  sidebar and a line on the welcome card, and so do Gemini CLI and
  OpenCode, which had neither. `pitwall hooks install` sets up Cursor's
  hooks in `~/.cursor/hooks.json`.
- An agent installed through npm or pip, which runs as `node`, `bun`,
  `deno` or `python`, is recognized by its script's path. pitwall reads the
  start of the argument list only for the processes in a pane's own
  foreground group.
- Without hooks or a decision model, Gemini CLI, OpenCode and Aider get
  their state from their screen, as Claude and Codex do.
- Gemini CLI, OpenCode and Cursor sessions resume after a restart, like
  Claude's.
- The side panel follows Gemini CLI sessions, with their plan from
  `write_todos`, and Settings > Usage counts Gemini's tokens.
- OpenCode shows Plan when its experimental plan mode asks to switch to the
  build agent. Reinstall hooks to update the plugin.
- Tabs on different branches of one repository that change the same files
  get a warning mark on their rows: amber when they touch the same files,
  red when merging their commits would conflict. The hover card lists the
  files, conflicting ones first, opens a file's diff on a click, and
  suggests which branch to merge first. A notice tells you the first time
  two tabs start sharing a file. `conflict_radar = false` under `[git]`
  turns it off.
- New worktree tab asks what the worktree checks out: a new branch off any
  local or remote branch, an existing local branch, a remote branch that a
  new local branch tracks, or, when origin is on GitHub, a pull request by
  number, fetched into `pr-<number>`. A new branch no longer tracks the
  branch it starts from, so Create pull request pushes it with `-u`.
- The first worktree in a repo adds `/.worktrees/` to `.git/info/exclude`,
  so worktrees stop showing as untracked files. `.gitignore` is untouched.
- Deleting a worktree tab lists the worktree's uncommitted and untracked
  files and offers Delete anyway, which used to fail without a word. Deleting
  its branch warns when the branch is not merged, and then deletes it
  anyway. The main checkout is never removed.
- Clean up worktrees, in the command palette, runs `git worktree prune` and
  lists the worktrees under `.worktrees` that no tab uses, with their last
  commit and changes, to open or delete. The daemon prunes on start and says
  when it finds some.
- `pitwall worktree ls`, `new`, `rm` and `prune` do the same from a
  terminal: `new <name> --from <ref>`, `--branch <branch>` or `--pr <n>`
  opens a tab, `-- cmd` runs a command in it.
- `pitwall completion bash`, `zsh` or `fish` prints a completion script for
  every pitwall command.
- Start an agent on a task from the window: New task (Ctrl+Shift+A, the
  command palette, or a group's menu) picks the project, where it runs
  (the folder, a new worktree off a branch you choose, or an existing
  branch), the agent on your PATH, Claude's or Codex's permission mode, and
  the prompt. Queue holds the task until a running agent of the session
  finishes, or under `[agents] max_running` until fewer than that many run.
  The queue shows under its group in the sidebar, survives a daemon
  restart, and `pitwall queue ls`, `add` and `rm` manage it from a shell.
  Windows need a daemon of this release for tasks; an older one says so in
  the dialog.
- A selection stays on its text while new output scrolls the pane. It used
  to stay on the same screen cells, so the text under it changed. Dragging
  past the top or bottom of a pane scrolls it, faster the further you drag,
  so one selection can copy any amount of history.
- Triple-click selects a whole line, across the rows it wrapped onto, and
  Alt+drag selects a block.
- Copy mode (Ctrl+Shift+X) selects and copies with the keyboard: vi motions,
  `/` to find, v, V or Ctrl+V to select, y to copy. See docs/keys.md.
- `[terminal] scrollback` sets how many lines of history new panes keep,
  10,000 by default and up to 200,000. It is also under Terminal in the
  settings page.
- A mac key preset puts the app's keys on Cmd: Cmd+C and Cmd+V copy and
  paste, Cmd+T opens a tab, Cmd+W closes a pane, Cmd+Shift+P opens the
  palette. It is the default on macOS when the config names no preset.
  `Cmd` is another name for `Super` in config chords.
- View diff opens a review view in place of the tab's panes instead of a
  pager. It lists the changed files, untracked ones included, with their
  status and line counts, and shows each file's diff with line numbers and
  the unchanged lines folded. Click a line number or press c to comment on
  a line or a range, then send every comment to the tab's agent as one
  prompt with file:line references and the quoted lines; a busy agent gets
  it when it next waits for input. Mark files reviewed (a file the agent
  changes again comes back unreviewed) or discard a file's changes. Open it
  with Ctrl+Shift+R, the tab menu, the command palette or a file under
  Changes; j/k move between files, n/p between hunks, Esc closes it. The
  pager stays as Open diff in pager. The header shows the branch's pull
  request as its sidebar row does, and a click opens it. A file in a
  conflict warning's hover card opens in the review view too.

## v0.1.0-beta.2

- Agent state stays current after an upgrade you chose to restart the
  daemon for later. Hooks run the new `pitwall` on your PATH, and the old
  daemon used to refuse every one of them until it restarted, so each
  agent's sidebar row stopped changing. Now a hook refused for its protocol
  version sends again in the daemon's, and daemons serve hooks of any
  version. A daemon that gets hooks from a newer pitwall shows a notice
  once asking you to restart it, and logs a line a minute with a count.
- The README and the site no longer list the AUR package. It is not on the
  AUR yet: new AUR accounts are closed for now.

## v0.1.0-beta.1

- pitwall is in beta. Releases are `v0.1.0-beta.N`, and
  `scripts/release.sh` numbers the next release in the newest series.
- When an agent that pitwall resumed after a restart exits and its pane
  drops back to a shell, the shell keeps the pane's size. It used to start
  at 80x24 and stay there until another pane opened, so a `claude
  --continue` typed in it drew in the top-left corner of the pane.

## v0.1.0-alpha.24

- The Update button stays hidden when Homebrew or a distro package owns the
  pitwall binary; update those with the package manager.
- pitwall checks for a newer release every hour instead of every 6 hours,
  and also when its window regains focus half an hour or more after the
  last check, so a new release shows its Update button soon after it is
  out.
- Install pitwall with `brew install quanticstudios/tap/pitwall` on macOS
  or Linux, `scoop install pitwall` from the quanticstudios bucket on
  Windows, or the `pitwall-bin` AUR package on Arch. On macOS, `get.sh` also
  installs an unsigned `pitwall.app` in `~/Applications` for the Dock and
  Spotlight; started from it, pitwall takes PATH from your login shell and
  opens in your home folder. Hooks installed from a Homebrew pitwall point at
  its `opt` path, so they survive `brew upgrade`.
- The first launch shows a welcome card over the shell instead of a bare
  prompt. It lists the agent CLIs on your PATH (Claude Code, Codex, Gemini
  CLI, OpenCode, pi), whether each has pitwall's hooks, a Start button that
  opens each in a tab of its own, an Install hooks button that opens the
  usual hooks dialog, and the keys for the agent waiting on you, a new tab
  and the command palette. With no agent on PATH it is one line of install
  links. Dismiss it, start an agent or type in the shell, and it never
  comes back. A launch with saved state or a config file skips it.
- Settings, Agents shows pi's hooks next to the other agents'.
- The README is short now: what pitwall does, install, quick start and the
  ten keys that matter. The rest moved, unchanged, to pages under `docs/`,
  and `docs/index.html` is a one-page site for GitHub Pages.
- Settings has a Usage page: what Claude Code, Codex and pi cost on this
  machine over the last 7, 30 or 90 days, at the API's list prices. It
  shows the total and each agent's share, a daily cost chart, the cached
  and uncached tokens with what the cache saved, and a table by model or
  by day. It reads the agents' own session files, and a reopen reads only
  what they appended since.
- Settings has a Decision stats page: what `pitwall jev report` prints,
  drawn, over the last 7 days, 30 days or all time. It leads with the
  verdict on whether you answer approvals faster with the model's
  suggestion shown, or how many answers each half still needs, then
  calls, cost, cost per day, median latency, the two holdout halves side
  by side with the median difference and its 95% interval, calls per day,
  turn checks and triage.

## v0.1.0-alpha.23

- Shift+Insert pastes and Ctrl+Insert copies, as in Ghostty and kitty, so
  Omarchy's Super+V and Super+C work in pitwall's panes. The Insert key
  itself now reaches programs; Gio used to drop it.
- Review an agent's changes and open a pull request without leaving
  pitwall. Clicking a file under Changes in the agent panel opens its diff
  from the default branch in a new pane, in your git pager (`$GIT_PAGER`,
  delta, or `less -R`); q closes it. View diff in the tab's "…" menu and the
  command palette shows the whole branch. Create pull request opens a tab
  that pushes the branch when it has no upstream, never with force, then
  runs `gh pr create --fill`. The menu says why it is greyed out: no gh, on
  the default branch, or no commits ahead. A daemon from an older release
  shows "restart daemon" until it restarts.
- Each worktree tab gets its own block of ports, 3010-3019 for the first,
  so agents in parallel worktrees stop fighting over port 3000. Its panes
  get `PORT`, `PITWALL_PORT_BASE` and `PITWALL_PORTS`; the main checkout
  keeps its usual ports. The block survives restarts and shows on the
  tab's hover card and as `PORT 3010` in the agent panel's Changes view;
  `[worktrees]` `port_base` and `port_step` move it. When a shell in the
  tab prints "address already in use", the tab asks for you and names its
  ports.
- A repo's `.pitwall/worktree.toml` (or `[worktrees]` in `config.toml`) can
  copy files such as `.env` from the main checkout into a new worktree, link
  `node_modules`, and type a `setup` command such as `pnpm install` into the
  new tab's shell. A `setup` from the repo's file waits for you to press
  Enter; one from your own `config.toml` runs at once. Nothing is
  overwritten, and nothing is copied unless you list it.
- `pitwall --host <host>` opens a window on the daemon of another machine
  over ssh, so agents keep running there when the laptop closes. It uses
  your `ssh` and its config, starts the daemon on the host when none runs,
  and after the link drops reconnects through a new ssh connection. The
  window title and the sidebar header name the host. `[[hosts]]` in
  config.toml gives hosts short names. A host without pitwall, or with one
  of another protocol major version, gets the install command instead of a
  window. See "Remote hosts" in the README.
- A command palette lists every action with its group and the keys bound to
  it in your config, and runs the one you pick. Ctrl+Shift+P opens it
  in both presets; `command_palette` rebinds it. Type to filter, Enter to
  run, Esc to close. Actions with no key, such as tab and pane mode in the
  conventional preset, run from it too. The empty pane area names its key.
- Upgrading pitwall no longer restarts the background service, so programs
  running in panes keep running. A window works with a daemon of the same
  protocol major version, older or newer. This release runs on the daemon
  from v0.1.0-alpha.22 as it is.
- When a release does need a new daemon, its window asks first: "pitwall
  <version> needs to restart its background service; programs running in
  panes will stop." Restart now restarts it, and agents resume as before.
  Later closes the window and leaves everything running.
- A window that loses its daemon connection keeps its last screen under
  "Disconnected from pitwall's background service." and reconnects on its
  own, starting a daemon when none runs, with a Reconnect button that tries
  at once. It used to sit dead until reopened.
- Resizing a pane rewraps its text, as Ghostty, kitty and WezTerm do. A long
  line cut by a split comes back whole when the pane widens again, in the
  scrollback too, and line breaks the program printed stay where they were.
  Colors, links and wide characters move with their text, the cursor stays
  on its character, and the bottom of the pane stays put: narrowing pushes
  the top into scrollback instead of losing the last lines. Full-screen
  programs on the alternate screen redraw themselves as before.
- A damaged `state.json`, or one a newer pitwall saved, no longer stops
  pitwall from starting. The daemon moves it to `state.json.bad-<time>`,
  starts without those tabs, and the window says why and where the file is
  until you close the notice. After you update, the next start adds the
  tabs from a `.bad` file it can now read and renames it to
  `state.json.restored-<time>`.
- Before it upgrades `state.json` to a newer format, pitwall copies the old
  file to `state.json.prev`, so going back to an older pitwall keeps your
  tabs.
- Ctrl+Shift+F (`find` in both presets) opens a find bar on the focused
  pane. It searches the scrollback and the screen as you type, case-blind
  unless the query has a capital, highlights every match in view with the
  current one in solid yellow, and shows "3 of 17" or "No matches". Enter
  or F3 goes to the next match up into history, Shift+Enter or Shift+F3 back
  down, and Escape closes the bar and returns to the live screen. Find
  needs the background service from this release: on an older one the bar
  says "Search needs the background service restarted" and searches
  nothing.
- The agent panel shows the agent's model, the tokens its session used and
  how full its context window is, with a meter that turns yellow at 80% and
  red at 95%. Flow's Session section splits the tokens into input, output,
  cache read and cache write, by model, subagents included. A tab's hover
  card shows the same line. `show_cost = true` under `[usage]`, or the
  switch under Agents in settings, adds the cost at the API's list prices
  from a dated table; a model the table lacks shows tokens only.
- Programs can set the clipboard with OSC 52, as Neovim, tmux and Helix do,
  also over ssh. A write holds up to 1 MB of text, and read requests get no
  answer, so no program can see what you copied. `osc52 = "off"` under
  `[terminal]` ignores the writes.
- A bell (BEL) in a pane you are not looking at rings it like an OSC 9
  notification, with a desktop notification, at most once a second. In the
  focused pane a bell does nothing. `bell = "off"` under `[terminal]`
  ignores bells.
- Ctrl+Shift+Up and Ctrl+Shift+Down scroll to the previous and next shell
  prompt in both presets (`prev_prompt`, `next_prompt`). pitwall records
  OSC 133 prompt marks on their rows, history included; fish 4 sends them,
  and the README has the lines for zsh and bash.
- Clipboard writes, bells and prompt jumps happen in the background
  service. While one from an earlier release runs, they do nothing until
  it restarts.
- Gemini CLI and OpenCode report their state through hooks, like Claude
  Code and Codex. `pitwall hooks install` adds pitwall's hooks to
  `~/.gemini/settings.json` and writes an OpenCode plugin to
  `~/.config/opencode/plugins/pitwall.js`, each only when that agent is
  installed. Gemini shows Working, Input, Approval, Plan and Done but never
  Error, because it fires no hook for a failed turn. OpenCode shows Working,
  Input, Approval, Done and Error, and has no Plan. Gemini's settings with
  comments are skipped with a warning instead of stopping the install.
- A pane whose agent runs without hooks says so: "Install hooks for live
  status" with an Install button, after the agent has run for 20 seconds
  without reporting, or as soon as its screen shows a turn. Install shows
  what changes in each file, then installs on confirm and reminds you to
  restart running agents and trust Codex's hooks with /hooks. Settings >
  Agents has the same Install button in place of the command to copy, and
  lists Gemini CLI and OpenCode.
- `pitwall hooks install --dry-run` lists each change as a comment line
  above the file it makes, and no longer prints files that stay unchanged.

- Answer agents from your phone. With `[remote] enabled = true`, the
  background service serves a page that lists the tabs needing you, with
  the agent's question and pitwall's risk flags, Allow and Deny buttons
  for Claude Code and Codex permission prompts, and a reply box. It never
  shows a pane's screen. It listens on 127.0.0.1:7777; `tailscale serve`
  puts it on your tailnet. Pair a phone with `pitwall remote pair` or in
  Settings, Phone, by QR code; `pitwall remote devices` and `revoke` list
  and unpair them. Any other address needs `tls = true`. See the README's
  Phone section.

## v0.1.0-alpha.22

- The first group at the top of the sidebar gets the same 6dp above its
  header as the others, so its hover box no longer touches the sidebar's
  header.

## v0.1.0-alpha.21

- The daemon logs each decision to `decisions.jsonl` in the state folder:
  latency, answer, probability, input tokens, and what you did next (how
  you answered a permission prompt and how fast, whether you prompted
  again after a turn check, when you focused a triaged pane). It holds ids
  and numbers, never prompts, commands, paths or screen text.
- `holdout` under `[decisions.approvals]`, 0.5 by default, hides Jev's
  recommendation on that share of permission prompts, at random, so the
  two can be compared. Risk flags always show, alone as "Risk: sudo" when
  the recommendation is hidden or Jev did not answer. `holdout = 0` shows
  every recommendation.
- `pitwall jev report [--days N]` prints calls, failures, latency, an
  estimated cost, answer times with and without the recommendation, how
  often Jev agreed with you, the turn check's follow-up rate and triage's
  time to focus, with a one-line verdict once there is enough data.
  `pitwall jev status` shows how many events the log holds.
- An older daemon logs a newer client's protocol version at most once a
  minute per version, with a count, instead of once per hook.

## v0.1.0-alpha.20

- Alt+1-9 and Ctrl+T then 1-9 count only the tabs the sidebar shows: a
  collapsed group's tabs no longer take a number. Hold Alt for a moment and
  the first nine rows show their number in place of their icon. Rebind
  `goto_tab_1`-`9` to another modifier, Ctrl+1-9 say, and holding that one
  shows them instead.

## v0.1.0-alpha.19

- Hovering a tab shows "…" then "×" in place of its time. The ×
  closes the tab, like the menu's Close and a middle-click on the row.
- An Update button shows at the bottom of the sidebar when GitHub has a
  newer release. A click downloads the archive for your system, checks it
  against the release's `checksums.txt`, and replaces the binary the window
  runs; "Restart to finish" then reopens the window on the new version.
  Release builds on Linux and macOS check when a window opens and every 6
  hours; builds from source and Windows never do. `[updates]` gains `check`
  to turn it off, also under About in the settings page.
- About in the settings page shows a release archive's version instead of
  `dev-` and its commit.

## v0.1.0-alpha.18

- Shift+Enter adds a new line in Claude Code and Codex. Under the kitty
  keyboard protocol, which both turn on, pitwall sent Shift+Enter as a plain
  Enter. Enter, Tab and Backspace with any modifier, Shift included, now get
  their own codes, as in kitty and Ghostty; unmodified they still send the
  bytes a shell expects.

## v0.1.0-alpha.17

- In tab mode, Ctrl+T then N opens a tab outside every group, like the
  sidebar's top "+", and Ctrl+T then G opens one in this tab's group, right
  after it. The global new-tab chord (aide Alt+Shift+T, conventional
  Ctrl+Shift+T) still opens it in the group. `[keys.tab]` gains
  `new_in_group`.

## v0.1.0-alpha.16

- The "+" next to the session name always opens a tab outside every group,
  in the folder of the tab you are on, even when that folder belongs to a
  group. A group's own "+" still opens one inside it.
- Protocol 16: the new `pitwall` restarts an older daemon.

## v0.1.0-alpha.15

- The sidebar's tab rows and group headers sit 8dp in from both edges, so a
  highlighted row no longer runs into the sidebar's right border. Inside a
  row, content is 12dp from each side again.

## v0.1.0-alpha.14

- A Claude Code session started with bypass (`claude
  --dangerously-skip-permissions`, a `cdang` alias) resumes with it, even
  after shift+tab, `/clear` or compaction moved it to another mode. pitwall
  tells agent processes apart by Claude's SessionStart hook: a plain
  `claude` started afterwards, in the same pane or the same script, resumes
  in its own mode. Run `pitwall hooks install` once to add that hook; until
  then the mode follows each report, as before.
- Claude Code's `auto` and `manual` permission modes are kept on resume.

## v0.1.0-alpha.13

- A group header shows "+" and "…" only on hover, so a long group name
  uses the whole row; on hover the name fades out under the two buttons.
  The buttons get a hover highlight of their own.
- A tab row's pill, time and "…" sit 16dp from its right edge, up from 12dp,
  so the pill's filled box no longer crowds the border.
- Every tab row highlights on hover, not only rows without an agent; a
  state's tint (approval, error, plan ready) shows through faintly.

## v0.1.0-alpha.12

- The agent panel opens with Ctrl+Shift+L, so Ctrl+L reaches the shell
  again to clear the screen. A `toggle_panel` set in config.toml still wins.

## v0.1.0-alpha.11

- On Wayland, a window on another workspace no longer hangs and gets
  "Application Not Responding". pitwall carries a patched copy of Gio
  (`third_party/gioui.org`, see its PATCH.md) that presents a frame only
  after the compositor showed the last one.
## v0.1.0-alpha.10

- The sidebar (Ctrl+B) slides in and out with the panes moving along,
  instead of the panes jumping to their new place. The agent panel
  (Ctrl+L) slides in from the right edge the same way, over 200ms. The
  terminals still resize once.

## v0.1.0-alpha.9

The `v0.1.0-alpha.7` and `v0.1.0-alpha.8` tags have no release: the first
build stopped before this section existed, the second never started, and
tags cannot be moved. alpha.9 is the same code.

- An agent side panel on the right, Ctrl+L (`toggle_panel`, rebindable).
  It follows the focused pane and reads the agent's own session file:
  Claude Code's transcript and its subagents, Codex's rollout, or pi's
  session. Its tabs:
  - Flow: the current turn as a graph from prompt to outcome, with a
    pending approval and Jev's suggestion, then tool calls, errors, turn
    time, changes and earlier turns.
  - Subagents: Active and Done. Opening one shows what it was asked, its
    latest message or result, and its tool calls.
  - Plan: Claude's todo list or Task tools, or Codex's `update_plan`.
  - Changes: files changed from the default branch, with +/-.
  - Timeline: prompts, tool calls, subagents and suggestions with their times.

  Claude gets every tab, and Codex gets Plan and Subagents when it uses them.
  pi has neither. The panel reads the files only while it is open, keeps
  their text in memory, and never logs or saves it. Ctrl+L no longer reaches
  the terminal; set `toggle_panel = []` to give it back.
- Run `pitwall hooks install` once: the pi extension now reports its session
  file.
- Branch +/- in the sidebar comes from one diff from the merge base to the
  work tree. A line changed both in a commit and in uncommitted work now
  counts once, so the numbers can drop slightly.
- Protocol 15: the new `pitwall` restarts an older daemon.

## v0.1.0-alpha.6

- pitwall logs. Windows write events to `gui.log` and the daemon to
  `daemon.log` in the state directory: starts, connections, panes starting
  and exiting, sizes, errors, slow requests, slow frames and stalls, one line
  each, never pane contents, typed input, prompts, hook payloads or command
  arguments. Go's standard crash traces go to `crash.log` instead: the panic
  value and the crashing goroutine's stack, verbatim. A file over 5 MB at startup moves to `.1`.
  `pitwall logs` prints the three paths and `pitwall logs -f` follows them;
  README's Troubleshooting says what to attach to a bug.
- A pane whose program printed nothing after a resize kept showing its old
  size, 80x24 for a new pane whose size arrived late, as it can behind a burst
  of typing. A resize now redraws the pane. A size the window failed to send
  is sent again.
- A command tab from `pitwall new -- <cmd>` survives a daemon restart. A
  command that had exited keeps its exit code, and an agent with a known
  session resumes and stays. Any other command that was still running is
  not run again, because that may not be safe: it comes back exited with
  its exit code unknown (`exit_code` null in `pitwall ls --json`, and
  `pitwall wait` prints `exit unknown` and exits 3). The output is not
  saved, so the pane shows a one-line notice instead. Command tabs saved by
  alpha.5 count as such tabs: a pane with a command and no agent session to
  resume is not run again after the upgrade.
- A `pi -p` tab opened with `pitwall new -- pi -p ...` ends showing Done or
  Error. pi's extension used to drop its queued reports at shutdown, the
  result included. Now it drops them and sends one final shutdown report
  that carries the run's result, from a process that outlives pi, so even a
  slow hook delivers it after pi exits. pitwall takes that report as the
  result, then the shutdown. In a command tab, the shutdown and the exit
  after it keep the Done or Error; elsewhere a pi shutdown clears the tab's
  status as before. Once a pi runtime has shut down, pitwall ignores any
  report it sends later. pi also exits up to a second sooner: the extension
  no longer holds it open for its whole shutdown deadline. Run
  `pitwall hooks install` to update the extension. It replaces the alpha.5
  file, which is not counted as edited.
- A resumed Claude pane no longer sends its first prompt again. A pane
  opened as `claude "fix the tests"` came back as
  `claude "fix the tests" --resume <id>`. Resume now keeps only the options
  `claude --help` documents, minus session selectors, print mode and one-off
  actions such as `--worktree`. A value option left without its value, such
  as a trailing `--model`, is dropped too, since it took the `--resume`.
- The daemon protocol is now version 14; the next `pitwall` restarts an
  older daemon. The state file format is now version 8, so an older pitwall
  can't read state this one saved.
- Tab pills say "Working", "Approval" or "Done" without the "Agent " prefix,
  so long titles keep more of their text. `pitwall ls` and desktop
  notifications use the same short labels.
- A tab row's content ends 12dp from the right edge, the same as on the left.
  The "…" menu button shows on hover at the right end of the second line, in
  place of the time, and the pill stays where it is.
- pi's mark is drawn smaller, so it carries the same weight as the Claude and
  Codex marks next to it.
- A window no longer stops taking state from the daemon when `pitwall
  attach` requests pile up faster than it raises itself: the newest request
  replaces an unread one. Killing a session logs the kill and the panes it
  closes in `daemon.log`, and each window's reaction in `gui.log`: the
  session it moved to, or that it closed because nothing was left to show.
  A pane that takes over 3s to close is logged too.

## v0.1.0-alpha.5

- Agents and scripts can drive tabs (issue #2). `pitwall new ... -- <cmd>`
  opens a tab running a command instead of a shell; its pane stays after the
  command exits, showing its output, until you close it or the daemon
  restarts. `pitwall new -d -- codex "<prompt>"` starts an agent on a task.
  `pitwall wait <tab> --until done|idle|blocked|exit` blocks until the
  agent gets there, with exit codes a script can branch on.
  [docs/agent-skill.md](docs/agent-skill.md) documents them for agents.
- **Breaking:** `pitwall ls --json` prints one documented object per tab
  (`n`, `id`, `title`, `group`, `cwd`, `branch`, `agent`, `state`,
  `question`, `exit_code`, `panes`, `detached`) instead of pitwall's internal
  structs, and `[]` when no daemon runs. Tabs can also be named by that `id`.
- A resumed agent keeps the permission mode its hooks last reported. Claude
  comes back with `--dangerously-skip-permissions` for bypassPermissions and
  `--permission-mode <mode>` for its other modes, default included, so a
  bypass set as the default in settings stays off once you left it; Codex comes
  back with `--dangerously-bypass-approvals-and-sandbox` for
  bypassPermissions. A pane whose command already sets permissions keeps its
  own flag. Other flags of an agent started in a shell, such as `--model`,
  are not restored.
- A restored agent in a pane that was a shell drops to a shell in the same
  folder when it exits, instead of closing the tab. A pane opened with a
  command closes as before, except a failed resume within 3 seconds of the
  restart, which also gets a shell.
- The daemon protocol is now version 13; the next `pitwall` restarts an
  older daemon. The state file format is unchanged.

## v0.1.0-alpha.4

- The window no longer freezes while the daemon is busy with a slow request,
  such as a new tab in a repository where `git` is slow. Messages to the
  daemon queue in order instead of blocking the window, and none are dropped
  while the connection is up; a newer resize of a pane replaces its queued
  one.
- When one window event runs for over 2 seconds, pitwall writes every
  goroutine's stack to `stall-<time>.txt` in its state directory and keeps the
  newest 5. Attach the newest one to a report of a frozen window.
- Ctrl+Backspace deletes the word before the cursor. It sends Ctrl+W, the
  delete-word key in bash, zsh, fish, Claude Code and Codex.
- Hovering a tab for half a second opens a card beside the sidebar with its
  full title, group and folder, branch and diff stats, the agent's state and
  question, and the pane count. It follows the pointer to other tabs at once
  and hides on click, scroll, drag or a key press.
- Tabs inside a group are indented under the group header.
- The "+" on a hovered tab row is gone; it covered the state pill. Right-click
  a tab for **New tab below**, or use the "+" on the group or sidebar header.
- Decision models, starting with TypeSafe's Jev. Connect it in Settings,
  Decisions, or with `pitwall jev login`; the key stays in a 0600
  `credentials` file or `TYPESAFE_API_KEY`, never in `config.toml`.
  Nothing is sent until you connect, and connecting keeps your settings.
  With the other `[decisions]` settings left at their defaults:
  - Approvals suggest only: a Claude Code or Codex permission request
    shows the model's recommendation on the tab's pill, in the switcher,
    in the hover card and on the pane, with any risk pitwall reads in the
    call ("Jev: allow 96% · sudo"). The agent's prompt is never delayed
    and pitwall never answers it. Automatic approval was left out because
    a command's text can't show what it will run, so it needs sandboxed
    execution.
  - Attention triage (on by default) sorts what needs you as fyi, later,
    soon or now for the jump-to-attention key and notifications; fyi sends
    no notification.
  - Optional: status for agent CLIs without hooks (Gemini CLI, OpenCode,
    Aider, Amp, Cursor agent, Goose, Crush) read from their screen, and a
    turn check that turns Done into Check when a turn needs review; it
    reads the agent's last message only, never the screen.
  - `provider = "command"` runs your own classifier instead. Every call
    has secrets removed before anything is cut to size, a timeout (1.5 s
    by default, 0.2 to 10 s) and a per-pane limit; any failure leaves
    pitwall as it was. See the README's Decisions section.
- A finished turn's notification shows the start of the agent's last
  message.
- The daemon protocol is now version 11; the next `pitwall` restarts an
  older daemon. The state file format is unchanged.
- pitwall supports the [pi](https://pi.dev) coding agent. A tab running `pi`
  shows pi's logo and its state, and resumes with `pi --session <id>` after
  a reboot. `pitwall hooks install` writes a pi extension,
  `~/.pi/agent/extensions/pitwall.ts`, that reports working, done, error and
  the tool in use; without it pitwall reads pi's state from the screen.

## v0.1.0-alpha.3

- Run `pitwall` as many times as you like: each run opens its own window.
  It reopens the most recent session no window shows, else starts a new
  session in the folder you ran it from. `pitwall -s <name>` still raises
  the window that shows that session.
- Copy on select: a mouse selection goes to the clipboard as soon as you make
  it (drag release or double-clicked word), with a quiet "Copied 42
  characters" notice at the bottom of the panes. The copy shortcut shows the
  notice too. Turn it off with `copy_on_select = false` under the new
  `[terminal]` table, or the switch in Settings, Terminal.
- Links in panes are underlined: web, `file://` and `www.` URLs in the text,
  and OSC 8 hyperlinks that programs print. Hold Ctrl (Cmd on macOS) over
  one to see it in the theme's link color, and click to open it in your
  browser, also inside Claude Code and Codex. Turn it off with `links = false` under
  `[terminal]`, or the switch in Settings, Terminal.

## v0.1.0-alpha.2

- Sessions, like tmux and zellij: many named sessions (`swift-otter`) run at
  once in the daemon, each with its own tabs, groups and order, and all of
  them survive restarts and reboots. Rename them any time.
- The session switcher (Ctrl+Shift+S, aide Alt+S, tab mode `s`) lists every
  session with its agents and live working and needs-you counts, beside a
  live view of the highlighted session's sidebar. Type to filter; `n`, `r`
  and `x` create, rename and kill sessions in place.
- The sidebar header and the window title show the current session.
  Ctrl+Shift+] and Ctrl+Shift+[ (aide Alt+] and Alt+[) step through
  sessions; Ctrl+Shift+N makes one.
- Several windows can be open, one per session. `pitwall` opens the last
  session you used, `pitwall -s <name>` a named one.
- `pitwall session ls / new / attach / rename / kill`. Tab commands act on
  the calling pane's session, else the last one used, or `-s <name>`.
- The jump-to-attention key crosses sessions, and notifications name the
  session.
- Saved state moves into a session called `main`. Protocol version 9, store
  format 7.

## v0.1.0-alpha.1

- Versions say alpha: `v0.1.0-alpha.N`.
- `pitwall notify <text>` rings the pane it runs in, e.g.
  `npm test && pitwall notify "tests passed"`.
- A terminal notification's desktop notification shows just its message.
- Windows: the daemon saves its state again. Syncing the state folder after
  the write is not allowed there and failed every save.
- The README opens with a short loop of the app and shows each feature with
  a short clip.

## v0.0.10

- Pane mode, like zellij's: the pane prefix (Ctrl+P in the aide preset,
  unbound in conventional) then `n` new pane, `d` split down, `r` split
  right, `x` close, `h/j/k/l` or arrows to move, `f` fullscreen, `p` or Tab
  next pane. It stays on until Esc or Enter, so keys chain. Ctrl+P twice
  sends Ctrl+P to the shell. Keys live in `[keys.pane]` and the settings page.
- First release with prebuilt Linux, macOS and Windows archives (the v0.0.9
  tag didn't start the release build).

## v0.0.9

- Install without cloning: `curl -fsSL https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.sh | sh`
  on Linux and macOS, `irm https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.ps1 | iex`
  on Windows. Releases carry prebuilt archives and `checksums.txt`.
- macOS and Windows builds (alpha, untested by us so far). Windows gets agent
  status from hooks only; see the README for the gaps.
- A pane that needs you and isn't in view gets a ring in its state color, and
  its sidebar row a stronger tint, accent bar and dot, until you look at it.
  Desktop notifications follow the same rule.
- `jump_attention` goes to the newest tab that needs you (Ctrl+Shift+U, aide
  Alt+U, `u` in tab mode).
- Any program can ask for attention with OSC 9, OSC 777 or kitty's OSC 99.
- MIT license. Protocol version 8.

## v0.0.8

- The tab switcher is hidden for now: no hold key, no switcher shortcuts,
  and the aide preset's Alt+J/K walk every tab across groups. The code stays
  (`config.SwitcherHidden`) for a later return. A `switcher_modifier`,
  `switcher` or `pin_switcher` line in config still loads and does nothing.
- An agent tab's second line drops the "Claude"/"Codex" name; the logo says it.

## v0.0.7

- Agent tabs show the real Claude and OpenAI logos (from Simple Icons)
  instead of drawn stand-ins. The OpenAI mark and the "Codex" label use the
  theme's text color.

## v0.0.6

- Groups and ungrouped tabs share one order: drag a group above loose tabs,
  or a tab between two groups.
- Tabs no longer get generated names. A tab shows its title (the agent's
  topic, the command, or the folder) until you name it. `pitwall ls` numbers
  tabs in sidebar order, and `attach`, `detach`, `kill` and `rename` take that
  number, a title, or a unique title prefix.
- A tab running Claude Code or Codex always shows the agent's mark and name,
  idle or working, in the sidebar and the switcher.
- Settings is a page instead of a dialog. Open it with the gear or `Ctrl+,`;
  it takes the pane area and the sidebar stays. Pick themes from previews,
  choose fonts and sizes, record shortcuts with conflict checks and swap,
  check whether the agent hooks are installed. Every change writes the one
  key to `config.toml` and keeps your comments.
- Protocol version 7, store format 6.

## v0.0.5

- A tab's branch, `+/-` stats and folder follow its shell's working
  directory, like its title, instead of the folder it started in.

## v0.0.4

- One level less: the sidebar lists **tabs**, optionally in groups. A tab
  holds its split panes; sessions are gone from the UI, the keys and the CLI.
  Saved state splits each old session's tabs into separate rows.
- Next/previous tab moves between rows (aide: Alt+J/K; conventional:
  Ctrl+Tab, Ctrl+PageDown/PageUp, Ctrl+Shift+PageDown/PageUp for groups).
  Alt+1-9 jumps to a tab. New tab opens right below the current one.
- A shell's tab title follows its working directory (`~`, `pitwall`).
- Drag and drop rebuilt: the row lifts, the others slide apart to open a gap,
  hovering a group header drops into it, Escape animates back.
- Config action names say tab (`next_tab`, `new_tab`, `goto_tab_N`); the old
  session names still load, with a note from `pitwall config check`.
- `pitwall ls` lists tabs with a label and a handle; commands accept either.
- Protocol version 6, store format 5.

## v0.0.3

- `~/.config/pitwall/config.toml` for keybindings, theme, fonts and pane
  spacing, reloaded live. `pitwall config init / check / default / path /
  schema`, with a generated JSON Schema for editor completion and checks.
- Two keybinding presets: **conventional** (the new default, Linux terminal
  conventions such as Ctrl+Shift+T, Ctrl+Tab, Ctrl+Shift+O/E) and **aide**
  (the previous Alt-key bindings). Single actions can be rebound or unbound.
- Themes: aide-dark, aide-light, tokyo-night, catppuccin-mocha, custom theme
  files, and per-color overrides. The daemon answers terminal color queries
  with the theme's palette.
- Configurable UI and terminal fonts, sizes, line height and fallback fonts.
- Tabs are listed under their session in the sidebar instead of a strip
  above the panes, with a hover "+" for a new tab.
- Sessions show the agent's topic as their name until you name them.
- Drag sessions and groups to reorder them; drop a session on a group to
  move it there.
- Hide the sidebar with Ctrl+Shift+B (Ctrl+B in the aide preset).
- Panes sit 4dp in from the window edges and 4dp apart.
- The sidebar shows the new pitwall logo.

## v0.0.2

- Claude Code's terminal titles reach the tab. Its `✳` title prefix contains
  byte 0x9C, which the terminal parser read as a string terminator, cutting
  the title to one byte and printing the rest on screen.
- Tabs are named after the agent's work: Claude's topic title, else the
  first prompt in the tab (Codex titles itself after the folder), else the
  running command. Slash commands are skipped.
- Sessions you never named carry a label that follows their busiest tab;
  the daemon keeps it current for the sidebar.
- New logo: a P whose stem is three status lights and whose bowl is a pane.
- The daemon stores session and group order and accepts reorder requests,
  ready for drag and drop in the sidebar.
- Protocol version 5: the next `pitwall` restarts an older running daemon.

## v0.0.1

The first tagged build.

- Native Gio window with an aide-style sidebar beside split terminal panes.
- Starts like tmux: straight into a shell in the folder you launch it from.
- Sessions with generated names (`swift-otter`), tabs, and splits. Exiting a
  pane closes it; the tab and session close when empty; the window closes with
  the last session.
- Ctrl+T tab mode, Alt+J/K/H/L navigation and the Alt-hold session switcher.
- Live state for every session: the command a terminal runs, and Claude Code
  or Codex working, waiting for input, asking for approval, plan ready, done,
  or failed. Exact through hooks, read from the screen without them.
- Tab titles follow the agent's terminal title; agents can name their tab with
  `pitwall tab rename`.
- Groups made after the fact, one-click grouping by folder, and git worktree
  sessions.
- Detach and attach, and `pitwall ls / new / attach / detach / kill / rename`.
- The daemon owns the PTYs: closing the window keeps sessions running, and
  after a reboot sessions come back with agents resumed.
- Desktop notifications when a session needs you.
- `pitwall hooks install` merges the hooks into Claude Code and Codex config.
