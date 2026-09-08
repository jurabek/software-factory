import assert from "node:assert/strict";
import { test } from "node:test";
import { eventArgumentEntries, eventDetailEntries, eventDuration, eventIcon, eventPreview, eventResult, eventResultLine, eventStartedAt, eventStatusLabel, eventSuccess, eventTarget, eventTitle, meaningfulWorkEvents, visibleWorkEvents } from "../src/client/work-log.ts";

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

test("work-log reports how many meaningful events the window hides", () => {
  const stream = [1, 2, 3].map((sequence) => ({ ...event({}), sequence, id: `event-${sequence}` }));
  stream.push({ ...event({}), sequence: 4, id: "transient", type: "message_start" });
  assert.equal(meaningfulWorkEvents(stream).length, 3);
  assert.deepEqual(visibleWorkEvents(stream, null, 2).map((value) => value.id), ["event-2", "event-3"]);
});

test("work-log marks failures, completions, and plain records", () => {
  const failure = { ...event({ status: "failed" }), type: "process_end" };
  const completion = { ...event({ exit_code: 0 }), type: "process_end" };
  const record = { ...event({}), type: "agent_start" };
  assert.equal(eventStatusLabel(failure), "failed");
  assert.equal(eventIcon(failure), "!");
  assert.equal(eventStatusLabel(completion), "completed");
  assert.equal(eventIcon(completion), "+");
  assert.equal(eventStatusLabel(record), "recorded");
  assert.equal(eventIcon(record), ">");
});

test("work-log details omit fields the input and result sections already show", () => {
  const detailed = event({ arguments: { path: "/tmp/file" }, result: "done", tool: "read", duration_ms: 12 });
  assert.deepEqual(eventDetailEntries(detailed).map(([key]) => key), ["tool", "duration_ms"]);
});

test("work-log result line annotates multi-line output", () => {
  assert.equal(eventResultLine(event({ result: "first\nsecond\nthird" })), "first ... (3 lines)");
  assert.equal(eventResultLine(event({ result: "only" })), "only");
  assert.equal(eventResultLine(event({})), "");
});

test("work-log prefers the payload start time over the envelope", () => {
  assert.equal(eventStartedAt(event({ started_at: "2026-02-02T00:00:00.000Z" })).toISOString(), "2026-02-02T00:00:00.000Z");
  assert.equal(eventStartedAt(event({})).toISOString(), "2026-01-01T00:00:00.000Z");
});

test("work-log skips empty output fields when deriving a result", () => {
  assert.equal(eventResult(event({ result: "  ", output: "real" })), "real");
});
