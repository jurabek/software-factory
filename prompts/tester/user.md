# Test Task

## Feature Request

```json
{{feature_request}}
```

## Factory session bus

Unix socket: `{{factory_socket}}`

Use `list_peer_sessions` and `read_peer_session` to load planner, builder, and reviewer Pi JSONL from WAL.

```json
{{peer_sessions}}
```

## Repository reviewer instructions

```markdown
{{repository_reviewer_instructions}}
```

## Task

Verify the implementation available from `{{worktree}}`.

1. Enumerate required checks from the Feature Request and the repository reviewer instructions.
2. Run each locally available documented check with `run_local_command`.
3. Correlate failures with the responsible work item when possible.
4. Preserve concise command evidence and classify unavailable checks as deferred.
5. Do not modify code.

Attempt: {{attempt}}

## Required handoff

{{required_output}}
