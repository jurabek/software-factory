# Boring Software Factory -> bsf

Software Factory consists of a self-hosted Next.js application and independent loopback-only Go daemons. The application owns login and daemon registrations in PostgreSQL. Each daemon coordinates Task Workspaces through Planner, Builder, deterministic checks, and Reviewer using its installed `pi` command.

## Prerequisites

- Go
- Git
- GitHub CLI (`gh`) for GitHub repositories
- Pi with authenticated providers
- Node.js 24 and Docker with Compose for PostgreSQL

## Run the application

PostgreSQL runs in Compose; the application runs via pure npm. Create configuration without committing it:

```bash
cp .env.example .env
cp application/.env.example application/.env.local
```

Replace the placeholders in `application/.env.local`, then start PostgreSQL, apply the application schema, and start the application:

```bash
docker compose up -d
npm ci
npm run application:migrations
npm run application:dev
```

Open `http://localhost:3000`. The PostgreSQL volume persists application login sessions and daemon registrations. Daemon Tasks and logs remain in each daemon's sandbox.

For production use `npm run application:build` then `npm run application:start`. The schema runner is idempotent; back up an existing database, then run `npm run application:migrations` before each application release. For a fresh database, the same command creates all required tables.

## Run a daemon

```bash
go -C daemon run .
```

Interactive Swagger API documentation is available at `http://127.0.0.1:8080/docs`; its OpenAPI document is served at `/swagger.yaml`. The daemon does not serve a frontend. `PORT` changes the port. `SOFTWARE_FACTORY_DIR` changes the default `~/.software-factory` state directory. `PI_PATH` selects Pi. The first run generates `config.yaml` and editable prompts without replacing existing files.

See [daemon architecture](daemon/REFACTORING.md) for module ownership and dependencies. See [`docs/USAGE.md`](docs/USAGE.md) for the daemon connection workflow.

The daemon binds only to loopback. Every `/api/*` request except `GET /api/v1/health` requires `Authorization: Bearer <daemon-token>`. The token is generated on first run, persisted at `$SOFTWARE_FACTORY_DIR/daemon-token`, and printed to stdout. To reach the daemon from the application, expose it through an encrypted tunnel whose exact origin is in `DAEMON_ALLOWED_ORIGINS`. Task workspaces, SQLite WAL state, JSONL traces, prompts, and Pi sessions remain under the factory directory until explicit deletion.

## Connecting a daemon to the application

On startup the daemon prints a `connection token` line and writes the same value to `$SOFTWARE_FACTORY_DIR/connection-token`. This token is a self-contained JWT that bundles the daemon endpoint (`http://<bind>:<port>`, derived from the bind address), the daemon identity, the daemon name (the OS hostname), and the bearer credential. In the application UI, open **Connect daemon**, paste the token, optionally override the name (it defaults to the hostname), and submit. The application server verifies the token, checks the endpoint against `DAEMON_ALLOWED_ORIGINS`, confirms the daemon identity, and registers the connection. The credential never leaves the application server.

## API example

`curl` is an API client, not a product CLI.

```bash
TOKEN=$(cat ~/.software-factory/daemon-token)
curl -s -X POST http://127.0.0.1:8080/api/v1/tasks \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"request":"Implement feature X","repository":{"type":"local","path":"/absolute/repository"}}'
```

Creating a Task allocates its private workspace, materializes its repository at `workspace/repository`, and starts execution. Agents and checks use that repository as their working directory. Plans contain `questions`; when non-empty, answer them with `POST /api/v1/tasks/{id}/messages` before approval.

## Task execution sequence

```mermaid
sequenceDiagram
    actor User
    participant App as Next.js application
    participant API as Daemon API
    participant O as Orchestrator
    participant T as Task service
    participant P as Pipeline
    participant Plan as Planner
    participant Build as Builder
    participant Verify as Verifier
    participant Review as Reviewer
    participant K as Stagekit
    participant Store as SQLite store

    User->>App: Create Task
    App->>App: Resolve registered daemon and credential
    App->>API: POST /api/v1/tasks
    API->>O: Create(request)
    O->>T: Create workspace and freeze configuration
    T->>Store: Persist Task and execution branch
    T-->>O: Task
    O-->>API: Task; background worker launched
    API-->>App: 201 Created
    App-->>User: Task workspace

    O->>P: Run(taskID)
    P->>Plan: Plan(input)
    Plan->>K: Prepare repository through sandbox and Git
    Plan->>K: Begin attempt; run agent through agentexec
    Plan->>K: Validate and publish plan; await approval
    K->>Store: Persist attempts, reports, snapshots and events
    Plan-->>P: PlanResult (not approved)
    P-->>O: Waiting for approval

    User->>App: Approve current plan digest
    App->>API: POST /api/v1/tasks/{id}/approve
    API->>O: Approve(id, actor, digest)
    O->>Store: Validate and persist approval
    O-->>API: Background worker launched
    API-->>App: 202 Accepted

    O->>P: Run(taskID)
    P->>Plan: Plan(input)
    Plan-->>P: Reuse approved PlanResult
    P->>Build: Build(input, plan)
    Build->>K: Execute agent; enforce protected paths; publish
    Build-->>P: BuildResult
    P->>Verify: Verify(input, plan, build)
    Verify->>Verify: Run deterministic checks and advisory comparisons
    Verify->>K: Persist evidence and transition
    Verify-->>P: VerificationResult
    alt Verification passed
        P->>Review: Review(input, plan, build, verification)
        Review->>K: Execute read-only agent; publish verdict
        K->>Store: Complete Task or block rejected review
        Review-->>P: ReviewResult
        P-->>O: Completed or blocked
    else Verification failed
        P-->>O: Blocked; verifier already persisted state
    end
    Note over O,Store: Stages own lifecycle transitions; worker blocks unhandled execution errors

    User->>App: Watch Task events
    App->>API: GET /api/v1/tasks/{id}/events/stream
    API->>Store: Read events after cursor
    Store-->>API: Persisted events
    API-->>App: SSE events
    App-->>User: Render state and evidence
```

The factory never commits, pushes, merges, deploys, or cleans up automatically.

## Checks

```bash
go -C daemon test ./...
go -C daemon test -race ./...
npm run typecheck
npm run build
npm run swagger:validate
```
