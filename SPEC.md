# Software Factory Go Rewrite Specification

## 1. Objective

Replace the current TypeScript orchestration, CLI, and server with a small Go application. The application starts with:

```bash
go run cmd/main.go
```

It serves a loopback-only HTTP API and the existing Vue visualizer. The API is the only control surface: the Vue application uses it, and there is no replacement CLI.

The factory coordinates one Task Workspace containing one or more repositories through:

```text
draft → preparing → planning → awaiting_plan_approval
      → building → checking → reviewing → completed
```

A failed operation moves the Task to `blocked`. Active work can be paused, resumed, or aborted.

Pi is the only implemented coding harness. It must be invoked through the installed `pi` command, not its Node SDK. The harness boundary must allow a future Codex or Claude adapter without changing orchestration.

## 2. Guiding constraints

- Prefer standard-library Go and explicit code over frameworks.
- Keep one Go process, one SQLite database, and one active Task at a time.
- Keep the existing Vue visual language and adapt it to the new API.
- Keep durable facts in SQLite WAL and JSONL; do not build a second in-memory read model.
- Let deterministic Go code own sequencing, process lifecycle, validation, checks, and acceptance. Agents propose; code disposes.
- Treat the rewrite as a clean break. Existing Task databases and CLI compatibility are out of scope.
- Do not merge, push, deploy, or automatically commit repository changes.

## 3. Scope

### In scope

- Go HTTP server and embedded Vue production assets.
- Global factory state under `~/.software-factory`.
- Generated agent roster and editable prompt templates.
- Tasks targeting one or more local Git or GitHub repositories.
- A private Task Workspace containing isolated repository materializations.
- Planner, Builder, deterministic checks, and Reviewer phases.
- Manual plan approval through the API/UI.
- Pi JSON event-stream execution, persistent Pi sessions, typed result envelopes, and bounded correction retries.
- SQLite/JSONL tracing, live event SSE, process tracking, usage, and cost.
- Pause, resume, abort, explicit deletion, and stale-process recovery.

### Plan feedback

Planner plans include a required `questions` array. Empty questions permit approval; non-empty questions require feedback and a revision before approval. Feedback is stored with actor, timestamp, referenced plan digest, and text, then continues the existing Planner session.

### Out of scope

- A factory CLI.
- Multiple active Tasks.
- Automatic review-repair loops.
- Pull requests, merges, pushes, releases, deployments, or rollbacks.
- Remote or multi-user API access.
- Automatic cleanup.
- Migration of old `.software-factory` data.
- Working Codex or Claude adapters.

## 4. Terminology

**Task**: The requested outcome, repository inputs, and durable execution history.

**Task Workspace**: The private filesystem root containing repository materializations, attempts, snapshots, sessions, and artifacts for one Task.

**Attempt**: One durable execution of a phase definition, owned by the factory, an agent role, Git, or a deterministic check runner.

**Envelope**: A role-specific, schema-validated JSON result parsed from an agent's final assistant message.

**Repository Profile**: The Task-pinned repository root, source identity, base SHA, checks, generated paths, protected paths, and applicable `AGENTS.md` instructions.

**Harness**: An adapter that runs a coding agent and normalizes its models, output, events, process lifecycle, and usage for orchestration.

## 5. Runtime and filesystem

The default root is `~/.software-factory`. `SOFTWARE_FACTORY_DIR` overrides it, including in tests.

```text
~/.software-factory/
├── config.yaml
├── factory.db
├── server.lock
├── prompts/
│   ├── planner/{system,user}.md
│   ├── builder/{system,user}.md
│   └── reviewer/{system,user}.md
└── tasks/
    └── <task-id>/
        ├── task.json
        ├── workspace/repositories/<name>/
        ├── repository-profiles/<name>.json
        ├── attempts/
        ├── snapshots/
        ├── artifacts/
        ├── config-snapshot.yaml
        ├── events.jsonl             # normalized durable trace
        ├── prompts/<role>/          # exact rendered prompt audit copies
        └── sessions/<role>/
            ├── raw-output.jsonl     # verbatim Pi JSON event output
            └── pi/                  # Pi-managed JSONL sessions
```

Requirements:

