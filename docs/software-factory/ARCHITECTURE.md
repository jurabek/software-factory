# Software Factory Architecture

This document defines the reusable implementation shape for [SPEC.md](SPEC.md). Domain behavior is plugged in through [DOMAIN_PROFILE.schema.json](DOMAIN_PROFILE.schema.json) and the agent roster in [`config.yaml`](../../config.yaml). The starter profile is [LOCAL_PROFILE.md](LOCAL_PROFILE.md).

## 1. Architectural rule

The controller is deterministic; agents are probabilistic.

The controller owns:

- Workflow state and role ordering.
- Request/profile/schema validation.
- Tool, path, credential, network, and budget policy.
- Agent creation and isolation.
- Approval checks.
- Git/GitHub/CI/CD mutations.
- Idempotency, reconciliation, and evidence.

Agents own:

- Planning and implementation reasoning.
- Code changes within granted scope.
- Review findings.
- Test selection and diagnosis.
- Structured recommendations/results.

An agent cannot authorize an action, change state directly, or grant itself more scope.

## 2. Core interface

```ts
interface SoftwareFactory {
  request(input: DeveloperInput): Promise<CampaignRef>;
  advance(campaign: CampaignRef, target?: FactoryState): Promise<CampaignStatus>;
  approve(input: Approval): Promise<CampaignStatus>;
  inspect(campaign: CampaignRef): Promise<CampaignStatus>;
  pause(campaign: CampaignRef, reason: string): Promise<CampaignStatus>;
  abort(campaign: CampaignRef, reason: string): Promise<CampaignStatus>;
}
```

This small interface hides planning, Pi sessions, repository adapters, dependency scheduling, GitHub, checks, and deployment providers.

## 3. Runtime topology

```text
Developer / GitHub issue
          │
          ▼
  CLI / future HTTP adapter
          │
          ▼
   Campaign Controller ───── Policy + Approval Engine
          │
          ├──── Feature Request + Domain Profile
          │
          ▼
       Planner Pi (read-only)
          │ plan + DAG
          ▼
   Scheduler / Budget Manager
          │
          ├──── Builder Pi: repository A worktree
          ├──── Builder Pi: repository B worktree
          └──── Builder Pi: repository N worktree
                         │
                         ▼
                 Reviewer Pi sessions
                         │ findings
                    repair loop
                         │
                         ▼
                    Tester Pi
                         │ failures
                    repair loop
                         │
                         ▼
           GitHub / CI / CD / runtime evidence
```

Every Pi assignment has a persistent session, isolated worktree, explicit role, model profile, active-tool set, completion criterion, and structured result.

## 4. Modules

### 4.1 Campaign Controller

Validates transitions and orchestrates the fixed role sequence. It delegates repository behavior to adapters and never embeds domain-specific paths or commands.

### 4.2 Feature Request Module

Validates [FEATURE_REQUEST.schema.json](FEATURE_REQUEST.schema.json), canonicalizes revisions, hashes approvals, tracks amendments, and computes invalidation.

### 4.3 Domain Profile Registry

Loads pinned profiles conforming to [DOMAIN_PROFILE.schema.json](DOMAIN_PROFILE.schema.json). A profile declares repository adapters, default risks, quality gates, contract/traffic relationships, providers, and evaluation scenarios.

A profile is data plus referenced skills/adapters. It cannot weaken global security invariants.

### 4.4 Planner Runtime

Creates a read-only Pi session with:

- Developer request.
- Domain Profile.
- Repository metadata and context files.
- Read-only Git/GitHub tools.
- `submit_agent_result` as the required completion tool.

The Planner result contains the normalized request, repository work items, DAG, acceptance criteria, risks, gates, and unresolved decisions.

### 4.5 Scheduler

Runs ready Builder work items according to DAG edges. It enforces global and per-repository concurrency, budget, cancellation, retry, and repair ordering.

### 4.6 Builder Runtime

Creates one Pi session per repository work item. The Builder receives:

- Only its approved slice of the Feature Request.
- Predecessor results and immutable artifact digests.
- Repository instructions and profile skill pointers.
- Scoped read/edit/write/bash tools.
- A worktree rooted at the approved base SHA.

The Builder cannot access sibling worktrees directly. Cross-repository facts arrive through verified results/artifacts.

### 4.7 Review Coordinator

Runs one or more independent read-only sessions after Builders finish locally:

- **Specification review** checks acceptance criteria and non-goals.
- **Standards review** checks repository instructions and architecture.
- **Contract review** checks provider/consumer, event, data, and network compatibility.
- **Security review** is enabled by risk/profile policy.

