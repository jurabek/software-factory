import assert from "node:assert/strict";
import { test } from "node:test";
import {
  maxEventSequence,
  mergeLiveEvents,
  qualifiedEventKey,
  qualifiedTaskKey,
  RequestScope,
} from "../src/client/daemon-ui-state.ts";

test("task and event keys are qualified by daemon registration", () => {
  assert.equal(qualifiedTaskKey({ daemonId: "daemon-a", taskId: "overlap" }), "daemon-a:overlap");
  assert.notEqual(
    qualifiedEventKey("daemon-a", "overlap", 7),
    qualifiedEventKey("daemon-b", "overlap", 7),
  );
});

test("request scopes suppress stale selection responses", () => {
  const scope = new RequestScope();
  const first = scope.next();
  const second = scope.next();
  assert.equal(scope.isCurrent(first), false);
  assert.equal(scope.isCurrent(second), true);
  scope.invalidate();
  assert.equal(scope.isCurrent(second), false);
});

test("event merges drop duplicates, accept gaps, and cap memory", () => {
  const current = [
    { sequence: 41, id: "e41", type: "log" },
    { sequence: 43, id: "e43", type: "log" },
  ];
  const merged = mergeLiveEvents(current, [
    { sequence: 43, id: "e43", type: "log" },
    { sequence: 47, id: "e47", type: "log" },
  ]);
  assert.deepEqual(merged.map((event) => event.sequence), [41, 43, 47]);
  const capped = mergeLiveEvents(
    Array.from({ length: 1000 }, (_, index) => ({ sequence: index, id: `e${index}`, type: "log" })),
    [{ sequence: 1000, id: "e1000", type: "log" }],
  );
  assert.equal(capped.length, 1000);
  assert.equal(capped[0].sequence, 1);
});

test("cursor advancement tolerates empty tails", () => {
  assert.equal(maxEventSequence([]), undefined);
  assert.equal(maxEventSequence([{ sequence: 9, id: "e9", type: "log" }]), 9);
});
