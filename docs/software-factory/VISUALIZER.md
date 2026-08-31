# Software Factory Visualizer

Reference inspiration: `disler/super-simple-software-factory/.claude/skills/sssf/apps/visualizer`.

The Visualizer is a local-first, read-only observability UI for Software Factory Campaigns. It makes Planner → Builder → Reviewer → Tester execution understandable while agents are running and after they finish.

## 1. Goals

The Visualizer MUST show:

- Campaign list and status.
- Developer request and selected Domain Profile.
- Phase progress and repair loops.
- Parallel agent lanes.
- Repository/work-item assignments.
- Agent harness, model, reasoning level, session ID, context use, tokens, cost, and duration.
- Tool-call spans with redacted arguments/results.
- Structured Agent Results.
- Review findings.
- Check/gate evidence.
- Approvals, waivers, blockers, budgets, and unresolved decisions.
- Git branches, commits, and draft PR links.
- Contract, generated-artifact, traffic, and deployment dependency graphs.
- Live updates while the controller writes events.

The Visualizer MUST NOT authorize, modify, retry, merge, deploy, or otherwise control a Campaign. Workflow mutations remain in the controller/CLI approval interface.

## 2. MVP architecture

Use the reference implementation’s simple shape:

```text
Factory controller / Pi event tracer
              │ writes
              ▼
      campaign SQLite database (WAL)
              │ read-only polling
              ▼
       Visualizer JSON server
              │ HTTP
              ▼
          Vue/Vite SPA
```

Recommended stack:

- TypeScript.
- Vue 3 and Vite.
- A small Node/Bun-compatible HTTP server.
- SQLite read connection with WAL and busy timeout.
- Polling with monotonic event-row cursor; no WebSocket requirement for MVP.

The Visualizer lives in the future factory repository under:

```text
apps/visualizer/
├── package.json
├── index.html
├── server/
├── shared/
└── src/
```

It is distributed with the factory but runs as a separate process.

## 3. Data ownership

The controller owns Campaign state and writes events. The Visualizer derives presentation state and performs no workflow writes.

If archive/triage state is needed, store it in a separate visualizer-preferences database or browser storage. It MUST NOT update Campaign workflow tables.

The visualizer server opens the Campaign database read-only and verifies WAL mode. Concurrent agent/controller writes must not block normal reads.

## 4. Observable model

### 4.1 Campaign

```ts
type CampaignStatus =
  | "received"
  | "planning"
  | "awaiting_plan_approval"
  | "building"
  | "reviewing"
  | "repairing_review"
  | "testing"
  | "repairing_test"
  | "opening_prs"
  | "validating_ci"
  | "implementation_complete"
  | "deploying"
  | "verifying"
  | "soaking"
  | "shipped"
  | "blocked"
  | "paused"
  | "failed"
  | "aborted";
```

Summary fields:

- Campaign ID and title.
- Profile ID/version.
- Developer/requester and owner.
- Request revision/hash.
- Risk level/signals.
- Current status.
- Start/update/end times.
- Cost/token/budget totals.
- Repository and PR counts.
- Blocking finding/check/unresolved counts.

### 4.2 Phase

A phase belongs to one Campaign and has:

- Stable phase ID.
- Sequence and attempt.
- Kind: `developer`, `planner`, `builder`, `reviewer`, `tester`, `controller`, `delivery`.
- Work-item/repository owner when applicable.
- Status: `queued`, `running`, `passed`, `failed`, `blocked`, `cancelled`.
- Start/end timestamps, retry count, and redacted error.

### 4.3 Agent session

- Role and assignment.
- Pi session ID.
- Provider/model/reasoning level.
- Tool allow-list.
- Worktree/repository identity.
- Context tokens/window.
- Input, output, cache read/write, reasoning tokens, and cost.
- Start/last-used/end time.

### 4.4 Event

Events use monotonic insertion order and parent IDs for nested spans:

```text
phase_start | phase_end
agent_start | agent_end
model_request | model_response
tool_start | tool_end
agent_result
finding
check_start | check_end
approval_requested | approval_recorded
budget_warning
external_effect
log | error
```

Payloads are versioned JSON and redacted before persistence.

### 4.5 Agent Result and checks

The UI displays [AGENT_RESULT.schema.json](AGENT_RESULT.schema.json) records with schema-validity status. It shows checks as evidence-bearing items, not only green/red badges:

- Check ID and kind.
- Required/optional.
- Executor and source SHA.
- Attempt and duration.
- Passed/failed/deferred/waived.
- Evidence links and concise redacted notes.

## 5. Views

### 5.1 Campaign list

Display recent active Campaigns first with:

- Overall status.
- Profile.
- Risk.
- Mini phase-progress dots.
- Agent status dots.
- Repositories.
- Elapsed time, cost, and budget percentage.
- Blocker count.

Filters:

- Profile.
- Status.
- Repository.
- Risk.
- Owner/requester.
- Date range.
- Active/archived UI preference.

### 5.2 Campaign trace

