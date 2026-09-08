"use client";

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
} from "../client/daemon-api.ts";
import { qualifiedEventKey, relativeTime, RequestScope, statePresentation } from "../client/daemon-ui-state.ts";
import { IconArchive, IconBranch, IconBrain, IconChevron, IconCollapse, IconCopy, IconExternalLink, IconFastForward, IconFile, IconFolder, IconMonitor, IconPencil, IconPeople, IconPlus, IconSend, IconShield, IconSkip, IconSplit, IconTerminal } from "./icons.tsx";

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
const transientEventTypes = new Set(["message_start", "message_update", "tool_execution_update"]);
const outputKeys = ["result", "output", "text", "message", "error"];
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

function readable(value: unknown): string {
  if (typeof value === "string") return value;
  try { return JSON.stringify(value, null, 2); } catch { return String(value); }
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

function payloadRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function eventResult(event: TaskEvent): string {
  const payload = payloadRecord(event.payload);
  for (const key of outputKeys) {
    const value = payload[key];
    if (typeof value === "string") {
      if (value.trim()) return value;
      continue;
    }
    if (key === "message") {
      const message = payloadRecord(value);
      if (typeof message.content === "string" && message.content.trim()) return message.content;
      if (Array.isArray(message.content)) {
        const text = message.content.map((part) => payloadRecord(part).text).filter((part): part is string => typeof part === "string").join("\n");
        if (text.trim()) return text;
      }
      const errorText = message.errorMessage ?? message.error;
      if (typeof errorText === "string" && errorText.trim()) return errorText;
      continue;
    }
    if (value !== undefined && value !== null) return readable(value);
  }
  return "";
}

function eventTitle(event: TaskEvent): string {
  if (event.type === "tool_call") {
    const raw = String(payloadRecord(event.payload).tool ?? event.name ?? "Tool");
    const knownNames: Record<string, string> = { apply_patch: "Edit", bash: "Bash", edit: "Edit", glob: "Files", grep: "Search", read: "Read", web_fetch: "Web Fetch", webfetch: "Web Fetch", write: "Write" };
    return knownNames[raw.toLowerCase()] ?? raw.replaceAll("_", " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
  }
  return ({ message_end: "Agent response", phase_end: "Attempt finished", phase_start: "Attempt started", process_end: "Agent process finished", process_start: "Agent process started" } as Record<string, string>)[event.type] ?? event.name ?? event.type.replaceAll("_", " ");
}

function eventArguments(event: TaskEvent): Record<string, unknown> {
  const payload = payloadRecord(event.payload);
  const value = payload.arguments ?? payload.args;
  if (value && typeof value === "object" && !Array.isArray(value)) return value as Record<string, unknown>;
  if (typeof value === "string") {
    try {
      const parsed = JSON.parse(value) as unknown;
      return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {};
    } catch {
      return {};
    }
  }
  return {};
}

function eventTarget(event: TaskEvent): string {
  const payload = payloadRecord(event.payload);
  const argumentsRecord = eventArguments(event);
  for (const key of ["file_path", "path", "url", "command", "pattern", "query", "label"]) {
    const value = argumentsRecord[key] ?? payload[key];
    if (typeof value === "string" && value.trim()) return value;
  }
  return event.type.startsWith("phase_") ? event.name ?? "" : "";
}

function eventSuccess(event: TaskEvent): boolean | undefined {
  const payload = payloadRecord(event.payload);
  if (typeof payload.success === "boolean") return payload.success;
  if (typeof payload.exit_code === "number") return payload.exit_code === 0;
  if (typeof payload.status === "string") {
    if (["failed", "error", "aborted"].includes(payload.status)) return false;
    if (["passed", "completed", "success"].includes(payload.status)) return true;
  }
  if (event.type.includes("error")) return false;
  return undefined;
}

function eventStartedAt(event: TaskEvent): Date {
  const startedAt = payloadRecord(event.payload).started_at;
  return new Date(typeof startedAt === "string" ? startedAt : event.started_at);
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

type ChatItem = { key: string; role: "user" | "agent" | "system" | "tool"; author?: string; text?: string; at: number; iso: string; event?: TaskEvent };

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
    if (errored) items.push({ key: `ev-${event.sequence}`, role: "system", text: eventTitle(event), at: at.getTime() || 0, iso, event });
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

function eventResultLine(event: TaskEvent): string {
  const result = eventResult(event);
  if (!result.trim()) return "";
  const lines = result.split("\n");
  const first = lines.find((line) => line.trim())?.trim() ?? "";
  const clipped = first.length > 140 ? `${first.slice(0, 140)}...` : first;
  return lines.length > 1 ? `${clipped} ... (${lines.length} lines)` : clipped;
}

function eventToolName(event: TaskEvent): string {
  return String(payloadRecord(event.payload).tool ?? event.name ?? event.type).toLowerCase();
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
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(new Set());
  const [autoScroll, setAutoScroll] = useState(true);
  const [events, setEvents] = useState<TaskEvent[]>([]);
  const [availableActions, setAvailableActions] = useState<string[]>([]);
  const [cursor, setCursor] = useState<number | undefined>(undefined);
  const [live, setLive] = useState<"connecting" | "live" | "reconnecting" | "offline">("connecting");
  const [error, setError] = useState<string | null>(null);
  const [pendingCommand, setPendingCommand] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [message, setMessage] = useState("");
  const [action, setAction] = useState<InterventionAction>("comment");
  const [sidebarTab, setSidebarTab] = useState<"artifacts" | "workspace" | "minimap">("artifacts");
  const chatScroll = useRef<HTMLDivElement | null>(null);
  const scope = useRef(new RequestScope());
  const mutationScope = useRef(new RequestScope());
  const mutationController = useRef<AbortController | null>(null);
  const eventDialog = useRef<HTMLElement | null>(null);
  const eventScroll = useRef<HTMLDivElement | null>(null);
  const eventTrigger = useRef<HTMLButtonElement | null>(null);
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
  const meaningfulEvents = events.filter((event) => !transientEventTypes.has(event.type) && (!selectedAttempt || event.attempt_id === selectedAttempt || event.phase_id === selectedAttempt));
  const visibleEvents = meaningfulEvents.slice(-visibleEventLimit);
  const hiddenEventCount = Math.max(0, meaningfulEvents.length - visibleEvents.length);
  const timeline = buildTimeline(currentTask.request, currentTask.created_at, login, interventions, visibleEvents);
  const timelineBlocks = groupTimeline(timeline);
  const editCount = diff.repositories.reduce((sum, repository) => sum + repository.files.length, 0);
  const otherCount = results.length + checks.length + diff.repositories.length;
  const workspacePath = currentTask.workspace_path ?? "Daemon sandbox";
  const canSend = !offline && !pending && pendingCommand === null;

  function toggleGroup(key: string) {
    setCollapsedGroups((previous) => {
      const next = new Set(previous);
      if (next.has(key)) next.delete(key); else next.add(key);
      return next;
    });
  }

  function closeEvent() {
    setSelectedEvent(null);
    eventTrigger.current?.focus();
  }

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
    if (!selectedEvent) return;
    eventDialog.current?.focus();
    function handleDialogKeyboard(event: KeyboardEvent) {
      if (event.key === "Escape" || event.key === "i") {
        event.preventDefault();
        setSelectedEvent(null);
        eventTrigger.current?.focus();
        return;
      }
      if (event.key === "j" || event.key === "ArrowDown") { event.preventDefault(); eventScroll.current?.scrollBy({ top: 64 }); return; }
      if (event.key === "k" || event.key === "ArrowUp") { event.preventDefault(); eventScroll.current?.scrollBy({ top: -64 }); return; }
      if (event.key === "Tab" && eventDialog.current) {
        const focusable = Array.from(eventDialog.current.querySelectorAll<HTMLElement>("button, [href], input, select, textarea, [tabindex]:not([tabindex='-1'])")).filter((element) => !element.hasAttribute("disabled"));
        const first = focusable[0];
        const last = focusable.at(-1);
        if (!first || !last) return;
        if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
      }
    }
    document.addEventListener("keydown", handleDialogKeyboard);
    return () => document.removeEventListener("keydown", handleDialogKeyboard);
  }, [selectedEvent]);

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
    <section className="chat-view" aria-label={`Session ${task.id} on ${daemonName}`}>
      <div className="chat-main">
        <header className="page-topbar">
          <nav className="crumbs" aria-label="Breadcrumb">
            <button type="button" onClick={onOpenTask}>Tasks</button><i>›</i>
            <button type="button" onClick={onOpenTask}>{rootTask.request}</button><i>›</i>
            <strong>{currentTask.request}</strong>
            <button type="button" className="icon-button" aria-label="Rename session"><IconPencil /></button>
          </nav>
          <div className="topbar-tools"><span className="live-dot" data-live={live} title={live} aria-label={`Stream ${live}`} /><button type="button" className="icon-button" aria-label="Collapse panel"><IconCollapse /></button><button type="button" className="icon-button" aria-label="New task"><IconPlus /></button></div>
        </header>

        <div className="chat-subbar">
          <div className="path-crumb"><code title={workspacePath}>{workspacePath}</code><button type="button" className="icon-button" aria-label="Copy path"><IconCopy /></button><button type="button" className="icon-button" aria-label="Open folder"><IconFolder /></button><button type="button" className="icon-button" aria-label="Split view"><IconSplit /></button><button type="button" className="icon-button" aria-label="Open terminal"><IconTerminal /></button></div>
          <span className="tag accent">{currentTask.coding_agent ?? "session"}</span>
        </div>

        {error ? <p role="alert" className="notice chat-notice">{error}</p> : null}
        {offline ? <p role="alert" className="notice chat-notice">Daemon offline. Actions are disabled until it reconnects.</p> : null}

        {(["completed", "aborted"] as string[]).includes(currentTask.state) || commands.some((command) => commandEnabled(command, currentTask.state)) ? (
          <div className="chat-commands" role="group" aria-label="Task commands">
            {commands.map((command) => commandEnabled(command, currentTask.state) ? (
              <button key={`${daemonId}:${task.id}:${command}`} type="button" disabled={offline || pendingCommand !== null || pending} onClick={() => void sendCommand(command)}>{pendingCommand === command ? `${command}…` : command}</button>
            ) : null)}
            {(["completed", "aborted"] as string[]).includes(currentTask.state) ? <button type="button" disabled={offline || pending} onClick={() => void removeTask()}>{pending ? "Working…" : "delete"}</button> : null}
          </div>
        ) : null}

        <div className="chat-scroll" ref={chatScroll}>
          {hiddenEventCount ? <p className="hint chat-hint">{hiddenEventCount} older events hidden to keep this view responsive.</p> : null}
          {timelineBlocks.map((block) => {
            if (block.kind === "user") {
              const author = block.item.author ?? login;
              return (
                <article className="msg msg-user" key={block.item.key}>
                  <span className="avatar" aria-hidden="true">{monogram(author)}</span>
                  <div className="msg-body">
                    <div className="msg-head"><strong>{displayName(author)}</strong><button type="button" className="icon-button msg-copy" aria-label="Copy message" onClick={() => void navigator.clipboard?.writeText(block.item.text ?? "")}><IconCopy /></button><time>{relativeTime(block.item.iso)}</time></div>
                    <p className="msg-text">{block.item.text}</p>
                  </div>
                </article>
              );
            }
            const collapsed = collapsedGroups.has(block.key);
            return (
              <section className="work-group" key={block.key}>
                <button type="button" className="work-group-head" aria-expanded={!collapsed} onClick={() => toggleGroup(block.key)}>
                  <IconChevron /><span>Worked for {formatSpan(block.span)}</span>
                </button>
                {collapsed ? null : (
                  <div className="work-items">
                    {block.items.map((item) => {
                      if (item.role === "tool" && item.event) {
                        const event = item.event;
                        return (
                          <button className="work-row work-tool" key={item.key} type="button" aria-haspopup="dialog" onClick={(clickEvent) => { eventTrigger.current = clickEvent.currentTarget; setSelectedEvent(event); }}>
                            <span className="work-icon" data-success={eventSuccess(event)} aria-hidden="true"><IconFile /></span>
                            <span className="work-body">
                              <span className="work-title"><strong>{eventTitle(event)}</strong>{eventTarget(event) ? <span className="work-path">{eventTarget(event)}</span> : null}<IconExternalLink className="work-ext" /></span>
                              {eventResultLine(event) ? <span className="work-preview"><i aria-hidden="true">└</i>{eventResultLine(event)}</span> : null}
                            </span>
                            <time className="work-time">{relativeTime(item.iso)}</time>
                          </button>
                        );
                      }
                      const isResponse = !!item.text && (item.text.includes("\n") || item.text.length > 160);
                      return (
                        <div className={`work-row work-think${item.role === "system" ? " work-error" : ""}`} key={item.key}>
                          <span className="work-icon think" aria-hidden="true"><IconBrain /></span>
                          <p className={`work-think-text${isResponse ? " is-response" : ""}`}>{item.text}</p>
                          <time className="work-time">{relativeTime(item.iso)}</time>
                        </div>
                      );
                    })}
                  </div>
                )}
              </section>
            );
          })}
          {!timeline.length ? <p className="chat-empty">No messages yet. The stream stays open while this session is selected.</p> : null}
        </div>

        <form className="composer" onSubmit={currentTask.state === "awaiting_plan_approval" ? revisePlan : submitMessage}>
          <div className="composer-status">
            <span className="status-pill" data-state={statePresentation(currentTask.state)}>{currentTask.state.replaceAll("_", " ").toUpperCase()}</span>
            <button type="button" className="chip"><span>{modelLabel(currentTask.model)}</span><IconPencil /></button>
            <button type="button" className="chip"><span>{(currentTask.thinking ?? "medium").toUpperCase()}</span><IconPencil /></button>
            <span className="token-meter">{typeof currentTask.total_cost === "number" && currentTask.total_cost > 0 ? `$${currentTask.total_cost.toFixed(2)}` : "0 tokens"}</span>
            <label className="composer-action"><span className="sr-only">Intervention action</span><select value={action} onChange={(event) => setAction(event.target.value as InterventionAction)} disabled={!canSend || currentTask.state === "awaiting_plan_approval"}>{interventionChoices(currentTask.state, availableActions).map((item) => <option key={item} value={item}>{actionLabels[item]}</option>)}</select></label>
          </div>
          <textarea value={message} onChange={(event) => setMessage(event.target.value)} onKeyDown={(event) => { if ((event.metaKey || event.ctrlKey) && event.key === "Enter") { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} disabled={offline || pending || pendingCommand !== null} placeholder={currentTask.state === "awaiting_plan_approval" ? "Explain what the planner should revise…" : "ENTER to start typing…"} />
          <div className="composer-bar">
            <div className="composer-icons" aria-label="Session controls">
              <button type="button" className="icon-button" disabled aria-label="Permissions"><IconShield /></button>
              <button type="button" className="icon-button" disabled={!canSend || !commandEnabled("resume", currentTask.state)} aria-label="Resume" onClick={() => void sendCommand("resume")}><IconFastForward /></button>
              <button type="button" className="icon-button" disabled={!canSend || !commandEnabled("approve", currentTask.state)} aria-label="Approve" onClick={() => void sendCommand("approve")}><IconSkip /></button>
              <button type="button" className="icon-button" disabled={!canSend || !commandEnabled("abort", currentTask.state)} aria-label="Abort" onClick={() => void sendCommand("abort")}><IconArchive /></button>
              <button type="button" className="icon-button" disabled aria-label="Branches"><IconBranch /></button>
              <button type="button" className="icon-button" disabled aria-label="Files"><IconFolder /></button>
              <button type="button" className="icon-button" disabled aria-label="Collaborators"><IconPeople /></button>
            </div>
            <button type="submit" className="send-button" disabled={offline || pending || pendingCommand !== null || (!message.trim() && action !== "retry")}><IconSend />{pending ? "SENDING…" : "SEND"}<kbd>⌘+ENTER</kbd></button>
          </div>
        </form>
      </div>

      <aside className="context-sidebar" aria-label="Session context">
        <header className="sidebar-head"><IconMonitor /><strong title={daemonName}>{daemonName}</strong><button type="button" className="icon-button" aria-label="Collapse sidebar"><IconSplit /></button></header>
        <div className="sidebar-tabs" role="tablist">
          <button type="button" role="tab" aria-selected={sidebarTab === "artifacts"} onClick={() => setSidebarTab("artifacts")}>Artifacts</button>
          <button type="button" role="tab" aria-selected={sidebarTab === "workspace"} onClick={() => setSidebarTab("workspace")}>Workspace</button>
          <button type="button" role="tab" aria-selected={sidebarTab === "minimap"} onClick={() => setSidebarTab("minimap")}>Minimap</button>
        </div>
        {sidebarTab === "artifacts" ? (
          <div className="sidebar-body">
            <p className="artifact-counts"><strong>0</strong> src · <strong>{editCount}</strong> edit · <strong>{artifacts.length}</strong> new · <strong>{otherCount}</strong> other</p>
            <label className="grouped-toggle"><span className="switch" aria-hidden="true" /> Grouped</label>
            <p className="group-label">Unreferenced ({artifactViews.length})</p>
            <ul className="artifact-cards">{artifactViews.map((artifact) => {
              const label = artifact.kind === "file" ? (artifact.subtitle.split("/").pop() || artifact.subtitle) : artifact.title;
              return <li key={artifact.id}><button type="button" aria-pressed={selectedArtifact === artifact.id} onClick={() => { setSelectedArtifact(artifact.id); setArtifactMode("rendered"); setArtifactQuote(""); }}><IconFile /><span className="artifact-name">{label}</span><span className="chip-count">0</span><span className="dots" aria-hidden="true">···</span></button></li>;
            })}</ul>
            {!artifactViews.length ? <p className="sidebar-empty">No artifacts yet.</p> : null}
            {selectedArtifactValue ? <article className="artifact-preview"><div className="section-heading"><strong>{selectedArtifactValue.title}</strong><div className="actions"><button type="button" aria-pressed={artifactMode === "rendered"} onClick={() => setArtifactMode("rendered")}>Rendered</button><button type="button" aria-pressed={artifactMode === "raw"} onClick={() => setArtifactMode("raw")}>Raw</button><button type="button" onClick={() => { setSelectedArtifact(null); setArtifactQuote(""); }}>Close</button></div></div><pre onMouseUp={() => setArtifactQuote(window.getSelection()?.toString().trim() ?? "")}>{artifactMode === "rendered" ? renderedArtifactContent(selectedArtifactValue) : selectedArtifactValue.content}</pre>{artifactQuote ? <div className="selection-action"><span>“{artifactQuote.slice(0, 60)}{artifactQuote.length > 60 ? "…" : ""}”</span><button type="button" onClick={() => { setAction("comment"); setMessage((current) => current ? `${current}\nRegarding “${artifactQuote}”` : `Regarding “${artifactQuote}”\n`); }}>Comment</button></div> : null}</article> : null}
          </div>
        ) : sidebarTab === "workspace" ? (
          <div className="sidebar-body">
            <dl className="sidebar-facts">
              <div><dt>Workspace</dt><dd title={workspacePath}>{workspacePath}</dd></div>
              <div><dt>State</dt><dd>{currentTask.state}</dd></div>
              <div><dt>Branch</dt><dd>{selectedBranch?.id?.slice(0, 8) ?? "-"} · head {selectedBranch?.head_attempt_id?.slice(0, 8) ?? "-"}</dd></div>
              <div><dt>Attempt</dt><dd>{attempts.at(-1)?.name ?? "not started"}</dd></div>
              <div><dt>Checks</dt><dd>{checks.filter((check) => check.status === "passed").length}/{checks.length} passed</dd></div>
            </dl>
            {branches.length > 1 ? <label className="branch-select">Branch<select value={selectedBranch?.id ?? ""} disabled={offline || pending} onChange={(event) => { setSelectedBranchId(event.target.value); setSelectedAttempt(null); }}>{branches.map((branch) => <option key={branch.id} value={branch.id}>{branch.id.slice(0, 8)} · {branch.status}</option>)}</select></label> : null}
            {repositories.length ? <div className="repository-chips">{repositories.map((repository) => <span key={repository.id}><strong>{repository.primary ? "◆" : "◇"} {repository.name}</strong><small>{repository.source_type}</small></span>)}</div> : null}
          </div>
        ) : (
          <div className="sidebar-body"><p className="sidebar-empty">Minimap is not available yet.</p></div>
        )}
      </aside>

      {selectedEvent ? (() => {
        const params = eventArguments(selectedEvent);
        const paramsText = Object.keys(params).length ? JSON.stringify(params, null, 2) : "";
        const result = eventResult(selectedEvent);
        const fallback = !paramsText && !result ? readable(selectedEvent.payload) : "";
        return (
          <div className="event-modal-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget) closeEvent(); }}>
            <section ref={eventDialog} className="event-modal" role="dialog" aria-modal="true" aria-label={`${eventTitle(selectedEvent)} event details`} tabIndex={-1}>
              <header className="event-modal-head">
                <IconFile />
                <strong>{eventToolName(selectedEvent)}</strong>
                {eventTarget(selectedEvent) ? <code className="event-modal-path">{eventTarget(selectedEvent)}</code> : null}
                <button type="button" className="event-modal-close" aria-label="Close event details" onClick={closeEvent}>×</button>
              </header>
              <div className="event-modal-body" ref={eventScroll}>
                {paramsText ? <pre className="event-params">{paramsText}</pre> : null}
                {result ? <><p className="event-result-label">Result</p><pre className="event-result-body">{result}</pre></> : null}
                {fallback ? <pre className="event-result-body">{fallback}</pre> : null}
              </div>
              <footer className="event-modal-foot"><span>j/k or ↓/↑ to scroll</span><span>i or ESC to close</span></footer>
            </section>
          </div>
        );
      })() : null}
    </section>
  );
}
