# Software Factory API usage

Start a daemon API with `go -C daemon run .`. State defaults to `~/.software-factory`; override it with `SOFTWARE_FACTORY_DIR`. The API binds to loopback and has no CORS support. The daemon does not serve a frontend; users operate it through the separate Next.js application.

On first run the daemon generates a 32-hex bearer token, persists it at `$SOFTWARE_FACTORY_DIR/daemon-token`, and prints it to stdout:

```
daemon token: 0123456789abcdef0123456789abcdef
daemon token file: /Users/you/.software-factory/daemon-token
```

Every `/api/*` request except `GET /api/v1/health` requires `Authorization: Bearer <daemon-token>`. Non-loopback binds are rejected. `GET /api/v1/identity` returns the stable identity stored in `$SOFTWARE_FACTORY_DIR/daemon-id`. Swagger UI is served at `/docs`.

Export the token:

```bash
TOKEN=$(cat ~/.software-factory/daemon-token)
```

Create a local task:

```bash
curl -s -X POST http://127.0.0.1:8080/api/v1/tasks \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"request":"Implement feature X","repositories":[{"type":"local","path":"/absolute/repository","primary":true}]}'
```

For GitHub, use `{"type":"github","repo":"owner/repository"}` inside `repositories`. Add more entries for a multi-repository Task and mark exactly one `primary`; repository access starts immediately after creation.

Read routes include Tasks, attempts, events, results, checks, interventions, and repository diffs. Live events use `/api/v1/tasks/{id}/events/stream`; reconnect with `Last-Event-ID` or `?after=`. Mutation routes are `approve`, `pause`, `resume`, `abort`, `feedback`, and `interventions`; inactive Tasks support `DELETE`.

`GET /api/v1/tasks/{id}/sessions` returns the bare task array extended with
`agent_sessions` per item: `{role, harness, provider?, model?, thinking?,
harness_session_id (UUID), session_directory, session_ready,
native_transcript_path?, usage, cost, accounting_complete, ...}`. `cost` is
accumulated reported cost (an estimate/lower bound when
`accounting_complete` is false); `usage` describes the last invocation.
`accounting_complete` is false while an invocation is pending, after crash
recovery with unrecorded spend, or when terminal accounting was incomplete.

`GET /api/v1/tasks/{id}/events` returns
`{"events": [...], "cursor": N, "format_version": 1}`; each event carries
`kind`, `format_version`, typed `payload`, and daemon-computed `display`
consumed verbatim by the UI. `GET /api/v1/models?harness=` returns models
with per-harness validated `thinking` capabilities.

Planner results always include `questions`. If questions remain, answer them while awaiting approval:

```bash
curl -s -X POST "http://127.0.0.1:8080/api/v1/tasks/$TASK_ID/feedback" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"feedback":"Use PostgreSQL and retain the public API.","current_plan_digest":"DIGEST_FROM_TASK"}'
```

After the revised plan has an empty `questions` array, approve it:

```bash
curl -s -X POST "http://127.0.0.1:8080/api/v1/tasks/$TASK_ID/approve" -H "Authorization: Bearer $TOKEN"
```

The Planner revision reuses its Task session and approval binds to the latest digest.

Task state and normalized events are stored in SQLite WAL and mirrored to task JSONL traces. Prompt audit copies and Pi sessions remain private beneath the factory directory. Do not expose that directory with a static server.

`curl` examples demonstrate the HTTP API; curl is not a Software Factory CLI.

## Application routes

The Next.js application owns the initial-user session and daemon registrations. Sign in, then register each tunneled daemon once; the browser only calls same-origin `/api/daemons/...` routes and never sees daemon credentials:

- `GET /api/daemons` lists registrations; `POST /api/daemons` registers `{name, endpoint, credential}`.
- `GET /api/daemons/{daemonId}/tasks` lists that daemon's tasks; `POST` with `{request, repositories, coding_agent?, model?, thinking?}` creates and starts a task.
- `GET /api/daemons/{daemonId}/creation-options[?harness=]` returns projected defaults, harnesses, and models for the creation form.
- `POST /api/daemons/{daemonId}/tasks/{taskId}/{start|approve|pause|resume|abort}` runs one lifecycle command; the approval actor comes from the login session.
- `GET /api/daemons/{daemonId}/tasks/{taskId}/events[?after=&limit=|?tail=]` replays events; `GET .../events/stream[?after=]` proxies the live SSE feed with `Last-Event-ID` support. Open streams revalidate the login session and close on logout or disconnect.
- Every read needs the login session; every mutation additionally needs the configured application origin. Guessed registration IDs return 404.

## Application deployment and schema

Docker Compose provides PostgreSQL only. Copy `.env.example` to the ignored `.env`, replace the placeholder, and run `docker compose up -d`. PostgreSQL data persists in the `factory-pgdata` volume; daemons are not Compose dependencies. The application itself runs via pure npm (see below).

For a direct Node.js deployment, copy `application/.env.example` to `application/.env.local`, then run:

```bash
npm ci
npm run application:migrations
npm run application:build
npm run application:start
```

Use the same migration command for both fresh and existing application databases. The schema is idempotent, so run it before each application release. Back up an existing database first. Application migrations never read or modify a daemon's SQLite database or sandbox files.
