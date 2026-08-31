import { randomUUID, timingSafeEqual } from "node:crypto";
import { createServer, type ServerResponse } from "node:http";
import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, extname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  CampaignNotFoundError,
  CampaignReadModel,
  InvalidCampaignQueryError,
} from "@software-factory/core/read";

const safeId = /^SF-[0-9]{4}-[0-9]{4,}$/;
const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const traceTypes = new Set([
  "phase_start", "phase_end", "agent_start", "agent_end", "agent_result",
  "session_attached", "turn_start", "turn_end", "model_request",
  "model_response", "model_selected", "model_fallback", "thinking_level",
  "subagent_start", "subagent_end", "tool_start", "tool_end", "log", "error",
]);
const mime: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
};

export interface VisualizerControl {
  actor: string;
  approvePlan(campaignId: string, actor: string): unknown | Promise<unknown>;
}

export interface VisualizerOptions {
  workspace: string;
  host?: string;
  port?: number;
  staticRoot?: string;
  readModel?: CampaignReadModel;
  /** Deliberately opt-in local plan approval; absent by default. */
  control?: VisualizerControl;
}

export function startVisualizer(options: VisualizerOptions) {
  const host = options.host ?? "127.0.0.1";
  const port = options.port ?? 4173;
  const control = options.control && { ...options.control, token: randomUUID() };
  const readModel = options.readModel ?? new CampaignReadModel(options.workspace);
  if (!["127.0.0.1", "::1", "localhost"].includes(host)) {
    throw new Error("local visualizer may only bind to loopback");
  }
  const staticRoot = options.staticRoot ?? resolve(packageRoot, "dist", "web");
  const server = createServer(async (request, response) => {
    const url = new URL(request.url ?? "/", `http://${host}:${port}`);
    response.setHeader("Cache-Control", "no-store");
    response.setHeader("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'");
    response.setHeader("X-Content-Type-Options", "nosniff");
    try {
      if (request.method === "GET" && url.pathname === "/api/control") {
        return json(response, 200, control
          ? { enabled: true, actor: control.actor, token: control.token }
          : { enabled: false });
      }
      if (request.method === "POST" && url.pathname.startsWith("/api/campaigns/") && url.pathname.endsWith("/approve-plan")) {
        return await approvePlan(url, request.headers["x-software-factory-control"], control, response);
      }
      if (request.method !== "GET") return json(response, 405, { error: "read-only API" });
      if (url.pathname === "/api/health") {
        return json(response, 200, {
          status: "ok",
          mode: control ? "local-plan-control" : "read-only",
          live: "sqlite-wal-polling",
          visualizerVersion: 3,
          ui: existsSync(resolve(staticRoot, "index.html")),
        });
      }
      if (url.pathname === "/api/campaigns") return campaigns(readModel, url, response);
      if (url.pathname.startsWith("/api/campaigns/")) return campaignRoute(readModel, url, response);
      return staticFile(staticRoot, url.pathname, response);
    } catch (error) {
      if (error instanceof CampaignNotFoundError || error instanceof RouteNotFound) {
        return json(response, 404, { error: error.message || "route not found" });
      }
      if (error instanceof InvalidCampaignQueryError) return json(response, 400, { error: error.message });
      return json(response, 500, { error: error instanceof Error ? error.message : "unknown error" });
    }
  });
  server.listen(port, host);
  return server;
}

function campaigns(readModel: CampaignReadModel, url: URL, response: ServerResponse): void {
  json(response, 200, readModel.list({
    limit: numberParameter(url.searchParams.get("limit"), 100),
    ...(url.searchParams.get("profile") ? { profile: url.searchParams.get("profile")! } : {}),
    ...(url.searchParams.get("status") ? { status: url.searchParams.get("status")! } : {}),
  }));
}

