# Software Factory MVP

This directory implements the local-first boundary of the design in
[`docs/software-factory`](docs/software-factory/README.md).

It provides:

- Draft 2020-12 validation for Domain Profiles, Feature Requests, and Agent Results.
- A persisted Planner → Builder → Reviewer → Tester state machine with bounded repair loops.
- SQLite/WAL state plus a redacted append-only event mirror.
- Deterministic local agents for offline testing and an opt-in embedded Pi SDK runtime.
- Builder worktrees, path/symlink/generated-file policy, immutable base SHAs, and drift checks.
- A CLI for intake, approval, execution, inspection, pause/resume/abort, and evidence export.
- Opt-in, idempotent campaign branch, draft PR, and CI-check integration through `gh`.
- An automatically started, loopback-only, GET-only Vue visualizer with live
  SQLite WAL session logs, trace filtering, and an agent waterfall.

GitHub delivery is disabled by default and uses only authenticated `gh` CLI commands when enabled.
Merge, deployment, and rollback remain unavailable. Delivery verification reports `deferred`.

Configure agents and repositories in [`config.yaml`](config.yaml). Each agent can
set its own `model`, `thinking`, and `prompt_engineering` system/user files;
those values are passed into Pi.

For complete setup, command reference, multi-repository configuration, campaign
operations, visualizer usage, and troubleshooting, see
[`docs/USAGE.md`](docs/USAGE.md).

## Setup

Node.js 24 or later is required.

```bash
cd software-factory
npm install
npm run typecheck
npm test
npm run build
```

## Run a local Campaign

The CLI defaults to authenticated embedded Pi sessions:

```bash
SOFTWARE_FACTORY_RUNTIME=pi npm run dev -- request \
  --text "Implement the requested change" \
  --repositories app

npm run dev -- approve SF-2026-1234 plan
npm run dev -- run SF-2026-1234 --until implementation_complete
npm run dev -- status SF-2026-1234 --verbose
```

To push campaign branches, open draft PRs, and observe their checks through `gh`:

```bash
gh auth status
export SOFTWARE_FACTORY_DELIVERY=github
npm run dev -- run SF-2026-1234 --until validating_ci
# Re-run while CI is pending; successful checks advance the Campaign.
npm run dev -- run SF-2026-1234 --until implementation_complete
```

Git authentication is configured through `gh auth setup-git`; the controller never reads or persists the token.

Campaign data is written to `.workspace/`. Builder assignments use detached
Git worktrees pinned to the source SHA.

For deterministic fixture/demo runs with no model calls, opt in explicitly:

```bash
SOFTWARE_FACTORY_RUNTIME=fake npm run dev --visualize --bind 127.0.0.1 --port 4173 request \
  --text "Implement the requested change" \
  --repositories app
```

The fake runtime is not evidence that product code was implemented; it exists for controller,
policy, persistence, and UI testing. The Pi runtime creates one persistent session per assignment and requires every role to finish
through the terminating `submit_agent_result` tool. Planner, Reviewer, and Tester do not receive
product write tools.

## Visualizer

Build and start the read-only UI:

```bash
npm run build
npm run dev -- visualize --bind 127.0.0.1 --port 4173
```

Open `http://127.0.0.1:4173`. The server rejects non-GET methods and non-loopback binds.

## Local repository discovery

Each profile repository resolves from `SOFTWARE_FACTORY_REPO_<ID>`, the
configured repository root, or a sibling directory named from the repository
URL. The starter `local` profile uses `app`:

```text
SOFTWARE_FACTORY_REPO_APP=/path/to/your-repo
```

Missing repositories are represented with unresolved base SHAs and are not scheduled locally.
