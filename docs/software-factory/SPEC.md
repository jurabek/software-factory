# Software Factory Specification

Status: Proposal  
Reference profile: [LOCAL_PROFILE.md](LOCAL_PROFILE.md)  
Factory roster: [`config.yaml`](../../config.yaml)  
Execution engine: Pi SDK

## 1. Purpose

The Software Factory turns a developer’s feature request into reviewed and tested repository changes. The factory core is domain-neutral. Domain-specific behavior is supplied by a versioned **Domain Profile** and the agent roster in `config.yaml`.

The starter `local` profile coordinates whatever repositories you list. It does not assume a product domain.

The factory MUST support other domains by adding profiles and repository adapters, without changing the orchestration state machine or agent contracts.

Normative terms **MUST**, **SHOULD**, and **MAY** have their RFC 2119 meanings.

The factory includes a live, read-only [Visualizer](VISUALIZER.md) for Campaign phases, agents, tools, findings, checks, costs, dependencies, and delivery evidence. It observes the workflow but has no workflow-control authority.

## 2. Core workflow

The primary workflow is fixed:

```text
Developer request
      ↓
Planner
      ↓
Builder(s)
      ↓
Reviewer(s)
      ↓
Tester
      ↓
Green draft PR set + delivery evidence
```

Repair loops are explicit:

```text
Reviewer finds defect ──→ Builder repairs ──→ Reviewer re-reviews
Tester finds failure  ──→ Builder repairs ──→ Reviewer re-reviews ──→ Tester retests
```

No agent may skip a downstream role or certify its own output.

### 2.1 Developer

The developer supplies an issue, free-form request, or structured Feature Request. The developer owns product intent and approves high-risk or multi-repository plans.

### 2.2 Planner

The Planner MUST:

1. Normalize the request against [FEATURE_REQUEST.schema.json](FEATURE_REQUEST.schema.json).
2. Load the selected Domain Profile.
3. Inspect repositories at immutable base SHAs.
4. Identify affected repositories, contracts, traffic, generated artifacts, tests, rollout, and rollback.
5. Produce acceptance criteria, write scopes, a dependency DAG, risks, unresolved decisions, and required approvals.
6. Stop rather than guess when ambiguity affects auth, contracts, migrations, secrets, network policy, or irreversible behavior.

The Planner is read-only with respect to product repositories.

### 2.3 Builder

A Builder receives one approved work item and one isolated repository worktree. It MUST:

1. Load repository instructions and profile guidance.
2. Change only approved paths.
3. Use repository-owned generators and commands.
4. Add or update tests.
5. Run available local checks.
6. Return a structured result conforming to [AGENT_RESULT.schema.json](AGENT_RESULT.schema.json).

The controller MAY run independent Builders in parallel according to the approved DAG.

### 2.4 Reviewer

A Reviewer is a separate read-only Pi session. It receives the approved request, diff, repository guidance, contract context, and Builder evidence—but not the Builder’s hidden reasoning.

Review MUST cover:

- Request and acceptance-criteria correctness.
- Repository standards and ownership rules.
- Cross-repository compatibility.
- Security, privacy, auth, and network policy when the profile requires them.
- Scope discipline and generated-artifact provenance.

Blocking findings return work to the Builder. Review approval does not replace human CODEOWNERS approval.

### 2.5 Tester

The Tester runs after blocking review findings are resolved. It MUST:

1. Build a test plan from acceptance criteria and Domain Profile gates.
2. Run all available required gates.
3. Mark unavailable gates `deferred`, never `passed`.
4. Correlate results to exact source SHAs.
5. Perform integration, contract, smoke, and deployment checks when required and authorized.
6. Certify the work only when every required gate is passed or validly waived.

The Tester is read-only except for test artifacts. A failed test returns work to a Builder, followed by fresh review and retest.

## 3. Domain Profile

A Domain Profile is configuration and guidance, not a fork of the factory. It defines:

- Domain identity, owners, and risk defaults.
- Repositories and default branches.
- Repository adapters.
- Context files and skills to load.
- Allowed write-path patterns.
- Contract and generation relationships.
- Required quality gates.
- Environment and service topology.
- Observability and deployment providers.
- Approval rules.
- Evaluation scenarios.

Profiles MUST be versioned and pinned in every run. Profile changes run regression evaluations before release.