- Create state directories with mode `0700` and sensitive regular files with mode `0600`, subject to a stricter umask.
- Never expose arbitrary files beneath this root through a static file route.
- Acquire `server.lock` at startup. If its recorded process is live, fail with a clear message. Recover a stale lock safely.
- Permit only one active Task. Draft, terminal, paused, and blocked Tasks do not consume the active slot.
- Task IDs use `SF-YYYYMMDD-<8 random lowercase hex characters>` generated with `crypto/rand`.

## 6. Startup

`go run main.go` must:

1. Resolve and secure the factory directory.
2. Acquire the single-server lock.
3. Generate missing config and prompt templates without overwriting existing files.
4. Open/migrate `factory.db`, enable WAL, set `synchronous=NORMAL`, enable foreign keys, and set a busy timeout.
5. Mark orphaned `running` processes and active phases as ended/failed, then move affected Tasks to `blocked`.
6. Validate config and probe `pi --list-models`.
7. Start `127.0.0.1:8080` unless `PORT` overrides the port.
8. Serve embedded Vue assets and `/api/v1` routes.
9. Log startup, validation state, HTTP requests, Task transitions, and child-process lifecycle with `log/slog`.

Supported environment variables are:

- `PORT`
- `LOG_LEVEL`
- `PI_PATH` (default `pi`)
- `SOFTWARE_FACTORY_DIR`

The server may start in a degraded state when config or Pi validation fails. Health and config endpoints expose the errors, while Task creation/start is rejected until they are fixed.

## 7. Generated configuration

The generated YAML is the default source of agent role configuration. A Task snapshots the resolved config and rendered prompts when it starts. `POST /api/v1/tasks` may override `coding_agent`, `model`, and `thinking` for the Task; API requests cannot override prompts, colors, or purpose.

Keep this shape:

```yaml
defaults:
  coding_agent: pi
  model: cursor/gpt-5.6-luna
  thinking: medium

observability:
  poll_ms: 500

runtime:
  agent_deadline_ms: 1800000
  empty_turn_retries: 2
  json_fix_attempts: 2

agents:
  - name: planner
    model: cursor/gpt-5.6-sol
    thinking: high
    color: "#a78bfa"
    purpose: Turn a request into a plan the builder can implement without asking questions.
    prompt_engineering:
      system: prompts/planner/system.md
      user: prompts/planner/user.md

  - name: builder
    model: cursor/gpt-5.6-luna
    thinking: medium
    color: "#22d3ee"
    purpose: Implement the approved plan exactly and report every changed file.
    prompt_engineering:
      system: prompts/builder/system.md
      user: prompts/builder/user.md

  - name: reviewer
    model: cursor/gpt-5.6-luna
    thinking: low
    color: "#fb7185"
    purpose: Review the implementation against the request, approved plan, and check evidence without changing files.
    prompt_engineering:
      system: prompts/reviewer/system.md
      user: prompts/reviewer/user.md
```

Rules:

