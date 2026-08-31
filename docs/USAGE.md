# Software Factory: Running and Usage Guide

This guide covers the local-first Software Factory CLI (`swf`). It can run a
Planner → Builder → Reviewer → Tester campaign against isolated local Git
worktrees of the current repository. GitHub branch, draft PR, and CI
integration is opt-in through `gh`. It does not merge or deploy.

## Prerequisites

- Node.js 24 or later
- npm
- Git
- Pi credentials for real agent runs
- GitHub CLI (`gh`) only for issue intake or GitHub delivery

## Install

```bash
cd software-factory
npm install
npm run typecheck
npm test
npm run build
npm link          # `swf` on PATH
```

While developing, `npm run dev -- <command>` is equivalent to `swf <command>`.

## Doctor

`swf doctor` checks Node ≥ 24, that the cwd is a git repo, that the AGENTS.md
block parses, that the Pi SDK loads, that the campaign workspace is writable,
and `gh auth` only when `delivery.provider` is `github`.

```bash
cd /path/to/your-repo
swf doctor
```

Write the report to a file:

```bash
swf doctor --output capability-report.json
```

Doctor also runs at the end of `swf init` and automatically when a request
fails, so setup problems are surfaced where they happen.

## Choose an agent runtime

### Pi runtime

Pi is the default and is the runtime to use for real local implementation work.
The project dependency includes its CLI, so authenticate it through npm:

```bash
npm exec pi
```

Inside Pi, run `/login` and select a provider. If Pi is installed globally,
running `pi` directly is equivalent.

Each assignment gets a persistent Pi session. Builders receive scoped write
tools for their worktree. Planner, Reviewer, and Tester assignments remain
read-only with respect to product code.

### Agent roster and models

[`config.yaml`](../config.yaml) is the factory roster. `defaults.model` and
`defaults.thinking` apply to every agent; an agent may override them. Those
values are passed into Pi sessions and Pi subagents.

```bash
export SOFTWARE_FACTORY_CONFIG=/path/to/config.yaml
```

### Agent prompt templates

Each agent in `config.yaml` may set `prompt_engineering.system` and
`prompt_engineering.user`. Paths are relative to the config file. Those files
are rendered and passed to Pi as the system prompt and the user prompt.

```yaml
agents:
  - name: planner
    prompt_engineering:
      system: prompts/planner/system.md
      user: prompts/planner/user.md
```

If omitted, the factory falls back to:

```text
software-factory/prompts/
  planner/{system,user}.md
  builder/{system,user}.md
  reviewer/{system,user}.md
  tester/{system,user}.md
```

The controller renders each task with the approved Feature Request, assigned
work item, worktree, attempt number, the campaign Unix socket path
(`factory.sock`), a catalog of prior Pi session IDs, and the repository's
`AGENTS.md` (the repository context, including the Software Factory block).
Agents load each other's work through `list_peer_sessions` /
`read_peer_session`, which read Pi JSONL session lines from campaign SQLite
WAL. The visualizer can read those same rows while an agent is still writing.
Planner and reviewer sessions also load the SSSF subagent harness:
`subagent_create`, `subagent_continue`, `subagent_list`, and `subagent_remove`.
Those spawn a child `pi --mode json` with a persistent JSONL session under
`sessions/.../subagents/`, ingest that file into WAL, and never receive `bash`.
Checks and tests come from each repository's `AGENTS.md` block and its
`prompts/reviewer` or `@prompts/reviewer` instructions. The factory does not
hardcode domain recipes. Builders and testers run those documented commands
through `run_local_command`. Agent output must still be submitted through
`submit_agent_result` and pass `AGENT_RESULT.schema.json`.

### Fake runtime

The fake runtime exercises orchestration, persistence, policy, and visualization
without making model calls:

```bash
export SOFTWARE_FACTORY_RUNTIME=fake
```

It is intended for tests and demonstrations. A fake campaign reaching
`implementation_complete` is not evidence that product code was implemented or
tested.

## Quick start

All commands run from the repository you want to build in.

1. Set the repo up:

   ```bash
   swf init
   ```

   `swf init` detects checks (package.json / pyproject.toml / Cargo.toml /
   go.mod), asks at most three questions with defaults, writes the AGENTS.md
   block, adds `.software-factory/` to `.gitignore`, and runs doctor. Non-TTY
   input takes all defaults.

2. File a request:

   ```bash
   swf request "Implement the requested change"
   ```

   The output includes the campaign ID and the next two commands.

3. Copy the returned campaign ID, for example `SF-2026-1234`:

   ```bash
   export CAMPAIGN_ID=SF-2026-1234
   ```

4. Inspect the generated Feature Request:

   ```bash
   swf request show "$CAMPAIGN_ID"
   ```

5. Approve the plan. When it is the only pending plan, no campaign ID is needed:

   ```bash
   swf approve
   ```

   Or use the campaign ID explicitly:

   ```bash
   swf approve "$CAMPAIGN_ID" plan
   ```