The core MUST NOT contain product-domain repository paths, endpoint assumptions, or team-specific commands. Those belong in a Domain Profile and its adapter implementations.

## 4. Feature Request

Every input MUST become a versioned Feature Request before building. Required content includes:

- Request ID, source, owner, and selected Domain Profile/version.
- Business outcome and non-goals.
- Observable acceptance criteria.
- Risk classification.
- Repository work items and immutable base SHAs.
- Approved write paths.
- Contract, data, traffic, and generation impact.
- Dependency DAG.
- Required checks and environments.
- Rollout, observability, and rollback.
- Budgets, approvals, waivers, and unresolved decisions.

Unknown values MUST be explicit unresolved entries. Plan approval binds to the canonical request revision hash. Amendments create a new revision and invalidate affected approvals and results.

## 5. Autonomy and approval

The factory MAY autonomously:

- Discover and plan.
- Create isolated worktrees and Pi sessions.
- Build approved work items.
- Run reviews and tests.
- Push campaign branches.
- Open draft PRs.
- Observe CI and perform bounded repairs.
- Perform read-only deployment verification.

Human approval is required before:

- Building an unapproved multi-repository or high-risk plan.
- Expanding write scope.
- Accepting a breaking contract, migration, auth change, network-policy widening, or waiver.
- Marking PRs ready, merging, deploying, or rolling back production.
- Executing any break-glass repository write.

Approvals MUST bind to the request revision, relevant source SHAs, generated contract/policy digests, action, actor, and expiry.

## 6. Isolation and identity

The factory MUST use:

- A TypeScript controller embedding Pi through the SDK.
- A version-pinned Pi package for extensions, skills, prompts, and policy.
- A separate persistent Pi session and Git worktree per agent assignment.
- Role-specific model and tool profiles.
- A GitHub App or workload identity with short-lived scoped credentials.

The controller MUST invoke GitHub authentication, PR, checks, run, and API operations through the preinstalled `gh` CLI. Git push authentication MUST be configured through `gh auth setup-git`; tokens MUST NOT be copied into command arguments or persisted as evidence.

Planner, Reviewer, and Tester sessions MUST NOT have product-code write tools. A Builder receives access only to one approved worktree. Production credentials MUST NOT enter Builder sessions.

Every run records factory, Pi, profile, prompt/skill, model, reasoning-level, source-SHA, and sandbox-image provenance.

## 7. Campaign and dependency model

One feature delivery is a **Campaign**. A Campaign contains:

- One Feature Request revision.
- One or more repository work items.
- A directed acyclic graph of contract, generation, build, merge, deployment, and cleanup dependencies.
- Agent runs and structured results.
- Checks, reviews, approvals, waivers, and evidence.

Cross-repository breaking changes MUST use expand/migrate/contract by default:

1. Provider expands compatibly.
2. Provider is validated and deployed.
3. Consumers migrate and deploy.
4. Compatibility telemetry is observed.
5. Removal occurs in a separate approved request.

## 8. Quality gates

The Domain Profile selects applicable gates from these classes:

- Format and compile.
- Unit and race tests.
- Source and architecture lint.
- Generated-artifact drift.
- OpenAPI/schema validation.
- Consumer/provider contracts.
- Database migrations.
- Integration and end-to-end tests.
- Service-mesh generation and analysis.
- Security and PII checks.
- Repository CI.
- Smoke, deployment, and soak verification.

A baseline failure requires an issue-linked, owned, expiring waiver with exact evidence and permitted phases. New failures cannot reuse an unrelated waiver.

Repair is bounded by attempts, elapsed time, model cost, and storage. Repeating the same failure without new evidence MUST stop and escalate.

## 9. GitHub output

The factory creates one focused draft PR per repository work item, or more where repository policy requires environmental separation. Each PR MUST include:

- Outcome, scope, and non-goals.
- Request and Campaign links.
- Dependency and rollout order.
- Contract, data, and traffic impact.
- Generated-artifact provenance.
- Review and test results.
- Deferred checks and waivers.
- Security/privacy assessment.
- Deployment and rollback plan.
- Factory, profile, Pi, and model provenance.

Factory-generated reviews never satisfy required human or CODEOWNERS approvals.

## 10. Security and privacy

Policy MUST be enforced by controller code and Pi tool interception, not prompts alone:

