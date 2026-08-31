# Local Domain Profile

Profile ID: `local`  
Initial version: `1.0.0`  
Core specification: [SPEC.md](SPEC.md)  
Machine-readable profile: [profiles/local.profile.json](profiles/local.profile.json)  
Factory roster and models: [`config.yaml`](../../config.yaml)

## Purpose

This is a domain-agnostic starter profile. It exercises Planner → Builder → Reviewer → Tester against repositories you list in `config.yaml` (or a `*.profile.json` file). It does not assume a product domain, repository layout, or vendor.

Replace the `app` repository entry with your own IDs, URLs, write paths, generated paths, and checks.

## Agent models and prompts

`config.yaml` is the factory roster. Each agent may set `model`, `thinking`, and `prompt_engineering`; omitted fields inherit `defaults` (or the built-in `prompts/<role>/{system,user}.md` files). The resolved model, thinking level, system prompt, and user prompt are passed into Pi sessions and Pi subagents.

```yaml
defaults:
  coding_agent: pi
  model: google/gemini-3.6-flash
  thinking: medium
  profile: local

agents:
  - name: planner
    model: google/gemini-3.6-flash
    thinking: high
    prompt_engineering:
      system: prompts/planner/system.md
      user: prompts/planner/user.md
  - name: builder
  - name: reviewer
    model: openai/gpt-5.6-terra
    thinking: high
  - name: tester
```

Point `SOFTWARE_FACTORY_CONFIG` at another YAML file when a campaign should use a different roster.

## Repositories

| ID | Role |
|---|---|
| `app` | Default write-capable checkout. Override the path with `SOFTWARE_FACTORY_REPO_APP`. |

Discovery order for each repository ID:

1. `SOFTWARE_FACTORY_REPO_<ID>` (hyphens become underscores)
2. The factory `repositoryRoot`
3. A sibling directory named from the repository URL

## Risk defaults

High-risk signals and prohibited evidence are profile data, not factory core. The starter profile flags auth, secrets, data-store contracts, and destructive rollback. Add domain-specific signals in your own profile.

## Planner, review, and test

The Planner inspects the selected repositories and records work items, checks, and approvals. Builders stay inside approved write paths and do not edit generated paths. Reviewers load each repository's own `prompts/reviewer` or `@prompts/reviewer` instructions. Testers execute the check IDs listed on those repositories.
