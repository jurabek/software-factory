"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { daemonTasks, listDaemons, registerDaemon, type QualifiedTask } from "../client/daemon-api.ts";
import { RequestScope } from "../client/daemon-ui-state.ts";
import type { DaemonConnection } from "../server/daemon-registry.ts";
import { TaskCreation } from "./task-creation.tsx";
import { TaskDetail } from "./task-detail.tsx";

type TaskState = { tasks: QualifiedTask[]; error: string | null; loading: boolean; offline: boolean };

function routeSelection(): { daemonId: string | null; taskId: string | null } {
  const parameters = new URLSearchParams(window.location.search);
  return { daemonId: parameters.get("daemon"), taskId: parameters.get("task") };
}

function writeRouteSelection(daemonId: string | null, taskId: string | null): void {
  const url = new URL(window.location.href);
  if (daemonId) url.searchParams.set("daemon", daemonId);
  else url.searchParams.delete("daemon");
  if (taskId) url.searchParams.set("task", taskId);
  else url.searchParams.delete("task");
  window.history.replaceState(null, "", url);
}

export function DaemonConnections() {
  const [connections, setConnections] = useState<DaemonConnection[]>([]);
  const [taskStates, setTaskStates] = useState<Record<string, TaskState>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [selectedDaemonId, setSelectedDaemonId] = useState<string | null>(null);
  const [selectedTaskId, setSelectedTaskId] = useState<string | null>(null);
  const [sessionExpired, setSessionExpired] = useState(false);
  const scopes = useRef(new Map<string, RequestScope>());
  const controllers = useRef(new Map<string, AbortController>());

  function scopeFor(daemonId: string): RequestScope {
    let scope = scopes.current.get(daemonId);
    if (!scope) {
      scope = new RequestScope();
      scopes.current.set(daemonId, scope);
    }
    return scope;
  }

  const loadTasks = useCallback(async (connection: DaemonConnection) => {
    const scope = scopeFor(connection.id);
    const generation = scope.next();
    controllers.current.get(connection.id)?.abort();
    const controller = new AbortController();
    controllers.current.set(connection.id, controller);
    setTaskStates((current) => ({
      ...current,
      [connection.id]: { tasks: current[connection.id]?.tasks ?? [], error: null, loading: true, offline: false },
    }));
    try {
      const result = await daemonTasks(connection.id, controller.signal);
      if (!scope.isCurrent(generation)) return;
      setTaskStates((current) => ({
        ...current,
        [connection.id]: { tasks: result.tasks, error: null, loading: false, offline: false },
      }));
    } catch (error) {
      if (controller.signal.aborted || !scope.isCurrent(generation)) return;
      const message = error instanceof Error ? error.message : "Daemon is unavailable.";
      if (message.startsWith("Session expired")) {
        setSessionExpired(true);
        setTaskStates((current) => ({
          ...current,
          [connection.id]: { tasks: current[connection.id]?.tasks ?? [], error: message, loading: false, offline: false },
        }));
        return;
      }
      // Keep previously loaded tasks but mark them explicitly stale/offline.
      setTaskStates((current) => ({
        ...current,
        [connection.id]: { tasks: current[connection.id]?.tasks ?? [], error: message, loading: false, offline: true },
      }));
    }
  }, []);

  const loadConnections = useCallback(async () => {
    try {
      const body = await listDaemons();
      setConnections(body.daemons);
      setFailure(null);
      const requested = routeSelection();
      setSelectedDaemonId((current) => current ?? body.daemons.find((daemon) => daemon.id === requested.daemonId)?.id ?? body.daemons[0]?.id ?? null);
      setSelectedTaskId((current) => current ?? requested.taskId ?? null);
      for (const connection of body.daemons) void loadTasks(connection);
    } catch (error) {
      const message = error instanceof Error ? error.message : "Could not load daemon connections.";
      if (message.startsWith("Session expired")) setSessionExpired(true);
      else setFailure(message);
    }
  }, [loadTasks]);

  useEffect(() => {
    void loadConnections();
    return () => {
      for (const controller of controllers.current.values()) controller.abort();
    };
  }, [loadConnections]);

  useEffect(() => {
    const refreshTimer = setInterval(() => {
      for (const connection of connections) void loadTasks(connection);
    }, 5_000);
    return () => clearInterval(refreshTimer);
  }, [connections, loadTasks]);

  useEffect(() => {
    function applyRoute() {
      const requested = routeSelection();
      setSelectedDaemonId(requested.daemonId);
      setSelectedTaskId(requested.taskId);
    }
    window.addEventListener("popstate", applyRoute);
    return () => window.removeEventListener("popstate", applyRoute);
  }, []);

  function selectDaemon(daemonId: string) {
    setSelectedDaemonId(daemonId);
    setSelectedTaskId(null);
    writeRouteSelection(daemonId, null);
  }

  function selectTask(daemonId: string, taskId: string) {
    setSelectedDaemonId(daemonId);
    setSelectedTaskId(taskId);
    writeRouteSelection(daemonId, taskId);
  }

  async function register(form: FormData) {
    setPending(true);
    setFailure(null);
    try {
      const response = await registerDaemon({
        name: String(form.get("name") ?? ""),
        endpoint: String(form.get("endpoint") ?? ""),
        credential: String(form.get("credential") ?? ""),
      });
      setConnections((current) => [...current, response.connection].sort((left, right) => left.name.localeCompare(right.name)));
      setSelectedDaemonId(response.connection.id);
      setSelectedTaskId(null);
      writeRouteSelection(response.connection.id, null);
      void loadTasks(response.connection);
    } catch (error) {
      const message = error instanceof Error ? error.message : "Could not connect the daemon.";
      if (message.startsWith("Session expired")) setSessionExpired(true);
      else setFailure(message);
    } finally {
      setPending(false);
    }
  }

  const selected = connections.find((connection) => connection.id === selectedDaemonId) ?? null;
  const selectedState = selected ? taskStates[selected.id] : undefined;
  const selectedTask = selectedState?.tasks.find((task) => task.id === selectedTaskId) ?? null;
  const allTasks = connections.flatMap((connection) => (taskStates[connection.id]?.tasks ?? []).map((task) => ({ connection, task })));

  return (
    <section aria-labelledby="daemons-heading" className="panel">
      <div className="section-heading">
        <h2 id="daemons-heading">Daemon connections</h2>
        <span className="badge" data-state={connections.length ? "configured" : "pending"}>{connections.length} connected</span>
      </div>
      <p>Credentials stay on the application server. Repository and artifact paths belong to each daemon sandbox.</p>
      {sessionExpired ? <p role="alert" className="notice">Session expired. Sign in again to continue.</p> : null}
      {failure ? <p role="alert" className="notice">{failure}</p> : null}
      <form className="form daemon-form" action={register}>
        <label>Name<input name="name" maxLength={80} required placeholder="sandbox-a" /></label>
        <label>Endpoint<input name="endpoint" type="url" required placeholder="http://127.0.0.1:8080" /></label>
        <label>Daemon credential<input name="credential" type="password" minLength={32} autoComplete="off" required /></label>
        <div className="actions"><button type="submit" disabled={pending}>{pending ? "Checking daemon..." : "Connect daemon"}</button></div>
      </form>
      {connections.length > 1 ? (
        <section className="combined-tasks" aria-labelledby="combined-tasks-heading">
          <div className="section-heading"><h3 id="combined-tasks-heading">All task sessions</h3><span className="badge">{allTasks.length}</span></div>
          <ul className="task-list">
             {allTasks.map(({ connection, task }) => (
               <li key={`${connection.id}:${task.id}`} className={connection.id === selectedDaemonId && task.id === selectedTaskId ? "selected" : undefined}>
                 <code>{connection.name}/{task.id}</code><span>{taskStates[connection.id]?.offline ? "offline" : task.state}</span><strong>{task.request}</strong>
                  <button type="button" onClick={() => selectTask(connection.id, task.id)}>Open</button>
              </li>
            ))}
          </ul>
          {!allTasks.length ? <p>No task sessions loaded from the connected daemons.</p> : null}
        </section>
      ) : null}
      <div className="daemon-grid">
        {connections.map((connection) => {
          const state = taskStates[connection.id];
          return (
            <article className="daemon-card" key={connection.id} aria-current={connection.id === selectedDaemonId}>
              <div className="section-heading">
                <h3>{connection.name}</h3>
                <div className="actions">
                  <button type="button" disabled={state?.loading} onClick={() => loadTasks(connection)}>Refresh</button>
                  <button type="button" disabled={connection.id === selectedDaemonId} onClick={() => selectDaemon(connection.id)}>Select</button>
                </div>
              </div>
              <code>{connection.endpoint}</code>
              <p className="daemon-identity">ID {connection.daemonIdentity}</p>
              {state?.offline ? <p role="alert" className="notice">Daemon offline. Showing last known tasks.</p> : null}
              {state?.error && !state.offline ? <p role="alert" className="notice">{state.error}</p> : null}
              {state?.loading ? <p>Loading tasks...</p> : null}
              {!state?.loading && !state?.error && state?.tasks.length === 0 ? <p>No tasks on this daemon.</p> : null}
              <ul className="task-list">
                {state?.tasks.filter((task) => !task.parent_task_id).map((rootTask) => {
                  const sessions = state.tasks.filter((task) => task.id === rootTask.id || task.parent_task_id === rootTask.id).sort((left, right) => left.created_at.localeCompare(right.created_at));
                   return <li key={`${connection.id}:${rootTask.id}`} className="task-group"><div className="task-group-heading"><strong>{rootTask.request}</strong><span>{sessions.length} sessions</span></div><ul className="task-list task-sessions">{sessions.map((task) => <li key={`${task.daemonId}:${task.id}`} className={task.id === selectedTaskId && connection.id === selectedDaemonId ? "selected" : undefined}><code>{task.id}</code><span>{state?.offline ? "offline" : task.state}</span><strong>{task.request}</strong><button type="button" onClick={() => selectTask(connection.id, task.id)}>Open</button></li>)}</ul></li>;
                })}
              </ul>
            </article>
          );
        })}
      </div>
      {selected ? (
        <TaskCreation
          key={selected.id}
          daemon={selected}
          offline={Boolean(selectedState?.offline)}
          onCreated={(task) => {
            selectTask(selected.id, task.id);
            void (async () => {
              try {
                const result = await daemonTasks(selected.id);
                setTaskStates((current) => ({
                  ...current,
                  [selected.id]: { tasks: result.tasks, error: null, loading: false, offline: false },
                }));
              } catch {
                // Creation already succeeded; the next refresh recovers the list.
              }
            })();
          }}
        />
      ) : null}
      {selected && selectedTask ? (
        <TaskDetail
          key={`${selected.id}:${selectedTask.id}`}
          daemonId={selected.id}
          daemonName={selected.name}
          task={selectedTask}
          offline={Boolean(selectedState?.offline)}
          onChanged={() => loadTasks(selected)}
          onSelectTask={(taskId) => selectTask(selected.id, taskId)}
           onRemoved={(parentTaskId) => {
             if (parentTaskId) selectTask(selected.id, parentTaskId);
             else { setSelectedTaskId(null); writeRouteSelection(selected.id, null); }
           }}
        />
      ) : null}
    </section>
  );
}
