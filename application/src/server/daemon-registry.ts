import { randomUUID } from "node:crypto";
import type { Pool } from "pg";
import { decryptCredential, encryptCredential } from "./credential-vault.ts";
import type {
  CreateTaskInput,
  DaemonClient,
  DaemonCommand,
  DaemonCreationDefaults,
  DaemonEvent,
  DaemonHealth,
  DaemonTask,
  EventQuery,
} from "./daemon-client.ts";
import { createDaemonClient, daemonCommands, DaemonRequestError } from "./daemon-client.ts";
import { getDatabasePool } from "./database.ts";
import { parseAllowedDaemonOrigins, normalizeDaemonEndpoint } from "./endpoint-policy.ts";
import { readDeploymentEnvironment } from "./environment.ts";

type DaemonConnectionRow = {
  id: string;
  name: string;
  endpoint: string;
  daemon_identity: string;
  credential_ciphertext: string;
  created_at: Date | string;
};

export type DaemonConnection = {
  id: string;
  name: string;
  endpoint: string;
  daemonIdentity: string;
  createdAt: string;
};

export type DaemonRegistryStore = {
  create(connection: Omit<DaemonConnectionRow, "created_at">): Promise<DaemonConnectionRow>;
  list(): Promise<DaemonConnectionRow[]>;
  find(id: string): Promise<DaemonConnectionRow | null>;
};

export class DaemonRegistryError extends Error {
  constructor(public readonly status: number, public readonly code: string, message: string) {
    super(message);
  }
}

function publicConnection(row: DaemonConnectionRow): DaemonConnection {
  return {
    id: row.id,
    name: row.name,
    endpoint: row.endpoint,
    daemonIdentity: row.daemon_identity,
    createdAt: new Date(row.created_at).toISOString(),
  };
}

export function createDaemonRegistryStore(pool: Pool): DaemonRegistryStore {
  const columns = "id, name, endpoint, daemon_identity, credential_ciphertext, created_at";
  return {
    async create(connection) {
      try {
        const result = await pool.query<DaemonConnectionRow>(
          `INSERT INTO daemon_connection (id, name, endpoint, daemon_identity, credential_ciphertext, created_at)
           VALUES ($1, $2, $3, $4, $5, $6) RETURNING ${columns}`,
          [connection.id, connection.name, connection.endpoint, connection.daemon_identity, connection.credential_ciphertext, new Date().toISOString()],
        );
        return result.rows[0];
      } catch (error) {
        if (error && typeof error === "object" && "code" in error && error.code === "23505") {
          throw new DaemonRegistryError(409, "daemon_already_registered", "Daemon name, endpoint, or identity is already registered.");
        }
        throw error;
      }
    },
    async list() {
      const result = await pool.query<DaemonConnectionRow>(
        `SELECT ${columns} FROM daemon_connection ORDER BY name, id`,
      );
      return result.rows;
    },
    async find(id) {
      const result = await pool.query<DaemonConnectionRow>(
        `SELECT ${columns} FROM daemon_connection WHERE id = $1`,
        [id],
      );
      return result.rows[0] ?? null;
    },
  };
}

type DaemonRegistryOptions = {
  store: DaemonRegistryStore;
  client: DaemonClient;
  credentialKey: string;
  allowedOrigins: readonly string[];
  createID?: () => string;
};

// Server-only resolved connection. Never serialize credential or return it to clients.
export type ResolvedDaemon = {
  connection: DaemonConnection;
  endpoint: string;
  credential: string;
  expectedIdentity: string;
};

const thinkingValues = new Set(["off", "minimal", "low", "medium", "high", "xhigh", "max"]);

function validatedTaskID(taskId: unknown): string {
  if (typeof taskId !== "string" || !taskId || taskId.length > 80) {
    throw new DaemonRegistryError(400, "invalid_task_id", "Task ID must be a non-empty string.");
  }
  return taskId;
}

