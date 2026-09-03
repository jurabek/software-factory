# Architecture

The repository is a private npm workspace with three packages:

```text
@software-factory/cli ──┐
                        ├──> @software-factory/core ──> Pi + Git + SQLite
@software-factory/ui  ──┘
```

## Core

`packages/core` is the deep module. Its interface exposes Campaign
orchestration, setup, domain types, and a concrete read model. Its
implementation owns request contracts, transition policy, local repository
worktrees, scoped checks, Pi sessions, redaction, and SQLite persistence.

The read model is the seam used by both command output and the visualizer. It
opens SQLite databases read-only, validates Campaign identifiers, applies
filters and pagination, normalizes JSON rows, and closes each store.

## CLI

`packages/cli` is the `swf` composition root. It parses commands, creates the
core module, and formats results. It has no UI import and performs no HTTP,
asset-build, or background-process work.

## UI

`packages/ui` is the `swf-ui` composition root. It owns the loopback HTTP
adapter, Vue application, and prebuilt assets. It reads through the core read
model. Optional plan approval is injected as a narrow control function and is
never part of the read interface.

## Local-only lifecycle

Campaigns move through planning, explicit plan approval, building, review with
independent required-check execution, bounded review repair, and
`implementation_complete`. Pausing, resuming, aborting, blocking, and
repair-budget failure are local lifecycle controls.

There is no package or transition policy for pull requests, remote checks,
deployment, release, rollout, or rollback.
