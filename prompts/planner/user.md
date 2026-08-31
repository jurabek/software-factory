# Plan Task

## Feature Request

```json
{{feature_request}}
```

## Factory session bus

Unix socket: `{{factory_socket}}`

Use `list_peer_sessions` and `read_peer_session` to load prior Pi JSONL from WAL. Do not read other agents' session files directly.

```json
{{peer_sessions}}
```

## Repository reviewer instructions

```markdown
{{repository_reviewer_instructions}}
```

## Task

1. Verify the request against the codebase at `{{worktree}}`.
2. Produce an ordered plan for every work item and dependency.
3. Map each acceptance criterion to verification steps from the repository reviewer instructions, not factory-hardcoded recipes.
4. Identify contract, traffic, security, migration, and rollback implications.
5. Record assumptions as unresolved items when they cannot be verified locally.

Attempt: {{attempt}}

## Required handoff

{{required_output}}
