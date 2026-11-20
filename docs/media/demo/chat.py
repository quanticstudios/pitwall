#!/usr/bin/env python3
import datetime
import json
import os
import sys
import uuid

USAGE = "usage: python3 -I chat.py <out dir> <webhook-retry worktree> <failed-deliveries-page worktree> <currencies worktree>"
MODEL = "claude-opus-5-5"
VERSION = "2.1.328"


class Transcript:
    def __init__(self, name, cwd, branch, start, sidechain=None):
        self.session = str(uuid.uuid5(uuid.NAMESPACE_URL, "pitwall-demo/" + name))
        self.cwd, self.branch = cwd, branch
        self.t = start
        self.parent = None
        self.sidechain = sidechain
        self.lines = []
        self.n = 0

    def _id(self, kind):
        self.n += 1
        return str(uuid.uuid5(uuid.NAMESPACE_URL, f"{self.session}/{self.sidechain}/{kind}/{self.n}"))

    def _add(self, line, secs):
        self.t += datetime.timedelta(seconds=secs)
        u = self._id("line")
        base = {
            "parentUuid": self.parent,
            "isSidechain": self.sidechain is not None,
            "userType": "external",
            "cwd": self.cwd,
            "sessionId": self.session,
            "version": VERSION,
            "gitBranch": self.branch,
            "uuid": u,
            "timestamp": self.t.isoformat(timespec="milliseconds").replace("+00:00", "Z"),
        }
        if self.sidechain:
            base["agentId"] = self.sidechain
        base.update(line)
        self.parent = u
        self.lines.append(base)
        return u

    def prompt(self, text, secs=0):
        return self._add({"type": "user", "message": {"role": "user", "content": text},
                          "permissionMode": "bypassPermissions", "origin": {"kind": "human"},
                          "promptSource": "typed"}, secs)

    def command(self, name, args, stdout, secs=0):
        self._add({"type": "user", "isMeta": True, "message": {"role": "user", "content":
                   "<local-command-caveat>The command below was run directly in Claude Code, not sent to you as a request, and its output goes straight to the user. It's recorded here as context for later messages.</local-command-caveat>"}}, secs)
        self._add({"type": "user", "message": {"role": "user", "content":
                   f"<command-name>/{name}</command-name>\n            <command-message>{name}</command-message>\n            <command-args>{args}</command-args>"}}, 0)
        self._add({"type": "user", "message": {"role": "user", "content":
                   f"<local-command-stdout>{stdout}</local-command-stdout>"}}, 0)

    def assistant(self, msg, blocks, secs, usage, thinking_ms=None):
        line = {"type": "assistant", "message": {
            "model": MODEL, "id": msg, "type": "message", "role": "assistant",
            "content": blocks, "stop_reason": "tool_use", "stop_sequence": None,
            "usage": usage}, "requestId": "req_" + msg[4:]}
        if thinking_ms:
            line["thinkingDurationMs"] = thinking_ms
        self._add(line, secs)

    def tool(self, msg, usage, name, inp, secs=1):
        tid = "toolu_" + self._id("tool").replace("-", "")[:24]
        self.assistant(msg, [{"type": "tool_use", "id": tid, "name": name, "input": inp}], secs, usage)
        return tid

    def result(self, tid, content, result, secs, error=False):
        block = {"tool_use_id": tid, "type": "tool_result", "content": content}
        if error:
            block["is_error"] = True
        self._add({"type": "user", "message": {"role": "user", "content": [block]},
                   "toolUseResult": result}, secs)

    def done(self, ms, secs=0):
        self._add({"type": "system", "subtype": "turn_duration", "durationMs": ms, "isMeta": False}, secs)

    def write(self, path):
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            for line in self.lines:
                f.write(json.dumps(line, ensure_ascii=False) + "\n")


def usage(inp, out, read, made=0):
    return {"input_tokens": inp, "cache_creation_input_tokens": made,
            "cache_read_input_tokens": read, "output_tokens": out,
            "service_tier": "standard"}


def todos(items):
    return {"todos": [{"content": c, "activeForm": a, "status": s} for c, a, s in items]}


