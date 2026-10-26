# Decisions (Jev)

pitwall can ask a decision model quick questions about what your agents are
doing. [TypeSafe's Jev](https://docs.typesafe.ai) is built for this: it
answers yes/no, multiple-choice and rating questions with calibrated
probabilities in roughly 70 to 500 ms and writes no text. pitwall uses it to
recommend an answer to approval prompts, to sort what needs you by urgency,
to flag finished turns that need a look, to catch a check that passed on
its exit code but not in its output, and to spot an agent that is stuck.
It only ever suggests: every permission prompt is still yours to answer.

Each question is one narrow yes/no, as TypeSafe advises, and pitwall's own
rules answer first where they can. Every feature logs what it would have
done and what you did next, so `pitwall jev report` can say whether it
beats doing without it; see [Is it worth it?](#is-it-worth-it).

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
suggest; turn checks, check runs and stuck agents stay off until you turn
them on.
`pitwall jev status` prints what is on.

## Features

| Feature | What it does | What it sends, and when |
| ------- | ------------ | ----------------------- |
| Approvals (`[decisions.approvals]`) | When Claude Code or Codex asks permission, a call pitwall sees a risk in shows that risk ("Risk: sudo", see below) and the model is not asked. For any other call it asks two questions, whether you asked for it and whether it is hard to undo, and recommends allow when the first is likely and the second is not. `suggest` (the default) shows the answer on the tab's pill, in the switcher, in the hover card and in the pane's corner ("Jev: allow 81%"). It is only a suggestion: the agent's prompt shows at once, as without pitwall, and you answer it. While measuring, `holdout` of the prompts show nothing; see [Is it worth it?](#is-it-worth-it). `off` asks nothing. | On each permission request pitwall sees no risk in: the tool, its input, the working directory, the repo root and your latest prompt. |
| Attention triage (`[decisions.triage]`) | Asks whether a pane that needs you is blocked on you (now) or has nothing for you to do (fyi); neither is soon. The jump-to-attention key goes to now first, desktop notifications go out most urgent first (now is marked urgent), and fyi sends no notification. The pill reads "Input · now" for now. `holdout` (default 0.2) leaves that share of panes untriaged, to measure it. | When an agent pane starts needing you: its state and its question, approval detail, error or turn summary. |
| Turn check (`[decisions.turn_check]`) | When an agent finishes a turn, asks whether it needs your review: failed tests, errors left, unfinished work. The Done pill reads Check when the answer reaches `threshold` (default 0.8). | When a turn ends: the agent's last message only, never the screen. |
| Check runs (`[decisions.check]`) | When an agent runs a check (pre-commit, tests, a linter, a type check, a build, `git commit`), one that exits non-zero fails at once. For one that exits 0, asks whether the output still shows a problem: failures, tests skipped or none found, hooks bypassed (`--no-verify`, `SKIP=`), errors hidden (`\|\| true`). A turn that ends on a failed check reads Check. | After a check that exited 0: the command and the end of its output. |
| Stuck agents (`[decisions.stuck]`) | When an agent has worked 10 minutes without stopping, asks whether it is stuck: repeating an action or an error, retrying a failing command, waiting on something that will not finish. It asks again every 10 minutes while the agent works. The pill reads "Working · stuck" when the answer reaches `threshold` (default 0.8). | The visible screen of that agent's pane, and only while the agent whose hooks reported is in the foreground. |

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
recommendation arrives a moment later. Before asking, pitwall reads the
call's text for a risk, and when it finds one it shows the first ("Risk:
sudo") and asks the model nothing: `sudo`,
`rm -rf` (long options by any prefix, such as `--rec --fo`), a force push,
`git reset --hard`, a download piped into a shell, a secrets path
(`~/.ssh`, `~/.aws`, `.env*`, ...), a write into `.git`, `.claude`,
`.codex` or `.mcp.json`, a write outside the repo, or a `never_allow`
entry you listed (shown as "never_allow #N", never its text). These flags
are hints, not a guarantee. pitwall never recommends for `AskUserQuestion`
or a plan approval.

The [guard](usage.md#the-guard) is no exception. It denies a command such
as `git push` before the agent's permission check runs, in a tab you turned
it on for, and the agent reads why. A deny is not an answer to a prompt: it
never allows a call, never approves one the agent asked you about, and
the guard reads only the command's text, so it is a seatbelt, not a
sandbox.

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
id: how you answered a permission prompt and how long that took, how long
a triaged pane waited for you to focus it, whether your next prompt after
a turn check or a check run read as a correction ("still fails", "that's
wrong", "fix ..."; pitwall reads the prompt and logs only yes or no), and
whether you pressed Esc or Ctrl+C on an agent asked about being stuck. A
turn check also logs what a plain rule says about the same message (it
names a failure, an error, a skip or work left, or ends on a question), so
the report can show whether the model beats the rule. The log never holds prompt text,
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
it. The default, 0.5, fills both halves fastest; set `holdout = 0` to
show every recommendation again. `holdout` under `[decisions.triage]`
does the same for triage: that share of panes, 0.2 by default, stays
untriaged, so the report can tell whether "now" gets you there sooner.

`pitwall jev report [--days N]` reads the log for the last N days (7 by
default) and prints calls, failures and latency per feature, an estimated
cost at $0.04 per million input tokens, approval answer times with and
without the recommendation and how often Jev agreed with you, time to
focus per triage level with the answer used and held out, and for turn
checks, check runs and stuck agents how often a flag was followed by a
correction or an interrupt, against the rest (the lift). Each feature gets
a line once both sides have 30 outcomes; until then it says how many it
has. The approval verdict adds a bootstrap interval on the difference in
median answer time.
`pitwall jev status` shows how many events the log holds. Settings,
Decision stats draws the same numbers over the last 7 days, 30 days or all
time, with calls per day as a chart.

## A local model instead

Set `provider = "command"` and `command = ["/path/to/classifier", "--flag"]`
under `[decisions]`. pitwall runs the command for every question, writes the
request on its stdin and reads the reply from its stdout, in Jev's shapes:

```json
{"state": {"tool": "Bash", "input": {"command": "go test ./..."}},
 "questions": {"requested": {"type": "noul", "instructions": "..."},
   "irreversible": {"type": "noul", "instructions": "..."}}}
```

```json
{"answers": {"requested": {"type": "noul", "noul": 0.96},
   "irreversible": {"type": "noul", "noul": 0.02}}}
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
