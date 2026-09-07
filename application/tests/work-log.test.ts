import assert from "node:assert/strict";
import { test } from "node:test";
import { eventArgumentEntries, eventDuration, eventPreview, eventResult, eventSuccess, eventTarget, eventTitle, visibleWorkEvents } from "../src/client/work-log.ts";

const event = (payload: unknown) => ({
  sequence: 1,
  id: "event-1",
  task_id: "task-1",
  type: "tool_call",
  payload,
  started_at: "2026-01-01T00:00:00.000Z",
});

test("work-log arguments preserve structured input", () => {
  assert.deepEqual(eventArgumentEntries(event({ arguments: { path: "/tmp/file" } })), [["path", "/tmp/file"]]);
  assert.deepEqual(eventArgumentEntries(event({ args: '{"command":"go test"}' })), [["command", "go test"]]);
});

test("work-log arguments preserve primitive input", () => {
  assert.deepEqual(eventArgumentEntries(event({ arguments: "--version" })), [["arguments", "--version"]]);
  assert.deepEqual(eventArgumentEntries(event({ arguments: 42 })), [["arguments", 42]]);
  assert.deepEqual(eventArgumentEntries(event({ arguments: "" })), []);
});

test("work-log derives title, target, result, preview, duration, and status", () => {
  const tool = event({ tool: "apply_patch", arguments: { path: "/tmp/file" }, result: { changed: true }, duration_ms: 1_250, exit_code: 0 });
  assert.equal(eventTitle(tool), "Edit");
  assert.equal(eventTarget(tool), "/tmp/file");
  assert.equal(eventResult(tool), '{\n  "changed": true\n}');
  assert.equal(eventPreview(tool), '{');
  assert.equal(eventDuration(tool), "1.3s");
  assert.equal(eventSuccess(tool), true);
});

test("work-log excludes transient events and retains attempted events", () => {
  const transient = { ...event({}), id: "transient", type: "message_update" };
  const attempted = { ...event({}), id: "attempted", attempt_id: "attempt-a" };
  assert.deepEqual(visibleWorkEvents([transient, attempted], "attempt-a").map((value) => value.id), ["attempted"]);
});
