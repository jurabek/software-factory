import { randomUUID, timingSafeEqual } from "node:crypto";
import { createServer, type ServerResponse } from "node:http";
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { extname, resolve } from "node:path";
import { CampaignStore } from "./store.js";

const safeId = /^SF-[0-9]{4}-[0-9]{4,}$/;
const safeSegment = /^[A-Za-z0-9._-]+$/;
const traceTypes = new Set([
  "phase_start", "phase_end", "agent_start", "agent_end", "agent_result",
  "session_attached", "turn_start", "turn_end", "model_request",
  "model_response", "model_selected", "thinking_level", "tool_start",
  "tool_end", "log", "error",
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
  /** Deliberately opt-in local control plane; absent for background visualizers. */
  control?: VisualizerControl;
}

export function startVisualizer(options: VisualizerOptions) {
  const host = options.host ?? "127.0.0.1";
  const port = options.port ?? 4173;
  const control = options.control && { ...options.control, token: randomUUID() };
  if (!["127.0.0.1", "::1", "localhost"].includes(host)) {
    throw new Error("local visualizer may only bind to loopback");
  }
  const staticRoot = options.staticRoot ?? resolve(process.cwd(), "apps/visualizer/dist");
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
      if (url.pathname === "/api/campaigns") return campaigns(options.workspace, url, response);
      if (url.pathname.startsWith("/api/campaigns/")) return campaignRoute(options.workspace, url, response);
      return staticFile(staticRoot, url.pathname, response);
    } catch (error) {
      if (error instanceof RouteNotFound) return json(response, 404, { error: "route not found" });
      return json(response, 500, { error: error instanceof Error ? error.message : "unknown error" });
    }
  });
  server.listen(port, host);
  return server;
}

function campaigns(workspace: string, url: URL, response: ServerResponse): void {
  const limit = bounded(url.searchParams.get("limit"));
  if (!existsSync(workspace)) return json(response, 200, []);
  const rows = readdirSync(workspace)
    .filter((name) => safeId.test(name) && existsSync(resolve(workspace, name, "campaign.db")))
    .map((id) => withStore(workspace, id, (store) => store.campaign()))
    .filter((campaign) => !url.searchParams.get("profile") || campaign.profileId === url.searchParams.get("profile"))
    .filter((campaign) => !url.searchParams.get("status") || campaign.state === url.searchParams.get("status"))
    .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
    .slice(0, limit);
  json(response, 200, rows);
}

function campaignRoute(workspace: string, url: URL, response: ServerResponse): void {
  const parts = url.pathname.split("/").filter(Boolean);
  const id = parts[2];
  if (!id || !safeId.test(id)) return json(response, 400, { error: "invalid campaign id" });
  if (!existsSync(resolve(workspace, id, "campaign.db"))) return json(response, 404, { error: "campaign not found" });
  const resource = parts[3];
  const output = withStore(workspace, id, (store) => {
    if (!resource) return { campaign: store.campaign(), request: store.request() };
    if (resource === "events") {
      return eventPage(store, url);
    }
    if (resource === "sessions") {
      const runId = parts[4];
      if (runId && !safeSegment.test(runId)) throw new RouteNotFound();
      return {
        ...eventPage(store, url, runId),
        source: "sqlite-wal",
      };
    }
    if (resource === "session-logs") {
      const after = Number(url.searchParams.get("after") ?? 0);
      const limit = bounded(url.searchParams.get("limit"));
      const sessionId = url.searchParams.get("sessionId")?.trim();
      if (sessionId && !/^[A-Za-z0-9._:-]+$/.test(sessionId)) throw new RouteNotFound();
      const rows = store.sessionLogs(
        sessionId || undefined,
        Number.isSafeInteger(after) && after >= 0 ? after : 0,
        limit,
      );
      return {
        source: "sqlite-wal",
        catalog: store.sessionCatalog(),
        logs: rows.map(normalizeRow),
        cursor: Number(rows.at(-1)?.id ?? after),
        hasMore: rows.length === limit,
      };
    }
    if (resource === "results") return store.results(url.searchParams.get("role") ?? undefined);
    if (resource === "agents" && parts[4] && parts[5] === "prompts") {
      return { available: false, reason: "Prompt bodies are hidden by local security policy" };
    }
    if (resource === "checks" || resource === "findings" || resource === "dependencies" || resource === "phases" || resource === "agents") {
      return store.rows(resource === "agents" ? "agent_runs" : resource).map(normalizeRow);
    }
    if (resource === "delivery") {
      const deliveries = store.deliveries();
      return deliveries.length
        ? { status: deliveries.every((delivery) => delivery.ciStatus === "passed") ? "passed" : "in_progress", pullRequests: deliveries }
        : { status: "deferred", reason: "GitHub delivery is not enabled for this Campaign" };
    }
    throw new RouteNotFound();
  });
  json(response, 200, output);
}

function eventPage(store: CampaignStore, url: URL, routeRunId?: string): Record<string, unknown> {
  const after = Number(url.searchParams.get("after") ?? 0);
  const limit = bounded(url.searchParams.get("limit"));
  const requestedTypes = (url.searchParams.get("types") ?? "")
    .split(",")
    .map((type) => type.trim())
    .filter((type) => traceTypes.has(type));
  const role = url.searchParams.get("role")?.trim();
  const queryRunId = routeRunId ?? url.searchParams.get("runId")?.trim();
  const filters: { types?: string[]; role?: string; runId?: string } = {};
  if (requestedTypes.length) filters.types = requestedTypes;
  if (role && safeSegment.test(role)) filters.role = role;
  if (queryRunId && safeSegment.test(queryRunId)) filters.runId = queryRunId;
  const rows = store.events(
    Number.isSafeInteger(after) && after >= 0 ? after : 0,
    limit,
    filters,
  );
  const events = rows.map(normalizeRow);
  return {
    events,
    cursor: Number(rows.at(-1)?.id ?? after),
    hasMore: rows.length === limit,
  };
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

function withStore<T>(workspace: string, id: string, action: (store: CampaignStore) => T): T {
  const store = new CampaignStore(workspace, id, { readonly: true });
  try { return action(store); } finally { store.close(); }
}

function normalizeRow(row: Record<string, unknown>): Record<string, unknown> {
  const normalized = { ...row };
  for (const key of ["payload", "body"]) {
    if (typeof normalized[key] === "string") normalized[key] = JSON.parse(normalized[key]);
  }
  return normalized;
}

function bounded(raw: string | null): number {
  const parsed = Number(raw ?? 100);
  return Number.isSafeInteger(parsed) ? Math.min(Math.max(parsed, 1), 500) : 100;
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
