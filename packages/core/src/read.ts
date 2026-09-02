import { existsSync, readdirSync } from "node:fs";
import { resolve } from "node:path";
import { CampaignStore } from "./store.js";
import type { AgentResult, Campaign, FeatureRequest, PeerSessionRef } from "./types.js";

type Row = Record<string, unknown>;

export class CampaignNotFoundError extends Error {}
export class InvalidCampaignQueryError extends Error {}

export class CampaignReadModel {
  constructor(private readonly workspace: string) {}

  list(filter: { limit?: number; profile?: string; status?: string } = {}): Campaign[] {
    if (!existsSync(this.workspace)) return [];
    return readdirSync(this.workspace)
      .filter((id) => isCampaignId(id) && existsSync(resolve(this.workspace, id, "campaign.db")))
      .map((id) => this.withStore(id, (store) => store.campaign()))
      .filter((campaign) => !filter.profile || campaign.profileId === filter.profile)
      .filter((campaign) => !filter.status || campaign.state === filter.status)
      .sort((left, right) => right.updatedAt.localeCompare(left.updatedAt))
      .slice(0, bounded(filter.limit));
  }

  detail(id: string): { campaign: Campaign; request: FeatureRequest } {
    return this.withStore(id, (store) => ({ campaign: store.campaign(), request: store.request() }));
  }

  rows(id: string, resource: "checks" | "findings" | "dependencies" | "phases" | "agents"): Row[] {
    return this.withStore(id, (store) =>
      store.rows(resource === "agents" ? "agent_runs" : resource).map(normalizeRow),
    );
  }

  results(id: string, role?: string): AgentResult[] {
    return this.withStore(id, (store) => store.results(role));
  }

  events(
    id: string,
    query: { after?: number; limit?: number; types?: string[]; role?: string; runId?: string } = {},
  ): { events: Row[]; cursor: number; hasMore: boolean } {
    const after = cursor(query.after);
    const limit = bounded(query.limit);
    if (query.role && !isSafeSegment(query.role)) throw new InvalidCampaignQueryError("invalid role");
    if (query.runId && !isSafeSegment(query.runId)) throw new InvalidCampaignQueryError("invalid run id");
    const rows = this.withStore(id, (store) => store.events(after, limit, {
      ...(query.types?.length ? { types: query.types } : {}),
      ...(query.role ? { role: query.role } : {}),
      ...(query.runId ? { runId: query.runId } : {}),
    }));
    return page(rows, after, limit, "events");
  }

  sessionLogs(
    id: string,
    query: { after?: number; limit?: number; sessionId?: string } = {},
  ): { source: "sqlite-wal"; catalog: PeerSessionRef[]; logs: Row[]; cursor: number; hasMore: boolean } {
    const after = cursor(query.after);
    const limit = bounded(query.limit);
    if (query.sessionId && !/^[A-Za-z0-9._:-]+$/.test(query.sessionId)) {
      throw new InvalidCampaignQueryError("invalid session id");
    }
    return this.withStore(id, (store) => {
      const rows = store.sessionLogs(query.sessionId, after, limit).map(normalizeRow);
      return {
        source: "sqlite-wal" as const,
        catalog: store.sessionCatalog(),
        logs: rows,
        cursor: Number(rows.at(-1)?.id ?? after),
        hasMore: rows.length === limit,
      };
    });
  }

  private withStore<T>(id: string, action: (store: CampaignStore) => T): T {
    if (!isCampaignId(id)) throw new InvalidCampaignQueryError("invalid campaign id");
    if (!existsSync(resolve(this.workspace, id, "campaign.db"))) {
      throw new CampaignNotFoundError(`campaign ${id} not found`);
    }
    const store = new CampaignStore(this.workspace, id, { readonly: true });
    try {
      return action(store);
    } finally {
      store.close();
    }
  }
}

function isCampaignId(value: string): boolean {
  return /^SF-[0-9]{4}-[0-9]{4,}$/.test(value);
}

function isSafeSegment(value: string): boolean {
  return /^[A-Za-z0-9._-]+$/.test(value);
}

function bounded(value: number | undefined): number {
  return Number.isSafeInteger(value) ? Math.min(Math.max(value ?? 100, 1), 500) : 100;
}

function cursor(value: number | undefined): number {
  return Number.isSafeInteger(value) && (value ?? 0) >= 0 ? value ?? 0 : 0;
}

function normalizeRow(row: Row): Row {
  const normalized = { ...row };
  for (const key of ["payload", "body", "entry"]) {
    if (typeof normalized[key] === "string") normalized[key] = JSON.parse(normalized[key]);
  }
  return normalized;
}

function page(rows: Row[], after: number, limit: number, key: "events") {
  return {
    [key]: rows.map(normalizeRow),
    cursor: Number(rows.at(-1)?.id ?? after),
    hasMore: rows.length === limit,
  } as { events: Row[]; cursor: number; hasMore: boolean };
}
