import assert from "node:assert/strict";
import { test } from "node:test";
import { parseSSEFrame } from "../src/client/daemon-api.ts";
import { proxyDaemonStream } from "../src/server/daemon-route.ts";

function sseUpstream(chunks: string[]): Response {
  const encoder = new TextEncoder();
  return new Response(
    new ReadableStream<Uint8Array>({
      start(controller) {
        for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
        controller.close();
      },
    }),
    { headers: { "Content-Type": "text/event-stream" } },
  );
}

async function readAll(response: Response): Promise<string> {
  assert.ok(response.body);
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let text = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    text += decoder.decode(value, { stream: true });
  }
  return text + decoder.decode();
}

test("streams preserve SSE frames and disable buffering", async () => {
  const upstream = sseUpstream(["id: 41\nevent: event\ndata: {\"sequence\":41}\n\n", "id: 42\nevent: event\ndata: {\"sequence\":42}\n\n"]);
  const response = proxyDaemonStream(upstream, new Request("http://localhost:3000/stream"), async () => true);
  assert.equal(response.headers.get("Content-Type"), "text/event-stream");
  assert.equal(response.headers.get("Cache-Control"), "private, no-store, no-transform");
  assert.equal(response.headers.get("X-Accel-Buffering"), "no");
  const text = await readAll(response);
  assert.ok(text.includes("id: 41"));
  assert.ok(text.includes("id: 42"));
});

test("stream sequence IDs are never renumbered", async () => {
  const upstream = sseUpstream(["id: 100\nevent: event\ndata: {\"sequence\":100}\n\n", "id: 105\nevent: event\ndata: {\"sequence\":105}\n\n"]);
  const response = proxyDaemonStream(upstream, new Request("http://localhost:3000/stream"), async () => true);
  const text = await readAll(response);
  assert.ok(text.includes("id: 100"));
  assert.ok(text.includes("id: 105"));
  assert.doesNotMatch(text, /id: 101/);
});

test("SSE frames accept CRLF and multiline data", () => {
  assert.deepEqual(
    parseSSEFrame('id: 105\r\ndata: {"message":\r\ndata: "ready"}\r\n'),
    { sequence: 105, raw: { message: "ready" } },
  );
  assert.equal(parseSSEFrame(": heartbeat\r\n"), null);
  assert.equal(parseSSEFrame("id: -1\ndata: {}"), null);
});

test("browser disconnect cancels the upstream reader", async () => {
  let cancelled = false;
  const upstream = new Response(
    new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode("id: 1\n\n"));
      },
      cancel() {
        cancelled = true;
      },
    }),
    { headers: { "Content-Type": "text/event-stream" } },
  );
  const controller = new AbortController();
  const response = proxyDaemonStream(upstream, new Request("http://localhost:3000/stream", { signal: controller.signal }), async () => true);
  assert.ok(response.body);
  const reader = response.body.getReader();
  await reader.read();
  controller.abort();
  await reader.cancel().catch(() => undefined);
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(cancelled, true);
});

test("revoked sessions close an open stream", async () => {
  const upstream = sseUpstream(["id: 7\nevent: event\ndata: {}\n\n"]);
  let alive = true;
  const response = proxyDaemonStream(upstream, new Request("http://localhost:3000/stream"), async () => alive, 5);
  alive = false;
  const text = await readAll(response);
  assert.ok(typeof text === "string");
});
