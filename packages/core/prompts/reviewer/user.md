# Review Task

## Feature Request

```json
{{feature_request}}
```

## Assigned Work Item

```json
{{work_item}}
```

## Factory session bus

Unix socket: `{{factory_socket}}`

Use `list_peer_sessions` and `read_peer_session` to load planner and builder Pi JSONL from WAL.

```json
{{peer_sessions}}
```

## Repository reviewer instructions

These are loaded from the assigned worktree's `prompts/reviewer` or `@prompts/reviewer`. Use them for check and test expectations.

```markdown
{{repository_reviewer_instructions}}
```

## Task

Review the implementation for this work item against the approved request.

1. Derive a requirement checklist from the Feature Request, planner plan, and the repository reviewer instructions above.
2. Inspect the actual diff and all changed or newly created files in `{{worktree}}`.
3. Run every required check by its Feature Request check ID with `run_local_command`; do not rely on builder-reported outcomes.
4. Record each command's outcome and evidence in `checks`; a failed or deferred required check is blocking.
5. Rule on every applicable requirement with concrete evidence.
6. Identify only actionable correctness, scope, contract, or security gaps.
7. Return `changes_requested` if any requirement or required check is unmet.

Attempt: {{attempt}}

## Required handoff

{{required_output}}

## Repository context

The repository's `AGENTS.md` — including the Software Factory block (checks, generated and protected paths, optional risk override) and any hand-written guidance — is authoritative repository context.

```markdown
{{repository_agents_block}}
```
