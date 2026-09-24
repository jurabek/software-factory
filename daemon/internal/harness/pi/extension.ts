import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

type FactoryRequest = {
  requestId: string;
  attempt?: number;
  prompt: string;
};

function decodeRequest(args: string): FactoryRequest {
  const text = Buffer.from(args.trim(), "base64url").toString("utf8");
  return JSON.parse(text) as FactoryRequest;
}

// The factory extension correlates each factory request with the native
// session subtree it produces. It appends a factory-request custom entry
// immediately before sending the user message, so the daemon can resolve the
// exact assistant response by requestId. The index is rebuilt on session_start.
export default function (pi: ExtensionAPI) {
  const attempts = new Map<string, number>();

  pi.on("session_start", async (_event, ctx) => {
    attempts.clear();
    for (const entry of ctx.sessionManager.getEntries()) {
      if (entry.type === "custom" && entry.customType === "factory-request") {
        const data = entry.data as { requestId?: string } | undefined;
        if (data?.requestId) {
          attempts.set(data.requestId, (attempts.get(data.requestId) ?? 0) + 1);
        }
      }
    }
  });

  pi.registerCommand("factory-run", {
    description: "Run an agent turn dispatched by the software factory",
    handler: async (args, ctx) => {
      const request = decodeRequest(args);
      const nextAttempt = (attempts.get(request.requestId) ?? 0) + 1;
      attempts.set(request.requestId, nextAttempt);
      pi.appendEntry("factory-request", {
        requestId: request.requestId,
        attempt: request.attempt ?? nextAttempt,
      });
      await pi.sendUserMessage(request.prompt);
    },
  });
}