The coordinator consolidates exact, actionable findings. Blocking findings generate Builder repair work. Repaired work always gets fresh review.

### 4.8 Tester Runtime

Runs after review is clear. It receives the approved request, final reviewed SHAs/diffs, profile gates, capability report, and Builder evidence.

The Tester may execute read-only commands and create test artifacts. It cannot modify product code. It returns one status for every required gate: `passed`, `failed`, `deferred`, or `waived`.

A failure creates Builder repair work and invalidates affected review/test results.

### 4.9 Repository Adapter Registry

Each repository adapter satisfies:

```ts
interface RepositoryAdapter {
  detect(ctx: ReadContext): Promise<Detection>;
  bootstrap(ctx: BootstrapContext): Promise<BootstrapResult>;
  loadInstructions(ctx: ReadContext): Promise<InstructionSet>;
  discoverScope(ctx: DiscoveryContext): Promise<ScopeProposal>;
  plan(ctx: PlanContext): Promise<RepositoryPlan>;
  generate(ctx: WorkContext): Promise<CheckResult[]>;
  format(ctx: WorkContext): Promise<CheckResult[]>;
  build(ctx: WorkContext): Promise<CheckResult[]>;
  unitTest(ctx: WorkContext): Promise<CheckResult[]>;
  integrationTest(ctx: WorkContext): Promise<CheckResult[]>;
  contractTest(ctx: WorkContext): Promise<CheckResult[]>;
  lint(ctx: WorkContext): Promise<CheckResult[]>;
  securityCheck(ctx: WorkContext): Promise<CheckResult[]>;
  preparePullRequest(ctx: PullRequestContext): Promise<PullRequestPlan>;
  deploymentImpact(ctx: ReadContext): Promise<DeploymentImpact>;
  collectEvidence(ctx: ReadContext): Promise<EvidenceRef[]>;
}
```

Adapters discover actual repository commands from pinned checkouts. The interface is stable across domains; adapter implementation hides repository conventions.

### 4.10 Policy Engine

Policy evaluates every requested operation against:

- Agent role.
- Domain Profile/version.
- Campaign/request revision.
- Repository and canonical path.
- Command and destination host.
- Credential class.
- Risk and required approval.
- Remaining budget.

Pi `tool_call` interception applies the same policy as controller-owned external mutations. Denial is fail-closed and returns a stable policy code.

### 4.11 Check and Evidence Module

Normalizes repository commands and external check providers into attributable evidence. It redacts output before model context or persistence and rejects raw secret/PII evidence.

### 4.12 Source Control and GitHub Module

Owns mirrors, worktrees, branches, commits, draft PRs, checks, comments, and workflow observations. Every mutation has a deterministic operation key and reconciliation strategy. The concrete GitHub adapter shells out with explicit argument arrays to authenticated `gh` commands (`gh auth`, `gh pr`, `gh run`, and narrowly scoped `gh api` where needed); it does not embed a separate GitHub client SDK. Git push credentials are provided by `gh auth setup-git`.

### 4.13 Delivery Verification Module

Uses profile-selected ports:

```ts
interface CIProvider {}
interface DeploymentProvider {}
interface ManifestProvider {}
interface ServiceMeshProvider {}
interface TraceProvider {}
interface LogProvider {}
interface MetricProvider {}
interface ArtifactStore {}
interface CredentialProvider {}
```

The core does not assume Kubernetes, ArgoCD, Grafana, Honeycomb, or a particular CD system. Profiles select concrete adapters. The Visualizer consumes normalized controller records and therefore remains profile-neutral; profiles may contribute labels and graph metadata, never executable UI code by default.

### 4.14 Persistence Module

SQLite stores current normalized state; append-only JSON events store durable history. Every Pi session JSONL line is also mirrored into `session_logs` in the same WAL database so tracers can read while an agent is still writing. Agents do not stuff prior envelopes into the next prompt; they query peer sessions over `factory.sock`. Original session files under `sessions/` remain linked evidence.

### 4.15 Visualizer Module

A separate read-only process implements [VISUALIZER.md](VISUALIZER.md). It polls Campaign events using a monotonic row cursor and derives phase timelines, agent lanes, costs, findings, checks, dependencies, and delivery graphs.

The Visualizer has no controller command channel and no Campaign database write connection. Optional UI archive preferences use separate storage. This seam allows the local SQLite reader to be replaced by a hosted read API without changing the UI model.

