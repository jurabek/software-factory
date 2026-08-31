# Easy Use Plan — `swf` on any repo

Status: implemented across phases 1–8 (packaging, config reshape, AGENTS.md
block, `swf init`, `swf request` ergonomics, doctor, delivery, docs/legacy cut).

## 1. Goal

The factory is currently hard to adopt: it is a separate checkout you must
build, it needs 4+ environment variables, the target repo is hardcoded in
`config.yaml`, per-repo facts live in a Domain Profile that must be authored by
hand, and the everyday flow juggles campaign IDs across five commands.

The goal: **on any repo, run one command, answer a few prompts, and the factory
is set up; then a single simple command files a feature request** through the
existing Planner → Builder → Reviewer → Tester pipeline.

"Easy" means easy *install, init, and invoke*. The pipeline itself — request →
approve → run → status, manual plan approval, campaign IDs, worktrees, policy,
evidence — **does not change**.

## 2. User stories

- U1: `swf` is on PATH after one install step, in any directory, including repos
  that have never touched the factory.
- U2: In any repo, `swf init` auto-detects what it can, asks at most three
  questions, writes one artifact into the repo, and leaves the repo ready.
- U3: `swf request "implement X"` (or interactive `swf request`) starts a
  campaign on the *current* repo with zero environment variables and prints the
  exact next command.
- U4: `swf approve` / `swf run <id>` / `swf status <id>` behave exactly as
  today.
- U5: `swf doctor` tells you what is missing and runs automatically when setup
  or a request fails.

## 3. Settled decisions (design tree)

### Global shape

- **D1 — Command name: `swf`** (`sf` collides with the Salesforce CLI).
- **D2 — Global tool, per-repo context.** One installable tool; per-repo facts
  live in the repo itself; campaign workspace lives inside the repo at
  `.software-factory/workspace` (gitignored). Verified feasible: git allows a
  detached worktree inside its own working tree and the ignored workspace keeps
  `git status` clean.
- **D3 — Audience: the local developer.** No teammate onboarding, credential
  wizard, or multi-machine setup in scope. The artifacts stay teammate-readable.
- **D4 — Install: `npm link` from this checkout** (private, unpublished,
  `0.1.0`). The `bin` field is renamed to `swf`; publishing later is a separate
  decision.
- **D5 — Pi: keep bundling the `@earendil-works/pi-coding-agent` SDK.** Auth
  config is shared on disk per-provider regardless; no global `pi` requirement.
- **D6 — Domain Profile machinery is removed.** No `.profile.json`, no profile
  schema, no `profiles/` dir, no `--profile` flag, no repository lists in
  config. `config.yaml` (the roster) is the only config; per-repo instructions
  live in the repo's `AGENTS.md`.

### Per-repo context

- **D7 — One artifact: a machine-parseable block in `AGENTS.md`.** `swf init`
  writes (and idempotently rewrites) a YAML block guarded by HTML comment
  markers. It is the repo's structured context: checks, generated paths,
  protected paths, optional risk-signal override. Agents see the whole block;
  the Tester literally runs each `checks[].command` and reports evidence.
- **D8 — Repo discovery by cwd.** `swf request` infers the target repo from the
  current directory. No registry, no `SOFTWARE_FACTORY_REPO_*` env vars.
  Multi-repo campaigns pass sibling paths to `--repos`; each repo needs its own
  block.
- **D9 — Risk signals and approval rules move to `config.yaml` as global
  defaults.** They shape what the Reviewer scrutinizes and what gets flagged;
  they do not add approval gates (approval is always manual anyway).
- **D10 — Extra instructions are `AGENTS.md` on the context.** This replaces
  profile-authored repository instructions.

### Command surface

- **D11 — `swf request "text"`** → campaign on the cwd repo; prints the
  campaign ID and the next two commands (`swf approve`, `swf run <id>`).
  `swf request` with no argument prompts interactively ("What should I build?").
- **D12 — The pipeline flow is unchanged**: `request` → `approve` → `run` →
  `status`. Manual plan approval, campaign IDs, `run --until`,
  pause/resume/abort, waivers, evidence export — all as today.
- **D13 — Done = local `implementation_complete`.** Delivery (draft PR via
  `gh`) stays an explicit, separate step.
- **D14 — `swf` with no command prints a four-command cheat sheet**:
  `request` / `approve` / `run` / `status` (+ `init`, `doctor`).

### Detection

- **D15 — Language-aware detection in `init`**: `package.json` (test /
  typecheck / lint scripts), `pyproject.toml` (pytest), `Cargo.toml` (`cargo
  test`), `go.mod` (`go test`), plus a fallback that asks for check commands.
- **D16 — `init` asks at most three questions**, all with defaults:
  1. Detected checks — add/remove?
  2. Paths builders must never touch? (default: none)
  3. Generated/build output paths? (auto-proposed from `.gitignore`)

