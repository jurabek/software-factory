# Visualizer

`swf-ui` is the only visualizer launcher. It runs in the foreground, serves
prebuilt Vue assets, and reads Campaign SQLite databases through
`CampaignReadModel`.

```bash
swf-ui [--cwd PATH] [--workspace PATH] [--bind LOOPBACK] [--port N] [--control]
```

Defaults are the current repository's `.software-factory/workspace`,
`127.0.0.1`, and port `4173`.

The server exposes health, Campaign detail/list, phases, agents, checks,
findings, results, events, session logs, and `/api/campaigns/:id/live`.
The live view reports active processes, the current model/tool stage, elapsed
time, progress counters, heartbeat age, and stale status. Reads are
cursor-paginated where appropriate and remain live while SQLite WAL writers
are active.

Each agent also writes the SDK event stream as it arrives to
`sessions/<agent-run>/raw-events.jsonl`. This generated local diagnostic can
be tailed when investigating a quiet run; its raw content is intentionally not
served by the visualizer API.

Without `--control`, non-GET requests are rejected. Control mode adds only
`POST /api/campaigns/:id/approve-plan`, protected by a per-process token and
restricted to loopback.

`swf` never imports, builds, probes, or starts this package.
