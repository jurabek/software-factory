import { randomUUID } from "node:crypto";
import type { Pool } from "pg";
import type {
	CreateSessionInput,
	CreateTaskInput,
	DaemonClient,
	DaemonCommand,
	DaemonCreationDefaults,
	DaemonEvent,
	DaemonHarnessModel,
	DaemonHealth,
	DaemonTask,
	EventQuery,
	FeedbackInput,
	InterventionInput,
} from "./daemon-client.ts";
import { createDaemonClient, daemonCommands } from "./daemon-client.ts";
import {
	ConnectionTokenError,
	parseConnectionToken,
} from "./connection-token.ts";
import { getDatabasePool } from "./database.ts";
import {
	normalizeDaemonEndpoint,
	parseAllowedDaemonOrigins,
} from "./endpoint-policy.ts";
import { readDeploymentEnvironment } from "./environment.ts";

type DaemonConnectionRow = {
	id: string;
	name: string;
	endpoint: string;
	daemon_identity: string;
	credential: string;
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
	create(
		connection: Omit<DaemonConnectionRow, "created_at">,
	): Promise<DaemonConnectionRow>;
	list(): Promise<DaemonConnectionRow[]>;
	find(id: string): Promise<DaemonConnectionRow | null>;
	delete(id: string): Promise<boolean>;
};

