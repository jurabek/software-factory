export const factoryStates = [
  "received", "planning", "awaiting_plan_approval", "building", "reviewing",
  "repairing_review", "re_reviewing", "awaiting_human_review", "implementation_complete",
  "blocked", "paused", "failed", "aborting", "aborted",
] as const;

export type FactoryState = (typeof factoryStates)[number];
export type AgentRole = "planner" | "builder" | "reviewer";
export type CheckStatus = "passed" | "failed" | "deferred";
export type ThinkingLevel = "off" | "minimal" | "low" | "medium" | "high" | "xhigh" | "max";

export interface PromptEngineering {
  system?: string;
  user?: string;
}

export interface AgentSpec {
  name: AgentRole;
  codingAgent?: string;
  model?: string;
  fallbackModel?: string;
  thinking?: ThinkingLevel;
  tools?: string[];
  color?: string;
  purpose?: string;
  promptEngineering?: PromptEngineering;
}

export interface ResolvedAgent {
  name: AgentRole;
  codingAgent: string;
  model: string;
  fallbackModel?: string;
  thinking: ThinkingLevel;
  tools: string[];
  promptEngineering: { system: string; user: string };
  color?: string;
  purpose?: string;
}

export interface ApprovalRule {
  id: string;
  when: string;
  approval: string;
}

export interface FactoryConfig {
  source: string;
  defaults: {
    codingAgent: string;
    model: string;
    thinking: ThinkingLevel;
    tools: string[];
  };
  observability: { pollMs: number };
  runtime: {
    agentDeadlineMs: number;
    emptyTurnRetries: number;
  };
  riskSignals: string[];
  approvalRules: ApprovalRule[];
  requiredReviewKinds: string[];
  agents: AgentSpec[];
}

export interface RepositoryCheck {
  id: string;
  command: string;
}

export interface ProfileRepository {
  id: string;
  url: string;
  defaultBranch: string;
  adapter: string;
  mode: "read_write" | "read_only" | "break_glass_write";
  instructionPaths: string[];
  defaultWritePaths: string[];
  generatedPaths: string[];
  checks: RepositoryCheck[];
  effectiveRiskSignals: string[];
}

export interface WorkItem {
  id: string;
  repositoryId: string;
  repositoryUrl: string;
  baseBranch: string;
  baseSha: string | null;
  purpose: string;
  writePaths: string[];
  generatedPaths: string[];
  dependsOn: string[];
}

export interface FeatureRequest {
  schemaVersion: "1.0.0";
  requestId: string;
  campaignId: string;
  revision: number;
  previousRevisionHash: string | null;
  status: "draft" | "awaiting_approval" | "approved" | "superseded" | "rejected";
  profile: { id: string; version: string; digest: string };
  title: string;
  source: { kind: string; reference: string; url: string | null };
  owner: { kind: string; id: string };
  requestedBy: { kind: string; id: string };
  businessOutcome: string;
  nonGoals: string[];
  acceptanceCriteria: Array<{ id: string; statement: string; verification: string[] }>;
  risk: { level: string; signals: string[]; rationale: string };
  workItems: WorkItem[];
  contracts: unknown[];
  trafficEdges: unknown[];
  dependencyGraph: { nodes: string[]; edges: Array<{ from: string; to: string; kind: string; condition: string }> };
  requiredChecks: Array<{ id: string; workItem: string; kind: string; required: boolean; executor: string; deferTo: string | null }>;
  observability: { expectedSignals: string[]; traceQueries: string[]; logQueries: string[]; metricQueries: string[]; piiLogScan: boolean };
  security: { dataClasses: string[]; allowedEgress: string[]; credentialClasses: string[]; prohibitedLogging: string[]; requiredReviews: string[] };
  approvalPolicy: { required: string[]; expiryMinutes: number; invalidateOn: string[] };
  budgets: { maxCostUsd: number; maxElapsedMinutes: number; maxConcurrentBuilders: number; maxRepairCyclesPerRepository: number; maxStorageMiB: number };
  unresolved: unknown[];
  createdAt: string;
  updatedAt: string;
}

export interface AgentResult {
  schemaVersion: "1.0.0";
  resultId: string;
  campaignId: string;
  requestRevision: number;
  requestHash: string;
  profile: { id: string; version: string; digest: string };
  workItemId: string | null;
  role: AgentRole;
  workerRunId: string;
  piSessionId: string;
  status: "completed" | "changes_requested" | "blocked" | "failed" | "budget_exhausted" | "cancelled";
  inputs: unknown[];
  plan: unknown | null;
  decisions: unknown[];
  unresolved: unknown[];
  changedFiles: Array<{ path: string; change: string; purpose: string; generated: boolean; digest: string }>;
  contractChanges: unknown[];
  trafficChanges: unknown[];
  commands: unknown[];
  checks: Array<{ checkId: string; status: CheckStatus; required: boolean; attempt: number; failureClass: string | null; evidence: unknown[] }>;
  findings: Array<{ id: string; severity: string; category: string; summary: string; rationale: string; location: { path: string; line: number | null }; blocking: boolean; evidence: unknown[] }>;
  risks: unknown[];
  artifacts: unknown[];
  gitState: unknown | null;
  nextActions: unknown[];
  summary: string;
  startedAt: string;
  completedAt: string;
}

export interface Campaign {
  id: string;
  title: string;
  state: FactoryState;
  requestHash: string;
  profileId: string;
  profileVersion: string;
  profileDigest: string;
  createdAt: string;
  updatedAt: string;
  previousState: FactoryState | null;
  repairCycles: number;
  pausedReason: string | null;
}

export interface PeerSessionRef {
  sessionId: string;
  runId: string;
  role: string;
  workItemId: string | null;
  attempt: number;
  sessionFile: string | null;
}

export interface Capability {
  id: string;
  available: boolean;
  detail: string;
}
