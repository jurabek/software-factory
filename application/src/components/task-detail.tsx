"use client";

import { useEffect, useRef, useState } from "react";
import {
  daemonArtifacts,
  daemonAttempts,
  daemonBranches,
  daemonChecks,
  daemonCommand,
  daemonCreateSession,
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
import { qualifiedEventKey, RequestScope } from "../client/daemon-ui-state.ts";

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

function artifactContent(artifact: TaskArtifact): string {
  return `This artifact remains on the daemon sandbox.\n\nPath: ${artifact.path}\nDigest: ${artifact.digest}`;
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

export function TaskDetail({ daemonId, daemonName, task, offline, onChanged, onSelectTask, onRemoved }: {
  daemonId: string;
  daemonName: string;
  task: QualifiedTask;
  offline: boolean;
  onChanged: () => Promise<void> | void;
  onSelectTask: (taskId: string) => void;
  onRemoved: () => void;
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
  const [expandedEvent, setExpandedEvent] = useState<number | null>(null);
  const [events, setEvents] = useState<TaskEvent[]>([]);
  const [availableActions, setAvailableActions] = useState<string[]>([]);
  const [cursor, setCursor] = useState<number | undefined>(undefined);
  const [live, setLive] = useState<"connecting" | "live" | "reconnecting" | "offline">("connecting");
  const [error, setError] = useState<string | null>(null);
  const [pendingCommand, setPendingCommand] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [sessionRequest, setSessionRequest] = useState("");
  const [message, setMessage] = useState("");
  const [action, setAction] = useState<InterventionAction>("comment");
  const scope = useRef(new RequestScope());
  const cursorRef = useRef<number | undefined>(undefined);
  const seen = useRef(new Set<string>());

  const currentTask = details ?? task;
  const rootTaskId = currentTask.parent_task_id ?? currentTask.id;
  const repositories = (currentTask.repositories ?? []).filter(isTaskRepository);
  const selectedBranch = branches.find((branch) => branch.id === selectedBranchId) ?? branches.find((branch) => branch.id === currentTask.selected_branch_id) ?? branches[0];
  const selectedArtifactValue = artifacts.find((artifact) => artifact.id === selectedArtifact) ?? null;

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
    setExpandedEvent(null);
    setError(null);
    const handleFailure = (failure: unknown) => {
      if (controller.signal.aborted || !scope.current.isCurrent(current)) return;
      setError(failure instanceof Error ? failure.message : "Could not load task details.");
    };
    void refreshDetails(controller.signal).catch(handleFailure);
    const refreshTimer = setInterval(() => void refreshDetails(controller.signal).catch(handleFailure), 5_000);
    return () => { clearInterval(refreshTimer); controller.abort(); };
  }, [daemonId, task.id]);

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
      setLive(retry ? "reconnecting" : "connecting");
      openTaskStream(daemonId, task.id, from, controller.signal, (event) => {
        if (!scope.current.isCurrent(current)) return;
        setLive("live");
        append([event]);
      }, () => {
        if (!scope.current.isCurrent(current) || controller.signal.aborted) return;
        setLive("reconnecting");
        reconnectTimer = setTimeout(() => connect(cursorRef.current, true), 2_000);
      }, () => setLive("live"));
    }

    void daemonEvents(daemonId, task.id, { tail: 100 }, controller.signal)
      .then((result) => {
        if (!scope.current.isCurrent(current)) return;
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
        setLive("offline");
      });
    return () => {
      streamCleanup = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      controller.abort();
    };
  }, [daemonId, task.id]);

  async function sendCommand(command: (typeof commands)[number]) {
    setPendingCommand(command);
    setError(null);
    try {
      await daemonCommand(daemonId, task.id, command);
      await refreshDetails();
      await onChanged();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Command failed.");
    } finally {
      setPendingCommand((current) => current === command ? null : current);
    }
  }

  async function createSession(event: React.FormEvent) {
    event.preventDefault();
    if (!sessionRequest.trim()) return;
    setPending(true);
    setError(null);
    try {
      const result = await daemonCreateSession(daemonId, rootTaskId, sessionRequest.trim());
      setSessionRequest("");
      await refreshDetails();
      await onChanged();
      onSelectTask(result.session.id);
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not create session.");
    } finally {
      setPending(false);
    }
  }

  async function submitMessage(event: React.FormEvent) {
    event.preventDefault();
    if (!message.trim() && action !== "retry") return;
    setPending(true);
    setError(null);
    try {
      if (action === "comment") {
        await daemonIntervene(daemonId, task.id, { target: selectedAttempt ? { attempt_id: selectedAttempt } : {}, intent: "comment", message: message.trim(), idempotency_key: crypto.randomUUID() });
      } else {
        const result = await daemonIntervene(daemonId, task.id, {
          target: selectedAttempt ? { attempt_id: selectedAttempt } : {},
          intent: action,
          message: message.trim(),
          ...(selectedBranch?.head_attempt_id ? { expected_branch_head: selectedBranch.head_attempt_id } : {}),
          idempotency_key: crypto.randomUUID(),
        });
        if (result.result.branch_id) setSelectedBranchId(result.result.branch_id);
        if (result.result.attempt_id) setSelectedAttempt(result.result.attempt_id);
      }
      setMessage("");
      await refreshDetails();
      await onChanged();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not send intervention.");
    } finally {
      setPending(false);
    }
  }

  async function revisePlan(event: React.FormEvent) {
    event.preventDefault();
    if (!message.trim()) return;
    setPending(true);
    setError(null);
    try {
      await daemonFeedback(daemonId, task.id, message.trim(), currentTask.plan_digest);
      setMessage("");
      await refreshDetails();
      await onChanged();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not send feedback.");
    } finally {
      setPending(false);
    }
  }

  async function removeTask() {
    if (!window.confirm("Delete this task and its daemon-owned files?")) return;
    setPending(true);
    try {
      await daemonRemoveTask(daemonId, task.id);
      await onChanged();
      onRemoved();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not delete the task.");
    } finally {
      setPending(false);
    }
  }

  return (
    <section className="panel task-detail" aria-label={`Task ${task.id} on ${daemonName}`}>
      <div className="section-heading">
        <div>
          <p className="eyebrow">Task workspace · {daemonName}</p>
          <h3><code>{currentTask.id}</code> · {currentTask.state}</h3>
        </div>
        <span className="badge" data-state={live === "live" ? "configured" : "pending"}>{live}</span>
      </div>
      <p><strong>{currentTask.request}</strong></p>
      {error ? <p role="alert" className="notice">{error}</p> : null}
      {offline ? <p role="alert" className="notice">Daemon offline. Actions are disabled until it reconnects.</p> : null}
      <div className="actions" role="group" aria-label="Task commands">
        {commands.map((command) => (
          <button key={`${daemonId}:${task.id}:${command}`} type="button" disabled={offline || !commandEnabled(command, currentTask.state) || pendingCommand !== null || pending} onClick={() => void sendCommand(command)}>
            {pendingCommand === command ? `${command}...` : command}
          </button>
        ))}
        {(["completed", "aborted"] as string[]).includes(currentTask.state) ? <button type="button" disabled={offline || pending} onClick={() => void removeTask()}>{pending ? "Working..." : "Delete"}</button> : null}
      </div>

      <dl className="task-facts">
        <div><dt>Workspace</dt><dd>{currentTask.workspace_path ?? "Daemon sandbox"}</dd></div>
        <div><dt>Repositories</dt><dd>{repositories.length}</dd></div>
        <div><dt>Branch</dt><dd>{selectedBranch?.id?.slice(0, 8) ?? "-"} · head {selectedBranch?.head_attempt_id?.slice(0, 8) ?? "-"}</dd></div>
        <div><dt>Current attempt</dt><dd>{attempts.at(-1)?.name ?? "not started"}</dd></div>
        <div><dt>Checks</dt><dd>{checks.filter((check) => check.status === "passed").length}/{checks.length}</dd></div>
      </dl>
      {repositories.length ? <div className="repository-chips" aria-label="Task repositories">{repositories.map((repository) => <span key={repository.id}><strong>{repository.primary ? "◆" : "◇"} {repository.name}</strong><small>{repository.source_type}</small></span>)}</div> : null}

      {branches.length > 1 ? <label className="branch-select">Branch<select value={selectedBranch?.id ?? ""} disabled={offline || pending} onChange={(event) => setSelectedBranchId(event.target.value)}>{branches.map((branch) => <option key={branch.id} value={branch.id}>{branch.id.slice(0, 8)} · {branch.status}</option>)}</select></label> : null}

      <form className="inline-form" onSubmit={createSession}>
        <label>New session<input value={sessionRequest} onChange={(event) => setSessionRequest(event.target.value)} placeholder="Follow up on this task" /></label>
        <button type="submit" disabled={offline || pending || !sessionRequest.trim()}>Create session</button>
      </form>
      {sessions.length > 1 ? <div className="session-links"><span className="hint">Sessions</span>{sessions.map((session) => <button key={session.id} type="button" className={session.id === task.id ? "selected" : undefined} onClick={() => onSelectTask(session.id)}>{session.id.slice(0, 8)} · {session.state}</button>)}</div> : null}

      <section className="detail-section" aria-labelledby="attempts-heading">
        <div className="section-heading"><h3 id="attempts-heading">Attempts and branches</h3><span className="badge">{attempts.length} attempts · {branches.length} branches</span></div>
        {attempts.length ? <ul className="attempt-list">{attempts.map((attempt) => <li key={attempt.id} className={selectedAttempt === attempt.id ? "selected" : undefined}><button type="button" onClick={() => setSelectedAttempt((current) => current === attempt.id ? null : attempt.id)}><strong>{attempt.name}</strong><span>{attempt.status}</span><small>{attempt.owner} · {attempt.error ?? attempt.description ?? "No evidence"}</small></button></li>)}</ul> : <p>No attempts yet. Start the task to create its first attempt.</p>}
      </section>

      <section className="detail-section" aria-labelledby="evidence-heading">
        <div className="section-heading"><h3 id="evidence-heading">Evidence</h3><span className="badge">{results.length + checks.length + artifacts.length} items</span></div>
        {results.map((result) => <article className="evidence-card" key={result.id}><strong>{result.agent_role} result</strong><small>attempt {result.attempt}</small><pre>{readable(result.payload)}</pre></article>)}
        {checks.map((check) => <article className="evidence-card" key={`check-${check.id}`}><strong>{check.name}</strong><small>{check.status} · {check.duration_ms}ms</small><pre>{check.output || check.command}</pre></article>)}
        {diff.repositories.map((repository) => <article className="evidence-card" key={`diff-${repository.repository_id}`}><strong>{repository.name} diff</strong><small>{repository.files.length} files</small><pre>{repository.patch || "No changes"}</pre></article>)}
        {artifacts.map((artifact) => <button className="artifact-link" key={artifact.id} type="button" onClick={() => setSelectedArtifact(artifact.id)}><strong>{artifact.type}</strong><span>{artifact.path}</span></button>)}
        {selectedArtifactValue ? <article className="artifact-preview"><div className="section-heading"><strong>{selectedArtifactValue.type}</strong><button type="button" onClick={() => setSelectedArtifact(null)}>Close</button></div><pre>{artifactContent(selectedArtifactValue)}</pre></article> : null}
      </section>

      <section className="detail-section" aria-labelledby="events-heading">
        <div className="section-heading"><h3 id="events-heading">Live events{cursor !== undefined ? ` · cursor ${cursor}` : ""}</h3><span className="badge" data-state={live === "live" ? "configured" : "pending"}>{live}</span></div>
        {events.length ? <ul className="event-list">{events.slice(-50).map((event) => <li key={qualifiedEventKey(daemonId, task.id, event.sequence)}><button type="button" onClick={() => setExpandedEvent((current) => current === event.sequence ? null : event.sequence)}><code>#{event.sequence}</code><span>{event.type}</span><span>{event.name ?? ""}</span></button>{expandedEvent === event.sequence ? <pre>{readable(event.payload)}</pre> : null}</li>)}</ul> : <p>No events yet. The stream stays open while this task is selected.</p>}
      </section>

      <form className="context-form" onSubmit={currentTask.state === "awaiting_plan_approval" ? revisePlan : submitMessage}>
        <div className="section-heading"><h3>{currentTask.state === "awaiting_plan_approval" ? "Revise plan" : "Task intervention"}</h3><select value={action} onChange={(event) => setAction(event.target.value as InterventionAction)} disabled={offline || currentTask.state === "awaiting_plan_approval"}>{interventionChoices(currentTask.state, availableActions).map((item) => <option key={item} value={item}>{actionLabels[item]}</option>)}</select></div>
        <textarea value={message} onChange={(event) => setMessage(event.target.value)} disabled={offline || pending} placeholder={currentTask.state === "awaiting_plan_approval" ? "Explain what the planner should revise..." : "Message this task..."} />
        <div className="actions"><span className="hint">{selectedAttempt ? `Targeting attempt ${selectedAttempt.slice(0, 8)}` : selectedBranch ? `Branch ${selectedBranch.id.slice(0, 8)}` : "Targets the current task history"}</span><button type="submit" disabled={offline || pending || (!message.trim() && action !== "retry")}>{pending ? "Sending..." : "Send"}</button></div>
      </form>
      {interventions.length ? <p className="hint">{interventions.length} intervention{interventions.length === 1 ? "" : "s"} recorded on this daemon.</p> : null}
    </section>
  );
}
