import { createHash } from "node:crypto";
import { DatabaseSync } from "node:sqlite";
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { CampaignBus } from "./bus.js";
import type { AgentResult, Campaign, DeliveryRecord, FactoryState, FeatureRequest, PeerSessionRef } from "./types.js";

interface Row { [key: string]: unknown }

export class CampaignStore {
  readonly db: DatabaseSync;
  readonly campaignDir: string;
  readonly socketPath: string;
  private readonly eventFile: string;
  private readonly readonly: boolean;
  private bus: CampaignBus | undefined;

  constructor(readonly workspace: string, readonly campaignId: string, options: { readonly?: boolean } = {}) {
    if (!/^SF-[0-9]{4}-[0-9]{4,}$/.test(campaignId)) throw new Error("unsafe campaign id");
    this.readonly = Boolean(options.readonly);
    this.campaignDir = resolve(workspace, campaignId);
    this.eventFile = resolve(this.campaignDir, "events/events.jsonl");
    const socketTag = createHash("sha256").update(this.campaignDir).digest("hex").slice(0, 12);
    this.socketPath = resolve(tmpdir(), `sf-${this.campaignId}-${socketTag}.sock`);
    if (!this.readonly) {
      for (const path of ["events", "requests", "profiles", "results", "evidence", "artifacts", "sessions", "mirrors", "worktrees"]) {
        mkdirSync(resolve(this.campaignDir, path), { recursive: true });
      }
      writeFileSync(resolve(this.campaignDir, "factory.sock"), `${this.socketPath}\n`);
    }
    const databasePath = resolve(this.campaignDir, "campaign.db");
    this.db = new DatabaseSync(databasePath, this.readonly ? { readOnly: true } : {});
    this.db.exec("PRAGMA busy_timeout=5000;");
    if (!this.readonly) this.migrate();
  }

  async listenBus(): Promise<string> {
    if (this.readonly) throw new Error("read-only store cannot own the factory socket");
    if (!this.bus) {
      this.bus = new CampaignBus(this, this.socketPath);
      await this.bus.listen();
    }
    return this.socketPath;
  }

  close(): void {
    this.bus?.close();
    this.bus = undefined;
    this.db.close();
  }

  private migrate(): void {
    this.db.exec(`
      PRAGMA journal_mode=WAL;
      CREATE TABLE IF NOT EXISTS campaigns (
        id TEXT PRIMARY KEY, title TEXT NOT NULL, state TEXT NOT NULL,
        previous_state TEXT, request_hash TEXT NOT NULL, profile_id TEXT NOT NULL,
        profile_version TEXT NOT NULL, profile_digest TEXT NOT NULL,
        repair_cycles INTEGER NOT NULL DEFAULT 0, paused_reason TEXT,
        created_at TEXT NOT NULL, updated_at TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS requests (
        revision INTEGER PRIMARY KEY, hash TEXT NOT NULL UNIQUE,
        status TEXT NOT NULL, body TEXT NOT NULL, created_at TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS approvals (
        kind TEXT PRIMARY KEY, request_hash TEXT NOT NULL, profile_digest TEXT NOT NULL,
        actor TEXT NOT NULL, expires_at TEXT NOT NULL, created_at TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS phases (
        id TEXT PRIMARY KEY, kind TEXT NOT NULL, role TEXT, work_item_id TEXT,
        status TEXT NOT NULL, attempt INTEGER NOT NULL, started_at TEXT,
        completed_at TEXT, error TEXT
      );
      CREATE TABLE IF NOT EXISTS agent_runs (
        id TEXT PRIMARY KEY, role TEXT NOT NULL, work_item_id TEXT, session_id TEXT NOT NULL,
        status TEXT NOT NULL, model TEXT, started_at TEXT NOT NULL, completed_at TEXT
      );
      CREATE TABLE IF NOT EXISTS results (
        id TEXT PRIMARY KEY, role TEXT NOT NULL, work_item_id TEXT, status TEXT NOT NULL,
        request_hash TEXT NOT NULL, body TEXT NOT NULL, created_at TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS checks (
        check_id TEXT NOT NULL, work_item_id TEXT, status TEXT NOT NULL,
        required INTEGER NOT NULL, attempt INTEGER NOT NULL, body TEXT NOT NULL,
        PRIMARY KEY(check_id, attempt)
      );
      CREATE TABLE IF NOT EXISTS findings (
        id TEXT PRIMARY KEY, work_item_id TEXT, severity TEXT NOT NULL,
        category TEXT NOT NULL, blocking INTEGER NOT NULL, resolved INTEGER NOT NULL DEFAULT 0,
        body TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS dependencies (
        source TEXT NOT NULL, target TEXT NOT NULL, kind TEXT NOT NULL, condition TEXT NOT NULL,
        PRIMARY KEY(source, target, kind)
      );
      CREATE TABLE IF NOT EXISTS events (
        id INTEGER PRIMARY KEY AUTOINCREMENT, schema_version INTEGER NOT NULL DEFAULT 1,
        type TEXT NOT NULL, parent_id INTEGER, payload TEXT NOT NULL, created_at TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS operations (
        operation_key TEXT PRIMARY KEY, digest TEXT NOT NULL, result TEXT NOT NULL, created_at TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS deliveries (
        work_item_id TEXT PRIMARY KEY, repository_id TEXT NOT NULL, branch TEXT NOT NULL,
        head_sha TEXT NOT NULL, pull_request_url TEXT NOT NULL, ci_status TEXT NOT NULL,
        body TEXT NOT NULL, updated_at TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS pi_sessions (
        session_id TEXT PRIMARY KEY, run_id TEXT NOT NULL, role TEXT NOT NULL,
        work_item_id TEXT, attempt INTEGER NOT NULL, session_file TEXT, created_at TEXT NOT NULL
      );
      CREATE TABLE IF NOT EXISTS session_logs (
        id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, run_id TEXT NOT NULL,
        role TEXT NOT NULL, work_item_id TEXT, seq INTEGER NOT NULL, entry TEXT NOT NULL,
        created_at TEXT NOT NULL, UNIQUE(session_id, seq)
      );
      CREATE INDEX IF NOT EXISTS session_logs_session ON session_logs(session_id, id);
    `);
  }

