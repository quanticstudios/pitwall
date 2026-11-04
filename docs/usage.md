# Using pitwall

pitwall is a terminal multiplexer for running coding agents side by side. It
opens straight into a shell like tmux, but it is a native window: a sidebar
lists every tab and what it is doing right now, whether that is a command
running in a terminal or a coding agent working, waiting for your answer,
asking for approval, done, or failed. Terminals are drawn with real fonts
and pixels, not character cells.

A background daemon owns every terminal. Closing the window leaves tabs
running, and after a reboot they come back in the same folders with agents
resumed where they left off.

## Concepts

- **Session**: a named set of tabs and groups, like a tmux or zellij
  session. Every session keeps running in the daemon; a window shows one of
  them. Names are generated (`swift-otter`) until you rename one.
- **Tab**: one working context, a set of split panes started in a folder. Its
  title follows the work: the agent's topic or first prompt, the running
  command, or the folder the shell is in now (`~` for home). Naming a tab
  replaces the title. The command line also takes its number in `pitwall ls`.
- **Pane**: one terminal.
- **Group**: tabs you put together after the fact, for example all the tabs
  working on one repo. Tabs start ungrouped.
- **Daemon**: the background process that owns the terminals. The window and
  the `pitwall` commands talk to it.

## First run

```sh
pitwall
```

The first window shows a welcome card. It lists the agent CLIs it found on
your `PATH` (Claude Code, Codex, Gemini CLI, OpenCode, pi, Oh My Pi, Cursor CLI, Amp,
Aider) and whether each one's hooks are installed. Start opens an agent in
a tab of its own, and Install hooks shows what it would change before it
changes it. Dismiss the card, or do anything else, and it never shows
again. With no agent on your `PATH`, you get a shell and a line with where
to install one.

Hooks let agents report their state exactly. To install them from a
terminal instead:

```sh
pitwall hooks install --dry-run   # see what would change
pitwall hooks install             # let your agents report their state
pitwall doctor                    # check every line says ok
```

