# Software Factory Runbook

This runbook operates the factory in [SPEC.md](SPEC.md). Examples use the [local Domain Profile](LOCAL_PROFILE.md).

## 1. Bootstrap a fresh sandbox

Supported initial tools:

- Linux
- Git
- GitHub CLI
- Go
- GitHub connectivity
- Short-lived bootstrap credential

Download the signed Go bootstrap launcher through GitHub Releases, verify its checksum/signature, and run it:

```bash
gh release download <version> \
  --repo your-org/software-factory \
  --pattern 'software-factory-bootstrap-*' \
  --dir /tmp/software-factory-bootstrap
# Verify release metadata, then execute the platform artifact.
```

The launcher selects the signed factory container when an approved container runtime exists. Otherwise it installs pinned Node.js, Pi, the controller, the factory Pi package, and schema tools into an isolated directory.

Verify readiness:

```bash
swf doctor --output capability-report.json
```

The Capability Report covers Node, the git repo, the AGENTS.md block, the Pi SDK, workspace writability, and `gh auth` when delivery is enabled. Missing capabilities defer checks; they do not pass them.

### 1.1 Start the Visualizer

```bash
software-factory visualize --workspace /workspace --bind 127.0.0.1
```

The command starts the default read-only API and UI defined in [VISUALIZER.md](VISUALIZER.md). It polls Campaign SQLite/WAL data and shows live Planner, Builder, Reviewer, Tester, findings, checks, dependencies, costs, and delivery evidence.

The default Visualizer does not approve, retry, edit, merge, or deploy. Explicit local control mode may approve a reviewed plan; use the CLI for other workflow actions. Do not bind it externally without approved authentication and TLS.

## 2. Developer request

Start from an issue:

```bash
swf request --issue <github-issue-url>
```

Or free-form intent (prompts interactively when `--text` is omitted):

```bash
swf request --text '<feature request>'
```

The command returns a Campaign ID and starts the Planner.

Inspect the normalized request:

```bash
software-factory request show <campaign-id>
software-factory status <campaign-id> --verbose
```

## 3. Planner phase

The Planner is read-only. It inspects the selected profile repositories and returns:

- Business outcome and acceptance criteria.
- Affected repositories and immutable base SHAs.
- Approved write paths.
- Contract, generation, traffic, deployment, and rollback impact.
- Dependency DAG and parallelizable work.
- Required review and test gates.
- Risks and unresolved decisions.

Resolve blocking questions through a new request revision:

```bash
software-factory request amend <campaign-id> \
  --set '<json-pointer>=<value>'
software-factory request submit <campaign-id>
```

Approve the plan:

```bash
software-factory approve <campaign-id> plan
```

Approval binds to the request revision/hash, profile digest, base SHAs, contract/policy digests, and write scopes.

## 4. Builder phase

Run approved Builders:

```bash
software-factory run <campaign-id> --until reviewing
```

The controller creates one worktree and Pi session per repository work item. Builders execute according to the DAG and cannot write outside their approved repository/path scope.

Monitor from the CLI or open the Campaign in the Visualizer:

```bash
software-factory workers <campaign-id>
software-factory results <campaign-id> --role builder
software-factory checks <campaign-id>
software-factory visualize --campaign <campaign-id>
```

Do not manually edit factory-owned worktrees. Pause and amend the request when human intervention changes scope or design.

## 5. Reviewer phase

Run independent reviews:

```bash
software-factory review <campaign-id>
```

The profile enables the review kinds listed in `requiredReviewKinds`.

List findings:

```bash
software-factory findings <campaign-id>
```

Blocking findings automatically produce Builder repair assignments. Every repaired change is reviewed again. Reviewers remain read-only and never certify their own implementation.

## 6. Tester phase

After blocking review findings are resolved:

```bash
software-factory test <campaign-id>
```

The Tester runs the complete profile/request check matrix. Outcomes:

- `passed`: evidence proves success on the expected SHA.
- `failed`: Builder repair required.
- `deferred`: sandbox lacks capability; trusted CI/runtime must execute it.
- `waived`: exact approved baseline waiver applies.

