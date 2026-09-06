# Software Factory API usage

Start a daemon API with `go -C daemon run .`. State defaults to `~/.software-factory`; override it with `SOFTWARE_FACTORY_DIR`. The API binds to loopback and has no CORS support. The daemon does not serve a frontend; users operate it through the separate Next.js application.

To connect the separate application, configure a server credential and expose the loopback daemon through an encrypted tunnel:

```bash
openssl rand -hex 32
SOFTWARE_FACTORY_DAEMON_TOKEN='replace-with-at-least-32-random-characters' \
go -C daemon run .
```

Non-loopback binds are rejected, including when a credential is present. Remote clients send `Authorization: Bearer $SOFTWARE_FACTORY_DAEMON_TOKEN` on every tunneled read, mutation, and stream. `GET /api/v1/identity` returns the stable identity stored in `$SOFTWARE_FACTORY_DIR/daemon-id`. Configuring the remote credential disables `/api/v1/control` and local Swagger routes. Without a remote credential, Swagger remains available at `/docs` for local API use.

Fetch the per-process mutation token:

```bash
TOKEN=$(curl -s http://127.0.0.1:8080/api/v1/control | jq -r .token)
```

Create and start a local draft:

```bash
TASK_ID=$(curl -s -X POST http://127.0.0.1:8080/api/v1/tasks \
  -H "X-Software-Factory-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"request":"Implement feature X","repositories":[{"type":"local","path":"/absolute/repository","primary":true}]}' | jq -r .id)
curl -s -X POST "http://127.0.0.1:8080/api/v1/tasks/$TASK_ID/start" -H "X-Software-Factory-Token: $TOKEN"
```

For GitHub, use `{"type":"github","repo":"owner/repository"}` inside `repositories`. Add more entries for a multi-repository Task and mark exactly one `primary`; repository access starts only after Start.

Read routes include Tasks, attempts, events, results, checks, interventions, and repository diffs. Live events use `/api/v1/tasks/{id}/events/stream`; reconnect with `Last-Event-ID` or `?after=`. Mutation routes are `start`, `approve`, `pause`, `resume`, `abort`, `feedback`, and `interventions`; inactive Tasks support `DELETE`.

Planner results always include `questions`. If questions remain, answer them while awaiting approval:

```bash
curl -s -X POST "http://127.0.0.1:8080/api/v1/tasks/$TASK_ID/feedback" \
  -H "X-Software-Factory-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"feedback":"Use PostgreSQL and retain the public API.","current_plan_digest":"DIGEST_FROM_TASK"}'
```

After the revised plan has an empty `questions` array, approve it:

```bash
curl -s -X POST "http://127.0.0.1:8080/api/v1/tasks/$TASK_ID/approve" -H "X-Software-Factory-Token: $TOKEN"
```

The Planner revision reuses its Task session and approval binds to the latest digest.

Task state and normalized events are stored in SQLite WAL and mirrored to task JSONL traces. Prompt audit copies and Pi sessions remain private beneath the factory directory. Do not expose that directory with a static server.

`curl` examples demonstrate the HTTP API; curl is not a Software Factory CLI.

## Application routes

The Next.js application owns the initial-user session and daemon registrations. Sign in, then register each tunneled daemon once; the browser only calls same-origin `/api/daemons/...` routes and never sees daemon credentials:

- `GET /api/daemons` lists registrations; `POST /api/daemons` registers `{name, endpoint, credential}`.
- `GET /api/daemons/{daemonId}/tasks` lists that daemon's tasks; `POST` with `{request, repositories, coding_agent?, model?, thinking?}` creates a draft.
- `GET /api/daemons/{daemonId}/creation-options[?harness=]` returns projected defaults, harnesses, and models for the creation form.
- `POST /api/daemons/{daemonId}/tasks/{taskId}/{start|approve|pause|resume|abort}` runs one lifecycle command; the approval actor comes from the login session.
- `GET /api/daemons/{daemonId}/tasks/{taskId}/events[?after=&limit=|?tail=]` replays events; `GET .../events/stream[?after=]` proxies the live SSE feed with `Last-Event-ID` support. Open streams revalidate the login session and close on logout or disconnect.
- Every read needs the login session; every mutation additionally needs the configured application origin. Guessed registration IDs return 404, and a replaced daemon at a registered endpoint returns 409.

## Application deployment and schema

For Docker Compose, copy `.env.example` to the ignored `.env`, replace all placeholders, and run `docker compose up --build`. The `migrate` service waits for PostgreSQL, applies `application/migrations/schema.sql`, and must exit successfully before the application starts. PostgreSQL data persists in the `factory-pgdata` volume; daemons are not Compose dependencies.

For a direct Node.js deployment, copy `application/.env.example` to `application/.env.local`, then run:

```bash
npm ci
npm run application:migrations
npm run application:build
npm run application:start
```

Use the same migration command for both fresh and existing application databases. The schema is idempotent, so run it before each application release. Back up an existing database first. Application migrations never read or modify a daemon's SQLite database or sandbox files.
