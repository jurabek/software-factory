"use client";

import { useEffect, useState } from "react";
import type { DaemonTask } from "../server/daemon-client.ts";
import type { DaemonConnection } from "../server/daemon-registry.ts";

type TaskState = { tasks: (DaemonTask & { daemonId: string })[]; error: string | null; loading: boolean };

async function responseMessage(response: Response): Promise<string> {
  const body = await response.json().catch(() => null) as { message?: unknown } | null;
  return typeof body?.message === "string" ? body.message : `Request failed with status ${response.status}.`;
}

export function DaemonConnections() {
  const [connections, setConnections] = useState<DaemonConnection[]>([]);
  const [taskStates, setTaskStates] = useState<Record<string, TaskState>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function loadTasks(connection: DaemonConnection) {
    setTaskStates((current) => ({ ...current, [connection.id]: { tasks: current[connection.id]?.tasks ?? [], error: null, loading: true } }));
    try {
      const response = await fetch(`/api/daemons/${encodeURIComponent(connection.id)}/tasks`, { cache: "no-store" });
      if (!response.ok) throw new Error(await responseMessage(response));
      const body = await response.json() as { tasks: (DaemonTask & { daemonId: string })[] };
      setTaskStates((current) => ({ ...current, [connection.id]: { tasks: body.tasks, error: null, loading: false } }));
    } catch (error) {
      setTaskStates((current) => ({ ...current, [connection.id]: { tasks: current[connection.id]?.tasks ?? [], error: error instanceof Error ? error.message : "Daemon is unavailable.", loading: false } }));
    }
  }

  async function loadConnections() {
    try {
      const response = await fetch("/api/daemons", { cache: "no-store" });
      if (!response.ok) throw new Error(await responseMessage(response));
      const body = await response.json() as { daemons: DaemonConnection[] };
      setConnections(body.daemons);
      for (const connection of body.daemons) void loadTasks(connection);
    } catch (error) {
      setFailure(error instanceof Error ? error.message : "Could not load daemon connections.");
    }
  }

  useEffect(() => { void loadConnections(); }, []);

  async function register(form: FormData) {
    setPending(true);
    setFailure(null);
    const response = await fetch("/api/daemons", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: form.get("name"), endpoint: form.get("endpoint"), credential: form.get("credential") }),
    });
    if (!response.ok) {
      setFailure(await responseMessage(response));
      setPending(false);
      return;
    }
    const body = await response.json() as { connection: DaemonConnection };
    setConnections((current) => [...current, body.connection].sort((left, right) => left.name.localeCompare(right.name)));
    setPending(false);
    void loadTasks(body.connection);
  }

  return (
    <section aria-labelledby="daemons-heading" className="panel">
      <div className="section-heading">
        <h2 id="daemons-heading">Daemon connections</h2>
        <span className="badge" data-state={connections.length ? "configured" : "pending"}>{connections.length} connected</span>
      </div>
      <p>Credentials stay on the application server. Repository and artifact paths belong to each daemon sandbox.</p>
      {failure ? <p role="alert" className="notice">{failure}</p> : null}
      <form className="form daemon-form" action={register}>
        <label>Name<input name="name" maxLength={80} required placeholder="sandbox-a" /></label>
        <label>Endpoint<input name="endpoint" type="url" required placeholder="http://127.0.0.1:8080" /></label>
        <label>Daemon credential<input name="credential" type="password" minLength={32} autoComplete="off" required /></label>
        <div className="actions"><button type="submit" disabled={pending}>{pending ? "Checking daemon..." : "Connect daemon"}</button></div>
      </form>
      <div className="daemon-grid">
        {connections.map((connection) => {
          const state = taskStates[connection.id];
          return (
            <article className="daemon-card" key={connection.id}>
              <div className="section-heading"><h3>{connection.name}</h3><button type="button" disabled={state?.loading} onClick={() => loadTasks(connection)}>Refresh</button></div>
              <code>{connection.endpoint}</code>
              <p className="daemon-identity">ID {connection.daemonIdentity}</p>
              {state?.error ? <p role="alert" className="notice">{state.error}</p> : null}
              {state?.loading ? <p>Loading tasks...</p> : null}
              {!state?.loading && !state?.error && state?.tasks.length === 0 ? <p>No tasks on this daemon.</p> : null}
              <ul className="task-list">
                {state?.tasks.map((task) => (
                  <li key={`${task.daemonId}:${task.id}`}><code>{task.id}</code><span>{task.state}</span><strong>{task.request}</strong></li>
                ))}
              </ul>
            </article>
          );
        })}
      </div>
    </section>
  );
}