- Resolve prompt paths relative to `config.yaml`.
- Resolve missing per-role values from `defaults`.
- Require exactly one `planner`, `builder`, and `reviewer`.
- Accept thinking levels `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, and `max`.
- Validate configured models against the selected harness model catalog (`pi --list-models` for Pi).
- Require `coding_agent` to name a registered harness (`pi` in this version; `codex` may be added later without changing orchestration); report unknown names as unsupported rather than silently selecting Pi.
- Do not restrict harness tools: never pass a `--tools` allowlist so agents use all available harness tools.
- `GET /api/v1/config` returns the resolved configuration and validation errors, not prompt bodies.

Use `gopkg.in/yaml.v3`; do not hand-write a YAML parser.

## 8. Task API lifecycle

### Create a draft

`POST /api/v1/tasks` stores metadata only. It must not clone, fetch, create a worktree, or start Pi.

Local repository request:

```json
{
  "request": "Implement feature X",
  "repositories": [
    {"name": "app", "type": "local", "path": "/absolute/path/to/repository", "primary": true}
  ],
  "coding_agent": "pi",
  "model": "cursor/gpt-5.6-luna",
  "thinking": "medium"
}
```

GitHub repository request:

```json
{
  "request": "Implement feature X",
  "repositories": [
    {"name": "app", "type": "github", "repo": "owner/repository", "primary": true}
  ]
}
```

Use strict decoding and reject unknown fields. Local paths must be absolute, names unique, and exactly one repository primary. `coding_agent`, `model`, and `thinking` are optional task-level overrides applied to every role at start; an empty value inherits the server config. `coding_agent` must name a registered harness (`pi` today, `codex` later), `thinking` must be a valid level, and `model` must be non-empty when provided. Task creation allocates the Task Workspace; repository access occurs at start.

### Start a Task

`POST /api/v1/tasks/{id}/start` returns `202 Accepted` after atomically claiming the active-Task slot. A background goroutine then prepares the repository and runs planning.

- Local source: resolve symlinks, identify the canonical Git root, pin current `HEAD`, and run `git worktree add --detach <task repository path> <sha>`.
- GitHub source: validate `owner/repository`, then run `gh repo clone <owner/repository> <task repository path>`. The fresh clone is the isolated workspace; do not add another worktree. Pin the checked-out default-branch `HEAD`.
- Do not share clone caches or automatically fetch existing clones.
- Trace Git/`gh` commands as deterministic phases with PID, command, bounded output, timestamps, and exit code.
- Failure in preparation blocks the Task before Pi starts.

### Approval and continuation

Planning ends in `awaiting_plan_approval`.

`POST /api/v1/tasks/{id}/approve` records actor, timestamp, plan-envelope digest, and approval, then asynchronously runs Builder, checks, and Reviewer. It returns `202 Accepted`.

`POST /api/v1/tasks/{id}/feedback` accepts `{ "feedback": "...", "current_plan_digest": "..." }` and returns `202 Accepted`. The optional digest prevents feedback against a stale revision. The Task returns to `planning`, then `awaiting_plan_approval` after a valid revision. Approval is rejected while questions remain and is bound to the latest plan digest. Abort remains available.

### Pause, resume, and abort

- `POST /pause` is valid for active states. Terminate the active child with `SIGTERM`, wait a short bounded interval, then use `SIGKILL` if needed. Persist the previous state and mark the Task `paused`.
- `POST /resume` is valid for `paused` or recoverable `blocked` Tasks. Re-enter the interrupted phase using the same role Pi session ID and Task workspace.
- `POST /abort` terminates active children and marks the Task `aborted`. Preserve trace data and repository workspace.
- A server restart never silently resumes a Task.

### Delete

`DELETE /api/v1/tasks/{id}` works only for inactive Tasks. In one coordinated operation, remove related database records, Task files, Pi sessions, and the Git worktree/clone. For a local worktree, invoke `git worktree remove --force` before deleting residual files.

## 9. Repository profile and checks

At Task start, create and persist an immutable Repository Profile.

If the target has a marked Software Factory block in `AGENTS.md`, it is authoritative:

````markdown
<!-- software-factory:start -->
```yaml
checks:
  - id: unit
    command: go test ./...
generated:
  - web/dist/
protected:
  - internal/store/schema.sql
```
<!-- software-factory:end -->
````

The implementation must support fenced or bare YAML between the markers and validate unique non-empty check IDs and commands. Paths must be repository-relative and cannot escape the repository.

If the marked block is absent, detect checks without modifying the repository:

- `package.json` scripts: `test`, `typecheck`, and `lint`.
- `go.mod`: `go test ./...`.
- `pyproject.toml` indicating pytest: `python -m pytest`.
- `Cargo.toml`: `cargo test`.

If no checks are declared or detected, block before starting Pi and show the reason in the API/UI.

Run each check as a separate deterministic phase through `/bin/sh -c` in the Task repository. Apply a timeout, stream lifecycle events, save bounded output in SQLite, and save full output as a Task artifact. A required failed check blocks the Task before Reviewer starts.

Pi loads the repository's normal `AGENTS.md` context. Start Pi with project approval so its ordinary trusted project resources, user skills, and extensions remain available. Agents use all available harness tools.

## 10. Harness boundary

Keep the generic boundary equivalent to:

```go
type Harness interface {
    Models(context.Context) ([]Model, error)
    Run(context.Context, Request, EventSink) (Result, error)
}
```

The exact Go types may differ, but the boundary must preserve these rules:

- `Request` contains cwd, prompt, system prompt, explicit model, thinking level, session ID, session directory, deadline, and raw-output path.
- `EventSink` receives normalized lifecycle/tool/usage events as execution happens.
- `Result` contains final assistant text, exit status, session ID, model/provider, accumulated usage/cost, and latest valid context occupancy.
- Orchestration depends on `Harness`, not `PiHarness`.
- A small registry selects a harness by `coding_agent` (`pi` today, `codex` later).
- Tests use a fake Harness; they do not require provider credentials.

Do not introduce an SDK-style abstraction over prompts, sessions, or messages beyond what orchestration currently consumes.

## 11. Pi adapter

### Model catalog

Run `pi --list-models`, parse its provider/model/context-window rows, and resolve configured patterns exactly as follows:

1. An exact `provider/model` match wins.
2. A unique exact model-ID match wins.
3. A unique substring match wins.
4. No match or multiple matches is a validation error.

Cache the catalog briefly in process; provide an explicit refresh path for health/config reads if needed. Do not read or recreate Pi's provider registry directly.

### Agent invocation

For every initial call and correction call, spawn a normal one-shot Pi process in the Task repository:

```text
$PI_PATH -p --mode json
  --provider <provider>
  --model <model-id>
  --thinking <level>
  --session-id <stable-role-session-id>
  --session-dir <task-role-pi-session-dir>
  --system-prompt <rendered-system-prompt>
  --approve
  <rendered-user-prompt>
