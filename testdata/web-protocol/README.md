# Web protocol conformance fixtures

One file per fake provider scenario (`internal/session/fake/testdata`),
named after it. pitwall owns them; aide vendors them with a checksum, so
the Go and TypeScript sides of the [web protocol](../../docs/web-protocol.md)
test against the same streams.

| Key             | Contents                                                                 |
| --------------- | ------------------------------------------------------------------------ |
| `scenario`      | The fake script's name.                                                  |
| `provider`      | `claude` or `codex`: the script's mode, else `claude`.                   |
| `prompt`        | The message sent; `""` for `fail-start`, which never gets one.           |
| `runtimeEvents` | The events the fake emitted, one per line.                               |
| `deltas`        | The delta stream a web client receives, one per line, seq 1 a snapshot.  |
| `snapshot`      | The snapshot `deltas` end on.                                            |

`TestConformance` in `internal/session/fake` replays each script on a fixed
clock, allowing every approval and answering every input, then closes the
driver. It numbers `Reduce` over the events as the chat loop does: a new
chat's snapshot at seq 1, the `user-prompt` item and `working` status after
the provider's start events, one revision per event. The loop's coalescing
of appends is left out, because it depends on wall time. `perf-stream`
runs for 2 seconds instead of aide's 150. `fail-start` has the chat loop's
start error as its one event.

`SHA256SUMS` lists each file's SHA-256, in `sha256sum` format; aide's
vendor script fetches it and checks every file it downloads against it.

The test fails when a file or `SHA256SUMS` differs by a byte, and when a
file's deltas, applied in order, do not end on its snapshot. After a
change to the fake, `Reduce` or the delta types, rewrite the files and
review the diff:

```sh
go test ./internal/session/fake -run TestConformance -update
```

aide's CI fails until its vendored copy is updated from `main`.
