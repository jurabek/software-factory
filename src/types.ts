export const factoryStates = [
  "received", "planning", "awaiting_plan_approval", "building", "reviewing",
  "repairing_review", "re_reviewing", "testing", "repairing_test",
  "re_reviewing_after_test", "re_testing", "opening_prs", "validating_ci",
  "repairing_ci", "re_reviewing_after_ci", "re_testing_after_ci",
  "awaiting_human_review", "implementation_complete",
  "awaiting_deploy_approval", "deploying", "verifying", "soaking", "shipped",
  "blocked", "paused", "failed", "aborting", "aborted",
  "rolling_back", "rolled_back",
] as const;

export type FactoryState = (typeof factoryStates)[number];
export type AgentRole = "planner" | "builder" | "reviewer" | "tester";
export type CheckStatus = "passed" | "failed" | "deferred" | "waived";
export type CiStatus = "pending" | "passed" | "failed";
export type ThinkingLevel = "off" | "minimal" | "low" | "medium" | "high" | "xhigh" | "max";

export interface PromptEngineering {
  system?: string;
  user?: string;
}

export interface AgentSpec {
  name: AgentRole;
  codingAgent?: string;
  model?: string;
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
  thinking: ThinkingLevel;
  tools: string[];
  promptEngineering: { system: string; user: string };
  color?: string;
  purpose?: string;
}

export interface FactoryConfig {
  source: string;
  defaults: {
    codingAgent: string;
    model: string;
    thinking: ThinkingLevel;
    profile: string;
    repositories: string[];
    tools: string[];
  };
  observability: { pollMs: number };
  agents: AgentSpec[];
  profile?: DomainProfile;
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
  checkIds: string[];
}

export interface DomainProfile {
  schemaVersion: string;
  id: string;
  version: string;
  name: string;
  repositories: ProfileRepository[];
  riskDefaults?: { highRiskSignals: string[]; prohibitedEvidenceData: string[] };
  requiredReviewKinds?: string[];
  [key: string]: unknown;
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
  environments: unknown[];
  rollout: { strategy: string; order: string[]; stopConditions: string[] };
  observability: { expectedSignals: string[]; traceQueries: string[]; logQueries: string[]; metricQueries: string[]; piiLogScan: boolean };
  security: { dataClasses: string[]; allowedEgress: string[]; credentialClasses: string[]; prohibitedLogging: string[]; requiredReviews: string[] };
  approvalPolicy: { required: string[]; expiryMinutes: number; invalidateOn: string[] };
  rollback: { classification: string; steps: string[]; irreversiblePolicy: string; approval: string };
  budgets: { maxCostUsd: number; maxElapsedMinutes: number; maxConcurrentBuilders: number; maxRepairCyclesPerRepository: number; maxStorageMiB: number };
  waivers: unknown[];
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
  checks: Array<{ checkId: string; status: CheckStatus; required: boolean; attempt: number; failureClass: string | null; evidence: unknown[]; waiverId: string | null }>;
  findings: Array<{ id: string; severity: string; category: string; summary: string; rationale: string; location: { path: string; line: number | null }; blocking: boolean; evidence: unknown[] }>;
  risks: unknown[];
  artifacts: unknown[];
  gitState: unknown | null;
  nextActions: unknown[];
  summary: string;
  startedAt: string;
  completedAt: string;
}

export interface DeliveryCheck {
  name: string;
  state: string;
  bucket: string;
  link: string | null;
}

export interface DeliveryRecord {
  workItemId: string;
  repositoryId: string;
  repositoryUrl: string;
  baseBranch: string;
  branch: string;
  headSha: string;
  pullRequestNumber: number;
  pullRequestUrl: string;
  draft: boolean;
  ciStatus: CiStatus;
  checks: DeliveryCheck[];
  updatedAt: string;
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
