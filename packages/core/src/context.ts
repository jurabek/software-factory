import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { basename, resolve } from "node:path";
import { readAgentsBlock } from "./repo-block.js";
import type { FactoryConfig, ProfileRepository } from "./types.js";

/**
 * A per-repo repository record resolved from the repo's own AGENTS.md block
 * and git state. `path` carries the absolute checkout used for worktrees.
 */
export interface RepoContextRepository extends ProfileRepository {
  path: string;
  agentsInstructions: string;
}

/**
 * The resolved campaign context: one record per target repository, derived
 * from each repo's AGENTS.md block plus git metadata. This is the pinned source
 * for role instructions, executable checks, policy paths, and effective risk.
 */
export interface RepoContext {
  schemaVersion: string;
  id: string;
  version: string;
  name: string;
  repositories: RepoContextRepository[];
  riskDefaults: { highRiskSignals: string[]; prohibitedEvidenceData: string[] };
  requiredReviewKinds: string[];
  approvalRules: Array<{ id: string; when: string; approval: string }>;
  evaluationScenarios: unknown[];
}

const prohibitedEvidenceData = [
  "credentials or tokens",
  "secrets",
  "complete request or response payloads containing secrets",
];

/**
 * D8 repo discovery: the cwd is always the primary repository; `repos` adds
 * sibling paths. Every target must be a git work tree with an AGENTS.md block
 * (`swf init` creates one).
 */
export function resolveRepoContext(cwd: string, config: FactoryConfig, repos: string[] = []): RepoContext {
  const paths = [...new Set([resolve(cwd), ...repos.map((path) => resolve(path))])];
  if (paths.length === 0) throw new Error("no repository selected");
  return {
    schemaVersion: "1.0.0",
    id: "local",
    version: "1.0.0",
    name: "Local",
    repositories: paths.map((path) => resolveRepository(path, config)),
    riskDefaults: {
      highRiskSignals: [...config.riskSignals],
      prohibitedEvidenceData,
    },
    requiredReviewKinds: config.requiredReviewKinds,
    approvalRules: config.approvalRules,
    evaluationScenarios: [],
  };
}

function resolveRepository(path: string, config: FactoryConfig): RepoContextRepository {
  if (!existsSync(path)) throw new Error(`repository not found: ${path}`);
  if (!isGitRepo(path)) throw new Error(`not a git repository: ${path}`);
  const agentsPath = resolve(path, "AGENTS.md");
  const block = readAgentsBlock(agentsPath);
  // The schema constrains ids to lowercase; directory basenames may not be.
  const id = basename(path).toLowerCase();
  return {
    id,
    url: gitRemoteUrl(path) ?? `local://${id}`,
    defaultBranch: gitBranch(path) ?? "main",
    adapter: "local-git",
    mode: "read_write",
    instructionPaths: ["AGENTS.md"],
    defaultWritePaths: ["**"],
    // `protected` and `generated` are both paths builders must never touch.
    generatedPaths: [...block.generated, ...block.protected],
    checks: block.checks.map((check) => ({ ...check })),
    effectiveRiskSignals: [...(block.riskSignals ?? config.riskSignals)],
    path,
    agentsInstructions: readFileSync(agentsPath, "utf8"),
  };
}

export function gitBranch(cwd: string): string | null {
  try {
    const branch = execFileSync("git", ["rev-parse", "--abbrev-ref", "HEAD"], { cwd, encoding: "utf8" }).trim();
    return branch === "HEAD" ? null : branch;
  } catch {
    return null;
  }
}

export function gitRemoteUrl(cwd: string): string | null {
  try {
    const remotes = execFileSync("git", ["remote"], { cwd, encoding: "utf8" }).trim().split("\n").filter(Boolean);
    const remote = remotes[0];
    if (!remote) return null;
    const url = execFileSync("git", ["remote", "get-url", remote], { cwd, encoding: "utf8" }).trim();
    if (!url) return null;
    // Local filesystem remotes must still satisfy the Feature Request uri format.
    return /^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(url) ? url : `file://${resolve(url)}`;
  } catch {
    return null;
  }
}

function isGitRepo(path: string): boolean {
  try {
    execFileSync("git", ["rev-parse", "--is-inside-work-tree"], { cwd: path, stdio: "pipe" });
    return true;
  } catch {
    return false;
  }
}
