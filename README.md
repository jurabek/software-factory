# Software Factory

A local-first software factory CLI: on any repo, `swf init` sets it up, and a
single command files a feature request that runs through a
Planner → Builder → Reviewer → Tester pipeline in isolated git worktrees.

- `swf` on PATH after one install step (`npm link`), usable in any directory.
- Per-repo facts (checks, generated/protected paths) live in a machine-parseable
  block in the repo's `AGENTS.md`, written by `swf init`.
- Campaign state lives in the repo at `.software-factory/workspace/`
  (gitignored); `git status` stays clean.
- Deterministic fake agents for offline testing; an opt-in embedded Pi SDK
  runtime for real local implementation work.
- Opt-in, idempotent draft PR + CI observation through `gh`
  (`config.yaml` → `delivery.provider: github`).
- An automatically started, loopback-only Vue visualizer with live session
  logs, trace filtering, and an opt-in plan-approval control.

GitHub delivery is disabled by default and uses only authenticated `gh` CLI
commands when enabled. Merge, deployment, and rollback remain unavailable.

## Install

Node.js 24 or later is required.

```bash
cd software-factory
npm install
npm run typecheck
npm test
npm run build
npm link          # puts `swf` on PATH
```

`swf` with no command prints a cheat sheet:

```text
swf init      set up this repo (AGENTS.md block + .gitignore + doctor)
swf request   file a feature request against the current repo
swf approve   approve a plan
swf run       run the campaign
swf status    inspect a campaign
swf doctor    diagnose missing requirements
```

## Quick start (any repo)

```bash
cd /path/to/your-repo
swf init                # detects checks, asks at most three questions
swf request "implement X"
# → prints the campaign ID and the next two commands
swf approve SF-2026-1234
swf run SF-2026-1234
swf status SF-2026-1234 --verbose
```

`swf init` is non-interactive (all defaults) when stdin is not a TTY, so it
works in scripts. Re-running it is byte-stable: it rewrites only the marked
block in `AGENTS.md` and leaves hand-written guidance untouched.

`swf request` infers the target repo from the current directory. Multi-repo
campaigns pass sibling paths:

```bash
swf request "change the contract" --repos ../api,../web
```

Every target repo needs its own `AGENTS.md` block (`swf init` in each).

For deterministic fixture/demo runs with no model calls:

```bash
SOFTWARE_FACTORY_RUNTIME=fake swf request "demo"
```

The fake runtime is not evidence that product code was implemented; it exists
for controller, policy, persistence, and UI testing.

## Enable GitHub draft PR delivery

```bash
gh auth status
# config.yaml: delivery.provider: github   (was SOFTWARE_FACTORY_DELIVERY)
swf run SF-2026-1234 --until validating_ci
swf run SF-2026-1234 --until implementation_complete   # poll while pending
```

Git authentication is configured through `gh auth setup-git`; the controller
never reads or persists the token. Branch push, draft PR creation, and CI
observation remain the only GitHub mutations.

## Configuration

[`config.yaml`](config.yaml) holds the agent roster and global defaults:
models, thinking, tools, risk signals, approval rules, required review kinds,
and `delivery.provider`. Per-repo facts come from the repo's `AGENTS.md` block
(checks, generated paths, protected paths, optional risk override) — there are
no domain profiles or repository lists in the config.

`SOFTWARE_FACTORY_WORKSPACE` and `SOFTWARE_FACTORY_CONFIG` survive as location
overrides only. Campaign data defaults to the repo's `.software-factory/`.

For the full command reference, campaign operations, visualizer usage, and
troubleshooting, see [`docs/USAGE.md`](docs/USAGE.md). The design plan is
[`docs/software-factory/EASY_USE.md`](docs/software-factory/EASY_USE.md).
