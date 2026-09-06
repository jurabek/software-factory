import assert from "node:assert/strict";
import { test } from "node:test";
import { eventArgumentEntries } from "../src/client/work-log.ts";

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
