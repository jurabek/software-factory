"use client";

import { useEffect, useRef, useState } from "react";
import { daemonCreationOptions, daemonCreateTask, type QualifiedTask } from "../client/daemon-api.ts";
import type { DaemonConnection } from "../server/daemon-registry.ts";

const thinkingLevels = ["off", "minimal", "low", "medium", "high", "xhigh", "max"] as const;
type RepositoryDraft = { type: "local" | "github"; value: string; name: string; primary: boolean };

function recentKey(daemonId: string): string {
  return `software-factory.recent-directories.${daemonId}`;
}

export function TaskCreation({ daemon, onCreated }: { daemon: DaemonConnection; onCreated: (task: QualifiedTask) => void }) {
  const [request, setRequest] = useState("");
  const [repositories, setRepositories] = useState<RepositoryDraft[]>([{ type: "github", value: "", name: "", primary: true }]);
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
       if (Array.isArray(recent) && typeof recent[0] === "string") setRepositories((previous) => previous.map((repository, index) => index === 0 && !repository.value ? { ...repository, type: "local", value: recent[0] } : repository));
    } catch {
      // Ignore corrupt local preferences; they are only a convenience.
    }
    return () => controller.abort();
  }, [daemon.id]);

  function updateRepository(index: number, update: Partial<RepositoryDraft>) {
    setRepositories((current) => current.map((repository, repositoryIndex) => repositoryIndex === index ? { ...repository, ...update } : repository));
  }

  function addRepository() {
    setRepositories((current) => [...current, { type: "local", value: "", name: "", primary: false }]);
  }

  function removeRepository(index: number) {
    setRepositories((current) => current.length === 1 ? current : current.filter((_, repositoryIndex) => repositoryIndex !== index).map((repository, repositoryIndex) => ({ ...repository, primary: repository.primary || repositoryIndex === 0 })));
  }

  function selectPrimary(index: number) {
    setRepositories((current) => current.map((repository, repositoryIndex) => ({ ...repository, primary: repositoryIndex === index })));
  }

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
       if (repositories.some((repository) => !repository.value.trim())) throw new Error("Complete every repository before creating the task.");
       if (repositories.some((repository) => repository.type === "local" && !repository.value.trim().startsWith("/"))) throw new Error("Local repositories need an absolute daemon path.");
       if (repositories.some((repository) => repository.type === "github" && !/^[^/\s]+\/[^/\s]+$/.test(repository.value.trim()))) throw new Error("GitHub repositories need owner/name.");
       const result = await daemonCreateTask(daemon.id, {
         request: trimmed,
         repositories: repositories.map((repository) => repository.type === "local"
           ? { type: "local", path: repository.value.trim(), ...(repository.name.trim() ? { name: repository.name.trim() } : {}), primary: repository.primary }
           : { type: "github", repo: repository.value.trim(), ...(repository.name.trim() ? { name: repository.name.trim() } : {}), primary: repository.primary }),
        ...(harness ? { coding_agent: harness } : {}),
        ...(model ? { model } : {}),
        ...(thinking ? { thinking } : {}),
      });
       if (repositories.some((repository) => repository.type === "local")) {
         try {
           const recent = JSON.parse(localStorage.getItem(recentKey(daemon.id)) ?? "[]") as unknown;
           const values = [...repositories.filter((repository) => repository.type === "local").map((repository) => repository.value.trim()), ...(Array.isArray(recent) ? recent.filter((entry): entry is string => typeof entry === "string") : [])].slice(0, 6);
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
       <div className="repository-drafts">
         {repositories.map((repository, index) => (
           <div className="form-row repository-draft" key={index}>
             <button type="button" aria-label={`Make repository ${index + 1} primary`} aria-pressed={repository.primary} onClick={() => selectPrimary(index)}>{repository.primary ? "Primary" : "Secondary"}</button>
             <label>Name<input value={repository.name} onChange={(event) => updateRepository(index, { name: event.target.value })} placeholder="optional" /></label>
             <label>Type<select value={repository.type} onChange={(event) => updateRepository(index, { type: event.target.value as RepositoryDraft["type"] })}><option value="github">GitHub</option><option value="local">Local daemon path</option></select></label>
             <label>{repository.type === "local" ? "Absolute daemon path" : "owner/repository"}<input value={repository.value} onChange={(event) => updateRepository(index, { value: event.target.value })} required placeholder={repository.type === "local" ? "/srv/sandbox/repo" : "owner/app"} /></label>
             <button type="button" disabled={repositories.length === 1} onClick={() => removeRepository(index)}>Remove</button>
           </div>
         ))}
         <button type="button" onClick={addRepository}>Add repository</button>
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
