# Shell permission screens

`command_screens.json` pairs permission screens with their exact shell inputs.
The files under `command_screens/` contain plain rows read from a temporary
pitwall daemon's VT.
Complete forms omit the transcript above the dialog. Clipped forms retain the
whole visible screen. No rows inside a form are edited.

The agents are Claude Code 2.1.322 with
`claude --model claude-sonnet-5-5 --effort low --permission-mode default`
and Codex 0.167.1 with `codex --model gpt-6.1-sol` and low effort.
Claude uses an explicit `Bash` ask rule. Codex uses `on-request` approvals and
`read-only` sandboxing with an escalated shell call. Each agent uses a temporary
configuration directory. The daemon uses temporary XDG directories.

All screens have 120 columns. The two-line, 30-line heredoc, tabs and wrapping
captures use 70 rows. The 150-line heredoc uses 200 rows for the complete form
and 70 rows for the clipped form. The JSON records each replay's dimensions.

Claude draws a `│` gutter on each command row and expands tabs to spaces.
Codex prefixes the first command row with `$ ` and indents later rows. Codex
drops tabs between tokens and in indentation, so commands containing tabs
refuse in Codex. Claude tab indentation uses 8-column stops.

Both agents wrap long lines between words. Both permission dialogs draw all
150 heredoc lines and scroll their opening rows off a short terminal. Neither
permission dialog shows a count marker in these captures. Codex's transcript
above its dialog uses `… +N lines`. Claude's compact transcript ends long tool
previews with `…`; Ctrl+O opens the detailed transcript and hides the dialog.

A truncated prefix cannot identify a command that differs only in hidden text.
`CommandShown` therefore requires the full command. It compares each nonblank
line with its relative indentation, collapsing only spaces and tabs within a
line. A screen row continues the same line only where the next word cannot fit
before the right margin, or a full row splits a word. Other Unicode whitespace
and control characters refuse.
Count markers, an ellipsis, missing opening rows, and unknown layouts refuse.
The synthetic count-marker cases in `form_test.go` cover prefixes longer than
80 characters that fit two different commands.
