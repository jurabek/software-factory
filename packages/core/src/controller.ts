import { randomInt } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { execFileSync } from "node:child_process";
import { CampaignLock } from "./campaign-lock.js";
import { loadFactoryConfig, resolveAgent } from "./config.js";
import { ContractValidator, digest } from "./contracts.js";
import { resolveRepoContext, type RepoContext } from "./context.js";
import { FeatureRequestModule } from "./feature-request.js";
import { assertWriteAllowed, type PolicyCommand } from "./policy.js";
import { RepositoryManager, toWorkItem } from "./repositories.js";
import {
  PiAgentRuntime,
  visiblePeerSessions,
  type AgentRuntime,
  type Assignment,
} from "./runtime.js";
import { compileRolePrompt } from "./prompts.js";
import {
  assertTransition,
  canTransition,
  decideCampaignTransition,
  isTerminal,
  type CampaignAdvancementOutcome,
} from "./state-machine.js";
import { CampaignStore } from "./store.js";
import type { Campaign, FactoryConfig, FactoryState, FeatureRequest, WorkItem } from "./types.js";

export interface FactoryOptions {
  workspace: string;
  repositoryRoot: string;
  runtime?: "pi" | AgentRuntime;
  config?: FactoryConfig;
}

export class SoftwareFactory {
  private readonly featureRequests: FeatureRequestModule;

  private constructor(
    private readonly options: FactoryOptions,
    private readonly validator: ContractValidator,
  ) {
    this.featureRequests = new FeatureRequestModule(validator);
  }

  static async create(options: FactoryOptions): Promise<SoftwareFactory> {
    mkdirSync(options.workspace, { recursive: true });
    const config = options.config ?? loadFactoryConfig(undefined, options.repositoryRoot);
    return new SoftwareFactory({ ...options, config }, await ContractValidator.create());
  }

  get config(): FactoryConfig {
    return this.options.config ?? loadFactoryConfig(undefined, this.options.repositoryRoot);
  }

  async request(input: { text: string; cwd?: string; repos?: string[]; requestedBy?: string }): Promise<Campaign> {
    const cwd = input.cwd ?? this.options.repositoryRoot;
    const context = resolveRepoContext(cwd, this.config, input.repos);
    const declaredChecks = context.repositories.flatMap((repository) => repository.checks);
    if (declaredChecks.length === 0) {
      throw new Error("no checks declared in AGENTS.md; run `swf init` in each repository and add check commands");
    }
    const campaignId = `SF-${new Date().getUTCFullYear()}-${randomInt(1000, 10_000)}`;
    const store = new CampaignStore(this.options.workspace, campaignId);
    try {
      const repositoryManager = new RepositoryManager(store, context);
      const workItems = context.repositories.map((repository, index) =>
        toWorkItem(repository, index, input.text, repositoryManager.baseSha(repository.id)),
      );
      const now = new Date().toISOString();
      const profileDigest = digest(context);
      const { request, hash: requestHash } = this.featureRequests.create({
        campaignId,
        text: input.text,
        profile: context,
        profileDigest,
        workItems,
        requestedBy: input.requestedBy ?? "local-developer",
        createdAt: now,
      });
      const campaign: Campaign = {
        id: campaignId, title: request.title, state: "received", previousState: null,
        requestHash, profileId: context.id, profileVersion: context.version,
        profileDigest, repairCycles: 0, pausedReason: null, createdAt: now, updatedAt: now,
      };
      store.createCampaign(campaign, request);
      writeFileSync(resolve(store.campaignDir, "profiles/resolved.json"), JSON.stringify(context, null, 2));
      await store.listenBus();
      await this.transition(store, "planning");
      const result = await this.executeAgent(store, this.assignment(store, context, request, "planner", null, 1, cwd));
      this.validator.result(result);
      store.saveResult(result);
      await this.transition(
        store,
        result.status === "completed" ? "awaiting_plan_approval" : "blocked",
        result.status === "completed" ? null : "planning",
        result.status === "completed" ? null : result.summary,
      );
      return store.campaign();
    } catch (error) {
      this.blockOnError(store, error);
      throw error;
    } finally {
      store.close();
    }
  }