Test failure flow:

```text
Tester failure
  → Builder repair
  → Reviewer re-review
  → Tester retest
```

A Campaign cannot become implementation-complete while a required check remains failed or deferred without its designated trusted executor reporting success.

## 7. Draft PRs and CI

After reviewed local checks:

```bash
software-factory run <campaign-id> --until validating_ci
```

The controller pushes Campaign branches and opens draft PRs. It reconciles existing branches/PRs by operation key rather than duplicating them.

CI repair defaults to three cycles or 60 minutes per repository, subject to Campaign budgets. Every repair receives fresh review and testing.

Escalate exhausted repair:

```bash
software-factory failures <campaign-id> --format escalation
```

## 8. Waivers

A baseline failure requires an issue-linked, owned, expiring waiver:

```bash
software-factory waiver propose <campaign-id> \
  --check <check-id> \
  --issue <issue-url> \
  --expires <timestamp>
software-factory approve <campaign-id> waiver
```

A waiver applies only to the exact check, baseline evidence, source scope, and permitted phases. New regressions remain blocking.

## 9. Profile checks

Typical checks are the `checkIds` on each profile repository. Run them from the assigned worktree with that repository's documented commands. Generated paths listed on the repository are outputs, not hand-edited sources.

## 10. Delivery verification

After approved dev delivery:

```bash
software-factory verify <campaign-id> --environment dev
```

Local mode reports `deferred` for deployment verification. When a profile later enables remote verification, the Tester must still prove:

1. Expected source SHAs and generated artifact digests.
2. Required checks passed or waived.
3. No new secret or prohibited-payload logging.
4. Completed soak window when the profile requires one.

Inconclusive evidence does not pass.

## 11. Pause, resume, and abort

```bash
software-factory pause <campaign-id> --reason '<reason>'
software-factory resume <campaign-id>
software-factory abort <campaign-id> --reason '<reason>'
```

On resume, the controller reconciles request/profile hashes, repositories, worktrees, branches, PRs, bot commits, checks, approvals, deployments, and budgets before mutation.

Abort stops workers and preserves evidence. It does not automatically delete branches, close PRs, stop deployments, or reverse business operations.

## 12. Source drift

```bash
software-factory drift <campaign-id>
```

Non-overlapping drift may be rebased automatically, followed by fresh review/testing. Touched-file, contract, migration, security, ownership, or traffic-policy drift invalidates affected approval and returns to planning.

Never overwrite unrecognized human commits.

## 13. Security incident

Stop the Campaign when:

- A secret or raw PII enters model context/evidence.
- A role accesses an unauthorized repository/path/network destination.
- A Builder receives production credentials.
- An unapproved privileged action occurs.

Procedure:

1. Block mutation and stop affected agents.
2. Quarantine raw material outside normal evidence.
3. Revoke/rotate exposed credentials.
4. Notify security/privacy, profile owner, and Campaign owner.
5. Record redacted scope, timestamps, and run IDs.
6. Resume only with explicit clearance and fresh credentials.

Do not repeat sensitive values in incident records.

## 14. Evidence export

```bash
software-factory evidence export <campaign-id> \
  --redacted \
  --output campaign-evidence.tar.zst
```

The export includes Feature Request revisions, profile digest, agent results, transitions, approvals, checks, findings, contract/traffic digests, GitHub references, deployment graph, Visualizer-compatible redacted events, and provenance. It excludes credentials, raw PII, and unrestricted logs.

## 15. Add another repository

1. Run `swf init` in the repository so its AGENTS.md has a Software Factory block.
2. Point the campaign at it with `swf request --repos <path>` (the cwd is the primary repo).
3. The repository's own instructions (AGENTS.md + `prompts/reviewer`) shape planning, review, and testing.
4. Create evaluation scenarios and fake repository fixtures.
5. Run shadow and read-only review phases.
6. Enable scoped Builder writes only after conformance/security thresholds pass.

The core Planner → Builder → Reviewer → Tester sequence remains unchanged.
