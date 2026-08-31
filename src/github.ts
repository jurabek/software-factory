import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import type { CampaignStore } from "./store.js";
import type { Campaign, DeliveryRecord, FeatureRequest, WorkItem } from "./types.js";
import type { RepoContext } from "./context.js";
import type { RepositoryManager } from "./repositories.js";

export interface DeliveryContext {
  campaign: Campaign;
  request: FeatureRequest;
  profile: RepoContext;
  repositories: RepositoryManager;
  store: CampaignStore;
}

export interface DeliveryRuntime {
  openDraftPullRequests(context: DeliveryContext): DeliveryRecord[];
  observeCi(context: DeliveryContext, deliveries: DeliveryRecord[]): DeliveryRecord[];
}

interface CommandOptions {
  cwd?: string;
  input?: string;
  allowFailure?: boolean;
}

export interface CommandRunner {
  run(command: string, args: string[], options?: CommandOptions): { stdout: string; stderr: string; status: number };
}

class ProcessCommandRunner implements CommandRunner {
  run(command: string, args: string[], options: CommandOptions = {}): { stdout: string; stderr: string; status: number } {
    const result = spawnSync(command, args, {
      cwd: options.cwd,
      input: options.input,
      encoding: "utf8",
      maxBuffer: 2 * 1024 * 1024,
      env: process.env,
    });
    if (result.error) throw result.error;
    const status = result.status ?? 1;
    const output = { stdout: result.stdout ?? "", stderr: result.stderr ?? "", status };
    if (status !== 0 && !options.allowFailure) {
      throw new Error(`${command} ${args[0] ?? ""} failed: ${output.stderr.trim() || output.stdout.trim() || `exit ${status}`}`);
    }
    return output;
  }
}

interface PullRequestJson {
  number: number;
  url: string;
  isDraft: boolean;
  headRefName: string;
  headRefOid: string;
  baseRefName: string;
  title: string;
  body: string;
}

interface CheckJson {
  name?: string;
  state?: string;
  bucket?: string;
  link?: string;
  workflow?: string;
}

export class GhCliDelivery implements DeliveryRuntime {
  private readonly authenticatedHosts = new Set<string>();

  constructor(private readonly commands: CommandRunner = new ProcessCommandRunner()) {}

  openDraftPullRequests(context: DeliveryContext): DeliveryRecord[] {
    return context.request.workItems
      .filter((workItem) => workItem.baseSha !== null)
      .filter((workItem) => context.profile.repositories.find((repository) => repository.id === workItem.repositoryId)?.mode !== "read_only")
      .map((workItem) => this.openDraftPullRequest(context, workItem));
  }

  observeCi(context: DeliveryContext, deliveries: DeliveryRecord[]): DeliveryRecord[] {
    return deliveries.map((delivery) => {
      const repository = githubRepository(delivery.repositoryUrl);
      this.ensureAuthentication(repository.host);
      const pullRequest = this.viewPullRequest(repository.slug, delivery.pullRequestUrl);
      if (pullRequest.headRefName !== delivery.branch || pullRequest.headRefOid !== delivery.headSha) {
        throw new Error(`pull request head no longer matches reviewed source ${delivery.headSha}: ${delivery.pullRequestUrl}`);
      }
      const result = this.commands.run("gh", [
        "pr", "checks", delivery.pullRequestUrl,
        "--repo", repository.slug,
        "--json", "name,state,bucket,link,workflow",
      ], { allowFailure: true });
      let checks: CheckJson[];
      try {
        checks = JSON.parse(result.stdout || "[]") as CheckJson[];
      } catch {
        throw new Error(`gh returned invalid check JSON for ${delivery.pullRequestUrl}`);
      }
      const buckets = checks.map((check) => String(check.bucket ?? check.state ?? "pending").toLowerCase());
      const ciStatus = buckets.some((bucket) => ["fail", "failure", "cancel", "cancelled", "timed_out", "action_required"].includes(bucket))
        ? "failed"
        : buckets.length > 0 && buckets.every((bucket) => ["pass", "passed", "success", "skipping", "skipped", "neutral"].includes(bucket))
          ? "passed"
          : "pending";
      return {
        ...delivery,
        ciStatus,
        checks: checks.map((check) => ({
          name: String(check.name ?? check.workflow ?? "unnamed-check"),
          state: String(check.state ?? check.bucket ?? "UNKNOWN"),
          bucket: String(check.bucket ?? "pending"),
          link: check.link ? String(check.link) : null,
        })),
        updatedAt: new Date().toISOString(),
      };
    });
  }

