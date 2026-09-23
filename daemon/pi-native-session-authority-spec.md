# Spec: Make Pi sessions authoritative for agent history and consolidate execution

## Problem Statement

The daemon maintains three overlapping modules — `harness`, `agentexec`, and `stagekit` — that duplicate each other's work: both `agentexec.RunTurn` and `stagekit.Drain` implement invocation accounting, repository read-only checks, session identity checks, envelope persistence, and JSON-correction retries. On top of that, the daemon copies agent conversation history into its own SQLite journal, even though Pi already persists every session natively as a tree of message, tool-call, usage, and prompt entries. The result is duplicated state, duplicated logic, and a journal that can drift from what the agent actually said and did.

## Solution

Treat Pi's native sessions as the authoritative record of agent history, usage, and published reports. The daemon keeps only a minimal workflow store (Task/Attempt state, approvals, checks, snapshots, durable message inbox, and a lightweight timeline reference index) that points into native session entries instead of copying their payloads. Execution is consolidated into one loop owned by the harness layer; `agentexec` is deleted; `stagekit` keeps workflow lifecycle and delegates execution to the harness. The existing HTTP/UI contract, workflow semantics, and Task transition policy are preserved.

## User Stories

1. As a factory operator, I want agent conversations stored once in Pi sessions, so that the factory and the agent never disagree about what was said or done.
2. As a factory operator, I want usage and cost derived from Pi session totals, so that there is one source of truth for what was billed, including tool and compaction usage.
3. As a factory operator, I want published agent reports read from the native assistant entry, so that the report a human approved is exactly the bytes the agent produced.
4. As a task author, I want to see the same live stream of agent messages and tool calls while work runs, so that my experience does not change.
5. As a task author, I want completed history served from the durable Pi journal, so that I see the settled record rather than a re-derived copy.
6. As a task author, I want exact retry to branch the conversation from the Attempt's input checkpoint, so that each Attempt is faithful to its recorded inputs and earlier paths remain navigable.
7. As a task author, I want Attempt boundaries visible in the conversation, so that I can tell which Attempt produced which response.
8. As a reviewer, I want human messages still target stable factory event and attempt identities, so that targeting does not change.
9. As a reviewer, I want a human-approved plan's digest still bound to the exact report content, so that approval cannot be satisfied by a different response.
10. As a daemon operator, I want the existing numeric event cursor and replay contract preserved, so that the application UI keeps working without changes.
11. As a daemon operator, I want accepted-but-undelivered messages to remain durable in the factory store, so that they survive a crash even though Pi's queue is memory-only.
12. As a daemon operator, I want in-flight work reconciled from native session entries after a restart, so that completed Turns are not re-run and lost Turns are re-driven.
13. As a daemon operator, I want a restarted Task to enter the explicit-resume (blocked) state as before, so that control-plane behavior is unchanged.
14. As a maintainer, I want one execution loop instead of two, so that read-only enforcement, accounting, session identity, and correction retries are fixed once and fixed everywhere.
15. As a maintainer, I want `agentexec` gone, so that prompt rendering, envelope helpers, and execution mechanics each have a single home.
16. As a maintainer, I want the harness layer to own the correction/repair loop driven by a stage-supplied validator, so that stages only describe what a valid envelope is.
17. As a maintainer, I want the factory Pi extension to correlate each request with its native subtree, so that a factory request id maps to the exact entries it produced.
18. As a maintainer, I want prompt history to come from Pi's native system-message entries, so that the factory stops writing its own prompt audit files.
19. As a maintainer, I want `config_snapshot` kept as frozen Task configuration, so that reproducibility and recipient resolution still work without the orchestrator.
20. As a maintainer, I want a clean-break rollout that refuses incompatible existing state, so that half-normalized, half-native Tasks never exist.

## Implementation Decisions

