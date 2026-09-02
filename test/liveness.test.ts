import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { appendRawSdkEvent } from "../packages/core/src/raw-events.js";
import { CampaignStore } from "../packages/core/src/store.js";
import type { Campaign, FeatureRequest } from "../packages/core/src/types.js";

const roots: string[] = [];

afterEach(() => {
  while (roots.length) rmSync(roots.pop()!, { recursive: true, force: true });
});

function campaignStore(): CampaignStore {
  const workspace = mkdtempSync(resolve(tmpdir(), "swf-liveness-"));
  roots.push(workspace);
  const id = "SF-2026-9101";
  const store = new CampaignStore(workspace, id);
  const now = new Date().toISOString();
  const campaign: Campaign = {
    id,
    title: "Track liveness",
    state: "planning",
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
  store.createCampaign(campaign, {
    schemaVersion: "1.0.0",
    requestId: "FR-2026-9101",
    campaignId: id,
    revision: 1,
    status: "awaiting_approval",
    title: campaign.title,
    updatedAt: now,
    dependencyGraph: { edges: [] },
  } as unknown as FeatureRequest);
  return store;
}

describe("runtime liveness", () => {
  it("tracks processes and open model requests until the agent finishes", () => {
    const store = campaignStore();
    const runId = "planner-campaign-1";
    store.startAgent(runId, "planner", null, "session-1", 1);
    const processId = store.startProcess(runId, "agent", "planner", 1234, "swf agent planner");
    const requestId = store.event("model_request", { runId, role: "planner" });
    store.event("model_heartbeat", {
      runId,
      role: "planner",
      elapsedMs: 5_000,
      thinkingChars: 200,
      textChars: 12,
    }, requestId);

    const active = store.liveness();
    expect(active.runs).toHaveLength(1);
    expect(active.runs[0]).toMatchObject({
      id: runId,
      stage: "reasoning",
      stale: false,
      modelRequest: {
        eventId: requestId,
        progress: { thinkingChars: 200, textChars: 12 },
      },
      processes: [{ id: processId, pid: 1234, running: true }],
    });

    store.event("model_response", { runId, role: "planner", status: "success" }, requestId);
    expect(store.liveness().runs[0]).toMatchObject({ stage: "idle", modelRequest: null });

    store.finishAgent(runId, "completed");
    expect(store.liveness().runs).toEqual([]);
    expect(store.rows("processes")[0]?.ended_at).toEqual(expect.any(String));
    store.close();
  });

  it("marks a running agent stale after its last signal ages past the threshold", () => {
    const store = campaignStore();
    store.startAgent("planner-campaign-1", "planner", null, "session-1", 1);
    const lastEvent = store.rows("events").at(-1);
    const lastActivity = Date.parse(String(lastEvent?.created_at));
    expect(store.liveness(lastActivity + 15_001).runs[0]).toMatchObject({
      stale: true,
      lastActivityMs: 15_001,
    });
    store.close();
  });

  it("appends SDK events immediately to a JSONL diagnostic mirror", () => {
    const root = mkdtempSync(resolve(tmpdir(), "swf-raw-events-"));
    roots.push(root);
    const path = resolve(root, "raw-events.jsonl");
    appendRawSdkEvent(path, "message_update", {
      assistantMessageEvent: { type: "thinking_delta", delta: "working" },
    });

    const record = JSON.parse(readFileSync(path, "utf8").trim());
    expect(record).toMatchObject({
      type: "message_update",
      payload: { assistantMessageEvent: { type: "thinking_delta", delta: "working" } },
    });
    expect(record.timestamp).toEqual(expect.any(String));
  });
});
