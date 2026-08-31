import type { FactoryState } from "./types.js";

export type CampaignAdvancementOutcome =
  | "builder_completed"
  | "review_passed"
  | "review_blocked"
  | "test_passed"
  | "test_failed"
  | "human_review_completed";

export interface CampaignTransitionPolicy {
  repairCycles: number;
  maxRepairCycles: number;
}

export type CampaignTransitionDecision =
  | { kind: "transition"; nextState: FactoryState; reason?: string }
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
  testing: ["repairing_test", "awaiting_human_review", "blocked"],
  repairing_test: ["re_reviewing_after_test", "blocked"],
  re_reviewing_after_test: ["repairing_test", "re_testing", "blocked"],
  re_testing: ["repairing_test", "awaiting_human_review", "blocked"],
  awaiting_human_review: ["implementation_complete"],
  aborting: ["aborted"],
};

const interruptible = new Set<FactoryState>([
  "received", "planning", "awaiting_plan_approval", "building", "reviewing",
  "repairing_review", "re_reviewing", "testing", "repairing_test",
  "re_reviewing_after_test", "re_testing", "awaiting_human_review",
]);

export function canTransition(from: FactoryState, to: FactoryState): boolean {
  if (interruptible.has(from) && (to === "paused" || to === "aborting" || to === "failed")) return true;
  if (from === "paused") return to !== "paused";
  if (from === "blocked") {
    return [
      "planning", "building", "reviewing", "repairing_review", "re_reviewing",
      "testing", "repairing_test", "re_reviewing_after_test", "re_testing",
    ].includes(to);
  }
  return normalTransitions[from]?.includes(to) ?? false;
}

export function assertTransition(from: FactoryState, to: FactoryState): void {
  if (!canTransition(from, to)) throw new Error(`illegal factory transition: ${from} -> ${to}`);
}

export function isTerminal(state: FactoryState): boolean {
  return ["implementation_complete", "failed", "aborted"].includes(state);
}

export function decideCampaignTransition(
  state: FactoryState,
  outcome: CampaignAdvancementOutcome,
  policy: CampaignTransitionPolicy,
): CampaignTransitionDecision {
  const nextState = nextStateFor(state, outcome);
  if (nextState === "repair" && policy.repairCycles >= policy.maxRepairCycles) {
    return { kind: "fail", nextState: "failed", reason: "repair budget exhausted" };
  }
  if (nextState === "repair") {
    return { kind: "transition", nextState: repairStateFor(state) };
  }
  return { kind: "transition", nextState };
}

type PolicyState = FactoryState | "repair";

function nextStateFor(
  state: FactoryState,
  outcome: CampaignAdvancementOutcome,
): PolicyState {
  const key = `${state}:${outcome}`;
  const fixed: Record<string, PolicyState> = {
    "building:builder_completed": "reviewing",
    "repairing_review:builder_completed": "re_reviewing",
    "repairing_test:builder_completed": "re_reviewing_after_test",
    "reviewing:review_passed": "testing",
    "re_reviewing:review_passed": "testing",
    "re_reviewing_after_test:review_passed": "re_testing",
    "reviewing:review_blocked": "repair",
    "re_reviewing:review_blocked": "repair",
    "re_reviewing_after_test:review_blocked": "repair",
    "testing:test_failed": "repair",
    "re_testing:test_failed": "repair",
    "testing:test_passed": "awaiting_human_review",
    "re_testing:test_passed": "awaiting_human_review",
    "awaiting_human_review:human_review_completed": "implementation_complete",
  };
  const fixedState = fixed[key];
  if (fixedState) return fixedState;
  throw new CampaignTransitionDecisionError(state, outcome);
}

function repairStateFor(state: FactoryState): FactoryState {
  if (state === "re_reviewing_after_test" || state === "testing" || state === "re_testing") return "repairing_test";
  return "repairing_review";
}
