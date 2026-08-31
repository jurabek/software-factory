# Archive — profile machinery (deprecated, not loaded)

Per-repo facts no longer come from a Domain Profile file. `swf init` writes a
machine-parseable block into each repo's `AGENTS.md` (see
[`../EASY_USE.md`](../EASY_USE.md)), and the factory's runtime context is
resolved from that block plus git metadata. The controller never loads these
files.

- `DOMAIN_PROFILE.schema.json` — the old domain-extension contract.
- `local.profile.json` — the old starter profile (`repositories`, risk
  defaults, approval rules, review kinds).
- `LOCAL_PROFILE.md` — the old profile authoring guide.

Kept for history; safe to delete once the profile-to-AGENTS.md pivot is
verified in your deployments.
