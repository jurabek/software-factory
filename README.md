# Software Factory

Software Factory is a loopback-only Go service that coordinates one repository through Planner, Builder, deterministic checks, and Reviewer using the installed `pi` command.

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

The server binds only to loopback. Mutations require the random token obtained by the same-origin UI from `/api/v1/control`. Campaign workspaces, SQLite WAL state, JSONL traces, prompts, and Pi sessions remain under the factory directory until explicit deletion.

## API example

`curl` is an API client, not a product CLI.

```bash
TOKEN=$(curl -s http://127.0.0.1:8080/api/v1/control | jq -r .token)
curl -s -X POST http://127.0.0.1:8080/api/v1/campaigns \
  -H "X-Software-Factory-Token: $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"request":"Implement feature X","repository":{"type":"local","path":"/absolute/repository"}}'
```

Creating a draft does not access the repository. `POST /api/v1/campaigns/{id}/start` creates its isolated worktree or GitHub clone. Plan approval uses `POST /api/v1/campaigns/{id}/approve`.

The factory never commits, pushes, merges, deploys, or cleans up automatically.

## Checks

```bash
go test ./...
go test -race ./...
npm run typecheck
npm run build
npm run swagger:validate
```
