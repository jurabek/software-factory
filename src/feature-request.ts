import { digest, type ContractValidator } from "./contracts.js";
import type { RepoContext } from "./context.js";
import { CampaignStore, redact } from "./store.js";
import { isTerminal } from "./state-machine.js";
import type { FeatureRequest, WorkItem } from "./types.js";

export interface CreateFeatureRequestInput {
  campaignId: string;
  text: string;
  profile: RepoContext;
  profileDigest: string;
  workItems: WorkItem[];
  requestedBy: string;
  createdAt: string;
  issueUrl?: string;
}

export interface FeatureRequestApproval {
  startBuilding: boolean;
}

export class FeatureRequestModule {
  constructor(
    private readonly validator: ContractValidator,
    private readonly now: () => string = () => new Date().toISOString(),
  ) {}

  create(input: CreateFeatureRequestInput): { request: FeatureRequest; hash: string } {
    const text = String(redact(input.text));
    const requiredChecks = input.workItems.flatMap((workItem) => {
      const repository = input.profile.repositories.find((item) => item.id === workItem.repositoryId);
      if (!repository) throw new Error(`repository context not found: ${workItem.repositoryId}`);
      return repository.checks.map((check) => ({
        id: `CHECK-${repository.id}-${check.id}`,
        workItem: workItem.id,
        kind: check.id,
        required: true,
        executor: "tester",
        deferTo: null,
      }));
    });
    const request: FeatureRequest = {
      schemaVersion: "1.0.0",
      requestId: `FR-${input.campaignId.slice(3)}`,
      campaignId: input.campaignId,
      revision: 1,
      previousRevisionHash: null,
      status: "awaiting_approval",
      profile: { id: input.profile.id, version: input.profile.version, digest: input.profileDigest },
      title: text.slice(0, 100),
      source: {
        kind: input.issueUrl ? "github_issue" : "free_form",
        reference: input.issueUrl ?? text,
        url: input.issueUrl ?? null,
      },
      owner: { kind: "human", id: input.requestedBy },
      requestedBy: { kind: "human", id: input.requestedBy },
      businessOutcome: text,
      nonGoals: ["Remote GitHub mutations", "Autonomous deployment"],
      acceptanceCriteria: [{
        id: "AC-1",
        statement: text,
        verification: requiredChecks.map((check) => check.id),
      }],
      risk: { level: "medium", signals: [], rationale: "Default until the planner revises risk from the request" },
      workItems: input.workItems,
      contracts: [],
      trafficEdges: [],
      dependencyGraph: { nodes: input.workItems.map((item) => item.id), edges: [] },
      requiredChecks,
      environments: [],
      rollout: {
        strategy: "feature_flag",
        order: input.workItems.map((item) => item.id),
        stopConditions: ["Any required check fails"],
      },
      observability: {
        expectedSignals: [],
        traceQueries: [],
        logQueries: [],
        metricQueries: [],
        piiLogScan: true,
      },
      security: {
        dataClasses: [],
        allowedEgress: [],
        credentialClasses: [],
        prohibitedLogging: [],
        requiredReviews: input.profile.requiredReviewKinds,
      },
      approvalPolicy: {
        required: ["plan", "codeowners"],
        expiryMinutes: 120,
        invalidateOn: ["request", "profile", "base-sha", "write-scope"],
      },
      rollback: {
        classification: "application",
        steps: ["Disable feature", "Restore prior application revision"],
        irreversiblePolicy: "Do not automatically reverse irreversible user-visible effects",
        approval: "rollback",
      },
      budgets: {
        maxCostUsd: 25,
        maxElapsedMinutes: 120,
        maxConcurrentBuilders: 3,
        maxRepairCyclesPerRepository: 3,
        maxStorageMiB: 2048,
      },
      waivers: [],
      unresolved: [],
      createdAt: input.createdAt,
      updatedAt: input.createdAt,
    };
    this.validator.request(request);
    return { request, hash: digest(request) };
  }

  approve(store: CampaignStore, kind: string, actor: string): FeatureRequestApproval {
    const request = store.request();
    const campaign = store.campaign();
    if (kind === "plan") {
      if (campaign.state !== "awaiting_plan_approval" || request.status !== "awaiting_approval") {
        throw new Error("plan approval is not currently requested");
      }
    } else if (kind === "waiver") {
      if (request.waivers.length === 0) throw new Error("no waiver is awaiting approval");
    } else {
      throw new Error(`unsupported local approval kind: ${kind}`);
    }
    store.approve(kind, actor, request.approvalPolicy.expiryMinutes);
    return { startBuilding: kind === "plan" && campaign.state === "awaiting_plan_approval" };
  }

  amend(store: CampaignStore, pointer: string, value: unknown): FeatureRequest {
    if (isTerminal(store.campaign().state)) throw new Error("terminal Campaigns cannot be amended");
    const request = structuredClone(store.request());
    applyJsonPointer(request as unknown as Record<string, unknown>, pointer, value);
    request.previousRevisionHash = store.campaign().requestHash;
    request.revision += 1;
    request.status = "draft";
    request.updatedAt = this.now();
    return this.bindRevision(store, request);
  }

  submit(store: CampaignStore): FeatureRequest {
    const request = store.request();
    if (request.status !== "draft") throw new Error("only draft requests can be submitted");
    request.status = "awaiting_approval";
    request.updatedAt = this.now();
    return this.bindRevision(store, request);
  }

  proposeWaiver(
    store: CampaignStore,
    checkId: string,
    issueUrl: string,
    expiresAt: string,
    reason: string,
  ): FeatureRequest {
    const request = structuredClone(store.request());
    if (!request.requiredChecks.some((check) => check.id === checkId)) {
      throw new Error(`unknown check: ${checkId}`);
    }
    request.previousRevisionHash = store.campaign().requestHash;
    request.revision += 1;
    request.status = "awaiting_approval";
    request.updatedAt = this.now();
    request.waivers.push({
      id: `WAIVER-${checkId.slice(6)}-${request.revision}`.toLowerCase().replace(/^waiver-/, "WAIVER-"),
      checkId,
      issueUrl,
      owner: { kind: "human", id: "local-developer" },
      reason,
      baselineEvidence: `Campaign ${request.campaignId} check ${checkId}`,
      expiresAt,
      approvedBy: { kind: "human", id: "pending-approval" },
    });
    return this.bindRevision(store, request);
  }

  private bindRevision(store: CampaignStore, request: FeatureRequest): FeatureRequest {
    this.validator.request(request);
    store.bindRequest(request, digest(request));
    return request;
  }
}

function applyJsonPointer(root: Record<string, unknown>, pointer: string, value: unknown): void {
  if (!pointer.startsWith("/")) throw new Error("JSON pointer must start with /");
  const parts = pointer.slice(1).split("/").map((part) =>
    part.replaceAll("~1", "/").replaceAll("~0", "~"),
  );
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
