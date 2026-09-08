## Software Factory

- In this current phase all Database or Modules can be destructive. If you change schemas or DB. You don't need to deal with migrations.

<!-- software-factory:start -->
```yaml
checks:
  - id: go-test
    command: go -C daemon test ./...
  - id: go-race
    command: go -C daemon test -race ./...
  - id: typecheck
    command: npm run typecheck
  - id: build
    command: npm run build
generated:
  - .workspace/
  - dist/
  - node_modules/
protected: []
# risk_signals: []   # optional per-repo override; global defaults apply otherwise
```
<!-- software-factory:end -->
