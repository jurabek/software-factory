# Software Factory

Software Factory consists of a self-hosted Next.js application and independent loopback-only Go daemons. The application owns login and daemon registrations in PostgreSQL. Each daemon coordinates Task Workspaces through Planner, Builder, deterministic checks, and Reviewer using its installed `pi` command.

## Prerequisites

- Go
- Git
- GitHub CLI (`gh`) for GitHub repositories
- Pi with authenticated providers
- Docker with Compose for the application, or Node.js 24 and PostgreSQL 16

## Run the application

Create deployment configuration without committing it:

```bash
cp .env.example .env
openssl rand -hex 32
```

Put the generated value in `DAEMON_CREDENTIAL_KEY`, replace the other placeholders, then start PostgreSQL, apply the application schema, and start the application:

```bash
docker compose up --build
```

Open `http://localhost:3000`. The PostgreSQL volume persists application login sessions and daemon registrations. Daemon Tasks and logs remain in each daemon's sandbox.

For an existing application database, back it up and run `docker compose run --rm migrate` before starting the updated application. The schema runner is idempotent and is also run automatically by `docker compose up`; startup stops if it fails. For a fresh database, the same command creates all required tables.

## Run a daemon

```bash
go -C daemon run .
```

Interactive Swagger API documentation is available at `http://127.0.0.1:8080/docs`; its OpenAPI document is served at `/swagger.yaml`. The daemon does not serve a frontend. `PORT` changes the port. `SOFTWARE_FACTORY_DIR` changes the default `~/.software-factory` state directory. `PI_PATH` selects Pi. The first run generates `config.yaml` and editable prompts without replacing existing files.

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for how the daemon coordinates repositories, agents, checks, events, persistence, recovery, and security. API examples are in [`docs/USAGE.md`](docs/USAGE.md).

The daemon binds only to loopback. Local API mutations require the random token from `/api/v1/control`. To register it with the application, configure `SOFTWARE_FACTORY_DAEMON_TOKEN` and make the daemon reachable through an encrypted tunnel whose exact origin is in `DAEMON_ALLOWED_ORIGINS`. Task workspaces, SQLite WAL state, JSONL traces, prompts, and Pi sessions remain under the factory directory until explicit deletion.

## API example

`curl` is an API client, not a product CLI.

```bash
TOKEN=$(curl -s http://127.0.0.1:8080/api/v1/control | jq -r .token)
curl -s -X POST http://127.0.0.1:8080/api/v1/tasks \
  -H "X-Software-Factory-Token: $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"request":"Implement feature X","repositories":[{"type":"local","path":"/absolute/repository","primary":true}]}'
```

Creating a Task allocates its private workspace but does not access its repositories. `POST /api/v1/tasks/{id}/start` materializes every repository and uses the designated primary repository as the default agent/check working directory. Plans contain `questions`; when non-empty, answer them with `POST /api/v1/tasks/{id}/feedback` before approval.

The factory never commits, pushes, merges, deploys, or cleans up automatically.

## Checks

```bash
go -C daemon test ./...
go -C daemon test -race ./...
npm run typecheck
npm run build
npm run swagger:validate
```
