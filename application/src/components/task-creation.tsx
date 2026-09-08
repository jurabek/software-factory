"use client";

import { ChevronRight } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { daemonCreationOptions, daemonCreateTask, type QualifiedTask } from "@/client/daemon-api.ts";
import type { DaemonConnection } from "@/server/daemon-registry.ts";
import { Alert, AlertDescription } from "@/components/ui/alert.tsx";
import { Button } from "@/components/ui/button.tsx";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible.tsx";
import { Input } from "@/components/ui/input.tsx";
import { Label } from "@/components/ui/label.tsx";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select.tsx";
import { Textarea } from "@/components/ui/textarea.tsx";

const thinkingLevels = ["off", "minimal", "low", "medium", "high", "xhigh", "max"] as const;
type RepositoryDraft = { type: "local" | "github"; value: string; name: string; primary: boolean };

function recentKey(daemonId: string): string {
  return `software-factory.recent-directories.${daemonId}`;
}

function Section({ title, defaultOpen = false, children }: { title: string; defaultOpen?: boolean; children: React.ReactNode }) {
  return (
    <Collapsible defaultOpen={defaultOpen} className="bg-card mt-3 rounded-md border">
      <CollapsibleTrigger className="text-subtle hover:text-foreground group flex w-full items-center gap-2 px-3 py-2.5 text-left">
        <ChevronRight className="size-4 transition-transform group-data-[state=open]:rotate-90" />{title}
      </CollapsibleTrigger>
      <CollapsibleContent className="p-3 pt-0">{children}</CollapsibleContent>
    </Collapsible>
  );
}

