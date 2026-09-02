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
findings, results, events, and session logs. Reads are cursor-paginated where
appropriate and remain live while SQLite WAL writers are active.

Without `--control`, non-GET requests are rejected. Control mode adds only
`POST /api/campaigns/:id/approve-plan`, protected by a per-process token and
restricted to loopback.

`swf` never imports, builds, probes, or starts this package.
