# Session event contract (format version 1)

The daemon owns a versioned, harness-agnostic session contract in
`daemon/internal/session` (`FormatVersion = 1`). Adapters normalize
harness-native records into `session.Entry{Kind, Name?, Payload, Display}`;
the store persists `kind`, `format_version`, and `display_json` with each
event; the UI consumes `display` verbatim and never re-derives titles,
statuses, or roles from payload keys.

## Envelope

`store.Event` JSON:

| field | meaning |
|---|---|
| `sequence` | SQLite monotonic cursor; SSE `id` |
| `id` | daemon event ID (random hex, not a native session UUID) |
| `task_id`, `phase_id?`, `attempt_id?`, `artifact_id?`, `branch_id?`, `parent_event_id?` | lineage |
| `kind` | `message` \| `tool_call` \| `process_start` \| `process_end` \| `phase_start` \| `phase_end` \| `intervention` \| `plan_feedback` \| `custom` |
| `format_version` | contract version (1) |
| `name?` | tool name for `tool_call`, empty otherwise |
| `payload` | typed per-kind object (snake_case) |
| `display` | daemon-computed `{role, status, title, target?, result?, preview?, duration_ms?}` |
| `available_actions?`, `token_count?`, `started_at`, `ended_at?` | delivery metadata |

`GET /tasks/{id}/events` returns
`{"events": [...], "cursor": N, "format_version": 1}`.
`GET /tasks/{id}/sessions` returns a bare array of tasks extended with
`agent_sessions` per item (see `AgentSession` in Swagger).

Native UUIDs (Pi `--session-id`) identify
the harness-native conversation. Daemon event `id` values identify folded
contract events. Folding many source records into one event (and omitting
partial/thinking records) means native `uuid`/`parentUuid`/`message.id` never
map one-to-one onto `parent_event_id`; exact native-tree navigation is out of
scope.

## Kind payloads

- `message`: `{role, text, stop_reason?, model?, usage?}` with
  `Usage{input, output, cache_read?, cache_write?, reasoning?, total_tokens}`.
- `tool_call` (folded, one entry per tool): `{tool_call_id, tool, arguments
  (bounded valid JSON, 16 KiB), result? (16 KiB), success? *bool,
  incomplete?, started_at?, ended_at?, duration_ms?}`. Completed calls carry
  `success: true/false`; missing outcomes carry `success` unset and
  `incomplete: true`, never a fabricated failure. Missing source timing stays
  absent.
- `process_start`: `{pid, command}`; `process_end`: `{pid, exit_code,
  duration_ms}`.
- `phase_start` / `phase_end`: `{phase, name?, kind?, owner?, status?, error?,
  input_snapshot?, output_snapshot?}`.
- `intervention`: `{actor, intent, text, delivery, intervention_id?,
  target_type?, target_id?}`.
- `plan_feedback`: `{feedback, actor?, plan_digest?}`.
- `custom`: `{custom_type, data (bounded)}`.

Bounded JSON stays marshalable: oversized objects become
`{truncated: true, preview: "..."}` within the limit; the full record stays in
the raw audit file. String truncation preserves UTF-8. Tool display lookup
normalizes case without changing the payload.

## Display derivation (single deterministic point)

| kind | role | status | title | target | result/preview |
|---|---|---|---|---|---|
| message user | user | neutral | "User message" | — | text / first line |
| message assistant | agent | failure if stop_reason ∈ {error, aborted} else neutral | "Agent response" | — | text / first line |
| message system | system | neutral | "System message" | — | text / first line |
| tool_call | tool | success/failure when success set; neutral if incomplete | toolTitles[tool] | toolTarget(arguments) | result / first line |
| process_start | event | neutral | "Agent process started" | — | command |
| process_end | event | success if exit_code==0 else failure | "Agent process finished" | — | — |
| phase_start | event | running | "Attempt started" | payload.name | — |
| phase_end | event | success/failure from payload.status | "Attempt finished" | payload.name | error |
| intervention | user | neutral | "Intervention <Intent>" | — | text |
| plan_feedback | user | neutral | "Planner feedback" | — | feedback |
| custom | event | neutral | displayName(custom_type) | — | bounded data |

## Pi mapping (`daemon/internal/harness/pi`)

Source: deterministic Pi JSONL session format
(https://pi.dev/docs/latest/session-format).

| Source | Normalized behavior |
|---|---|
| `process_start` / `process_end` | `NewProcessStart` / `NewProcessEnd` |
| `message_start`, `message_update`, `tool_execution_update` | dropped (partial state) |
| `tool_execution_start` buffered; `tool_execution_end` folded | one `tool_call` with `folded` dedupe |
| `message_end` assistant terminal | `message/assistant` with text, stop reason, model, usage |
| `message_end` user / system | `message/user` / `message/system` |
| `message_end` toolResult | folded into `tool_call` (either ordering, exactly once) |
| unknown record | `custom/<type>` with bounded whole object |

`tool_execution_end` vs `message_end`-toolResult ordering is not guaranteed;
the `folded` flag (not map deletion) dedupes. Raw JSONL is mirrored
append-only; `result.Text` and usage accumulation are unchanged.

## Custom / incomplete policy

Unknown harness entries become generic `custom` events; raw harness JSONL stays
on disk for audit. Thinking/redacted content is excluded before every
bounding/preview path (raw audit files unchanged and private). External tool
references stay bounded references; missing files yield
reference/unavailable, never an adapter crash.

## Versioning

`format_version: 1`. Schema changes are clean breaks: `incompatibleSchema`
gates on required columns via `pragma_table_info` before schema exec and
surfaces the existing `ErrStateIncompatible` path. No migrations.
