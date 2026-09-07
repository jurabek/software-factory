"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { daemonTasks, listDaemons, registerDaemon, type QualifiedTask } from "../client/daemon-api.ts";
import { normalizeWorkspaceSelection, RequestScope, workspaceSearch, type WorkspaceSelection } from "../client/daemon-ui-state.ts";
import type { DaemonConnection } from "../server/daemon-registry.ts";
import { DaemonSetup } from "./daemon-setup.tsx";
import { TaskCreation } from "./task-creation.tsx";
import { TaskDetail } from "./task-detail.tsx";
import { TaskOverview } from "./task-overview.tsx";
import { TaskRail } from "./task-rail.tsx";

type TaskState = { tasks: QualifiedTask[]; error: string | null; loading: boolean; offline: boolean };

export function DaemonConnections({ login }: { login: string }) {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const [connections, setConnections] = useState<DaemonConnection[]>([]);
  const [taskStates, setTaskStates] = useState<Record<string, TaskState>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [sessionExpired, setSessionExpired] = useState(false);
  const [railOpen, setRailOpen] = useState(false);
  const scopes = useRef(new Map<string, RequestScope>());
  const controllers = useRef(new Map<string, AbortController>());
  const pendingSelectionIds = useRef(new Map<string, number>());
  const daemonSettledLoadStart = useRef(new Map<string, number>());
  const requested: WorkspaceSelection = { daemonId: searchParams.get("daemon"), taskId: searchParams.get("task"), sessionId: searchParams.get("session") };
  const allTasks = connections.flatMap((connection) => taskStates[connection.id]?.tasks ?? []);
  const selection = normalizeWorkspaceSelection(requested, connections.map((connection) => connection.id), allTasks);

  const navigate = useCallback((next: WorkspaceSelection, replace = false) => {
    const target = `${pathname}${workspaceSearch(next)}`;
    if (replace) router.replace(target); else router.push(target);
  }, [pathname, router]);
  function scopeFor(daemonId: string): RequestScope { let scope = scopes.current.get(daemonId); if (!scope) { scope = new RequestScope(); scopes.current.set(daemonId, scope); } return scope; }
  const loadTasks = useCallback(async (connection: DaemonConnection) => {
    const scope = scopeFor(connection.id); const generation = scope.next(); controllers.current.get(connection.id)?.abort(); const controller = new AbortController(); controllers.current.set(connection.id, controller); const startedAt = Date.now();
    setTaskStates((current) => ({ ...current, [connection.id]: { tasks: current[connection.id]?.tasks ?? [], error: null, loading: true, offline: false } }));
    try { const result = await daemonTasks(connection.id, controller.signal); if (!scope.isCurrent(generation)) return; daemonSettledLoadStart.current.set(connection.id, Math.max(daemonSettledLoadStart.current.get(connection.id) ?? 0, startedAt)); setTaskStates((current) => ({ ...current, [connection.id]: { tasks: result.tasks, error: null, loading: false, offline: false } })); }
    catch (error) { if (controller.signal.aborted || !scope.isCurrent(generation)) return; const message = error instanceof Error ? error.message : "Daemon is unavailable."; if (message.startsWith("Session expired")) setSessionExpired(true); setTaskStates((current) => ({ ...current, [connection.id]: { tasks: current[connection.id]?.tasks ?? [], error: message, loading: false, offline: !message.startsWith("Session expired") } })); }
  }, []);
  const loadConnections = useCallback(async () => {
    try { const body = await listDaemons(); setConnections(body.daemons); setFailure(null); for (const connection of body.daemons) void loadTasks(connection); }
    catch (error) { const message = error instanceof Error ? error.message : "Could not load daemon connections."; if (message.startsWith("Session expired")) setSessionExpired(true); else setFailure(message); }
  }, [loadTasks]);
  useEffect(() => { void loadConnections(); return () => { for (const controller of controllers.current.values()) controller.abort(); }; }, [loadConnections]);
  useEffect(() => { const timer = setInterval(() => { for (const connection of connections) void loadTasks(connection); }, 5_000); return () => clearInterval(timer); }, [connections, loadTasks]);
  useEffect(() => {
    if (!connections.length || JSON.stringify(selection) === JSON.stringify(requested)) return;
    if (requested.taskId && requested.daemonId) {
      const requestedState = taskStates[requested.daemonId];
      if (!requestedState || requestedState.loading) return;
    }
    // A just-created task/session can be missing from a poll that started
    // before its selection. Only treat it as invalid once a task load for
    // the requested daemon that started afterwards has settled.
    const now = Date.now();
    const settledStart = (requested.daemonId && daemonSettledLoadStart.current.get(requested.daemonId)) || 0;
    for (const [id, selectedAt] of pendingSelectionIds.current) {
      if (now - selectedAt > 30_000 || allTasks.some((task) => task.id === id) || settledStart > selectedAt) pendingSelectionIds.current.delete(id);
    }
    if ([requested.taskId, requested.sessionId].some((id) => id && pendingSelectionIds.current.has(id))) return;
    navigate(selection, true);
  }, [allTasks, connections.length, navigate, requested, selection, taskStates]);
  const selected = connections.find((connection) => connection.id === selection.daemonId) ?? null;
  const selectedState = selected ? taskStates[selected.id] : undefined;
  const selectedTaskId = selection.sessionId ?? selection.taskId;
  const selectedTask = selected ? selectedState?.tasks.find((task) => task.id === selectedTaskId) ?? null : null;
  const rootTask = selected && selection.taskId ? selectedState?.tasks.find((task) => task.id === selection.taskId) ?? null : null;
  async function register(form: FormData) { const response = await registerDaemon({ name: String(form.get("name") ?? ""), endpoint: String(form.get("endpoint") ?? ""), credential: String(form.get("credential") ?? "") }); setConnections((current) => [...current, response.connection].sort((left, right) => left.name.localeCompare(right.name))); await loadTasks(response.connection); navigate({ daemonId: response.connection.id, taskId: null, sessionId: null }); }
  function selectRecord(record: QualifiedTask) { pendingSelectionIds.current.set(record.id, Date.now()); const rootId = record.parent_task_id ?? record.id; navigate({ daemonId: record.daemonId, taskId: rootId, sessionId: record.id === rootId ? null : record.id }); }
  function selectTaskId(taskId: string) { const task = selectedState?.tasks.find((record) => record.id === taskId); if (task) selectRecord(task); }
  function openSession(sessionId: string, rootId: string) { pendingSelectionIds.current.set(sessionId, Date.now()); navigate({ daemonId: selected?.id ?? null, taskId: rootId, sessionId }); }
  function handleRemoved(parentTaskId?: string) { navigate({ daemonId: selected?.id ?? null, taskId: parentTaskId ?? null, sessionId: null }); }
  const tasksByDaemon = Object.fromEntries(connections.map((connection) => [connection.id, taskStates[connection.id]?.tasks ?? []]));
  const offlineByDaemon = Object.fromEntries(connections.map((connection) => [connection.id, Boolean(taskStates[connection.id]?.offline)]));
  return <div className="task-shell" data-rail-open={railOpen}><button className="rail-toggle mobile-only" type="button" onClick={() => setRailOpen(true)} aria-label="Open task navigation">Tasks</button><TaskRail connections={connections} tasksByDaemon={tasksByDaemon} offlineByDaemon={offlineByDaemon} selection={selection} login={login} open={railOpen} onClose={() => setRailOpen(false)} /><section className="workspace-content">{sessionExpired ? <p className="notice" role="alert">Session expired. Sign in again to continue.</p> : null}{failure ? <p className="notice" role="alert">{failure}</p> : null}{!connections.length ? <DaemonSetup onRegister={register} /> : !selected ? <DaemonSetup compact onRegister={register} /> : !rootTask ? <TaskCreation key={selected.id} daemon={selected} offline={Boolean(selectedState?.offline)} onCreated={(task) => { selectRecord(task); void loadTasks(selected); }} /> : !selection.sessionId ? <TaskOverview key={`${selected.id}:${rootTask.id}`} daemon={selected} task={rootTask} login={login} offline={Boolean(selectedState?.offline)} onOpenSession={(sessionId) => openSession(sessionId, rootTask.id)} onCreated={(session) => { openSession(session.id, rootTask.id); void loadTasks(selected); }} /> : selectedTask ? <TaskDetail key={`${selected.id}:${selectedTask.id}`} daemonId={selected.id} daemonName={selected.name} task={selectedTask} rootTask={rootTask} login={login} offline={Boolean(selectedState?.offline)} onChanged={() => loadTasks(selected)} onOpenTask={() => selectTaskId(rootTask.id)} onRemoved={handleRemoved} /> : <p className="workspace-empty">Selected session is no longer available.</p>}</section></div>;
}