DELIVER_PATCH = [
    {"oldStart": 19, "oldLines": 8, "newStart": 19, "newLines": 14, "lines": [
        " export async function deliverWebhook(d: Delivery): Promise<void> {",
        "   const res = await post(d.url, d.payload, { timeout: 10_000 })",
        "-  if (!res.ok) {",
        "-    await markFailed(d.id, `HTTP ${res.status}`)",
        "-    return",
        "-  }",
        "-  await markDelivered(d.id)",
        "+  if (res.ok) return markDelivered(d.id)",
        "+  const attempts = d.attempts + 1",
        "+  const error = `HTTP ${res.status}`",
        "+  if (attempts >= MAX_ATTEMPTS) {",
        "+    return markFailed(d.id, error, attempts)",
        "+  }",
        "+  await scheduleRetry(d.id, {",
        "+    attempts,",
        "+    error,",
        "+    nextAttemptAt: new Date(Date.now() + nextDelay(attempts)),",
        "+  })",
        " }",
    ]},
]

DELIVER_TS = """import { post } from '../http'
import { db } from '../db'

export interface Delivery {
  id: string
  url: string
  payload: unknown
  attempts: number
}

export async function markDelivered(id: string): Promise<void> {
  await db.deliveries.update(id, { status: 'delivered', deliveredAt: new Date() })
}

export async function markFailed(id: string, error: string): Promise<void> {
  await db.deliveries.update(id, { status: 'failed', lastError: error })
}

export async function deliverWebhook(d: Delivery): Promise<void> {
  const res = await post(d.url, d.payload, { timeout: 10_000 })
  if (!res.ok) {
    await markFailed(d.id, `HTTP ${res.status}`)
    return
  }
  await markDelivered(d.id)
}
"""

BACKOFF_TS = """// Full jitter: wait a random time up to the attempt's base delay.
export const MAX_ATTEMPTS = 6
const FIRST_DELAY = 30_000
const MAX_DELAY = 4 * 60 * 60 * 1000

export function nextDelay(attempt: number, rand = Math.random): number {
  const base = FIRST_DELAY * 4 ** (attempt - 1)
  return Math.floor(rand() * Math.min(base, MAX_DELAY))
}
"""

TEST_OUT = (
    "\x1b[2m> api@0.9.0 test\n> vitest run webhooks\x1b[22m\n\n"
    " \x1b[32m✓\x1b[39m src/webhooks/backoff.test.ts \x1b[2m(5 tests)\x1b[22m \x1b[33m4ms\x1b[39m\n"
    " \x1b[32m✓\x1b[39m src/webhooks/deliver.test.ts \x1b[2m(4 tests)\x1b[22m \x1b[33m31ms\x1b[39m\n\n"
    " \x1b[2mTest Files\x1b[22m  \x1b[1m\x1b[32m2 passed\x1b[39m\x1b[22m\x1b[90m (2)\x1b[39m\n"
    "      \x1b[2mTests\x1b[22m  \x1b[1m\x1b[32m9 passed\x1b[39m\x1b[22m\x1b[90m (9)\x1b[39m\n"
    "   \x1b[2mDuration\x1b[22m  612ms"
)

REPLY = """Failed deliveries now retry with exponential backoff and full jitter. After the sixth failed attempt the delivery is marked `failed`, with its last error.

| Attempt | Base delay | Sent within | Total so far |
| ---: | ---: | ---: | ---: |
| 1 | none | at once | 0 s |
| 2 | 30 s | 30 s | 30 s |
| 3 | 2 min | 2 min | 2.5 min |
| 4 | 8 min | 8 min | 10.5 min |
| 5 | 32 min | 32 min | 42.5 min |
| 6 | 2 h 8 min | 2 h 8 min | 2 h 50 min |

```ts
export function nextDelay(attempt: number, rand = Math.random): number {
  const base = FIRST_DELAY * 4 ** (attempt - 1)
  return Math.floor(rand() * Math.min(base, MAX_DELAY))
}
```

- **`deliver.ts`** keeps `attempts`, `last_error` and `next_attempt_at` on the delivery row.
- **The queue worker** picks up rows whose `next_attempt_at` has passed, so a restart loses no retries.
- All 9 webhook tests pass; one pins the schedule with a fixed random source.

The columns need a migration: `npm run migrate` adds them. I haven't run it."""


