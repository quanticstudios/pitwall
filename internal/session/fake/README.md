# Scripted managed providers

`fake.New(Options{Clock: clock, Script: &script})` implements
`session.Provider` without spawning an agent. Driver, eventlog and protocol
consumers can share `Load(name)` or decode an aide fixture into `Script`.
Inject `Clock` and `Options.NewID` to replay the same timestamps and IDs.
Tests advance their clocks without sleeping.

The daemon's provider registry can opt in once at startup:

```go
providers = fake.ProvidersFromEnv(providers, func(message string) {
    log.Print(message)
})
```

Only `PITWALL_FAKE_PROVIDERS=1` replaces Claude and Codex. It emits a warning
and has the same behavior in test and release binaries. There is no automatic
fake mode. The socket/chat-loop ticket owns the call site; this package does
not change real drivers or storage.

`PITWALL_FAKE_SCRIPT=<name>` selects a script when the next chat starts.
Otherwise a matching prompt selects the script. Multiword aide names take
precedence over the spec's first-word fallback. Unknown prompts reply with
`Handled: <prompt>`. Context appended by aide is removed before selection.
`PITWALL_FAKE_SCRIPT=fail-start` refuses Start with a `ProviderError`.

A JSON script has `mode`, `prompt`, and `runtimeEvents`, like aide's fixtures.
An empty mode works for both providers. Each runtime event may add:

- `delayMs`: wait on the injected clock before emitting the event.
- `answers`: pause after opening an approval or input request. `{}` accepts
  any response. `{"allow": true}` checks a decision;
  `{"input": {"question-id": ["answer"]}}` checks input answers.
- `repeat` and `events`: repeat an event group, substituting `${index}` or
  `${paddedIndex}`. This keeps 2,000-item transcripts small on disk.

`${provider}` and `${prompt}` substitute string values without breaking JSON
escaping. Runtime item, tool and request IDs are prefixed per turn. Turn IDs
use a fresh namespace per Start, so resume never reuses an old ID. Explicit
fixture timestamps are preserved; absent ones come from the clock. Consumers
must enqueue events from the callback, rather than synchronously calling a
driver method inside it. Close waits for emissions to finish.

The bundled aide names, from `FakeManagedSessionBackend` in
`src/main/managed-session-manager.ts`, are:

```text
approval                 edit approval          plan approval
input                    tools                  todos
subagent                 long transcript        long transcript wait
perf-stream              stream-lines           stream-codex
wait-claude              wait-codex             fail-claude
fail-codex               edit readme            fail-start
fleet plan               fleet consult
```

`edit readme` appends `edited by the turn\n` to the chat's existing README.md,
as aide does. Fleet prompts reply with aide's text, but do not invoke fleet
orchestration, which the managed-session spec excludes. `todos` emits a
running call before the completed tasks so it exercises `plan.update`.
`reasoning`, `limit`, and `error` add the ticket's other event cases.

`perf-stream` accepts aide's existing environment knobs, sampled at Start:
`AIDE_FAKE_STREAM_TOKENS_PER_SECOND` (50), `AIDE_FAKE_STREAM_SECONDS` (150),
`AIDE_FAKE_STREAM_TOOL_EVERY` (150), and `AIDE_FAKE_STREAM_TOOL_MIX`
(`Bash,Edit,Read`). A nonpositive tool interval or empty mix disables tools.
Direct callers use `Options.Stream` instead of process environment variables.
Real-driver tests still use fake CLI binaries, as the spec requires.
