import { randomUUID } from "node:crypto";
import type { Pool } from "pg";
import { decryptCredential, encryptCredential } from "./credential-vault.ts";
import type { DaemonClient, DaemonHealth, DaemonTask } from "./daemon-client.ts";
import { createDaemonClient } from "./daemon-client.ts";
import { getDatabasePool } from "./database.ts";
import { parseAllowedDaemonOrigins, normalizeDaemonEndpoint } from "./endpoint-policy.ts";
import { readDeploymentEnvironment } from "./environment.ts";

type DaemonConnectionRow = {
  id: string;
  name: string;
  endpoint: string;
  daemon_identity: string;
  credential_ciphertext: string;
  created_at: Date;
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
    createdAt: row.created_at.toISOString(),
  };
}

export function createDaemonRegistryStore(pool: Pool): DaemonRegistryStore {
  const columns = "id, name, endpoint, daemon_identity, credential_ciphertext, created_at";
  return {
    async create(connection) {
      try {
        const result = await pool.query<DaemonConnectionRow>(
          `INSERT INTO factory_application.daemon_connection (id, name, endpoint, daemon_identity, credential_ciphertext)
           VALUES ($1, $2, $3, $4, $5) RETURNING ${columns}`,
          [connection.id, connection.name, connection.endpoint, connection.daemon_identity, connection.credential_ciphertext],
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
        `SELECT ${columns} FROM factory_application.daemon_connection ORDER BY name, id`,
      );
      return result.rows;
    },
    async find(id) {
      const result = await pool.query<DaemonConnectionRow>(
        `SELECT ${columns} FROM factory_application.daemon_connection WHERE id = $1`,
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

export function createDaemonRegistry(options: DaemonRegistryOptions) {
  return {
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
    async tasks(id: string): Promise<{ connection: DaemonConnection; tasks: (DaemonTask & { daemonId: string })[] }> {
      const row = await options.store.find(id);
      if (!row) throw new DaemonRegistryError(404, "daemon_not_found", "Daemon connection not found.");
      const credential = decryptCredential(row.credential_ciphertext, options.credentialKey);
      const identity = await options.client.identity(row.endpoint, credential);
      if (identity.id !== row.daemon_identity) {
        throw new DaemonRegistryError(409, "daemon_identity_changed", "Daemon identity no longer matches this registration.");
      }
      const connection = publicConnection(row);
      const tasks = (await options.client.tasks(row.endpoint, credential)).map((task) => ({ ...task, daemonId: connection.id }));
      return { connection, tasks };
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
