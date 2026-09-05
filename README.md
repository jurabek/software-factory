# Software Factory

Software Factory is a loopback-only Go service that coordinates Task Workspaces through Planner, Builder, deterministic checks, and Reviewer using the installed `pi` command. A Task Workspace may contain one or more isolated repository worktrees or clones.

## Prerequisites

- Go
- Git
- GitHub CLI (`gh`) for GitHub repositories
- Pi with authenticated providers
- Node.js only for UI development

## Run

```bash
go run main.go
```

Open `http://127.0.0.1:8080`. Interactive Swagger API documentation is available at `http://127.0.0.1:8080/docs`; its OpenAPI document is served at `/swagger.yaml`. `PORT` changes the port. `SOFTWARE_FACTORY_DIR` changes the default `~/.software-factory` state directory. `PI_PATH` selects Pi. The first run generates `config.yaml` and editable prompts without replacing existing files.

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for how the daemon coordinates repositories, agents, checks, events, persistence, recovery, and security. API examples are in [`docs/USAGE.md`](docs/USAGE.md).

The server binds only to loopback. Mutations require the random token obtained by the same-origin UI from `/api/v1/control`. Task workspaces, SQLite WAL state, JSONL traces, prompts, and Pi sessions remain under the factory directory until explicit deletion.

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
go test ./...
go test -race ./...
npm run typecheck
npm run build
npm run swagger:validate
```
