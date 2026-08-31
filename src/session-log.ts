import { existsSync, readFileSync } from "node:fs";
import type { CampaignStore } from "./store.js";

export interface SessionLogMeta {
  sessionId: string;
  runId: string;
  role: string;
  workItemId: string | null;
}

export function ingestSessionJsonl(store: CampaignStore, meta: SessionLogMeta, sessionFile: string | undefined): number {
  if (!sessionFile || !existsSync(sessionFile)) return 0;
  const already = store.sessionLogCount(meta.sessionId);
  const lines = readFileSync(sessionFile, "utf8").split("\n").filter((line) => line.trim());
  let added = 0;
  for (const line of lines.slice(already)) {
    try {
      store.appendSessionLog({ ...meta, entry: JSON.parse(line) });
      added += 1;
    } catch {
      store.appendSessionLog({ ...meta, entry: { type: "invalid_jsonl", raw: line.slice(0, 2_000) } });
      added += 1;
    }
  }
  return added;
}
