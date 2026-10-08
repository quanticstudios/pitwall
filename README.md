# pitwall
<img src="packaging/pitwall.svg" alt="pitwall logo: a P whose stem is three status lights and whose bowl is a terminal pane" width="96" height="96">

<img src="docs/media/pitwall-hero.webp" alt="The pitwall window: a sidebar of Claude Code and Codex tabs with live states, a pane that rings green when its agent finishes, and the jump key switching to the tab whose agent asks for approval" width="800">

pitwall is a terminal multiplexer for running coding agents side by side. It
opens straight into a shell like tmux, but it is a native window: a sidebar
lists every tab and what it is doing right now, whether that is a command
running in a terminal or a Claude Code, Codex or pi agent working, waiting for
your answer, asking for approval, done, or failed. Terminals are drawn with
real fonts and pixels, not character cells.

A background daemon owns every terminal. Closing the window leaves tabs
running, and after a reboot they come back in the same folders with agents
resumed where they left off.

**pitwall is in alpha.** It is used every day on Linux, but expect rough
edges, and config or saved state may change between releases. It is
developed on Linux (Hyprland) and works on any Wayland or X11 desktop.
macOS and Windows builds are new; see the notes below. Releases are tagged
`v0.1.0-alpha.N`.

## Install

With Homebrew, on macOS or Linux:

```sh
brew install quanticstudios/tap/pitwall
```

With Scoop, on Windows:

```powershell
scoop bucket add quanticstudios https://github.com/quanticstudios/scoop-bucket
scoop install pitwall
```

From the AUR, on Arch Linux, with an AUR helper such as yay or paru:

```sh
yay -S pitwall-bin
```