- **Ownership.** Pi native sessions are authoritative for agent conversation, tool results, structured prompt/tool changes, usage/cost, and published agent reports. SQLite remains authoritative for workflow state: Task/Attempt identity, approvals, checks, snapshots, the durable message inbox, orchestration commands, and the timeline reference index.
- **Integration.** The integration is Pi-first. The `harness.Harness` interface becomes session-oriented (start/resume/fork a session, prompt with follow-ups, read native entries and stats, observe settled) rather than a lowest-common-denominator abstraction over multiple agents.
- **Session granularity.** One native session per Task + stage, reused across correction turns, message continuations, and Attempts. Exact retry forks that session at the Attempt's input checkpoint entry and labels Attempt boundaries.
- **Process lifetime.** One Pi process per actively executing stage; it exits when that execution completes or pauses and is reopened from the native session when needed. Conversations persist in the session file across process exits.
- **Correlation.** A factory-generated `requestId` is written into a `factory-request` custom entry's data immediately before the user message is sent, so the request subtree is resolvable by `parentId`. Entry IDs are not used as keys (unstable across re-creation).
- **Extension.** A small factory Pi extension ships with the daemon, exposing a single `/factory-run` command that appends the `factory-request` entry (plus attempt label) and sends the message; it reconstructs its index on `session_start`. Go never writes to a live session file.
- **Execution consolidation.** `agentexec` is deleted. Prompt rendering moves to `stagekit`; envelope helpers move to the `stage` package; the shared correction/repair loop moves into the harness layer, driven by a stage-supplied validator and correction instructions.
- **Reference index.** A lightweight timeline keeps ordering, ownership, and native entry references. Factory event IDs remain the targeting identity; agent-derived events carry native entry references resolved at read time. Numeric cursors and replay order are preserved.
- **Reports.** Agent reports are resolved from the native assistant entry (identity, digest, and exact entry reference). Factory-generated verification reports keep their own content.
- **Accounting.** Task usage/cost is derived from native session stats, reconciled idempotently. The per-invocation accounting machinery and the `accounting_complete` flag are removed.
- **Durability.** Existing SQLite tables remain the durable outbox/inbox. A request is recorded before dispatch and reconciled after `agent_settled` plus native-entry reconciliation. The pre-first-assistant in-memory window is accepted; the outbox re-drives until a durable marker exists.
- **Restart.** Keep the "blocked, resume explicitly" model; drop `accounting_complete` flagging; reconcile in-flight Turns from native sessions on resume.
- **Config.** `config_snapshot` is retained; the factory prompt audit (`system.md`/`user.md`) is dropped in favor of native prompt history. The dead `empty_turn_retries` config is removed.
- **Rollout.** Clean break: bump the session format version and require deleting the factory home; no migration.
- **Versioning.** Target Pi 0.87.0. No version gate is enforced; correct operation relies on operational discipline.

## Testing Decisions

A good test asserts external behavior through a seam, not implementation details: what a caller observes, not how the code produces it.

- **Harness seam.** Test the session-oriented harness adapter and the shared correction/repair loop with a scripted fake adapter (prior art: `errorScriptedHarness`, `liveEventFailureHarness` in the agent runner tests). Cover session identity preservation, read-only enforcement, accounting-from-stats, correction retries, and cancellation.
- **Workflow seam.** Test Task transition policy, exact-retry branching, message delivery/idempotency, and restart reconciliation through the existing pipeline and e2e task-flow tests (prior art: `TestMessageDrainReachesDownstream`, `TestCancellationPreventsLatePublication`, `TestCancellationBetweenStagesStopsDownstream`, `TestMessagesAreIdempotentFIFOAndAbortFailsQueue`).
- **API/contract seam.** Test numeric cursors, event targeting, report resolution, and SSE replay through the existing API and store tests (prior art: cursor/replay assertions in the API tests and the task-flow e2e test).
- **Store seam.** Test the reference index, outbox durability, and clean-break incompatibility through the existing store tests (prior art: `TestReserveAgentSessionConcurrentCallersShareWinner`, `TestMessageDeliveryAndEventRollbackTogether`, `TestOpenRecoversPendingAgentInvocation`).

## Out of Scope

- Multi-harness abstraction; the integration targets Pi only.
- Pi version enforcement at startup.
- Migrating existing normalized history; rollout is a clean break.
- Changing the Task transition policy, approval semantics, or the plan → build → verify → review pipeline order.
- Rewriting the application UI; the HTTP/UI contract is preserved.

## Further Notes

- Native sessions already live under the Task Workspace (`--session-dir` points there), so Task deletion removes them without extra work.
- Fork/clone copy native entry IDs verbatim, so `factory-request` correlation survives forking.
- The factory must not read `get_messages` as the journal; `get_entries` includes abandoned branches and pre-compaction history.
- Full durability and exactly-once semantics were deferred in design but are partially addressed by the outbox + native-reconciliation decisions above.

## Implementation Status

Legend: `[x]` complete, `[~]` partial, `[ ]` not started.

### Implementation Decisions

- [x] **Ownership.** Pi native sessions are authoritative for usage/cost and the
  published agent report; SQLite owns Task/Attempt, approvals, checks,
  snapshots, durable messages, orchestration commands, and the timeline.
  Conversation payloads are copied into the live event stream for streaming and
  then hydrated from the referenced native entry at read time (see Reference
  index).
- [x] **Integration.** `harness` is session-oriented: `Harness.Open(SessionSpec)`
  returns a `Session` that prompts with follow-ups, reads native entries and
  stats, resolves reports, and observes settlement. The Pi adapter drives
  `pi --mode rpc` over JSONL, so no lowest-common-denominator multi-agent
  abstraction remains.
- [x] **Session granularity.** One native session per Task + stage, reused
  across correction turns, message continuations, and Attempts (via
  `--session-id`). Exact retry records each Attempt's input checkpoint
  (`phases.native_base_entry_id`) and branches the native session back to it.
