"use client";

import { Archive, Brain, ChevronRight, Columns2, Copy, ExternalLink, FastForward, File, Folder, GitBranch, Monitor, Pencil, PanelLeftClose, Plus, Send, Shield, SkipForward, Terminal, Users } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import {
  daemonArtifacts,
  daemonAttempts,
  daemonBranches,
  daemonChecks,
  daemonCommand,
  daemonDiff,
  daemonEvents,
  daemonFeedback,
  daemonIntervene,
  daemonInterventions,
  daemonRemoveTask,
  daemonResults,
  daemonSessions,
  daemonTask,
  openTaskStream,
  type QualifiedTask,
  type TaskArtifact,
  type TaskAttempt,
  type TaskBranch,
  type TaskCheck,
  type TaskDetails,
  type TaskDiff,
  type TaskEvent,
  type TaskIntervention,
  type TaskResult,
} from "@/client/daemon-api.ts";
import { qualifiedEventKey, relativeTime, RequestScope } from "@/client/daemon-ui-state.ts";
import {
  eventDuration,
  eventIcon,
  eventResult,
  eventResultLine,
  eventStartedAt,
  eventSuccess,
  eventTarget,
  eventTitle,
  meaningfulWorkEvents,
  payloadRecord,
  visibleWorkEvents,
} from "@/client/work-log.ts";
import { AttemptGraph } from "@/components/attempt-graph.tsx";
import { EventDialog } from "@/components/event-dialog.tsx";
import { Alert, AlertDescription } from "@/components/ui/alert.tsx";
import { Avatar, AvatarFallback } from "@/components/ui/avatar.tsx";
import { Badge } from "@/components/ui/badge.tsx";
import { Button } from "@/components/ui/button.tsx";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible.tsx";
import { Label } from "@/components/ui/label.tsx";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select.tsx";
import { SidebarTrigger } from "@/components/ui/sidebar.tsx";
import { Switch } from "@/components/ui/switch.tsx";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs.tsx";
import { Textarea } from "@/components/ui/textarea.tsx";
import { stateTextClass } from "@/lib/state-style.ts";
import { cn } from "@/lib/utils.ts";

const commands = ["start", "approve", "pause", "resume", "abort"] as const;
const interventionActions = ["comment", "steer", "follow_up", "retry", "revise", "repair"] as const;
type InterventionAction = (typeof interventionActions)[number];
const actionLabels: Record<InterventionAction, string> = {
  comment: "Comment",
  steer: "Steer running agent",
  follow_up: "Follow up after settle",
  retry: "Retry exact",
  revise: "Revise and retry",
  repair: "Continue repair",
};
const liveTone: Record<string, string> = { live: "bg-success shadow-[0_0_0.4rem_var(--success)]", reconnecting: "bg-warning", connecting: "bg-warning", offline: "bg-destructive" };
const visibleEventLimit = 500;
type DisplayArtifact = { id: string; kind: "result" | "check" | "diff" | "file"; title: string; subtitle: string; content: string };

function commandEnabled(command: (typeof commands)[number], state: string): boolean {
  switch (command) {
    case "start": return state === "draft";
    case "approve": return state === "awaiting_plan_approval";
    case "pause": return ["preparing", "planning", "building", "checking", "reviewing"].includes(state);
    case "resume": return ["paused", "blocked"].includes(state);
    case "abort": return !["completed", "aborted"].includes(state);
  }
}

type TaskRepository = { id: string; name: string; source_type: string; primary: boolean };

function isTaskRepository(value: unknown): value is TaskRepository {
  if (!value || typeof value !== "object") return false;
  const repository = value as Partial<TaskRepository>;
  return typeof repository.id === "string" && typeof repository.name === "string" && typeof repository.source_type === "string" && typeof repository.primary === "boolean";
}

function interventionChoices(state: string, availableActions: string[]): InterventionAction[] {
  const serverActions = availableActions.filter((action): action is InterventionAction => interventionActions.includes(action as InterventionAction));
  if (state === "draft") return ["comment"];
  if (state === "blocked" || state === "paused") return [...new Set<InterventionAction>(["comment", ...serverActions.filter((action) => ["retry", "revise", "repair"].includes(action))])];
  return serverActions.length ? serverActions : ["comment"];
}

function renderedArtifactContent(artifact: DisplayArtifact): string {
  if (artifact.kind !== "result") return artifact.content;
  try { return JSON.stringify(JSON.parse(artifact.content), null, 2); } catch { return artifact.content; }
}

function monogram(name: string): string {
  const parts = name.replace(/@.*/, "").split(/[.\s_@-]+/).filter(Boolean);
  return ((parts[0]?.[0] ?? name[0] ?? "?") + (parts[1]?.[0] ?? "")).toUpperCase();
}

function displayName(name: string): string {
  const base = name.replace(/@.*/, "");
  return base.split(/[.\s_-]+/).filter(Boolean).map((part) => (part[0]?.toUpperCase() ?? "") + part.slice(1)).join(" ") || name;
}

function modelLabel(model?: string): string {
  if (!model) return "DEFAULT";
  return (model.split("/").pop() ?? model).toUpperCase();
}

type ChatItem = { key: string; role: "user" | "agent" | "system" | "tool" | "event"; author?: string; text?: string; at: number; iso: string; event?: TaskEvent };