  createCampaign(campaign: Campaign, request: FeatureRequest): void {
    this.db.exec("BEGIN IMMEDIATE");
    try {
      this.db.prepare(`INSERT INTO campaigns VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`).run(
        campaign.id, campaign.title, campaign.state, campaign.previousState, campaign.requestHash,
        campaign.profileId, campaign.profileVersion, campaign.profileDigest, campaign.repairCycles,
        campaign.pausedReason, campaign.createdAt, campaign.updatedAt,
      );
      this.saveRequest(request, campaign.requestHash);
      for (const edge of request.dependencyGraph.edges) {
        this.db.prepare(`INSERT OR IGNORE INTO dependencies VALUES (?, ?, ?, ?)`)
          .run(edge.from, edge.to, edge.kind, edge.condition);
      }
      this.db.exec("COMMIT");
    } catch (error) {
      this.db.exec("ROLLBACK");
      throw error;
    }
    this.event("campaign_created", { campaignId: campaign.id, title: campaign.title, state: campaign.state });
  }

  saveRequest(request: FeatureRequest, hash: string): void {
    const safeRequest = redact(request) as FeatureRequest;
    this.db.prepare(`INSERT OR REPLACE INTO requests VALUES (?, ?, ?, ?, ?)`).run(
      safeRequest.revision, hash, safeRequest.status, JSON.stringify(safeRequest), safeRequest.updatedAt,
    );
    writeFileSync(resolve(this.campaignDir, `requests/revision-${safeRequest.revision}.json`), JSON.stringify(safeRequest, null, 2));
  }

  bindRequest(request: FeatureRequest, hash: string): void {
    this.saveRequest(request, hash);
    this.db.prepare(`UPDATE campaigns SET request_hash=?, title=?, updated_at=?`)
      .run(hash, request.title, request.updatedAt);
    this.invalidateApprovals();
    this.db.exec(`DELETE FROM results; DELETE FROM checks; DELETE FROM findings; DELETE FROM pi_sessions; DELETE FROM session_logs;`);
    this.event("request_amended", { revision: request.revision, hash });
  }

  campaign(): Campaign {
    const row = this.db.prepare(`SELECT * FROM campaigns LIMIT 1`).get() as Row | undefined;
    if (!row) throw new Error(`campaign ${this.campaignId} does not exist`);
    return {
      id: String(row.id), title: String(row.title), state: String(row.state) as FactoryState,
      previousState: row.previous_state ? String(row.previous_state) as FactoryState : null,
      requestHash: String(row.request_hash), profileId: String(row.profile_id),
      profileVersion: String(row.profile_version), profileDigest: String(row.profile_digest),
      repairCycles: Number(row.repair_cycles), pausedReason: row.paused_reason ? String(row.paused_reason) : null,
      createdAt: String(row.created_at), updatedAt: String(row.updated_at),
    };
  }

