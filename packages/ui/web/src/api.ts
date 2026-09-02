import type {
  AgentRun,
  Campaign,
  CheckRow,
  EventPage,
  FindingRow,
  Phase,
  ControlState,
} from "./types";
import type { AgentResult } from "@software-factory/core";

async function get<T>(path: string): Promise<T> {
  const response = await fetch(path, { cache: "no-store" });
  if (!response.ok) throw new Error(`${response.status} ${response.statusText}`);
  return response.json() as Promise<T>;
}

export const api = {
  control: () => get<ControlState>("/api/control"),
  approvePlan: async (id: string, token: string) => {
    const response = await fetch(`/api/campaigns/${encodeURIComponent(id)}/approve-plan`, {
      method: "POST",
      headers: { "X-Software-Factory-Control": token },
    });
    if (!response.ok) throw new Error(`${response.status} ${response.statusText}`);
    return response.json() as Promise<{ campaign: Campaign }>;
  },
  campaigns: () => get<Campaign[]>("/api/campaigns?limit=100"),
  campaign: (id: string) => get<{ campaign: Campaign }>(`/api/campaigns/${encodeURIComponent(id)}`),
  phases: (id: string) => get<Phase[]>(`/api/campaigns/${encodeURIComponent(id)}/phases`),
  agents: (id: string) => get<AgentRun[]>(`/api/campaigns/${encodeURIComponent(id)}/agents`),
  results: (id: string) => get<AgentResult[]>(`/api/campaigns/${encodeURIComponent(id)}/results`),
  checks: (id: string) => get<CheckRow[]>(`/api/campaigns/${encodeURIComponent(id)}/checks`),
  findings: (id: string) => get<FindingRow[]>(`/api/campaigns/${encodeURIComponent(id)}/findings`),
  events: (
    id: string,
    options: { after?: number; limit?: number; types?: string[]; role?: string; runId?: string } = {},
  ) => {
    const parameters = new URLSearchParams({
      after: String(options.after ?? 0),
      limit: String(options.limit ?? 500),
    });
    if (options.types?.length) parameters.set("types", options.types.join(","));
    if (options.role) parameters.set("role", options.role);
    if (options.runId) parameters.set("runId", options.runId);
    return get<EventPage>(
      `/api/campaigns/${encodeURIComponent(id)}/sessions?${parameters.toString()}`,
    );
  },
};
