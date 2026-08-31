import type { FactoryState } from "./types.js";

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
  "re_reviewing_after_test", "re_testing", "awaiting_human_review",
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
