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

Native UUIDs (Claude `--session-id` / `--resume`, Pi `--session-id`) identify
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
normalizes case and covers Claude names (`Bash`, `Read`, `Edit`, `Write`,
`Glob`, `Grep`, `WebFetch`, `WebSearch`, `Agent`) without changing the payload.

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

## Claude saved-transcript mapping (`transcript.go`)

Pinned reference: community JSONL schema at
https://github.com/weirdgiraffe/claude-code-sessions-explorer/blob/f993edf8c845b36fa0277f849644f42b431f27cf/docs/JSONL-SCHEMA.md
(reference only, not an official transport spec; tolerate unknown fields).

| Source | Normalized behavior |
|---|---|
| `user.message.content` string or text blocks, `isMeta != true` | `message/user`; non-tool text preserved when mixed with results |
| `user` with `isMeta=true` | `custom/claude.meta_user`, never a human message |
| `assistant.message.content` text blocks | `message/assistant` with block order, model, final stop reason, deduped usage |
| assistant `tool_use` + user `tool_result` | one folded `tool_call` in session/subagent scope; `is_error` decides success |
| result content string/object/block array | bounded text; stdout/stderr/interrupted and structured Agent results supported |
| unmatched tool use at end | incomplete `tool_call`, unknown success |
| orphan tool result | `custom/claude.orphan_tool_result` with correlation ID |
| `thinking` / redacted thinking | omitted everywhere, including nested custom data |
| `system` `local_command` with text | `message/system` |
| `system` `turn_duration` or other subtype | `custom/claude.system.<subtype>` |
| `permission-mode`, `attachment`, `file-history-snapshot`, `custom-title`, `agent-name`, `last-prompt` | `custom/claude.<type>` |
| `progress`, sidechain, nested subagent records | scoped custom metadata; no main-chat attribution |
| `summary`, `queue-operation`, unrecognized types/blocks | bounded custom; no silent loss except partial/thinking |

Notes: native `uuid` dedupes records; `message.id` + session/agent scope
groups an API response for usage (final usage once per message, cache
read/write mapped separately, `total = input + output + cache_read +
cache_write`); transcript replay leaves USD cost unknown and accounting
incomplete; partial/final snapshots sharing `message.id` collapse repeated
prefixes; subagent files archive but replay as scoped custom entries, not
per-role sessions.

## Claude live stream mapping (`stream.go`)

Sources: official programmatic usage (https://code.claude.com/docs/en/headless),
CLI reference (https://code.claude.com/docs/en/cli-reference), cost tracking
(https://code.claude.com/docs/en/agent-sdk/cost-tracking). Verified baseline:
`claude --version` 2.1.263 on 2026-09-08 (flags, not auth/proof of billing).

- `session_id` / `parent_tool_use_id` are transport fields, not transcript
  `sessionId` / `parentUuid`. Startup hook/system records may precede
  `system/init`; init is not guaranteed first.
- `system/init` captures session/model metadata → bounded `claude.init`
  custom; never a timeline message.
- Main-loop assistant/user blocks feed shared normalization; `stream_event`
  partial deltas are dropped; non-null `parent_tool_use_id` becomes scoped
  `claude.subagent` custom and never overwrites the root response.
- Block fragments sharing message IDs are deduplicated by record UUID;
  tool IDs emit once; reception time is the live tool timing.
- `result` is the terminal outcome, not another assistant message. Root result
  text becomes `harness.Result.Text`. Error subtype / `is_error`, missing
  result, nonzero exit, session mismatch, or malformed stream return an error
  even with text; denials surface as bounded `claude.error_result` /
  `claude.permission_denial` customs.
- `total_cost_usd` is the single-invocation estimate, added once across
  resume invocations. `modelUsage` whole-tree totals are preferred over root
  `usage` (never both). Context window comes from selected-model metadata;
  cumulative tokens are not occupancy. Placeholder per-message usage is
  omitted; failures without terminal totals keep known input/cache counts,
  mark incomplete, and fabricate nothing.

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