`pitwall update` installs the latest release and hands the running
daemon's panes to it, their programs still running; see
[Updates](install.md#updates) for what it does when that fails.

The window opens on a shell in the folder you launched it from, in a
session with a generated name such as `swift-otter`. Run `claude`, `codex`,
`pi`, a dev server, anything. The tab's row in the sidebar shows what is
happening: the name of a running command, or the agent's state.

Open more tabs with **+** in the sidebar header. Each tab can be split into
panes. Typing `exit` closes a pane; an empty tab closes, and the session
ends with its last tab. Closing the window only detaches it: every session
keeps running, and `pitwall` opens the most recently used one again.

## Everyday use

### Forking terminal agents

<img src="media/fork-dark.png" alt="Fork in a terminal tab menu, dark theme" width="288"> <img src="media/fork-light.png" alt="Fork in a terminal tab menu, light theme" width="288">

Use **Fork** in a tab's menu to continue its Claude Code or Codex conversation
in a new tab. The source agent keeps running. The fork uses its live folder and saved
account profile. If the tab was opened with the agent's command, the fork
keeps the model and permission flags that a resume keeps. For an agent typed
into a shell or started by a wrapper, only the hook-reported permission mode
carries over; the original command flags are not recorded. With several agents in a tab,
the menu forks the focused agent, or the first agent in layout order.

```sh
pitwall fork                         # the calling pane
pitwall fork '#3' -n 'Second approach' # tab 3's main agent
pitwall fork '#3.2' --worktree side   # pane 2 of tab 3, in a new worktree
pitwall fork @pane-id                # a pane id from pitwall ls --json
```

The command prints the new tab's number. Menu forks and CLI forks from outside
any pane have no parent. A menu fork sits right after its source in the
same session and group. A CLI fork run inside a pane joins the calling
pane's session and group and hangs under its tab, like `pitwall new`. A
user's menu fork does not count as an orchestrator's child for `wait --children`.
Claude Code uses `--resume <id> --fork-session`;
Codex uses `fork <id>`. Both need a stored session id from the source pane.
The fork accepts its own id only after a hook reports one different from
the source id. Until then it cannot resume after a restart; it never resumes
the source session in place. [Claude's session docs](https://code.claude.com/docs/en/sessions#branch-a-session)
describe separate fork IDs; [its hook reference](https://code.claude.com/docs/en/hooks#sessionstart)
names `fork` as the SessionStart source.
The command names a missing session id or unsupported agent in its error;
the menu hides Fork for those panes and for daemons that lack terminal forks.
A failed menu fork shows a notice with the reason.

Codex keeps its source `--cd` selector for a same-folder fork. A new worktree
fork explicitly sets `--cd` to the new folder. The source folder selects the
repository for the worktree, including a source `--cd` selector. If the
pane's live folder is gone, the fork falls back to the source tab's folder.

`--worktree name` creates a branch and worktree under the main checkout's
`.worktrees`, starting at the source checkout's HEAD, including a detached
HEAD. It uses the same copy, link, port allocation and setup rules as
`pitwall worktree new`. Setup runs in a shell below the fork. Commands from
a repo's setup file wait for Enter, as they do for other worktree tabs.
Relative paths in other kept flags, such as `--settings` and `--add-dir`,
stay as written and resolve in the fork's folder. Use absolute paths when
those files or folders live outside the new worktree.

Uncommitted changes do not carry into a new worktree. Copying staged,
unstaged and untracked changes is deferred. Configured worktree copy and
link paths still apply. Without `--worktree`, both agents share the existing
files, including uncommitted changes.

Deleting an owned worktree tab or running `pitwall worktree rm` refuses
while another tab uses that folder or a folder inside it. The error names
the other tab. The delete dialog warns before **Delete anyway**; the CLI
requires `--force` and prints the same warning. Closing a tab alone keeps
the worktree folder.

<img src="media/fork-delete-dark.png" alt="Shared-folder warning before deleting a worktree, dark theme" width="480"> <img src="media/fork-delete-light.png" alt="Shared-folder warning before deleting a worktree, light theme" width="480">

For a tab on a remote host, that host's daemon creates the fork and any
worktree. The window sends Fork to the daemon that owns the source pane.
Run the command on that host to use its daemon.

### Watching agents

Each sidebar row shows a tab's state:

| State    | Meaning                                                                    |
| -------- | -------------------------------------------------------------------------- |
| Working  | The agent is in a turn                                                     |
| Input    | The agent asked you a question                                             |
| Approval | The agent wants permission to run a tool                                   |
| Plan     | The agent finished a plan and waits for approval                           |
| PR open  | The agent finished its turn, and the tab's branch has an open pull request |
| Done     | The agent finished its turn                                                |
| Error    | The turn failed                                                            |
| `go`     | A terminal is running that command                                         |

A tab running an agent pitwall knows always shows it, idle or busy: the
agent's logo replaces the row icon. The logo goes when the agent exits back
to the shell.

In a repo, the tab shows the repo's icon with the agent's logo as a badge
on its corner, or the icon alone for a shell, and the repo's group shows it
in place of the folder. So does a group you made by grouping tabs, when
all its tabs are in one repo; a worktree counts as its repo. The icon follows the folder the tab's first pane is
in now, so a shell that changes into a repo shows that repo's icon.
pitwall looks for `.pitwall/icon.svg`, `.png` or `.ico` first, so you can
pick one by putting it there. Then it looks for `favicon.svg`,
`favicon.ico` or `favicon.png` at the top, then in `public/`, `static/`,
`app/`, `src/app/`, `assets/` and `apps/*/public/`, then `logo` and
`icon` files. It reads SVG, PNG and ICO files, and looks once per repo
while the window is open. A repo with none gets a tile with its name's
first letter, in its group's color or one picked from the name. An icon
you pick for a group in **Icon and color** wins over the repo's. The
picker's first tile is the repo's icon itself, for a group that has a
repo: pick it to go back to the repo's icon.

After its name, a group header counts its tabs by state, with the glyph
their rows show: tabs that need you (an approval, a question or a plan),
then tabs working, then tabs with a finished turn you have not seen. A
state with no tabs is left out, so an idle group shows nothing after its
name.

A tab with two or more agent panes keeps one row with its most urgent state
and a count such as "3 agents · 1 approval". Under it, each agent pane gets
a small row of its own with the agent's logo, the pane's title and its
state. Click one to show the tab with that pane focused. Click the count to
fold these rows away or bring them back; one that asks for an answer stays.
Plain shells and panes running a command get none. A tab's workers list the
same way, see [Workers](#workers).

When an agent needs you (a question, an approval, a plan, an error, a
finished turn) in a pane you are not looking at, that pane gets a ring in the
state's color and its sidebar row gets an accent bar. Not looking
means another tab, another pane of the same tab, or the window in the
background. The mark stays until you focus that pane. pitwall also sends a desktop notification
for it. Ctrl+Shift+U (Alt+U in the aide preset) jumps to the pane that most
recently started waiting; press it again for the next one. Once you have
seen them all, it walks the waiting panes by priority.

A tab whose Claude Code or Codex asks permission shows Allow and Deny on
its sidebar row, or on the asking pane's own row when the tab lists its
agents, and Ctrl+Shift+Y and Ctrl+Shift+D press them for the
focused pane, else for the shown tab. They press the same keys the
[phone page](phone.md) does, and only while the pane still shows the prompt
the row showed, so a click never lands on a newer question. With a decision
model, its advice stays in the pill above them, and the button it
recommends is outlined. Where the prompt offers to stop asking, such as
Claude Code's "Yes, and don't ask again" or Codex's "(a)" option, an
**Always** button comes up beside them within a second and presses that
option. A prompt with two such options gets no button.

A question from Claude Code's AskUserQuestion shows its options as buttons
on the row, and a click presses that option's number. Long labels are cut
short and the hover card has them whole; options that do not fit leave for a
**+N** button that opens the tab. The buttons need one question with a single
answer and a menu on screen that matches it in full: the whole question and
every option with its description. pitwall reads the screen right before it
presses, so if any of it changed, nothing is pressed and the buttons go within
a second. A click also counts only if the question stayed the same from
pressing the button to releasing it.

Any other menu, such as a multi-select or several questions at once, shows
**Open tab** in place of Reply. Enter on a menu picks the highlighted option,
so a typed reply could not go in. A question without a menu keeps **Reply**.

A Plan row has **Approve plan** where Claude Code's dialog offers "Yes,
manually approve edits". It presses that option, never one that also raises
the permission mode, and a dialog without it gets no button.

The row's **Request changes…**, shortened to **Changes…** beside Approve plan,
is on the planning pane's or worker's own row when the tab lists them. It
opens a field on that row: type what should change and press Enter, or Esc to
cancel. pitwall queues your text and types it, as your reply, the next time
the agent waits for input, so choose No in the dialog yourself. A notice says
when the text waits.

A working agent whose screen has not changed for 20 minutes shows
**quiet 20m** on its row in place of the time, and in its hover card. An
agent can hang mid-turn, and Gemini CLI never reports a failed turn, so
Working alone does not mean busy. Set the minutes with `quiet_after` under
`[agents]` in `config.toml`, or **Quiet after** in Settings; 0 turns it
off. With `continue_when_quiet = true` (**Continue when quiet**), pitwall
also types `continue` and Enter into a quiet agent, once. It does so only
for Claude Code, Codex, pi, Gemini CLI and OpenCode, whose prompts it reads
from the screen, and only while the screen shows no prompt, form or
spinner. It types again only after the agent stops working or you type in
its pane.

A script can put a line of text under a tab's row, such as the state of CI:

```sh
pitwall status 3 -- "CI 3/7"     # tab #3; a pane address works too
pitwall status -- "on review"    # inside a pane: its own tab
pitwall status 3                 # clear it, as --clear does
```

The hover card shows it too, and it goes when the tab closes.

On Linux, a click on a notification raises the window on its pane, as
Ctrl+Shift+U does, and an approval's notification has Allow and Deny too.
This needs notify-send from libnotify 0.7.10 or later; older ones show plain
notifications. macOS notifications from osascript take no actions, so a
click there does nothing, and Windows toasts stay plain.

Settings > Notifications, or `[notifications]` in config.toml, picks what
notifies:

```toml
[notifications]
sound = "bell"                # or a sound file's path; "" (the default) for none
approval = true               # permission prompts and plans
input = true                  # questions, pitwall notify, bells
done = false                  # finished turns
failed = true                 # failed turns
muted_agents = ["codex"]      # claude, codex, pi, omp, gemini, opencode, terminal
quiet_hours = "22:00-08:00"   # no sound, and only approvals, errors and triage's "now"
```

The sound plays with paplay, pw-play or afplay, whichever is installed.
Triage's fyi still sends nothing.

<img src="media/attention.webp" alt="A Codex pane finishes in the background and rings green, the billing tab asks for approval, and Ctrl+Shift+U jumps to each in turn" width="800">

Any command can ask for your attention, no hooks needed:
`npm test && pitwall notify "tests passed"` rings the pane it runs in. Tools
that send terminal notifications ring it too: OSC 9 (iTerm2's form), OSC 777
(urxvt, foot, Ghostty) and kitty's OSC 99 in its single-chunk form. The pane
then shows as Input with the message until you focus it or the agent's state
changes. ConEmu's numeric OSC 9 forms, such as `9;4` progress, are ignored.

A bell (BEL, `printf '\a'`) rings the pane the same way, with "Bell" as the
message, when you are not looking at it. In the pane you are looking at a
bell does nothing, so a failed tab completion stays quiet. A pane rings at
most once a second however many bells arrive, and a pane whose agent already
needs you keeps that agent's state. Set `bell = "off"` under `[terminal]`
to ignore bells.

<img src="media/notify.webp" alt="A test pane runs npm test and pitwall notify, then rings amber and sends a desktop notification saying tests passed" width="800">

States are exact when the agent's hooks are installed (`pitwall hooks
install`). Without hooks, pitwall still recognizes an agent running in a
pane and, for most, reads its state from the screen, which is a little less
precise. It knows an agent by its process name, and one an interpreter runs
(`node`, `bun`, `deno` or `python`, as an npm or pip install does) by
the path of its script. For that it reads the start of the argument list of
the processes in the pane's own foreground group, and nothing else's.

| Agent      | Hooks        | States without hooks    | Resumes after a restart | Side panel and usage |
| ---------- | ------------ | ----------------------- | ----------------------- | -------------------- |
| Claude     | yes          | from the screen         | yes                     | yes                  |
| Codex      | yes          | from the screen         | yes                     | yes                  |
| pi         | an extension | from the screen         | yes                     | yes                  |
| Oh My Pi   | an extension | none (the logo only)    | yes                     | side panel only      |
| Gemini CLI | yes          | from the screen         | yes                     | yes                  |
| OpenCode   | a plugin     | from the screen         | yes                     | no                   |
| Cursor CLI | yes          | none (the logo only)    | yes                     | no                   |
| Amp        | none         | none (the logo only)    | no                      | no                   |
| Aider      | none         | Working, Input and Done | no                      | no                   |

After a restart, a resumed agent whose turn had finished still shows Done.
One that was in the middle of a turn gets one line typed once it is back:
pitwall restarted and resumed the session, so continue the task where it
left off and leave the uncommitted work in place.

Some states are out of reach:

- pi has no permission prompts of its own, so a pi tab shows Working, Done,
  Error or nothing, never Input, Approval or Plan; a dialog an extension
  opens with `ctx.ui.confirm` is not reported.
- Oh My Pi shows the same states as pi. It asks before a tool only when
  `tools.approvalMode` is set (the default, `yolo`, never asks), and pitwall
  does not report that prompt, so the tab stays Working.
- Gemini CLI fires no hook when a turn fails, so with hooks it never shows
  Error; a failed turn stays Working until the next prompt, and its row
  reads quiet once `quiet_after` passes.
- OpenCode 1 shows Plan only with its experimental plan mode on
  (`OPENCODE_EXPERIMENTAL_PLAN_MODE=1`), when it asks to switch to the
  build agent. OpenCode 2 never asks, so it never shows Plan.
- Cursor's prompt and approval hooks decide for the agent, so pitwall does
  not install them: a Cursor tab shows Working, Done, Error or nothing,
  never Approval.
- Amp and Aider have no hooks. Amp gets no state from pitwall's own rules,
  as its screen's text is not documented; turn on its notifications
  (`amp.notifications.enabled`, with `AMP_FORCE_BEL=1`) and its bell rings
  the pane. Aider shows Working while it waits for the model, Input at a
  yes/no question, and Done at its prompt.

OpenCode keeps its sessions in SQLite, which pitwall does not read, so its
side panel shows only its state.

When an agent runs in a pane without reporting, the pane shows "Install
hooks for live status" with an Install button: after the agent has run for
20 seconds without a hook, or as soon as its screen shows a turn (Codex and
Cursor send nothing until their first prompt). Install lists what would
change in each config, then installs on confirm. Settings > Agents has the
same button. Agents already running load their hooks only when restarted.

### Keep the machine awake

Turn on **Keep awake** in Settings, Agents to block Linux sleep or macOS
idle sleep while any agent is Working, including chat turns. The setting
is off by default. Agents waiting for approval or at a prompt do not count. **Keep awake grace**
keeps the inhibitor for 15 minutes after no agent works. Enter a duration
such as `10s`, `15m` or `48h`; `0s` releases at once. Press Enter or leave
the field to save. Clear the field to reset it to 15 minutes. Both settings
apply live. Turning Keep awake off releases the inhibitor without waiting.
The config keys are `[agents] keep_awake` and `keep_awake_grace = "15m"`.

Linux takes a logind `sleep` block lock, leaving idle reporting alone. The
screen can still lock and sleep. Automatic suspend requested through
logind respects the lock. Manual Suspend or the sleep key may be refused
while the lock is held. A closed lid may still suspend the machine; that
follows the machine's settings. Compositor idle daemons may also need to
honor sleep inhibitors.

Systemd 262.1's [idle-suspend path](https://github.com/systemd/systemd/blob/v262.1/src/login/logind.c#L1160)
checks sleep inhibitors. Real suspend, other `IdleAction` values and
desktop overrides remain unverified. macOS runs `caffeinate -i -w <daemon pid>`
only while the inhibitor is needed. It prevents idle sleep only; it does
not block manual sleep. Windows and other platforms do nothing.

An agent stuck in Working keeps the machine awake without a time limit,
even if its row says quiet. A failed turn that sends no hook can cause
this. The grace period starts only after no agent is Working. Turn Keep
awake off to release the lock.

### Sessions

<img src="media/sessions.webp" alt="The session switcher: three sessions with their agents and live counts on the left, the highlighted session's sidebar on the right" width="640">

A session is a separate set of tabs and groups with a name, the way tmux
and zellij work: one per project or per piece of work, each with its own
sidebar. All of them keep running in the daemon. The sidebar header shows
the session's name, the window title is `<session> · pitwall`, and the panes
fade in when the window switches, so you know where you are.

Ctrl+Shift+S (Alt+S in the aide preset), or a click on the session name in
the sidebar header, opens the session switcher. It lists every session with
its agents, how many are working and how many need you, and when it was last
active; a session with something you have not seen gets an accent bar. The
right side draws the highlighted session's tabs and groups as they are now, so you can
watch its agents before you switch. In the switcher:

| Key           | Does                                                                     |
| ------------- | ------------------------------------------------------------------------ |
| j / k, arrows | Move                                                                     |
| Enter, click  | Switch the window to the session                                         |
| 1-9           | Switch to the Nth session                                                |
| any other key | Filter by name (`/` starts a filter that may begin with j, k, n, r or x) |
| n             | New session; type a name or keep the suggested one                       |
| r             | Rename the highlighted session                                           |
| x             | Kill the highlighted session, after a y                                  |
| Esc           | Clear the filter, then close                                             |

Ctrl+Shift+] and Ctrl+Shift+[ (Alt+] and Alt+[ in aide) step through the
sessions without the switcher, and Ctrl+Shift+N makes one. Ctrl+Shift+U
crosses sessions: it switches to the session of the pane that needs you.
Desktop notifications start with the session's name.

With `[[hosts]] window = true`, the host list above the tabs shows each
session's most urgent error, approval, question or plan, even while another
agent works. Otherwise its dot shows an unseen Done or PR open, then work,
or nothing. Unseen Done and PR open dots are green. Looking at a finished
agent in the focused pane of a focused window clears its unseen mark.
A finished parent whose workers still work reads Watching, without an
unseen completion. The count uses the session switcher's needs-you rule
and excludes detached tabs. Errors, approvals and questions count while
open. Plans and finished turns count while unseen. The count appears only
when it fits beside the session name.

Hover a session for 400 ms to open its tab list beside the sidebar. Tabs
follow the dot's urgency order, up to 12 rows, then `+N more`. Move into the
popover to keep it open, or click a tab to switch to it. Rows keep their
order while the pointer is inside. Their pills keep updating. Another
session opens after its own 400 ms hover delay. Leaving both the
session row and the popover closes the list. **Settings > Appearance >
Sidebar > Session preview** turns it off or on live. The setting is
`[sidebar] session_preview`, on by default. The preview uses the state the
window already has, including sessions on other hosts.

<img src="media/session-preview.webp" alt="A session preview in dark and light themes lists waiting agents, unseen finished turns and working agents, with marks and state pills" width="900">

<img src="media/session-dots.webp" alt="Host lists in dark and light themes show plans and unseen finished turns above work, and clear seen finished turns" width="576">

Each window shows one session, and you can open as many windows as you like.
Running `pitwall` again opens another window: on the most recent session no
window shows, else on a new session in the folder you ran it from.
`pitwall -s <name>` opens one on that session, making it if it does not
exist. Asking for a session another window already shows raises that window.
When a session ends, because you killed it or closed its last tab, its
windows move to the most recently used session, or close when none is left.

### Tabs and panes

Right-click a pane and choose **Open scrollback in editor** to search or copy
its history and screen in your editor. When a program owns the mouse, use
Shift-right-click. The editor runs in a normal terminal split to the right.
The daemon uses `$VISUAL`, else `$EDITOR`, else `vi`; quoted executable paths
and arguments work. The file holds plain text with wrapped lines joined.
Pitwall removes the temporary file when the editor process exits, including
when you close its pane. Use a terminal editor, or your editor's wait flag,
so the process stays open while you read the file.

The `edit_scrollback` action is unbound by default. Bind it under `[keys]`
or run **Open scrollback in editor** from the command palette. With a remote
host, the file and editor are on that host.
`pitwall scrollback <pane> [--lines N]` prints the same text to stdout
without opening an editor.
Full-screen programs that use the alternate screen export only their current
screen. Editor panes close on a daemon restart or reboot.
CLI exports above 63 MiB return an error; use `--lines N` for fewer lines.
The editor action has no reply-size limit.

<img src="media/scrollback-editor.png" alt="A pane's scrollback open in nvim in a split to its right" width="800">

<img src="media/panemode.webp" alt="Pane mode: split down, move focus right, toggle fullscreen and leave with Esc, with the PANE hint showing the keys" width="800">

The keyboard shortcuts are in [Keybindings](keys.md). With the mouse:
click a tab to switch to it, double-click it to rename it, middle-click to
close it, right-click it for **New tab below**, **Rename tab**, **Detach
tab**, **Close tab**, grouping, and more. The "+" in the sidebar header or on
a group header opens a new tab right below the one you were in, in the same
group and folder.

The same menu pins a tab and colors it. As in a browser, pinning moves the
tab to the top, after the tabs already pinned (inside its group when it
has one), and unpinning puts it right after them. A pinned tab shows a pin
after its title and does not close: middle-click, "×", Close tab, the close key and
closing its last pane ask you to unpin it first, and `pitwall kill` and
`pitwall tab close` refuse it. Archive on merge leaves it alone. Ending its
whole session still closes it. The row of dots under Pin tab tints the tab
in one of eight colors, or none.

A tab an agent opens from its pane, with `pitwall new` or `pitwall
worktree new`, is that agent's worker. It joins
the agent's group, or stays out of groups with an ungrouped agent, even
when its folder has a group of its own. The sidebar lists it under the
agent's row as a small row of its own (see [Workers](#workers)). When the
agent's own turn ends while a worker still works, its pill reads Watching,
a blue outline with an eye, until the last worker stops. Ctrl+Shift+U
still jumps to a folded worker that needs you. [The agent skill
page](agent-skill.md#workers) has the commands an orchestrator runs.

Rest the pointer on a tab for half a second and a card opens beside the
sidebar with what the row cuts off: the full title, the group and folder, the
branch with its diff stats, the agent's state and question, and the pane
count. Move to another tab and the card follows at once.
The card stays aligned with its tab below the host, session and waiting lists.

<img src="media/tab-hovercards.webp" alt="Tab hover cards align with the tab below two hosts and a waiting agent in dark and light themes" width="1000">

Drag a tab or a group header to reorder it: the other rows slide apart to
show where it will land, and Escape puts it back. Groups and ungrouped tabs
share one order, so a group can sit above loose tabs and a loose tab between
two groups: drop on the top half of a group header to land above the group,
on the bottom half to land first inside it. Rest on a group header for
a moment, or drop on a collapsed one, to move the tab to the end of that
group. Ctrl+click or Shift+click several tabs to drag them together. Drag the
gaps between panes to resize them.
Ctrl+Shift+B (Ctrl+B in the aide preset) hides the sidebar; pitwall
remembers that across restarts.

Ctrl+Shift+L opens the agent panel on the right. It follows the focused pane and
reads the agent's own session file: Flow shows the current turn from prompt
to outcome, with a pending approval and the decision model's suggestion;
Subagents lists what each spawned agent was asked and is doing; Plan is the
agent's todo list; Changes lists the files changed from the default branch;
Timeline lists prompts, tool calls and subagents with their times. The panel
is 432dp wide (scaled with the display), narrows to 320dp in a small window,
then hides. Each window keeps its own open state until it closes. Ctrl+L still
reaches the shell, to clear the screen.

<img src="media/review.webp" alt="The review view: three changed files on the left, the diff of rateLimit.ts on the right with two pending comments, and a Send 2 comments to claude button" width="800">

To review an agent's work, press Ctrl+Shift+R (Cmd+Shift+R in mac), pick
View diff from the tab's "…" menu or the command palette, or click a file
under Changes. The review
view takes the place of the tab's panes, as the settings page does, and Esc
closes it. It shows the changes from the merge base with the default branch
to what is on disk now: committed, staged, unstaged and untracked. The files
are on the left with their status (M, A, D, R for a rename, ? for untracked)
and line counts; the selected file's diff is on the right, with both line
numbers and the unchanged lines between hunks folded into a row you click to
unfold. j and k move between files, n and p between hunks, the arrows
between lines, Shift+arrows select a range. A binary file, an untracked file
over 1 MiB and a file with over 3,000 changed lines show a line about
themselves instead; the last has a button to show its diff anyway.

Staging, next to Branch and This turn in the header, is for committing
part of an agent's work. It lists the uncommitted files in two groups:
Unstaged, the work tree against the index, with untracked files at the
end, and Staged, the index against HEAD. A file staged in part shows in
both. s stages the hunk the selected line is in, or the next one below
it, so pressing s again moves down the file; u unstages a staged file's
hunk. Shift+S and Shift+U, the Stage file and Unstage file buttons, and
the checkbox in the file list do the whole file. Each hunk's header has a
Stage hunk or Unstage hunk button too. A hunk goes in through
`git apply --cached` (with `-R` to unstage) and a whole file through
`git add` or `git restore --staged`; the list reads again after each.
Untracked, added, deleted, renamed and binary files, and files over 3,000
changed lines, stage whole. A file a merge left in conflict heads Unstaged;
staging it, once you have resolved it, marks it resolved.

Click a line number, or press c on the selected lines, to comment;
Shift+click a second line number to comment on the lines between. Enter
saves the comment under its last line. "Send N comments to claude" types
every pending comment into the tab's agent as one prompt: a line saying what
it is, then each comment's file:line, the lines it is on quoted with their
diff marks, and its text. It goes in as a bracketed paste followed by Enter,
the way the phone's replies do. An agent that is working, asking permission
or showing a plan gets it when it next waits for input; the comments read
Queued until then, and Sent after. Comments still queued when the agent's pane
closes go back to pending. A sent comment goes away once the agent changes its
lines.

Mark reviewed checks a file off in the list. pitwall remembers the mark per
worktree and per change, in `gui.json` in the state directory, so a file the
agent changes again comes back unchecked. Discard changes, after a dialog,
puts the file back as it is at the merge base with `git restore`, on disk
and staged, or deletes an untracked file. The header shows the branch, its
base and the totals, Create pull request, and Open in pager, which runs the
same diff through `git diff` in your pager in a new pane: `$GIT_PAGER`, else
`core.pager`, else delta when it is installed, else `less -R`. Press q to
close it. The palette's Open diff in pager does the same. With `pitwall
--host`, the worktree is on the host, so View diff opens the pager there.
To stage hunks there, the palette's Stage hunks in terminal runs
`git add -p` and then `git reset -p`, to take a hunk back, in a new pane in
the tab's worktree; q ends either one.
Create pull request opens a tab in the tab's folder that runs
`git push -u origin HEAD` when the branch has no upstream, then
`gh pr create --fill`, so you see both commands and answer gh's questions
there; the tab stays when gh exits, with the pull request's link. It never
force-pushes. The menu greys the entry out and says why when gh is not
installed, the tab is on the default branch, or the branch has no commits
ahead of it. Create pull request and Open diff in pager have no key by
default; set `create_pr` or `diff_pager` under `[keys]` to give them one,
and `stage_terminal` for Stage hunks in terminal.

Second opinion has another agent review the tab's changes and send its
findings to the tab's agent. The tab's "…" menu has it as Second opinion
from Codex for a Claude Code tab and from Claude Code for a Codex tab, and
offers both for any other agent. The review view's Second opinion button,
the command palette and `pitwall review [tab] [--agent claude|codex]` pick
the same reviewer, Claude Code for another agent, unless `--agent` names
one. pitwall splits the tab and starts the reviewer beside the tab's agent,
in its folder, read only: Claude Code with `--permission-mode plan`, Codex
with `-s read-only`. Its prompt is one line pointing at a file in the state
directory, `reviews/<pane>.md`, which asks it to review the branch against
its base and the uncommitted and untracked changes, to change nothing, and
to end with a summary that gives each finding a severity (blocker, major,
minor or nit), a `path:line` and a fix. When the reviewer's first turn
ends, pitwall queues that reply for the tab's agent, as `pitwall send`
does, under one line asking it to check each blocker and major against the
code before changing anything and to say which findings it dropped and
why. That is the only round: the reviewer's pane stays open for you to
read, and its later replies stay there. Close it as any pane. A reviewer
that asks permission shows Approval like any agent; pitwall never answers
it. The entry is greyed out with the reason when the tab runs no agent,
its agent reports nothing through hooks, or the reviewer's CLI is not on
PATH, and it shows only for a tab in a git repository. A daemon restart
before the reviewer finishes drops the hand-off. Second opinion has no key
by default; set `second_opinion` under `[keys]` to give it one.

When an agent with hooks gets a prompt, the daemon snapshots the tab's
files: everything on disk, untracked files included and ignored ones left
out, as a commit object under `refs/pitwall/turns/<tab>/<n>`. It builds the
snapshot with an index of its own, so your index, stash, branches and HEAD
stay as they are. A tab keeps its last 20 snapshots, and closing the tab
deletes them. Once a tab has one, the review view's header has Branch and
This turn. Branch is the diff above. This turn is what changed on disk since
the latest turn started, and the file list and totals follow it. It is read
again when you switch to it, when the agent changes state and when a new
turn starts, not every few seconds, since each read snapshots the files. The
view opens on This turn when the agent finished its turn. The prompt's hook
waits up to 2 seconds for the snapshot, so the turn's first edits stay out
of it. Agents without hooks get no snapshots, so their tabs have no This
turn.

Undo turn, in This turn, asks first, naming the file count, then puts the
files back as they were when the turn started: it deletes the files the
turn added and brings back the ones it changed or deleted. It writes files
only, never the index, HEAD or a commit. Once HEAD moved since the turn
started, for example because the agent committed, Undo turn is hidden and
the line under the header says why. Before it restores, Undo turn
snapshots the files again, so Redo turn brings the turn back. Redo turn
shows until you change a file or the next turn starts, and refuses if a
file changed since the undo. Undo turn also refuses, changing nothing, when
the turn changed a `.gitignore` and files would be deleted, since a file it
ignored before can look new, and when a file no snapshot has, such as an
ignored one, is in the way of a file it would write back. This turn has no
Discard changes; Undo turn is its way back.

Once the branch has a pull request, its row shows a chip with the number,
green while open, grey as a draft, purple once merged and red when closed.
On an open one, a dot before the number is CI: yellow while checks run,
green when they pass, red when one failed, and a check or a red diff glyph
after it is the review: approved, or changes requested. Once nothing stops
a merge, a green Ready replaces the review, and Merge PR in the menu says
ready. The hover card says why an open pull request cannot merge yet, the
first that applies: it conflicts with the base branch, checks failed (by
name), changes were requested (by whom, and "awaiting re-review" when the
review is older than the last commit), checks still run, or it is a draft,
has no approving review or is blocked by branch protection. Under that it
lists the checks by name. Click the chip to open the pull request in your
browser.
The daemon asks `gh pr view` about each branch: at once for a new branch,
every minute for an open pull request and every five for a branch without
one, five times slower while no pitwall window has focus, and not at all
for 15 minutes after GitHub says its rate limit is hit. It never asks you to
log in: without `gh`, or logged out, the row shows nothing. When `origin`
is not on GitHub, such as GitLab, Gitea or Forgejo, and the branch has
commits pushed there, the "…" menu has Open compare page in place of
Create pull request. It opens the host's page to compare the branch with
the default branch and open a pull request from it: `/-/compare/` on a
host whose name has `gitlab` in it, `/compare/` on any other.

The "…" menu and the command palette then have Open PR, Merge PR and, when
a check failed, Re-run failed checks in place of Create pull request.
Merge PR asks first and, with failed checks, asks again; it runs
`gh pr merge --squash` in a new tab, or `--merge` or `--rebase` per
`merge_method` under `[git]`. Re-run failed checks runs
`gh run rerun --failed` on the branch's latest failed run, in a tab too.

Two more entries hand the pull request's problems to the tab's agent.
Send failures to agent shows when a check failed. It asks `gh pr checks`
which checks failed, reads the last 150 lines of each GitHub Actions job's
failed steps with `gh run view --job <id> --log-failed`, and sends one
prompt: each failed check with its link, the end of each log, and "Fix
these and push." It names the first 20 failed checks and counts the rest.
A check from another CI, such as Jenkins, goes in by name and link only,
and a job whose log `gh` cannot read says "log unavailable" with the
reason. Logs are cut from the top, and the prompt never passes 30 KB.
Send review comments to agent asks GitHub for every unresolved review
thread and sends each one's file, line and comments, plus the text of
each reviewer's change request that still stands: a later approval lifts
it, a comment does not. It is in the menu for any open pull
request, since pitwall only learns about unresolved threads when you ask.
The hover card also shows Send failures to agent when a check failed, and
Send review comments to agent when a reviewer requested changes. Both
fetch in the background, then type the prompt into the tab's agent as the
review view sends comments: at once when the agent waits for input, else
when it next does. An agent holds one such prompt at a time: while one
waits, another is dropped and a notice says so. A notice says whether it
went or waits, and when `gh` cannot list the checks or the review, its
error shows at the top of the panes until you close it. They need
`gh` and an agent in the tab, and are off in a window opened with
`--host`. Neither has a key by default; set `send_failures` or
`send_review` under `[keys]` to give them one.
When the pull request has merged, the row says Merged and its hover buttons
add Archive, which closes the tab and, for a worktree pitwall made, removes
the worktree and deletes the branch, as Delete does with its box ticked.
Set `archive_on_merge = true` under `[git]` to archive a merged worktree
tab without the click.

In Settings, Agents, Pull requests, turn on **Send CI failures automatically**
or **Send requested changes automatically**. Both are off by default. The
config keys under `[git]` are `send_failures_on_fail` and
`send_review_on_request`. The older `auto_send_failures` key still works;
an explicit `send_failures_on_fail` takes precedence.

The daemon sends failed checks once per head commit, only once GitHub
reports the commit the tab has checked out. Requested changes go once per
review, so pushing a new commit does not resend the same review. Both use
the same fetch and prompt formatting as the manual commands. A tab without
an agent gets nothing. A busy agent holds at most one message until it
waits for input. A delivery notice says "Sent CI failures to <agent>" or
"Sent review comments to <agent>". Failed or empty fetches can retry at the
next poll. These sends also work with the window closed. The daemon keeps
its deduplication history in memory, so a restart allows another send.
Fleet workers with escalation enabled still escalate after two failing
commits, as described below.
<img src="media/conflict.webp" alt="The hover card of a tab whose branch conflicts with another: its pull request and checks, the conflicting file in red, the suggestion to merge this tab first, and its ports" width="600">

Agents in worktrees of one repository can edit the same files without
knowing it. When two tabs on different branches of the same repository
change a file in common, committed or not, and one of them has an open
pull request, the other's row gets a warning triangle after its diff stats
(both rows, when both have one): amber when they only touch the same files,
red when merging their commits would conflict (`git merge-tree`, which
leaves the work trees alone). Overlaps between branches with no open pull
request, such as a fleet of agents still at work, show only in the hover
card.
Uncommitted changes count as touching, never as a conflict. The hover card
lists every overlap: it names the other tab, lists the files with the
conflicting ones first in red, and suggests which branch to merge first: the
one with fewer conflicting files, else the one not behind the default
branch, else the smaller one. Click a file in the card to open that tab's
review view on it. The first time two tabs share a file while one of them has an open pull request, a notice says so, such as
"billing and acme-api both edit src/server/router.ts"; it comes back only for
a new file or a new conflict. pitwall checks when it refreshes the branch
stats, at most every 30 seconds, and only while tabs sit on two or more
branches. Set `conflict_radar = false` under `[git]` to turn it off.

A soft green broadcast mark under a tab's agent icon means you can drive
that agent from another device: Claude Code with `/remote-control` on.
pitwall reads it from Claude Code's own session files
(`~/.claude/sessions/<pid>.json`) about once a second. Turn it off under
Appearance in Settings, or with `remote_control = false` under
`[appearance]`.

Under the tabs, a line shows the agent's model, the tokens its session used
and how full its context window is, with a small meter that turns yellow at
80% and red at 95%. Flow's Session section splits the tokens into input,
output, cache read and cache write, by model when the session used several,
subagents included. A tab's hover card shows the same line for its agents.
The counts come from the agent's session file: Claude Code's message usage,
each message counted once, Codex's token counts, pi's and Gemini CLI's
message usage. A file over 8 MiB is read from its last 8 MiB, so its counts
end in "+". Cost in dollars is off, since a subscription does not bill per
token; set `show_cost = true` under `[usage]` or use the switch under Agents
in the settings page to add an estimate of what the tokens would cost at the
API's list prices, such as `412k tokens · ~$3.20`. The hover card adds "at
API prices", and Flow's Session section names the prices' date. pitwall
carries a dated price table for Claude and OpenAI models, Claude's fast mode
included; a model missing from it, such as Gemini or Claude Haiku 5.5, shows
tokens and no cost.

<img src="media/cost.webp" alt="Settings, Usage, Cost: the 30-day total split between Claude Code and Codex, and a daily cost chart" width="640">

Settings, Usage adds up every Claude Code, Codex and pi session on the
machine over the last 7, 30 or 90 days: the total cost and each agent's
share, a daily cost chart, cached and uncached input, output, what cache
reads saved against the full input price, and a table by model or by day.
It reads `~/.claude/projects`, `~/.codex/sessions` and pi's `sessions`
directory, opens only files written in the window, and on a reopen reads
only what the agents appended since. A call that Claude Code repeats in a
subagent's or a resumed session's file counts once. Models without a list
price show as Unpriced and add tokens but no cost.

Above the costs, Limits shows how much of each plan limit Claude Code and
Codex last reported using: a bar per window (5-hour and weekly), when it
resets, how long ago the agent reported it, and, at the pace of the window
so far, when it would reach 100% if that is before the reset. A window
whose reset time has passed shows 0% until the agent reports again. Codex
logs its limits in its session files; pitwall reads the last 8 days of them
every minute. Claude Code reports its limits only to its status line, so
the Set up button there opens the hooks dialog with pitwall's status line
ticked (see [Hooks](hooks.md#plan-limits)). pitwall never asks Anthropic or
OpenAI for them. When a window reaches 90%, a notice at the top of the
panes says so once, such as "Claude 5-hour window at 90%, resets 16:40".
Set `limits_in_sidebar = true` under `[usage]`, or use the switch under
Limits, to show each agent's fullest window at the bottom of the sidebar,
yellow from 80% and red from 95%.

Selecting text with the mouse copies it to the clipboard, as zellij and Warp
do, and a small "Copied 42 characters" notice shows at the bottom of the panes.
Drag to select, double-click a word, triple-click a line (the whole line
the program printed, across the rows it wrapped onto), Alt+drag a block, or
hold Shift when a program owns the mouse. Drag past the top or bottom of the
pane to scroll it, faster the further you go, so one selection can take any
amount of history. A selection stays on its text while new output scrolls
it, and goes when that text leaves the history or the pane is resized. Copy
mode selects with the keyboard; see [keys](keys.md). To turn copying on
select off, set `copy_on_select = false` under `[terminal]` in
config.toml or use the switch under Terminal in the settings page; Ctrl+Shift+C
still copies.

Links in panes are underlined, both URLs in the text and OSC 8 hyperlinks.
Ctrl+click one (Cmd+click on macOS) to open it in your browser, even inside
Claude Code or Codex.
Set `links = false` under `[terminal]` to turn this off.

In a pane running Claude Code or Codex, Ctrl+V and the paste key
(Ctrl+Shift+V, Cmd+V on macOS) both paste whatever is on the clipboard.
Text goes in as a bracketed paste. When the clipboard holds an image,
pitwall reads it with `wl-paste` on Wayland, `xclip` on X11, `osascript`
on macOS or PowerShell on Windows. On Linux, install `wl-clipboard` or
`xclip` for your display server. The daemon saves the image in a private
`paste/<tab-id>` folder under pitwall's state directory and the window
pastes its quoted path. Claude Code and Codex attach the file as an image.
Images may be up to 20 MiB and stay available until the tab closes, including
when you close and reopen the window. Closing the tab removes its saved
images. A plain shell ignores clipboard images. Ctrl+V in other programs
still reaches the program as a key, so Vim's block select and the shell's
quoted insert work as before.

Drop image files from your file manager onto a pane to paste their quoted
paths. The drop goes to the pane under the pointer, even when another pane
has keyboard focus. Spaces and quotes in file names stay part of the path.
A plain shell also receives dropped file paths. Dropped source files are
left in place when the tab closes.

In a `pitwall --host` window, clipboard images and files dropped onto an
agent pane are sent to the host and saved under its state directory, then
the agent receives the host's path. Files dropped onto a remote shell paste
the local path; transfer those files yourself before using that path on the
host. A missing clipboard tool, failed upload or old daemon shows an error
under the pane and pastes nothing.

Programs can set the clipboard with OSC 52, the way Neovim, tmux and Helix
copy, also over ssh: `printf '\e]52;c;%s\a' "$(printf hi | base64)"` puts
"hi" on it. A write may hold up to 1 MB of text. Programs can never read
the clipboard this way: pitwall leaves OSC 52 read requests unanswered, so a
program cannot see what you copied elsewhere. Set `osc52 = "off"` under
`[terminal]` to ignore the writes.

Panes show images that programs print with the kitty graphics protocol,
such as `kitten icat`, `chafa`, `timg` and agents that preview a
screenshot. Try it with a PNG file:

```sh
printf '\e_Ga=T,f=100;%s\e\\' "$(base64 -w0 picture.png)"; echo
```

An image sits on the cells it was placed on and scrolls with them into the
history; clearing the screen (`clear`) or the program's delete command
removes it. Each pane keeps up to 64 MB of decoded images and all panes
together 256 MB; past that the oldest go. pitwall takes images sent in the
escape sequence itself, in chunks or whole, as PNG, RGB or RGBA, zlib
compressed or not. It refuses a program's request to read an image from a
file, a temporary file or shared memory, because a program in a pane must
not make the daemon read paths for it; programs that ask first, as
`kitten icat` does, then send the image in the sequence. pitwall does not
do animation, Unicode placeholders, relative placements or images under
the text (negative z-index), and images on the alt screen stay where they
were placed when the program scrolls. The web protocol carries no pane
screens, so it has no images either. Set `graphics = "off"` under
`[terminal]` to turn images off for new panes: pitwall then answers
graphics queries as a terminal without images does, so programs print
text instead.

#### Shell integration

Ctrl+Shift+Up and Ctrl+Shift+Down scroll the pane back and forward to the
previous and next shell prompt, putting it at the top of the pane; past the
last prompt the pane is back at the live screen. This needs a shell that
marks its prompts with OSC 133 (shell integration). fish 4.0 and later
mark them on their own. For zsh, add to `~/.zshrc`:

```zsh
_pitwall_precmd() { print -Pn '\e]133;D;%?\a\e]133;A\a' }
_pitwall_preexec() { print -n '\e]133;C\a' }
autoload -Uz add-zsh-hook
add-zsh-hook precmd _pitwall_precmd
add-zsh-hook preexec _pitwall_preexec
PS1+=$'%{\e]133;B\a%}'
```

For bash 4.4 or later, add to `~/.bashrc`, after anything that sets `PS1`:

```bash
PROMPT_COMMAND='printf "\e]133;D;%s\a\e]133;A\a" "$?"'${PROMPT_COMMAND:+;$PROMPT_COMMAND}
PS1+='\[\e]133;B\a\]'
PS0='\e]133;C\a'
```

Only the prompt start (`133;A`) is needed to jump; pitwall keeps the
other marks on their rows too. The keys are `prev_prompt` and
`next_prompt` in config.toml. Without prompt marks, a jump opens the
[browser's shell integration help](#command-scrollback-browser).

### Picture in picture

Picture in picture keeps one pane in view while you work in another tab.
Right-click a tab and choose **Picture in picture**, or press
`picture_in_picture`, which no preset binds; the command palette lists it
too. The tab's focused pane then shows as a small card in the bottom right
corner of the pane area, live but read only: typing and scrolling still go
to the pane you are in. The card's header shows the tab's name, the pane's
agent and its status glyph, as the sidebar does.

Drag the card to move it to another corner. Click it to go to its tab,
where the pane shows in full and the card hides until you leave. The ×,
**Close picture in picture** in the tab's menu, or the key again in that
pane closes it. Pinning another pane replaces it, and closing the pane
closes the card. The card belongs to the window: it is not saved, and
another window does not show it.

The card shows the pane's live screen even when its tab is scrolled back
in history; the tab keeps its place in the scrollback for when you go back.
A pane too wide or too tall for the card shows its left part and its rows
down to the cursor or the last line of text, whichever is lower, where an
agent's prompt is.

### Agent errors

Settings, Agent errors shows where your agents waste steps and tokens. From
the hooks, the daemon keeps every tool call that failed and every turn the
API ended, in `errors.jsonl` in the state folder: its time, agent, command
or file path, the end of its output, exit code, folder and how long it ran.
It skips nothing. A command that exited 1 with no output, as grep finding
nothing, is marked quiet and a call you stopped is marked interrupted, and
each of those gets a group of its own. Secrets in the command and output
are redacted before the line is written, and at 10 MB the file moves to
`errors.jsonl.1`.

What each agent reports:

| Agent       | Failed tool calls                                                   | API errors |
| ----------- | ------------------------------------------------------------------- | ---------- |
| Claude Code | every failed tool call (PostToolUseFailure)                         | StopFailure |
| Codex       | shell commands whose last line reads as an error, such as `zsh: command not found`; Codex hooks carry no exit code and fire nothing for a failed patch or MCP call | none |
| Cursor      | every failed tool call (postToolUseFailure)                         | a stop with status error |
| Gemini CLI  | a tool's error, and shell commands that exit non-zero (AfterTool)   | none: Gemini has no hook for a failed turn |
| OpenCode    | a tool that errors, and shell commands that exit non-zero (pitwall's plugin) | session errors |
| pi, omp     | every tool result marked an error (pitwall's extension)             | a run that ends in an error |

The page groups the errors of the last 7 days, 30 days or all time by the
line that says what went wrong, with numbers, paths and quoted text set
aside, so `zsh: no matches found: --include=*.go` and `--include=*.ts` count
as one. Each group names its latest error's tab when the log has its name,
even after that tab closes. Each group shows how often it happened and about
how many tokens its commands and output took, at four characters a token; Most frequent and
Most tokens sort by either. A group with a known cause shows a Likely fix
line: quote globs zsh would expand, quote words that start with `=`,
install a missing command, read a file again before editing it, check
`package.json` before `npm run`, or pick a model your plan includes. Rate
limits and overloaded APIs are marked as nothing to fix locally.

Copy report puts the top groups in the order shown, each with its latest
command, output and likely fix, on the clipboard as Markdown to paste into
an agent and ask it to fix the causes in CLAUDE.md, your shell config or a
missing tool. Ask an agent to fix these opens New task with that report as
the prompt. The prompt asks the agent to group the errors by cause, propose
the smallest fix for each with the tokens it would save, show each change
as a diff, and ask before it edits any file outside the project. You pick
the agent and folder as for any task.

Above the groups, four tiles give the errors and their causes, the tokens
they took, the errors on a day that had any, and the top cause's share.
Errors per day stacks each day's commands, tool calls and API errors; hover
a day for its counts and tokens. Where errors come from splits the window by
its four most frequent causes and the rest. Hovering a cause shows its count
and tokens and outlines its group in the list below in the cause's color.

### Timing

Settings, Timing shows where your agents' time goes. A turn runs from your
prompt to the agent stopping, failing, being interrupted or its pane
closing, and the daemon writes one line per turn to `timing.jsonl` in the
state folder: its tab, branch, agent, start and end, and how its time split
between the model, tools and waiting on you for an approval or an answer.
Tool time comes from the hooks around each call, so parallel calls count
once, and commands sort into tests, builds, lint, git, CI and `gh` (watches
and sleeps included), installs and other shell; other tools into reading,
editing, subagents, web and MCP. Model time is what is left. Claude Code,
Codex, Gemini CLI and OpenCode bracket every call that runs. Claude Code
and Codex send no end for a call another hook blocked, you denied or the
agent cancelled, and Codex none for a command still running when it hands
the output back. Such a call is over once the agent starts another after
some call ended, or once the turn finishes, and counts for nothing: its
time goes to the model or the calls beside it. A turn that stops without
the agent finishing it, because you interrupted it or closed its pane, ends
at most ten minutes after the agent's last call, so a session that hung
does not count the hours it sat. Cursor reports only how
long a call ran, and pi and omp only calls that failed, so their tool time
is short. Each turn keeps its five slowest commands, redacted and cut to
200 characters; prompts and output are never stored. The file also gets a
line when a tab gets its first prompt, opens a pull request, finishes CI,
merges and closes. The page shows 7 days, 30 days or all time: a stacked
bar, agent hours per day, the commands that took longest and each task's
time from prompt to merge. Copy report puts it on the clipboard as Markdown
to ask an agent what to speed up. At 10 MB the file moves to
`timing.jsonl.1`.

Use **Leave out of stats** on a task row to append its tab name to
`[stats].exclude` in `config.toml`. Timing and Agent errors leave out tabs
whose name or branch matches any of these globs. Both pages show
"Excluding N tabs" with the patterns when the window has matching tabs.
The logs keep every event. Remove a pattern from the config to include
those tabs again. The row action escapes glob characters in a tab name and
preserves existing patterns and comments in the list.

For turns ending before 2026-10-31 19:00:39 UTC, the summary caps agent time
at ten minutes only when the turn was interrupted or its pane closed.
Completed and failed turns, and turns ending at or after that cutoff, keep
their recorded durations. Waiting on you stays intact; model, tool and slow
command times keep their recorded proportions. This is an estimate for old
abandoned turns because those lines have no last-activity timestamp.

### Claude Code agent teams

<img src="media/tmux-dark-large.png" alt="A team lead and its researcher teammate in native split panes opened through the tmux shim" width="800">

Run this from a shell pane in pitwall:

```sh
pitwall tmux-shim -- env CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1 claude
```

Each teammate opens as a native split in the lead's tab. Native splits keep
teammates in the same tmux window and use pitwall's pane input, agent detection
and process cleanup. They do not create worker tabs or worktrees.

The command gets a `tmux` link first on its `PATH`, with `TMUX` and `TMUX_PANE`
set to this daemon and pane. Each split gets its own pane id and the shim's
`PATH`. Your shell's environment and the real `tmux` stay unchanged. The link
stays in the daemon's private runtime directory so workers can outlive the
lead. The shim is off until you use this command. It requires a Unix PTY and
a daemon at protocol Level 51 or above. An older daemon returns an error;
the launcher does not restart it.

Flags after `--` belong to the command. To keep the shim active in a shell:

```sh
pitwall tmux-shim -- sh
```

The supported commands are:

| tmux command | pitwall behavior |
| --- | --- |
| `split-window` | Split the target pane, vertically or with `-h` horizontally. `-d` preserves focus, `-c` sets the directory, `-e NAME=value` sets environment variables, and `-P -F` prints the new pane. `-l 70%` or `-p 70` gives the new pane 70 percent of the split |
| `respawn-pane -k` | Replace a shim pane's process, keeping its id, title and place. One command argument runs through `/bin/sh -c`; several form an argv. An empty command repeats the pane's command |
| `send-keys` | Type text and tmux key names such as `Enter`, `C-c` and `Up`. `-l` sends literal text, `-H` sends hex bytes, and `-N` repeats |
| `list-panes`, `display-message -p` | Expand `-F` formats and print pane ids, titles, indices, sizes, directories and commands. `display-message -p '#{window_id}'` names this tab |
| `kill-pane` | Close the pane and its processes |
| `select-pane` | Focus the pane, or name it with `-T` without moving focus |
| `capture-pane -p` | Print the screen. `-S` and `-E` include history using tmux line numbers; `-` means either end |
| `has-session`, `list-sessions`, `list-windows`, `rename-window` | Query or name the caller's session and tab |
| `set-option`, `set-window-option` | Accept pane styles and border formats while pitwall keeps its own appearance. `remain-on-exit` accepts `off`, `on` and `failed` |
| `select-layout`, `resize-pane` | Accept the named layouts and `-x` or `-y` sizes used by agent teams. The native split tree keeps its layout and ratios |
| `-V` | Print `tmux 3.4` |

Targets accept `%N` pane ids, `@N` window ids and `session:0.pane` addresses.
Every query and mutation stays inside the caller's tab, including
`list-panes -a`. A target in another tab fails. `-S` must name this daemon's
socket; another server or `-L` fails.

Formats support `#{variable}`, tmux's short aliases such as `#D` and `#T`,
`##`, `#,`, `#}`, `#{?condition,yes,no}`, `#{==:a,b}` and `#{!=:a,b}`.
Unknown variables expand to empty, as in tmux. Other format expressions,
unknown commands and unknown flags fail with a message and a nonzero exit
code. Cell sizes on `split-window` produce equal halves. Command chaining,
control mode, buffers and creating or killing sessions are unsupported.

### Omarchy theme

On [Omarchy](https://omarchy.org), pitwall takes the colors of the Omarchy
theme in force when config.toml names no theme. `[theme] name = "omarchy"`
asks for it by name, and any other name keeps that theme. pitwall reads
`~/.local/state/omarchy/current/theme/colors.toml` (Omarchy 4) or
`~/.config/omarchy/current/theme/colors.toml` (Omarchy 3), resolving its keys
the way `omarchy-theme-color` does for the rest of the desktop.

Switch the Omarchy theme and the window, the panes and the daemon follow
within a fraction of a second. pitwall watches the current-theme folder with
inotify, so an unchanged theme costs nothing. Programs in panes see the new
colors the next time they query them (OSC 4, 10, 11, 12). One that turned on
mode 2031, as Claude Code does, gets `CSI ? 997 ; 1 n` when the
background turns dark and `CSI ? 997 ; 2 n` when it turns light.

Panes use the theme's terminal colors as Omarchy's own terminals do. The
window builds its grounds from the theme's background and its text from the
foreground and `muted`. The accent becomes `primary`, and red, yellow, green,
blue and magenta become the status colors. A text color below 4.5:1 on the
sidebar or a selected row, or a status color or the accent below 3:1, is
mixed toward white on a dark theme (black on a light one) in steps of 1/16
until it reads. Hackerman's `muted`, `#2d3450`, measures 1.6:1 on its
background, so pitwall draws muted text lighter than the theme asks.
`[theme.colors]` entries still override any color. Omarchy installed after
pitwall started needs a restart to be followed.

### Command scrollback browser

<img src="media/scrollback-dark-wide.png" alt="Command blocks with exit status, start time, output size and copy buttons" width="800">

Press Ctrl+Shift+J in aide or conventional, or Cmd+Shift+J in mac, to browse
commands in the focused pane. The command palette lists **Browse command
blocks in the focused pane** with its current key.

The browser takes a snapshot of commands retained in the pane's scrollback.
Each block shows the command, exit status, local start time and output size
in bytes. The preview shows the selected block's output. Start time is when
pitwall receives the command-start mark. Output size counts rendered UTF-8
text, without terminal escape sequences.

- Up/Down or k/j selects a block. Home/End selects the oldest/newest block.
- PageUp/PageDown scrolls the output preview. The mouse wheel scrolls each list.
- c copies the command, y copies its output, and Shift+Y copies both.
  The buttons do the same. Copies omit the shell prompt and preserve output
  line breaks. A command with no output copies only its command with **Both**.
- Escape or q closes the browser.

The browser needs [OSC 133 shell integration](#shell-integration), with A,
B, C and D marks for prompt start, input start, command start and command end.
Without marks, the browser says that the shell needs integration and offers
the docs link. pitwall does not guess commands from prompt-like text.
A command-start mark without an end mark shows **running**. An end mark
without an exit code shows **exit unknown**.

Only retained commands have blocks. A command whose prompt has left
scrollback has no block. The browser shows at most 16 MiB of the newest
blocks and reports when older blocks exceed that limit. Fullscreen apps
have no command blocks. Close and reopen the browser to refresh its snapshot.
The daemon keeps marks through an update handoff. Marks do not survive a
restart that starts a new shell.

### Grouping

<img src="media/drag.webp" alt="Dragging a tab into the web-app group while the other rows slide apart, then dragging the billing group above the loose tabs" width="800">

- **By folder:** right-click a tab and choose **Group tabs in `<folder>`**.
  Every ungrouped tab in that repo or folder joins one group, and new tabs you
  start inside that folder join it automatically.
- **By hand:** Ctrl+click or Shift+click to pick tabs, then right-click
  and choose **New group** or **Move to group**.
- **Ungroup** or **Remove from group** never close anything.

Tabs inside a group sit indented under its header; loose tabs stay flush.

For a Git repo group, **New worktree tab** in the group menu starts a tab in a
fresh worktree under `<repo>/.worktrees/`, so parallel agents on one repo do
not step on each other. The dialog picks what it checks out:

- **New branch**: a branch named after the tab, off a base you pick from
  the local and remote branches (the default branch unless you change it).
- **Branch**: an existing local branch.
- **Remote branch**: a new local branch of the same name that tracks it.
- **Pull request**: when origin is on GitHub, the pull request's head,
  fetched like `gh pr checkout` into the branch `pr-<number>`.

The first worktree in a repo adds `/.worktrees/` to `.git/info/exclude`, so
the worktrees never show in `git status`. Your `.gitignore` is left alone.

Deleting that tab removes the worktree it made. pitwall never deletes a
folder it did not create, nor the main checkout. When the worktree has
uncommitted or untracked files, the delete dialog lists them and its button
reads **Delete anyway**. Deleting the branch too warns when it has commits
the default branch lacks.

**Clean up worktrees** in the command palette runs `git worktree prune` in
every repo group and lists the worktrees under `.worktrees/` that no tab
uses, with their last commit and whether they have changes, to open in a
tab or delete. Their branches stay. The daemon prunes on start too, and
says when it finds such worktrees. `pitwall worktree` does the same from a
terminal (see [Command line](cli.md)).

### What a repository's git config can run

A repository's config can name programs that git runs, such as `core.fsmonitor`
or a filter in `.git/config`. An agent can write that file, and pitwall runs git
by itself: for branch stats, checkpoints and worktrees. So pitwall reads the
repository's config just before each git command it starts, and before the
daemon calls `gh`, and turns off what it finds. A program added in the moment
between the two can still run, so this reduces the risk and is not a sandbox
boundary. What it turns off:

- hooks, in `.git/hooks`, under `core.hooksPath` or from `hook.<name>.command`,
  your global ones too, so a worktree pitwall makes runs no `post-checkout`
  hook; use `setup` instead
- `core.fsmonitor` and automatic gc
- external diff and textconv drivers, pagers and editors
- filter and merge drivers, `core.sshCommand`, credential helpers, signing
  programs and upload-pack paths, when the repository's own config sets them,
  also in a file it includes under a condition that does not hold yet

A file the repository's config includes, under a condition or not, that git
cannot read or parse stops pitwall's git in that repository with an error naming
the file. Fix or remove it to go on.

The same keys from your global or system config still apply: a `filter.lfs`
that `git lfs install` wrote to `~/.gitconfig` works, even when the repository
sets its own value for it. pitwall does not look inside a submodule on status
and diff, and reports only its moved commit, so a dirty submodule does not count
as a change.

A filter set only in `.git/config`, as git-crypt and `git lfs install --local`
do, does not run for pitwall. Status and diffs read those files as they are on
disk, and a checkpoint stores a changed one's raw bytes in `.git/objects` under
`refs/pitwall/`. Stage, Discard, undo and redo from pitwall refuse in such a
repository, since they would write those raw bytes into your index or work
tree. Do those in a terminal, or set the filter in your global config.

The actions that open a pane are your own git. Open diff in pager never takes a
pager from the repository's config and turns fsmonitor off, but a diff driver or
filter in `.git/config` still runs in it, as it does for a `git diff` you type.
So do hooks on Create pull request, and what `git add -p` runs for Stage hunks in
terminal.

This does not contain an agent. One that runs without a sandbox or an approval
prompt can already run any program, as you.

### Tab search

Ctrl+Shift+K (Cmd+K in the mac preset) searches every tab of every
session, on every host the window shows, and the tabs you closed. Plain
Ctrl+K stays with the terminal, where shells and agent prompts delete to the
end of the line with it. Rebind it with `tab_search` under `[keys]`; the
command palette lists it as **Search tabs**.

Type part of a tab's name, branch, group, repo or folder, agent, pull request
(`#41`) or the first prompt of its agent or chat. Letters may be left out,
as in the palette, and a word of four letters or more may have one typo.
Open tabs come before closed ones, and the current session's tabs before
other sessions', best match first. With nothing typed, the list starts with
the tabs used most recently.

Each row shows the tab's agent and status glyph as the sidebar does, then,
for a tab of another session, that session and its host, then its group. Settled, snoozed and detached tabs say so on
the right, and a closed tab says when it closed. Enter or a click shows the
tab, switching session or host when it is elsewhere. The last row hands the
query to **Search all sessions**, which searches scrollback and transcripts
instead.

pitwall keeps the tabs that close: the last 200 of the last 30 days, in the
background service's saved state, so they survive a restart. Choosing one
opens it again as a new tab in its session, group and folder, and resumes
its agent in the session it had, as a restart does. A session or group that
ended with it comes back under its name. A tab that ran a command other than
an agent comes back as a shell in its folder, without running the command
again. When its folder is gone, as after Archive removed its worktree, the
row says so and Enter opens it in the repository instead. Delete forgets
the highlighted closed tab, and reopening one takes it off the list.

### Insert a path

Ctrl+Shift+I (Cmd+Shift+I in the mac preset) types file paths into the
focused pane without leaving the keyboard, for a prompt to an agent or a
shell command. It opens a picker in the command palette's card over the
files under the pane's folder: in a git repository, the tracked files still
in the folder and the untracked ones `.gitignore` does not hide; elsewhere, the files found
by walking the folder, skipping folders whose names start with a dot, up to
20,000 entries. A shell pane's folder follows `cd`; an agent's is where it
started. Rebind it with `insert_path` under `[keys]`; the command palette
lists it as **Insert a path**.

Type part of a path to filter, letters left out as in the palette; a match
in the file's name ranks above one only in its folder. Enter types the
highlighted path at the cursor, relative to the pane's folder, and closes
the picker. Tab or Ctrl+Space marks the highlighted file and moves down;
with files marked, Enter types all of them, separated by spaces, in the
order you marked them. A click types one file, or marks it while others are
marked. Shift+Enter opens the highlighted file with `[terminal] open_path`,
or the desktop opener when unset. Esc closes without typing.

A path with only letters, digits and `_ - . / + :` goes in as it is; any
other is single-quoted, so a space or `$` stays part of the path. A path
that starts with `-` goes in as `./-name`, so the command reads the file
instead of taking it as an option. When the
program asked for bracketed paste, as shells and agent prompts do, the
paths arrive as one paste. The picker shows the first 200 matches. It lists
this machine's files only, so a pane on a `--host` says so instead.

<img src="media/path-picker.webp" alt="The path picker shows two files and footer keys for inserting, marking and opening the highlighted path with Shift+Enter" width="800">

### Workers

The tabs an agent opened (its workers, and their workers in turn) list
under the agent's row, after its agent panes, one small row each: the
worker's agent logo, its tab's name and its state, with Allow and Deny on
one that asks for approval. The agent's row counts them, as in "2 agents ·
3 workers · 1 approval": its agent panes, its workers, and how many of them
all share the most urgent state.

The agent's row shows the most urgent state of it and its workers, so a
worker waiting on approval turns its pill and accent bar too. Allow and
Deny on the row answer the agent's own prompt; a worker answers on its own
small row.

Click the count to fold the small rows away and again to bring them back.
Folded, a worker that asks for an answer and the worker on screen keep
theirs. Each row remembers its fold, in the tree and the inbox alike and
across switches to other sessions and hosts, until the window closes. Next tab and previous tab visit a worker only while its
row is unfolded.

Click a worker's row to show its tab, Ctrl+click or Shift+click it to
select it with other tabs, and right-click it, or press the Menu key on it
with the sidebar focused, for the tab menu. A worker stays a tab
of its own everywhere else: its worktree and panes, `pitwall ls --tree`,
the switcher and goto_tab, which counts only the rows. Close the agent's
tab and its workers become rows of their own again.

#### Separate worker rows

Settings, Appearance, Sidebar, Workers, or `[sidebar] workers = "separate"`
in `config.toml`, gives each worker a row of its own instead. It applies at
once; `folded`, the small rows above, is the default.

In the tree, the agent's row grows a bar with one segment per worker in its
state's color and, under it, a count each of the workers that need you,
failed, work, have a pull request open, are done and stopped. A stopped
worker, grey in the bar, has no agent running in its tab: the agent exited
or never started. The workers start folded into the agent's row, all but
the one on screen. Click the bar, or press Right on the row with the
sidebar focused, to list them under it a step further in, and again, or
Left, to fold them. The agent's row shows its own state, and each worker
answers on its own row.

In the inbox each worker is a row like any other tab and sorts by its own
state. Settling or snoozing the agent's row still does the same to its
workers, with each worker's own snooze back on Undo, and a worker that asks
for an answer still wakes or unsettles it. Each worker row has its own
Settle and Snooze too. Next tab, previous tab, the switcher, goto_tab and
the sidebar's keyboard cursor visit the worker rows the sidebar shows.

### Programs

A long build, a test run or a deploy can tell pitwall what it is doing.
A program that writes OSC 7501, the Program Status Protocol, or a script
that runs `pitwall status working --app cargo --progress 40`, shows on its
tab's row: a line with the program's name, its message and a progress bar.
Where no agent owns the pane, the row's glyph and pill take the program's
state too, and the inbox puts a failed program in Waiting on you and a running one
in Working. An agent in the same pane keeps its own state; its build shows
on the line under it. See
[Programs and scripts](agent-report.md#programs-and-scripts) for the states
and when a report goes.

### Forms

<img src="media/form-dark.webp" alt="A form over a tab: a title, a line of text, a radio choice of environment, checkboxes of who to tell, a release note field and Deploy and Not now buttons; the tab's row in the sidebar reads Input" width="800">

`pitwall form` shows the user a form or a small dashboard in a panel over the
calling pane's tab. A script or an agent asks with it the way it would ask in
its own terminal, and the tab's row shows that it waits on you: the inbox
puts it in Waiting on you with the questions, and a snoozed tab wakes. The spec comes
from a file or stdin:

```sh
answer=$(pitwall form <<'EOF'
<form title="Deploy the release?">
  <text>Tests passed on 4 of 4 runners. Pick where it goes.</text>
  <choice id="env" label="Environment">
    <option>staging</option>
    <option selected>production</option>
  </choice>
  <choice id="notify" label="Tell" multi>
    <option selected>#releases</option>
    <option>on-call</option>
  </choice>
  <input id="note" label="Release note" lines="3" placeholder="Optional"/>
  <button>Deploy</button>
  <button cancel>Not now</button>
</form>
EOF
) || exit
echo "$answer"
# {"button":"Deploy","env":"production","note":"","notify":["#releases"]}
```

When you press a button the command prints the answers as one JSON object
and exits 0. `button` names the button you pressed (its `id`, else its
label), each `input` gives its text, a `choice` its option's `value` (else
its label), and a `multi` choice the list of them. Escape or a `cancel`
button exits 1 with nothing printed, and a spec that does not parse exits 2
with the line and the fix. Enter in a one-line field presses the first
button. A form with fields and no buttons gets Submit and Cancel.

The markup is a small XML subset in the spirit of tuios's TML. The root is
`<form title="...">`, and it holds, top to bottom:

| Element | Attributes | What it is |
| --- | --- | --- |
| `<text>` | | A paragraph, or several split by a blank line |
| `<input>` | `id`, `label`, `value`, `placeholder`, `lines` | A text field; `lines="3"` makes it multi-line |
| `<choice>` | `id`, `label`, `multi` | Radio buttons, or checkboxes with `multi`, one `<option>` each (`value`, `selected`) |
| `<button>` | `id`, `cancel` | A button; `cancel` makes it cancel |
| `<progress>` | `label`, `value`, `max` | A progress bar (`max` 100 unless set); its text replaces the percent |
| `<status>` | `label`, `state` | A status line with a dot: `idle`, `run`, `ok`, `warn` or `fail` |

A form without fields or buttons is a dashboard. It sits in the tab's top
right corner and leaves the keys to the panes, and it never marks the tab
as waiting. Give it an `--id`, run it in the background, and send it again
with `--update` as the work moves; `--close` takes it away. The `pitwall
form` that showed it exits when you close it with its x or the script
closes it.

```sh
dash() { # $1 tests done, $2 lint state
  cat <<EOF
<form title="Release 1.4">
  <progress label="Tests" value="$1" max="40">$1 of 40</progress>
  <status label="Lint" state="$2"/>
  <status label="Deploy" state="idle">waits for tests</status>
</form>
EOF
}
dash 0 run | pitwall form --id rel >/dev/null &
dash 12 ok | pitwall form --id rel --update
dash 40 ok | pitwall form --id rel --update
pitwall form --id rel --close
```

<img src="media/form-dashboard-light.webp" alt="A dashboard in the top right corner of a tab: progress bars for tests and build, and status lines for lint, integration, docs and deploy" width="600">

An update keeps what you typed or picked in fields whose `id` stays. A form
lives as long as the `pitwall form` that showed it: stop that and the form
goes. Close the tab and the form ends too, and its `pitwall form` exits 1.
Every window shows it over the tab; the first answer counts.

### Popups

A popup is a pane that floats over the tab's splits for one command, such
as a picker or lazygit, and closes when the command exits. The splits under
it keep their sizes and keep running.

```sh
pitwall popup -- lazygit
pitwall popup --width 60% --height 20 --title branches -- sh -c 'git branch | fzf'
```

It starts in the tab's folder, the live folder of its first pane, or in
`--dir`. `--width` and `--height` are a number of cells or a percentage of
the tab's area, 80% and 70% by default; a popup never grows past the area.
`--title` goes on its border, the command's name without one. Outside a pane,
name the tab as `pitwall split` does.

Every key goes to the popup's program, Escape too, so fzf and lazygit
behave as in a terminal. pitwall's own keys still work, and the close pane
key (Ctrl+Shift+W) closes the popup. Clicks on the tab outside it do
nothing. A tab has one popup at a time: a second `pitwall popup` on it
fails until the first closes. Another tab can have its own.

`--wait` blocks until the command exits and returns its exit code, or 130
when the popup closed first, so a script can ask with a picker. The command's
output stays on the popup's screen; write the answer to a file to keep it:

```sh
if pitwall popup --wait -- sh -c 'git branch --format="%(refname:short)" | fzf > /tmp/pick'; then
  git switch "$(cat /tmp/pick)"
fi
```

`[[popups]]` in config.toml puts a command on a key (see
[Configuration](config.md)); the key opens it over the shown tab. A daemon
restart drops popups, and a window older than Level 54 does not show them.

For a file tree in a popup, install yazi and add this to config.toml:

```toml
[[popups]]
name = "files"
key = "Ctrl+Alt+Y"
command = ["yazi"]
```

Ctrl+Alt+Y opens yazi in the tab's folder. Quit yazi to close the popup.

### Hints

Agents print commit hashes, file paths, URLs, pull request numbers and IP
addresses all day. Hints mode copies or opens one without the mouse, as
tmux-fingers and kitty's hints kitten do. Press Ctrl+Shift+G (Cmd+Shift+G in
mac), or run Hints from the command palette. Each match in the focused pane
gets a short label in the theme's accent color, the rest of the pane dims,
and a HINTS badge at the bottom right lists the keys.

| Keys                    | Action                                                   |
| ----------------------- | -------------------------------------------------------- |
| a label, such as `s`    | Copy the text and leave                                  |
| Shift and the label     | Open the text and leave; text that cannot open is copied |
| Backspace               | Take back the last letter typed                          |
| Esc, or the hints key   | Leave                                                    |

Ctrl+Alt+Shift+G (Cmd+Option+Shift+G in mac), Hints in all panes in the
palette, labels every pane the tab shows at once. The focused pane gets the
shortest labels, and the same text has the same label in every pane. Its key
pressed again leaves; the other hints key switches between one pane and all.

Hints mode labels:

- URLs: `https://`, `http://`, `file://` and `www.` links, which end where
  a Ctrl+click link ends.
- Paths: `/etc/hosts`, `~/notes.md`, `./run.sh`, `internal/ui/app.go`, and
  a file name with a line, `main.go:12` or `main.go:12:5`.
- Git hashes: 7 to 40 hex digits with at least one digit and one letter.
- `#123`: an issue or pull request number.
- IPv4 addresses, with a port: `10.0.0.12:8080`.
- Your own patterns from `[terminal] hint_patterns`; see
  [Configuration](config.md).

The labels use the home row first and leave out i, l and o, which read as
1, I and 0. No label starts another, so a label is done when typed. The
newest output, at the bottom, gets the shortest labels. While hints mode is
on, a label stays on its text as long as the text stays on screen, even as
other output arrives. A URL the
terminal wrapped onto the next row is one match; a line that only fills its
row is not joined to the next.

Shift and a label opens:

- a URL in your browser.
- a path with your desktop's opener (xdg-open, open on macOS), without its
  `:line`. A relative path is read from the pane's folder. A path that is not
  on this machine, or a window on a `--host`, copies instead.
- a `#number` or a hash on GitHub, as the tab's pull request or commit: the
  repository comes from the tab's pull request, else from its `origin`
  remote on github.com.
- anything else, such as an IP address, is copied.

To view a path in a popup, install a terminal viewer and set:

```toml
[terminal]
open_path = ["bat", "--paging=always"]
```

Shift and the path's label then runs that command in the pane's folder,
with the path as its last argument and without its `:line` or `:line:col`.
The daemon passes one absolute path without a shell, including spaces,
quotes and leading dashes. In a `--host` tab the popup runs on that host,
and `~/` means the host's home folder. The viewer must be installed there.
Path popups need daemon Level 60 or newer. Quit the viewer or use the
Close pane action to close the popup; its current key appears on the border.
Unset `open_path`, or set it to `[]`, to keep the desktop opener. Web links
still open in the browser.

A local hint that names no regular file copies its text instead. A daemon
refusal or a viewer that fails or closes immediately shows a notice.
Windows path popups refuse viewers that resolve to `.bat` or `.cmd` files
because cmd.exe can read file names as commands. Use an executable viewer.

The path picker, Ctrl+Shift+I, also opens its highlighted file with
Shift+Enter. Enter still inserts the highlighted or marked paths.

<img src="media/file-popup-dark.webp" alt="A less popup displays a file whose name contains spaces, with the close key on its border" width="800">

<img src="media/file-popup-light.webp" alt="A less popup displays a file with a leading dash in the light theme" width="800">

Hints mode reads the screen the pane shows, so scroll back first to label
older output.

### The inbox

The sidebar has a second view for when the question is "what needs me?"
rather than "where is it?". The inbox button left of + in the sidebar's
header switches to it and back. Settings, Appearance, Sidebar and
`[sidebar] view` in `config.toml` hold the same choice; the tree stays the
default.

The inbox lists every agent tab of the session in one list, groups
included, each row with its group's icon and name. A worker has no row of
its own (unless [separate](#separate-worker-rows)): it rides under its
agent's row as in the tree, and the agent's row
sorts by the most urgent of the two. Settling or snoozing that row does the
same to its workers, Undo puts back each worker's own snooze, and a worker
that asks for an answer wakes or unsettles it. An agent tab settles for
idle days only once its workers have been quiet that long too. It sorts the rows into sections:

- **Pinned**: the tabs you pinned, in their order.
- **Waiting on you**: the rest. Approvals, questions and failures come first, then
  finished turns you have not looked at, then the others.
- **Working**: tabs whose agent works now. pitwall fills it; you cannot drop
  a tab there. A pinned or settled tab stays where you put it while its
  agent works. `working_shelf = false` keeps these tabs in Waiting on you.
- **Snoozed**: tabs hidden until a time you pick, which the row shows. An
  approval, a question, a plan or a failure wakes the tab early.
- **Settled**: done tabs, still open. A tab settles when you settle it,
  after `settle_after_days` days (3) without agent activity, or when its pull
  request merges (`settle_on_merge`). Pinned tabs never settle on their own.
  A new turn or a request from its agent brings it back.
- **Shells**: tabs without an agent sit at the end, so Alt navigation and the
  switcher still reach them, without crowding the agents.

Queued tasks show below the sections.

A row shows the same status the tree does, in this order: approval, input,
working, failed or usage limit, ready. Working rows and ready rows you have
seen dim; approvals and questions never do.

Drag a row into Pinned to pin it, into Settled to settle it, or into
Waiting on you to unpin, unsettle or wake it. Its menu has the same actions, with Snooze
offering in an hour, this evening, tomorrow morning and Monday. Settling
takes one click too: hover an agent's row and a double check shows left of
"…" and "×". On a settled row an undo arrow in the same place unsettles it.
A row showing answer buttons or a plan's buttons leaves it out. A notice
with Undo follows each move for a few seconds, and Undo puts a pinned or
unpinned tab back in its old place. Snooze and settle live in the
daemon, so they survive a restart and every window sees them.

Clicking a row shows its tab, as in the tree, and next tab, previous tab,
goto_tab and the switcher follow the inbox's order while it shows.

### Replying from the sidebar

An agent that asks a question shows Reply beside its pill, in the inbox and
in the tree, so you can answer without opening its tab. Reply… in any
agent row's menu does the same for an agent that is working or done; the
reply then waits until the agent stops for input.

Reply opens a small editor under the row, with the agent's question above
the field:

- Enter sends. Shift+Enter starts a new line.
- Escape closes the editor and keeps what you wrote for that tab. Reply on
  the same tab brings it back. A click outside the editor does the same.
- Up on the first line brings back your earlier replies, newest first, and
  Down goes forward again. pitwall keeps the last 50 while the window is
  open.
- A paste of more than three lines or 800 characters shows as one block,
  such as `[Pasted text #1 · 42 lines]`, and goes out whole. Backspace right
  after a block removes all of it.
- Ctrl+V with an image and no text on the clipboard attaches the image as
  `[Image #1]`. pitwall reads it the way it pastes an image into a pane:
  the daemon saves it and the agent gets the file's path.

pitwall types the reply into the agent the way Request changes and the
review view's comments go in: once the agent waits for input, it pastes
each image's path, then the text as one bracketed paste, then presses
Enter. While the screen shows a form the text waits, as Enter would answer
the form, and a form that comes up after the paste does not get the Enter, so
the text stays in the agent's input. A notice under the panes says whether it
went now or waits. If you typed in that pane, it says the reply goes once the
input box is empty.

### Your draft comes first

An agent at rest is also where you write your next prompt, so nothing pitwall
types lands in text you left in the input box. That covers `pitwall send`,
second opinions, review comments, sent CI failures, Reply and the line after a
restart. They wait while you typed in the pane in the last 3 seconds, and
while Claude Code's or Codex's input box holds your text, even with the pane
scrolled up. The row shows "Waits: your draft" where the time goes. Send or
clear the draft and the message goes in by itself 3 seconds after your last
key, in queue order. Your own reply or comments from the window pass a message
that cannot go yet, so that they can answer a question it waits on.

`pitwall prompt` and a reply from the phone are refused instead, with the
reason. `continue` after a usage limit or a quiet stretch is not typed. A
placeholder or a suggested prompt does not count, as both agents draw it faint.

Text counts as your draft only if you typed in the pane since you last pressed
Return on a prompt, so a pane nobody typed in never waits. Other agents, and a
screen that shows no input box, get the 3 second pause only.

### Worktree ports and setup

Each worktree tab pitwall makes gets its own block of 10 ports, so two
agents can each run a dev server without one crashing on a taken port. The
first gets 3010-3019, the next 3020-3029, and the main checkout keeps 3000.
Every pane in the tab has:

| Variable            | Value       |
| ------------------- | ----------- |
| `PORT`              | `3010`      |
| `PITWALL_PORT_BASE` | `3010`      |
| `PITWALL_PORTS`     | `3010-3019` |

Tabs outside a pitwall worktree, the main checkout's included, get none of
them. The block is saved with the tab, so it survives a restart, and no
other tab gets it while this one exists. The tab's hover card shows the
block, and the agent panel's Changes view shows `PORT 3010`. When a shell
or command in the tab prints `EADDRINUSE` or "address already in use", the
tab asks for you with "Port in use: this worktree's ports are 3010-3019".

Every 3 seconds the daemon checks which ports of each block accept a
connection on localhost. When one does, the tab's row shows a chip with the
first such port, `:3010`, before the PR chip. Click it to open
`http://localhost:3010` in your browser. The hover card and the Changes
view list every open port of the block. With `pitwall --host` the daemon
checks the host's ports, so the hover card and Changes view still list
them, but the row shows no chip: localhost on your machine is not the
host.

Next.js, Create React App, Rails with Puma's default config and any app that
reads `process.env.PORT` pick up `PORT` on their own. Others need it passed:

- Vite: `server: { port: Number(process.env.PORT) || 5173, strictPort: true }`
  in `vite.config.ts`, or `vite --port $PORT --strictPort`. Without
  `strictPort`, Vite quietly moves to the next port, which may be another
  tab's.
- Astro: `astro dev --port $PORT`.
- Storybook or a second service in the same tab: count up from the base,
  `storybook dev -p $((PITWALL_PORT_BASE + 1))`, up to the end of
  `PITWALL_PORTS`.

`port_base` and `port_step` under `[worktrees]` in `config.toml` move the
blocks and size them; `port_step = 0` turns ports off.

A repo can also bring files into each new worktree and run a command there,
from `.pitwall/worktree.toml` at its root, or `[worktrees]` in `config.toml`
for every repo. Nothing is copied, linked or run without one.

```toml
# .pitwall/worktree.toml
copy = [".env", ".env.local"] # copied from the main checkout
link = ["node_modules"]       # symlinked to the main checkout's
setup = "pnpm install"        # typed into the new tab's shell
```

- `copy` takes files, not folders, and keeps their permissions. `.env`
  holds secrets, so pitwall copies it only when you list it.
- `link` saves an install, but a shared `node_modules` breaks when branches
  need different dependencies, and an install in one worktree changes it for
  all of them. A `.gitignore` line `node_modules/` matches only a folder, not
  the link: write `node_modules`.
- A path the worktree already has, or the main checkout lacks, is skipped.
  Nothing is overwritten, and paths must stay inside the repo.
- `setup` is typed into the first pane of the new tab after `copy` and
  `link`, so you watch it run and keep the shell afterwards. A `setup` from
  your own `config.toml` runs at once. One from the repo's
  `.pitwall/worktree.toml` is typed without Enter: anyone can put a command
  in a repo you clone, so it waits on the prompt until you read it and
  press Enter. One with a line break or another control character is not
  typed at all.

Keys in the repo's file win over `config.toml`. A mistake in it shows as an
error when you make the tab, and the tab is made anyway.

### Tasks and the queue

<img src="media/new-task.webp" alt="The New task dialog: project, where it runs, agent, mode, model and prompt, with Queue and Start buttons" width="480">

**New task** (Ctrl+Shift+A, the command palette, or **New task…** in a
group's menu) starts an agent on a prompt in a tab of its own, as
`pitwall new -- codex "…"` does. Pick:

- **Project**: a folder of this session's groups and tabs, the open tab's by
  default. A worktree tab counts as its main checkout.
- **Where**: the folder itself, a new worktree on a new branch (named from
  the prompt, made off the default branch unless you name another), or a
  new worktree on an existing branch. Worktrees get their ports and
  `.pitwall/worktree.toml` setup as **New worktree tab** does; setup runs
  in a shell under the agent.
- **Agent**: the agent CLIs on your PATH, then your
  [presets](#agent-presets). The dialog remembers the last one.
  Ctrl+click picks more agents, to compare their work on one prompt: each
  gets a task of its own in a new worktree, on the branch you name with the
  agent's name added, such as `issue-12-login-claude` and
  `issue-12-login-codex`. Their tabs open next to each other.
- **Mode**: for Claude Code, Accept edits or Plan (`--permission-mode`);
  for Codex, Read only or Auto (`-s` and `-a`). With several agents, each
  has its own row.
- **Model**: pick a suggestion or type a full model ID. Claude offers `opus`
  and `sonnet`; Codex offers the visible models in `~/.codex/models_cache.json`
  when present. Without a cache, type a model ID. `default` or an empty field
  passes no model flag. Each selected agent has its own field, remembered per
  agent after Start or Queue. A preset starts with its configured model; the
  field overrides it for this task. Remote windows use free text for Codex.
- **From issue**: in a GitHub repository, with `gh` installed and logged
  in, the newest 50 open issues. Type a number or words of the title and
  pick one. It fills the prompt with the issue's number, title, URL and
  body, and names the new worktree's branch `issue-<n>-<title>`. Both stay
  editable.
- **Prompt**: passed as the agent's first argument. Ctrl+Enter starts it.

**Queue** puts the task, or one per agent, in the session's queue instead, shown under its
group in the sidebar (or at the bottom), with start-now and remove on hover.
A queued task starts when a running agent of its session finishes: done,
failed or closed. With `max_running` under `[agents]` in `config.toml` (or
**Agents at once** in Settings), queued tasks start whenever fewer than that
many agents run in the session. The queue survives a daemon restart.

Choose **Start at** beside Queue to enter a local `HH:MM` time and an optional
repeat interval, then press **Queue**. The CLI accepts `--at 02:00`,
`--every 24h`, or both. A time that has passed means tomorrow; a repeat
without `--at` first runs one interval from now. Repeats use elapsed durations,
so `24h` may shift the local hour across daylight saving changes.

The sidebar and `queue ls` show the next start. The daemon checks every second;
due tasks still wait for queue capacity and respect `max_running`. A future
task does not hold up ready tasks behind it. After sleep or a restart, a missed
time runs once when capacity allows. A repeat then advances to its next future
slot, skipping missed runs. Repeats run in **This folder** or an existing
folder passed to the CLI; they cannot create a worktree on each run. Remove
its queue entry to stop future repeats.
**Start** and the sidebar's start-now button run immediately; start-now also
removes the repeat. Scheduling needs a daemon that supports it.

```sh
pitwall new --model sonnet -- claude "fix the flaky login test"
pitwall queue add --model gpt-custom -- codex "fix the flaky login test"   # in the current folder
pitwall queue add --issue 12 -- claude                  # GitHub issue 12 as the prompt
pitwall queue add --preset "Opus high" -- "fix it"       # a preset, "fix it" its prompt
pitwall queue add --at 02:00 --every 24h -- codex "run nightly checks"
pitwall queue ls
pitwall queue rm 1
```

`pitwall new` and `pitwall queue add` accept `--model ID` before `--`.
Claude receives `--model ID`, Codex receives `-m ID`. With `--preset`, this
replaces its model for this task. `--model default` clears an explicit model;
omitting the option keeps the preset or CLI default. Other agents receive
`--model ID` and must support that option.

### Agent presets

A preset is an agent with its model, effort, arguments and environment
under a name. Add, edit and delete them in **Settings, Agents**: pick the
agent, type the model, pick the effort and add any extra arguments. Each
preset is an `[[agents.preset]]` table in `config.toml`, and the model and
effort go into `args` the way each agent's CLI takes them:

```toml
[[agents.preset]]
name = "Opus high"
agent = "claude"
args = ["--model", "opus", "--effort", "high"]

[[agents.preset]]
name = "Codex worker"
agent = "codex"
args = ["-m", "gpt-5", "-c", "model_reasoning_effort=high"]
env = ["CODEX_HOME=/home/me/.codex-work"]
guard = true
```

pi takes the effort after the model (`["--model", "anthropic/claude-opus-5-5:high"]`),
and OpenCode as a variant (`["-m", "anthropic/claude-opus-5-5#high"]`).

Settings offers the efforts each agent takes: `low` to `max` for Claude
Code, `none` to `max` for Codex and OpenCode, `low` to `xhigh` for pi. For
other agents it edits the name, extra arguments and guard only. Extra
arguments split as a shell would, so quote a value with spaces. A
hand-written preset whose `args` don't read as a model and effort keeps
them in its extra arguments. `env` stays in `config.toml`: Settings shows
the variables' names and keeps them when you save.

The New task dialog lists presets after the plain agents, with the same
modes as their agent. `--preset NAME` starts one from `pitwall new`,
`pitwall worktree new` and `pitwall queue add`. Arguments after `--` follow
the preset's own, so `pitwall new --preset "Opus high" -- "fix the flaky
test"` passes the prompt. `guard = true` turns on the [guard](#the-guard).
Renaming a preset in Settings renames it in the fleet lanes that use it.
Deleting one turns those lanes off, and the page says which.

A preset's tab keeps its environment after a daemon restart, and a resumed
agent keeps the arguments its CLI documents as session settings: Claude
Code's `--model` and `--effort`, Codex's `-m` and `-c`, pi's `--model`.
OpenCode drops `-m` on resume, but OpenCode picks the model and variant of
the session's last message when it opens one, so the preset's model stays.

### The guard

The guard stops an agent from running the commands that are yours to run:
`git push`, `gh pr merge` and `gh pr create`, `terraform` and `tofu`
`apply` and `destroy`, `kubectl apply` and `delete`, and `helm install`,
`upgrade` and `uninstall`. Turn it on for one tab with `--guard`:

```sh
pitwall new --guard -- claude "fix the flaky test"
pitwall worktree new login-fix --guard -- codex "fix the login redirect"
```

`guard = true` in a preset does the same. With `guard_workers = true` under
`[agents]` (or **Guard workers** in Settings), every tab an agent opens
with `pitwall worktree new` gets the guard, so an orchestrator's workers
can't push or merge. It is off by default.

When the agent asks to run a denied command, the hook answers with a deny
and a reason, `` pitwall guard: `git push origin fix` is for the human; stop
and ask. `` The agent reads the reason and the call never runs. Every other
call goes through as before. Add your own patterns under `[agents]`:

```toml
[agents]
guard_deny = ["npm publish *", "docker push *"]
```

`*` matches any run of characters and `?` one; a trailing ` *` also matches
the bare command. These patterns add to the list. Nothing in `config.toml`
can lift a built-in one, and a config with problems still leaves the
built-in list on.

The guard covers Claude Code and Codex through their `PreToolUse` hook, and
pi, omp and OpenCode 1 through pitwall's extension and plugin, which run
`pitwall guard` before each shell command. Run `pitwall hooks install`
after an upgrade so the extension and plugin have the check. OpenCode 2
runs plugins in one background service that every OpenCode window shares,
so pitwall can't tell a guarded tab's calls apart there and does not guard
OpenCode 2. Gemini CLI, Cursor and the other agents have no guard either.

The guard is a seatbelt, not a sandbox. It reads the command the agent
asks to run, split on `&&`, `||`, `;`, `|`, `&` and newlines, with leading
`VAR=value` dropped. A command that hides the push gets through:
`echo $(git push)`, `eval "git push"`, `sh -c 'git push'` typed as one
string, a script that pushes, or an alias. A deny is not an answer to a
permission prompt: the guard never allows anything, and pitwall still
never answers a prompt for you. Use it to catch an agent that forgot its
instructions, and keep credentials an agent must not use out of its
environment.

### Fleet mode

Fleet mode lets an orchestrator agent start workers by a task's size, and
keeps an eye on them while they run. Turn it on in **Settings, Fleet**, or
under `[fleet]` in `config.toml`. It is off by default.

```toml
[fleet]
enabled = true
contract = "~/fleet/contract.md"   # goes before every --lane brief
escalate = "parent"                # or "respawn", "off"

[fleet.small]                      # one clear pull request, mechanical work included
preset = "Quick"
timebox = 30

[fleet.normal]                     # normal work
preset = "Standard"
timebox = 60

[fleet.big]                        # whole features, investigations, risky changes
preset = "Deep"
timebox = 120
```

Each lane names one of your [presets](#agent-presets), and the preset
picks the model: a fast, cheap one for small tasks, your strongest for big
ones. Lanes ship empty; a lane without a preset is off. **Settings, Fleet**
shows what each lane runs, such as "Codex, gpt-x, high", and its **New
preset** button opens the preset editor. `pitwall worktree lanes` lists
them, and
the pitwall skill tells an orchestrator agent to route by size, one worker
per task:

```sh
pitwall worktree new login-fix --lane small -- "Fix the login redirect. FEAT-123 has the steps."
```

The words after `--` are the brief. pitwall starts the lane's preset with
the brief as its prompt, and puts the contents of the `contract` file
before it. A contract holds what every worker should do, for example:

```markdown
Work only in this worktree. Commit on its branch with a message that says
what changed. Run the tests before you push. Open a pull request when the
tests pass, and stop there: the orchestrator reviews and merges.
```

A worker on a lane that runs past the lane's `timebox` (minutes) with no
pull request open gets one nudge, typed at its prompt as `pitwall send`
does. Set its words with `nudge`. Past twice the timebox, still with no
pull request, the tab that started it gets a line from pitwall. Each
happens once per worker, and a daemon restart keeps track of what was
sent.

With `send_failures_on_fail` or its older name `auto_send_failures` under
`[git]` on and fleet escalation enabled, pitwall sends a lane worker's
failed checks to its agent at most twice. When a lane worker's checks
fail again after that, `escalate` decides what happens: `parent` (the
default) tells the tab that started it, `respawn` closes the worker's
agent and starts the `big` lane in the same worktree, with the branch's
diff stat, the failed checks and the old agent's last screen as its brief,
and `off` does nothing. Without a `big` preset, `respawn` tells the parent
instead.

A finished turn reads **PR open** in place of Done while its branch has an
open pull request, fleet mode or not. `pitwall wait --until done` and
`wait --children --until done` keep waiting until the pull request merges
or closes; `--until idle` returns at once.

### Resume after a usage limit

When Claude Code or Codex stops because your plan's usage limit is hit,
pitwall reads the reset time from the message the agent printed ("resets
3pm" or "try again at 3:04 PM"). When the message names no time, it takes
the reset of the fullest limit window it tracks for that agent. A minute
after the reset, pitwall types `continue` and Enter into the pane, and a
desktop notification says so. Until then the tab's pill reads
**Resumes 15:04**.

Claude Code also has **Continue automatically at usage limit** in `/config`.
Its [`autoContinueAtUsageLimit` setting](https://code.claude.com/docs/en/settings-reference#autocontinueatusagelimit)
defaults to on. pitwall reads it from `~/.claude/settings.json`, or
`$CLAUDE_CONFIG_DIR/settings.json`, including a tab's environment override.
Unless that file explicitly sets it to `false`, pitwall schedules no Claude
resume and the hover card says **Claude will continue on its own**. This also
applies when the file is missing, unreadable or invalid. pitwall never changes
Claude's settings. Codex resumes are unchanged.

<img src="media/claude-auto-continue.png" alt="A Claude tab's hover card says Claude will continue on its own after a usage limit" width="320">

This is a user-settings check, not a guarantee that Claude has armed its wait.
Older Claude versions, managed settings, `--settings`, project settings and
Claude's own eligibility rules can change its behavior. If you use those
overrides, keep only one automatic continue enabled. To have pitwall handle
Claude resumes, turn Claude's option off in `/config` and check any managed
or command-line overrides too.

pitwall types it only when the pane still runs the same agent and still
sits at the turn the limit stopped. Typing anything in the pane after the
turn stopped cancels it, and so do **Cancel resume** in the tab's
right-click menu and turning the setting off. The schedule
lives in the daemon's memory only, so a daemon restart drops
every pending resume.

Turn it off with `resume_after_limit = false` under `[agents]` in
`config.toml`, or **Resume after limit** in Settings.

### Detaching

<img src="media/survive.webp" alt="The window closes, pitwall ls shows every tab still running, the window comes back, and after a reboot the agents resume" width="800">

**Detach** (right-click a tab, or `pitwall detach`) hides a tab and keeps
everything in it running. The **Detached** list in the sidebar footer brings
it back, as does `pitwall attach <name>`.

### Let agents name their tab

Claude Code and Codex set a terminal title, which pitwall shows as the tab's
title with spinners stripped. pi's title is `π - <folder>`, which says
nothing about the work, so a pi tab shows its first prompt instead, or the
session name once you set one with `/name`. An agent can also name its tab explicitly:

```sh
pitwall tab rename "fix login redirects"
```

To have agents do this on their own, add to your `CLAUDE.md` or `AGENTS.md`:

```markdown
At the start of a task inside a pitwall pane, run
`pitwall tab rename "<short description of the task>"`.
```