- [~] **Process lifetime.** One Pi process per native session, pooled and reused
  across prompts and stage executions, opened from the session file when
  missing. It is reaped on an idle timer rather than synchronously on
  stage completion or pause, so a paused stage's process may linger briefly.
- [x] **Correlation.** A factory-generated `requestId` is written into a
  `factory-request` custom entry immediately before the user message; the
  request subtree is resolved by `parentId`, never by entry id as a key.
- [x] **Extension.** `internal/harness/pi/extension.ts` ships embedded,
  installed to the factory home, and loaded with `-e`. `/factory-run` branches
  at the Attempt checkpoint with `ctx.navigateTree` when forking, appends the
  entry plus attempt label, and sends the message; it rebuilds its index on
  `session_start`. Go never writes to a live session file.
- [x] **Execution consolidation.** `agentexec` is deleted. Prompt rendering
  lives in `stagekit`, envelope helpers in `stage`, and the shared
  correction/repair loop plus invocation mechanics in `harness`.
- [x] **Reference index.** `events.native_entry_id` and
  `agent_sessions.last_entry_id` exist. Every streamed event carries its
  `events.request_id`; after a turn settles the harness correlates each
  agent-derived event with its native entry, and `internal/timeline` hydrates
  payloads from that entry at read time. Factory event ids remain the targeting
  identity and numeric cursors/replay order are unchanged.
- [x] **Reports.** The harness resolves the report text and its exact native
  entry id from the assistant entry for the request subtree. Reports validate and extract `report_markdown` from native text. Factory-generated verification reports keep their own content.
- [x] **Accounting.** Task usage/cost derives from native session stats and is
  reconciled idempotently (`FinalizeAgentInvocation` sets the absolute cost and
  adjusts the task total by the delta). `accounting_complete` and its
  per-invocation machinery are removed.
- [x] **Durability.** SQLite remains the durable outbox/inbox; the invocation
  marker (with its request and phase) is recorded before dispatch and settled
  after the turn plus native reconciliation. The outbox re-drives until a
  durable marker exists.
- [x] **Restart.** The blocked/explicit-resume model is preserved and
  `accounting_complete` flagging is gone. On startup
  `harness.ReconcilePendingTurns` adopts native usage/cost/leaf for in-flight
  sessions and returns their phases to the queue; `RunTurn` then either settles
  the recovered native response or releases the marker and re-drives the turn.
- [x] **Config.** `config_snapshot` retained; the `system.md`/`user.md` prompt
  audit is dropped; `empty_turn_retries` is removed from config, template, and
  swagger.
- [x] **Rollout.** `session.FormatVersion` is bumped to 2 and
  `incompatibleSchema` refuses the legacy accounting schema
  (`ErrStateIncompatible`), so the factory home must be deleted.
- [x] **Versioning.** Pi-only integration; no startup version gate.

### User Stories

- [x] 1 (single native conversation record), 9 (plan digest bound to report
  content), 10 (numeric cursor/replay), 13 (restart enters blocked),
  14/15/16 (one loop, `agentexec` gone, stage-supplied validator),
  17 (request→subtree correlation), 18 (prompt history native, no audit files),
  19 (`config_snapshot` kept), 20 (clean-break rollout).
- [x] 2 (usage is native from `get_session_stats`, including tool and
  compaction usage), 3 (report content and entry reference from the native
  assistant entry).
- [x] 5 (completed history is served from the durable native journal; the
  timeline hydrates agent events from the referenced native entries at read
  time).
- [x] 6 (retry restores the git/snapshot input and branches the native session
  at the Attempt's recorded input checkpoint).
- [x] 11 (accepted-undelivered messages are durable in SQLite), 12 (in-flight
  sessions are reconciled from native entries; completed turns are settled
  without re-prompting and lost turns are re-driven).
- [x] 4 (live stream unchanged), 7 (Attempt boundaries recorded via attempt
  labels in `factory-request`), 8 (message targeting unchanged).

### Deferred / Remaining Work

1. [x] Hydrate agent-derived timeline events from native entries at read time
   (`internal/timeline`) and correlate streamed events to native entry ids
   (`events.request_id` + post-turn native correlation).
2. [x] Fork the native session at the Attempt input checkpoint on exact retry
   (`phases.native_base_entry_id`/`fork_native`, extension `navigateTree`).
3. [x] Reconcile in-flight Turns from native session entries after restart
   (`harness.ReconcilePendingTurns` + `RunTurn` pending-turn reconciliation).
4. [x] Persistent per-stage process over Pi RPC (`harness.Harness.Open` →
   `pi --mode rpc`), pooled per native session and reused across prompts and
   stage executions. Process lifetimes track sessions rather than individual
   prompts; idle sessions are reaped by timer.

### Verification

All repository checks pass with this change set: `go -C daemon test ./...`,
`go -C daemon test -race ./...`, `npm --prefix application run typecheck`, and
`npm --prefix application run build`.
