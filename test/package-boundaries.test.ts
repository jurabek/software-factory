import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { CampaignReadModel } from "../packages/core/src/read.js";
import { CampaignStore } from "../packages/core/src/store.js";
import type { Campaign, FeatureRequest } from "../packages/core/src/types.js";

const roots: string[] = [];
afterEach(() => {
  while (roots.length) rmSync(roots.pop()!, { recursive: true, force: true });
});

describe("package boundaries", () => {
  it("keeps swf independent from the UI package", () => {
    const cli = readFileSync(resolve(import.meta.dirname, "../packages/cli/src/cli.ts"), "utf8");
    expect(cli).not.toContain("@software-factory/ui");
    expect(cli).not.toContain("startVisualizer");
    expect(cli).not.toContain("visualizer-assets");
    expect(cli).not.toContain("background-visualizer");
  });

  it("publishes only the intended executables", () => {
    const root = JSON.parse(readFileSync(resolve(import.meta.dirname, "../package.json"), "utf8"));
    const cli = JSON.parse(readFileSync(resolve(import.meta.dirname, "../packages/cli/package.json"), "utf8"));
    const ui = JSON.parse(readFileSync(resolve(import.meta.dirname, "../packages/ui/package.json"), "utf8"));
    expect(root).not.toHaveProperty("bin");
    expect(cli.bin).toEqual({ swf: "./dist/cli.js" });
    expect(ui.bin).toEqual({ "swf-ui": "./dist/cli.js" });
  });
});

describe("CampaignReadModel", () => {
  it("reads Campaigns without exposing a writable store", () => {
    const workspace = mkdtempSync(resolve(tmpdir(), "swf-read-model-"));
    roots.push(workspace);
    const id = "SF-2026-9001";
    const store = new CampaignStore(workspace, id);
    const now = new Date().toISOString();
    const campaign: Campaign = {
      id,
      title: "Read model",
      state: "received",
      previousState: null,
      requestHash: "a".repeat(64),
      profileId: "local",
      profileVersion: "1.0.0",
      profileDigest: "b".repeat(64),
      repairCycles: 0,
      pausedReason: null,
      createdAt: now,
      updatedAt: now,
    };
    const request = {
      schemaVersion: "1.0.0",
      requestId: "FR-2026-9001",
      campaignId: id,
      revision: 1,
      status: "awaiting_approval",
      title: "Read model",
      updatedAt: now,
      dependencyGraph: { edges: [] },
    } as unknown as FeatureRequest;
    store.createCampaign(campaign, request);
    store.close();

    const reader = new CampaignReadModel(workspace);
    expect(reader.list()).toEqual([campaign]);
    expect(reader.detail(id).campaign).toEqual(campaign);
  });

  it("rejects Campaigns persisted in removed lifecycle states", () => {
    const workspace = mkdtempSync(resolve(tmpdir(), "swf-legacy-state-"));
    roots.push(workspace);
    const id = "SF-2026-9002";
    const store = new CampaignStore(workspace, id);
    const now = new Date().toISOString();
    store.db.prepare(`
      INSERT INTO campaigns VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).run(id, "Legacy", "validating_ci", null, "a".repeat(64), "local", "1.0.0", "b".repeat(64), 0, null, now, now);
    store.close();

    expect(() => new CampaignReadModel(workspace).detail(id))
      .toThrow("unsupported legacy state: validating_ci");
  });
});
