# Development Runbook

## Required checks

```bash
npm install
npm test
npm run typecheck
npm run build
```

## Executable smoke test

```bash
npm link --workspace @software-factory/cli
npm link --workspace @software-factory/ui

SOFTWARE_FACTORY_RUNTIME=fake swf request "smoke test"
swf approve SF-YYYY-NNNN
SOFTWARE_FACTORY_RUNTIME=fake swf run SF-YYYY-NNNN
swf status SF-YYYY-NNNN

swf-ui
```

Use `swf-ui --control` to verify local plan approval. Use another loopback
port when `4173` is occupied.

## Generated paths

Do not edit or commit `node_modules/`, package `dist/` directories, or
`.software-factory/` Campaign workspaces.

## Compatibility

This refactor intentionally has no migration for Campaigns persisted in
removed remote lifecycle states. The store reports the unsupported state;
start a new Campaign.