export class DaemonRegistryError extends Error {
	constructor(
		public readonly status: number,
		public readonly code: string,
		message: string,
	) {
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
	const columns = "id, name, endpoint, daemon_identity, credential, created_at";
	return {
		async create(connection) {
			try {
				const result = await pool.query<DaemonConnectionRow>(
					`INSERT INTO daemon_connection (id, name, endpoint, daemon_identity, credential, created_at)
           VALUES ($1, $2, $3, $4, $5, $6) RETURNING ${columns}`,
					[
						connection.id,
						connection.name,
						connection.endpoint,
						connection.daemon_identity,
						connection.credential,
						new Date().toISOString(),
					],
				);
				return result.rows[0];
			} catch (error) {
				if (
					error &&
					typeof error === "object" &&
					"code" in error &&
					error.code === "23505"
				) {
					throw new DaemonRegistryError(
						409,
						"daemon_already_registered",
						"Daemon name, endpoint, or identity is already registered.",
					);
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
		async delete(id) {
			const result = await pool.query<{ id: string }>(
				`DELETE FROM daemon_connection WHERE id = $1 RETURNING id`,
				[id],
			);
			return result.rowCount !== null && result.rowCount > 0;
		},
	};
}

type DaemonRegistryOptions = {
	store: DaemonRegistryStore;
	client: DaemonClient;
	allowedOrigins: readonly string[];
	createID?: () => string;
};

// Server-only resolved connection. Never serialize credential or return it to clients.
export type ResolvedDaemon = {
	connection: DaemonConnection;
	endpoint: string;
	credential: string;
};

const thinkingValues = new Set([
	"off",
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
]);

function validatedTaskID(taskId: unknown): string {
	if (typeof taskId !== "string" || !taskId || taskId.length > 80) {
		throw new DaemonRegistryError(
			400,
			"invalid_task_id",
			"Task ID must be a non-empty string.",
		);
	}
	return taskId;
}

function validatedEventQuery(query: EventQuery): EventQuery {
	const result: EventQuery = {};
	if (query.after !== undefined) {
		if (!Number.isInteger(query.after) || query.after < 0)
			throw new DaemonRegistryError(
				400,
				"invalid_cursor",
				"Event cursor must be a non-negative integer.",
			);
		result.after = query.after;
	}
	if (query.limit !== undefined) {
		if (!Number.isInteger(query.limit) || query.limit < 1 || query.limit > 1000)
			throw new DaemonRegistryError(
				400,
				"invalid_limit",
				"Event limit must be between 1 and 1000.",
			);
		result.limit = query.limit;
	}
	if (query.tail !== undefined) {
		if (!Number.isInteger(query.tail) || query.tail < 1 || query.tail > 1000)
			throw new DaemonRegistryError(
				400,
				"invalid_tail",
				"Event tail must be between 1 and 1000.",
			);
		result.tail = query.tail;
	}
	return result;
}

function validatedCreateInput(input: CreateTaskInput): CreateTaskInput {
	if (!input || typeof input !== "object")
		throw new DaemonRegistryError(
			400,
			"invalid_request",
			"Task request is invalid.",
		);
	if (
		typeof input.request !== "string" ||
		!input.request.trim() ||
		input.request.length > 20000
	) {
		throw new DaemonRegistryError(
			400,
			"invalid_request",
			"Task request must contain 1-20000 characters.",
		);
	}
	if (
		!Array.isArray(input.repositories) ||
		input.repositories.length < 1 ||
		input.repositories.length > 10
	) {
		throw new DaemonRegistryError(
			400,
			"invalid_repositories",
			"Provide 1-10 repositories.",
		);
	}
	for (const repository of input.repositories) {
		if (!repository || typeof repository !== "object")
			throw new DaemonRegistryError(
				400,
				"invalid_repositories",
				"Repository entry is invalid.",
			);
		if (repository.type !== "local" && repository.type !== "github")
			throw new DaemonRegistryError(
				400,
				"invalid_repositories",
				"Repository type must be local or github.",
			);
		if (
			repository.name !== undefined &&
			(typeof repository.name !== "string" ||
				!repository.name ||
				repository.name.length > 80)
		) {
			throw new DaemonRegistryError(
				400,
				"invalid_repositories",
				"Repository name is invalid.",
			);
		}
		if (
			repository.type === "local" &&
			(typeof repository.path !== "string" || !repository.path.startsWith("/"))
		) {
			throw new DaemonRegistryError(
				400,
				"invalid_repositories",
				"Local repositories need an absolute path.",
			);
		}
		if (
			repository.type === "github" &&
			(typeof repository.repo !== "string" ||
				!/^[^/\s]+\/[^/\s]+$/.test(repository.repo))
		) {
			throw new DaemonRegistryError(
				400,
				"invalid_repositories",
				"GitHub repositories need owner/name.",
			);
		}
		if (
			repository.primary !== undefined &&
			typeof repository.primary !== "boolean"
		) {
			throw new DaemonRegistryError(
				400,
				"invalid_repositories",
				"Repository primary flag is invalid.",
			);
		}
	}
	if (
		input.coding_agent !== undefined &&
		typeof input.coding_agent !== "string"
	) {
		throw new DaemonRegistryError(
			400,
			"invalid_harness",
			"Coding agent selection is invalid.",
		);
	}
	if (
		input.model !== undefined &&
		(typeof input.model !== "string" ||
			!input.model ||
			input.model.length > 200)
	) {
		throw new DaemonRegistryError(
			400,
			"invalid_model",
			"Model selection is invalid.",
		);
	}
	if (
		input.thinking !== undefined &&
		(typeof input.thinking !== "string" || !thinkingValues.has(input.thinking))
	) {
		throw new DaemonRegistryError(
			400,
			"invalid_thinking",
			"Thinking level is invalid.",
		);
	}
	return {
		request: input.request.trim(),
		repositories: input.repositories,
		...(input.coding_agent ? { coding_agent: input.coding_agent } : {}),
		...(input.model ? { model: input.model } : {}),
		...(input.thinking ? { thinking: input.thinking } : {}),
	};
}

function remapIdentityMismatch(error: unknown): unknown {
	return error;
}

export function createDaemonRegistry(options: DaemonRegistryOptions) {
	async function resolve(id: string): Promise<ResolvedDaemon> {
		const row = await options.store.find(id);
		if (!row)
			throw new DaemonRegistryError(
				404,
				"daemon_not_found",
				"Daemon connection not found.",
			);
		return {
			connection: publicConnection(row),
			endpoint: row.endpoint,
			credential: row.credential,
		};
	}

	return {
		async resolve(id: string): Promise<ResolvedDaemon> {
			return resolve(id);
		},
		async deregister(id: string): Promise<{
			connection: DaemonConnection;
			result: { deleted: boolean };
		}> {
			const resolved = await resolve(id);
			const deleted = await options.store.delete(id);
			if (!deleted)
				throw new DaemonRegistryError(
					404,
					"daemon_not_found",
					"Daemon connection not found.",
				);
			return { connection: resolved.connection, result: { deleted: true } };
		},
		async register(input: { token: string; name?: string }): Promise<{
			connection: DaemonConnection;
			health: Pick<DaemonHealth, "status">;
		}> {
			if (typeof input.token !== "string" || !input.token.trim()) {
				throw new DaemonRegistryError(
					400,
					"invalid_token",
					"Connection token is required.",
				);
			}
			let parsed: {
				endpoint: string;
				credential: string;
				daemonId: string;
				name?: string;
			};
			try {
				parsed = parseConnectionToken(input.token);
			} catch (error) {
				if (error instanceof ConnectionTokenError) {
					throw new DaemonRegistryError(400, error.code, error.message);
				}
				throw error;
			}
			const name = (
				input.name !== undefined && input.name.trim()
					? input.name
					: (parsed.name ?? "")
			).trim();
			if (!name || name.length > 80)
				throw new DaemonRegistryError(
					400,
					"invalid_name",
					"Daemon name must contain 1-80 characters.",
				);
			if (
				parsed.credential.length < 32 ||
				parsed.credential.trim() !== parsed.credential
			) {
				throw new DaemonRegistryError(
					400,
					"invalid_credential",
					"Daemon credential must contain at least 32 characters without surrounding whitespace.",
				);
			}
			let endpoint: string;
			try {
				endpoint = normalizeDaemonEndpoint(
					parsed.endpoint,
					options.allowedOrigins,
				);
			} catch (error) {
				throw new DaemonRegistryError(
					400,
					"endpoint_not_allowed",
					error instanceof Error
						? error.message
						: "Daemon endpoint is not allowed.",
				);
			}
			const [identity, health] = await Promise.all([
				options.client.identity(endpoint, parsed.credential),
				options.client.health(endpoint, parsed.credential),
			]);
			if (identity.id !== parsed.daemonId) {
				throw new DaemonRegistryError(
					400,
					"identity_mismatch",
					"Connection token daemon id does not match the daemon identity.",
				);
			}
			const row = await options.store.create({
				id: (options.createID ?? randomUUID)(),
				name,
				endpoint,
				daemon_identity: identity.id,
				credential: parsed.credential,
			});
			return {
				connection: publicConnection(row),
				health: { status: health.status },
			};
		},
		async list(): Promise<DaemonConnection[]> {
			return (await options.store.list()).map(publicConnection);
		},
		async tasks(
			id: string,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			tasks: (DaemonTask & { daemonId: string })[];
		}> {
			const resolved = await resolve(id);
			try {
				const tasks = await options.client.tasks(
					resolved.endpoint,
					resolved.credential,
					{ signal },
				);
				return {
					connection: resolved.connection,
					tasks: tasks.map((task) => ({
						...task,
						daemonId: resolved.connection.id,
					})),
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async creationOptions(
			id: string,
			harness?: string,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			defaults: DaemonCreationDefaults;
			harnesses: string[];
			models: { harness: string; models: DaemonHarnessModel[] };
		}> {
			const resolved = await resolve(id);
			const operation = { signal };
			try {
				const [defaults, harnesses] = await Promise.all([
					options.client.configDefaults(
						resolved.endpoint,
						resolved.credential,
						operation,
					),
					options.client.harnesses(
						resolved.endpoint,
						resolved.credential,
						operation,
					),
				]);
				const selected = harness?.trim()
					? harness.trim()
					: defaults.coding_agent;
				if (selected.length > 80)
					throw new DaemonRegistryError(
						400,
						"invalid_harness",
						"Harness selection is invalid.",
					);
				const models = await options.client.models(
					resolved.endpoint,
					resolved.credential,
					selected,
					operation,
				);
				return { connection: resolved.connection, defaults, harnesses, models };
			} catch (error) {
				if (error instanceof DaemonRegistryError) throw error;
				throw remapIdentityMismatch(error);
			}
		},
		async createTask(
			id: string,
			input: CreateTaskInput,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			task: DaemonTask & { daemonId: string };
		}> {
			const validated = validatedCreateInput(input);
			const resolved = await resolve(id);
			try {
				const task = await options.client.createTask(
					resolved.endpoint,
					resolved.credential,
					validated,
					{
						signal,
					},
				);
				return {
					connection: resolved.connection,
					task: { ...task, daemonId: resolved.connection.id },
				};
			} catch (error) {
				if (error instanceof DaemonRegistryError) throw error;
				throw remapIdentityMismatch(error);
			}
		},
		async task(
			id: string,
			taskId: string,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			task: unknown;
		}> {
			const validatedTask = validatedTaskID(taskId);
			const resolved = await resolve(id);
			try {
				const task = await options.client.task(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					{
						signal,
					},
				);
				return { connection: resolved.connection, taskId: validatedTask, task };
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async sessions(
			id: string,
			taskId: string,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			sessions: unknown;
		}> {
			const validatedTask = validatedTaskID(taskId);
			const resolved = await resolve(id);
			try {
				const sessions = await options.client.sessions(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					{
						signal,
					},
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					sessions,
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async createSession(
			id: string,
			taskId: string,
			input: CreateSessionInput,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			session: unknown;
		}> {
			const validatedTask = validatedTaskID(taskId);
			if (
				!input ||
				typeof input.request !== "string" ||
				!input.request.trim() ||
				input.request.length > 20_000
			) {
				throw new DaemonRegistryError(
					400,
					"invalid_session",
					"Session request must contain 1-20000 characters.",
				);
			}
			const resolved = await resolve(id);
			try {
				const session = await options.client.createSession(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					{ request: input.request.trim() },
					{
						signal,
					},
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					session,
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async feedback(
			id: string,
			taskId: string,
			actor: string,
			input: FeedbackInput,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			result: unknown;
		}> {
			const validatedTask = validatedTaskID(taskId);
			if (!actor || actor.length > 64)
				throw new DaemonRegistryError(
					400,
					"invalid_actor",
					"Feedback actor is invalid.",
				);
			if (
				!input ||
				typeof input.feedback !== "string" ||
				!input.feedback.trim()
			) {
				throw new DaemonRegistryError(
					400,
					"invalid_feedback",
					"Feedback is required.",
				);
			}
			const resolved = await resolve(id);
			try {
				const result = await options.client.feedback(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					{
						feedback: input.feedback.trim(),
						...(input.current_plan_digest
							? { current_plan_digest: input.current_plan_digest }
							: {}),
					},
					{ actor, signal },
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					result,
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async intervene(
			id: string,
			taskId: string,
			actor: string,
			input: InterventionInput,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			result: unknown;
		}> {
			const validatedTask = validatedTaskID(taskId);
			if (!actor || actor.length > 64)
				throw new DaemonRegistryError(
					400,
					"invalid_actor",
					"Intervention actor is invalid.",
				);
			const resolved = await resolve(id);
			try {
				const result = await options.client.intervene(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					input,
					{
						actor,
						signal,
					},
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					result,
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async interventions(
			id: string,
			taskId: string,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			interventions: unknown;
		}> {
			const validatedTask = validatedTaskID(taskId);
			const resolved = await resolve(id);
			try {
				const interventions = await options.client.interventions(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					{
						signal,
					},
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					interventions,
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async remove(
			id: string,
			taskId: string,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			result: unknown;
		}> {
			const validatedTask = validatedTaskID(taskId);
			const resolved = await resolve(id);
			try {
				const result = await options.client.remove(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					{
						signal,
					},
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					result,
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async attempts(
			id: string,
			taskId: string,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			attempts: unknown;
		}> {
			const validatedTask = validatedTaskID(taskId);
			const resolved = await resolve(id);
			try {
				const attempts = await options.client.attempts(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					{
						signal,
					},
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					attempts,
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async attempt(
			id: string,
			taskId: string,
			attemptId: string,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			attemptId: string;
			attempt: unknown;
		}> {
			const validatedTask = validatedTaskID(taskId);
			const validatedAttempt = validatedTaskID(attemptId);
			const resolved = await resolve(id);
			try {
				const attempt = await options.client.attempt(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					validatedAttempt,
					{
						signal,
					},
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					attemptId: validatedAttempt,
					attempt,
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async branches(
			id: string,
			taskId: string,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			branches: unknown;
		}> {
			const validatedTask = validatedTaskID(taskId);
			const resolved = await resolve(id);
			try {
				const branches = await options.client.branches(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					{
						signal,
					},
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					branches,
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async artifacts(
			id: string,
			taskId: string,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			artifacts: unknown;
		}> {
			const validatedTask = validatedTaskID(taskId);
			const resolved = await resolve(id);
			try {
				const artifacts = await options.client.artifacts(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					{
						signal,
					},
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					artifacts,
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async checks(
			id: string,
			taskId: string,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			checks: unknown;
		}> {
			const validatedTask = validatedTaskID(taskId);
			const resolved = await resolve(id);
			try {
				const checks = await options.client.checks(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					{
						signal,
					},
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					checks,
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async results(
			id: string,
			taskId: string,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			results: unknown;
		}> {
			const validatedTask = validatedTaskID(taskId);
			const resolved = await resolve(id);
			try {
				const results = await options.client.results(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					{
						signal,
					},
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					results,
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async diff(
			id: string,
			taskId: string,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			diff: unknown;
		}> {
			const validatedTask = validatedTaskID(taskId);
			const resolved = await resolve(id);
			try {
				const diff = await options.client.diff(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					{
						signal,
					},
				);
				return { connection: resolved.connection, taskId: validatedTask, diff };
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async command(
			id: string,
			taskId: string,
			command: DaemonCommand,
			actor: string,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			accepted: boolean;
		}> {
			if (!daemonCommands.includes(command))
				throw new DaemonRegistryError(
					404,
					"unknown_command",
					"Unsupported daemon command.",
				);
			const validatedTask = validatedTaskID(taskId);
			if (!actor || actor.length > 64)
				throw new DaemonRegistryError(
					400,
					"invalid_actor",
					"Command actor is invalid.",
				);
			const resolved = await resolve(id);
			try {
				const result = await options.client.command(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					command,
					{
						actor,
						signal,
					},
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					accepted: result.accepted,
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async events(
			id: string,
			taskId: string,
			query: EventQuery,
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			 events: DaemonEvent[];
			 cursor: number;
			 format_version: number;
		}> {
			const validatedTask = validatedTaskID(taskId);
			const validatedQuery = validatedEventQuery(query);
			const resolved = await resolve(id);
			try {
				const result = await options.client.events(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					validatedQuery,
					{
						signal,
					},
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					events: result.events,
					cursor: result.cursor,
					format_version: result.format_version,
				};
			} catch (error) {
				throw remapIdentityMismatch(error);
			}
		},
		async eventStream(
			id: string,
			taskId: string,
			cursor: { after?: number; lastEventID?: string },
			signal?: AbortSignal,
		): Promise<{
			connection: DaemonConnection;
			taskId: string;
			upstream: Response;
		}> {
			const validatedTask = validatedTaskID(taskId);
			if (
				cursor.after !== undefined &&
				(!Number.isInteger(cursor.after) || cursor.after < 0)
			) {
				throw new DaemonRegistryError(
					400,
					"invalid_cursor",
					"Event cursor must be a non-negative integer.",
				);
			}
			if (
				cursor.lastEventID !== undefined &&
				!/^\d+$/.test(cursor.lastEventID)
			) {
				throw new DaemonRegistryError(
					400,
					"invalid_cursor",
					"Last event ID must be a non-negative integer.",
				);
			}
			const resolved = await resolve(id);
			try {
				const upstream = await options.client.eventStream(
					resolved.endpoint,
					resolved.credential,
					validatedTask,
					cursor,
					{
						signal,
					},
				);
				return {
					connection: resolved.connection,
					taskId: validatedTask,
					upstream,
				};
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
			store: createDaemonRegistryStore(
				getDatabasePool(environment.DATABASE_URL),
			),
			client: createDaemonClient(),
			allowedOrigins: parseAllowedDaemonOrigins(
				environment.DAEMON_ALLOWED_ORIGINS,
			),
		});
	}
	return cached;
}