6. Run through Builder, Reviewer, and Tester:

   ```bash
   swf run "$CAMPAIGN_ID" --until implementation_complete
   ```

7. Inspect the outcome:

   ```bash
   swf status "$CAMPAIGN_ID" --verbose
   swf results "$CAMPAIGN_ID"
   swf checks "$CAMPAIGN_ID"
   swf findings "$CAMPAIGN_ID"
   ```

## Enable GitHub draft PR delivery

GitHub operations use the installed `gh` CLI; no GitHub SDK configuration is
required. Set `delivery.provider` in `config.yaml` (replaces the old
`SOFTWARE_FACTORY_DELIVERY` env var):

```yaml
delivery:
  provider: github
```

Authenticate, then advance past testing:

```bash
gh auth login
gh auth status
swf doctor
swf run "$CAMPAIGN_ID" --until validating_ci
```

The controller invokes `gh auth setup-git`, pushes a deterministic Campaign branch,
reconciles an existing PR before mutation, and creates a draft with `gh pr create`.
It observes checks with `gh pr checks`. While checks are pending, the Campaign remains
`validating_ci`; run the command again to poll. Failed checks enter the bounded Builder
→ Reviewer → Tester CI repair loop.

The authenticated identity must have appropriately scoped repository access. Prefer a
short-lived `GH_TOKEN` for unattended runs. Tokens and `gh auth token` output are never
stored as Campaign evidence.

## Create a request

### From text

```bash
swf request "Add a new field to the request"
```

With no `--text`, `swf request` prompts interactively:

```bash
swf request
```

### Against a sibling set of repositories

The cwd is always the primary repository; `--repos` adds sibling paths. Every
target must have its own AGENTS.md block (`swf init` in each).

```bash
swf request "change the contract" --repos ../api,../web
```

### From a GitHub issue

This is a read-only GitHub operation and requires an authenticated `gh` CLI:

```bash
gh auth status

swf request --issue "https://github.com/OWNER/REPOSITORY/issues/123"
```

The issue title and body become the request text. The factory does not comment
on or modify the issue.

## Repository context

Each repository is identified by its checkout path. Its AGENTS.md Software
Factory block supplies:

- `checks`: commands the Tester must run and report evidence for.
- `generated`: build/output paths builders must never touch.
- `protected`: additional paths builders must never touch.
- `risk_signals`: optional per-repo override of the global list.

Selected repositories must exist locally as git work trees and have the pinned
base commit available. The factory creates a detached worktree for each work
item.

## Review or amend a plan

The request is represented by a versioned Feature Request. Amend a value using a
JSON Pointer:

```bash
swf request amend "$CAMPAIGN_ID" \
  --set '/businessOutcome=Add the field without changing public semantics'
```

JSON values are accepted:

```bash
swf request amend "$CAMPAIGN_ID" \
  --set '/nonGoals=["Changing the provider API","Deploying to production"]'
```

Submit and approve the new revision:

```bash
swf request submit "$CAMPAIGN_ID"
swf approve "$CAMPAIGN_ID" plan
```

An amendment invalidates previous approvals and agent results. Terminal
campaigns cannot be amended.

## Run individual workflow stages

Run until review is clear and testing is next:

```bash
swf review "$CAMPAIGN_ID"
```

Run testing and any bounded repair cycle through local completion:

```bash
swf test "$CAMPAIGN_ID"
```

Or select an explicit target:

```bash
swf run "$CAMPAIGN_ID" --until testing
```

The controller enforces role order. Blocking review findings return work to a
Builder. Failed tests return work to a Builder, followed by fresh review and
testing. Required deferred checks do not pass a campaign.

## Inspect a campaign

```bash
swf status "$CAMPAIGN_ID"
swf status "$CAMPAIGN_ID" --verbose
swf workers "$CAMPAIGN_ID"
swf results "$CAMPAIGN_ID"
swf results "$CAMPAIGN_ID" --role reviewer
swf checks "$CAMPAIGN_ID"
swf findings "$CAMPAIGN_ID"
swf failures "$CAMPAIGN_ID"
swf failures "$CAMPAIGN_ID" --format escalation
```

Check whether source repositories moved from their pinned base SHAs:

```bash
swf drift "$CAMPAIGN_ID"
```

## Pause, resume, or abort

```bash
swf pause "$CAMPAIGN_ID" \
  --reason "Waiting for product clarification"

swf resume "$CAMPAIGN_ID"

swf abort "$CAMPAIGN_ID" \
  --reason "Request superseded"
```

Aborting preserves campaign evidence and does not delete external repositories
or reverse product operations.

## Waive a known baseline failure

A waiver must name a known check, link an issue, and expire:

```bash
swf waiver propose "$CAMPAIGN_ID" \
  --check CHECK-unit \
  --issue "https://github.com/OWNER/REPOSITORY/issues/456" \
  --expires "2026-09-15T12:00:00Z" \
  --reason "Known integration environment baseline failure"

swf approve "$CAMPAIGN_ID" waiver
```

