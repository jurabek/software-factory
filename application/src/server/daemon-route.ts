import { NextResponse } from "next/server";
import { DaemonRequestError } from "./daemon-client.ts";
import { DaemonRegistryError } from "./daemon-registry.ts";

export function privateJSON(body: unknown, status = 200) {
  return NextResponse.json(body, { status, headers: { "Cache-Control": "private, no-store" } });
}

export function daemonErrorResponse(error: unknown) {
  if (error instanceof DaemonRegistryError || error instanceof DaemonRequestError) {
    return privateJSON({ error: error.code, message: error.message }, error.status);
  }
  return privateJSON({ error: "daemon_operation_failed", message: "Daemon operation failed." }, 500);
}

const streamRevocationCheckMilliseconds = 5_000;

export function daemonStreamHeaders(): Record<string, string> {
  return {
    "Content-Type": "text/event-stream",
    "Cache-Control": "private, no-store, no-transform",
    "X-Accel-Buffering": "no",
    Connection: "keep-alive",
  };
}

// Proxy an upstream daemon SSE body without buffering or renumbering frames.
// Closes promptly when the browser disconnects or the application session is revoked.
export function proxyDaemonStream(
  upstream: Response,
  request: Request,
  isSessionAlive: () => Promise<boolean>,
  revokeIntervalMilliseconds = streamRevocationCheckMilliseconds,
): Response {
  const body = upstream.body;
  if (!body) {
    return privateJSON({ error: "daemon_unavailable", message: "Daemon is unavailable." }, 502);
  }
  const reader = body.getReader();
  let closed = false;
  let timer: ReturnType<typeof setInterval> | undefined;

  async function cancelUpstream(reason?: unknown) {
    if (closed) return;
    closed = true;
    if (timer) clearInterval(timer);
    try {
      await reader.cancel(reason);
    } catch {
      // Ignore cancellation races; downstream is already closing.
    }
  }

  if (request.signal.aborted) void cancelUpstream(request.signal.reason);
  request.signal.addEventListener("abort", () => void cancelUpstream(request.signal.reason), { once: true });

  timer = setInterval(() => {
    void isSessionAlive().then((alive) => {
      if (!alive) void cancelUpstream(new Error("session revoked"));
    });
  }, revokeIntervalMilliseconds);
  if (timer.unref) timer.unref();

  const stream = new ReadableStream<Uint8Array>({
    async start(controller) {
      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          if (value) controller.enqueue(value);
        }
        controller.close();
      } catch (error) {
        try {
          controller.error(error);
        } catch {
          // Downstream already closed.
        }
      } finally {
        closed = true;
        if (timer) clearInterval(timer);
        try {
          reader.releaseLock();
        } catch {
          // Ignore lock release races.
        }
      }
    },
    async cancel(reason) {
      await cancelUpstream(reason);
    },
  });

  return new Response(stream, { status: 200, headers: daemonStreamHeaders() });
}
