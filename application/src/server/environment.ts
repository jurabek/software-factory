type Environment = Readonly<Record<string, string | undefined>>;

const deploymentKeys = [
  "APPLICATION_ORIGIN",
  "DATABASE_URL",
  "INITIAL_OWNER_EMAIL",
  "BETTER_AUTH_SECRET",
  "GITHUB_CLIENT_ID",
  "GITHUB_CLIENT_SECRET",
  "GOOGLE_CLIENT_ID",
  "GOOGLE_CLIENT_SECRET",
] as const;

type DeploymentKey = (typeof deploymentKeys)[number];
type DeploymentEnvironment = Record<DeploymentKey, string>;
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
      (url.protocol === "http:" && (environment.NODE_ENV === "production" || !local))
    ) {
      issues.push({
        variable: "APPLICATION_ORIGIN",
        message: "Use an HTTPS origin without credentials, path, query, or fragment. HTTP is allowed only on loopback outside production.",
      });
    }
  }
  if (values.INITIAL_OWNER_EMAIL && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(values.INITIAL_OWNER_EMAIL)) {
    issues.push({ variable: "INITIAL_OWNER_EMAIL", message: "Set a valid initial-owner email address. Identity verification is not implemented yet." });
  }
  if (values.BETTER_AUTH_SECRET && values.BETTER_AUTH_SECRET.length < 32) {
    issues.push({ variable: "BETTER_AUTH_SECRET", message: "Use a randomly generated secret of at least 32 characters." });
  }
  return issues.length ? { ok: false, issues } : { ok: true, values };
}
