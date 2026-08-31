# Software Factory Docs

This folder contains a complete design package for a profile-driven Software Factory that can plan, build, review, and test multi-repository changes.

The architecture is domain-neutral. Repositories, checks, risk, and reviews come from `config.yaml` or a Domain Profile. The starter profile is `local`.

## Start Here

1. Read `SPEC.md` for the end-to-end behavior and state machine.
2. Read `ARCHITECTURE.md` for module seams and runtime topology.
3. Read `AGENT_WORKFLOW.md` for role contracts and repair loops.
4. Read `LOCAL_PROFILE.md` plus `config.yaml` for the starter roster, models, and repositories.
5. Use `RUNBOOK.md` for operational flow and command examples.

## Contracts

- `FEATURE_REQUEST.schema.json`: canonical developer-intent and approved-plan contract.
- `DOMAIN_PROFILE.schema.json`: domain extension contract for reusable orchestration.
- `AGENT_RESULT.schema.json`: planner/builder/reviewer/tester handoff contract.

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