def webhook_retry(cwd, start):
    t = Transcript("webhook-retry", cwd, "webhook-retry", start)
    p = lambda f: os.path.join(cwd, f)
    t.prompt("Failed webhook deliveries are dropped after one try. Retry them with exponential "
             "backoff and jitter, at most 6 attempts, then mark the delivery failed. Show me the schedule.")
    m = "msg_01DemoWebhookRetry0001"
    u = usage(6, 412, 41210, 5120)
    t.assistant(m, [{"type": "thinking", "thinking": "The delivery loop lives in src/webhooks. I need to see how a delivery row stores its state before adding attempts, and whether the queue worker can pick rows up by time.", "signature": ""}], 7, u, thinking_ms=6800)
    tid = t.tool(m, u, "TodoWrite", todos([
        ("Read the delivery path", "Reading the delivery path", "in_progress"),
        ("Add backoff with jitter", "Adding backoff with jitter", "pending"),
        ("Retry from the queue worker", "Retrying from the queue worker", "pending"),
        ("Test the schedule", "Testing the schedule", "pending")]))
    t.result(tid, "Todos have been modified successfully.", {"oldTodos": [], "newTodos": []}, 1)
    tid = t.tool(m, u, "Read", {"file_path": p("src/webhooks/deliver.ts")})
    numbered = "".join(f"{i:6}\t{line}\n" for i, line in enumerate(DELIVER_TS.splitlines(), 1))
    t.result(tid, numbered, {"type": "text", "file": {"filePath": p("src/webhooks/deliver.ts"), "content": DELIVER_TS,
             "numLines": DELIVER_TS.count("\n"), "startLine": 1, "totalLines": DELIVER_TS.count("\n")}}, 1)
    tid = t.tool(m, u, "Grep", {"pattern": "deliverWebhook|markFailed", "output_mode": "files_with_matches"})
    files = ["src/webhooks/deliver.ts", "src/webhooks/worker.ts", "src/webhooks/deliver.test.ts"]
    t.result(tid, "Found 3 files\n" + "\n".join(p(f) for f in files), {
        "mode": "files_with_matches", "filenames": [p(f) for f in files], "numFiles": 3}, 1)
    m = "msg_01DemoWebhookRetry0002"
    u = usage(4, 1630, 46350, 2210)
    tid = t.tool(m, u, "Write", {"file_path": p("src/webhooks/backoff.ts"), "content": BACKOFF_TS}, 18)
    t.result(tid, "File created successfully at: " + p("src/webhooks/backoff.ts"), {
        "type": "create", "filePath": p("src/webhooks/backoff.ts"), "content": BACKOFF_TS, "structuredPatch": []}, 1)
    old = "\n".join(l[1:] for l in DELIVER_PATCH[0]["lines"] if l[0] in " -")
    new = "\n".join(l[1:] for l in DELIVER_PATCH[0]["lines"] if l[0] in " +")
    tid = t.tool(m, u, "Edit", {"file_path": p("src/webhooks/deliver.ts"), "old_string": old, "new_string": new}, 9)
    t.result(tid, "The file " + p("src/webhooks/deliver.ts") + " has been updated successfully.", {
        "filePath": p("src/webhooks/deliver.ts"), "oldString": old, "newString": new,
        "structuredPatch": DELIVER_PATCH, "userModified": False, "replaceAll": False}, 1)
    tid = t.tool(m, u, "TodoWrite", todos([
        ("Read the delivery path", "Reading the delivery path", "completed"),
        ("Add backoff with jitter", "Adding backoff with jitter", "completed"),
        ("Retry from the queue worker", "Retrying from the queue worker", "completed"),
        ("Test the schedule", "Testing the schedule", "in_progress")]), 14)
    t.result(tid, "Todos have been modified successfully.", {"oldTodos": [], "newTodos": []}, 1)
    m = "msg_01DemoWebhookRetry0003"
    u = usage(4, 220, 52040, 1180)
    tid = t.tool(m, u, "Bash", {"command": "npm test -- webhooks", "description": "Run the webhook tests"}, 6)
    t.result(tid, TEST_OUT, {"stdout": TEST_OUT, "stderr": "", "interrupted": False, "isImage": False}, 4)
    m = "msg_01DemoWebhookRetry0004"
    u = usage(4, 30, 53360, 640)
    tid = t.tool(m, u, "TodoWrite", todos([
        ("Read the delivery path", "Reading the delivery path", "completed"),
        ("Add backoff with jitter", "Adding backoff with jitter", "completed"),
        ("Retry from the queue worker", "Retrying from the queue worker", "completed"),
        ("Test the schedule", "Testing the schedule", "completed")]), 2)
    t.result(tid, "Todos have been modified successfully.", {"oldTodos": [], "newTodos": []}, 1)
    t.assistant("msg_01DemoWebhookRetry0005", [{"type": "text", "text": REPLY}], 12, usage(2, 486, 54020, 210))
    t.lines[-1]["message"]["stop_reason"] = "end_turn"
    t.done(84000, 1)
    return t


