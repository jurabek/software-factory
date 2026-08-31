# Agent Workflow Contract

This is the executable role protocol for the Software Factory. The controller invokes these roles in order and validates every completion through [AGENT_RESULT.schema.json](AGENT_RESULT.schema.json).

## 1. Shared agent envelope

Every role receives:

- Campaign ID.
- Approved Feature Request revision/hash.
- Resolved Domain Profile version/digest.
- Role and work-item ID.
- Immutable input references.
- Allowed repositories, paths, tools, commands, network destinations, and credentials.
- Budget and deadline.
- Completion criterion.

Every role MUST finish with `submit_agent_result`. A prose claim without a valid result is incomplete.

## 2. Planner

### Input

- Developer request.
- Domain Profile.
- Read-only repository checkouts/metadata.
- Repository instructions, skills, workflows, generators, ownership, and baseline check state.

### Mission

Transform intent into an implementable, testable, and reviewable Campaign.

### Required work

1. Restate business outcome and non-goals.
2. Write observable acceptance criteria.
3. Discover every affected repository.
4. Identify contract, generated artifact, data, network, deployment, and rollback impact.
5. Pin base SHAs and external contract artifacts.
6. Define one scoped work item per repository/PR.
7. Build an acyclic dependency graph.
8. Select profile checks and required environments.
9. Classify risk and approvals.
10. List unresolved decisions with owner and blocking state.

### Stop conditions

Stop and return `blocked` when unresolved intent affects auth, secrets, contracts, migrations, network trust, acceptance criteria, or irreversible behavior.

### Completion criterion

A schema-valid Feature Request revision exists; every acceptance criterion maps to one or more checks; every repository write maps to one work item; and every dependency, risk, unresolved decision, rollout step, and rollback step is explicit.

## 3. Builder

### Input

- One approved work item.
- Its acceptance criteria and checks.
- Predecessor results/artifacts.
- One isolated worktree.
- Repository/profile instructions.

### Mission

Implement only the assigned work item and leave it ready for independent review.

### Required work

1. Verify worktree/base SHA and instructions before editing.
2. Inspect existing patterns.
3. Implement the smallest coherent change satisfying assigned criteria.
4. Run repository-owned generators.
5. Add or update tests for changed behavior and failure paths.
6. Run all locally available assigned checks.
7. Inspect the final diff for scope, generated provenance, secrets, and PII.
8. Submit exact changed files, commands, check results, risks, and Git state.

### Stop conditions

Stop and return `blocked` for scope expansion, stale contract/input, ambiguous high-risk behavior, missing required approval, policy denial, or incompatible predecessor output.

### Completion criterion

The assigned behavior is implemented in approved paths; generated artifacts are current; available local checks have evidence; the worktree state is exact; and no unresolved blocking issue is hidden.

## 4. Reviewer

### Input

- Approved Feature Request.
- Domain Profile and repository instructions.
- Builder diff/commit and verified evidence.
- Relevant cross-repository contract and traffic artifacts.

The Reviewer does not receive Builder hidden reasoning as authority.

### Mission

Find defects before testing and human review.

### Required lenses

- **Spec**: acceptance criteria, non-goals, edge cases, status semantics.
- **Standards**: repository architecture, conventions, ownership, generation.
- **Contract**: API/event/data/network compatibility and rollout order.
- **Security/privacy**: auth, authorization, identity, secrets, logging, network widening.
- **Scope**: unrelated changes, missing generated artifacts, unjustified dependencies.

### Findings

Every blocking finding includes severity, category, exact location/artifact, impact, evidence, and required correction. Optional suggestions are clearly non-blocking.

### Completion criterion

Every required review lens has a verdict; all findings are structured and evidence-backed; and the result is either `completed` with no blocking findings or `changes_requested`.

## 5. Tester

### Input

- Approved Feature Request and Domain Profile.
- Reviewed work-item heads.
- Required-check matrix.
- Capability Report.
- Builder and Reviewer results.

### Mission

Prove the reviewed implementation satisfies acceptance criteria and delivery requirements.

### Required work

1. Map every acceptance criterion to executable checks.
2. Verify check inputs belong to exact reviewed SHAs.
3. Run all available required checks.
4. Defer unavailable checks to their designated trusted executor.
5. Validate contracts and generated artifacts across repositories.
6. Run integration/smoke/runtime checks required by the profile.
7. Record one status and evidence set per check.
8. Refuse certification for stale, missing, inconclusive, or uncorrelated evidence.

### Completion criterion

Every acceptance criterion has evidence; every required check is passed or validly waived; no deferred check remains at a state that requires completion; and source/artifact identities match reviewed inputs.

## 6. Repair loops

### Review repair

```text
Reviewer changes_requested
  → controller creates repair assignment for owning Builder
  → Builder submits new head/result
  → all invalidated Reviewers rerun
  → Tester runs only after review is clear
```

### Test repair

```text
Tester failed
  → controller creates repair assignment for owning Builder
  → Builder submits new head/result
  → affected Reviewers rerun
  → Tester reruns affected and downstream checks
```

### CI repair

CI failure uses the test repair loop. Repair attempts are bounded. A repeated signature without new evidence escalates rather than looping.

## 7. Controller invariants

The controller MUST enforce:

- Planner precedes Builder.
- Builder output is independently reviewed.
- Tester evaluates reviewed output.
- Any code change invalidates affected reviews and tests.
- Any contract/profile/base-SHA change may invalidate the plan approval.
- An agent cannot change its role or tools.
- An agent cannot approve its own result.
- Only controller-verified evidence advances state.
- Parallel Builders run only for DAG-ready work items.
- Human gates remain human even when all agents recommend approval.

## 8. Example

```text
Developer: "Ship the requested change"

Planner:
  selects affected repositories
  pins contracts and base SHAs
  defines required checks and approvals

Builders:
  implement each ready repository work item in isolation
  generate clients/specs through repository workflows

Reviewers:
  detect incorrect semantics, auth/path mismatch, contract drift,
  or prohibited logging

Tester:
  runs the profile check IDs
  verifies reviewed SHAs and the end-to-end path when available

Controller:
  opens ordered draft PRs and reports deferred/human gates
```