  request(): FeatureRequest {
    const row = this.db.prepare(`SELECT body FROM requests ORDER BY revision DESC LIMIT 1`).get() as Row | undefined;
    if (!row) throw new Error("request not found");
    return JSON.parse(String(row.body)) as FeatureRequest;
  }

  setState(state: FactoryState, previousState: FactoryState | null = null, reason: string | null = null): void {
    const now = new Date().toISOString();
    this.db.prepare(`UPDATE campaigns SET state=?, previous_state=?, paused_reason=?, updated_at=?`)
      .run(state, previousState, reason, now);
    this.event("state_changed", { state, previousState, reason });
  }

  incrementRepair(): number {
    this.db.exec(`UPDATE campaigns SET repair_cycles=repair_cycles+1`);
    return this.campaign().repairCycles;
  }

  approve(kind: string, actor: string, expiryMinutes: number): void {
    const campaign = this.campaign();
    const now = new Date();
    const expires = new Date(now.getTime() + expiryMinutes * 60_000);
    this.db.prepare(`INSERT OR REPLACE INTO approvals VALUES (?, ?, ?, ?, ?, ?)`).run(
      kind, campaign.requestHash, campaign.profileDigest, actor, expires.toISOString(), now.toISOString(),
    );
    this.event("approval_recorded", { kind, actor, expiresAt: expires.toISOString() });
  }

  hasApproval(kind: string): boolean {
    const campaign = this.campaign();
    const row = this.db.prepare(`SELECT 1 ok FROM approvals WHERE kind=? AND request_hash=? AND profile_digest=? AND expires_at>?`)
      .get(kind, campaign.requestHash, campaign.profileDigest, new Date().toISOString()) as Row | undefined;
    return Boolean(row);
  }

  invalidateApprovals(): void {
    this.db.exec(`DELETE FROM approvals`);
    this.event("approvals_invalidated", {});
  }

  saveResult(result: AgentResult): void {
    const safeResult = redact(result) as AgentResult;
    this.db.prepare(`INSERT OR REPLACE INTO results VALUES (?, ?, ?, ?, ?, ?, ?)`).run(
      safeResult.resultId, safeResult.role, safeResult.workItemId, safeResult.status, safeResult.requestHash,
      JSON.stringify(safeResult), safeResult.completedAt,
    );
    writeFileSync(resolve(this.campaignDir, `results/${safeResult.resultId}.json`), JSON.stringify(safeResult, null, 2));
    for (const check of safeResult.checks) {
      this.db.prepare(`INSERT OR REPLACE INTO checks VALUES (?, ?, ?, ?, ?, ?)`).run(
        check.checkId, safeResult.workItemId, check.status, check.required ? 1 : 0,
        check.attempt, JSON.stringify(check),
      );
    }
    for (const finding of safeResult.findings) {
      this.db.prepare(`INSERT OR REPLACE INTO findings (id, work_item_id, severity, category, blocking, body) VALUES (?, ?, ?, ?, ?, ?)`).run(
        finding.id, safeResult.workItemId, finding.severity, finding.category, finding.blocking ? 1 : 0, JSON.stringify(finding),
      );
    }
    this.event("agent_result", {
      resultId: safeResult.resultId,
      runId: safeResult.workerRunId,
      role: safeResult.role,
      workItemId: safeResult.workItemId,
      status: safeResult.status,
      summary: safeResult.summary,
    });
  }

  startAgent(runId: string, role: string, workItemId: string | null, sessionId: string, attempt: number): number {
    const now = new Date().toISOString();
    this.db.prepare(`INSERT OR REPLACE INTO agent_runs VALUES (?, ?, ?, ?, ?, ?, ?, ?)`).run(
      runId, role, workItemId, sessionId, "running", null, now, null,
    );
    this.db.prepare(`INSERT OR REPLACE INTO phases VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`).run(
      runId, role, role, workItemId, "running", attempt, now, null, null,
    );
    const phaseId = this.event("phase_start", {
      runId, role, workItemId, attempt, phase: role,
    });
    return this.event("agent_start", {
      runId, role, workItemId, attempt, sessionId,
    }, phaseId);
  }

