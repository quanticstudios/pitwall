# Glossary

**Daemon**: the background process (`pitwall daemon`) that owns every terminal and the state; the window and the `pitwall` commands are its clients.
_Avoid_: server, service (outside messages to users, which say "pitwall's background service")

**Session**: a named set of tabs and groups, like a tmux session; a window shows one session at a time. `model.Session` in the code.
_Avoid_: workspace, window

**Tab**: one working context in a session: a split tree of panes started in a folder, shown as one sidebar row. `model.Workspace` in the code, and `#N` on the command line.
_Avoid_: workspace (outside the code), window, row

**Pane**: one terminal in a tab, running a shell, a command or an agent. Addressed as `TAB.N` or `@<id>`.
_Avoid_: terminal (for the address), split, window

**Group**: tabs the user put together under one sidebar header, often for one repo or folder. `model.Project` in the code.
_Avoid_: project (outside the code), folder

**Agent**: a coding agent pitwall knows (Claude Code, Codex, OpenCode, Gemini CLI, Cursor CLI, pi, Oh My Pi, and those it reads from the screen) running in a pane. `model.Provider` names which one.
_Avoid_: provider (outside the code), bot, assistant

**Agent state**: what a pane's agent is doing. The sidebar shows Working, Input, Approval, Plan, Done or Error, or a running command's name; `pitwall ls --json` and `pitwall wait` report `working`, `blocked` (Input, Approval or Plan), `done`, `idle`, `running` and `exited`.
_Avoid_: status (for the agent's), activity (outside the code)

**Needs you**: Input, Approval or Plan: the agent waits on the human. `blocked` on the command line.
_Avoid_: waiting, stuck, attention

**Worker**: a tab another tab's agent opened with `pitwall new` or `pitwall worktree new`; it hangs under that tab in the sidebar.
_Avoid_: child (except in the `--children` flag), subagent (an agent's own in-process helpers), fleet

**Orchestrator**: the agent whose tab opened workers and waits for them with `pitwall wait --children`. The sidebar shows its row as Watching while its turn has ended and a worker still works.
_Avoid_: parent, lead, manager

**Worktree**: a git worktree pitwall made for a tab with `pitwall worktree new` or the New task dialog, on its own branch, so agents in different tabs never edit the same checkout.
_Avoid_: checkout, clone, branch (for the folder)

**Hooks**: the commands `pitwall hooks install` adds to each agent's config (a plugin for OpenCode, an extension for pi and Oh My Pi) that report the agent's state to the daemon.
_Avoid_: integration, plugin (for all of them)

**Task**: a prompt for an agent from the New task dialog or `pitwall queue add`. Start opens its tab now; a queued task waits until an agent of its session finishes.
_Avoid_: job, ticket