function buildTimeline(request: string, createdAt: string, author: string, interventions: TaskIntervention[], events: TaskEvent[]): ChatItem[] {
  const items: ChatItem[] = [];
  if (request.trim()) items.push({ key: "request", role: "user", author, text: request, at: new Date(createdAt).getTime() || 0, iso: createdAt });
  for (const intervention of interventions) {
    if (!intervention.text?.trim()) continue;
    items.push({ key: `iv-${intervention.id}`, role: "user", author: intervention.actor, text: intervention.text, at: new Date(intervention.created_at).getTime() || 0, iso: intervention.created_at });
  }
  for (const event of events) {
    const at = eventStartedAt(event);
    const iso = Number.isNaN(at.getTime()) ? event.started_at : at.toISOString();
    if (event.type === "tool_call") { items.push({ key: `ev-${event.sequence}`, role: "tool", at: at.getTime() || 0, iso, event }); continue; }
    const message = payloadRecord(payloadRecord(event.payload).message);
    const errored = event.type.includes("error") || eventSuccess(event) === false || message.stopReason === "error" || message.stop_reason === "error";
    const text = eventResult(event).trim();
    if (text) { items.push({ key: `ev-${event.sequence}`, role: errored ? "system" : "agent", text, at: at.getTime() || 0, iso, event }); continue; }
    // Lifecycle events carry no prose, but they are still the record of what
    // the daemon did; they stay in the log as openable one-line markers.
    items.push({ key: `ev-${event.sequence}`, role: errored ? "system" : "event", text: eventTitle(event), at: at.getTime() || 0, iso, event });
  }
  return items.sort((left, right) => left.at - right.at || left.key.localeCompare(right.key));
}