export function TaskCreation({ daemon, offline, onCreated }: { daemon: DaemonConnection; offline: boolean; onCreated: (task: QualifiedTask) => void }) {
  const [request, setRequest] = useState("");
  const [repositories, setRepositories] = useState<RepositoryDraft[]>([{ type: "github", value: "", name: "", primary: true }]);
  const [harness, setHarness] = useState("");
  const [model, setModel] = useState("");
  const [thinking, setThinking] = useState("");
  const [harnesses, setHarnesses] = useState<string[]>([]);
  const [models, setModels] = useState<{ provider: string; id: string }[]>([]);
  const [recentDirectories, setRecentDirectories] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const optionsGeneration = useRef(0);
  const modelOptions = models.map((entry) => `${entry.provider}/${entry.id}`);
  const showCustomModel = model !== "" && !modelOptions.includes(model);
  const modelGeneration = useRef(0);
  const mutationController = useRef<AbortController | null>(null);

  useEffect(() => {
    const current = ++optionsGeneration.current;
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    setHarnesses([]);
    setModels([]);
    setHarness("");
    setModel("");
    setThinking("");
    setRecentDirectories([]);
    void daemonCreationOptions(daemon.id, undefined, controller.signal)
      .then((options) => {
        if (optionsGeneration.current !== current) return;
        setHarnesses(options.harnesses);
        setModels(options.models.models);
        setHarness(options.defaults.coding_agent);
        setModel(options.defaults.model);
        setThinking(options.defaults.thinking);
        setLoading(false);
      })
      .catch((failure: unknown) => {
        if (controller.signal.aborted || optionsGeneration.current !== current) return;
        setError(failure instanceof Error ? failure.message : "Could not load creation options.");
        setLoading(false);
      });
    try {
      const recent = JSON.parse(localStorage.getItem(recentKey(daemon.id)) ?? "[]") as unknown;
      if (Array.isArray(recent)) {
        const paths = recent.filter((entry): entry is string => typeof entry === "string");
        setRecentDirectories(paths);
        if (paths[0]) setRepositories((previous) => previous.map((repository, index) => index === 0 && !repository.value ? { ...repository, type: "local", value: paths[0] } : repository));
      }
    } catch {
      // Ignore corrupt local preferences; they are only a convenience.
    }
    return () => {
      controller.abort();
      mutationController.current?.abort();
    };
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
    const daemonGeneration = optionsGeneration.current;
    const current = ++modelGeneration.current;
    const controller = new AbortController();
    void daemonCreationOptions(daemon.id, harness, controller.signal)
      .then((options) => {
        if (optionsGeneration.current !== daemonGeneration || modelGeneration.current !== current) return;
        setHarnesses(options.harnesses);
        setModels(options.models.models);
        const available = options.models.models.map((entry) => `${entry.provider}/${entry.id}`);
        setModel((previous) => {
          if (available.includes(previous)) return previous;
          if (available.includes(options.defaults.model)) return options.defaults.model;
          return available[0] ?? previous;
        });
      })
      .catch(() => undefined);
    return () => controller.abort();
  }, [daemon.id, harness]);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    mutationController.current?.abort();
    const controller = new AbortController();
    mutationController.current = controller;
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
      }, controller.signal);
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
      if (!controller.signal.aborted) onCreated(result.task);
    } catch (failure) {
      if (controller.signal.aborted) return;
      setError(failure instanceof Error ? failure.message : "Could not create the task.");
    } finally {
      if (!controller.signal.aborted) setSubmitting(false);
    }
  }

  return (
    <form className="mx-auto my-[clamp(4rem,13vh,11rem)] w-[min(68rem,calc(100%-4rem))]" onSubmit={submit} aria-label={`Create a task on ${daemon.name}`}>
      <p className="text-primary text-xs uppercase tracking-[0.12em]">{daemon.name} / New task</p>
      <h1 className="my-2 mb-8 text-center text-4xl font-medium tracking-tight">What should the factory build?</h1>
      {offline ? <Alert role="alert" className="mb-3 border-l-2 border-l-info"><AlertDescription>Daemon offline. Reconnect before creating a task.</AlertDescription></Alert> : null}
      {error ? <Alert role="alert" variant="destructive" className="mb-3"><AlertDescription>{error}</AlertDescription></Alert> : null}

      <Label className="bg-secondary block border-l-2 border-l-primary">
        <span className="sr-only">Task request</span>
        <Textarea
          value={request}
          onChange={(event) => setRequest(event.target.value)}
          onKeyDown={(event) => { if ((event.ctrlKey || event.metaKey) && event.key === "Enter") { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }}
          required
          maxLength={20000}
          placeholder="Coordinate the change..."
          className="min-h-48 resize-y rounded-none border-0 bg-transparent p-6 text-base shadow-none focus-visible:ring-0 dark:bg-transparent"
        />
      </Label>

      <Section title="Repository sources" defaultOpen>
        <div className="grid gap-3 border-t pt-3">
          {repositories.map((repository, index) => (
            <div className="grid items-end gap-3 md:grid-cols-[auto_minmax(7rem,.5fr)_minmax(8rem,.6fr)_minmax(0,1fr)_auto]" key={index}>
              <Button type="button" variant="outline" size="sm" aria-label={`Make repository ${index + 1} primary`} aria-pressed={repository.primary} className={repository.primary ? "border-primary text-primary" : ""} onClick={() => selectPrimary(index)}>{repository.primary ? "Primary" : "Secondary"}</Button>
              <div className="grid gap-1.5"><Label htmlFor={`repository-name-${index}`}>Name</Label><Input id={`repository-name-${index}`} value={repository.name} onChange={(event) => updateRepository(index, { name: event.target.value })} placeholder="optional" /></div>
              <div className="grid gap-1.5">
                <Label htmlFor={`repository-type-${index}`}>Type</Label>
                <Select value={repository.type} onValueChange={(value) => updateRepository(index, { type: value as RepositoryDraft["type"] })}>
                  <SelectTrigger id={`repository-type-${index}`} className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent><SelectItem value="github">GitHub</SelectItem><SelectItem value="local">Local daemon path</SelectItem></SelectContent>
                </Select>
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor={`repository-value-${index}`}>{repository.type === "local" ? "Absolute daemon path" : "owner/repository"}</Label>
                <Input id={`repository-value-${index}`} value={repository.value} onChange={(event) => updateRepository(index, { value: event.target.value })} list={repository.type === "local" ? `recent-directories-${daemon.id}` : undefined} required placeholder={repository.type === "local" ? "/srv/sandbox/repo" : "owner/app"} />
              </div>
              <Button type="button" variant="outline" size="sm" disabled={repositories.length === 1} onClick={() => removeRepository(index)}>Remove</Button>
            </div>
          ))}
          <Button type="button" variant="outline" size="sm" className="justify-self-start" onClick={addRepository}>Add repository</Button>
        </div>
      </Section>

      {recentDirectories.length ? <datalist id={`recent-directories-${daemon.id}`}>{recentDirectories.map((path) => <option key={path} value={path} />)}</datalist> : null}

      {loading ? <p className="text-subtle mt-3">Loading harness options…</p> : (
        <Section title="Harness and model">
          <div className="grid gap-3 border-t pt-3 md:grid-cols-3">
            <div className="grid gap-1.5">
              <Label htmlFor="harness">Harness</Label>
              <Select value={harness} onValueChange={setHarness}>
                <SelectTrigger id="harness" className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>{harnesses.map((entry) => <SelectItem key={entry} value={entry}>{entry}</SelectItem>)}</SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="model">Model</Label>
              <Select value={model} onValueChange={setModel} disabled={modelOptions.length === 0 && !showCustomModel}>
                <SelectTrigger id="model" className="w-full"><SelectValue placeholder="No models available" /></SelectTrigger>
                <SelectContent>
                  {showCustomModel ? <SelectItem key={model} value={model}>{model}</SelectItem> : null}
                  {modelOptions.map((entry) => <SelectItem key={entry} value={entry}>{entry}</SelectItem>)}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="thinking">Thinking</Label>
              <Select value={thinking} onValueChange={setThinking}>
                <SelectTrigger id="thinking" className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>{thinkingLevels.map((level) => <SelectItem key={level} value={level}>{level}</SelectItem>)}</SelectContent>
              </Select>
            </div>
          </div>
        </Section>
      )}

      <footer className="bg-secondary text-muted-foreground mt-3 flex items-center gap-4 rounded-md border px-3 py-2.5 text-xs">
        <span>Ctrl/Cmd + Enter to create</span>
        <Button type="submit" variant="outline" size="sm" className="border-primary text-primary ml-auto" disabled={offline || submitting || loading}>{submitting ? "Creating..." : "Create draft"}</Button>
      </footer>
    </form>
  );
}
