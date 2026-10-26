# Concepts

pitwall has six nouns. The rest is keys and config.

```
daemon ──owns──▶ sessions ──hold──▶ groups and tabs ──split into──▶ panes
                                                                     │
                                   each pane: a shell, a command or an agent
```

[GLOSSARY.md](../GLOSSARY.md) has one line per term and the words to avoid.

## Daemon

The background process that owns every terminal. The window and the
`pitwall` commands are clients of it. Close the window and the daemon keeps
your tabs running. After a reboot it reopens them in the same folders and
resumes the agents that have a session to resume.

## Session

A named set of tabs and groups, like a tmux session. A window shows one
session; the session switcher moves between them. `pitwall -s <name>` opens a
window on one.

## Tab

One working context: a set of split panes started in a folder. It is one row in the sidebar, and `#N` on the command line, its
number in `pitwall ls`. A tab opened by another tab's agent is a **worker**.
It hangs under that tab, whose agent is the **orchestrator**. The code calls
a tab a `Workspace`.

## Group

Tabs you put under one sidebar header, often all the tabs of one repo. Tabs
start ungrouped. The code calls a group a `Project`.

## Pane

One terminal in a tab, running a shell, a command or an agent. A pane is
`TAB.N` (`3.2` is tab 3's second pane) or `@<id>` from `pitwall ls --json`.

## Agent and its state

A coding agent in a pane reports through the hooks `pitwall hooks install`
adds (see [hooks.md](hooks.md)). The sidebar row shows its state, and
`pitwall ls --json` and `pitwall wait` report it under these names:

| State     | Means                                                                       |
| --------- | --------------------------------------------------------------------------- |
| `working` | a turn runs; for a tab without an agent, a command runs in its shell        |
| `blocked` | the agent waits on a permission prompt, a question or a plan approval       |
| `done`    | the agent finished its turn, or ended it with an error                      |
| `idle`    | the agent sits at its prompt with nothing new                               |
| `running` | no agent is known and the process runs: a shell at its prompt, or a command |
| `exited`  | the tab's process ended (`exit_code` has its status)                        |

`blocked` is what the sidebar shows as Input, Approval or Plan. pitwall
never answers a permission prompt for an agent; the human does.

A worktree gives a tab its own checkout and branch: `pitwall worktree new`
opens one, so agents in different tabs never edit the same files.