function validatedEventQuery(query: EventQuery): EventQuery {
  const result: EventQuery = {};
  if (query.after !== undefined) {
    if (!Number.isInteger(query.after) || query.after < 0) throw new DaemonRegistryError(400, "invalid_cursor", "Event cursor must be a non-negative integer.");
    result.after = query.after;
  }
  if (query.limit !== undefined) {
    if (!Number.isInteger(query.limit) || query.limit < 1 || query.limit > 1000) throw new DaemonRegistryError(400, "invalid_limit", "Event limit must be between 1 and 1000.");
    result.limit = query.limit;
  }
  if (query.tail !== undefined) {
    if (!Number.isInteger(query.tail) || query.tail < 1 || query.tail > 1000) throw new DaemonRegistryError(400, "invalid_tail", "Event tail must be between 1 and 1000.");
    result.tail = query.tail;
  }
  return result;
}

function validatedCreateInput(input: CreateTaskInput): CreateTaskInput {
  if (!input || typeof input !== "object") throw new DaemonRegistryError(400, "invalid_request", "Task request is invalid.");
  if (typeof input.request !== "string" || !input.request.trim() || input.request.length > 20000) {
    throw new DaemonRegistryError(400, "invalid_request", "Task request must contain 1-20000 characters.");
  }
  if (!Array.isArray(input.repositories) || input.repositories.length < 1 || input.repositories.length > 10) {
    throw new DaemonRegistryError(400, "invalid_repositories", "Provide 1-10 repositories.");
  }
  for (const repository of input.repositories) {
    if (!repository || typeof repository !== "object") throw new DaemonRegistryError(400, "invalid_repositories", "Repository entry is invalid.");
    if (repository.type !== "local" && repository.type !== "github") throw new DaemonRegistryError(400, "invalid_repositories", "Repository type must be local or github.");
    if (repository.name !== undefined && (typeof repository.name !== "string" || !repository.name || repository.name.length > 80)) {
      throw new DaemonRegistryError(400, "invalid_repositories", "Repository name is invalid.");
    }
    if (repository.type === "local" && (typeof repository.path !== "string" || !repository.path.startsWith("/"))) {
      throw new DaemonRegistryError(400, "invalid_repositories", "Local repositories need an absolute path.");
    }
    if (repository.type === "github" && (typeof repository.repo !== "string" || !/^[^/\s]+\/[^/\s]+$/.test(repository.repo))) {
      throw new DaemonRegistryError(400, "invalid_repositories", "GitHub repositories need owner/name.");
    }
    if (repository.primary !== undefined && typeof repository.primary !== "boolean") {
      throw new DaemonRegistryError(400, "invalid_repositories", "Repository primary flag is invalid.");
    }
  }
  if (input.coding_agent !== undefined && typeof input.coding_agent !== "string") {
    throw new DaemonRegistryError(400, "invalid_harness", "Coding agent selection is invalid.");
  }
  if (input.model !== undefined && (typeof input.model !== "string" || !input.model || input.model.length > 200)) {
    throw new DaemonRegistryError(400, "invalid_model", "Model selection is invalid.");
  }
  if (input.thinking !== undefined && (typeof input.thinking !== "string" || !thinkingValues.has(input.thinking))) {
    throw new DaemonRegistryError(400, "invalid_thinking", "Thinking level is invalid.");
  }
  return {
    request: input.request.trim(),
    repositories: input.repositories,
    ...(input.coding_agent ? { coding_agent: input.coding_agent } : {}),
    ...(input.model ? { model: input.model } : {}),
    ...(input.thinking ? { thinking: input.thinking } : {}),
  };
}

function identityMismatch(error: unknown): boolean {
  return error instanceof DaemonRequestError && (error.code === "daemon_identity_mismatch" || error.status === 409 && error.code === "daemon_identity_mismatch");
}

function remapIdentityMismatch(error: unknown): unknown {
  if (identityMismatch(error)) {
    return new DaemonRegistryError(409, "daemon_identity_changed", "Daemon identity no longer matches this registration.");
  }
  return error;
}

