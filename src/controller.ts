import { randomInt } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { execFileSync } from "node:child_process";
import { loadFactoryConfig, resolveAgent } from "./config.js";
import { ContractValidator, digest } from "./contracts.js";
import { resolveRepoContext, type RepoContext } from "./context.js";
import { GhCliDelivery, type DeliveryRuntime } from "./github.js";
import { assertWriteAllowed } from "./policy.js";
import { RepositoryManager, toWorkItem } from "./repositories.js";
import { FakeAgentRuntime, PiAgentRuntime, type AgentRuntime, type Assignment } from "./runtime.js";
import { compileRolePrompt } from "./prompts.js";
import { assertTransition, isTerminal } from "./state-machine.js";
import { CampaignStore, redact } from "./store.js";
import type { Campaign, FactoryConfig, FactoryState, FeatureRequest, WorkItem } from "./types.js";

export interface FactoryOptions {
  workspace: string;
  repositoryRoot: string;
  runtime?: "fake" | "pi" | AgentRuntime;
  delivery?: "github" | DeliveryRuntime;
  config?: FactoryConfig;
}

export class SoftwareFactory {
  private githubDelivery: GhCliDelivery | undefined;

  private constructor(
    private readonly options: FactoryOptions,
    private readonly validator: ContractValidator,
  ) {}

  static async create(options: FactoryOptions): Promise<SoftwareFactory> {
    mkdirSync(options.workspace, { recursive: true });
    const config = options.config ?? loadFactoryConfig();
    return new SoftwareFactory({ ...options, config }, await ContractValidator.create());
  }

  get config(): FactoryConfig {
    return this.options.config ?? loadFactoryConfig();
  }