  inspect(campaignId: string): { campaign: Campaign; request: FeatureRequest } {
    const store = this.open(campaignId);
    try { return { campaign: store.campaign(), request: store.request() }; } finally { store.close(); }
  }

  approve(campaignId: string, kind: string, actor = "local-developer"): Campaign {
    const store = this.open(campaignId);
    try {
      const approval = this.featureRequests.approve(store, kind, actor);
      if (approval.startBuilding) this.transitionSync(store, "building");
      return store.campaign();
    } finally { store.close(); }
  }

  async advance(campaignId: string, target: FactoryState = "implementation_complete"): Promise<Campaign> {
    const lock = CampaignLock.acquire(resolve(this.options.workspace, campaignId), "advance");
    let store: CampaignStore | undefined;
    try {
      store = this.open(campaignId);
      const staleRuns = store.failRunningAgents(
        lock.recoveredStaleOwner
          ? "agent run interrupted after stale campaign ownership"
          : "agent run interrupted before campaign ownership was acquired",
      );
      if (staleRuns > 0) {
        this.blockOnError(store, new Error(`${staleRuns} interrupted agent run${staleRuns === 1 ? "" : "s"} recovered`));
      }
      await store.listenBus();
      const profile = this.pinnedProfile(store);
      const request = store.request();
      for (let guard = 0; guard < 50; guard += 1) {
        const state = store.campaign().state;
        if (state === target || isTerminal(state) || ["blocked", "paused"].includes(state)) break;
        if (state === "awaiting_plan_approval") throw new Error("plan approval is required");
        if (["building", "repairing_review"].includes(state)) {
          await this.runBuilders(store, profile, request, state !== "building");
          this.applyAdvancementDecision(store, request, "builder_completed");
          continue;
        }
        if (["reviewing", "re_reviewing"].includes(state)) {
          const blocked = await this.runReviewers(store, profile, request);
          this.applyAdvancementDecision(store, request, blocked ? "review_blocked" : "review_passed");
          continue;
        }
        if (state === "awaiting_human_review") {
          this.applyAdvancementDecision(store, request, "human_review_completed");
          continue;
        }
        throw new Error(`local mode cannot advance state ${state}`);
      }
      return store.campaign();
    } catch (error) {
      if (store) this.blockOnError(store, error);
      throw error;
    } finally {
      store?.close();
      lock.release();
    }
  }

  pause(campaignId: string, reason: string): Campaign {
    const store = this.open(campaignId);
    try {
      const current = store.campaign().state;
      this.transitionSync(store, "paused", current, reason);
      return store.campaign();
    } finally { store.close(); }
  }

  resume(campaignId: string): Campaign {
    const store = this.open(campaignId);
    try {
      const campaign = store.campaign();
      if (!["paused", "blocked"].includes(campaign.state) || !campaign.previousState) {
        throw new Error("campaign is not resumable");
      }
      this.transitionSync(store, campaign.previousState);
      return store.campaign();
    } finally { store.close(); }
  }

  abort(campaignId: string, reason: string): Campaign {
    const store = this.open(campaignId);
    try {
      this.transitionSync(store, "aborting", store.campaign().state, reason);
      this.transitionSync(store, "aborted");
      return store.campaign();
    } finally { store.close(); }
  }

  amend(campaignId: string, pointer: string, value: unknown): FeatureRequest {
    const store = this.open(campaignId);
    try {
      const request = this.featureRequests.amend(store, pointer, value);
      if (store.campaign().state !== "awaiting_plan_approval") store.setState("awaiting_plan_approval");
      return request;
    } finally { store.close(); }
  }

  submit(campaignId: string): FeatureRequest {
    const store = this.open(campaignId);
    try { return this.featureRequests.submit(store); } finally { store.close(); }
  }

  drift(campaignId: string): unknown[] {
    const store = this.open(campaignId);
    try {
      const request = store.request();
      const resolved = JSON.parse(readFileSync(resolve(store.campaignDir, "profiles/resolved.json"), "utf8")) as RepoContext;
      const manager = new RepositoryManager(store, resolved);
      return request.workItems.map((item) => manager.drift(item));
    } finally { store.close(); }
  }

  exportEvidence(campaignId: string, output: string): void {
    const store = this.open(campaignId);
    try { store.exportSnapshot(output); } finally { store.close(); }
  }

