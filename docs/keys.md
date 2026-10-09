# Keybindings

Two presets ship. **conventional** is the default and follows Linux terminal
defaults (Ghostty, kitty, GNOME Terminal); it leaves plain Ctrl+letters and
readline's Alt+B/F/D/. to the shell. **aide** is the Alt-key layout pitwall
started with. Pick one with `preset` in [config.toml](config.md) and
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
| Ctrl+Shift+Y / Ctrl+Shift+D             | Allow / Deny the permission prompt of the focused pane, else of the shown tab |
| Ctrl+Shift+A                            | New task: an agent on a prompt, started now or queued     |
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
