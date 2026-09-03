import { randomUUID } from "node:crypto";
import type { AgentResult } from "../packages/core/src/types.js";
import type { AgentRuntime, Assignment } from "../packages/core/src/runtime.js";

export type StubBehavior = (assignment: Assignment) => Partial<Pick<
  AgentResult,
  "status" | "findings" | "checks" | "changedFiles" | "summary"
>>;

export class StubAgentRuntime implements AgentRuntime {
  constructor(private readonly behavior?: StubBehavior) {}

  async run(assignment: Assignment): Promise<AgentResult> {
    const now = new Date().toISOString();
    const custom = this.behavior?.(assignment) ?? {};
    const evidence = {
      kind: "fixture",
      reference: `${assignment.role}:${assignment.workItem?.id ?? "campaign"}:${assignment.attempt}`,
      digest: null,
      classification: "internal",
    };
    const relevantChecks = assignment.request.requiredChecks.filter((check) =>
      check.workItem === assignment.workItem?.id,
    );
    const checks = assignment.role === "reviewer" || assignment.role === "builder"
      ? relevantChecks.map((check) => ({
          checkId: check.id,
          status: check.executor === "sandbox" || check.executor === "reviewer" ? "passed" as const : "deferred" as const,
          required: check.required,
          attempt: assignment.attempt,
          failureClass: null,
          evidence: [evidence],
        }))
      : [];
    return {
      schemaVersion: "1.0.0",
      resultId: randomUUID(),
      campaignId: assignment.campaign.id,
      requestRevision: assignment.request.revision,
      requestHash: assignment.campaign.requestHash,
      profile: assignment.request.profile,
      workItemId: assignment.workItem?.id ?? null,
      role: assignment.role,
      workerRunId: `${assignment.role}-${assignment.workItem?.id ?? "campaign"}-${assignment.attempt}`,
      piSessionId: `stub-${randomUUID()}`,
      status: custom.status ?? "completed",
      inputs: [evidence],
      plan: assignment.role === "planner" ? {
        workItems: assignment.request.workItems.map((item) => item.id),
        dependencyEdges: assignment.request.dependencyGraph.edges.map((edge) => `${edge.from}->${edge.to}`),
        requiredChecks: assignment.request.requiredChecks.map((check) => check.id),
        requiredApprovals: assignment.request.approvalPolicy.required,
      } : null,
      decisions: [],
      unresolved: [],
      changedFiles: custom.changedFiles ?? [],
      contractChanges: [],
      trafficChanges: [],
      commands: [],
      checks: custom.checks ?? checks,
      findings: custom.findings ?? [],
      risks: [],
      artifacts: [evidence],
      gitState: null,
      nextActions: [],
      summary: custom.summary ?? `${assignment.role} fixture completed`,
      startedAt: now,
      completedAt: now,
    };
  }
}
