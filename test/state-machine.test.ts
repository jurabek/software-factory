import { describe, expect, it } from "vitest";
import {
  CampaignTransitionDecisionError,
  canTransition,
  decideCampaignTransition,
  isTerminal,
  type CampaignAdvancementOutcome,
} from "../packages/core/src/state-machine.js";
import type { FactoryState } from "../packages/core/src/types.js";

const policy = { repairCycles: 0, maxRepairCycles: 2 };

describe("campaign transition policy", () => {
  it.each([
    ["building", "builder_completed", "reviewing"],
    ["repairing_review", "builder_completed", "re_reviewing"],
    ["repairing_test", "builder_completed", "re_reviewing_after_test"],
    ["reviewing", "review_passed", "testing"],
    ["re_reviewing", "review_passed", "testing"],
    ["re_reviewing_after_test", "review_passed", "re_testing"],
    ["reviewing", "review_blocked", "repairing_review"],
    ["re_reviewing", "review_blocked", "repairing_review"],
    ["re_reviewing_after_test", "review_blocked", "repairing_test"],
    ["testing", "test_failed", "repairing_test"],
    ["re_testing", "test_failed", "repairing_test"],
    ["testing", "test_passed", "awaiting_human_review"],
    ["re_testing", "test_passed", "awaiting_human_review"],
    ["awaiting_human_review", "human_review_completed", "implementation_complete"],
  ] satisfies Array<[FactoryState, CampaignAdvancementOutcome, FactoryState]>)(
    "maps %s + %s to %s",
    (state, outcome, nextState) => {
      expect(decideCampaignTransition(state, outcome, policy)).toEqual({ kind: "transition", nextState });
    },
  );

  it.each([
    ["reviewing", "review_blocked"],
    ["re_reviewing", "review_blocked"],
    ["testing", "test_failed"],
    ["re_testing", "test_failed"],
  ] satisfies Array<[FactoryState, CampaignAdvancementOutcome]>)(
    "fails %s + %s when repair budget is exhausted",
    (state, outcome) => {
      expect(decideCampaignTransition(state, outcome, { repairCycles: 2, maxRepairCycles: 2 }))
        .toEqual({ kind: "fail", nextState: "failed", reason: "repair budget exhausted" });
    },
  );

  it("rejects invalid outcomes", () => {
    expect(() => decideCampaignTransition("reviewing", "test_passed", policy))
      .toThrow(CampaignTransitionDecisionError);
  });

  it("supports local lifecycle controls", () => {
    expect(canTransition("building", "paused")).toBe(true);
    expect(canTransition("paused", "building")).toBe(true);
    expect(isTerminal("implementation_complete")).toBe(true);
    expect(isTerminal("testing")).toBe(false);
  });
});