  finishAgent(runId: string, status: string, error: string | null = null, sessionId?: string): void {
    const now = new Date().toISOString();
    const run = this.db.prepare(`SELECT role, work_item_id FROM agent_runs WHERE id=?`).get(runId) as Row | undefined;
    const metadata = {
      runId,
      role: run?.role ? String(run.role) : null,
      workItemId: run?.work_item_id ? String(run.work_item_id) : null,
      status,
      error,
    };
    this.db.prepare(`UPDATE agent_runs SET status=?, completed_at=?, session_id=COALESCE(?, session_id) WHERE id=?`)
      .run(status, now, sessionId ?? null, runId);
    this.db.prepare(`UPDATE phases SET status=?, completed_at=?, error=? WHERE id=?`).run(
      status === "completed" ? "passed" : "failed", now, error, runId,
    );
    this.event("agent_end", metadata);
    this.event("phase_end", metadata);
  }

  resolveFindings(workItemId: string): void {
    this.db.prepare(`UPDATE findings SET resolved=1 WHERE work_item_id=? AND blocking=1`).run(workItemId);
    this.event("findings_resolved", { workItemId });
  }

  recordPiSession(session: {
    sessionId: string;
    runId: string;
    role: string;
    workItemId: string | null;
    attempt: number;
    sessionFile: string | null;
  }): void {
    this.db.prepare(`INSERT OR REPLACE INTO pi_sessions VALUES (?, ?, ?, ?, ?, ?, ?)`)
      .run(session.sessionId, session.runId, session.role, session.workItemId, session.attempt, session.sessionFile, new Date().toISOString());
  }

  sessionCatalog(): PeerSessionRef[] {
    const rows = this.db.prepare(
      `SELECT session_id, run_id, role, work_item_id, attempt, session_file FROM pi_sessions ORDER BY created_at`,
    ).all() as Row[];
    return rows.map((row) => ({
      sessionId: String(row.session_id),
      runId: String(row.run_id),
      role: String(row.role),
      workItemId: row.work_item_id ? String(row.work_item_id) : null,
      attempt: Number(row.attempt),
      sessionFile: row.session_file ? String(row.session_file) : null,
    }));
  }