def failed_page(cwd, start):
    t = Transcript("failed-page", cwd, "failed-deliveries-page", start)
    p = lambda f: os.path.join(cwd, f)
    t.command("effort", "high", "Set effort level to high: Comprehensive implementation with extensive testing and documentation")
    t.prompt("Add a Failed deliveries page to the dashboard: the dead-lettered webhooks with their "
             "last error and a Retry button. Keep the dev server running so I can look.", 20)
    m = "msg_01DemoFailedPage00001"
    u = usage(6, 380, 38900, 4410)
    t.assistant(m, [{"type": "thinking", "thinking": "Two unknowns: how the dashboard fetches deliveries, and whether the API can requeue one. Both are read-only searches, so two Explore agents can run at once while the dev server starts.", "signature": ""}], 5, u, thinking_ms=4200)
    tid = t.tool(m, u, "TodoWrite", todos([
        ("Start the dev server", "Starting the dev server", "completed"),
        ("Find how the dashboard reads deliveries", "Finding how the dashboard reads deliveries", "in_progress"),
        ("Add a requeue endpoint", "Adding a requeue endpoint", "pending"),
        ("Build the Failed deliveries page", "Building the Failed deliveries page", "pending"),
        ("Test the page and the endpoint", "Testing the page and the endpoint", "pending")]))
    t.result(tid, "Todos have been modified successfully.", {"oldTodos": [], "newTodos": []}, 1)
    tid = t.tool(m, u, "Bash", {"command": "npm run dev", "description": "Start the dev server", "run_in_background": True}, 1)
    t.result(tid, "Command running in background with ID: b7k2m9q. Output is being written to: /tmp/claude/tasks/b7k2m9q.output",
             {"stdout": "", "stderr": "", "interrupted": False, "isImage": False, "backgroundTaskId": "b7k2m9q"}, 1)
    m = "msg_01DemoFailedPage00002"
    u = usage(4, 520, 44120, 900)
    a1 = t.tool(m, u, "Agent", {"description": "Find the dashboard's delivery queries", "subagent_type": "Explore",
                                "prompt": "In web/src, find where the dashboard fetches webhook deliveries and how a page gets its data. Report files and function names."}, 2)
    a2 = t.tool(m, u, "Agent", {"description": "Find how the API could requeue a delivery", "subagent_type": "Explore",
                                "prompt": "In src/webhooks and src/server, find what a requeue endpoint would need: the delivery row's columns, the worker's query, the router. Report files and line numbers."}, 0)
    found = "The dashboard fetches deliveries with `useDeliveries(status)` in web/src/api/deliveries.ts, a SWR hook over GET /api/deliveries?status=. Pages live in web/src/pages and register in web/src/routes.tsx."
    t.result(a1, [{"type": "text", "text": found}],
             {"status": "completed", "agentId": "a1demo", "totalDurationMs": 21400, "totalTokens": 18320,
              "totalToolUseCount": 6, "content": [{"type": "text", "text": found}]}, 22)
    t.assistant("msg_01DemoFailedPage00003", [{"type": "text", "text": "The dashboard already filters deliveries by status through `useDeliveries(status)`, so the page can ask for `failed` and needs no new query. Waiting on the second agent for the requeue side."}], 3, usage(2, 64, 46010, 520))
    sub = Transcript("failed-page", cwd, "failed-deliveries-page", t.t - datetime.timedelta(seconds=24), sidechain="a2demo")
    sub.prompt("In src/webhooks and src/server, find what a requeue endpoint would need: the delivery row's columns, the worker's query, the router. Report files and line numbers.")
    sm = "msg_01DemoFailedSub00001"
    su = usage(8, 140, 12400, 3100)
    s = sub.tool(sm, su, "Grep", {"pattern": "next_attempt_at", "path": p("src")}, 3)
    sub.result(s, "Found 2 files", {"mode": "files_with_matches", "filenames": [p("src/webhooks/worker.ts"), p("src/db/schema.ts")], "numFiles": 2}, 1)
    s = sub.tool(sm, su, "Read", {"file_path": p("src/webhooks/worker.ts")}, 2)
    sub.result(s, "", {"type": "text", "file": {"filePath": p("src/webhooks/worker.ts"), "content": "", "numLines": 61, "startLine": 1, "totalLines": 61}}, 1)
    s = sub.tool(sm, su, "Read", {"file_path": p("src/server/router.ts")}, 2)
    sub.result(s, "", {"type": "text", "file": {"filePath": p("src/server/router.ts"), "content": "", "numLines": 140, "startLine": 1, "totalLines": 140}}, 1)
    sub.tool(sm, su, "Grep", {"pattern": "router\\.(post|get)\\('/api/deliveries", "path": p("src/server")}, 2)
    return t, sub, a2

