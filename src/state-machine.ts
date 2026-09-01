import type { FactoryState } from "./types.js";

export type CampaignAdvancementOutcome =
  | "builder_completed"
  | "review_passed"
  | "review_blocked"
  | "test_passed"
  | "test_failed"
  | "draft_pull_requests_opened"
  | "draft_pull_requests_missing"
  | "ci_pending"
  | "ci_passed"
  | "ci_failed"
  | "human_review_completed";

export interface CampaignTransitionPolicy {
  deliveryEnabled: boolean;
  repairCycles: number;
  maxRepairCycles: number;
}

export type CampaignTransitionDecision =
  | { kind: "transition"; nextState: FactoryState; reason?: string }
  | { kind: "wait"; reason: "ci_pending" }
  | { kind: "fail"; nextState: "failed"; reason: "repair budget exhausted" };

export class CampaignTransitionDecisionError extends Error {
  constructor(state: FactoryState, outcome: CampaignAdvancementOutcome) {
    super(`invalid Campaign advancement outcome: ${outcome} in ${state}`);
  }
}

const normalTransitions: Partial<Record<FactoryState, readonly FactoryState[]>> = {
  received: ["planning"],
  planning: ["awaiting_plan_approval", "blocked"],
  awaiting_plan_approval: ["building"],
  building: ["reviewing", "blocked"],
  reviewing: ["repairing_review", "testing", "blocked"],
  repairing_review: ["re_reviewing", "blocked"],
  re_reviewing: ["repairing_review", "testing", "blocked"],
  testing: ["repairing_test", "opening_prs", "awaiting_human_review", "blocked"],
  repairing_test: ["re_reviewing_after_test", "blocked"],
  re_reviewing_after_test: ["repairing_test", "re_testing", "blocked"],
  re_testing: ["repairing_test", "opening_prs", "awaiting_human_review", "blocked"],
  opening_prs: ["validating_ci", "blocked"],
  validating_ci: ["repairing_ci", "awaiting_human_review", "blocked"],
  repairing_ci: ["re_reviewing_after_ci", "blocked"],
  re_reviewing_after_ci: ["repairing_ci", "re_testing_after_ci", "blocked"],
  re_testing_after_ci: ["repairing_ci", "opening_prs", "blocked"],
  awaiting_human_review: ["implementation_complete"],
  implementation_complete: ["awaiting_deploy_approval"],
  awaiting_deploy_approval: ["deploying"],
  deploying: ["verifying", "rolling_back"],
  verifying: ["soaking", "rolling_back"],
  soaking: ["shipped", "rolling_back"],
  rolling_back: ["rolled_back"],
  aborting: ["aborted"],
};

const interruptible = new Set<FactoryState>([
  "received", "planning", "awaiting_plan_approval", "building", "reviewing",
  "repairing_review", "re_reviewing", "testing", "repairing_test",
  "re_reviewing_after_test", "re_testing", "opening_prs", "validating_ci",
  "repairing_ci", "re_reviewing_after_ci", "re_testing_after_ci", "awaiting_human_review",
]);

export function canTransition(from: FactoryState, to: FactoryState): boolean {
  if (interruptible.has(from) && (to === "paused" || to === "aborting" || to === "failed")) return true;
  if (from === "paused") return to !== "paused" && to !== "shipped";
  return normalTransitions[from]?.includes(to) ?? false;
}

export function assertTransition(from: FactoryState, to: FactoryState): void {
  if (!canTransition(from, to)) throw new Error(`illegal factory transition: ${from} -> ${to}`);
}

export function isTerminal(state: FactoryState): boolean {
  return ["implementation_complete", "shipped", "failed", "aborted", "rolled_back"].includes(state);
}

export function decideCampaignTransition(
  state: FactoryState,
  outcome: CampaignAdvancementOutcome,
  policy: CampaignTransitionPolicy,
): CampaignTransitionDecision {
  const nextState = nextStateFor(state, outcome, policy.deliveryEnabled);
  if (nextState === "wait") return { kind: "wait", reason: "ci_pending" };
  if (nextState === "repair" && policy.repairCycles >= policy.maxRepairCycles) {
    return { kind: "fail", nextState: "failed", reason: "repair budget exhausted" };
  }
  if (nextState === "repair") {
    return { kind: "transition", nextState: repairStateFor(state) };
  }
  if (nextState === "blocked") {
    return {
      kind: "transition",
      nextState: "blocked",
      reason: "one or more write-capable work items have no GitHub draft PR",
    };
  }
  return { kind: "transition", nextState };
}

type PolicyState = FactoryState | "repair" | "wait";

function nextStateFor(
  state: FactoryState,
  outcome: CampaignAdvancementOutcome,
  deliveryEnabled: boolean,
): PolicyState {
  const key = `${state}:${outcome}`;
  const fixed: Record<string, PolicyState> = {
    "building:builder_completed": "reviewing",
    "repairing_review:builder_completed": "re_reviewing",
    "repairing_test:builder_completed": "re_reviewing_after_test",
    "repairing_ci:builder_completed": "re_reviewing_after_ci",
    "reviewing:review_passed": "testing",
    "re_reviewing:review_passed": "testing",
    "re_reviewing_after_test:review_passed": "re_testing",
    "re_reviewing_after_ci:review_passed": "re_testing_after_ci",
    "reviewing:review_blocked": "repair",
    "re_reviewing:review_blocked": "repair",
    "re_reviewing_after_test:review_blocked": "repair",
    "re_reviewing_after_ci:review_blocked": "repair",
    "testing:test_failed": "repair",
    "re_testing:test_failed": "repair",
    "re_testing_after_ci:test_failed": "repair",
    "opening_prs:draft_pull_requests_opened": "validating_ci",
    "opening_prs:draft_pull_requests_missing": "blocked",
    "validating_ci:ci_pending": "wait",
    "validating_ci:ci_passed": "awaiting_human_review",
    "validating_ci:ci_failed": "repair",
    "awaiting_human_review:human_review_completed": "implementation_complete",
  };
  const fixedState = fixed[key];
  if (fixedState) return fixedState;
  if (outcome === "test_passed" && ["testing", "re_testing", "re_testing_after_ci"].includes(state)) {
    return deliveryEnabled ? "opening_prs" : "awaiting_human_review";
  }
  throw new CampaignTransitionDecisionError(state, outcome);
}

function repairStateFor(state: FactoryState): FactoryState {
  if (state === "re_reviewing_after_test" || state === "testing" || state === "re_testing") return "repairing_test";
  if (state === "re_reviewing_after_ci" || state === "re_testing_after_ci" || state === "validating_ci") return "repairing_ci";
  return "repairing_review";
}