### Config and legacy

- **D17 — `config.yaml` final shape**: keeps `defaults` (model/thinking/tools),
  `observability`, `agents` roster; gains `risk_signals`, `approval_rules`,
  `required_review_kinds`, and `delivery` (replacing the
  `SOFTWARE_FACTORY_DELIVERY` env var). Deletes the `profile:` section,
  `defaults.profile`, `defaults.repositories`, and repository lists.
- **D18 — Legacy cut**: old `.workspace` campaigns are not migrated (disposable
  pre-release data). `SOFTWARE_FACTORY_WORKSPACE` and
  `SOFTWARE_FACTORY_CONFIG` survive as location overrides only.

### Diagnostics

- **D19 — `swf doctor`** checks: Node ≥ 24, cwd is a git repo, AGENTS.md block
  parses, Pi SDK loads, workspace writable, and `gh auth` only when delivery is
  enabled. Runs at the end of `init` and on `request` failures; also explicit.

## 4. AGENTS.md block format

`swf init` writes exactly this into the repo's `AGENTS.md` (creating the file
if absent):

````markdown
## Software Factory
<!-- software-factory:start -->
```yaml
checks:
  - id: unit
    command: npm test
  - id: typecheck
    command: npm run typecheck
generated: [dist/, coverage/, node_modules/]
protected: []
# risk_signals: []   # optional per-repo override; global defaults apply otherwise
```
<!-- software-factory:end -->
````

Contract:
- The block is located by the `software-factory:start` / `software-factory:end`
  comment pair; `init` replaces everything between them. Hand edits outside the
  markers are preserved.
- A missing block is an error for `request`/`run`; the error message says to
  run `swf init`.
- The Tester runs each `checks[].command` in the worktree and must report
  evidence per check (existing `run_local_command` + `submit_agent_result`
  flow). A check whose command is absent from the environment is `deferred`,
  never passed.
- The Planner and Reviewer receive the block (and the rest of `AGENTS.md`) as
  repository context, replacing the removed profile-authored instructions.

## 5. `config.yaml` final shape

```yaml
defaults:
  coding_agent: pi
  model: openai-codex/gpt-5.6-luna
  thinking: medium
  tools: [read, grep, find, ls]

observability:
  poll_ms: 500

risk_signals:
  - authentication or authorization
  - secrets or credentials
  - data-store contract
  - destructive rollback

approval_rules:
  - id: multi-repository-plan
    when: more than one work item exists
    approval: plan
  - id: break-glass
    when: a normally read-only repository write is requested
    approval: break-glass

required_review_kinds: [spec, standards, codeowners]

delivery:
  provider: github   # or local; replaces SOFTWARE_FACTORY_DELIVERY

agents:
  - name: planner
    model: openai-codex/gpt-5.6-sol
    thinking: high
    prompt_engineering:
      system: prompts/planner/system.md
      user: prompts/planner/user.md
  # builder, reviewer, tester unchanged; profiles removed from all agents
```

Notes:
- `delivery.provider: local` is the default; nothing is pushed unless set to
  `github` (mirrors the old env-var opt-in, now declarative).
- The per-repo `risk_signals` override in the AGENTS.md block replaces the
  global list for that repo only.

## 6. What does not change

- The campaign state machine and repair loops (`state-machine.ts`).
- Role order and role contracts; Planner/Reviewer/Tester stay read-only with
  respect to product code.
- Builder worktrees, path/symlink/generated-file policy, immutable base SHAs,
  drift checks.
- Feature Request and Agent Result schemas and redaction.
- The visualizer and its auto-start behavior; `--control` plan approval stays.
- Waivers, pause/resume/abort, evidence export, `verify`, `gh` delivery
  mechanics.

## 7. File-by-file change list

### Changed — factory source

| File | Change |
|---|---|
| `package.json` | `bin` renamed to `swf` (keep `software-factory` as an alias). |
| `config.yaml` | Reshape per §5: remove `profile:`, `defaults.profile`, `defaults.repositories`; add `risk_signals`, `approval_rules`, `required_review_kinds`, `delivery`. |
| `src/config.ts` | `normalizeFactoryConfig`: drop `raw.profile` embedding and `defaults.profile`/`defaults.repositories`; parse new top-level fields into `FactoryConfig`; remove `defaultRepositories` (replaced by cwd inference + `--repos`). |
| `src/cli.ts` | Add `init` command; add bare-`swf` cheat sheet; `request` reads cwd repo + block, drops `--profile`, prints next commands; extend `doctor`; wire auto-doctor on init/request failure. |
| `src/repositories.ts` | Replace env-var repo resolution with cwd inference and `--repos` sibling paths; each repo resolves its own AGENTS.md block. |
| `src/prompts.ts` | Inject the AGENTS.md block into repository context handed to Planner/Reviewer/Tester. |
| `src/policy.ts`, `src/controller.ts`, `src/repository-reviewer.ts` | Replace profile-sourced risk/approval/review-kind lookups with the new `FactoryConfig` fields + per-repo block override. |
| `src/types.ts` | `FactoryConfig` shape update; remove `DomainProfile` runtime dependency where possible. |