PLAN_MD = """# Multi-currency invoices

## Goal
Invoices carry a currency. Totals, PDFs and exports use it.

## Steps
1. Migration 0006: add `currency` to `invoices` and backfill `USD`.
2. Migration 0007: add `invoice_lines.currency`, copied from the invoice.
3. `totals.ts`: sum per currency; reject mixed lines.
4. `pdf.ts`: format amounts with `Intl.NumberFormat`.
5. CSV export: add a `currency` column after `amount`.

## Out of scope
- Converting between currencies.
- Changing a sent invoice's currency.

## Tests
- A EUR invoice renders `€1.234,50` in the de-DE locale.
- Mixed-currency lines fail with 422.
- Old invoices read back as USD.

## Rollout
Ship behind `invoices.multi_currency`, on for new accounts first.
"""

PLAN_PATCH = [
    {"oldStart": 6, "oldLines": 7, "newStart": 6, "newLines": 10, "lines": [
        " ## Steps",
        "-1. Migration 0006: add `currency` to `invoices` and backfill `USD`.",
        "-2. Migration 0007: add `invoice_lines.currency`, copied from the invoice.",
        "-3. `totals.ts`: sum per currency; reject mixed lines.",
        "+1. Migration 0006: add `currency` to `invoices` with default `USD`, then",
        "+   backfill, so old rows never fail NOT NULL.",
        "+2. Migration 0007: add `invoice_lines.currency`, copied from the invoice.",
        "+3. Both migrations get a down step: `npm run migrate:down 0006`.",
        "+4. `totals.ts`: sum per currency; reject mixed lines. Integer cents are",
        "+   a separate ticket, #212.",
        "-4. `pdf.ts`: format amounts with `Intl.NumberFormat`.",
        "-5. CSV export: add a `currency` column after `amount`.",
        "+5. `pdf.ts`: format amounts with `Intl.NumberFormat`.",
        "+6. CSV export: add a `currency` column after `amount`.",
        " ",
    ]},
]

CODEX_PANE = "41d2e7c06a9f3b28"