  private async runBuilders(store: CampaignStore, profile: RepoContext, request: FeatureRequest, repair: boolean): Promise<void> {
    const manager = new RepositoryManager(store, profile);
    const attempt = repair ? store.incrementRepair() + 1 : 1;
    const pending = new Map(request.workItems.filter((item) => {
      const repository = profile.repositories.find((candidate) => candidate.id === item.repositoryId);
      return repository?.mode !== "read_only";
    }).map((item) => [item.id, item]));
    if ([...pending.values()].some((item) => item.baseSha === null)) {
      throw new Error(`selected repositories are unavailable: ${[...pending.values()].filter((item) => !item.baseSha).map((item) => item.repositoryId).join(", ")}`);
    }
    const completed = new Set<string>();
    while (pending.size > 0) {
      const ready = [...pending.values()].filter((item) => {
        const graphDependencies = request.dependencyGraph.edges.filter((edge) => edge.to === item.id).map((edge) => edge.from);
        return [...item.dependsOn, ...graphDependencies].every((dependency) => completed.has(dependency) || !pending.has(dependency));
      });
      if (ready.length === 0) throw new Error("work-item dependency graph contains a cycle");
      const results = await Promise.all(ready.slice(0, request.budgets.maxConcurrentBuilders).map(async (workItem) => {
        const worktree = manager.worktree(workItem);
        const assignment = this.assignment(store, profile, request, "builder", workItem, attempt, worktree);
        return this.executeAgent(store, assignment);
      }));
      for (const result of results) {
        this.validator.result(result);
        if (result.status !== "completed") throw new Error(`builder ${result.workItemId} did not complete`);
        store.saveResult(result);
        if (result.workItemId) {
          completed.add(result.workItemId);
          pending.delete(result.workItemId);
        }
      }
    }
  }

  private async runReviewers(store: CampaignStore, profile: RepoContext, request: FeatureRequest): Promise<boolean> {
    const attempt = store.campaign().repairCycles + 1;
    const manager = new RepositoryManager(store, profile);
    const results = await Promise.all(request.workItems.filter((item) => item.baseSha).map((workItem) =>
      this.executeAgent(store, this.assignment(store, profile, request, "reviewer", workItem, attempt, manager.worktree(workItem))),
    ));
    for (const result of results) {
      this.validator.result(result);
      if (result.status === "completed" && result.workItemId) store.resolveFindings(result.workItemId);
      store.saveResult(result);
    }
    return results.some((result) =>
      result.status !== "completed" ||
      result.findings.some((finding) => finding.blocking) ||
      result.checks.some((check) => check.required && check.status !== "passed"),
    );
  }

  private assignment(
    store: CampaignStore, profile: RepoContext, request: FeatureRequest, role: Assignment["role"],
    workItem: WorkItem | null, attempt: number, worktree: string,
  ): Assignment {
    const repository = workItem ? profile.repositories.find((item) => item.id === workItem.repositoryId) : undefined;
    const agent = resolveAgent(this.config, role);
    const workerRunId = `${role}-${workItem?.id ?? "campaign"}-${attempt}`;
    const prompts = compileRolePrompt({
      role,
      request,
      requestHash: store.campaign().requestHash,
      workItem,
      workerRunId,
      peerSessions: visiblePeerSessions(role, store.sessionCatalog(), workerRunId),
      factorySocket: store.socketPath,
      worktree,
      attempt,
      repositoryContext: this.repositoryPromptContext(profile, workItem),
      systemTemplate: agent.promptEngineering.system,
      userTemplate: agent.promptEngineering.user,
    });
    return {
      campaign: store.campaign(), request, role, workItem, attempt, worktree,
      grant: {
        role, worktree,
        writePaths: workItem?.writePaths ?? [],
        generatedPaths: repository?.generatedPaths ?? [],
        commands: role === "builder" || role === "reviewer"
          ? this.policyCommands(store, profile, request, workItem, worktree)
          : [],
        allowedHosts: [],
      },
      systemPrompt: prompts.system,
      prompt: prompts.user,
      agent,
      deadlineMs: this.config.runtime.agentDeadlineMs,
      emptyTurnRetries: this.config.runtime.emptyTurnRetries,
    };
  }

