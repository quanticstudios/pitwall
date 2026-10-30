# Session delta fixtures

Each `*.json` file is one recorded managed session. A delta consumer in any language replays `deltas` in order and must end with a snapshot deep-equal to `snapshot`. The TypeScript types and the reference `applyDelta` live in `src/shared/session-delta.ts`.

| Key             | Contents                                                                                   |
| --------------- | ------------------------------------------------------------------------------------------ |
| `description`   | What the session does.                                                                     |
| `mode`          | The provider, `claude` or `codex`.                                                         |
| `prompt`        | The user prompt sent after attach.                                                         |
| `runtimeEvents` | The provider runtime events main received. Only aide's parity test reads these.            |
| `deltas`        | The delta stream. The first one is always a `snapshot`.                                    |
| `snapshot`      | The last snapshot main published.                                                          |

`src/main/session-delta-fixtures.test.ts` feeds `runtimeEvents` through the real `ManagedSessionManager` and checks that it publishes `snapshot`. `src/shared/session-delta.test.ts` checks that `deltas` reduce to it. Random ids are renumbered to `00000000-0000-4000-8000-<n>` in order of first appearance.

## Envelope

Every delta carries:

- `version`: the schema version, `1`. A consumer refuses any other value and asks for a fresh snapshot.
- `sessionId`: the session the delta belongs to.
- `seq`: 1 more than the previous delta for this session. Any other step is a gap, and the consumer asks for a fresh snapshot.
- `revision`, `updatedAt`: the snapshot's `revision` and `updatedAt` after the delta applies. Main publishes several deltas under one revision when one snapshot publish changed several things.
- `type`: one of the types below.

## Types

A delta that names an item looks it up by `content[].id`. An unknown id means the consumer is out of sync.

| Type                 | Fields                                                             | Effect on the snapshot                                                                                                        |
| -------------------- | ------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------- |
| `snapshot`           | `snapshot`                                                         | Replaces everything. Accepted at any `seq`, which becomes the new base.                                                       |
| `item.add`           | `item`                                                             | Appends `item` to `content`.                                                                                                  |
| `item.text.append`   | `itemId`, `text`, `itemUpdatedAt`                                  | Appends `text` to the item's `text` and sets its `updatedAt`.                                                                 |
| `item.patch`         | `itemId`, `patch`                                                  | Shallow-merges `patch` into the item.                                                                                         |
| `tool.start`         | `item` (with `tool`)                                               | Appends `item` to `content`.                                                                                                  |
| `tool.output.append` | `itemId`, `text`, `itemUpdatedAt`                                  | Appends `text` to `tool.output` (absent counts as empty) and sets the item's `updatedAt`.                                     |
| `tool.end`           | `itemId`, `status`, `exitCode?`, `startedAt`, `endedAt`            | Sets `tool.status`, `tool.exitCode` when present, and the item's `updatedAt` to `endedAt`.                                    |
| `reasoning.append`   | `itemId`, `text`, `itemUpdatedAt`                                  | Same as `item.text.append`, on a reasoning item.                                                                              |
| `plan.update`        | `itemId`, `tasks`, `itemUpdatedAt`                                 | Replaces `tool.tasks` and sets the item's `updatedAt`.                                                                        |
| `approval.add`       | `approval`                                                         | Appends to `pendingApprovals`, then recomputes `planApproval`.                                                                |
| `approval.resolve`   | `requestId`                                                        | Removes that approval, then recomputes `planApproval`.                                                                        |
| `input.add`          | `input`                                                            | Appends to `pendingUserInputs`.                                                                                               |
| `input.resolve`      | `requestId`                                                        | Removes that input.                                                                                                           |
| `turn`               | `turn`                                                             | Replaces the turn with the same `id` in `turns`, or appends it.                                                               |
| `status`             | `status`, `sessionState`, `running`, `detail`, `lastCompletedAt`   | Sets the last four. `status` is a coarse label and does not touch the snapshot.                                               |
| `usage`              | `usage`                                                            | Sets `usage`.                                                                                                                 |
| `usage.reports`      | `usageReports`, `usageSummary`                                     | Sets both.                                                                                                                    |
| `error`              | `lastError`, `lastErrorAt`                                         | Sets both.                                                                                                                    |
| `session.patch`      | `set`, `unset?`                                                    | Shallow-merges `set` into the snapshot and deletes each key in `unset`. An empty `set` only moves `revision` and `updatedAt`. |

`planApproval` is the first pending approval whose `requestType` is `plan-approval`, as `{ detail, id }`, or `null`.

`status` maps from `sessionState`: `connecting` and `running` are `working`, `waiting` is `waiting`, `ready` is `done`, `stopped` is `interrupted`, `error` is `error`.
