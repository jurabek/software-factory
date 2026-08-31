## Software Factory
<!-- software-factory:start -->
```yaml
checks:
  - id: unit
    command: npm test
  - id: typecheck
    command: npm run typecheck
generated:
  - .workspace/
  - dist/
  - node_modules/
protected: []
# risk_signals: []   # optional per-repo override; global defaults apply otherwise
```
<!-- software-factory:end -->
