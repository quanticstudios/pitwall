# Keybindings

Three presets ship. **conventional** is the default on Linux and Windows and
follows Linux terminal defaults (Ghostty, kitty, GNOME Terminal); it leaves
plain Ctrl+letters and readline's Alt+B/F/D/. to the shell. **aide** is the
Alt-key layout pitwall started with. **mac** is the default on macOS: it puts
the app's keys on Cmd, as macOS apps do, so every Ctrl key reaches the
shell. Pick one with `preset` in [config.toml](config.md) and override single
actions there; a config that names a preset keeps it on every platform. The
settings button in the sidebar footer shows the bindings in effect.

In a config, `Super` and `Cmd` are the same modifier: the Command key on
macOS, the logo key elsewhere.

Ctrl+Shift+P (Cmd+Shift+P in mac) opens the command palette: every
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
| Ctrl+Shift+R                            | Review the tab's changes: comment, then send to its agent |
| Ctrl+Shift+U                            | Go to the tab that needs you, newest first, in any session |
| Ctrl+Shift+Y / Ctrl+Shift+D             | Allow / Deny the permission prompt of the focused pane, else of the shown tab |
| Ctrl+Shift+A                            | New task: an agent on a prompt, started now or queued     |
| Ctrl+Shift+S                            | Session switcher                                          |
| Ctrl+Shift+] / Ctrl+Shift+[             | Next / previous session                                   |
| Ctrl+Shift+N                            | New session                                               |
| Ctrl+Shift+P                            | Command palette: every action and its keys                |
| Ctrl+Shift+C / Ctrl+Shift+V             | Copy selection / paste (also Ctrl+Insert / Shift+Insert)  |
| Ctrl+Shift+X                            | Copy mode: select and copy with the keyboard              |
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
| Ctrl+Shift+R                      | Review the tab's changes and comment on them   |
| Alt+U                             | Go to the tab that needs you, newest first, in any session |
| Ctrl+Shift+Y / Ctrl+Shift+D       | Allow / Deny the permission prompt of the focused pane, else of the shown tab |
| Ctrl+Shift+A                      | New task: an agent on a prompt, started now or queued |
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
| Ctrl+Shift+X                      | Copy mode                                      |
| Ctrl+Backspace                    | Delete the word before the cursor (sends Ctrl+W) |
| Shift+PageUp / Shift+PageDown     | Scroll back / forward one page                 |
| Ctrl+Shift+F                      | Find in the pane's scrollback                  |
| Ctrl+Shift+Up / Ctrl+Shift+Down   | Scroll back / forward to the previous / next shell prompt |
| Escape                            | Close a dialog or settings, cancel a drag      |

mac:

| Keys                                    | Action                                                    |
| --------------------------------------- | --------------------------------------------------------- |
| Cmd+T                                   | New tab below this one, in its folder                     |
| Cmd+W                                   | Close the pane (the tab with its last one)                |
| Cmd+Shift+] / Cmd+Shift+[               | Next / previous tab (also Ctrl+Tab / Ctrl+Shift+Tab)      |
| Cmd+Shift+PageDown / Cmd+Shift+PageUp   | First tab of the next / previous group                    |
| Cmd+1-9                                 | Go to the Nth tab the sidebar shows                       |
| Cmd+D / Cmd+Shift+D                     | Split the pane to the right / below                       |
| Cmd+Option+Arrows                       | Next / previous pane                                      |
| Cmd+B                                   | Show or hide the sidebar                                  |
| Cmd+L                                   | Show or hide the agent panel                              |
| Cmd+Shift+R                             | Review the tab's changes: comment, then send to its agent |
| Cmd+U                                   | Go to the tab that needs you, newest first, in any session |
| Cmd+Shift+Y / Cmd+Shift+N               | Allow / Deny the permission prompt of the focused pane, else of the shown tab |
| Cmd+Shift+A                             | New task: an agent on a prompt, started now or queued     |
| Cmd+S                                   | Session switcher                                          |
| Cmd+] / Cmd+[                           | Next / previous session                                   |
| Cmd+N                                   | New session                                               |
| Cmd+Shift+P                             | Command palette: every action and its keys                |
| Cmd+,                                   | Settings                                                  |
| Cmd+C / Cmd+V                           | Copy selection / paste                                    |
| Cmd+Shift+X                             | Copy mode                                                 |
| Shift+PageUp / Shift+PageDown           | Scroll back / forward one page                            |
| Cmd+F                                   | Find in the pane's scrollback                             |
| Cmd+Up / Cmd+Down                       | Scroll back / forward to the previous / next shell prompt |

Like conventional, mac leaves tab mode and pane mode unbound.

Tab mode runs one key and ends. Pane mode stays on, zellij style, so
Ctrl+P d j x splits, moves down and closes in one go; it ends on Esc, Enter,
Ctrl+P or any key it does not know. A pill at the bottom left shows the
mode and its keys. A fullscreen pane ends when focus leaves it or it closes.

Ctrl+Shift+F (Cmd+F in mac) opens a find bar at the top right of the focused pane. It
searches the pane's history (10,000 lines unless `scrollback` says otherwise) and screen as you type,
ignoring case unless the query has a capital letter. Enter or F3 goes to the
next match up, Shift+Enter or Shift+F3 back down, and the bar shows "3 of 17".
Escape closes it and returns to the live screen. A line that wrapped matches
only within each of its rows. While the background service runs a pitwall
from before find, the bar says "Search needs the background service
restarted" and searches nothing; it searches once the service restarts.

Copy mode (Ctrl+Shift+X, Cmd+Shift+X in mac) selects and copies with the
keyboard, as in tmux. A block cursor starts on the terminal's cursor and a
badge at the bottom right of the pane names the mode. While it is on, no key
reaches the program.

| Keys                          | Action                                                  |
| ----------------------------- | ------------------------------------------------------- |
| h j k l, Arrows               | Move a cell or a line                                   |
| w / b                         | Next / previous word                                    |
| 0 / $, Home / End             | Start / end of the line                                 |
| g / G                         | Oldest line in history / the last line                  |
| Ctrl+U / Ctrl+D               | Half a page up / down                                   |
| PageUp / PageDown             | A page up / down                                        |
| / or ?                        | Open the find bar; its current match moves the cursor   |
| v / V / Ctrl+V                | Select characters / lines / a block from the cursor     |
| y or Enter                    | Copy the selection and leave                            |
| Esc or q                      | Leave without copying                                   |

Every action, with its config name, is listed by `pitwall config default`.
The session-era names `next_session`, `prev_session`, `new_session` and
`jump_session_1`-`9` still work as `next_tab`, `prev_tab`, `new_tab` and
`goto_tab_1`-`9`; `pitwall config check` notes each one to rename.
