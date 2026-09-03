import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { ContractValidator, digest } from "../packages/core/src/contracts.js";
import type { RepoContext } from "../packages/core/src/context.js";
import { FeatureRequestModule } from "../packages/core/src/feature-request.js";
import { CampaignStore } from "../packages/core/src/store.js";
import type { Campaign, WorkItem } from "../packages/core/src/types.js";

const roots: string[] = [];
const now = "2026-09-01T15:00:00.000Z";
const later = "2026-09-01T16:00:00.000Z";
let validator: ContractValidator;

beforeAll(async () => {
  validator = await ContractValidator.create();
});

afterEach(() => {
  while (roots.length) rmSync(roots.pop()!, { recursive: true, force: true });
});

function fixture() {
  const workspace = mkdtempSync(resolve(tmpdir(), "swf-feature-request-"));
  roots.push(workspace);
  const profile: RepoContext = {
    schemaVersion: "1.0.0",
    id: "local",
    version: "1.0.0",
    name: "Local",
    repositories: [{
      id: "app",
      url: "local://app",
      defaultBranch: "main",
      adapter: "local-git",
      mode: "read_write",
      instructionPaths: ["AGENTS.md"],
      defaultWritePaths: ["**"],
      generatedPaths: [],
      checks: [{ id: "unit", command: "npm test" }],
      effectiveRiskSignals: ["authentication"],
      path: "/tmp/app",
      agentsInstructions: "## Software Factory",
    }],
    riskDefaults: { highRiskSignals: ["authentication"], prohibitedEvidenceData: ["secrets"] },
    requiredReviewKinds: ["spec", "standards"],
    approvalRules: [],
    evaluationScenarios: [],
  };
  const workItem: WorkItem = {
    id: "WI-app-1",
    repositoryId: "app",
    repositoryUrl: "local://app",
    baseBranch: "main",
    baseSha: "a".repeat(40),
    purpose: "Add the feature",
    writePaths: ["**"],
    generatedPaths: [],
    dependsOn: [],
  };
  const module = new FeatureRequestModule(validator, () => later);
  const created = module.create({
    campaignId: "SF-2026-1234",
    text: "Add the feature",
    profile,
    profileDigest: digest(profile),
    workItems: [workItem],
    requestedBy: "developer",
    createdAt: now,
  });
  const campaign: Campaign = {
    id: created.request.campaignId,
    title: created.request.title,
    state: "awaiting_plan_approval",
    requestHash: created.hash,
    profileId: profile.id,
    profileVersion: profile.version,
    profileDigest: digest(profile),
    createdAt: now,
    updatedAt: now,
    previousState: null,
    repairCycles: 0,
    pausedReason: null,
  };
  const store = new CampaignStore(workspace, campaign.id);
  store.createCampaign(campaign, created.request);
  return { module, store, created };
}

describe("Feature Request Module", () => {
  it("creates and hashes a canonical request from Repository Context", () => {
    const { store, created } = fixture();
    try {
      expect(created.request).toMatchObject({
        requestId: "FR-2026-1234",
        revision: 1,
        status: "awaiting_approval",
        requiredChecks: [{
          id: "CHECK-app-unit",
          workItem: "WI-app-1",
          kind: "unit",
          executor: "reviewer",
        }],
      });
      expect(created.request.security.requiredReviews).toEqual(["spec", "standards"]);
      expect(created.hash).toBe(digest(created.request));
    } finally {
      store.close();
    }
  });

  it("owns amendment, submission, hashing, and approval invalidation", () => {
    const { module, store, created } = fixture();
    try {
      expect(module.approve(store, "plan", "developer")).toEqual({ startBuilding: true });
      expect(store.hasApproval("plan")).toBe(true);

      const amended = module.amend(store, "/businessOutcome", "Changed outcome");
      expect(amended).toMatchObject({
        revision: 2,
        previousRevisionHash: created.hash,
        status: "draft",
        businessOutcome: "Changed outcome",
        updatedAt: later,
      });
      expect(store.campaign().requestHash).toBe(digest(amended));
      expect(store.hasApproval("plan")).toBe(false);

      const submitted = module.submit(store);
      expect(submitted.revision).toBe(2);
      expect(submitted.status).toBe("awaiting_approval");
      expect(store.campaign().requestHash).toBe(digest(submitted));
    } finally {
      store.close();
    }
  });

  it("rejects invalid lifecycle operations through its interface", () => {
    const { module, store } = fixture();
    try {
      expect(() => module.submit(store)).toThrow("only draft requests can be submitted");
      expect(() => module.amend(store, "businessOutcome", "Invalid pointer"))
        .toThrow("JSON pointer must start with /");
      store.setState("implementation_complete");
      expect(() => module.amend(store, "/businessOutcome", "Too late"))
        .toThrow("terminal Campaigns cannot be amended");
    } finally {
      store.close();
    }
  });

});