  private policyCommands(
    store: CampaignStore,
    profile: RepoContext,
    request: FeatureRequest,
    workItem: WorkItem | null,
    worktree: string,
  ): PolicyCommand[] {
    const manager = new RepositoryManager(store, profile);
    const workItems = workItem ? [workItem] : request.workItems;
    return workItems.flatMap((item) => {
      const repository = profile.repositories.find((candidate) => candidate.id === item.repositoryId);
      if (!repository) throw new Error(`repository context not found: ${item.repositoryId}`);
      const cwd = workItem ? worktree : manager.worktree(item);
      return repository.checks.map((check) => {
        const requirement = request.requiredChecks.find((candidate) =>
          candidate.workItem === item.id && candidate.kind === check.id,
        );
        if (!requirement) throw new Error(`required check not found: ${item.id}/${check.id}`);
        return { id: requirement.id, command: check.command, cwd };
      });
    });
  }

  private repositoryPromptContext(profile: RepoContext, workItem: WorkItem | null): string {
    const repositories = workItem
      ? profile.repositories.filter((repository) => repository.id === workItem.repositoryId)
      : profile.repositories;
    return repositories.map((repository) => [
      `Repository: ${repository.id}`,
      `Effective risk signals: ${repository.effectiveRiskSignals.join(", ") || "none"}`,
      "Pinned AGENTS.md:",
      repository.agentsInstructions.trim(),
    ].join("\n")).join("\n\n---\n\n");
  }

  private runtime(store: CampaignStore): AgentRuntime {
    if (typeof this.options.runtime === "object") return this.options.runtime;
    return new PiAgentRuntime(this.validator, store);
  }

  private async executeAgent(store: CampaignStore, assignment: Assignment) {
    const runId = `${assignment.role}-${assignment.workItem?.id ?? "campaign"}-${assignment.attempt}`;
    const traceParentId = store.startAgent(
      runId,
      assignment.role,
      assignment.workItem?.id ?? null,
      `pending:${runId}`,
      assignment.attempt,
    );
    const tracedAssignment = { ...assignment, runId, traceParentId };
    store.event("log", {
      runId,
      role: assignment.role,
      workItemId: assignment.workItem?.id ?? null,
      level: "info",
      text: `${assignment.role} assignment started`,
    }, traceParentId);
    try {
      const result = await this.runtime(store).run(tracedAssignment);
      this.recordSessionHandoff(store, tracedAssignment, result);
      this.verifyAgentResult(store, tracedAssignment, result);
      store.event("log", {
        runId,
        role: assignment.role,
        workItemId: assignment.workItem?.id ?? null,
        level: "info",
        text: result.summary,
        status: result.status,
      }, traceParentId);
      store.finishAgent(runId, result.status, null, result.piSessionId);
      return result;
    } catch (error) {
      store.event("error", {
        runId,
        role: assignment.role,
        workItemId: assignment.workItem?.id ?? null,
        message: error instanceof Error ? error.message : "unknown error",
      }, traceParentId);
      store.finishAgent(runId, "failed", error instanceof Error ? error.message : "unknown error");
      throw error;
    }
  }

  private recordSessionHandoff(
    store: CampaignStore,
    assignment: Assignment,
    result: Awaited<ReturnType<AgentRuntime["run"]>>,
  ): void {
    const runId = assignment.runId ?? `${assignment.role}-${assignment.workItem?.id ?? "campaign"}-${assignment.attempt}`;
    const existing = store.sessionCatalog().find((session) => session.sessionId === result.piSessionId);
    store.recordPiSession({
      sessionId: result.piSessionId,
      runId,
      role: assignment.role,
      workItemId: assignment.workItem?.id ?? null,
      attempt: assignment.attempt,
      sessionFile: existing?.sessionFile ?? null,
    });
    if (store.sessionLogCount(result.piSessionId) > 0) return;
    store.appendSessionLog({
      sessionId: result.piSessionId,
      runId,
      role: assignment.role,
      workItemId: assignment.workItem?.id ?? null,
      entry: {
        type: "session",
        version: 3,
        id: result.piSessionId,
        timestamp: result.startedAt,
        cwd: assignment.worktree,
      },
    });
    store.appendSessionLog({
      sessionId: result.piSessionId,
      runId,
      role: assignment.role,
      workItemId: assignment.workItem?.id ?? null,
      entry: {
        type: "message",
        id: "handoff",
        parentId: null,
        timestamp: result.completedAt,
        message: {
          role: "assistant",
          content: [{ type: "text", text: result.summary }],
        },
      },
    });
  }

