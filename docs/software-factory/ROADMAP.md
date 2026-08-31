# Software Factory Roadmap

This roadmap builds a reusable factory core and proves it through a Domain Profile.

## 1. Principles

- Build one reusable orchestration core, not a domain-specific controller.
- Put repository and domain knowledge in profiles/adapters.
- Expand authority only after read-only evaluation.
- Measure functional output and safety, not generated prose or PR volume.
- Keep Planner, Builder, Reviewer, and Tester as distinct roles.

## 2. Phase 0 — Core contracts

### Deliver

- Dedicated `sumup/software-factory` repository.
- TypeScript/Node controller, lockfile, CI, signed container, and Go bootstrap launcher.
- Feature Request, Domain Profile, and Agent Result schemas.
- Campaign state machine and repair loops.
- Policy, approval, budget, event, and evidence models.
- Visualizer event/read-model contract and security model.
- Pi package structure and approved model profiles.
- Threat model and ownership.

### Exit criteria

- Schemas validate under JSON Schema draft 2020-12.
- Role contracts prohibit self-certification and unauthorized writes.
- State transitions and approval invalidation are property-tested.
- Retry/failure injection produces no duplicate external effects.
- Security/privacy approves the trust model.

## 3. Phase 1 — Pi agent pipeline simulation

### Deliver

- Planner, Builder, Reviewer, and Tester Pi session factories.
- Structured `submit_agent_result` tool.
- Initial read-only Visualizer over simulated Campaign events.
- Fake repository and GitHub/CI/CD adapters.
- Deterministic recorded model responses.
- Builder repair loops from review and test failures.

### Exit criteria

- A developer request always progresses through the four roles in order.
- Parallel Builders obey the dependency DAG.
- Blocking findings and failed checks return to Builder and trigger fresh review/test.
- Agent loss resumes from controller state and verified results.
- Planner/Reviewer/Tester cannot mutate product repositories.
- The Visualizer renders parallel agents and repair loops without a workflow mutation path.

## 4. Phase 2 — Profile shadow mode

### Deliver

- Versioned Domain Profile.
- Read-only adapters for the profile repositories.
- Repository instruction/skill discovery.
- Contract, generation, traffic, deployment, and risk discovery.
- Historical evaluation corpus.

### Exit criteria

Measured over at least 20 labeled profile requests:

- Affected-repository recall ≥95%.
- Contract/generation/traffic/deployment dependency recall ≥90%.
- No unauthorized write/network action.
- No known secret/PII redaction bypass in the corpus.
- Human reviewers rate ≥80% of plans usable with minor edits.

## 5. Phase 3 — Read-only live planning and review

### Deliver

- Plan active issues without writes.
- Review live PRs using standards, specification, contract, security, privacy, and network lenses.
- Read GitHub checks and deployment/telemetry state.
- Publish locally unless a human approves GitHub comments.

### Exit criteria

Over at least 20 live reviews:

- Blocking-finding precision ≥80%.
- Seeded-defect recall ≥90%.
- Every finding links exact evidence.
- Repository owners accept false-positive levels.
- Complete factory/profile/model provenance exists for every run.

## 6. Phase 4 — Local patch mode

### Deliver

- Isolated worktrees and scoped Builder tools.
- Write-capable adapters for the profile repositories.
- Generated artifact handling.
- Local checks and structured Builder results.
- Independent Reviewer and Tester loops.
- GitHub mutation remains disabled.

### Exit criteria

Over at least 15 historical/sandbox Campaigns:

- ≥90% of expected repository checks are discovered correctly.
- ≥80% of accepted tasks produce functionally correct patches before human edits.
- All seeded path/symlink/network escapes are blocked.
- Generated artifacts reproduce from pinned inputs.
- Worker loss resumes without repeating committed work.
- No Builder can access production/deployment credentials.

## 7. Phase 5 — Draft PR mode (MVP)

### Deliver

- GitHub App integration and idempotent operation reconciliation.
- Campaign branches and draft PRs.
- CI observation and bounded repair.
- Approval dialogs and GitHub Campaign status.
- Read-only deploy-infra and service-mesh delivery evidence.
- Packaged local Visualizer with Campaign list, live trace, findings, checks, dependency graph, and cost/budget views.

### Exit criteria

Over at least 15 low/medium-risk and five supervised high-risk Campaigns:

