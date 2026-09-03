import type { Campaign, Check, Control, Diff, Envelope, Health, Phase, TraceEvent } from "./types";

let token = "";
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  if (init?.method && init.method !== "GET") headers.set("X-Software-Factory-Token", token);
  if (init?.body) headers.set("Content-Type", "application/json");
  const response = await fetch(`/api/v1${path}`, { ...init, headers, cache: "no-store" });
  if (!response.ok) {
    const body = await response.json().catch(() => ({ message: response.statusText })) as { message?: string };
    throw new Error(body.message ?? response.statusText);
  }
  return response.json() as Promise<T>;
}
export const api = {
  initialize: async () => { const value = await request<Control>("/control"); token = value.token ?? ""; return value; },
  health: () => request<Health>("/health"),
  campaigns: () => request<Campaign[]>("/campaigns"),
  campaign: (id: string) => request<Campaign>(`/campaigns/${encodeURIComponent(id)}`),
  create: (body: { request: string; repository: { type: string; path?: string; repo?: string } }) => request<Campaign>("/campaigns", { method: "POST", body: JSON.stringify(body) }),
  command: (id: string, command: string) => request<{ accepted: boolean }>(`/campaigns/${encodeURIComponent(id)}/${command}`, { method: "POST" }),
  remove: (id: string) => request<{ deleted: boolean }>(`/campaigns/${encodeURIComponent(id)}`, { method: "DELETE" }),
  phases: (id: string) => request<Phase[]>(`/campaigns/${encodeURIComponent(id)}/phases`),
  events: (id: string, after = 0) => request<{ events: TraceEvent[]; cursor: number }>(`/campaigns/${encodeURIComponent(id)}/events?after=${after}`),
  checks: (id: string) => request<Check[]>(`/campaigns/${encodeURIComponent(id)}/checks`),
  results: (id: string) => request<Envelope[]>(`/campaigns/${encodeURIComponent(id)}/results`),
  diff: (id: string) => request<Diff>(`/campaigns/${encodeURIComponent(id)}/diff`),
};