  private open(campaignId: string): CampaignStore { return new CampaignStore(this.options.workspace, campaignId); }

  private pinnedProfile(store: CampaignStore): RepoContext {
    const profile = JSON.parse(readFileSync(resolve(store.campaignDir, "profiles/resolved.json"), "utf8")) as RepoContext;
    if (digest(profile) !== store.campaign().profileDigest) throw new Error("pinned profile digest mismatch");
    return profile;
  }

  private verifyAgentResult(store: CampaignStore, assignment: Assignment, result: Awaited<ReturnType<AgentRuntime["run"]>>): void {
    const campaign = store.campaign();
    if (result.campaignId !== campaign.id || result.requestHash !== campaign.requestHash ||
        result.requestRevision !== assignment.request.revision ||
        result.profile.id !== campaign.profileId || result.profile.version !== campaign.profileVersion ||
        result.profile.digest !== campaign.profileDigest) {
      throw new Error("agent result is not bound to the active Campaign inputs");
    }
    if (result.role !== assignment.role || result.workItemId !== (assignment.workItem?.id ?? null)) {
      throw new Error("agent result does not match its role/work-item assignment");
    }
    if (result.role === "builder") {
      if (result.checks.length === 0) throw new Error("builder result requires local check evidence");
      for (const file of result.changedFiles) assertWriteAllowed(assignment.grant, file.path);
      if (assignment.workItem?.baseSha) {
        const tracked = execFileSync("git", ["diff", "--name-only", assignment.workItem.baseSha], {
          cwd: assignment.worktree, encoding: "utf8",
        }).trim().split("\n").filter(Boolean).sort();
        const untracked = execFileSync("git", ["ls-files", "--others", "--exclude-standard"], {
          cwd: assignment.worktree, encoding: "utf8",
        }).trim().split("\n").filter(Boolean);
        const actual = [...new Set([...tracked, ...untracked])].sort();
        const reported = result.changedFiles.map((file) => file.path).sort();
        if (JSON.stringify(actual) !== JSON.stringify(reported)) throw new Error("agent changed-file claims do not match the worktree diff");
      }
    }
    if (result.role === "reviewer") {
      const required = assignment.request.requiredChecks.filter((check) =>
        check.required && check.workItem === assignment.workItem?.id,
      );
      const outcomes = new Map(result.checks.map((check) => [check.checkId, check]));
      if (required.some((check) => !outcomes.has(check.id))) throw new Error("reviewer omitted a required check");
      if (required.some((check) => outcomes.get(check.id)?.required !== true)) {
        throw new Error("reviewer misclassified a required check");
      }
    }
  }

  private async transition(
    store: CampaignStore,
    next: FactoryState,
    previous: FactoryState | null = null,
    reason: string | null = null,
  ): Promise<void> {
    this.transitionSync(store, next, previous, reason);
  }

  private transitionSync(store: CampaignStore, next: FactoryState, previous: FactoryState | null = null, reason: string | null = null): void {
    const current = store.campaign().state;
    assertTransition(current, next);
    store.setState(next, previous, reason);
  }

  private blockOnError(store: CampaignStore, error: unknown): void {
    const current = store.campaign().state;
    if (!canTransition(current, "blocked")) return;
    const reason = error instanceof Error ? error.message : "unknown error";
    this.transitionSync(store, "blocked", current, reason);
  }

  private applyAdvancementDecision(
    store: CampaignStore,
    request: FeatureRequest,
    outcome: CampaignAdvancementOutcome,
  ): void {
    const campaign = store.campaign();
    const decision = decideCampaignTransition(campaign.state, outcome, {
      repairCycles: campaign.repairCycles,
      maxRepairCycles: request.budgets.maxRepairCyclesPerRepository,
    });
    this.transitionSync(store, decision.nextState, null, decision.reason ?? null);
    if (decision.kind === "fail") throw new Error(decision.reason);
  }
}

