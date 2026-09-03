# Local Software Factory Specification

## Goal

Coordinate bounded repository changes on one machine from an approved Feature
Request through implementation completion.

## Required behavior

1. `swf init` records repository checks and protected/generated paths in
   `AGENTS.md`.
2. `swf request` resolves local repository context, pins Git SHAs, creates a
   Campaign, and asks the Planner for a structured plan.
3. A human approves the current request revision.
4. Builders change only assigned worktrees and allowed paths.
5. Reviewers inspect the implementation, execute every required repository
   check locally, and report blocking findings without changing product code.
6. Blocking review or failed checks enter a bounded repair loop.
7. Successful review and required checks complete the Campaign as
   `implementation_complete`.
8. Campaign input, results, checks, findings, sessions, and events are stored
   locally with sensitive values redacted.

## Invariants

- Target repositories are existing local Git work trees.
- Every target has a valid Software Factory `AGENTS.md` block.
- Agents are bound to the current Campaign, request hash/revision, repository
  context digest, role, and work item.
- Required checks pass; deferred checks do not count as passing.
- Planner and Reviewer do not modify product code.
- Builder file claims match the worktree diff.
- The factory does not require or implement a remote control plane.
- Legacy Campaigns in removed remote lifecycle states fail clearly.

## Interfaces

- `swf`: local orchestration and inspection.
- `swf-ui`: explicit local visualization, read-only by default.
- `@software-factory/core`: programmatic orchestration and read model.
