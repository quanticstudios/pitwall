# Decisions (Jev)

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

## Connect

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

## Features

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

## Recommendations, not decisions

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

## Costs

Jev bills per input token, about $0.04 per million; output is free. A
question is a few hundred to a few thousand tokens, so a busy day of agents
costs cents. Settings, Decisions shows each feature's calls and failures
today, and `pitwall jev report` adds up the tokens the log recorded.

## Is it worth it?

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

## A local model instead

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

## Turn it off

Set approvals to `off` or switch a feature off in Settings, Decisions. To stop everything, set `provider = ""` (Disconnect in Settings
or `pitwall jev logout` do that when the provider is Jev): then nothing is
sent.