function campaignRoute(readModel: CampaignReadModel, url: URL, response: ServerResponse): void {
  const parts = url.pathname.split("/").filter(Boolean);
  const id = parts[2];
  if (!id || !safeId.test(id)) return json(response, 400, { error: "invalid campaign id" });
  const resource = parts[3];
  let output: unknown;
  if (!resource) {
    output = readModel.detail(id);
  } else if (resource === "events" || resource === "sessions") {
    const runId = resource === "sessions" ? parts[4] : undefined;
    output = {
      ...readModel.events(id, {
        after: numberParameter(url.searchParams.get("after"), 0),
        limit: numberParameter(url.searchParams.get("limit"), 100),
        types: requestedTraceTypes(url),
        ...(url.searchParams.get("role") ? { role: url.searchParams.get("role")! } : {}),
        ...(runId || url.searchParams.get("runId") ? { runId: runId ?? url.searchParams.get("runId")! } : {}),
      }),
      ...(resource === "sessions" ? { source: "sqlite-wal" } : {}),
    };
  } else if (resource === "session-logs") {
    output = readModel.sessionLogs(id, {
      after: numberParameter(url.searchParams.get("after"), 0),
      limit: numberParameter(url.searchParams.get("limit"), 100),
      ...(url.searchParams.get("sessionId") ? { sessionId: url.searchParams.get("sessionId")! } : {}),
    });
  } else if (resource === "results") {
    output = readModel.results(id, url.searchParams.get("role") ?? undefined);
  } else if (resource === "agents" && parts[4] && parts[5] === "prompts") {
    output = { available: false, reason: "Prompt bodies are hidden by local security policy" };
  } else if (resource === "checks" || resource === "findings" || resource === "dependencies" || resource === "phases" || resource === "agents") {
    output = readModel.rows(id, resource);
  } else {
    throw new RouteNotFound();
  }
  json(response, 200, output);
}

function requestedTraceTypes(url: URL): string[] {
  return (url.searchParams.get("types") ?? "")
    .split(",")
    .map((type) => type.trim())
    .filter((type) => traceTypes.has(type));
}

async function approvePlan(
  url: URL,
  suppliedToken: string | string[] | undefined,
  control: (VisualizerControl & { token: string }) | undefined,
  response: ServerResponse,
): Promise<void> {
  if (!control) return json(response, 404, { error: "local control is not enabled" });
  const id = url.pathname.split("/").filter(Boolean)[2];
  if (!id || !safeId.test(id)) return json(response, 400, { error: "invalid campaign id" });
  const token = Array.isArray(suppliedToken) ? suppliedToken[0] : suppliedToken;
  if (!token || !safeTokenEqual(token, control.token)) return json(response, 403, { error: "local control authorization required" });
  const campaign = await control.approvePlan(id, control.actor);
  json(response, 200, { campaign });
}

function safeTokenEqual(left: string, right: string): boolean {
  const leftBuffer = Buffer.from(left);
  const rightBuffer = Buffer.from(right);
  return leftBuffer.length === rightBuffer.length && timingSafeEqual(leftBuffer, rightBuffer);
}

class RouteNotFound extends Error {}

function numberParameter(raw: string | null, fallback: number): number {
  const parsed = Number(raw ?? fallback);
  return Number.isSafeInteger(parsed) ? parsed : fallback;
}

function staticFile(root: string, pathname: string, response: ServerResponse): void {
  const relative = pathname === "/" ? "index.html" : pathname.replace(/^\/+/, "");
  const path = resolve(root, relative);
  if (!path.startsWith(`${resolve(root)}/`) || !existsSync(path) || !statSync(path).isFile()) {
    const fallback = resolve(root, "index.html");
    if (!existsSync(fallback)) {
      response.writeHead(200, { "Content-Type": mime[".html"] });
      response.end(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Software Factory</title></head><body><p>Visualizer UI assets are not built yet. API is live at <a href="/api/health">/api/health</a>.</p></body></html>`);
      return;
    }
    response.writeHead(200, { "Content-Type": mime[".html"] });
    response.end(readFileSync(fallback));
    return;
  }
  response.writeHead(200, { "Content-Type": mime[extname(path)] ?? "application/octet-stream" });
  response.end(readFileSync(path));
}

function json(response: ServerResponse, status: number, body: unknown): void {
  response.writeHead(status, { "Content-Type": "application/json; charset=utf-8" });
  response.end(JSON.stringify(body));
}