export function createDaemonRegistry(options: DaemonRegistryOptions) {
  async function resolve(id: string): Promise<ResolvedDaemon> {
    const row = await options.store.find(id);
    if (!row) throw new DaemonRegistryError(404, "daemon_not_found", "Daemon connection not found.");
    let credential: string;
    try {
      credential = decryptCredential(row.credential_ciphertext, options.credentialKey);
    } catch {
      throw new DaemonRegistryError(500, "daemon_credential_unavailable", "Stored daemon credential is unavailable.");
    }
    return {
      connection: publicConnection(row),
      endpoint: row.endpoint,
      credential,
      expectedIdentity: row.daemon_identity,
    };
  }

  return {
    async resolve(id: string): Promise<ResolvedDaemon> {
      return resolve(id);
    },
    async register(input: { name: string; endpoint: string; credential: string }): Promise<{ connection: DaemonConnection; health: Pick<DaemonHealth, "status"> }> {
      const name = input.name.trim();
      if (!name || name.length > 80) throw new DaemonRegistryError(400, "invalid_name", "Daemon name must contain 1-80 characters.");
      if (input.credential.length < 32 || input.credential.trim() !== input.credential) {
        throw new DaemonRegistryError(400, "invalid_credential", "Daemon credential must contain at least 32 characters without surrounding whitespace.");
      }
      let endpoint: string;
      try {
        endpoint = normalizeDaemonEndpoint(input.endpoint, options.allowedOrigins);
      } catch (error) {
        throw new DaemonRegistryError(400, "endpoint_not_allowed", error instanceof Error ? error.message : "Daemon endpoint is not allowed.");
      }
      const [identity, health] = await Promise.all([
        options.client.identity(endpoint, input.credential),
        options.client.health(endpoint, input.credential),
      ]);
      const row = await options.store.create({
        id: (options.createID ?? randomUUID)(),
        name,
        endpoint,
        daemon_identity: identity.id,
        credential_ciphertext: encryptCredential(input.credential, options.credentialKey),
      });
      return { connection: publicConnection(row), health: { status: health.status } };
    },
    async list(): Promise<DaemonConnection[]> {
      return (await options.store.list()).map(publicConnection);
    },
    async tasks(id: string, signal?: AbortSignal): Promise<{ connection: DaemonConnection; tasks: (DaemonTask & { daemonId: string })[] }> {
      const resolved = await resolve(id);
      try {
        const tasks = await options.client.tasks(resolved.endpoint, resolved.credential, { expectedIdentity: resolved.expectedIdentity, signal });
        return { connection: resolved.connection, tasks: tasks.map((task) => ({ ...task, daemonId: resolved.connection.id })) };
      } catch (error) {
        throw remapIdentityMismatch(error);
      }
    },
    async creationOptions(id: string, harness?: string, signal?: AbortSignal): Promise<{
      connection: DaemonConnection;
      defaults: DaemonCreationDefaults;
      harnesses: string[];
      models: { harness: string; models: { provider: string; id: string }[] };
    }> {
      const resolved = await resolve(id);
      const operation = { expectedIdentity: resolved.expectedIdentity, signal };
      try {
        const [defaults, harnesses] = await Promise.all([
          options.client.configDefaults(resolved.endpoint, resolved.credential, operation),
          options.client.harnesses(resolved.endpoint, resolved.credential, operation),
        ]);
        const selected = harness?.trim() ? harness.trim() : defaults.coding_agent;
        if (selected.length > 80) throw new DaemonRegistryError(400, "invalid_harness", "Harness selection is invalid.");
        const models = await options.client.models(resolved.endpoint, resolved.credential, selected, operation);
        return { connection: resolved.connection, defaults, harnesses, models };
      } catch (error) {
        if (error instanceof DaemonRegistryError) throw error;
        throw remapIdentityMismatch(error);
      }
    },
    async createTask(id: string, input: CreateTaskInput, signal?: AbortSignal): Promise<{ connection: DaemonConnection; task: DaemonTask & { daemonId: string } }> {
      const validated = validatedCreateInput(input);
      const resolved = await resolve(id);
      try {
        const task = await options.client.createTask(resolved.endpoint, resolved.credential, validated, {
          expectedIdentity: resolved.expectedIdentity,
          signal,
        });
        return { connection: resolved.connection, task: { ...task, daemonId: resolved.connection.id } };
      } catch (error) {
        if (error instanceof DaemonRegistryError) throw error;
        throw remapIdentityMismatch(error);
      }
    },
    async command(
      id: string,
      taskId: string,
      command: DaemonCommand,
      actor: string,
      signal?: AbortSignal,
    ): Promise<{ connection: DaemonConnection; taskId: string; accepted: boolean }> {
      if (!daemonCommands.includes(command)) throw new DaemonRegistryError(404, "unknown_command", "Unsupported daemon command.");
      const validatedTask = validatedTaskID(taskId);
      if (!actor || actor.length > 64) throw new DaemonRegistryError(400, "invalid_actor", "Command actor is invalid.");
      const resolved = await resolve(id);
      try {
        const result = await options.client.command(resolved.endpoint, resolved.credential, validatedTask, command, {
          expectedIdentity: resolved.expectedIdentity,
          actor,
          signal,
        });
        return { connection: resolved.connection, taskId: validatedTask, accepted: result.accepted };
      } catch (error) {
        throw remapIdentityMismatch(error);
      }
    },
    async events(
      id: string,
      taskId: string,
      query: EventQuery,
      signal?: AbortSignal,
    ): Promise<{ connection: DaemonConnection; taskId: string; events: DaemonEvent[]; cursor: number }> {
      const validatedTask = validatedTaskID(taskId);
      const validatedQuery = validatedEventQuery(query);
      const resolved = await resolve(id);
      try {
        const result = await options.client.events(resolved.endpoint, resolved.credential, validatedTask, validatedQuery, {
          expectedIdentity: resolved.expectedIdentity,
          signal,
        });
        return { connection: resolved.connection, taskId: validatedTask, events: result.events, cursor: result.cursor };
      } catch (error) {
        throw remapIdentityMismatch(error);
      }
    },
    async eventStream(
      id: string,
      taskId: string,
      cursor: { after?: number; lastEventID?: string },
      signal?: AbortSignal,
    ): Promise<{ connection: DaemonConnection; taskId: string; upstream: Response }> {
      const validatedTask = validatedTaskID(taskId);
      if (cursor.after !== undefined && (!Number.isInteger(cursor.after) || cursor.after < 0)) {
        throw new DaemonRegistryError(400, "invalid_cursor", "Event cursor must be a non-negative integer.");
      }
      if (cursor.lastEventID !== undefined && !/^\d+$/.test(cursor.lastEventID)) {
        throw new DaemonRegistryError(400, "invalid_cursor", "Last event ID must be a non-negative integer.");
      }
      const resolved = await resolve(id);
      try {
        const upstream = await options.client.eventStream(resolved.endpoint, resolved.credential, validatedTask, cursor, {
          expectedIdentity: resolved.expectedIdentity,
          signal,
        });
        return { connection: resolved.connection, taskId: validatedTask, upstream };
      } catch (error) {
        throw remapIdentityMismatch(error);
      }
    },
  };
}

export type DaemonRegistry = ReturnType<typeof createDaemonRegistry>;

let cached: DaemonRegistry | undefined;

export function getDaemonRegistry(): DaemonRegistry {
  if (!cached) {
    const environment = readDeploymentEnvironment(process.env);
    cached = createDaemonRegistry({
      store: createDaemonRegistryStore(getDatabasePool(environment.DATABASE_URL)),
      client: createDaemonClient(),
      credentialKey: environment.DAEMON_CREDENTIAL_KEY,
      allowedOrigins: parseAllowedDaemonOrigins(environment.DAEMON_ALLOWED_ORIGINS),
    });
  }
  return cached;
}
