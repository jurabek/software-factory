import { describe, expect, it } from "vitest";
import {
  CampaignTransitionDecisionError,
  assertTransition,
  canTransition,
  decideCampaignTransition,
  type CampaignAdvancementOutcome,
  type CampaignTransitionDecision,
} from "../src/state-machine.js";
import type { FactoryState } from "../src/types.js";

const policy = {
  deliveryEnabled: true,
  repairCycles: 0,
  maxRepairCycles: 2,
};

const cases: Array<[FactoryState, CampaignAdvancementOutcome, CampaignTransitionDecision]> = [
  ["building", "builder_completed", { kind: "transition", nextState: "reviewing" }],
  ["repairing_review", "builder_completed", { kind: "transition", nextState: "re_reviewing" }],
  ["repairing_test", "builder_completed", { kind: "transition", nextState: "re_reviewing_after_test" }],
  ["repairing_ci", "builder_completed", { kind: "transition", nextState: "re_reviewing_after_ci" }],

  ["reviewing", "review_passed", { kind: "transition", nextState: "testing" }],
  ["re_reviewing", "review_passed", { kind: "transition", nextState: "testing" }],
  ["re_reviewing_after_test", "review_passed", { kind: "transition", nextState: "re_testing" }],
  ["re_reviewing_after_ci", "review_passed", { kind: "transition", nextState: "re_testing_after_ci" }],
  ["reviewing", "review_blocked", { kind: "transition", nextState: "repairing_review" }],
  ["re_reviewing", "review_blocked", { kind: "transition", nextState: "repairing_review" }],
  ["re_reviewing_after_test", "review_blocked", { kind: "transition", nextState: "repairing_test" }],
  ["re_reviewing_after_ci", "review_blocked", { kind: "transition", nextState: "repairing_ci" }],

  ["testing", "test_passed", { kind: "transition", nextState: "opening_prs" }],
  ["re_testing", "test_passed", { kind: "transition", nextState: "opening_prs" }],
  ["re_testing_after_ci", "test_passed", { kind: "transition", nextState: "opening_prs" }],
  ["testing", "test_failed", { kind: "transition", nextState: "repairing_test" }],
  ["re_testing", "test_failed", { kind: "transition", nextState: "repairing_test" }],
  ["re_testing_after_ci", "test_failed", { kind: "transition", nextState: "repairing_ci" }],

  ["opening_prs", "draft_pull_requests_opened", { kind: "transition", nextState: "validating_ci" }],
  [
    "opening_prs",
    "draft_pull_requests_missing",
    {
      kind: "transition",
      nextState: "blocked",
      reason: "one or more write-capable work items have no GitHub draft PR",
    },
  ],
  ["validating_ci", "ci_pending", { kind: "wait", reason: "ci_pending" }],
  ["validating_ci", "ci_passed", { kind: "transition", nextState: "awaiting_human_review" }],
  ["validating_ci", "ci_failed", { kind: "transition", nextState: "repairing_ci" }],
  [
    "awaiting_human_review",
    "human_review_completed",
    { kind: "transition", nextState: "implementation_complete" },
  ],
];

describe("Campaign transition policy", () => {
  it.each(cases)("decides %s + %s", (state, outcome, expected) => {
    const decision = decideCampaignTransition(state, outcome, policy);
    expect(decision).toEqual(expected);
    if (decision.kind === "transition") expect(canTransition(state, decision.nextState)).toBe(true);
  });

  it.each([
    ["reviewing", "review_blocked"],
    ["re_reviewing", "review_blocked"],
    ["re_reviewing_after_test", "review_blocked"],
    ["re_reviewing_after_ci", "review_blocked"],
    ["testing", "test_failed"],
    ["re_testing", "test_failed"],
    ["re_testing_after_ci", "test_failed"],
    ["validating_ci", "ci_failed"],
  ] satisfies Array<[FactoryState, CampaignAdvancementOutcome]>)(
    "fails %s + %s when the repair budget is exhausted",
    (state, outcome) => {
      expect(decideCampaignTransition(state, outcome, {
        ...policy,
        repairCycles: policy.maxRepairCycles,
      })).toEqual({ kind: "fail", nextState: "failed", reason: "repair budget exhausted" });
    },
  );

  it.each(["testing", "re_testing", "re_testing_after_ci"] satisfies FactoryState[])(
    "skips delivery after successful testing in %s when delivery is disabled",
    (state) => {
      expect(decideCampaignTransition(state, "test_passed", {
        ...policy,
        deliveryEnabled: false,
      })).toEqual({ kind: "transition", nextState: "awaiting_human_review" });
    },
  );

  it("rejects outcomes that are invalid for the current state", () => {
    expect(() => decideCampaignTransition("reviewing", "ci_failed", policy))
      .toThrow(CampaignTransitionDecisionError);
  });

  it("retains the legal-transition invariant guard", () => {
    expect(canTransition("planning", "testing")).toBe(false);
    expect(() => assertTransition("building", "testing")).toThrow("illegal factory transition");
  });
});
