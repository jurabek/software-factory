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

1. Enumerate required check IDs from the Feature Request.
2. Run each required check ID with `run_local_command`.
3. Correlate failures with the responsible work item when possible.
4. Preserve concise command evidence and classify unavailable checks as deferred.
5. Do not modify code.

Attempt: {{attempt}}

## Required handoff

{{required_output}}

## Repository context

The repository's `AGENTS.md` — including the Software Factory block (checks, generated and protected paths, optional risk override) and any hand-written guidance — is authoritative repository context.

```markdown
{{repository_agents_block}}
```
