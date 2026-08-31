import { existsSync, mkdirSync } from "node:fs";
import { resolve } from "node:path";
import { execFileSync } from "node:child_process";
import type { CampaignStore } from "./store.js";
import type { DomainProfile, ProfileRepository, WorkItem } from "./types.js";

export class RepositoryManager {
  constructor(
    private readonly store: CampaignStore,
    private readonly profile: DomainProfile,
    private readonly repositoryRoot: string,
  ) {}

  sourcePath(repositoryId: string): string | null {
    const override = process.env[`SOFTWARE_FACTORY_REPO_${repositoryId.toUpperCase().replaceAll("-", "_")}`];
    if (override && existsSync(override)) return resolve(override);
    const repository = this.profile.repositories.find((item) => item.id === repositoryId);
    if (!repository) return null;
    const folder = repository.url.split("/").at(-1);
    const candidates = [
      this.repositoryRoot,
      folder ? resolve(this.repositoryRoot, folder) : "",
      folder ? resolve(this.repositoryRoot, "..", folder) : "",
    ].filter(Boolean);
    return candidates.find((candidate) => existsSync(resolve(candidate, ".git"))) ?? null;
  }

  baseSha(repositoryId: string): string | null {
    const path = this.sourcePath(repositoryId);
    if (!path) return null;
    return execFileSync("git", ["rev-parse", "HEAD"], { cwd: path, encoding: "utf8" }).trim();
  }

  worktree(workItem: WorkItem): string {
    const source = this.sourcePath(workItem.repositoryId);
    if (!source || !workItem.baseSha) throw new Error(`repository unavailable: ${workItem.repositoryId}`);
    const target = resolve(this.store.campaignDir, "worktrees", workItem.repositoryId, workItem.id);
    if (existsSync(resolve(target, ".git"))) return target;
    mkdirSync(resolve(target, ".."), { recursive: true });
    const baseSha = workItem.baseSha;
    const key = `${this.store.campaignId}/${workItem.id}/create-worktree/${baseSha}`;
    this.store.operation(key, baseSha, () => {
      execFileSync("git", ["worktree", "add", "--detach", target, baseSha], { cwd: source, stdio: "pipe" });
      return { target, baseSha };
    });
    return target;
  }

  drift(workItem: WorkItem): { repositoryId: string; expected: string | null; current: string | null; drifted: boolean } {
    const current = this.baseSha(workItem.repositoryId);
    return { repositoryId: workItem.repositoryId, expected: workItem.baseSha, current, drifted: Boolean(current && workItem.baseSha && current !== workItem.baseSha) };
  }
}

export function toWorkItem(repository: ProfileRepository, index: number, purpose: string, sha: string | null): WorkItem {
  return {
    id: `WI-${repository.id}-${index + 1}`,
    repositoryId: repository.id,
    repositoryUrl: repository.url,
    baseBranch: repository.defaultBranch,
    baseSha: sha,
    purpose,
    writePaths: repository.mode === "read_only" ? [] : repository.defaultWritePaths,
    generatedPaths: repository.generatedPaths,
    dependsOn: [],
  };
}
