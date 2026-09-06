import { parseAllowedDaemonOrigins } from "./endpoint-policy.ts";

type Environment = Readonly<Record<string, string | undefined>>;

const deploymentKeys = [
  "APPLICATION_ORIGIN",
  "DATABASE_URL",
  "INITIAL_USER_LOGIN",
  "INITIAL_USER_PASSWORD",
  "DAEMON_CREDENTIAL_KEY",
  "DAEMON_ALLOWED_ORIGINS",
] as const;

type DeploymentKey = (typeof deploymentKeys)[number];
export type DeploymentEnvironment = Record<DeploymentKey, string>;
export type AuthenticationEnvironment = Pick<DeploymentEnvironment, "APPLICATION_ORIGIN" | "DATABASE_URL" | "INITIAL_USER_LOGIN" | "INITIAL_USER_PASSWORD">;
export type EnvironmentIssue = { variable: DeploymentKey; message: string };
type Validation =
  | { ok: true; values: DeploymentEnvironment }
  | { ok: false; issues: EnvironmentIssue[] };

function parseURL(value: string): URL | undefined {
  try {
    return new URL(value);
  } catch {
    return undefined;
  }
}

export function validateDatabaseURL(value: string | undefined): string | undefined {
  if (!value?.trim()) return "Set a PostgreSQL connection URL.";
  const url = parseURL(value);
  if (
    /[\s\\]/.test(value) ||
    !url ||
    !["postgres:", "postgresql:"].includes(url.protocol) ||
    !url.hostname ||
    url.pathname.length < 2 ||
    url.hash
  ) {
    return "Use a postgres:// or postgresql:// URL with a host and database name.";
  }
  return undefined;
}

// Call at runtime only. Errors contain fixed guidance, never supplied values.
export function validateEnvironment(environment: Environment): Validation {
  const issues: EnvironmentIssue[] = [];
  const values = {} as DeploymentEnvironment;
  for (const variable of deploymentKeys) {
    const value = environment[variable];
    if (!value?.trim()) {
      issues.push({ variable, message: "Set this server-side deployment variable." });
    } else if (value !== value.trim()) {
      issues.push({ variable, message: "Remove leading or trailing whitespace." });
    } else {
      values[variable] = value;
    }
  }

  if (values.DATABASE_URL) {
    const message = validateDatabaseURL(values.DATABASE_URL);
    if (message) issues.push({ variable: "DATABASE_URL", message });
  }
  if (values.APPLICATION_ORIGIN) {
    const url = parseURL(values.APPLICATION_ORIGIN);
    const local = url && ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname);
    if (
      !url ||
      /[\s\\]/.test(values.APPLICATION_ORIGIN) ||
      !["http:", "https:"].includes(url.protocol) ||
      url.username || url.password || url.search || url.hash ||
      url.pathname !== "/" ||
      ![url.origin, `${url.origin}/`].includes(values.APPLICATION_ORIGIN) ||
      (url.protocol === "http:" && !local)
    ) {
      issues.push({
        variable: "APPLICATION_ORIGIN",
        message: "Use an HTTPS origin without credentials, path, query, or fragment. HTTP is allowed only on loopback outside production.",
      });
    }
  }
  if (values.INITIAL_USER_LOGIN && !/^[a-zA-Z0-9._-]{1,64}$/.test(values.INITIAL_USER_LOGIN)) {
    issues.push({ variable: "INITIAL_USER_LOGIN", message: "Use 1-64 letters, numbers, dots, underscores, or hyphens." });
  }
  if (values.INITIAL_USER_PASSWORD && values.INITIAL_USER_PASSWORD.length < 12) {
    issues.push({ variable: "INITIAL_USER_PASSWORD", message: "Use a password of at least 12 characters." });
  }
  if (values.DAEMON_CREDENTIAL_KEY && !/^[a-fA-F0-9]{64}$/.test(values.DAEMON_CREDENTIAL_KEY)) {
    issues.push({ variable: "DAEMON_CREDENTIAL_KEY", message: "Use exactly 64 hexadecimal characters (32 random bytes)." });
  }
  if (values.DAEMON_ALLOWED_ORIGINS) {
    try {
      parseAllowedDaemonOrigins(values.DAEMON_ALLOWED_ORIGINS);
    } catch {
      issues.push({ variable: "DAEMON_ALLOWED_ORIGINS", message: "Use comma-separated HTTP(S) origins with IP-address hosts; localhost is also allowed." });
    }
  }
  return issues.length ? { ok: false, issues } : { ok: true, values };
}

export function readDeploymentEnvironment(environment: Environment): DeploymentEnvironment {
  const validation = validateEnvironment(environment);
  if (!validation.ok) {
    throw new Error(`Invalid deployment variables: ${validation.issues.map((issue) => issue.variable).join(", ")}.`);
  }
  return validation.values;
}

export function validateAuthenticationEnvironment(environment: Environment):
  | { ok: true; values: AuthenticationEnvironment }
  | { ok: false; issues: EnvironmentIssue[] } {
  const validation = validateEnvironment({
    ...environment,
    DAEMON_CREDENTIAL_KEY: "00".repeat(32),
    DAEMON_ALLOWED_ORIGINS: "http://127.0.0.1:8080",
  });
  if (!validation.ok) return validation;
  const { APPLICATION_ORIGIN, DATABASE_URL, INITIAL_USER_LOGIN, INITIAL_USER_PASSWORD } = validation.values;
  return { ok: true, values: { APPLICATION_ORIGIN, DATABASE_URL, INITIAL_USER_LOGIN, INITIAL_USER_PASSWORD } };
}

export function readAuthenticationEnvironment(environment: Environment): AuthenticationEnvironment {
  const validation = validateAuthenticationEnvironment(environment);
  if (!validation.ok) {
    throw new Error(`Invalid authentication variables: ${validation.issues.map((issue) => issue.variable).join(", ")}.`);
  }
  return validation.values;
}