### New — factory source

| File | Purpose |
|---|---|
| `src/init.ts` | `swf init` logic: git-remote/branch detection, language-aware check detection, three interactive questions (defaults only, non-TTY = all defaults), AGENTS.md block writer, `.gitignore` entry for `.software-factory/`, then `doctor`. |
| `src/repo-block.ts` | Parse/write the AGENTS.md block: marker locate, YAML parse, idempotent replace, validation (check IDs unique, commands non-empty, paths normalized). |

### Removed / archived

| File | Disposition |
|---|---|
| `docs/software-factory/DOMAIN_PROFILE.schema.json` | Archived (deprecated), not loaded. |
| `docs/software-factory/profiles/local.profile.json` | Archived. |
| `docs/software-factory/LOCAL_PROFILE.md` | Archived; replaced by this plan + rewritten usage docs. |
| `--profile` flag and `SOFTWARE_FACTORY_DELIVERY`, `SOFTWARE_FACTORY_REPO_*` env vars | Removed (repo env vars removed; delivery now `config.yaml`). |

### Docs

| File | Change |
|---|---|
| `README.md` | Rewrite "Run a local Campaign" for `swf init` + `swf request`; keep pipeline docs. |
| `docs/USAGE.md` | Rewrite quick start around `swf init`/`swf request`; keep advanced campaign, delivery, visualizer, troubleshooting sections. |
| `docs/software-factory/ROADMAP.md` | Note the profile-to-AGENTS.md pivot in Phase 2/4 wording (no scope change). |
| `docs/software-factory/ARCHITECTURE.md` | Update config/profile references. |

## 8. Implementation phases

Each phase ends green (typecheck + tests) and independently shippable.

1. **Packaging** — rename bin to `swf`, `npm link`, verify `swf --help` from
   any cwd. Gate: U1.
2. **Config reshape** — new `FactoryConfig` fields, remove profile plumbing,
   update `config.yaml`, update config tests. Gate: existing + new config unit
   tests pass.
3. **AGENTS.md block** — `repo-block.ts` parse/write with unit tests
   (idempotency, markers, malformed input). No CLI wiring yet.
4. **`swf init`** — detection matrix (package.json / pyproject.toml /
   Cargo.toml / go.mod / fallback), three questions, block write, `.gitignore`
   entry, trailing `doctor`. Gate: run against the factory checkout itself and
   a scratch repo; re-run is byte-stable.
5. **`swf request` ergonomics** — cwd inference, `--repos`, interactive
   fallback, next-command output, drop `--profile`; inject block into prompts.
   Gate: end-to-end fake-runtime campaign in a scratch repo via
   `swf init` → `swf request` → `swf approve` → `swf run`.
6. **`swf doctor`** — full scope (D19) + auto-run hooks.
7. **Delivery** — `delivery.provider` field replaces the env var; keep gh
   mechanics untouched. Gate: existing gh delivery tests.
8. **Docs + legacy cut** — rewrite README/USAGE, archive profile artifacts,
   remove dead env vars.

## 9. Test plan

- Existing vitest suite stays green (adjust config/profile-dependent tests in
  phase 2; everything else must not need touching).
- New unit tests: block parse/write (idempotent rewrite, marker preservation,
  malformed YAML, duplicate check IDs), detection matrix per manifest type,
  config reshape (defaults, risk/approval/delivery fields, per-repo override).
- New integration test: fake-runtime campaign on a scratch repo through the
  full `swf` surface (phases 4–5).
- Manual acceptance per user story U1–U5, including running `swf init` in a
  non-Node repo (fallback path) and in the factory's own checkout (self-host).

## 10. Risks and notes

- **Profile removal touches policy and reviewer internals.** The plan treats
  `policy.ts` / `controller.ts` / `repository-reviewer.ts` as the audit surface
  for profile-sourced data; do not remove the schema until every read site is
  migrated (phase 2 explicitly covers this).
- **`AGENTS.md` block is a new contract.** Existing repos that already have an
  `AGENTS.md` must get the block via `swf init`; the factory refuses to guess.
- **Nested-worktree edge cases** were verified for the common case; the
  `.software-factory/` gitignore line is written by `init` so a fresh clone's
  workspace never pollutes `git status`.
- **Model/thinking stays global-only** (D10): no per-repo roster questions in
  `init`; `SOFTWARE_FACTORY_CONFIG` remains the escape hatch.
