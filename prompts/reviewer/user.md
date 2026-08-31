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
3. Confirm the builder followed that repository's documented checks and tests; name any missing command or evidence.
4. Rule on every applicable requirement with concrete evidence.
5. Identify only actionable correctness, scope, contract, or security gaps.
6. Return `changes_requested` if any requirement is unmet.

Attempt: {{attempt}}

## Required handoff

{{required_output}}