  async request(input: { text: string; cwd?: string; repos?: string[]; requestedBy?: string; issueUrl?: string }): Promise<Campaign> {
    const cwd = input.cwd ?? this.options.repositoryRoot;
    const context = resolveRepoContext(cwd, this.config, input.repos);
    const declaredChecks = context.repositories.flatMap((repository) => repository.checkIds);
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
      const safeText = String(redact(input.text));
      const request = this.normalizeRequest(
        campaignId, safeText, context, profileDigest, workItems,
        input.requestedBy ?? "local-developer", now, input.issueUrl,
      );
      this.validator.request(request);
      const requestHash = digest(request);
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
      await this.transition(store, result.status === "completed" ? "awaiting_plan_approval" : "blocked");
      return store.campaign();
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
      const request = store.request();
      if (kind === "plan") {
        if (store.campaign().state !== "awaiting_plan_approval" || request.status !== "awaiting_approval") {
          throw new Error("plan approval is not currently requested");
        }
      } else if (kind === "waiver") {
        if (request.waivers.length === 0) throw new Error("no waiver is awaiting approval");
      } else {
        throw new Error(`unsupported local approval kind: ${kind}`);
      }
      store.approve(kind, actor, request.approvalPolicy.expiryMinutes);
      if (kind === "plan" && store.campaign().state === "awaiting_plan_approval") {
        this.transitionSync(store, "building");
      }
      return store.campaign();
    } finally { store.close(); }
  }

  async advance(campaignId: string, target: FactoryState = "implementation_complete"): Promise<Campaign> {
    const store = this.open(campaignId);
    try {
      await store.listenBus();
      const profile = this.pinnedProfile(store);
      const request = store.request();
      for (let guard = 0; guard < 50; guard += 1) {
        const state = store.campaign().state;
        if (state === target || isTerminal(state) || ["blocked", "paused"].includes(state)) break;
        if (state === "awaiting_plan_approval") throw new Error("plan approval is required");
        if (["building", "repairing_review", "repairing_test", "repairing_ci"].includes(state)) {
          await this.runBuilders(store, profile, request, state !== "building");
          const next = state === "building" ? "reviewing"
            : state === "repairing_review" ? "re_reviewing"
              : state === "repairing_test" ? "re_reviewing_after_test"
                : "re_reviewing_after_ci";
          await this.transition(store, next);
          continue;
        }
        if (["reviewing", "re_reviewing", "re_reviewing_after_test", "re_reviewing_after_ci"].includes(state)) {
          const blocked = await this.runReviewers(store, profile, request);
          if (blocked) {
            this.assertRepairBudget(store, request);
            const next = state === "re_reviewing_after_test" ? "repairing_test"
              : state === "re_reviewing_after_ci" ? "repairing_ci"
                : "repairing_review";
            await this.transition(store, next);
          } else {
            const next = state === "re_reviewing_after_test" ? "re_testing"
              : state === "re_reviewing_after_ci" ? "re_testing_after_ci"
                : "testing";
            await this.transition(store, next);
          }
          continue;
        }
        if (["testing", "re_testing", "re_testing_after_ci"].includes(state)) {
          const failed = await this.runTester(store, profile, request);
          if (failed) {
            this.assertRepairBudget(store, request);
            await this.transition(store, state === "re_testing_after_ci" ? "repairing_ci" : "repairing_test");
          } else {
            await this.transition(store, this.options.delivery ? "opening_prs" : "awaiting_human_review");
          }
          continue;
        }
        if (state === "opening_prs") {
          const manager = new RepositoryManager(store, profile);
          const deliveries = this.delivery().openDraftPullRequests({ campaign: store.campaign(), request, profile, repositories: manager, store });
          const expectedDeliveries = request.workItems.filter((item) =>
            profile.repositories.find((repository) => repository.id === item.repositoryId)?.mode !== "read_only",
          );
          if (deliveries.length !== expectedDeliveries.length || deliveries.length === 0) {
            this.transitionSync(store, "blocked", null, "one or more write-capable work items have no GitHub draft PR");
            break;
          }
          for (const delivery of deliveries) store.saveDelivery(delivery);
          await this.transition(store, "validating_ci");
          continue;
        }
        if (state === "validating_ci") {
          const manager = new RepositoryManager(store, profile);
          const deliveries = this.delivery().observeCi(
            { campaign: store.campaign(), request, profile, repositories: manager, store },
            store.deliveries(),
          );
          for (const delivery of deliveries) store.saveDelivery(delivery);
          if (deliveries.some((delivery) => delivery.ciStatus === "failed")) {
            this.assertRepairBudget(store, request);
            await this.transition(store, "repairing_ci");
            continue;
          }
          if (deliveries.length === 0 || deliveries.some((delivery) => delivery.ciStatus === "pending")) break;
          await this.transition(store, "awaiting_human_review");
          continue;
        }
        if (state === "awaiting_human_review") {
          await this.transition(store, "implementation_complete");
          continue;
        }
        throw new Error(`local mode cannot advance state ${state}`);
      }
      return store.campaign();
    } finally { store.close(); }
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
      if (campaign.state !== "paused" || !campaign.previousState) throw new Error("campaign is not resumable");
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
      const previous = store.request();
      if (isTerminal(store.campaign().state)) throw new Error("terminal Campaigns cannot be amended");
      const request = structuredClone(previous);
      applyJsonPointer(request as unknown as Record<string, unknown>, pointer, value);
      request.previousRevisionHash = store.campaign().requestHash;
      request.revision += 1;
      request.status = "draft";
      request.updatedAt = new Date().toISOString();
      this.validator.request(request);
      store.bindRequest(request, digest(request));
      if (store.campaign().state !== "awaiting_plan_approval") store.setState("awaiting_plan_approval");
      return request;
    } finally { store.close(); }
  }

  submit(campaignId: string): FeatureRequest {
    const store = this.open(campaignId);
    try {
      const request = store.request();
      if (request.status !== "draft") throw new Error("only draft requests can be submitted");
      request.status = "awaiting_approval";
      request.updatedAt = new Date().toISOString();
      store.bindRequest(request, digest(request));
      return request;
    } finally { store.close(); }
  }

  proposeWaiver(campaignId: string, checkId: string, issueUrl: string, expiresAt: string, reason: string): FeatureRequest {
    const store = this.open(campaignId);
    try {
      const request = structuredClone(store.request());
      if (!request.requiredChecks.some((check) => check.id === checkId)) throw new Error(`unknown check: ${checkId}`);
      const previousHash = store.campaign().requestHash;
      request.previousRevisionHash = previousHash;
      request.revision += 1;
      request.status = "awaiting_approval";
      request.updatedAt = new Date().toISOString();
      request.waivers.push({
        id: `WAIVER-${checkId.slice(6)}-${request.revision}`.toLowerCase().replace(/^waiver-/, "WAIVER-"),
        checkId,
        issueUrl,
        owner: { kind: "human", id: "local-developer" },
        reason,
        baselineEvidence: `Campaign ${campaignId} check ${checkId}`,
        expiresAt,
        approvedBy: { kind: "human", id: "pending-approval" },
      });
      this.validator.request(request);
      store.bindRequest(request, digest(request));
      if (store.campaign().state !== "awaiting_plan_approval") store.setState("awaiting_plan_approval");
      return request;
    } finally { store.close(); }
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

  private normalizeRequest(
    campaignId: string, text: string, profile: RepoContext, profileDigest: string,
    workItems: WorkItem[], requestedBy: string, now: string, issueUrl?: string,
  ): FeatureRequest {
    const requiredChecks = workItems.flatMap((workItem) => {
      const repository = profile.repositories.find((item) => item.id === workItem.repositoryId);
      return (repository?.checkIds ?? []).map((checkId) => ({
        id: `CHECK-${checkId}`, workItem: workItem.id, kind: checkId, required: true,
        executor: "tester", deferTo: null,
      }));
    });
    return {
      schemaVersion: "1.0.0", requestId: `FR-${campaignId.slice(3)}`, campaignId,
      revision: 1, previousRevisionHash: null, status: "awaiting_approval",
      profile: { id: profile.id, version: profile.version, digest: profileDigest },
      title: text.slice(0, 100), source: {
        kind: issueUrl ? "github_issue" : "free_form",
        reference: issueUrl ?? text,
        url: issueUrl ?? null,
      },
      owner: { kind: "human", id: requestedBy }, requestedBy: { kind: "human", id: requestedBy },
      businessOutcome: text, nonGoals: ["Remote GitHub mutations", "Autonomous deployment"],
      acceptanceCriteria: [{ id: "AC-1", statement: text, verification: requiredChecks.map((check) => check.id) }],
      risk: { level: "medium", signals: [], rationale: "Default until the planner revises risk from the request" },
      workItems, contracts: [], trafficEdges: [],
      dependencyGraph: { nodes: workItems.map((item) => item.id), edges: [] },
      requiredChecks, environments: [],
      rollout: { strategy: "feature_flag", order: workItems.map((item) => item.id), stopConditions: ["Any required check fails"] },
      observability: { expectedSignals: [], traceQueries: [], logQueries: [], metricQueries: [], piiLogScan: true },
      security: {
        dataClasses: [], allowedEgress: [], credentialClasses: [],
        prohibitedLogging: [],
        requiredReviews: this.config.requiredReviewKinds,
      },
      approvalPolicy: { required: ["plan", "codeowners"], expiryMinutes: 120, invalidateOn: ["request", "profile", "base-sha", "write-scope"] },
      rollback: { classification: "application", steps: ["Disable feature", "Restore prior application revision"], irreversiblePolicy: "Do not automatically reverse irreversible user-visible effects", approval: "rollback" },
      budgets: { maxCostUsd: 25, maxElapsedMinutes: 120, maxConcurrentBuilders: 3, maxRepairCyclesPerRepository: 3, maxStorageMiB: 2048 },
      waivers: [], unresolved: [], createdAt: now, updatedAt: now,
    };
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
    return results.some((result) => result.status === "changes_requested" || result.findings.some((finding) => finding.blocking));
  }

  private async runTester(store: CampaignStore, profile: RepoContext, request: FeatureRequest): Promise<boolean> {
    const result = await this.executeAgent(store, this.assignment(
      store, profile, request, "tester", null, store.campaign().repairCycles + 1, this.options.repositoryRoot,
    ));
    this.validator.result(result);
    store.saveResult(result);
    return result.status !== "completed" || result.checks.some((check) => check.required && !["passed", "waived"].includes(check.status));
  }

  private assignment(
    store: CampaignStore, profile: RepoContext, request: FeatureRequest, role: Assignment["role"],
    workItem: WorkItem | null, attempt: number, worktree: string,
  ): Assignment {
    const repository = workItem ? profile.repositories.find((item) => item.id === workItem.repositoryId) : undefined;
    const agent = resolveAgent(this.config, role);
    const prompts = compileRolePrompt({
      role,
      request,
      workItem,
      peerSessions: store.sessionCatalog(),
      factorySocket: store.socketPath,
      worktree,
      attempt,
      systemTemplate: agent.promptEngineering.system,
      userTemplate: agent.promptEngineering.user,
    });
    return {
      campaign: store.campaign(), request, role, workItem, attempt, worktree,
      grant: {
        role, worktree,
        writePaths: workItem?.writePaths ?? [],
        generatedPaths: repository?.generatedPaths ?? [],
        commandIds: repository?.checkIds ?? request.requiredChecks.map((check) => check.kind),
        allowedHosts: [],
      },
      systemPrompt: prompts.system,
      prompt: [prompts.user, this.repairContext(store, role)].filter(Boolean).join("\n\n"),
      agent,
    };
  }

  private runtime(store: CampaignStore): AgentRuntime {
    if (typeof this.options.runtime === "object") return this.options.runtime;
    return this.options.runtime === "pi" ? new PiAgentRuntime(this.validator, store) : new FakeAgentRuntime();
  }

  private delivery(): DeliveryRuntime {
    if (!this.options.delivery) throw new Error("GitHub delivery is not enabled");
    if (typeof this.options.delivery === "object") return this.options.delivery;
    this.githubDelivery ??= new GhCliDelivery();
    return this.githubDelivery;
  }

  private repairContext(store: CampaignStore, role: Assignment["role"]): string {
    if (role !== "builder" || store.campaign().state !== "repairing_ci") return "";
    const failed = store.deliveries().filter((delivery) => delivery.ciStatus === "failed");
    if (!failed.length) return "";
    return [
      "Controller-verified CI failures requiring repair:",
      JSON.stringify(failed.map((delivery) => ({
        repositoryId: delivery.repositoryId,
        headSha: delivery.headSha,
        pullRequestUrl: delivery.pullRequestUrl,
        checks: delivery.checks.filter((check) => ["fail", "failure", "cancel", "cancelled", "timed_out"].includes(check.bucket.toLowerCase())),
      })), null, 2),
    ].join("\n");
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
    if (result.role === "tester") {
      const required = assignment.request.requiredChecks.filter((check) => check.required);
      const outcomes = new Map(result.checks.map((check) => [check.checkId, check]));
      if (required.some((check) => !outcomes.has(check.id))) throw new Error("tester omitted a required check");
      for (const requirement of required) {
        const outcome = outcomes.get(requirement.id)!;
        if (outcome.status === "waived") {
          const waivers = assignment.request.waivers as Array<{ id: string; checkId: string; expiresAt: string }>;
          const waiver = waivers.find((candidate) => candidate.id === outcome.waiverId && candidate.checkId === requirement.id);
          if (!waiver || waiver.expiresAt <= new Date().toISOString() || !store.hasApproval("waiver")) {
            throw new Error(`check ${requirement.id} has no valid approved waiver`);
          }
        }
      }
    }
  }

  private async transition(store: CampaignStore, next: FactoryState): Promise<void> { this.transitionSync(store, next); }

  private transitionSync(store: CampaignStore, next: FactoryState, previous: FactoryState | null = null, reason: string | null = null): void {
    const current = store.campaign().state;
    assertTransition(current, next);
    store.setState(next, previous, reason);
  }

  private assertRepairBudget(store: CampaignStore, request: FeatureRequest): void {
    if (store.campaign().repairCycles >= request.budgets.maxRepairCyclesPerRepository) {
      this.transitionSync(store, "failed");
      throw new Error("repair budget exhausted");
    }
  }
}

function applyJsonPointer(root: Record<string, unknown>, pointer: string, value: unknown): void {
  if (!pointer.startsWith("/")) throw new Error("JSON pointer must start with /");
  const parts = pointer.slice(1).split("/").map((part) => part.replaceAll("~1", "/").replaceAll("~0", "~"));
  let current: Record<string, unknown> = root;
  for (const part of parts.slice(0, -1)) {
    const next = current[part];
    if (!next || typeof next !== "object") throw new Error(`JSON pointer does not exist: ${pointer}`);
    current = next as Record<string, unknown>;
  }
  const key = parts.at(-1);
  if (!key) throw new Error("cannot replace document root");
  current[key] = value;
}

