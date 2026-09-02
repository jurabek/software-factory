# Software Factory

A local-only coding workflow built around Pi. `swf` coordinates a Feature
Request through Planner, Builder, Reviewer, and Tester agents in isolated Git
worktrees. `swf-ui` visualizes Campaign state from local SQLite files.

The factory does not open pull requests, poll remote checks, deploy, or call a
remote Software Factory server. Pi can still use its normal tools, including
`gh`, when the user explicitly asks it to.

## Packages

- `@software-factory/core`: Campaign orchestration, local Git worktrees,
  repository checks, Pi runtime, persistence, and the read model.
- `@software-factory/cli`: the `swf` executable.
- `@software-factory/ui`: the `swf-ui` executable, loopback server, and Vue UI.

Dependencies point inward: CLI and UI depend on core; core never imports an
executable package, and `swf` never starts the UI.

## Install

Node.js 24 or later is required.

```bash
npm install
npm run typecheck
npm test
npm run build
npm link --workspace @software-factory/cli
npm link --workspace @software-factory/ui
```

## Run a Campaign

From a local Git repository:

```bash
swf init
swf request "implement X"
swf approve SF-2026-1234
swf run SF-2026-1234
swf status SF-2026-1234 --verbose
```

Repository checks and protected/generated paths live in the marked
Software Factory block in `AGENTS.md`. Campaign data lives under
`.software-factory/workspace/`.

For deterministic orchestration tests without model calls:

```bash
SOFTWARE_FACTORY_RUNTIME=fake swf request "demo"
```

## Run the UI

Build once, then start the visualizer explicitly:

```bash
swf-ui
swf-ui --port 4180
swf-ui --control
```

The server binds to loopback only and runs in the foreground. It is read-only
unless `--control` is supplied; control mode adds only token-protected local
plan approval.

See [`docs/USAGE.md`](docs/USAGE.md) for the command reference.
