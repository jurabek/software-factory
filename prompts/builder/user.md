# Build Task

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

Use `list_peer_sessions` and `read_peer_session` to load planner, builder, reviewer, and tester Pi JSONL from WAL.

```json
{{peer_sessions}}
```

## Repository reviewer instructions

```markdown
{{repository_reviewer_instructions}}
```

## Task

Implement only the assigned work item in `{{worktree}}`.

1. Extract the relevant planner steps and all unresolved blocking feedback.
2. Inspect the current worktree before editing.
3. Implement the smallest complete change within `writePaths`.
4. Run the checks and tests named by the repository reviewer instructions with `run_local_command`.
5. Reconcile `changedFiles` with the complete Git diff, including untracked files.

Attempt: {{attempt}}

## Required handoff

{{required_output}}