- Canonical-path write scoping and symlink-escape prevention.
- Command and network allow-lists by role/profile.
- Secret masking before model context and persistence.
- PII scanning of diffs, logs, results, and evidence.
- Prohibition of logging secrets or complete sensitive payloads.
- Short-lived credentials and no production authority for Builders.
- Attributable privileged approvals.
- Audit records for tool calls and external mutations.

Raw secrets and raw PII MUST never be persisted as evidence.

## 11. State, recovery, and evidence

The controller persists workflow state in SQLite plus append-only events. Pi JSONL sessions are attached evidence, not the workflow database.

Every external mutation uses a stable operation key. On retry or recovery the controller reconciles GitHub, Git, CI/CD, and deployment state before acting, preventing duplicate branches, commits, PRs, comments, or deployments.

Every agent handoff uses [AGENT_RESULT.schema.json](AGENT_RESULT.schema.json). Claims about changed files, commands, checks, and Git state MUST be verified by the controller.

## 12. State machine

```text
received
→ planning
→ awaiting_plan_approval
→ building
→ reviewing
→ repairing_review
→ testing
→ repairing_test
→ opening_prs
→ validating_ci
→ awaiting_human_review
→ implementation_complete
→ awaiting_deploy_approval
→ deploying
→ verifying
→ soaking
→ shipped
```

Exceptional states:

```text
blocked | paused | failed | aborting | aborted | rolling_back | rolled_back
```

Transitions have explicit entry conditions, completion criteria, timeout, retry policy, evidence, and approval invalidation rules.

## 13. Fresh-sandbox contract

The initial sandbox may assume only Linux, Git, GitHub CLI, Go, GitHub connectivity, and a short-lived bootstrap credential.

A signed Go bootstrap launcher MUST install or select pinned Node.js, Pi, factory package, schema tools, and optional repository tooling. It emits a Capability Report. Missing privileged or external capabilities defer affected checks to trusted CI; they are never silently installed or considered passed.

## 14. MVP

Version 1 delivers:

- Developer intake and Feature Request approval.
- Planner, Builder, Reviewer, and Tester agents.
- Profile-driven repository discovery.
- Isolated worktrees and Pi sessions.
- Local checks, generation, independent review, and testing.
- Draft PR creation.
- CI observation and bounded repair.
- Read-only dev deployment verification.
- A local-first, read-only Campaign Visualizer.
- A complete Domain Profile for the target repositories.

Version 1 excludes autonomous merge, autonomous stage/live deployment, production rollback, a workflow-control web UI, and unprofiled repository support.

## 15. Acceptance criteria

The design is implemented when:

1. A new domain can be added through a Domain Profile and adapters without changing the core state machine or agent-result contract.
2. A developer request reliably invokes Planner → Builder → Reviewer → Tester.
3. Blocking review or test failures enter the correct repair loop.
4. Agent roles cannot exceed their tool, repository, path, credential, or network scope.
5. A fresh sandbox can install pinned runtime dependencies and emit a Capability Report.
6. Interrupted Campaigns resume without duplicate external mutations.
7. Required unavailable checks remain deferred until trusted infrastructure reports them.
8. Profile evaluation scenarios produce coordinated, reviewed, tested draft changes across the necessary repositories.
9. Deployment evidence correlates source, contracts, traffic policy, artifacts, runtime, and telemetry.
10. The Visualizer renders live Planner, Builder, Reviewer, Tester, repair, check, cost, and delivery state without workflow mutation authority.
11. Security, approval, and profile regression evaluations pass the rollout thresholds in [ROADMAP.md](ROADMAP.md).

## 16. Related documents

- [ARCHITECTURE.md](ARCHITECTURE.md): reusable modules, agent runtime, state, and adapter seams.
- [AGENT_WORKFLOW.md](AGENT_WORKFLOW.md): executable Planner → Builder → Reviewer → Tester protocol.
- [VISUALIZER.md](VISUALIZER.md): live read-only Campaign observability UI.
- [EASY_USE.md](EASY_USE.md): the `swf init` / `swf request` UX and per-repo AGENTS.md block contract.
- [FEATURE_REQUEST.schema.json](FEATURE_REQUEST.schema.json): developer request and approved plan contract.
- [AGENT_RESULT.schema.json](AGENT_RESULT.schema.json): Planner/Builder/Reviewer/Tester result contract.
- [RUNBOOK.md](RUNBOOK.md): operating the factory on repository contexts.
- [ROADMAP.md](ROADMAP.md): implementation and adoption stages.
