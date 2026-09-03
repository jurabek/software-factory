# Software Factory Usage

## Setup

```bash
npm install
npm run build
npm link --workspace @software-factory/cli
npm link --workspace @software-factory/ui
```

`swf doctor` verifies Node, Git, the repository `AGENTS.md` block, the Pi SDK,
and the local Campaign workspace. `swf init` creates the marked `AGENTS.md`
block, updates `.gitignore`, and runs doctor.

## Campaign workflow

```bash
cd /path/to/local/repository
swf init
swf request "Implement the requested change"
export CAMPAIGN_ID=SF-2026-1234
swf request show "$CAMPAIGN_ID"
swf approve "$CAMPAIGN_ID"
swf run "$CAMPAIGN_ID"
```

The local state sequence is:

```text
planning → plan approval → building → reviewing
         → bounded review repair → implementation_complete
```

Use `--repos ../api,../web` to include sibling local Git repositories. Every
selected repository needs its own Software Factory `AGENTS.md` block.

## Inspect and control

```bash
swf status "$CAMPAIGN_ID" --verbose
swf workers "$CAMPAIGN_ID"
swf results "$CAMPAIGN_ID"
swf checks "$CAMPAIGN_ID"
swf findings "$CAMPAIGN_ID"
swf failures "$CAMPAIGN_ID"
swf drift "$CAMPAIGN_ID"

swf pause "$CAMPAIGN_ID" --reason "Waiting for clarification"
swf resume "$CAMPAIGN_ID"
swf abort "$CAMPAIGN_ID" --reason "Request superseded"
```

Amendments use JSON Pointer syntax and invalidate prior approval and results:

```bash
swf request amend "$CAMPAIGN_ID" --set '/businessOutcome=Updated outcome'
swf request submit "$CAMPAIGN_ID"
swf approve "$CAMPAIGN_ID"
```

Export redacted local evidence:

```bash
swf evidence export "$CAMPAIGN_ID" --output "$CAMPAIGN_ID.evidence.json"
```

## Agent runtime

Pi is the default runtime. Interactive `swf init` lists the Pi models available
with your configured credentials and asks which model to use for each agent
role. It installs the resulting roster and editable prompt files under
`.software-factory/`, using `packages/core/config.yaml` as the template. Set
`SOFTWARE_FACTORY_CONFIG=/absolute/config.yaml` to override the repo-local
roster.

Agent sessions have a wall-clock deadline and a bounded empty-response retry
budget. Both are configurable:

```yaml
runtime:
  agent_deadline_ms: 1800000
  empty_turn_retries: 2

agents:
  - name: reviewer
    fallback_model: provider/model-id # optional; used after repeated empty turns
```

Only one `swf run` process may advance a Campaign at a time. If an owner is
interrupted, the next run marks its stale worker failed and blocks the
Campaign; use `swf resume CAMPAIGN_ID` after inspecting the failure.

## Visualizer

`swf` never builds, probes, or starts the visualizer. Build the workspace and
run the separate executable:

```bash
swf-ui
swf-ui --cwd /path/to/repository --port 4180
swf-ui --workspace /absolute/workspace
swf-ui --control
```

`swf-ui` runs in the foreground and accepts only loopback bind addresses.
Without `--control`, all data routes are read-only. Control mode enables only
local plan approval using a per-process token.

The Reviewer independently runs every required repository check. The UI polls
Campaign SQLite databases in WAL mode and shows agent phases, checks, findings,
and redacted session events. It does not expose prompt
bodies.

## Local-only constraints

- No factory-owned remote server integration.
- No pull-request creation or remote check polling.
- No merge, release, deployment, rollout, or rollback workflow.
- No waiver path that converts a required failed check into a pass.
- All target repositories and required checks must be available locally.

Existing Campaign databases in removed remote-delivery states are rejected
with an unsupported legacy state error. Create a new Campaign instead.
