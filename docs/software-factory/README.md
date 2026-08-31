# Software Factory Docs

This folder contains a complete design package for a Software Factory that can plan, build, review, and test repository changes.

The architecture is domain-neutral. Repositories, checks, risk, and reviews come from each repository's `AGENTS.md` block (written by `swf init`) plus `config.yaml`. The per-repo block replaces the old Domain Profile machinery (archived under `archive/`).

## Start Here

1. Read `SPEC.md` for the end-to-end behavior and state machine.
2. Read `ARCHITECTURE.md` for module seams and runtime topology.
3. Read `AGENT_WORKFLOW.md` for role contracts and repair loops.
4. Read `EASY_USE.md` for the install/init/request UX plan, then `config.yaml` for the roster and global defaults.
5. Use `RUNBOOK.md` for operational flow and command examples.

## Contracts

- `FEATURE_REQUEST.schema.json`: canonical developer-intent and approved-plan contract.
- `AGENT_RESULT.schema.json`: planner/builder/reviewer/tester handoff contract.
- `archive/`: deprecated Domain Profile schema, starter profile, and profile guide (not loaded).

## Supporting Docs

- `ROADMAP.md`: phased delivery and measurable exit criteria.
- `VISUALIZER.md`: read-only campaign observability UI model.
- [`../../software-factory/docs/USAGE.md`](../../software-factory/docs/USAGE.md):
  local installation, commands, campaign operation, and troubleshooting.

## Scope Note

The local MVP is implemented under [`../../software-factory/`](../../software-factory/README.md).
It runs through `implementation_complete` with local worktrees, embedded Pi or deterministic
agents, repair loops, evidence, and a read-only visualizer. GitHub mutations, merge, deployment,
and production rollback remain outside the local boundary.