function formatSpan(ms: number): string {
  const seconds = Math.max(0, Math.round(ms / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${minutes % 60}m`;
}

type TimelineBlock = { kind: "user"; item: ChatItem } | { kind: "work"; key: string; items: ChatItem[]; span: number };

function groupTimeline(items: ChatItem[]): TimelineBlock[] {
  const blocks: TimelineBlock[] = [];
  let current: ChatItem[] = [];
  const flush = () => {
    if (!current.length) return;
    const first = current[0];
    const last = current[current.length - 1];
    blocks.push({ kind: "work", key: first.key, items: current, span: Math.max(0, last.at - first.at) });
    current = [];
  };
  for (const item of items) {
    if (item.role === "user") { flush(); blocks.push({ kind: "user", item }); continue; }
    current.push(item);
  }
  flush();
  return blocks;
}

export function TaskDetail({ daemonId, daemonName, task, rootTask, login, offline, onChanged, onOpenTask, onRemoved }: {
  daemonId: string;
  daemonName: string;
  task: QualifiedTask;
  rootTask: QualifiedTask;
  login: string;
  offline: boolean;
  onChanged: () => Promise<void> | void;
  onOpenTask: () => void;
  onRemoved: (parentTaskId?: string) => void;
}) {
  const [details, setDetails] = useState<TaskDetails | null>(null);
  const [attempts, setAttempts] = useState<TaskAttempt[]>([]);
  const [branches, setBranches] = useState<TaskBranch[]>([]);
  const [artifacts, setArtifacts] = useState<TaskArtifact[]>([]);
  const [checks, setChecks] = useState<TaskCheck[]>([]);
  const [results, setResults] = useState<TaskResult[]>([]);
  const [diff, setDiff] = useState<TaskDiff>({ repositories: [] });
  const [sessions, setSessions] = useState<TaskDetails[]>([]);
  const [interventions, setInterventions] = useState<TaskIntervention[]>([]);
  const [selectedAttempt, setSelectedAttempt] = useState<string | null>(null);
  const [selectedBranchId, setSelectedBranchId] = useState<string | null>(null);
  const [selectedArtifact, setSelectedArtifact] = useState<string | null>(null);
  const [artifactMode, setArtifactMode] = useState<"rendered" | "raw">("rendered");
  const [artifactQuote, setArtifactQuote] = useState("");
  const [selectedEvent, setSelectedEvent] = useState<TaskEvent | null>(null);
  const [autoScroll] = useState(true);
  const [events, setEvents] = useState<TaskEvent[]>([]);
  const [availableActions, setAvailableActions] = useState<string[]>([]);
  const [, setCursor] = useState<number | undefined>(undefined);
  const [live, setLive] = useState<"connecting" | "live" | "reconnecting" | "offline">("connecting");
  const [error, setError] = useState<string | null>(null);
  const [pendingCommand, setPendingCommand] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [message, setMessage] = useState("");
  const [action, setAction] = useState<InterventionAction>("comment");
  const chatScroll = useRef<HTMLDivElement | null>(null);
  const scope = useRef(new RequestScope());
  const mutationScope = useRef(new RequestScope());
  const mutationController = useRef<AbortController | null>(null);
  const cursorRef = useRef<number | undefined>(undefined);
  const seen = useRef(new Set<string>());

  const currentTask = details ?? task;
  const rootTaskId = currentTask.parent_task_id ?? currentTask.id;
  const repositories = (currentTask.repositories ?? []).filter(isTaskRepository);
  const selectedBranch = branches.find((branch) => branch.id === selectedBranchId) ?? branches.find((branch) => branch.id === currentTask.selected_branch_id) ?? branches[0];
  const artifactViews: DisplayArtifact[] = [
    ...results.map((result) => ({ id: result.id, kind: "result" as const, title: `${result.agent_role} result`, subtitle: `attempt ${result.attempt}`, content: result.payload })),
    ...checks.map((check) => ({ id: `check-${check.id}`, kind: "check" as const, title: check.name, subtitle: check.status, content: check.output || check.command })),
    ...diff.repositories.map((repository) => ({ id: `diff-${repository.repository_id}`, kind: "diff" as const, title: `${repository.name} diff`, subtitle: `${repository.files.length} files`, content: repository.patch || "No changes" })),
    ...artifacts.map((artifact) => ({ id: artifact.id, kind: "file" as const, title: artifact.type, subtitle: artifact.path, content: `This artifact remains on the daemon sandbox.\n\nPath: ${artifact.path}\nDigest: ${artifact.digest}` })),
  ];
  const selectedArtifactValue = artifactViews.find((artifact) => artifact.id === selectedArtifact) ?? null;
  const meaningfulEvents = meaningfulWorkEvents(events, selectedAttempt);
  const visibleEvents = visibleWorkEvents(events, selectedAttempt, visibleEventLimit);
  const hiddenEventCount = Math.max(0, meaningfulEvents.length - visibleEvents.length);
  const timeline = buildTimeline(currentTask.request, currentTask.created_at, login, interventions, visibleEvents);
  const timelineBlocks = groupTimeline(timeline);
  const editCount = diff.repositories.reduce((sum, repository) => sum + repository.files.length, 0);
  const otherCount = results.length + checks.length + diff.repositories.length;
  const workspacePath = currentTask.workspace_path ?? "Daemon sandbox";
  const canSend = !offline && !pending && pendingCommand === null;

  function beginMutation(): { generation: number; controller: AbortController } {
    mutationController.current?.abort();
    const controller = new AbortController();
    mutationController.current = controller;
    return { generation: mutationScope.current.next(), controller };
  }

  function mutationIsCurrent(generation: number, controller: AbortController): boolean {
    return !controller.signal.aborted && mutationScope.current.isCurrent(generation);
  }

  useEffect(() => {
    const choices = interventionChoices(currentTask.state, availableActions);
    if (!choices.includes(action)) setAction(choices[0] ?? "comment");
  }, [action, availableActions, currentTask.state]);

  async function refreshDetails(signal?: AbortSignal) {
    const [taskResult, attemptResult, branchResult, artifactResult, checksResult, resultsResult, diffResult, sessionsResult, interventionsResult] = await Promise.all([
      daemonTask(daemonId, task.id, signal),
      daemonAttempts(daemonId, task.id, signal),
      daemonBranches(daemonId, task.id, signal),
      daemonArtifacts(daemonId, task.id, signal),
      daemonChecks(daemonId, task.id, signal),
      daemonResults(daemonId, task.id, signal),
      daemonDiff(daemonId, task.id, signal),
      daemonSessions(daemonId, rootTaskId, signal),
      daemonInterventions(daemonId, task.id, signal),
    ]);
    setDetails(taskResult.task);
    setAttempts(attemptResult.attempts ?? []);
    setBranches(branchResult.branches ?? []);
    setArtifacts(artifactResult.artifacts ?? []);
    setChecks(checksResult.checks ?? []);
    setResults(resultsResult.results ?? []);
    setDiff(diffResult.diff ?? { repositories: [] });
    setSessions(sessionsResult.sessions ?? []);
    setInterventions(interventionsResult.interventions ?? []);
    setSelectedBranchId((current) => current && branchResult.branches?.some((branch) => branch.id === current)
      ? current
      : taskResult.task.selected_branch_id ?? branchResult.branches?.[0]?.id ?? null);
  }

  useEffect(() => {
    const current = scope.current.next();
    const controller = new AbortController();
    setDetails(null);
    setAttempts([]);
    setBranches([]);
    setArtifacts([]);
    setChecks([]);
    setResults([]);
    setDiff({ repositories: [] });
    setSessions([]);
    setInterventions([]);
    setSelectedAttempt(null);
    setSelectedBranchId(null);
    setSelectedArtifact(null);
    setArtifactQuote("");
    setSelectedEvent(null);
    setError(null);
    const handleFailure = (failure: unknown) => {
      if (controller.signal.aborted || !scope.current.isCurrent(current)) return;
      setError(failure instanceof Error ? failure.message : "Could not load task details.");
    };
    void refreshDetails(controller.signal).catch(handleFailure);
    const refreshTimer = setInterval(() => void refreshDetails(controller.signal).catch(handleFailure), 5_000);
    return () => {
      clearInterval(refreshTimer);
      controller.abort();
      mutationScope.current.invalidate();
      mutationController.current?.abort();
    };
  }, [daemonId, task.id]);

  useEffect(() => {
    if (autoScroll && chatScroll.current) chatScroll.current.scrollTop = chatScroll.current.scrollHeight;
  }, [autoScroll, events, interventions]);

  useEffect(() => {
    const current = scope.current.next();
    const controller = new AbortController();
    setEvents([]);
    setCursor(undefined);
    setAvailableActions([]);
    setLive("connecting");
    cursorRef.current = undefined;
    seen.current = new Set();
    let streamCleanup = false;
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
    let attempts = 0;

    function scheduleReconnect(run: () => void) {
      if (streamCleanup || controller.signal.aborted || !scope.current.isCurrent(current)) return;
      setLive(attempts >= 3 ? "offline" : "reconnecting");
      const delay = Math.min(30_000, 1_000 * 2 ** attempts);
      attempts += 1;
      reconnectTimer = setTimeout(run, delay);
    }

    function append(incoming: { sequence: number; raw: unknown }[]) {
      const fresh = incoming.filter((entry) => !seen.current.has(qualifiedEventKey(daemonId, task.id, entry.sequence)));
      if (!fresh.length) return;
      for (const entry of fresh) seen.current.add(qualifiedEventKey(daemonId, task.id, entry.sequence));
      const mapped = fresh.map((entry) => {
        const raw = entry.raw as Partial<TaskEvent> | null;
        return {
          sequence: entry.sequence,
          id: typeof raw?.id === "string" ? raw.id : String(entry.sequence),
          task_id: task.id,
          type: typeof raw?.type === "string" ? raw.type : "event",
          ...(typeof raw?.phase_id === "string" ? { phase_id: raw.phase_id } : {}),
          ...(typeof raw?.attempt_id === "string" ? { attempt_id: raw.attempt_id } : {}),
          ...(typeof raw?.artifact_id === "string" ? { artifact_id: raw.artifact_id } : {}),
          ...(typeof raw?.branch_id === "string" ? { branch_id: raw.branch_id } : {}),
          ...(typeof raw?.name === "string" ? { name: raw.name } : {}),
          payload: raw && "payload" in raw ? raw.payload : entry.raw,
          ...(Array.isArray(raw?.available_actions) ? { available_actions: raw.available_actions } : {}),
          started_at: typeof raw?.started_at === "string" ? raw.started_at : "",
        } satisfies TaskEvent;
      });
      setEvents((previous) => [...previous, ...mapped].sort((left, right) => left.sequence - right.sequence).slice(-1000));
      setAvailableActions(mapped.at(-1)?.available_actions ?? []);
      const max = Math.max(...fresh.map((entry) => entry.sequence));
      if (cursorRef.current === undefined || max > cursorRef.current) {
        cursorRef.current = max;
        setCursor(max);
      }
    }

    function connect(from: number | undefined, retry: boolean) {
      if (streamCleanup || !scope.current.isCurrent(current)) return;
      setLive(retry ? (attempts >= 3 ? "offline" : "reconnecting") : "connecting");
      openTaskStream(daemonId, task.id, from, controller.signal, (event) => {
        if (!scope.current.isCurrent(current)) return;
        attempts = 0;
        setLive("live");
        append([event]);
      }, () => {
        if (!scope.current.isCurrent(current) || controller.signal.aborted) return;
        scheduleReconnect(() => connect(cursorRef.current, true));
      }, () => { attempts = 0; setLive("live"); });
    }

    function bootstrap() {
      if (streamCleanup || !scope.current.isCurrent(current)) return;
      void daemonEvents(daemonId, task.id, { tail: 100 }, controller.signal)
        .then((result) => {
          if (!scope.current.isCurrent(current)) return;
          attempts = 0;
          setError(null);
          result.events.forEach((event) => seen.current.add(qualifiedEventKey(daemonId, task.id, event.sequence)));
          setEvents(result.events);
          setAvailableActions(result.events.at(-1)?.available_actions ?? []);
          cursorRef.current = result.events.length ? result.cursor : 0;
          setCursor(cursorRef.current);
          connect(cursorRef.current, false);
        })
        .catch((failure: unknown) => {
          if (controller.signal.aborted || !scope.current.isCurrent(current)) return;
          setError(failure instanceof Error ? failure.message : "Could not load events.");
          scheduleReconnect(bootstrap);
        });
    }

    bootstrap();
    return () => {
      streamCleanup = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      controller.abort();
    };
  }, [daemonId, task.id]);

  async function sendCommand(command: (typeof commands)[number]) {
    const { generation, controller } = beginMutation();
    setPendingCommand(command);
    setError(null);
    try {
      await daemonCommand(daemonId, task.id, command, controller.signal);
      await refreshDetails(controller.signal);
      if (mutationIsCurrent(generation, controller)) await onChanged();
    } catch (failure) {
      if (!mutationIsCurrent(generation, controller)) return;
      setError(failure instanceof Error ? failure.message : "Command failed.");
    } finally {
      if (mutationIsCurrent(generation, controller)) setPendingCommand((current) => current === command ? null : current);
    }
  }

  async function submitMessage(event: React.FormEvent) {
    event.preventDefault();
    if (pending || pendingCommand !== null) return;
    if (!message.trim() && action !== "retry") return;
    const { generation, controller } = beginMutation();
    setPending(true);
    setError(null);
    try {
      if (action === "comment") {
        await daemonIntervene(daemonId, task.id, {
          target: {
            ...(selectedAttempt ? { attempt_id: selectedAttempt } : {}),
            ...(selectedArtifactValue?.kind === "file" ? { artifact_id: selectedArtifactValue.id } : {}),
            ...(artifactQuote ? { anchor: { kind: "text_range", quote: artifactQuote } } : {}),
          },
          intent: "comment",
          message: message.trim(),
          idempotency_key: crypto.randomUUID(),
        }, controller.signal);
      } else {
        const result = await daemonIntervene(daemonId, task.id, {
          target: selectedAttempt ? { attempt_id: selectedAttempt } : {},
          intent: action,
          message: message.trim(),
          ...(selectedBranch?.head_attempt_id ? { expected_branch_head: selectedBranch.head_attempt_id } : {}),
          idempotency_key: crypto.randomUUID(),
        }, controller.signal);
        if (result.result.branch_id) setSelectedBranchId(result.result.branch_id);
        if (result.result.attempt_id) setSelectedAttempt(result.result.attempt_id);
      }
      setMessage("");
      setArtifactQuote("");
      await refreshDetails(controller.signal);
      if (mutationIsCurrent(generation, controller)) await onChanged();
    } catch (failure) {
      if (!mutationIsCurrent(generation, controller)) return;
      setError(failure instanceof Error ? failure.message : "Could not send intervention.");
    } finally {
      if (mutationIsCurrent(generation, controller)) setPending(false);
    }
  }

  async function revisePlan(event: React.FormEvent) {
    event.preventDefault();
    if (!message.trim()) return;
    const { generation, controller } = beginMutation();
    setPending(true);
    setError(null);
    try {
      await daemonFeedback(daemonId, task.id, message.trim(), currentTask.plan_digest, controller.signal);
      setMessage("");
      await refreshDetails(controller.signal);
      if (mutationIsCurrent(generation, controller)) await onChanged();
    } catch (failure) {
      if (!mutationIsCurrent(generation, controller)) return;
      setError(failure instanceof Error ? failure.message : "Could not send feedback.");
    } finally {
      if (mutationIsCurrent(generation, controller)) setPending(false);
    }
  }

  async function removeTask() {
    if (pending || pendingCommand !== null) return;
    if (!window.confirm("Delete this task and its daemon-owned files?")) return;
    const { generation, controller } = beginMutation();
    setPending(true);
    try {
      await daemonRemoveTask(daemonId, task.id, controller.signal);
      if (mutationIsCurrent(generation, controller)) {
        await onChanged();
        onRemoved(currentTask.parent_task_id);
      }
    } catch (failure) {
      if (!mutationIsCurrent(generation, controller)) return;
      setError(failure instanceof Error ? failure.message : "Could not delete the task.");
    } finally {
      if (mutationIsCurrent(generation, controller)) setPending(false);
    }
  }

  return (
    <section className="grid h-dvh min-w-0 grid-cols-1 lg:grid-cols-[minmax(0,1fr)_22rem]" aria-label={`Session ${task.id} on ${daemonName}`}>
      <div className="flex min-w-0 flex-col overflow-hidden">
        <header className="border-rail-line bg-background flex h-14 items-center justify-between gap-3 border-t-2 border-b px-4">
          <nav className="text-muted-foreground flex min-w-0 items-center gap-1.5" aria-label="Breadcrumb">
            <SidebarTrigger className="mr-1" />
            <button type="button" className="hover:text-foreground" onClick={onOpenTask}>Tasks</button><span aria-hidden="true">›</span>
            <button type="button" className="hover:text-foreground max-w-40 truncate" onClick={onOpenTask}>{rootTask.request}</button><span aria-hidden="true">›</span>
            <strong className="text-foreground truncate font-medium">{currentTask.request}</strong>
            <Button type="button" variant="ghost" size="icon-xs" aria-label="Rename session"><Pencil /></Button>
          </nav>
          <div className="flex items-center gap-1">
            <span className={cn("size-2 rounded-full", liveTone[live])} title={live} aria-label={`Stream ${live}`} />
            <Button type="button" variant="ghost" size="icon-sm" aria-label="Collapse panel"><PanelLeftClose /></Button>
            <Button type="button" variant="ghost" size="icon-sm" aria-label="New task"><Plus /></Button>
          </div>
        </header>

        <div className="bg-background flex items-center justify-between gap-4 border-b px-4 py-2">
          <div className="flex min-w-0 items-center gap-1">
            <code className="text-info truncate text-[0.78rem]" title={workspacePath}>{workspacePath}</code>
            <Button type="button" variant="ghost" size="icon-xs" aria-label="Copy path"><Copy /></Button>
            <Button type="button" variant="ghost" size="icon-xs" aria-label="Open folder"><Folder /></Button>
            <Button type="button" variant="ghost" size="icon-xs" aria-label="Split view"><Columns2 /></Button>
            <Button type="button" variant="ghost" size="icon-xs" aria-label="Open terminal"><Terminal /></Button>
          </div>
          <Badge variant="outline" className="text-info border-info shrink-0">{currentTask.coding_agent ?? "session"}</Badge>
        </div>

        {error ? <Alert role="alert" variant="destructive" className="mx-4 mt-3 w-auto"><AlertDescription>{error}</AlertDescription></Alert> : null}
        {offline ? <Alert role="alert" className="mx-4 mt-3 w-auto border-l-2 border-l-info"><AlertDescription>Daemon offline. Actions are disabled until it reconnects.</AlertDescription></Alert> : null}

        {(["completed", "aborted"] as string[]).includes(currentTask.state) || commands.some((command) => commandEnabled(command, currentTask.state)) ? (
          <div className="flex flex-wrap gap-2 border-b px-4 py-2.5" role="group" aria-label="Task commands">
            {commands.map((command) => commandEnabled(command, currentTask.state) ? (
              <Button key={`${daemonId}:${task.id}:${command}`} type="button" variant="outline" size="sm" className="uppercase" disabled={offline || pendingCommand !== null || pending} onClick={() => void sendCommand(command)}>{pendingCommand === command ? `${command}…` : command}</Button>
            ) : null)}
            {(["completed", "aborted"] as string[]).includes(currentTask.state) ? <Button type="button" variant="outline" size="sm" className="uppercase" disabled={offline || pending} onClick={() => void removeTask()}>{pending ? "Working…" : "delete"}</Button> : null}
          </div>
        ) : null}

        <div className="flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto px-4 pt-6 pb-8" ref={chatScroll}>
          {hiddenEventCount ? <p className="text-muted-foreground text-xs">{hiddenEventCount} older events hidden to keep this view responsive.</p> : null}
          {timelineBlocks.map((block) => {
            if (block.kind === "user") {
              const author = block.item.author ?? login;
              return (
                <article className="grid grid-cols-[1.9rem_minmax(0,1fr)] items-start gap-3" key={block.item.key}>
                  <Avatar className="size-8 rounded-md"><AvatarFallback className="rounded-md bg-gradient-to-br from-[#5b4b78] to-[#37506a] text-[0.62rem] font-semibold text-[#efe9ff]">{monogram(author)}</AvatarFallback></Avatar>
                  <div className="grid min-w-0 gap-1">
                    <div className="flex items-baseline gap-2">
                      <strong className="text-subtle text-[0.8rem] font-medium">{displayName(author)}</strong>
                      <Button type="button" variant="ghost" size="icon-xs" aria-label="Copy message" onClick={() => void navigator.clipboard?.writeText(block.item.text ?? "")}><Copy /></Button>
                      <time className="text-muted-foreground ml-auto text-[0.68rem]">{relativeTime(block.item.iso)}</time>
                    </div>
                    <p className="whitespace-pre-wrap break-words">{block.item.text}</p>
                  </div>
                </article>
              );
            }
            return (
              <Collapsible defaultOpen className="group/work grid gap-1" key={block.key}>
                <CollapsibleTrigger className="text-muted-foreground hover:text-subtle inline-flex items-center gap-2 justify-self-start px-1 text-[0.68rem] uppercase tracking-[0.08em]">
                  <ChevronRight className="size-3.5 transition-transform group-data-[state=open]/work:rotate-90" /><span>Worked for {formatSpan(block.span)}</span>
                </CollapsibleTrigger>
                <CollapsibleContent className="grid gap-5 pt-3 pb-1">
                  {block.items.map((item) => {
                    if (item.event && (item.role === "tool" || item.role === "event")) {
                      const event = item.event;
                      const failed = eventSuccess(event) === false;
                      const duration = eventDuration(event);
                      return (
                        <button className="hover:bg-secondary grid w-full grid-cols-[1.5rem_minmax(0,1fr)_auto] items-start gap-3 rounded-md px-1 py-0.5 text-left" key={item.key} type="button" aria-haspopup="dialog" onClick={() => setSelectedEvent(event)}>
                          <span className={cn("grid size-6 place-items-center", failed ? "text-destructive" : "text-subtle")} aria-hidden="true">
                            {item.role === "tool" ? <File className="size-4" /> : <i className="border-input grid size-5 place-items-center rounded-full border text-[0.6rem] not-italic">{eventIcon(event)}</i>}
                          </span>
                          <span className="grid min-w-0 gap-1">
                            <span className="flex min-w-0 items-center gap-2">
                              <strong className="text-foreground shrink-0 font-semibold">{eventTitle(event)}</strong>
                              {eventTarget(event) ? <span className="text-subtle truncate">{eventTarget(event)}</span> : null}
                              <ExternalLink className="text-muted-foreground size-3.5 shrink-0" />
                            </span>
                            {eventResultLine(event) ? <span className="text-muted-foreground flex min-w-0 gap-2 truncate text-[0.82rem]"><i aria-hidden="true" className="text-input not-italic">└</i>{eventResultLine(event)}</span> : null}
                          </span>
                          <span className="text-muted-foreground flex items-baseline gap-2 pt-0.5 text-[0.7rem] whitespace-nowrap">
                            {duration ? <span>{duration}</span> : null}
                            <time>{relativeTime(item.iso)}</time>
                          </span>
                        </button>
                      );
                    }
                    const isResponse = !!item.text && (item.text.includes("\n") || item.text.length > 160);
                    const content = (
                      <>
                        <span className={cn("grid size-6 place-items-center", item.role === "system" ? "text-warning" : "text-primary")} aria-hidden="true"><Brain className="size-4" /></span>
                        <p className={cn("m-0 min-w-0 break-words", item.role === "system" ? "text-warning" : isResponse ? "text-subtle whitespace-pre-wrap" : "text-muted-foreground italic")}>{item.text}</p>
                        <time className="text-muted-foreground pt-0.5 text-[0.7rem] whitespace-nowrap">{relativeTime(item.iso)}</time>
                      </>
                    );
                    if (!item.event) return <div className="grid w-full grid-cols-[1.5rem_minmax(0,1fr)_auto] items-start gap-3 px-1 py-0.5" key={item.key}>{content}</div>;
                    const source = item.event;
                    return (
                      <button className="hover:bg-secondary grid w-full grid-cols-[1.5rem_minmax(0,1fr)_auto] items-start gap-3 rounded-md px-1 py-0.5 text-left" key={item.key} type="button" aria-haspopup="dialog" onClick={() => setSelectedEvent(source)}>
                        {content}
                      </button>
                    );
                  })}
                </CollapsibleContent>
              </Collapsible>
            );
          })}
          {!timeline.length ? <p className="text-muted-foreground m-auto text-center">No messages yet. The stream stays open while this session is selected.</p> : null}
        </div>

        <form className="bg-background grid gap-2.5 border-t px-4 pt-3 pb-4" onSubmit={currentTask.state === "awaiting_plan_approval" ? revisePlan : submitMessage}>
          <div className="flex flex-wrap items-center gap-2.5">
            <Badge variant="outline" className={cn("border-current tracking-[0.07em]", stateTextClass(currentTask.state))}>{currentTask.state.replaceAll("_", " ").toUpperCase()}</Badge>
            <Button type="button" variant="outline" size="xs" className="text-primary">{modelLabel(currentTask.model)}<Pencil className="text-muted-foreground" /></Button>
            <Button type="button" variant="outline" size="xs" className="text-primary">{(currentTask.thinking ?? "medium").toUpperCase()}<Pencil className="text-muted-foreground" /></Button>
            <span className="text-muted-foreground text-[0.7rem]">{typeof currentTask.total_cost === "number" && currentTask.total_cost > 0 ? `$${currentTask.total_cost.toFixed(2)}` : "0 tokens"}</span>
            <div className="ml-auto">
              <Label className="sr-only" htmlFor="intervention-action">Intervention action</Label>
              <Select value={action} onValueChange={(value) => setAction(value as InterventionAction)} disabled={!canSend || currentTask.state === "awaiting_plan_approval"}>
                <SelectTrigger id="intervention-action" size="sm" className="text-xs"><SelectValue /></SelectTrigger>
                <SelectContent>{interventionChoices(currentTask.state, availableActions).map((item) => <SelectItem key={item} value={item}>{actionLabels[item]}</SelectItem>)}</SelectContent>
              </Select>
            </div>
          </div>
          <Textarea value={message} onChange={(event) => setMessage(event.target.value)} onKeyDown={(event) => { if ((event.metaKey || event.ctrlKey) && event.key === "Enter") { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} disabled={offline || pending || pendingCommand !== null} placeholder={currentTask.state === "awaiting_plan_approval" ? "Explain what the planner should revise…" : "ENTER to start typing…"} className="min-h-14" />
          <div className="flex items-center justify-between gap-4">
            <div className="flex items-center gap-0.5" aria-label="Session controls">
              <Button type="button" variant="ghost" size="icon-sm" className="text-destructive" disabled aria-label="Permissions"><Shield /></Button>
              <Button type="button" variant="ghost" size="icon-sm" disabled={!canSend || !commandEnabled("resume", currentTask.state)} aria-label="Resume" onClick={() => void sendCommand("resume")}><FastForward /></Button>
              <Button type="button" variant="ghost" size="icon-sm" disabled={!canSend || !commandEnabled("approve", currentTask.state)} aria-label="Approve" onClick={() => void sendCommand("approve")}><SkipForward /></Button>
              <Button type="button" variant="ghost" size="icon-sm" disabled={!canSend || !commandEnabled("abort", currentTask.state)} aria-label="Abort" onClick={() => void sendCommand("abort")}><Archive /></Button>
              <Button type="button" variant="ghost" size="icon-sm" disabled aria-label="Branches"><GitBranch /></Button>
              <Button type="button" variant="ghost" size="icon-sm" disabled aria-label="Files"><Folder /></Button>
              <Button type="button" variant="ghost" size="icon-sm" disabled aria-label="Collaborators"><Users /></Button>
            </div>
            <Button type="submit" variant="outline" size="sm" className="bg-secondary tracking-[0.05em]" disabled={offline || pending || pendingCommand !== null || (!message.trim() && action !== "retry")}>
              <Send className="text-primary" />{pending ? "SENDING…" : "SEND"}<kbd className="border-input text-subtle rounded-sm border px-1 text-[0.65rem]">⌘+ENTER</kbd>
            </Button>
          </div>
        </form>
      </div>

      <aside className="bg-card hidden min-w-0 flex-col overflow-hidden border-l lg:flex" aria-label="Session context">
        <header className="border-rail-line text-muted-foreground flex h-14 shrink-0 items-center gap-2 border-t-2 border-b px-3">
          <Monitor className="size-4 shrink-0" />
          <strong className="text-subtle truncate text-[0.78rem] font-medium" title={daemonName}>{daemonName}</strong>
          <Button type="button" variant="ghost" size="icon-sm" className="ml-auto" aria-label="Collapse sidebar"><Columns2 /></Button>
        </header>
        <Tabs defaultValue="graph" className="min-h-0 flex-1 gap-0 overflow-hidden">
          <TabsList variant="line" className="w-full justify-start gap-4 border-b px-3">
            <TabsTrigger value="graph" className="text-[0.68rem] uppercase tracking-[0.07em]">Graph</TabsTrigger>
            <TabsTrigger value="artifacts" className="text-[0.68rem] uppercase tracking-[0.07em]">Artifacts {artifactViews.length}</TabsTrigger>
            <TabsTrigger value="workspace" className="text-[0.68rem] uppercase tracking-[0.07em]">Workspace</TabsTrigger>
          </TabsList>
          <TabsContent value="artifacts" className="min-h-0 flex-1 overflow-y-auto p-3.5">
            <p className="text-muted-foreground mb-3 text-[0.74rem]"><strong className="text-subtle font-semibold">0</strong> src · <strong className="text-subtle font-semibold">{editCount}</strong> edit · <strong className="text-subtle font-semibold">{artifacts.length}</strong> new · <strong className="text-subtle font-semibold">{otherCount}</strong> other</p>
            <Label className="text-muted-foreground mb-4 flex items-center gap-2 text-[0.7rem] uppercase tracking-[0.06em]"><Switch className="h-4 w-7" />Grouped</Label>
            <p className="text-muted-foreground mb-2 text-[0.66rem] uppercase tracking-[0.07em]">Unreferenced ({artifactViews.length})</p>
            <ul className="grid gap-1.5">{artifactViews.map((artifact) => {
              const label = artifact.kind === "file" ? (artifact.subtitle.split("/").pop() || artifact.subtitle) : artifact.title;
              return (
                <li key={artifact.id}>
                  <button type="button" className="bg-background hover:bg-secondary aria-pressed:bg-secondary aria-pressed:border-input grid w-full grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 rounded-md border px-2 py-2 text-left" aria-pressed={selectedArtifact === artifact.id} onClick={() => { setSelectedArtifact(artifact.id); setArtifactMode("rendered"); setArtifactQuote(""); }}>
                    <File className="text-muted-foreground size-4" />
                    <span className="text-foreground truncate text-[0.78rem]">{label}</span>
                    <span className="text-muted-foreground text-[0.66rem]">···</span>
                  </button>
                </li>
              );
            })}</ul>
            {!artifactViews.length ? <p className="text-muted-foreground text-[0.78rem]">No artifacts yet.</p> : null}
            {selectedArtifactValue ? (
              <article className="border-primary mt-4 rounded-md border p-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <strong className="font-medium">{selectedArtifactValue.title}</strong>
                  <div className="flex gap-1.5">
                    <Button type="button" variant="outline" size="xs" aria-pressed={artifactMode === "rendered"} onClick={() => setArtifactMode("rendered")}>Rendered</Button>
                    <Button type="button" variant="outline" size="xs" aria-pressed={artifactMode === "raw"} onClick={() => setArtifactMode("raw")}>Raw</Button>
                    <Button type="button" variant="outline" size="xs" onClick={() => { setSelectedArtifact(null); setArtifactQuote(""); }}>Close</Button>
                  </div>
                </div>
                <pre className="text-muted-foreground mt-2.5 max-h-72 overflow-auto text-xs leading-relaxed break-words whitespace-pre-wrap" onMouseUp={() => setArtifactQuote(window.getSelection()?.toString().trim() ?? "")}>{artifactMode === "rendered" ? renderedArtifactContent(selectedArtifactValue) : selectedArtifactValue.content}</pre>
                {artifactQuote ? (
                  <div className="text-muted-foreground mt-2.5 flex items-center justify-between gap-3 border-t pt-2.5 text-xs">
                    <span className="truncate">“{artifactQuote.slice(0, 60)}{artifactQuote.length > 60 ? "…" : ""}”</span>
                    <Button type="button" variant="outline" size="xs" onClick={() => { setAction("comment"); setMessage((current) => current ? `${current}\nRegarding “${artifactQuote}”` : `Regarding “${artifactQuote}”\n`); }}>Comment</Button>
                  </div>
                ) : null}
              </article>
            ) : null}
          </TabsContent>
          <TabsContent value="workspace" className="min-h-0 flex-1 overflow-y-auto p-3.5">
            <dl className="grid gap-2.5">
              <div className="grid gap-0.5 border-t pt-2"><dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">Workspace</dt><dd className="text-subtle truncate text-[0.78rem]" title={workspacePath}>{workspacePath}</dd></div>
              <div className="grid gap-0.5 border-t pt-2"><dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">State</dt><dd className="text-subtle truncate text-[0.78rem]">{currentTask.state}</dd></div>
              <div className="grid gap-0.5 border-t pt-2"><dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">Branch</dt><dd className="text-subtle truncate text-[0.78rem]">{selectedBranch?.id?.slice(0, 8) ?? "-"} · head {selectedBranch?.head_attempt_id?.slice(0, 8) ?? "-"}</dd></div>
              <div className="grid gap-0.5 border-t pt-2"><dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">Attempt</dt><dd className="text-subtle truncate text-[0.78rem]">{attempts.at(-1)?.name ?? "not started"}</dd></div>
              <div className="grid gap-0.5 border-t pt-2"><dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">Checks</dt><dd className="text-subtle truncate text-[0.78rem]">{checks.filter((check) => check.status === "passed").length}/{checks.length} passed</dd></div>
              <div className="grid gap-0.5 border-t pt-2"><dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">Sessions</dt><dd className="text-subtle truncate text-[0.78rem]">{sessions.length}</dd></div>
            </dl>
            {branches.length > 1 ? (
              <div className="mt-4 grid gap-1.5">
                <Label htmlFor="branch">Branch</Label>
                <Select value={selectedBranch?.id ?? ""} disabled={offline || pending} onValueChange={(value) => { setSelectedBranchId(value); setSelectedAttempt(null); }}>
                  <SelectTrigger id="branch" size="sm" className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>{branches.map((branch) => <SelectItem key={branch.id} value={branch.id}>{branch.id.slice(0, 8)} · {branch.status}</SelectItem>)}</SelectContent>
                </Select>
              </div>
            ) : null}
            {repositories.length ? (
              <div className="mt-4 flex flex-wrap gap-2">
                {repositories.map((repository) => (
                  <span key={repository.id} className="grid gap-0.5 rounded-md border px-2 py-1.5">
                    <strong className="text-[0.75rem] font-medium">{repository.primary ? "◆" : "◇"} {repository.name}</strong>
                    <small className="text-muted-foreground text-[0.7rem]">{repository.source_type}</small>
                  </span>
                ))}
              </div>
            ) : null}
          </TabsContent>
          <TabsContent value="graph" className="min-h-0 flex-1 overflow-y-auto p-3.5">
            <AttemptGraph attempts={attempts} branchId={selectedBranch?.id} selectedId={selectedAttempt} onSelect={setSelectedAttempt} />
          </TabsContent>
        </Tabs>
      </aside>

      <EventDialog event={selectedEvent} onClose={() => setSelectedEvent(null)} />
    </section>
  );
}