Proposing a waiver creates a new request revision and invalidates prior
approvals/results. A waiver applies only to its exact check and expiry.

## Export evidence

```bash
swf evidence export "$CAMPAIGN_ID" \
  --redacted \
  --output "$CAMPAIGN_ID.evidence.json"
```

The export contains the request, campaign state, results, events, checks,
findings, and dependency records. Exports are always redacted; `--redacted`
makes that local-mode behavior explicit. Sensitive fields are redacted before
persistence and export.

## Use the visualizer

The CLI automatically starts the visualizer as a detached background process
when `request`, `run`, `review`, or `test` starts an agent workflow. It first
checks `/api/health`, so later commands reuse the existing server instead of
starting duplicates.

`request` and `visualize` build `apps/visualizer/dist` on first use when it is
missing. You can still prebuild with `npm run build`.

The default address is `http://127.0.0.1:4173`. Set a different port before
running a task when needed:

```bash
export SOFTWARE_FACTORY_VISUALIZER_PORT=4180
```

The host is always loopback. Visualizer startup failures are reported as
warnings on stderr and do not discard or block the agent task.

The server can still be started manually:

```bash
swf visualize \
  --bind 127.0.0.1 \
  --port 4173
```

Open `http://127.0.0.1:4173`.

For an explicit, local one-click **plan approval** control, start the visualizer
in control mode (the background visualizer remains read-only):

```bash
swf visualize --control
```

Open a campaign waiting for plan approval, review it, and select **Approve
plan**. Control mode is loopback-only, only approves plans, and requires a
per-process token sent in a custom request header. It never enables merge,
deployment, retry, or other workflow mutations.

The visualizer uses a live, three-level workflow:

1. Campaign cards show active and completed sessions.
2. Opening a campaign shows a time-scaled Planner/Builder/Reviewer/Tester
   waterfall.
3. The Session WAL panel tails redacted trace events from every agent run every
   500 milliseconds.

Trace controls can show or hide lifecycle, model, tool, and log events; filter
by role or individual agent run; pause live polling; and opt into structured
payload display. Clicking a waterfall phase filters the log to that run.

The server reads cursor-paginated events directly from each campaign's SQLite
database while the controller writes in WAL mode. Pi runs record model request
boundaries, turns, tool starts and results, session attachment, logs, errors,
and phase lifecycle events. Prompt bodies and model reasoning remain hidden.
All persisted trace payloads pass through the factory redactor.

By default the visualizer has no mutation endpoints and rejects non-GET
requests. `visualize --control` is the sole exception: it enables only the
loopback-only, token-protected plan-approval endpoint. Non-loopback bind
addresses are always rejected.

## Workspace location

By default, runtime data is stored in the target repository:

```text
<repo>/.software-factory/workspace/<campaign-id>/
```

Use another location with:

```bash
export SOFTWARE_FACTORY_WORKSPACE=/absolute/path/to/factory-workspace
```

Important contents include:

```text
campaign.db       SQLite state and Pi session JSONL in WAL mode
factory.sock      pointer to the short campaign Unix socket path
events/           redacted append-only event stream
requests/         Feature Request revisions
profiles/         pinned resolved repository context
results/          structured Agent Results
sessions/         persistent Pi session files
worktrees/        isolated repository worktrees
```

Do not manually edit factory-owned worktrees or campaign database files while a
campaign is active.

## Local-mode limitations

- Branch push, draft PR creation, and CI observation require
  `delivery.provider: github` in `config.yaml`
- No merge
- No CI workflow dispatch or cancellation
- No deployment or rollback
- Runtime delivery verification is deferred
- Missing repository context or capabilities cannot be treated as passed

The verification command therefore reports `deferred`:

```bash
swf verify "$CAMPAIGN_ID" --environment dev
```

## Troubleshooting

### A request fails with a missing AGENTS.md block

The error message points at `swf init`. Run it in the repository (and in every
`--repos` sibling), then request again.

### Pi cannot start an agent

Confirm Pi is installed and authenticated:

```bash
npm exec pi
```

Use `/login` inside Pi, then retry. For an orchestration-only smoke test, set
`SOFTWARE_FACTORY_RUNTIME=fake`.

### A selected repository is unavailable

`swf doctor` reports it. Every repository in a campaign must be a local git
work tree with a committed AGENTS.md block and the pinned base commit
available. Re-run `swf init` where the block is missing.

### Plan approval is required

Inspect and approve the current revision:

```bash
swf request show "$CAMPAIGN_ID"
swf approve "$CAMPAIGN_ID" plan
```

If the request was amended but not submitted:

```bash
swf request submit "$CAMPAIGN_ID"
swf approve "$CAMPAIGN_ID" plan
```

### A required check is deferred

Deferred means the local environment cannot execute the check. It is not a
pass. Install/configure the missing capability or run the check in its trusted
executor before expecting implementation completion.

### Visualizer build not found

```bash
npm run build
swf visualize
```

### Inspect command help

```bash
swf --help
swf request --help
swf waiver propose --help
```
