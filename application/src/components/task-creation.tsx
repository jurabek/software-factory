"use client";

import { useEffect, useRef, useState } from "react";
import { daemonCreationOptions, daemonCreateTask, type QualifiedTask } from "../client/daemon-api.ts";
import type { DaemonConnection } from "../server/daemon-registry.ts";

const thinkingLevels = ["off", "minimal", "low", "medium", "high", "xhigh", "max"] as const;

function recentKey(daemonId: string): string {
  return `software-factory.recent-directories.${daemonId}`;
}

export function TaskCreation({ daemon, onCreated }: { daemon: DaemonConnection; onCreated: (task: QualifiedTask) => void }) {
  const [request, setRequest] = useState("");
  const [repoType, setRepoType] = useState<"local" | "github">("github");
  const [repoValue, setRepoValue] = useState("");
  const [harness, setHarness] = useState("");
  const [model, setModel] = useState("");
  const [thinking, setThinking] = useState("");
  const [harnesses, setHarnesses] = useState<string[]>([]);
  const [models, setModels] = useState<{ provider: string; id: string }[]>([]);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const generation = useRef(0);

  useEffect(() => {
    const current = ++generation.current;
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    void daemonCreationOptions(daemon.id, undefined, controller.signal)
      .then((options) => {
        if (generation.current !== current) return;
        setHarnesses(options.harnesses);
        setModels(options.models.models);
        setHarness((previous) => previous || options.defaults.coding_agent);
        setModel((previous) => previous || options.defaults.model);
        setThinking((previous) => previous || options.defaults.thinking);
        setLoading(false);
      })
      .catch((failure: unknown) => {
        if (controller.signal.aborted || generation.current !== current) return;
        setError(failure instanceof Error ? failure.message : "Could not load creation options.");
        setLoading(false);
      });
    try {
      const recent = JSON.parse(localStorage.getItem(recentKey(daemon.id)) ?? "[]") as unknown;
      if (Array.isArray(recent) && typeof recent[0] === "string") setRepoValue((previous) => previous || recent[0]);
    } catch {
      // Ignore corrupt local preferences; they are only a convenience.
    }
    return () => controller.abort();
  }, [daemon.id]);

  useEffect(() => {
    if (!harness) return;
    const current = ++generation.current;
    const controller = new AbortController();
    void daemonCreationOptions(daemon.id, harness, controller.signal)
      .then((options) => {
        if (generation.current !== current) return;
        setHarnesses(options.harnesses);
        setModels(options.models.models);
      })
      .catch(() => undefined);
    return () => controller.abort();
  }, [daemon.id, harness]);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      const trimmed = request.trim();
      if (!trimmed) throw new Error("Describe the task first.");
      if (!repoValue.trim()) throw new Error(repoType === "local" ? "Provide an absolute local path." : "Provide owner/repository.");
      const result = await daemonCreateTask(daemon.id, {
        request: trimmed,
        repositories: [repoType === "local" ? { type: "local", path: repoValue.trim(), primary: true } : { type: "github", repo: repoValue.trim(), primary: true }],
        ...(harness ? { coding_agent: harness } : {}),
        ...(model ? { model } : {}),
        ...(thinking ? { thinking } : {}),
      });
      if (repoType === "local") {
        try {
          const recent = JSON.parse(localStorage.getItem(recentKey(daemon.id)) ?? "[]") as unknown;
          const values = [repoValue.trim(), ...(Array.isArray(recent) ? recent.filter((entry): entry is string => typeof entry === "string") : [])].slice(0, 6);
          localStorage.setItem(recentKey(daemon.id), JSON.stringify([...new Set(values)]));
        } catch {
          // Recent paths are a convenience; creation already succeeded.
        }
      }
      setRequest("");
      onCreated(result.task);
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not create the task.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form className="form task-form" onSubmit={submit} aria-label={`Create a task on ${daemon.name}`}>
      <h3>Create a draft on {daemon.name}</h3>
      {error ? <p role="alert" className="notice">{error}</p> : null}
      <label>Task request<textarea value={request} onChange={(event) => setRequest(event.target.value)} required maxLength={20000} placeholder="Coordinate the change…" /></label>
      <div className="form-row">
        <label>Repository type
          <select value={repoType} onChange={(event) => setRepoType(event.target.value as "local" | "github")}>
            <option value="github">GitHub</option>
            <option value="local">Local path on the daemon</option>
          </select>
        </label>
        <label>{repoType === "local" ? "Absolute daemon path" : "owner/repository"}
          <input value={repoValue} onChange={(event) => setRepoValue(event.target.value)} required placeholder={repoType === "local" ? "/srv/sandbox/repo" : "owner/app"} />
        </label>
      </div>
      {loading ? <p>Loading harness options…</p> : (
        <div className="form-row">
          <label>Harness
            <select value={harness} onChange={(event) => setHarness(event.target.value)}>
              {harnesses.map((entry) => <option key={entry} value={entry}>{entry}</option>)}
            </select>
          </label>
          <label>Model<input value={model} onChange={(event) => setModel(event.target.value)} placeholder="provider/model" /></label>
          <label>Thinking
            <select value={thinking} onChange={(event) => setThinking(event.target.value)}>
              {thinkingLevels.map((level) => <option key={level} value={level}>{level}</option>)}
            </select>
          </label>
        </div>
      )}
      {models.length ? <p className="hint">Available models: {models.map((entry) => `${entry.provider}/${entry.id}`).join(", ")}</p> : null}
      <div className="actions"><button type="submit" disabled={submitting || loading}>{submitting ? "Creating…" : "Create draft"}</button></div>
    </form>
  );
}