def talk(cwd, start):
    t = Transcript("talk", cwd, "currencies", start)
    p = lambda f: os.path.join(cwd, f)
    header = lambda: f'[pitwall: message from codex in #6.2; reply with: pitwall send @{CODEX_PANE} "..."]\n'
    t.prompt("Plan multi-currency invoices in plan.md, then have Codex critique it until you both agree.")
    m = "msg_01DemoTalk0000000001"
    u = usage(6, 900, 30100, 3800)
    tid = t.tool(m, u, "Write", {"file_path": p("plan.md"), "content": PLAN_MD}, 16)
    t.result(tid, "File created successfully at: " + p("plan.md"), {
        "type": "create", "filePath": p("plan.md"), "content": PLAN_MD, "structuredPatch": []}, 1)
    tid = t.tool(m, u, "Bash", {"command": "pitwall split -- codex", "description": "Open Codex beside this pane"}, 3)
    t.result(tid, "#6.2", {"stdout": "#6.2", "stderr": "", "interrupted": False, "isImage": False}, 1)
    ask = "Critique plan.md. Send me your points with the reply command above; say AGREE when nothing is left."
    tid = t.tool(m, u, "Bash", {"command": f"pitwall send '#6.2' -- \"{ask}\"", "description": "Ask Codex for a critique"}, 2)
    t.result(tid, "queued for codex", {"stdout": "queued for codex", "stderr": "", "interrupted": False, "isImage": False}, 1)
    t.assistant("msg_01DemoTalk0000000002", [{"type": "text", "text": "plan.md is written and Codex has it. I'll wait for its points."}], 2, usage(2, 40, 34000, 300))
    t.lines[-1]["message"]["stop_reason"] = "end_turn"
    t.done(31000, 1)
    t.prompt(header() + "1. Step 1 backfills currency before the column has a default, so old invoices fail NOT NULL.\n"
             "2. No down migration for 0007_invoice_lines.\n3. totals.ts still sums floats; rounding drifts.", 64)
    m = "msg_01DemoTalk0000000003"
    u = usage(4, 610, 36200, 1400)
    t.assistant(m, [{"type": "text", "text": "1 and 2 are right. 3 is real but its own ticket."}], 4, u)
    old = "\n".join(l[1:] for l in PLAN_PATCH[0]["lines"] if l[0] in " -")
    new = "\n".join(l[1:] for l in PLAN_PATCH[0]["lines"] if l[0] in " +")
    tid = t.tool(m, u, "Edit", {"file_path": p("plan.md"), "old_string": old, "new_string": new}, 6)
    t.result(tid, "The file " + p("plan.md") + " has been updated successfully.", {
        "filePath": p("plan.md"), "oldString": old, "newString": new,
        "structuredPatch": PLAN_PATCH, "userModified": False, "replaceAll": False}, 1)
    msg = "Revised: default first, down migration added, rounding split out. Left?"
    tid = t.tool(m, u, "Bash", {"command": f"pitwall send @{CODEX_PANE} -- \"{msg}\"", "description": "Send the revision to Codex"}, 2)
    t.result(tid, "queued for codex", {"stdout": "queued for codex", "stderr": "", "interrupted": False, "isImage": False}, 1)
    t.assistant("msg_01DemoTalk0000000004", [{"type": "text", "text": "plan.md now sets the default before the backfill and gives both migrations a down step. Rounding is real but its own ticket, #212. Codex has the revision."}], 1, usage(2, 12, 38100, 200))
    t.lines[-1]["message"]["stop_reason"] = "end_turn"
    t.done(16000, 1)
    t.prompt(header() + "AGREE. Ship it.", 40)
    m = "msg_01DemoTalk0000000005"
    u = usage(4, 300, 39000, 900)
    t.assistant(m, [{"type": "text", "text": "Codex agrees. Writing migration 0007 now."}], 3, u)
    t.tool(m, u, "Write", {"file_path": p("migrations/0007_invoice_lines.sql"),
                           "content": "ALTER TABLE invoice_lines ADD COLUMN currency text;\n"}, 2)
    return t


def main():
    if len(sys.argv) != 5:
        sys.exit(USAGE)
    out, retry, page, cur = sys.argv[1:]
    old = "\n".join(l[1:] for l in DELIVER_PATCH[0]["lines"] if l[0] in " -")
    edited = "\n".join(l[1:] for l in DELIVER_PATCH[0]["lines"] if l[0] in " +")
    files = {"repo/src/webhooks/deliver.ts": DELIVER_TS,
             "retry/src/webhooks/deliver.ts": "import { MAX_ATTEMPTS, nextDelay } from './backoff'\n" + DELIVER_TS.replace(old, edited),
             "retry/src/webhooks/backoff.ts": BACKOFF_TS,
             "cur/plan.md": PLAN_MD.replace(*("\n".join(l[1:] for l in PLAN_PATCH[0]["lines"] if l[0] in k) for k in (" -", " +")))}
    for name, body in files.items():
        os.makedirs(os.path.dirname(os.path.join(out, name)), exist_ok=True)
        with open(os.path.join(out, name), "w") as f:
            f.write(body)
    now = datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0)
    webhook_retry(retry, now - datetime.timedelta(minutes=14)).write(os.path.join(out, "webhook-retry.jsonl"))
    t, sub, tool = failed_page(page, now - datetime.timedelta(minutes=2))
    t.write(os.path.join(out, "failed-page.jsonl"))
    agents = os.path.join(out, "failed-page", "subagents")
    sub.write(os.path.join(agents, "agent-a2demo.jsonl"))
    with open(os.path.join(agents, "agent-a2demo.meta.json"), "w") as f:
        json.dump({"agentType": "Explore", "toolUseId": tool, "description": "Find how the API could requeue a delivery"}, f)
    talk(cur, now - datetime.timedelta(minutes=3)).write(os.path.join(out, "talk.jsonl"))


if __name__ == "__main__":
    main()