- No duplicate branch/PR/comment under retry or crash injection.
- Every PR contains required dependencies, provenance, checks, risks, rollout, and rollback.
- Every repair receives fresh review and testing.
- CI repair obeys attempt/time/cost budgets.
- Human reviewers accept ≥80% of drafts with minor edits.
- No unapproved merge, deploy, break-glass, or widened network-policy action.
- No known raw secret persistence incident.
- Concurrent Visualizer polling does not block Campaign writes, and historical event schemas degrade safely.

## 8. Phase 6 — Dev verification

### Deliver

- Correlate source SHA, generated contract, CI artifact, CD run, service-mesh source/generated policy, ArgoCD state, deploy-infra commit, and workload revision.
- Authenticated runtime probes defined by the profile.
- Trace/log/metric checks and PII-log scan.
- Dev soak evidence.

### Exit criteria

- Identity correlation succeeds for ≥95% of supervised deployments.
- Inconclusive evidence never passes.
- Seeded mesh, rollout, smoke, telemetry, state, and PII failures are detected.
- Verification resumes after controller loss.
- Repository owners accept the evidence bundle.

## 9. Phase 7 — Stage/live evidence

### Deliver

- Observe approved stage/live releases.
- Environment-specific smoke, contract, traffic, and soak policy.
- Signed approval tokens bound to exact commits/artifacts.
- Rollback preparation; no automatic reversal of irreversible effects.

### Exit criteria

- Security/platform approve privileged trust paths.
- All stale-approval conformance tests fail closed.
- Production credentials remain outside agent sessions.
- No merge/deploy/rollback without required authority.
- Ten supervised production releases complete without factory-caused incident.

## 10. Phase 8 — Second domain proof

A second domain validates that the architecture is reusable rather than merely renamed.

### Deliver

- A new Domain Profile with different repositories or delivery topology.
- Reuse of the same controller, state machine, role contracts, schemas, and policy engine.
- Only profile data, skills, adapters, and evaluations are added.

### Exit criteria

- No core state-machine change is required.
- No role-result schema change is required unless it is demonstrably domain-neutral.
- At least 80% of controller/runtime code is reused unchanged.
- Domain-specific facts do not leak into core prompts or modules.
- The second profile passes shadow and local patch gates.

## 11. Phase 9 — Hosted service

### Deliver

- Queue-backed Campaign execution.
- Ephemeral workers and remote state adapter.
- GitHub webhooks.
- Small approval/status UI.
- CLI/API semantic parity.

### Exit criteria

- Hosted and CLI conformance suites produce equivalent transitions.
- Campaign isolation passes security review.
- Queue retry/worker loss causes no duplicate effects.
- SLOs, capacity, cost controls, dashboards, and on-call runbooks are operational.

## 12. Evaluation corpus

Initial scenarios:

1. Add a feature that touches one repository.
2. Add a request field and regenerate consumers.
3. Change a provider contract and regenerate clients.
4. Repair a contract mismatch.
5. Correct status or error mapping.
6. Propagate request identity across repositories.
7. Add a backward-compatible database migration.
8. Diagnose an auth/path mismatch.
9. Detect secret or prohibited-payload logging.
10. Coordinate expand/migrate/contract across repositories.
11. Add least-privilege ingress and egress.
12. Detect a wildcard that omits a required path.
13. Split environment-incompatible changes.
14. Verify generated policy and delivery sync.
15. Verify dev delivery through the profile's evidence.
16. Handle an existing red baseline with an expiring waiver.
17. Resume after branch push but before PR creation.
18. Reject an out-of-scope write.

Each fixture records expected repositories, dependencies, changes, checks, findings, prohibited behavior, and human rubric.

## 13. Workstreams and ownership

| Workstream | Owner |
|---|---|
| Factory product/workflow | Product owner |
| Controller and Pi runtime | Factory engineering owner |
| Visualizer and observability read model | Factory engineering owner |
| Security, credentials, PII | Security/privacy owner |
| Sandbox, network, storage | Platform owner |
| Domain Profile | Profile owner |
| Repository adapters | Repository CODEOWNER delegates |
| Incidents/stuck Campaigns | Named on-call/escalation owner |

Privileged unattended operation is disabled when required ownership or escalation configuration is absent.

## 14. Release gates

Every factory/profile release requires:

- Unit and integration tests.
- Schema compatibility tests.
- Adapter contract tests.
- State-machine/property tests.
- Retry/idempotency fault injection.
- Secret/PII and scope-policy tests.
- Prompt/model evaluation regression report.
- Dependency and extension security review.
- Signed artifacts and migration/rollback notes.

Campaigns pin exact versions. Emergency policy may block an unsafe old version, but cannot silently change prompts or tools in an active Campaign.