  appendSessionLog(record: {
    sessionId: string;
    runId: string;
    role: string;
    workItemId: string | null;
    entry: unknown;
  }): number {
    const seq = this.sessionLogCount(record.sessionId);
    const now = new Date().toISOString();
    const result = this.db.prepare(
      `INSERT INTO session_logs (session_id, run_id, role, work_item_id, seq, entry, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
    ).run(record.sessionId, record.runId, record.role, record.workItemId, seq, JSON.stringify(redact(record.entry)), now);
    return Number(result.lastInsertRowid);
  }

  sessionLogCount(sessionId: string): number {
    const row = this.db.prepare(`SELECT COUNT(*) AS count FROM session_logs WHERE session_id=?`).get(sessionId) as Row;
    return Number(row.count);
  }

  sessionLogs(sessionId?: string, after = 0, limit = 200): Row[] {
    const bounded = Math.min(Math.max(limit, 1), 500);
    if (sessionId) {
      return this.db.prepare(
        `SELECT * FROM session_logs WHERE session_id=? AND id>? ORDER BY id LIMIT ?`,
      ).all(sessionId, after, bounded) as Row[];
    }
    return this.db.prepare(`SELECT * FROM session_logs WHERE id>? ORDER BY id LIMIT ?`).all(after, bounded) as Row[];
  }

  results(role?: string): AgentResult[] {
    const rows = (role
      ? this.db.prepare(`SELECT body FROM results WHERE role=? ORDER BY created_at`).all(role)
      : this.db.prepare(`SELECT body FROM results ORDER BY created_at`).all()) as Row[];
    return rows.map((row) => JSON.parse(String(row.body)) as AgentResult);
  }

  rows(table: "events" | "checks" | "findings" | "dependencies" | "phases" | "agent_runs", after = 0, limit = 200): Row[] {
    const bounded = Math.min(Math.max(limit, 1), 500);
    if (table === "events") {
      return this.db.prepare(`SELECT * FROM events WHERE id>? ORDER BY id LIMIT ?`).all(after, bounded) as Row[];
    }
    return this.db.prepare(`SELECT * FROM ${table} LIMIT ?`).all(bounded) as Row[];
  }

  events(
    after = 0,
    limit = 200,
    filters: { types?: string[]; role?: string; runId?: string } = {},
  ): Row[] {
    const bounded = Math.min(Math.max(limit, 1), 500);
    const clauses = ["id > ?"];
    const parameters: Array<string | number> = [after];
    if (filters.types?.length) {
      clauses.push(`type IN (${filters.types.map(() => "?").join(", ")})`);
      parameters.push(...filters.types);
    }
    if (filters.role) {
      clauses.push(`json_extract(payload, '$.role') = ?`);
      parameters.push(filters.role);
    }
    if (filters.runId) {
      clauses.push(`json_extract(payload, '$.runId') = ?`);
      parameters.push(filters.runId);
    }
    parameters.push(bounded);
    return this.db.prepare(
      `SELECT * FROM events WHERE ${clauses.join(" AND ")} ORDER BY id LIMIT ?`,
    ).all(...parameters) as Row[];
  }

  event(type: string, payload: unknown, parentId: number | null = null): number {
    const safe = redact(payload);
    const now = new Date().toISOString();
    const result = this.db.prepare(`INSERT INTO events (type, parent_id, payload, created_at) VALUES (?, ?, ?, ?)`)
      .run(type, parentId, JSON.stringify(safe), now);
    const record = {
      id: Number(result.lastInsertRowid),
      schemaVersion: 1,
      type,
      parentId,
      payload: safe,
      createdAt: now,
    };
    mkdirSync(dirname(this.eventFile), { recursive: true });
    appendFileSync(this.eventFile, `${JSON.stringify(record)}\n`);
    return Number(result.lastInsertRowid);
  }

  operation(key: string, digest: string, action: () => unknown): unknown {
    const existing = this.db.prepare(`SELECT digest, result FROM operations WHERE operation_key=?`).get(key) as Row | undefined;
    if (existing) {
      if (existing.digest !== digest) throw new Error(`idempotency conflict for ${key}`);
      return JSON.parse(String(existing.result));
    }
    const result = action();
    this.db.prepare(`INSERT INTO operations VALUES (?, ?, ?, ?)`).run(key, digest, JSON.stringify(redact(result)), new Date().toISOString());
    return result;
  }

  saveDelivery(delivery: DeliveryRecord): void {
    const safe = redact(delivery) as DeliveryRecord;
    this.db.prepare(`INSERT OR REPLACE INTO deliveries VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
      .run(
        safe.workItemId, safe.repositoryId, safe.branch, safe.headSha,
        safe.pullRequestUrl, safe.ciStatus, JSON.stringify(safe), safe.updatedAt,
      );
    this.event("delivery_updated", {
      workItemId: safe.workItemId,
      repositoryId: safe.repositoryId,
      branch: safe.branch,
      headSha: safe.headSha,
      pullRequestUrl: safe.pullRequestUrl,
      ciStatus: safe.ciStatus,
      checks: safe.checks,
    });
  }

  deliveries(): DeliveryRecord[] {
    const rows = this.db.prepare(`SELECT body FROM deliveries ORDER BY repository_id`).all() as Row[];
    return rows.map((row) => JSON.parse(String(row.body)) as DeliveryRecord);
  }

  exportSnapshot(output: string): void {
    const snapshot = {
      campaign: this.campaign(),
      request: this.request(),
      results: this.results(),
      events: this.rows("events", 0, 500),
      checks: this.rows("checks"),
      findings: this.rows("findings"),
      dependencies: this.rows("dependencies"),
      deliveries: this.deliveries(),
    };
    writeFileSync(output, JSON.stringify(redact(snapshot), null, 2));
  }

  rawRequestFile(revision: number): string {
    return readFileSync(resolve(this.campaignDir, `requests/revision-${revision}.json`), "utf8");
  }
}

const sensitiveKey = /^(authorization|token|secret|password|credentials|api[_-]?key)$/i;
const bearer = /Bearer\s+[A-Za-z0-9._~+/-]+=*/gi;
const longDigitRun = /\b\d{8,18}\b/g;

export function redact(value: unknown): unknown {
  if (typeof value === "string") return value.replace(bearer, "Bearer [REDACTED]").replace(longDigitRun, "[REDACTED-NUMBER]");
  if (Array.isArray(value)) return value.map(redact);
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value as Record<string, unknown>).map(([key, item]) => [
      key,
      sensitiveKey.test(key) ? "[REDACTED]" : redact(item),
    ]));
  }
  return value;
}