  private openDraftPullRequest(context: DeliveryContext, workItem: WorkItem): DeliveryRecord {
    const repository = githubRepository(workItem.repositoryUrl);
    this.ensureAuthentication(repository.host);
    const worktree = context.repositories.worktree(workItem);
    const branch = campaignBranch(context.campaign.id, context.request.revision, workItem);
    const headSha = this.prepareAndPushBranch(context, workItem, worktree, branch, repository.host);
    const marker = pullRequestMarker(context, workItem);
    const body = pullRequestBody(context, workItem, marker);
    const title = prTitle(context.request.title, workItem.repositoryId);
    const existing = this.findPullRequest(repository.slug, branch);
    let pullRequest: PullRequestJson;
    if (existing) {
      if (existing.baseRefName !== workItem.baseBranch || !existing.body.includes(marker)) {
        throw new Error(`conflicting pull request already exists for ${repository.slug}:${branch}`);
      }
      pullRequest = this.updatePullRequest(context, workItem, repository.slug, existing, headSha, title, body);
    } else {
      const operationKey = `${context.campaign.id}/${context.request.revision}/${workItem.id}/open-pr/${branch}`;
      const operationDigest = sha256(`${repository.slug}\n${branch}\n${workItem.baseBranch}\n${marker}`);
      const result = context.store.operation(operationKey, operationDigest, () => {
        const reconciled = this.findPullRequest(repository.slug, branch);
        if (reconciled) {
          if (reconciled.baseRefName !== workItem.baseBranch || !reconciled.body.includes(marker)) {
            throw new Error(`conflicting pull request appeared for ${repository.slug}:${branch}`);
          }
          return reconciled;
        }
        const created = this.commands.run("gh", [
          "pr", "create",
          "--repo", repository.slug,
          "--head", branch,
          "--base", workItem.baseBranch,
          "--draft",
          "--title", title,
          "--body-file", "-",
        ], { input: body });
        const url = created.stdout.trim().split("\n").find((line) => /^https:\/\//.test(line.trim()))?.trim();
        if (!url) throw new Error(`gh pr create did not return a pull request URL for ${repository.slug}`);
        return this.viewPullRequest(repository.slug, url);
      }) as PullRequestJson;
      pullRequest = result;
    }
    if (!pullRequest.isDraft) throw new Error(`factory pull request is no longer a draft: ${pullRequest.url}`);
    if (pullRequest.headRefName !== branch || pullRequest.headRefOid !== headSha) {
      throw new Error(`pull request does not point to delivered head ${headSha}: ${pullRequest.url}`);
    }
    return {
      workItemId: workItem.id,
      repositoryId: workItem.repositoryId,
      repositoryUrl: workItem.repositoryUrl,
      baseBranch: workItem.baseBranch,
      branch,
      headSha,
      pullRequestNumber: pullRequest.number,
      pullRequestUrl: pullRequest.url,
      draft: pullRequest.isDraft,
      ciStatus: "pending",
      checks: [],
      updatedAt: new Date().toISOString(),
    };
  }

  private updatePullRequest(
    context: DeliveryContext,
    workItem: WorkItem,
    repository: string,
    existing: PullRequestJson,
    headSha: string,
    title: string,
    body: string,
  ): PullRequestJson {
    if (existing.title === title && existing.body === body) return existing;
    const operationKey = `${context.campaign.id}/${context.request.revision}/${workItem.id}/update-pr/${headSha}`;
    const operationDigest = sha256(`${title}\n${body}`);
    return context.store.operation(operationKey, operationDigest, () => {
      const current = this.viewPullRequest(repository, existing.url);
      if (current.title === title && current.body === body) return current;
      if (current.baseRefName !== workItem.baseBranch || !current.body.includes(pullRequestMarker(context, workItem))) {
        throw new Error(`pull request changed while reconciling ${existing.url}`);
      }
      this.commands.run("gh", [
        "pr", "edit", existing.url, "--repo", repository,
        "--title", title, "--body-file", "-",
      ], { input: body });
      return this.viewPullRequest(repository, existing.url);
    }) as PullRequestJson;
  }

  private prepareAndPushBranch(
    context: DeliveryContext,
    workItem: WorkItem,
    worktree: string,
    branch: string,
    host: string,
  ): string {
    const currentBranch = this.git(["branch", "--show-current"], worktree).stdout.trim();
    if (currentBranch !== branch) {
      const localBranch = this.git(["branch", "--list", branch], worktree).stdout.trim();
      this.git(localBranch ? ["switch", branch] : ["switch", "-c", branch], worktree);
    }
    this.git(["add", "--all"], worktree);
    const staged = this.git(["diff", "--cached", "--name-only"], worktree).stdout.trim();
    if (staged) {
      this.git([
        "-c", "user.name=Software Factory",
        "-c", "user.email=software-factory@users.noreply.github.com",
        "commit", "-m", commitTitle(context.request.title),
        "-m", `Campaign: ${context.campaign.id}\nRequest-Revision: ${context.request.revision}\nWork-Item: ${workItem.id}`,
      ], worktree);
    }
    const headSha = this.git(["rev-parse", "HEAD"], worktree).stdout.trim();
    if (headSha === workItem.baseSha) throw new Error(`work item ${workItem.id} has no changes to deliver`);
    const operationKey = `${context.campaign.id}/${context.request.revision}/${workItem.id}/push/${headSha}`;
    context.store.operation(operationKey, headSha, () => {
      const remoteSha = this.remoteBranchSha(worktree, branch);
      if (remoteSha === headSha) return { branch, headSha, reconciled: true };
      const previous = context.store.deliveries().find((delivery) => delivery.workItemId === workItem.id);
      if (remoteSha && remoteSha !== previous?.headSha) {
        throw new Error(`remote branch ${branch} contains an unrecognized commit ${remoteSha}`);
      }
      this.ensureAuthentication(host);
      const refspec = `${headSha}:refs/heads/${branch}`;
      const args = remoteSha
        ? ["push", `--force-with-lease=refs/heads/${branch}:${remoteSha}`, "origin", refspec]
        : ["push", "origin", refspec];
      this.git(args, worktree);
      return { branch, headSha, reconciled: false };
    });
    return headSha;
  }

  private remoteBranchSha(worktree: string, branch: string): string | null {
    const output = this.git(["ls-remote", "--heads", "origin", `refs/heads/${branch}`], worktree).stdout.trim();
    return output ? output.split(/\s+/)[0] ?? null : null;
  }

  private ensureAuthentication(host: string): void {
    if (this.authenticatedHosts.has(host)) return;
    this.commands.run("gh", ["auth", "status", "--hostname", host]);
    this.commands.run("gh", ["auth", "setup-git", "--hostname", host]);
    this.authenticatedHosts.add(host);
  }

  private findPullRequest(repository: string, branch: string): PullRequestJson | null {
    const output = this.commands.run("gh", [
      "pr", "list", "--repo", repository, "--state", "all", "--head", branch,
      "--json", "number,url,isDraft,headRefName,headRefOid,baseRefName,title,body",
    ]).stdout;
    const pullRequests = JSON.parse(output || "[]") as PullRequestJson[];
    if (pullRequests.length > 1) throw new Error(`multiple pull requests found for ${repository}:${branch}`);
    return pullRequests[0] ?? null;
  }

  private viewPullRequest(repository: string, reference: string): PullRequestJson {
    const output = this.commands.run("gh", [
      "pr", "view", reference, "--repo", repository,
      "--json", "number,url,isDraft,headRefName,headRefOid,baseRefName,title,body",
    ]).stdout;
    return JSON.parse(output) as PullRequestJson;
  }

  private git(args: string[], cwd: string): { stdout: string; stderr: string; status: number } {
    return this.commands.run("git", args, { cwd });
  }
}

function githubRepository(url: string): { host: string; slug: string } {
  let host: string;
  let path: string;
  if (/^git@[^:]+:/.test(url)) {
    const match = /^git@([^:]+):(.+)$/.exec(url);
    if (!match) throw new Error(`unsupported GitHub repository URL: ${url}`);
    host = match[1]!;
    path = match[2]!;
  } else {
    const parsed = new URL(url);
    host = parsed.hostname;
    path = parsed.pathname.replace(/^\//, "");
  }
  const parts = path.replace(/\.git$/, "").split("/").filter(Boolean);
  if (parts.length !== 2) throw new Error(`GitHub repository URL must identify owner/repository: ${url}`);
  const ownerRepo = `${parts[0]}/${parts[1]}`;
  return { host, slug: host === "github.com" ? ownerRepo : `${host}/${ownerRepo}` };
}

function campaignBranch(campaignId: string, revision: number, workItem: WorkItem): string {
  return `software-factory/${campaignId.toLowerCase()}/r${revision}/${workItem.repositoryId}`;
}

function pullRequestMarker(context: DeliveryContext, workItem: WorkItem): string {
  return `<!-- software-factory:campaign=${context.campaign.id};request=${context.campaign.requestHash};work-item=${workItem.id} -->`;
}

function pullRequestBody(context: DeliveryContext, workItem: WorkItem, marker: string): string {
  const results = context.store.results().filter((result) => result.workItemId === workItem.id || result.role === "tester");
  const checks = results.flatMap((result) => result.checks)
    .filter((check, index, all) => all.findLastIndex((candidate) => candidate.checkId === check.checkId) === index);
  const findings = results.flatMap((result) => result.findings);
  const dependencies = context.request.dependencyGraph.edges
    .filter((edge) => edge.from === workItem.id || edge.to === workItem.id)
    .map((edge) => `- ${edge.from} → ${edge.to}: ${edge.kind} (${edge.condition})`);
  const generated = results.flatMap((result) => result.changedFiles).filter((file) => file.generated);
  return [
    marker,
    "## Outcome",
    context.request.businessOutcome,
    "",
    "## Scope",
    workItem.purpose,
    `Approved paths: ${workItem.writePaths.join(", ") || "none"}`,
    "",
    "## Non-goals",
    ...context.request.nonGoals.map((item) => `- ${item}`),
    "",
    "## Campaign and dependencies",
    `- Campaign: ${context.campaign.id}`,
    `- Request revision: ${context.request.revision}`,
    `- Work item: ${workItem.id}`,
    ...(dependencies.length ? dependencies : ["- No cross-work-item dependencies"]),
    "",
    "## Contract, data, and traffic impact",
    `- Contracts: ${context.request.contracts.length ? "See approved Feature Request" : "None declared"}`,
    `- Traffic edges: ${context.request.trafficEdges.length ? "See approved Feature Request" : "None declared"}`,
    `- Data classes: ${context.request.security.dataClasses.join(", ") || "None declared"}`,
    "",
    "## Generated artifacts",
    ...(generated.length ? generated.map((file) => `- ${file.path} (${file.digest})`) : ["- None reported"]),
    "",
    "## Review and test evidence",
    ...results.map((result) => `- ${result.role}: ${result.status} — ${result.summary}`),
    ...checks.map((check) => `- ${check.checkId}: ${check.status}`),
    ...findings.filter((finding) => !finding.blocking).map((finding) => `- Non-blocking: ${finding.summary}`),
    "",
    "## Security and privacy",
    `- Risk: ${context.request.risk.level} — ${context.request.risk.rationale}`,
    `- Required reviews: ${context.request.security.requiredReviews.join(", ")}`,
    "",
    "## Rollout and rollback",
    `- Strategy: ${context.request.rollout.strategy}`,
    ...context.request.rollback.steps.map((step) => `- Rollback: ${step}`),
    "",
    "## Provenance",
    `- Base SHA: ${workItem.baseSha}`,
    `- Profile: ${context.profile.id}@${context.profile.version} (${context.campaign.profileDigest})`,
    `- Factory package: software-factory-local`,
    "",
    "This pull request is intentionally a draft. Factory review does not replace human or CODEOWNERS approval.",
  ].join("\n");
}

function commitTitle(title: string): string {
  return `factory: ${title.replace(/\s+/g, " ").trim()}`.slice(0, 72);
}

function prTitle(title: string, repositoryId: string): string {
  return `${title.replace(/\s+/g, " ").trim()} [${repositoryId}]`.slice(0, 120);
}

function sha256(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}
