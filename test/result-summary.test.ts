import { describe, expect, it } from "vitest";
import { formatAgentResult, summarizeAgentResult } from "../packages/core/src/result-summary.js";
import type { AgentResult } from "../packages/core/src/types.js";

function result(overrides: Partial<AgentResult> = {}): AgentResult {
  return {
    schemaVersion: "1.0.0",
    resultId: "11111111-1111-4111-8111-111111111111",
    campaignId: "SF-2026-7791",
    requestRevision: 1,
    requestHash: "a".repeat(64),
    profile: { id: "local", version: "1.0.0", digest: "b".repeat(64) },
    workItemId: "WI-software-factory-1",
    role: "builder",
    workerRunId: "builder-1",
    piSessionId: "session-1",
    status: "completed",
    inputs: [],
    plan: null,
    decisions: [{ id: "format-results", statement: "Show readable results." }],
    unresolved: [],
    changedFiles: [{
      path: "packages/core/src/result-summary.ts",
      change: "added",
      purpose: "Format agent results",
      generated: false,
      digest: "c".repeat(64),
    }],
    contractChanges: [],
    trafficChanges: [],
    commands: [],
    checks: [{
      checkId: "CHECK-software-factory-unit",
      status: "passed",
      required: true,
      attempt: 1,
      failureClass: null,
      evidence: [],
    }],
    findings: [],
    risks: [],
    artifacts: [],
    gitState: null,
    nextActions: [],
    summary: "The result is ready to inspect.",
    startedAt: "2026-09-02T20:00:00.000Z",
    completedAt: "2026-09-02T20:01:00.000Z",
    ...overrides,
  };
}

describe("agent result summaries", () => {
  it("turns result fields into readable sections", () => {
    const summary = summarizeAgentResult(result());

    expect(summary.summary).toBe("The result is ready to inspect.");
    expect(summary.sections).toEqual([
      { title: "Decisions", items: ["format-results: Show readable results."] },
      {
        title: "Changed files",
        items: ["packages/core/src/result-summary.ts (added) — Format agent results"],
      },
      {
        title: "Checks",
        items: ["CHECK-software-factory-unit: passed (required)"],
      },
    ]);
    expect(formatAgentResult(result())).toContain("BUILDER · completed · WI-software-factory-1");
  });

  it("ignores malformed optional entries instead of throwing", () => {
    const malformed = result({
      status: "failed",
      decisions: [null, "raw", { id: "missing-statement" }],
      changedFiles: [],
      risks: [{ severity: "high", statement: "Check failed." }],
    });

    expect(summarizeAgentResult(malformed).sections).toEqual([
      { title: "Checks", items: ["CHECK-software-factory-unit: passed (required)"] },
      { title: "Risks", items: ["high: Check failed."] },
    ]);
    expect(formatAgentResult(malformed)).toContain("BUILDER · failed");
  });
});