```

Implementation requirements:

- Use an argument vector, never construct this command through a shell.
- Set stdin to a closed/null stream so Pi cannot wait for piped input.
- Read stdout incrementally using strict LF-delimited JSONL semantics; tolerate CRLF by stripping one trailing `\r`.
- Append every stdout line to `raw-output.jsonl` and flush it before processing the event.
- Capture bounded stderr separately.
- Register the child PID before reading output and mark it ended after `Wait` returns.
- Enforce the configured wall-clock deadline with context cancellation and process-group termination.
- The last non-empty assistant `message_end` text is the result text.
- Accumulate usage and cost from assistant `message_end` events. Preserve Pi's `input`, `output`, `cacheRead`, `cacheWrite`, `reasoning`, `totalTokens`, and cost fields.
- Update context occupancy only from the last valid assistant turn whose stop reason is not `aborted` or `error`.
- A non-zero exit with no assistant text is an execution error that includes the bounded stderr tail.

Use one stable Pi session ID per Task role. JSON correction retries and resumed phases invoke a new Pi process with the same session ID and session directory.

### Tool-event normalization

Pi emits announcement, start, update, and end records for a tool call. Store one normalized `tool_call` trace event only when `tool_execution_end` arrives:

- Correlate by `toolCallId`.
- Preserve tool name and bounded arguments.
- Generate a short label from `command`, `path`, `file_path`, `pattern`, `query`, or `url`.
- Preserve start/end timestamps, duration, success, and a bounded result snippet.
- Keep intermediate updates only in raw Pi JSONL.

## 12. Typed envelopes

Prompts must require the final assistant response to contain only the applicable JSON object. Parse a fenced JSON object or the outermost JSON object as a tolerant first pass, then validate the exact shape.

Common fields:

```json
{
  "status": "success",
  "summary": "...",
  "artifacts": [],
  "notes_for_next_agent": "..."
}
```

Planner extension (questions is required):

```json
{
  "questions": [],
  "steps": [
    {
      "id": "step-1",
      "description": "...",
      "expected_files": ["..."],
      "acceptance_criteria": ["..."]
    }
  ]
}
```

Builder extension:

```json
{
  "changed_files": ["path/to/file"],
  "commit_message": "suggested message only"
}
```

Reviewer extension:

```json
{
  "approved": true,
  "findings": [
    {"requirement": "...", "met": true, "evidence": "path or observation"}
  ],
  "blocking": []
}
```

Rules:

- Validate required fields, enum values, arrays, and nested objects in Go.
- Persist every parse attempt to `envelopes`, including invalid raw-output tails.
- On invalid JSON, invoke Pi again with the same session ID and a correction requesting only the expected object.
- Permit at most `runtime.json_fix_attempts` correction calls after the initial call.
- Feed the approved Planner envelope to Builder. Planner revisions continue the stable Planner session with the current plan, questions, and feedback.
- Feed the request, approved plan, deterministic check evidence, and changed-file facts to Reviewer.
- Verify Builder `changed_files` against Git rather than trusting it; persisted changed-file facts come from `git diff` and untracked-file inspection.
- Reviewer approval must be internally consistent: `approved: true` cannot have blocking items or unmet findings.

## 13. Orchestration and acceptance

States:

- `draft`
- `preparing`
- `planning`
- `awaiting_plan_approval`
- `building`
- `checking`
- `reviewing`
- `completed`
- `blocked`
- `paused`
- `aborted`

Persist and validate allowed transitions in one state-machine module. Every phase starts pessimistically and earns success only after its operation and validation complete.

A Task is `completed` only when all are true:

1. Repository preparation succeeded.
2. Planner returned a valid successful envelope.
3. A human approved the exact plan-envelope digest.
4. Builder returned a valid successful envelope.
5. Builder did not change protected paths.
6. Every configured deterministic check passed.
7. Reviewer returned a valid envelope with `status: success`, `approved: true`, no blocking items, and no unmet findings.

Otherwise the Task becomes `blocked`, preserving the failed phase, error, process output, and workspace. There is no waiver that converts a failed required check into a pass.

Planner and Reviewer are expected to be read-only. Compare Git state before and after each call; any new repository change from either role blocks the Task. Generated paths remain visible in the trace but can be identified as generated in diff results.

## 14. Trace and database

Use `database/sql` with a pure-Go SQLite driver. The schema must be explicit SQL migrations, not an ORM.

The initial model follows the referenced SSSF tracer:

### `tasks`

Task ID, request, repository source type/value, submitted and canonical paths, base SHA, state, previous state, active phase, error, config snapshot, plan digest, approval actor/time, total usage/cost, and created/started/ended timestamps.

### `attempts`

Phase ID, Task ID, sequence, name, kind, owner, description, status, attempt, retries, error, and start/end timestamps.

### `events`

An autoincrement sequence cursor, stable event ID, Task ID, phase ID, parent event ID, type, name, JSON payload, token count, and start/end timestamps. The sequence is the SSE cursor.

### `envelopes`

Envelope ID, Task ID, phase ID, agent role, output type, payload JSON, validity, attempt, and creation timestamp.

### `checks`

Check ID, Task ID, phase ID, name, command, attempt, status, exit code, bounded output, full-output artifact path, duration, and timestamps.

### `processes`

Process row ID, Task ID, phase ID, kind (`git`, `gh`, `pi`, or `check`), name, PID, display command, status, exit code, and start/end timestamps.

### `agent_sessions`

Task ID plus role primary key, harness, provider, model, thinking, color, Pi session ID, session directory, context tokens/window, accumulated usage/cost, and created/last-used timestamps.

Use foreign keys with cascading deletion where appropriate. Keep SQL writes short. Every trace event is appended to the Task's normalized `events.jsonl` as it happens and mirrored to SQLite. SQLite is the API/UI query source; JSONL is the raw durable record.

## 15. HTTP API

All routes are under `/api/v1` and return JSON errors with a stable `code` and human-readable `message`.

### System

- `GET /health`
- `GET /config`
- `GET /models`
- `GET /control` — returns whether control is enabled and the per-process mutation token to same-origin clients

### Task commands

- `POST /tasks`
- `POST /tasks/{id}/start`
- `POST /tasks/{id}/approve`
- `POST /tasks/{id}/pause`
- `POST /tasks/{id}/resume`
- `POST /tasks/{id}/abort`
- `DELETE /tasks/{id}`

### Task reads

- `GET /tasks`
- `GET /tasks/{id}`
- `GET /tasks/{id}/attempts`
- `GET /tasks/{id}/events`
- `GET /tasks/{id}/events/stream`
- `GET /tasks/{id}/results`
- `GET /tasks/{id}/checks`
- `GET /tasks/{id}/diff`
- `GET /tasks/{id}/interventions`
- `POST /tasks/{id}/interventions`

Requirements:

- Command endpoints return `202` for accepted asynchronous work, `200/201` for completed synchronous mutations, `409` for invalid state or active-Task conflicts, `422` for invalid config/repository input, and `404` for missing Tasks.
- SSE uses the SQLite event sequence as `id`, accepts `Last-Event-ID` and an `after` query cursor, emits heartbeats, and resumes without gaps or duplicates.
- Do not enable CORS.
- Bind only to `127.0.0.1`/loopback.
- Generate a random mutation token at startup. The embedded UI fetches it from the same origin and sends `X-Software-Factory-Token` on mutations.
- Reject mutation requests with a missing/invalid token or a foreign `Origin`.
- Add security headers and `Cache-Control: no-store` to API responses.
- Serve only embedded frontend assets outside `/api/v1` and use the SPA index as the route fallback.

## 16. Vue UI

Retain the current Vue UI's visual identity, layout, trace lanes, colors, icons, and detail views. Replace polling-only/read-only behavior with the new API.

Required interactions:

- Create a draft with request text and either local absolute path or GitHub `owner/repository`.
- Start a draft and show repository preparation progress.
- Inspect the generated plan and approve or abort it.
- Pause, resume, abort, and delete when valid for the current state.
- Show Task list/state, active phase, repository identity, worktree path, base SHA, agent lanes, normalized tool calls, checks, findings, usage/cost, process failures, and diff.
- Subscribe to SSE and reconnect from the last event cursor.
- Surface degraded health/config/model errors before Task start.
- Keep exact prompt bodies hidden; show role, path, digest, and timestamp only.

Keep Vue/Vite as a frontend-only toolchain. Commit production assets under `web/dist` and embed them with `go:embed`, so normal `go run main.go` does not require Node or Vite. UI development may continue to use Vite.

## 17. Logging and process safety

- Use `log/slog` human-readable text output by default.
- Include Task ID, phase ID, role, PID, state transition, duration, and error where applicable.
- Never log prompt bodies, API tokens, provider credentials, full tool output, or repository file contents.
- Run each child in its own process group on Unix so pause, abort, timeout, and shutdown terminate descendants.
- On graceful server shutdown, stop accepting commands, terminate active children, close their process records, block the Task, flush trace writes, release the lock, and close SQLite.
- Before killing a recorded stale PID, verify process identity. Startup recovery should normally mark stale records rather than kill unrelated recycled PIDs.

## 18. Source layout

Target layout:

```text
main.go
internal/
  api/
  config/
  factory/
  git/
  harness/
    pi/
  store/
  trace/