## 5. Role result contract

Every agent finishes by invoking `submit_agent_result` with [AGENT_RESULT.schema.json](AGENT_RESULT.schema.json).

Common fields include:

- Campaign, request revision/hash, profile/version, role, run, and Pi session.
- Immutable inputs.
- Decisions and unresolved questions.
- Files/contracts/traffic changed or reviewed.
- Commands/checks/evidence.
- Findings and risks.
- Git state.
- Recommended next actions.

Role-specific constraints are enforced by controller validation:

- Planner MUST return a plan and no changed product files.
- Builder MAY return changed files and MUST return local checks.
- Reviewer MUST return findings/verdict and no changed product files.
- Tester MUST return complete required-gate statuses and no changed product files.

Controller verification, not agent assertion, determines accepted Git/check state.

## 6. State machine and repair loops

```text
received
planning
awaiting_plan_approval
building
reviewing
repairing_review
re_reviewing
testing
repairing_test
re_reviewing_after_test
re_testing
opening_prs
validating_ci
repairing_ci
re_reviewing_after_ci
re_testing_after_ci
awaiting_human_review
implementation_complete
awaiting_deploy_approval
deploying
verifying
soaking
shipped
```

Exceptional states:

```text
blocked | paused | failed | aborting | aborted | rolling_back | rolled_back
```

A transition definition contains:

```ts
type Transition = {
  from: FactoryState[];
  to: FactoryState;
  preconditions: Predicate[];
  approvals: ApprovalKind[];
  effect?: IdempotentEffect;
  completion: Predicate[];
  timeoutMs: number;
  retry: RetryPolicy;
  invalidates: ResultKind[];
};
```

The controller, not an agent, chooses the next state.

## 7. Campaign workspace

```text
/workspace/<campaign-id>/
├── campaign.db
├── factory.sock
├── events/
├── requests/
├── profiles/
├── results/
├── evidence/
├── artifacts/
├── sessions/
├── visualizer-preferences/
├── mirrors/
└── worktrees/
    └── <repository-id>/<work-item-id>/
```

Secrets are mounted outside evidence and session paths. Agents receive only role-required environment variables.

## 8. Idempotency and reconciliation

External operation keys use:

```text
<campaign>/<request-revision>/<work-item>/<operation>/<scope>
```

Before mutation, the controller reconciles:

- Branch and commit trailers.
- PR labels/head/base.
- Check and workflow SHAs.
- Bot-generated commits.
- Approval bindings.
- Deployment artifact identity.
- Profile-specific generated and runtime state.

Matching effects are adopted. Conflicting effects block; they are never overwritten automatically.

## 9. Profile inheritance

Profiles MAY extend a shared base profile:

```text
base-service
  ├── team-a-domain
  └── team-b-domain
```

Inheritance is merge-by-key with these rules:

- Security invariants are additive and cannot be removed.
- Repositories, checks, and approval rules have stable IDs.
- Overrides record provenance.
- Final resolved profile is validated and hashed.
- Campaigns pin the fully resolved profile digest.

Prefer composition over deep inheritance. Shared repository adapters should be reusable packages referenced by multiple profiles.

## 10. Fresh-sandbox bootstrap

A small signed Go launcher uses preinstalled `gh` to fetch signed release metadata. It verifies and installs:

- Pinned Node.js runtime.
- Pi and factory Pi package.
- Controller CLI.
- Schema validators.
- Profile-required tools that need no privileged credentials.

It emits an installation receipt and Capability Report. Container execution is preferred when an approved runtime exists; local isolated installation is the fallback.

## 11. Security seams

- Built-in Pi tools are wrapped with canonical path and role policy.
- Bash receives a sanitized environment and process-tree cancellation.
- Network policy is enforced by sandbox infrastructure as well as tool policy.
- Tool output is truncated and redacted before model use.
- GitHub tokens are repository- and operation-scoped.
- Review/Test roles use read-only credentials.
- Production approval credentials never enter agent sessions.
- Profile code/packages are pinned and reviewed because extensions execute with process authority.

## 12. Reference instantiation

A Domain Profile demonstrates the general model:

```text
Developer asks for a feature
  → Planner identifies affected repositories and checks
  → Builders implement ready repository work items
  → Reviewers check request, repository rules, contracts, and profile risk
  → Tester runs the profile's required checks
  → Controller records evidence (remote PR/deploy remain out of local mode)
```

Repository paths, commands, identities, and topology belong in the Domain Profile and `config.yaml`, not in core modules.