Or with the install script, on Linux or macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.sh | sh
```

On Windows, in PowerShell:

```powershell
irm https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.ps1 | iex
```

The script downloads the latest release for your system, checks it against
the release's `checksums.txt`, and installs `pitwall` in `~/.local/bin`. On
macOS it also installs `pitwall.app` in `~/Applications`, so pitwall opens
from the Dock, Launchpad and Spotlight. On Windows it installs `pitwall.exe`
in `%LOCALAPPDATA%\pitwall\bin` and adds that folder to your user PATH. To
pin a release, set `PITWALL_VERSION=v0.1.0-alpha.1`; to install somewhere
else, set `PITWALL_INSTALL_DIR`. In PowerShell, set them first with
`$env:PITWALL_VERSION = 'v0.1.0-alpha.1'`.

| Platform                       | Release builds | Status                     |
| ------------------------------ | -------------- | -------------------------- |
| Linux (glibc 2.35 or newer)    | x86_64, arm64  | alpha, used every day      |
| macOS                          | arm64, x86_64  | alpha, new and not yet run |
| Windows 10 1809 or newer, 11   | x86_64, arm64  | alpha, new and not yet run |

The macOS and Windows builds compile and pass the platform-independent tests
in CI, but nobody has used them yet. Expect rough edges, and please report
what breaks.

Known gaps on macOS:

- Release binaries are not signed. Homebrew sets no quarantine flag and
  `get.sh` clears it; for an archive you downloaded yourself, run
  `xattr -dr com.apple.quarantine pitwall pitwall.app`.
- The app bundle comes from `get.sh`, or from the release archive: copy its
  `pitwall.app` to `~/Applications`. Homebrew installs only the `pitwall`
  command. Started from the Dock, pitwall reads PATH from your login shell
  (`$SHELL -ilc`) once, so it finds `claude` and `codex` as a terminal
  would, and opens its first session in your home folder.
- Notifications come through `osascript`, so macOS shows them under Script
  Editor.

Known gaps on Windows:

- Agent status comes from hooks only. Windows has no foreground process group
  to read, so an agent started without hooks, or a command running in a
  shell, shows nothing in the sidebar.
- A tab's folder does not follow `cd`. It stays the folder the tab opened in.
- There is no Start menu entry. Run `pitwall` from a terminal; started from
  Explorer, a console window flashes before the window opens.
- Claude Code runs hook commands through Git Bash. Other shells get a path
  with forward slashes, quoted only when it contains spaces.
- When an upgrade replaces a running daemon, the old daemon is stopped without
  a final save. It saves within moments of every change, so little is lost.

The AUR package installs a desktop entry and icon. The Linux release archive
holds them as `pitwall.desktop` and `pitwall.svg` next to the binary, and
`scripts/install.sh` installs them when you build from source as below.

### Updates

On Linux and macOS, a release build checks GitHub for a newer release when a
window opens, every hour after, and when the window regains focus half an hour
or more after the last check. When there is one, an Update button
shows at the bottom of the sidebar. It downloads the release's archive for
your system, checks it against `checksums.txt`, and replaces the `pitwall`
binary the window runs; a symlink to it keeps pointing at the new one. When
the download or the checksum fails, nothing is installed, the button reads
"Update failed", and a click tries again. After an install, "Restart to
finish" opens a new window on the same tab and closes this one.

The update leaves the daemon running, so tabs keep their processes. Most
releases work with the daemon already running; when one cannot, the new
window asks before restarting it, as after any upgrade (see
[Upgrades](#upgrades)).

A build from source (`git describe` past a tag, or `-dirty`) never checks.
Windows has no button; run the install command again to update. To stop the
check, set `check = false` under `[updates]` in config.toml or use the switch
under About in the settings page.

Update a Homebrew or AUR install with its package manager, and stop
the check there. The button cannot write over an AUR package's
`/usr/bin/pitwall`, and on Homebrew it would replace the binary behind brew's
back.

### Build from source

You need Git, a C compiler, pkg-config, and [mise](https://mise.jdx.dev). Gio,
the UI toolkit, needs the development headers for EGL, Wayland, X11,
xkbcommon, Xcursor and Xfixes (`egl`, `wayland-egl`, `wayland-client`,
`wayland-cursor`, `x11`, `x11-xcb`, `xkbcommon`, `xkbcommon-x11`, `xcursor`,
`xfixes` in pkg-config).

```sh
git clone https://github.com/quanticstudios/pitwall.git
cd pitwall
mise install go
./scripts/install.sh
```

The installer builds `~/.local/bin/pitwall` and adds a desktop entry and icon
under `~/.local/share`, so pitwall shows up in your app launcher. Set `PREFIX`
to install elsewhere. It never edits Claude Code or Codex configuration.

Check the install:

```sh
pitwall --version
```

## Quick start

```sh
pitwall hooks install --dry-run   # see what would change
pitwall hooks install             # let your agents report their state
pitwall                           # open the window
```

The window opens on a shell in the folder you launched it from, in a session
with a generated name such as `swift-otter`. Run `claude`, `codex`, `pi`, a dev
server, anything. The tab's row in the sidebar shows what is happening: the
name of a running command, or the agent's state.

Open more tabs with **+** in the sidebar header. Each tab can be split into
panes. Typing `exit` closes a pane; an empty tab closes, and the session ends
with its last tab. Closing the window only detaches it: every session keeps
running, and `pitwall` opens the most recently used one again.

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

## Everyday use

### Watching agents

Each sidebar row shows a tab's state:

| State    | Meaning                                           |
| -------- | ------------------------------------------------- |
| Working  | The agent is in a turn                            |
| Input    | The agent asked you a question                    |
| Approval | The agent wants permission to run a tool          |
| Plan     | The agent finished a plan and waits for approval  |
| Done     | The agent finished its turn                       |
| Error    | The turn failed                                   |
| `go`     | A terminal is running that command                |

A tab running Claude, Codex or pi always shows it, idle or busy: the agent's logo
replaces the row icon. The logo goes when the agent exits back to the shell.

When an agent needs you (a question, an approval, a plan, an error, a
finished turn) in a pane you are not looking at, that pane gets a ring in the
state's color and its sidebar row gets an accent bar and a dot. Not looking
means another tab, another pane of the same tab, or the window in the
background. The mark stays until you focus that pane, and a group header
counts its tabs that carry one. pitwall also sends a desktop notification
for it. Ctrl+Shift+U (Alt+U in the aide preset) jumps to the pane that most
recently started waiting; press it again for the next one. Once you have
seen them all, it walks the waiting panes by priority.

<img src="docs/media/attention.webp" alt="A Codex pane finishes in the background and rings green, the billing tab asks for approval, and Ctrl+Shift+U jumps to each in turn" width="800">

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

<img src="docs/media/notify.webp" alt="A test pane runs npm test and pitwall notify, then rings amber and sends a desktop notification saying tests passed" width="800">

States are exact when the agent's hooks are installed (`pitwall hooks
install`). Without hooks, pitwall still recognizes `claude`, `codex` and
`pi` running in a pane and reads their state from the screen, which is a
little less precise. pi has no permission prompts of its own, so a pi tab
shows Working, Done, Error or nothing, never Input, Approval or Plan; a
dialog an extension opens with `ctx.ui.confirm` is not reported.

Gemini CLI and OpenCode report through hooks too. Gemini CLI fires no hook
when a turn fails, so it never shows Error; a failed turn stays Working
until the next prompt. OpenCode has no plan to approve, so it never shows
Plan. Without their hooks, they get a state only from a decision model
reading their screen (`[decisions.agents]`, below).

When an agent runs in a pane without reporting, the pane shows "Install
hooks for live status" with an Install button: after the agent has run for
20 seconds without a hook, or as soon as its screen shows a turn (Codex
sends nothing until its first prompt). Install lists what would change in
each config, then installs on confirm. Settings > Agents has the same
button. Agents already running load their hooks only when restarted.
pitwall knows Gemini CLI and OpenCode by their process name, so one
installed through npm, which runs as `node`, gets no notice.

### Sessions

<img src="docs/media/sessions.webp" alt="The session switcher: three sessions with their agents and live counts on the left, the highlighted session's sidebar on the right" width="800">

A session is a separate set of tabs and groups with a name, the way tmux
and zellij work: one per project or per piece of work, each with its own
sidebar. All of them keep running in the daemon. The sidebar header shows
the session's name, the window title is `<session> · pitwall`, and the panes
fade in when the window switches, so you know where you are.

Ctrl+Shift+S (Alt+S in the aide preset), or a click on the session name in
the sidebar header, opens the session switcher. It lists every session with
its agents, how many are working and how many need you, and when it was last
active; a session with something you have not seen gets an accent bar. The
right side draws the highlighted session's sidebar as it is now, so you can
watch its agents before you switch. In the switcher:

| Key            | Does                                                   |
| -------------- | ------------------------------------------------------ |
| j / k, arrows  | Move                                                   |
| Enter, click   | Switch the window to the session                       |
| 1-9            | Switch to the Nth session                              |
| any other key  | Filter by name (`/` starts a filter that may begin with j, k, n, r or x) |
| n              | New session; type a name or keep the suggested one     |
| r              | Rename the highlighted session                         |
| x              | Kill the highlighted session, after a y                |
| Esc            | Clear the filter, then close                           |

Ctrl+Shift+] and Ctrl+Shift+[ (Alt+] and Alt+[ in aide) step through the
sessions without the switcher, and Ctrl+Shift+N makes one. Ctrl+Shift+U
crosses sessions: it switches to the session of the pane that needs you.
Desktop notifications start with the session's name.

Each window shows one session, and you can open as many windows as you like.
Running `pitwall` again opens another window: on the most recent session no
window shows, else on a new session in the folder you ran it from.
`pitwall -s <name>` opens one on that session, making it if it does not
exist. Asking for a session another window already shows raises that window.
When a session ends, because you killed it or closed its last tab, its
windows move to the most recently used session, or close when none is left.

### Tabs and panes

<img src="docs/media/panemode.webp" alt="Pane mode: split down, move focus right, toggle fullscreen and leave with Esc, with the PANE hint showing the keys" width="800">

The keyboard shortcuts are in [Keybindings](#keybindings). With the mouse:
click a tab to switch to it, double-click it to rename it, middle-click to
close it, right-click it for **New tab below**, **Rename tab**, **Detach
tab**, **Close tab**, grouping, and more. The "+" in the sidebar header or on
a group header opens a new tab right below the one you were in, in the same
group and folder.

Rest the pointer on a tab for half a second and a card opens beside the
sidebar with what the row cuts off: the full title, the group and folder, the
branch with its diff stats, the agent's state and question, and the pane
count. Move to another tab and the card follows at once.

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

To review an agent's work, click a file under Changes: its diff opens in a
new pane beside the tab's panes, from the merge base with the default branch
to what is on disk now, committed or not. The tab's "…" menu and the command
palette have View diff, the same for the whole branch, and Create pull
request. The diff runs `git diff` in your pager: `$GIT_PAGER`, else
`core.pager`, else delta when it is installed, else `less -R`. Press q to
close it. Create pull request opens a tab in the tab's folder that runs
`git push -u origin HEAD` when the branch has no upstream, then
`gh pr create --fill`, so you see both commands and answer gh's questions
there; the tab stays when gh exits, with the pull request's link. It never
force-pushes. The menu greys the entry out and says why when gh is not
installed, the tab is on the default branch, or the branch has no commits
ahead of it. Neither action has a key by default; set `view_diff` or
`create_pr` under `[keys]` to give them one.

Under the tabs, a line shows the agent's model, the tokens its session used
and how full its context window is, with a small meter that turns yellow at
80% and red at 95%. Flow's Session section splits the tokens into input,
output, cache read and cache write, by model when the session used several,
subagents included. A tab's hover card shows the same line for its agents.
The counts come from the agent's session file: Claude Code's message usage,
Codex's token counts, pi's message usage. A file over 8 MiB is read from its
last 8 MiB, so its counts read "≥". Cost in dollars is off, since a
subscription does not bill per token; set `show_cost = true` under `[usage]`
or use the switch under Agents in the settings page to add what the tokens
would cost at the API's list prices. pitwall carries a dated price table and
shows its date; a model missing from it shows tokens and no cost.

Selecting text with the mouse copies it to the clipboard, as zellij and Warp
do, and a small "Copied 42 characters" notice shows at the bottom of the panes.
Drag to select, double-click a word, or hold Shift when a program owns the
mouse. To turn it off, set `copy_on_select = false` under `[terminal]` in
config.toml or use the switch under Terminal in the settings page; Ctrl+Shift+C
still copies.

Links in panes are underlined, both URLs in the text and OSC 8 hyperlinks.
Ctrl+click one (Cmd+click on macOS) to open it in your browser, even inside
Claude Code or Codex.
Set `links = false` under `[terminal]` to turn this off.

Programs can set the clipboard with OSC 52, the way Neovim, tmux and Helix
copy, also over ssh: `printf '\e]52;c;%s\a' "$(printf hi | base64)"` puts
"hi" on it. A write may hold up to 1 MB of text. Programs can never read
the clipboard this way: pitwall leaves OSC 52 read requests unanswered, so a
program cannot see what you copied elsewhere. Set `osc52 = "off"` under
`[terminal]` to ignore the writes.

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
`next_prompt` in config.toml.

### Grouping

<img src="docs/media/drag.webp" alt="Dragging a tab into the web-app group while the other rows slide apart, then dragging the billing group above the loose tabs" width="800">

- **By folder:** right-click a tab and choose **Group tabs in `<folder>`**.
  Every ungrouped tab in that repo or folder joins one group, and new tabs you
  start inside that folder join it automatically.
- **By hand:** Ctrl+click or Shift+click to pick tabs, then right-click
  and choose **New group** or **Move to group**.
- **Ungroup** or **Remove from group** never close anything.

Tabs inside a group sit indented under its header; loose tabs stay flush.

For a Git repo group, **New worktree tab** in the group menu starts a tab in a
fresh worktree under `<repo>/.worktrees/`, so parallel agents on one repo do
not step on each other. Deleting that tab removes the worktree it made. pitwall never deletes a folder it did not create.

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

### Detaching

<img src="docs/media/survive.webp" alt="The window closes, pitwall ls shows every tab still running, the window comes back, and after a reboot the agents resume" width="800">

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

## Command line

Run these from any terminal. Tab commands act on the current session: the
calling pane's session inside a pitwall pane, else the most recently used
one; `-s <session>` picks another. Inside a pane, commands that take an
optional name act on the pane's own tab.

| Command                               | Does                                                          |
| ------------------------------------- | ------------------------------------------------------------- |
| `pitwall`                             | Open a window on the most recently used session (starts the daemon if needed) |
| `pitwall -s <name>`                   | Open a window on a session, made if missing                   |
| `pitwall session ls [--json]`         | List sessions: tabs, agents working and needing you, windows, last used; `*` marks the current one |
| `pitwall session new [name] [-d] [dir]` | Make a session with a shell in dir and open a window on it; `-d` does not |
| `pitwall session attach <name>`       | Open a window on a session, or raise the one showing it       |
| `pitwall session rename [old] <new>`  | Rename a session, the current one without old                 |
| `pitwall session kill [-f] <name>`    | End a session and close its processes                         |
| `pitwall ls [--json]`                 | List the session's tabs in sidebar order: #, name, state, folder, group |
| `pitwall new [-n name] [-d] [dir] [-- cmd...]` | Open a tab and print its #; `-d` leaves it detached; with `-- cmd` the tab runs cmd instead of a shell |
| `pitwall wait <tab> --until done\|idle\|blocked\|exit` | Block until the tab's agent gets there (`--timeout 10m` to give up) |
| `pitwall attach [name]`               | Show a tab in the window, opening the window if needed        |
| `pitwall detach [name]`               | Hide a tab; its processes keep running                        |
| `pitwall rename [old] <new>`          | Rename a tab                                                  |
| `pitwall kill [-f] <name>`            | Close a tab and its processes; files are never touched        |
| `pitwall tab new`                     | Open a tab next to this pane's tab, in its folder             |
| `pitwall tab rename [name...]`        | Name this pane's tab; no name goes back to the automatic one  |
| `pitwall tab close`                   | Close this pane's tab                                         |
| `pitwall hooks install` / `uninstall` | Add or remove agent hooks (`--dry-run` to preview)            |
| `pitwall jev login` / `status` / `logout` / `report` | Connect TypeSafe's Jev, test the connection, disconnect, report what it did (see [Decisions](#decisions-jev)) |
| `pitwall logs [-f]`                   | Print the log paths; `-f` follows both logs (see [Logs](#logs)) |
| `pitwall remote pair` / `devices` / `revoke` | Pair a phone, list paired phones, unpair one (see [Phone](#phone)) |
| `pitwall --version`                   | Print the version                                             |

A name is a tab's `#` from `pitwall ls` (`3` or `#3`), its `id` from
`pitwall ls --json`, else its title. A
title matches exactly first, then by a unique prefix, so
`pitwall attach fix` finds the tab titled `fix login redirects`. Numbers
count within the session, and detached tabs come after the ones the sidebar
shows. A session is named in full or by a unique prefix.