prompts/
  planner/
  builder/
  reviewer/
web/
  src/
  dist/
docs/
```

Keep interfaces at the HTTP, persistence, and harness boundaries. Keep orchestration in one factory module and transition policy in one file. Avoid dependency-injection frameworks, ORMs, routers, message brokers, and worker services.

Backend dependencies are limited to:

- `gopkg.in/yaml.v3`
- One pure-Go SQLite driver

Frontend dependencies needed by the retained Vue application may remain.

## 19. Deliverable sequence

Each deliverable must leave its own tests green. Do not defer all validation to the final step.

### Deliverable 1 — Go shell and global bootstrap

- Add `go.mod`, `main.go`, server options, signal handling, secure global directory creation, lock ownership, and `slog` configuration.
- Generate config and prompt templates without overwriting user files.
- Add a minimal health endpoint and embedded static-file serving.

Complete when a test with a temporary `SOFTWARE_FACTORY_DIR` proves first-run generation, idempotent restart, lock exclusion, and loopback serving.

### Deliverable 2 — Configuration and model catalog

- Parse and resolve the generated YAML.
- Validate roles, prompt paths, thinking levels, and harness names.
- Implement Pi model catalog parsing/resolution through an injected command runner.
- Expose health, config, and models reads.

Complete when table-driven tests cover defaults, malformed YAML, missing roles/prompts, exact and ambiguous model matching, degraded startup, and redaction of prompt bodies.

### Deliverable 3 — SQLite store and tracer

- Add migrations and models for Tasks, repositories, attempts, events, interventions, envelopes, checks, processes, and agent sessions.
- Enable WAL and implement normalized JSONL-plus-SQLite event writes.
- Implement read queries using stable event cursors.
- Add startup stale-run recovery.

Complete when tests prove persistence across reopen, WAL reads during writes, ordered cursors, parent spans, process closure, recovery to `blocked`, and cascading deletion.

### Deliverable 4 — Repository preparation

- Add strict Task draft creation.
- Implement local Git-root resolution and detached worktree creation.
- Implement fresh `gh repo clone` preparation.
- Parse authoritative `AGENTS.md` blocks and add fallback check detection.
- Persist the immutable Repository Profile and preparation traces.

Complete when integration tests with temporary Git repositories cover local paths, symlinks, invalid/non-Git paths, worktree isolation, missing checks, check detection, authoritative blocks, clone command construction, and preparation failure. Stub `gh`; tests must not require network access.

### Deliverable 5 — Harness contract and Pi adapter

- Define the generic Harness request/result/event types and registry.
- Implement Pi one-shot JSON mode, strict incremental JSONL reading, raw-output flushing, process tracking, tool-call folding, usage, deadline, cancellation, and stable sessions.
- Add a fake Harness for orchestration tests.

Complete when a fake `pi` executable proves exact arguments, closed stdin, streaming before exit, last-assistant-text selection, malformed-line tolerance, usage aggregation, tool normalization, non-zero exits, timeout termination, and session reuse.

### Deliverable 6 — Envelopes and orchestration

- Implement role envelope validators and bounded same-session JSON corrections.
- Implement the state machine and pessimistic phase primitive.
- Render and save prompts.
- Run Planner, approval stop, Builder, Git-derived change capture, deterministic checks, and Reviewer.
- Enforce acceptance and protected/read-only change rules.

Complete when fake-Harness tests cover the successful path and every blocking branch: invalid plan, exhausted correction retries, unapproved plan, Builder failure, protected change, failed check, Reviewer mutation, inconsistent verdict, and rejection.

### Deliverable 7 — Complete HTTP API and SSE

- Add command/read endpoints, strict bodies, status/error mapping, active-Task exclusion, mutation token, Origin checks, and SSE resume semantics.
- Implement pause, resume, abort, deletion, and graceful shutdown behavior.

Complete when `httptest` suites cover route contracts, authorization, state conflicts, asynchronous `202` behavior, SSE cursor reconnection, cancellation, stale recovery, and inactive-only deletion.

### Deliverable 8 — Vue integration

- Move/retain the existing Vue visualizer under `web` without redesigning its visual identity.
- Replace old API calls and polling with the new REST/SSE client.
- Add draft creation, repository input, start, approval, lifecycle controls, errors, checks, diff, and live traces.
- Build and commit `web/dist` for `go:embed`.

Complete when Vue typechecking/build succeeds and a browser smoke test can create and run a fake-Harness Task entirely through the API.

### Deliverable 9 — Plan feedback workflow

- Implement Planner questions, feedback revisions, digest-bound approval, and the feedback UI/API.
- Rewrite `README.md` and usage documentation for API-only operation, global state, prerequisites (`go`, `git`, `gh`, `pi`), security, and repository preparation.
- Document API examples with `curl` while making clear that curl is an API client, not a product CLI.

Complete when documentation contains no instructions for the removed `swf`/`swf-ui` executables or old local database layout.

### Deliverable 10 — Remove legacy code and enforce checks

- Delete TypeScript core, CLI, Node server, obsolete tests, generated package outputs, and old schemas.
- Retain only the Vue frontend toolchain needed under `web`.
- Update `.gitignore` so `web/dist` is committed while transient Go/Vite artifacts remain ignored.
- Update the repository's Software Factory `AGENTS.md` block.

Required repository checks:

```bash
go test ./...
go test -race ./...
npm run typecheck
npm run build
```

Complete when all four commands pass from a clean checkout and `go run main.go` serves a usable embedded UI without first running npm.

## 20. Final acceptance scenario

From a clean checkout with authenticated `pi` and `gh` installations:

1. Run `go run main.go`.
2. Open the logged loopback URL.
3. Observe generated `~/.software-factory/config.yaml` and prompts.
4. Create a draft for a local repository; verify no worktree exists yet.
5. Start it; verify an isolated worktree, pinned SHA, preparation trace, and Planner Pi session.
6. Inspect and approve the plan in the UI.
7. Observe Builder tool calls live, deterministic checks, Reviewer evidence, usage, and cost.
8. Confirm success only when all acceptance conditions pass.
9. Create a GitHub draft; verify cloning occurs only when Start is invoked.
10. Pause/resume a fake long-running Task and confirm PID termination plus Pi session reuse.
11. Restart the server during a fake active run and confirm explicit blocked recovery rather than silent continuation.
12. Delete an inactive Task and confirm its workspace, sessions, worktree, and database records are removed.

The rewrite is complete only when this scenario and every required repository check pass.