The primary detail screen is a timeline with lanes:

```text
Developer      request ───────── approval
Planner                  ███████
Builder/app                        █████████
Builder/lib                        █████████████
Builder/mesh                       ██████
Reviewer/spec                                    ████
Reviewer/contract                                ██████
Builder/repair                                          ███
Tester                                                       ███████
Controller      state/effect markers ───────────────────────────────
```

Selecting a phase shows inputs, exact role prompt metadata, Agent Result, changed files, decisions, findings, checks, and artifacts.

### 5.3 Dependency graph

Show repository work items and edges for:

- Contract publication.
- Generation.
- Build.
- Review/test invalidation.
- Merge.
- Deployment.
- Cleanup.

Node state reflects queued/running/passed/blocked/failed. A profile may additionally render its own runtime traffic edges.

### 5.4 Review and test board

Provide focused lists of:

- Blocking findings awaiting repair.
- Findings resolved by each Builder head.
- Required checks grouped by work item/environment.
- Deferred checks and designated executor.
- Waivers with owner and expiry.
- Evidence freshness and SHA correlation.

### 5.5 Delivery view

For profiles with delivery providers, show the identity graph:

```text
source SHA
→ CI artifact
→ image/version
→ CD run
→ configuration/policy commits
→ runtime revision
→ smoke/trace/log/metric evidence
→ soak verdict
```

A profile may include generated artifacts, delivery state, and connectivity probes when those adapters exist.

## 6. API

MVP server endpoints:

```text
GET /api/health
GET /api/campaigns?limit=&profile=&status=
GET /api/campaigns/:campaign_id
GET /api/campaigns/:campaign_id/phases
GET /api/campaigns/:campaign_id/agents
GET /api/campaigns/:campaign_id/events?after=<rowid>&limit=<n>
GET /api/campaigns/:campaign_id/results
GET /api/campaigns/:campaign_id/checks
GET /api/campaigns/:campaign_id/findings
GET /api/campaigns/:campaign_id/dependencies
GET /api/campaigns/:campaign_id/delivery
GET /api/campaigns/:campaign_id/agents/:run_id/prompts
```

Responses set `Cache-Control: no-store`. IDs used in filesystem paths must match a strict safe-segment expression. Every list endpoint has a bounded limit.

The events endpoint returns:

```ts
type EventsPage = {
  events: FactoryEvent[];
  cursor: number;
  hasMore: boolean;
};
```

The client polls using the returned cursor. It backs off while a Campaign is idle or terminal.

## 7. Prompt and tool visibility

Exact prompts and tool arguments are sensitive. Defaults:

- Prompt bodies hidden unless explicitly enabled by local policy.
- Secrets and PII redacted before database write.
- Tool arguments/results shown as redacted summaries.
- Full command output linked only when its evidence classification permits the viewer.
- Authorization headers, tokens, and environment secrets never rendered.
- The UI visibly marks missing, redacted, truncated, and legacy fields.

The visualizer server MUST NOT perform redaction as the only defense; persisted observable data must already be safe.

## 8. Security

- Bind to loopback by default.
- No external listener without explicit authentication/TLS configuration.
- Read-only Campaign database connection.
- Strict path-segment and path-root validation.
- Bounded query/page sizes.
- No raw SQL/query passthrough.
- No workflow mutation endpoints.
- Content Security Policy and safe Markdown rendering.
- External links use safe rel attributes.
- Displayed JSON is escaped and size-limited.
- Visualizer access is audited when hosted.

## 9. Resilience and compatibility

- Optional/new database columns degrade to `null` rather than breaking old runs.
- Event payloads carry a schema version.
- The server tolerates agents still running and incomplete rows.
- Unknown event/profile types render generically.
- The UI computes durations/progress rather than storing derived state.
- A missing session artifact returns an explicit unavailable state, not a server error.
- Database migration is owned by the controller/tracer, never by the read-only Visualizer.

## 10. Visualizer checks

Required tests:

- Database query and cursor pagination tests.
- Concurrent WAL writer/read tests.
- API path-traversal and input-bound tests.
- Redaction fixtures for secrets.
- Vue component tests for all statuses and missing fields.
- Timeline lane/layout tests for parallel Builders and repair loops.
- Golden tests for Campaign list, trace, findings, checks, dependency, and delivery views.
- Compatibility tests against old/new event database versions.
- Build, typecheck, lint, and accessibility checks.

## 11. MVP acceptance criteria

1. A running Campaign appears without restarting the UI.
2. Planner, parallel Builders, Reviewers, Tester, and repair loops render as distinct lanes/phases.
3. Tokens, cost, model, context use, tool spans, Agent Results, findings, and checks are inspectable.
4. Event polling is cursor-based and does not block controller writes.
5. The Visualizer cannot mutate workflow state.
6. Secret/PII fixtures never appear in API responses or rendered output.
7. Campaign dependency and runtime traffic graphs are understandable from the UI.
8. Historical Campaign databases remain readable after additive schema evolution.