```sh
pitwall session new billing -d ~/src/billing   # a background session
pitwall new -s billing -n api -d               # a tab in it
pitwall ls -s billing
pitwall session attach billing                 # open a window on it
```

### Driving tabs from agents and scripts

`new -- cmd`, `wait` and `ls --json` let an agent or a shell script start an
agent or a command in a tab you can watch and take over, then wait for it.
[docs/agent-skill.md](docs/agent-skill.md) is a page to hand an agent: the
states, exit codes and recipes.

```sh
tab=$(pitwall new -n review -d -- codex "review origin/main..HEAD; write SHIP or HOLD to review.txt")
pitwall wait "$tab" --until done --timeout 20m   # 0 done, 2 blocked, 3 exited, 124 timed out
pitwall ls --json | jq '.[] | {n, title, state, question}'
```

Give the agent its whole task as the prompt argument. Typing into a running
agent is not offered, because pitwall can't reliably tell when an agent is
ready to take input. A command tab's pane, and its exit code, last until you
close it, across daemon restarts too. A restart keeps the exit code but not
the output, resumes an agent with a known session, and does not run any
other unfinished command again: its exit code shows as unknown. Any process running as you can reach the
daemon's socket, so these commands give nothing a local process did not
already have.

## Keybindings

Two presets ship. **conventional** is the default and follows Linux terminal
defaults (Ghostty, kitty, GNOME Terminal); it leaves plain Ctrl+letters and
readline's Alt+B/F/D/. to the shell. **aide** is the Alt-key layout pitwall
started with. Pick one with `preset` in [config.toml](#configuration) and
override single actions there. The settings button in the sidebar footer
shows the bindings in effect.

Ctrl+Shift+P, in both presets, opens the command palette: every
action, its group and the keys bound to it in your config, filtered as you
type. Enter runs the highlighted one, the same as its key would; an action
with no key, like the tab and pane mode ones while their prefix is unbound,
runs from there too. Arrows or Ctrl+J/K move, Esc closes. Rebind it with
`command_palette`.

conventional:

| Keys                                    | Action                                                    |
| --------------------------------------- | --------------------------------------------------------- |
| Ctrl+Shift+T                            | New tab below this one, in its folder                     |
| Ctrl+Shift+W                            | Close the pane (the tab with its last one)                |
| Ctrl+Tab / Ctrl+Shift+Tab               | Next / previous tab, across groups                        |
| Ctrl+PageDown / Ctrl+PageUp             | Next / previous tab, across groups                        |
| Ctrl+Shift+PageDown / Ctrl+Shift+PageUp | First tab of the next / previous group                    |
| Alt+1-9                                 | Go to the Nth tab the sidebar shows; hold Alt to number them |
| Ctrl+Shift+O                            | Split the pane to the right                               |
| Ctrl+Shift+E                            | Split the pane below                                      |
| Ctrl+Alt+Right/Down, Ctrl+Alt+Left/Up   | Next / previous pane                                      |
| Ctrl+Shift+B                            | Show or hide the sidebar                                  |
| Ctrl+Shift+L                            | Show or hide the agent panel                              |
| Ctrl+Shift+U                            | Go to the tab that needs you, newest first, in any session |
| Ctrl+Shift+S                            | Session switcher                                          |
| Ctrl+Shift+] / Ctrl+Shift+[             | Next / previous session                                   |
| Ctrl+Shift+N                            | New session                                               |
| Ctrl+Shift+P                            | Command palette: every action and its keys                |
| Ctrl+Shift+C / Ctrl+Shift+V             | Copy selection / paste (also Ctrl+Insert / Shift+Insert)  |
| Ctrl+Backspace                          | Delete the word before the cursor (sends Ctrl+W)          |
| Shift+PageUp / Shift+PageDown           | Scroll back / forward one page                            |
| Ctrl+Shift+F                            | Find in the pane's scrollback                             |
| Ctrl+Shift+Up / Ctrl+Shift+Down         | Scroll back / forward to the previous / next shell prompt |
| Escape                                  | Close a dialog or settings, cancel a drag                 |

conventional leaves tab mode and pane mode unbound so Ctrl+T and Ctrl+P
reach the shell. Give `tab_prefix` or `pane_prefix` a chord in config.toml
or the settings page to use them; their keys are the ones in the aide table.
The command palette runs their actions either way.

aide:

| Keys                              | Action                                         |
| --------------------------------- | ---------------------------------------------- |
| Alt+J / Alt+K                     | Next / previous tab, across groups             |
| Alt+H / Alt+L                     | Previous / next pane                           |
| Alt+Arrows                        | Same as J / K / H / L                          |
| Alt+1-9                           | Go to the Nth tab the sidebar shows; hold Alt to number them |
| Alt+Shift+T                       | New tab below this one, in its folder          |
| Alt+N                             | Split the pane to the right                    |
| Alt+Shift+N                       | Split the pane below                           |
| Alt+Shift+W                       | Close the pane                                 |
| Ctrl+B                            | Show or hide the sidebar (the shell no longer gets Ctrl+B) |
| Ctrl+Shift+L                      | Show or hide the agent panel                   |
| Alt+U                             | Go to the tab that needs you, newest first, in any session |
| Alt+S                             | Session switcher                               |
| Alt+] / Alt+[                     | Next / previous session                        |
| Ctrl+Shift+P                      | Command palette: every action and its keys     |
| Ctrl+T then n                     | New tab outside every group                    |
| Ctrl+T then g                     | New tab in this tab's group                    |
| Ctrl+T then x                     | Close the tab                                  |
| Ctrl+T then r                     | Rename the tab                                 |
| Ctrl+T then h / l or Left / Right | Previous / next tab in the group               |
| Ctrl+T then 1-9                   | Go to the Nth tab the sidebar shows            |
| Ctrl+T then u                     | Go to the tab that needs you, newest first     |
| Ctrl+T then s                     | Session switcher                               |
| Ctrl+T twice                      | Send Ctrl+T to the terminal                    |
| Ctrl+P then n                     | New pane, split along its longer side          |
| Ctrl+P then d / r                 | Split the pane down / right                    |
| Ctrl+P then x                     | Close the pane                                 |
| Ctrl+P then h / j / k / l or Arrows | Focus the pane left / below / above / right  |
| Ctrl+P then f                     | Fullscreen the pane, or end it                 |
| Ctrl+P then p or Tab              | Next pane                                      |
| Ctrl+P then Esc or Enter          | Leave pane mode                                |
| Ctrl+P twice                      | Send Ctrl+P to the terminal                    |
| Ctrl+Shift+C / Ctrl+Shift+V       | Copy / paste (also Ctrl+Insert / Shift+Insert) |
| Ctrl+Backspace                    | Delete the word before the cursor (sends Ctrl+W) |
| Shift+PageUp / Shift+PageDown     | Scroll back / forward one page                 |
| Ctrl+Shift+F                      | Find in the pane's scrollback                  |
| Ctrl+Shift+Up / Ctrl+Shift+Down   | Scroll back / forward to the previous / next shell prompt |
| Escape                            | Close a dialog or settings, cancel a drag      |

Tab mode runs one key and ends. Pane mode stays on, zellij style, so
Ctrl+P d j x splits, moves down and closes in one go; it ends on Esc, Enter,
Ctrl+P or any key it does not know. A pill at the bottom left shows the
mode and its keys. A fullscreen pane ends when focus leaves it or it closes.

Ctrl+Shift+F opens a find bar at the top right of the focused pane. It
searches the pane's history (its last 10,000 lines) and screen as you type,
ignoring case unless the query has a capital letter. Enter or F3 goes to the
next match up, Shift+Enter or Shift+F3 back down, and the bar shows "3 of 17".
Escape closes it and returns to the live screen. A line that wrapped matches
only within each of its rows. While the background service runs a pitwall
from before find, the bar says "Search needs the background service
restarted" and searches nothing; it searches once the service restarts.

Every action, with its config name, is listed by `pitwall config default`.
The session-era names `next_session`, `prev_session`, `new_session` and
`jump_session_1`-`9` still work as `next_tab`, `prev_tab`, `new_tab` and
`goto_tab_1`-`9`; `pitwall config check` notes each one to rename.

## Configuration

<img src="docs/media/settings.webp" alt="The settings page: theme cards recolor the window live, the font size steps up, and the shortcut recorder catches a conflict and swaps it, with config.toml updating alongside" width="800">

pitwall reads `~/.config/pitwall/config.toml` (`$XDG_CONFIG_HOME`). Without
the file everything has its default. An open window rereads the file within
a second of a change and applies keys, theme, fonts and spacing at once.
Mistakes show as a desktop notification; the window keeps running, and only
the broken entries fall back to their defaults.

| Command                  | Does                                                          |
| ------------------------ | ------------------------------------------------------------- |
| `pitwall config init`    | Write a commented config listing every option, and its schema |
| `pitwall config check`   | Print problems as `config.toml:LINE: message`; exit 1 if any  |
| `pitwall config default` | Print the commented config                                    |
| `pitwall config path`    | Print the config file's path                                  |
| `pitwall config schema`  | Print the JSON Schema (`schema theme` for theme files)        |

The file `init` writes starts with `#:schema ~/.config/pitwall/schema.json`,
so editors with taplo or Even Better TOML complete action names and flag a
bad chord, color or key as you type. The window refreshes the schema files
when a new pitwall knows more keys.

```toml
[keys]
preset = "conventional"
new_tab = ["Ctrl+Shift+T", "Super+T"]  # a chord or a list of chords
toggle_sidebar = "Ctrl+B"
tab_prefix = []                         # [] unbinds

[keys.tab]                              # tab mode, after tab_prefix
rename = "F2"

[keys.pane]                             # pane mode, after pane_prefix
fullscreen = ["F", "Z"]

[theme]
name = "tokyo-night"

[theme.colors]
primary = "#ff9e64"

[font]
mono_family = "Iosevka"
mono_size = 14
line_height = 1.1
mono_fallback = ["Noto Sans Mono CJK SC"]

[layout]
pane_gap = 4
pane_margin = 4
```

Chords are modifiers (`Ctrl`, `Alt`, `Shift`, `Super`) and a key joined by
`+`, in any case. Keys are a printable character, `Space`, `Tab`, `Enter`,
`Esc`, `Backspace`, `Delete`, `Home`, `End`, `PageUp`, `PageDown`, `Up`,
`Down`, `Left`, `Right` or `F1`-`F12`. Two actions on one chord is an error
naming both.

### Themes

Built in: `aide-dark` (the default), `aide-light`, `tokyo-night`,
`catppuccin-mocha`. A custom theme is `~/.config/pitwall/themes/<name>.toml`
with the same keys as `[theme]`; its `name` picks the built-in it starts
from, so it only lists what differs. Add `#:schema
~/.config/pitwall/theme.schema.json` as its first line for completion.

| `[theme.colors]`    | Used for                                    |
| ------------------- | ------------------------------------------- |
| `bg`                | Window background                           |
| `sidebar`           | Sidebar background                          |
| `surface`           | Pane canvas, dialogs, cards                 |
| `surface_secondary` | Selected rows, active tab, fields           |
| `surface_elevated`  | Hovered and floating surfaces, badges       |
| `border`            | Hairlines                                   |
| `fg`                | Text                                        |
| `muted`             | Secondary text                              |
| `primary`           | Accent: buttons, focus, the tab-mode chip   |
| `on_primary`        | Text on primary buttons                     |
| `red`               | Errors                                      |
| `yellow`            | Waiting for you                             |
| `green`             | Done, idle                                  |
| `blue`              | Working                                     |
| `purple`            | Plan ready                                  |

`[theme.terminal]` has `foreground`, `background`, `cursor` and `ansi`, an
array of the 16 ANSI colors (black, red, green, yellow, blue, magenta, cyan,
white, then the bright ones). Colors are `#rrggbb` or `#rrggbbaa`. The
daemon answers programs' color queries (OSC 10, 11 and 4) from the terminal
colors; a running daemon picks a change up the next time it starts.

### Fonts and spacing

`[font]` takes `ui_family` (default the bundled Geist), `ui_size` (13),
`mono_family` (`JetBrainsMono Nerd Font`), `mono_size` (13), `line_height`
(a multiple of the font's, 1.0) and `mono_fallback`, families tried for
characters the terminal font lacks before any monospace font and color
emoji. Families are any installed font (`fc-list : family`); a missing one is
reported and the default is used. `[layout]` sets `pane_gap` and
`pane_margin` in dp (both 4).

## Hooks

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

## Remote hosts

Agents can run on another machine, such as a build box or a cloud VM, while
the window runs on your laptop. Close the laptop and they keep working; open
it and the window reconnects.

```sh
pitwall --host me@box           # any ssh destination
pitwall --host box -s work      # session work on box
```

pitwall runs your own `ssh` with your `~/.ssh/config`, so the user, port,
key, agent and jump host are the ones `ssh box` already uses. There is no
new login, and no secret goes on a command line. It runs `pitwall
remote-start` on the host, which starts the daemon there unless one runs,
then forwards the daemon's socket to a socket in pitwall's runtime folder
here and opens the window on it. All of it goes through one shared ssh
connection (`ControlMaster`), which closes 10 minutes after the last window
on that host. A new session there opens in your home folder on the host.

To give a host a short name, list it in config.toml:

```toml
[[hosts]]
name = "box"
ssh = "me@box.example.com"
```

`pitwall --host box` then connects to `me@box.example.com`, and the window
title and the sidebar header say `box`. A name that is not listed goes to
ssh as it is.

Set the host up once:

```sh
ssh box 'curl -fsSL https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.sh | sh'
ssh box '~/.local/bin/pitwall hooks install'
```

The hooks belong on the host, where the agents run: they report to the
daemon there. pitwall looks for itself on the host's `PATH` and in
`~/.local/bin`, which a non-interactive ssh login often leaves off the
`PATH`. When pitwall is missing there, too old for `remote-start`, or of
another protocol major version, the window does not open and the error
gives the install command, pinned to this pitwall's release. pitwall never
installs anything on a host by itself. When the daemon running on the host
is of another major version than the pitwall installed there, the window
asks before restarting it, as after an upgrade.

On a Linux host with systemd, run `loginctl enable-linger` there once.
Without it, logging out of the host's last session, which closing the
laptop does, deletes the runtime folder that holds the daemon's socket, and
the next window can no longer reach the daemon your agents run in.

When the link drops, the window keeps its last screen under "Disconnected
from pitwall's background service." and reconnects as described under
[Upgrades](#upgrades). Each try runs ssh again, which opens a new connection
when the old one died, and forwards the socket again. The daemon on the host
does not notice, and its agents keep running.

Some things stay on the machine they belong to:

- Notifications come from the state the daemon sends, so they show on the
  laptop as usual.
- Ctrl+click opens a web link here. A file link names a file on the host and
  does nothing.
- Open folder as group takes the folder's full path on the host, without
  completion.
- The agent panel shows the agent's state, but not its transcript or the
  changed files, which are on the host.
- `pitwall ls`, `new`, `wait` and the other commands talk to the daemon of
  the machine they run on. Run them on the host, for example in one of its
  panes.
- Windows has no remote windows: its ssh cannot share a connection.

## Decisions (Jev)

pitwall can ask a decision model quick questions about what your agents are
doing. [TypeSafe's Jev](https://docs.typesafe.ai) is built for this: it
answers yes/no, multiple-choice and rating questions with calibrated
probabilities in roughly 70 to 500 ms and writes no text. pitwall uses it to
recommend an answer to approval prompts, to sort what needs you by urgency,
to see what agents without hooks are doing, and to flag finished turns that
need a look. It only ever suggests: every permission prompt is still yours
to answer.

Nothing is sent anywhere until you connect a provider. Each call times out,
after 1.5 s by default (`timeout` under `[decisions]`, 0.2 to 10 s), and a
failed or slow answer changes nothing: pitwall does what it would have done
without decisions.

### Connect

Get a key at [console.typesafe.ai/keys](https://console.typesafe.ai/keys),
then either open Settings, Decisions, paste it and press Connect, or run:

```sh
pitwall jev login     # asks for the key without echo; or: pass show typesafe | pitwall jev login
pitwall jev status    # one small real call: prints ok, the latency and what is on
pitwall jev logout    # removes the saved key; turns decisions off if the provider was jev
pitwall jev report    # what the decisions log says about the last 7 days (--days N)
```

The key goes into `credentials` next to `config.toml`
(`~/.config/pitwall/credentials`) with mode 0600, and on Linux and macOS
pitwall makes that folder private (0700) when other users could read or
change it; it refuses a credentials file others can read. On Windows the
file sits in your profile's AppData folder and inherits that folder's
permissions: pitwall sets no ACL of its own, so check the folder if your
profile is shared. The key never goes into `config.toml`, which people
share with their dotfiles, and pitwall never logs or prints it.
`TYPESAFE_API_KEY` in pitwall's environment wins over the file, as it does
for TypeSafe's own SDKs.

Connecting sets `provider = "jev"` under `[decisions]` and keeps every
other setting you have there. With none set, triage is on and approvals
suggest; screen reading and turn checks stay off until you turn them on.
`pitwall jev status` prints what is on.

### Features

| Feature | What it does | What it sends, and when |
| ------- | ------------ | ----------------------- |
| Approvals (`[decisions.approvals]`) | When Claude Code or Codex asks permission, asks whether the call is safe. `suggest` (the default) shows the answer on the tab's pill, in the switcher, in the hover card and in the pane's corner ("Jev: allow 96%"), with any risk pitwall sees in the call ("Jev: allow 96% · sudo"). It is only a suggestion: the agent's prompt shows at once, as without pitwall, and you answer it. While measuring, `holdout` of the prompts show only the risk; see [Is it worth it?](#is-it-worth-it). `off` asks nothing. | On each permission request: the tool, its input, the working directory, the repo root and your latest prompt. |
| Attention triage (`[decisions.triage]`) | Rates a pane that needs you as fyi, later, soon or now. The jump-to-attention key goes to the most urgent first, desktop notifications go out most urgent first (now is marked urgent), and fyi sends no notification. The pill reads "Input · now" for now. | When an agent pane starts needing you: its state and its question, approval detail, error or turn summary. |
| Agents without hooks (`[decisions.agents]`) | For Aider, Amp, Cursor agent, Goose and Crush, and Gemini CLI and OpenCode while their hooks are not installed (add more with `programs`), reads the screen and sets the pane's state when the answer's confidence reaches `threshold` (default 0.8). Programs are matched by process name, so a CLI that shows up as `node` is not seen. | The visible screen of those programs only, at most once per pane every 2 seconds and only while it changes. A shell or any other program's screen is never sent. |
| Turn check (`[decisions.turn_check]`) | When an agent finishes a turn, asks whether it needs your review: failed tests, errors left, unfinished work. The Done pill reads Check when the answer reaches `threshold` (default 0.8). | When a turn ends: the agent's last message only, never the screen. |

Before anything leaves your machine, pitwall removes what looks secret:
private keys, `Authorization` and cookie headers, bearer tokens, values
under secret-looking names (`KEY=value` lines, quoted values across lines,
`"password": ...` fields at any depth, `--password`-style flags), passwords
in URLs, common API token shapes (OpenAI, Anthropic, GitHub, GitLab, Slack,
AWS, Google, npm, JWTs), and your saved TypeSafe key wherever it appears,
map keys included, whichever provider answers. Only then does it cut long
text to fit the size it sends, and never in the middle of a word. That is pattern matching, not a guarantee, so
leave a feature off for work you would not send. Each pane may make at most
30 calls a minute.

### Recommendations, not decisions

pitwall never answers a permission prompt for you. Automatic approval was
left out because a command's text can't show what it will run (a test
runner runs project code, git runs commands from its config, a program name
can resolve to anything on `PATH`), so it needs sandboxed execution.

The `PermissionRequest` hook that `pitwall hooks install` registers reports
the request and exits at once, so the agent's prompt is never delayed. The
recommendation arrives a moment later. Next to it pitwall shows the first
risk it reads in the call's text, whatever the model said: `sudo`,
`rm -rf` (long options by any prefix, such as `--rec --fo`), a force push,
`git reset --hard`, a download piped into a shell, a secrets path
(`~/.ssh`, `~/.aws`, `.env*`, ...), a write into `.git`, `.claude`,
`.codex` or `.mcp.json`, a write outside the repo, or a `never_allow`
entry you listed (shown as "never_allow #N", never its text). These flags
are hints, not a guarantee. pitwall never recommends for `AskUserQuestion`
or a plan approval.

### Costs

Jev bills per input token, about $0.04 per million; output is free. A
question is a few hundred to a few thousand tokens, so a busy day of agents
costs cents. Settings, Decisions shows each feature's calls and failures
today, and `pitwall jev report` adds up the tokens the log recorded.

### Is it worth it?

The daemon logs every decision to `decisions.jsonl` in the state folder,
next to `daemon.log` (`~/.local/state/pitwall/` on Linux). Each call gets a
line with its feature, the pane id, the latency, the failure kind (timeout,
http, bad_reply, ...), the answer with its probability and confidence, and
the input tokens. Each outcome gets a line joined to its call by a random
id: how you answered a permission prompt and how long that took, whether
you prompted the agent again after a turn check, and how long a triaged
pane waited for you to focus it. The log never holds prompt text,
commands, tool input, file paths, screen text or error messages. It is
mode 0600 in a 0700 folder; at 10 MB it moves to `decisions.jsonl.1`,
replacing the one before. Token counts come from Jev's reply
(`usage.input_tokens`). A provider whose reply carries none, and a call
that timed out, get the request's JSON size divided by 4, marked as an
estimate.

Neither Claude Code nor Codex reports your answer to a permission prompt,
so pitwall reads it from what follows. The tool's `PostToolUse` (or
`PostToolUseFailure`) means you allowed it. A new prompt
(`UserPromptSubmit`), the end of the turn (`Stop`, `StopFailure`, Codex's
`Interrupt`) or of the session means you denied it. Another permission
request first, or closing the pane, leaves it unknown. The answer's time is
the last key you typed into the pane while it asked, else that hook. Blind
spots: a subagent running the same tool in between reads as an allow;
parallel prompts read as unknown; a deny Codex carries on from is only
seen when the turn ends, though its time still comes from your key.

To tell whether the recommendation helps, `holdout` under
`[decisions.approvals]` hides it on that share of permission prompts,
picked at random per prompt and recorded in the log. Jev is still asked and
its answer logged, but no pill, switcher, hover card or pane corner shows
it. pitwall's own risk flags still show ("Risk: sudo"). The default, 0.5,
fills both halves fastest, so a normal week gives an answer; set
`holdout = 0` to show every recommendation again.

`pitwall jev report [--days N]` reads the log for the last N days (7 by
default) and prints calls, failures and latency per feature, an estimated
cost at $0.04 per million input tokens, approval answer times with and
without the recommendation, how often Jev agreed with you, how often a
Check turn was followed by another prompt within 5 minutes against a Done
turn, and time to focus per triage level. It gives a one-line verdict once
each half has 30 answered prompts, with a bootstrap interval on the
difference in median answer time; until then it says how many it has.
`pitwall jev status` shows how many events the log holds.

### A local model instead

Set `provider = "command"` and `command = ["/path/to/classifier", "--flag"]`
under `[decisions]`. pitwall runs the command for every question, writes the
request on its stdin and reads the reply from its stdout, in Jev's shapes:

```json
{"state": {"tool": "Bash", "input": {"command": "go test ./..."}},
 "questions": {"verdict": {"type": "choice", "instructions": "...",
   "criteria": {"allow": "...", "ask": "...", "deny": "..."}}}}
```

```json
{"answers": {"verdict": {"type": "choice", "choice": "allow",
   "probabilities": {"allow": 0.96, "ask": 0.03, "deny": 0.01}, "confidence": 0.9}}}
```

A choice answer needs a probability for every option, summing to 1, with
the chosen option highest; a `score` answer carries `score`, `probabilities`
for every level keyed `"0"`, `"1"`, ... and `confidence`; a `noul` answer
carries `noul`, the probability of yes. pitwall ignores any other reply.
The same timeout and redaction apply, and the command never sees
`TYPESAFE_API_KEY`. On timeout pitwall kills the command's process group
(Windows: the command itself); a provider that detaches its own children
(`setsid`, daemonizing) is responsible for stopping them.

### Turn it off

Set approvals to `off` or switch a feature off in Settings, Decisions. To stop everything, set `provider = ""` (Disconnect in Settings
or `pitwall jev logout` do that when the provider is Jev): then nothing is
sent.

## Phone

pitwall can serve a small page where your phone sees which agents need you
and answers them: Allow or Deny on a permission prompt, a typed reply to a
question or a finished turn. It runs inside pitwall's background service.
There is no account, no relay and no app to install: the phone talks to your
computer over your own network.

It is off until you turn it on in Settings, Phone, or in config.toml:

```toml
[remote]
enabled = true
```

By default it listens on `127.0.0.1:7777`, which only your computer can
reach. The way to get it onto your phone is your tailnet. With
[Tailscale](https://tailscale.com) on both devices:

```sh
tailscale serve --bg 7777
```

That prints an address such as `https://laptop.tail1234.ts.net`, with a
certificate Tailscale manages and reachable only from your own devices. Put
it in the config so the pairing code points there:

```toml
[remote]
enabled = true
url = "https://laptop.tail1234.ts.net"
```

An SSH tunnel works too: `ssh -L 7777:127.0.0.1:7777 your-computer` from a
phone SSH client, then open `http://127.0.0.1:7777`.

### Pair a phone

Run `pitwall remote pair`, or press Pair in Settings, Phone. It shows a QR
code and a URL holding a one-time code, good for 10 minutes. Scan it and the
page trades the code for a device token, which the phone keeps; pitwall keeps
only the token's SHA-256 under `remote/` in the state folder (mode 0600). A
request without a valid token gets nothing. After 10 failed codes or tokens
in a minute, every request is refused until the minute is up.

| Command                         | Does                                              |
| ------------------------------- | ------------------------------------------------- |
| `pitwall remote pair [name]`    | Pair a phone; prints the QR code and URL          |
| `pitwall remote devices`        | List paired devices                               |
| `pitwall remote revoke <name>`  | Unpair one by name or id; its token stops at once |

### What the page shows and sends

The page lists the tabs that need you: approvals, questions, finished turns
and errors, each with the agent's question or the tool it wants, and the
risk pitwall flags in a permission request. It shows what the sidebar shows,
never a pane's screen or scrollback. It asks for the list every 3 seconds
while it is open.

Allow presses the first option of the agent's prompt, Yes: `1` for Claude
Code and `y` for Codex. Deny presses Esc. pitwall sends either only while
the pane still shows the prompt the phone showed, and only when its screen
has a permission prompt on it. A reply loses its control characters, so it
cannot press keys or end a paste, and goes in as a paste followed by Enter.
pi has no permission prompts, so its tabs only take replies.

`daemon.log` gets a line for each pairing and each answer, with the device's
name: "allow sent to pane …", "reply sent to pane …", never what the reply
said.

### Another address

To serve on your network without Tailscale, set an address other than
loopback. pitwall then requires HTTPS, with a certificate it makes for
itself:

```toml
[remote]
enabled = true
listen = "0.0.0.0:7777"
tls = true
```

The phone's browser will warn that it does not know the certificate.
`pitwall remote pair` prints the certificate's SHA-256; compare it with the
one the browser shows before you continue. A non-loopback address without
`tls = true` is a config problem, and the page stays off.

## Where things live

| What                | Where                                                         |
| ------------------- | ------------------------------------------------------------- |
| Saved tabs          | `~/.local/state/pitwall/state.json` (`$XDG_STATE_HOME`)       |
| Saved tab backups   | `state.json.prev`, `state.json.bad-<time>` and `state.json.restored-<time>` next to it |
| Logs                | `~/.local/state/pitwall/gui.log`, `daemon.log` and `crash.log` (`pitwall logs`) |
| Decisions log       | `~/.local/state/pitwall/decisions.jsonl` (`pitwall jev report`) |
| Socket              | `$XDG_RUNTIME_DIR/pitwall/pitwall.sock`, else `/tmp/pitwall-<uid>/` |
| Config and themes   | `~/.config/pitwall/` (`$XDG_CONFIG_HOME`)                     |
| Decision model key  | `~/.config/pitwall/credentials` (mode 0600), or `$TYPESAFE_API_KEY` |
| Paired phones       | `~/.local/state/pitwall/remote/` (token hashes, mode 0600)    |
| Window state        | `~/.local/state/pitwall/gui.json` (sidebar shown or hidden)   |

macOS uses the same paths. On Windows, config and themes live in
`%APPDATA%\pitwall`, and saved tabs, the logs, window state and the
socket in `%LOCALAPPDATA%\pitwall`. The `XDG_*` variables win when set.

After a reboot, run `pitwall`: tabs, groups and panes come back in their
folders. State saved by an older version opens with each of its nested tabs
as a tab of its own, in the same place and group. Agent panes resume with `claude --resume <id>`,
`codex resume <id>` or `pi --session <id>`, run as the agent's name on PATH or as the binary from the
pane's original command, with that command's options. Prompts, session selectors, print mode and
options the agent does not document are dropped, so a resume never sends the first prompt again. A
resume also keeps the permission mode the
agent's hooks last reported: Claude gets `--dangerously-skip-permissions` or `--permission-mode
<mode>`, and Codex gets `--dangerously-bypass-approvals-and-sandbox` when it ran with the full
bypass; Codex's other modes are left to its config. Other flags of an agent started in a shell, such
as `--model`, are not restored. If a resume fails within 3 seconds, the pane falls back to a shell
in the same folder. A restored agent in a pane that was a shell also drops to a shell in the same
folder whenever it exits. Running processes and scrollback do not survive a reboot.

pitwall never deletes saved tabs it cannot read. Before it upgrades
`state.json` to a newer format, it copies the old file to `state.json.prev`,
so you can copy it back for an older pitwall. A damaged `state.json`, or one a
newer pitwall saved, moves to `state.json.bad-<time>`, and pitwall starts
without those tabs and says so at the top of the window until you close the
notice. When a later pitwall can read a `state.json.bad-<time>` file, as
after you update past the version that saved it, it adds that file's tabs to
the ones you have and renames it to `state.json.restored-<time>`. A damaged
file stays where it is for you to fix or delete.

### Upgrades

The window and the daemon speak a protocol with a major version and a
feature level. A new window works with a daemon of the same major version,
whatever either one's level, so after most upgrades the running daemon keeps
serving new windows and every program in a pane keeps running. The daemon
runs the old binary until it restarts, so features that need the new daemon
arrive then.

A release that changes the protocol's major version cannot use the running
daemon. Its first window asks: "pitwall <version> needs to restart its
background service; programs running in panes will stop." Nothing stops
until you choose:

- **Restart now** has the old daemon save its state and stop, then starts
  the new one. Tabs come back as after a reboot, with agents resumed.
- **Later** closes the window and leaves the old daemon and its programs
  running. Windows still open from before the upgrade keep working. Run
  `pitwall` again when you are ready to restart; it asks again.

A window from before the upgrade whose daemon was restarted by a newer one
reopens itself on the installed binary. CLI commands and agent hooks of the
other major version get an error or, for hooks, nothing, until the restart.

When a window loses its daemon connection, for example because the daemon
crashed, it keeps showing the last screen under "Disconnected from pitwall's
background service." It reconnects on its own, starting a daemon when none
runs: at once, then 0.5 seconds after a failed try and twice as long after
each further one, up to 30 seconds. The Reconnect button tries at once. A
daemon started this way restores tabs as after a reboot.

## Troubleshooting

- **The window does not open from the launcher.** Errors are sent as a
  desktop notification (an alert on macOS, a message box on Windows); run
  `pitwall` in a terminal to see them.
- **An agent's state is missing or late.** Check `pitwall hooks install` was
  run with the installed binary, and in Codex run `/hooks` once.
- **The window freezes or is reported as not responding.** When one window
  event runs for over 2 seconds, pitwall writes every goroutine's stack to
  `~/.local/state/pitwall/stall-<time>.txt` and keeps the newest 5. Attach
  the newest stall file to the bug report. `gui.log` names it too, and notes
  frames slower than 250 ms, split into layout and rendering time.
- **Something else.** Look at the logs (below).

### Logs

`pitwall logs` prints the paths of the three log files, and `pitwall logs -f`
follows them as they grow. They live in the state directory. On Linux and
macOS the files are mode 0600 in a 0700 directory, so only you can read them;
on Windows they inherit the permissions of your profile folder. A file that
is over 5 MB when a window or the daemon starts moves to `<name>.1`,
replacing the older one; the size is checked at startup only. Two known
limits come with that: two processes starting at the same moment can lose
some lines in the move, and a window started before another one moved
`gui.log` keeps writing to `gui.log.1` until it restarts, which
`pitwall logs -f` does not show.

Every window writes events to `gui.log` and the daemon to `daemon.log`, one
line each, starting with the time, `gui` or `daemon`, the version and the
process id, so the lines of two windows can be told apart. They record
starts and stops, connections, panes starting and exiting (with the
program's name and exit code), sizes sent and applied, errors, slow requests
and frames. They never record what a pane shows, what you type, prompts,
hook payloads, environment variables or a command's arguments. They do hold
folder paths and pane ids.

`crash.log` is different. It holds Go's standard crash trace from windows
and the daemon, and anything the daemon prints before its log opens. A
crash trace is the panic value and the crashing goroutine's stack (more
goroutines if `GOTRACEBACK` asks for them), verbatim, and does not follow the
line format, so the panic value can hold any text pitwall was handling.
Read it before you share it.

For a bug report, attach `gui.log` and `daemon.log` from around the time it
happened, any stall file, and, after checking it, `crash.log`.

## Development

`mise.toml` sets `GOFLAGS=-tags=novulkan` so Gio builds with OpenGL and no
Vulkan headers. Use mise so every command gets it:

```sh
mise exec -- go build ./cmd/pitwall
mise exec -- go vet ./...
mise exec -- go test -race ./...
```

Releases use patch versioning; see [CHANGELOG.md](CHANGELOG.md).

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) for setup, checks and the pull request
process. Use the [issue templates](https://github.com/quanticstudios/pitwall/issues/new/choose)
to report bugs or propose features. Report vulnerabilities privately as
described in [SECURITY.md](SECURITY.md).

## Credits

pitwall stands on other people's open source work. The full list, with every
license, is in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). In short:

- [tuios](https://github.com/Gaurav-Gosain/tuios) (MIT, Gaurav Gosain):
  pitwall adapted its PTY spawning, agent detection, resume commands and
  screen patterns, and its test fixtures.
- [charmbracelet/x/vt](https://github.com/charmbracelet/x) (MIT,
  Charmbracelet): the terminal emulator, vendored with a one-line patch.
- [Gio](https://gioui.org) (MIT / Unlicense): the UI toolkit.
- [creack/pty](https://github.com/creack/pty) (MIT): pseudo-terminals.
- [go-text/typesetting](https://github.com/go-text/typesetting) (BSD / Unlicense): text shaping.
- [Lucide](https://lucide.dev) (ISC) and Feather (MIT): the icons.
- [Geist](https://vercel.com/font) (OFL 1.1): the UI font.
- [BurntSushi/toml](https://github.com/BurntSushi/toml) (MIT): the config parser.
- [Tokyo Night](https://github.com/folke/tokyonight.nvim), [Catppuccin](https://github.com/catppuccin/palette)
  and GitHub's [Primer](https://github.com/primer/primitives) (all MIT): theme colors.
- aide (Quantic Studios): the sidebar design and agent states pitwall ports.
- [zj-radar](https://github.com/marktoda/zj-radar), [zellij](https://zellij.dev),
  [tmux](https://github.com/tmux/tmux) and [Ghostty](https://ghostty.org):
  ideas for hook handling, tab mode, session naming, detach and keymaps.

## License

MIT, see [LICENSE](LICENSE).
