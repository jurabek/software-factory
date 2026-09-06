import { createHash, randomBytes, timingSafeEqual } from "node:crypto";
import type { Pool } from "pg";
import { getDatabasePool } from "./database.ts";
import { readAuthenticationEnvironment } from "./environment.ts";

export const sessionCookieName = "factory_session";
export const sessionLifetimeSeconds = 7 * 24 * 60 * 60;
const failedLoginDelayMilliseconds = 750;

export type AuthStore = {
  createSession(tokenDigest: string, expiresAt: Date): Promise<void>;
  hasSession(tokenDigest: string, now: Date): Promise<boolean>;
  deleteSession(tokenDigest: string): Promise<void>;
  deleteExpiredSessions(now: Date): Promise<void>;
};

type AuthenticationOptions = {
  login: string;
  password: string;
  store: AuthStore;
  now?: () => Date;
  createToken?: () => string;
  delayAfterFailure?: () => Promise<void>;
};

function digest(value: string): Buffer {
  return createHash("sha256").update(value).digest();
}

function matchesSecret(provided: string, expected: string): boolean {
  return timingSafeEqual(digest(provided), digest(expected));
}

export function sessionTokenFromCookie(cookieHeader: string | null): string | undefined {
  for (const cookie of cookieHeader?.split(";") ?? []) {
    const [name, ...parts] = cookie.trim().split("=");
    if (name === sessionCookieName) return parts.join("=") || undefined;
  }
  return undefined;
}

export function createAuthentication(options: AuthenticationOptions) {
  const now = options.now ?? (() => new Date());
  const createToken = options.createToken ?? (() => randomBytes(32).toString("base64url"));
  const delayAfterFailure = options.delayAfterFailure ?? (() => new Promise((resolve) => setTimeout(resolve, failedLoginDelayMilliseconds)));
  return {
    async login(login: string, password: string): Promise<string | null> {
      if (!matchesSecret(login, options.login) || !matchesSecret(password, options.password)) {
        await delayAfterFailure();
        return null;
      }
      const token = createToken();
      const current = now();
      await options.store.deleteExpiredSessions(current);
      await options.store.createSession(
        digest(token).toString("hex"),
        new Date(current.getTime() + sessionLifetimeSeconds * 1000),
      );
      return token;
    },
    async current(token: string | undefined): Promise<{ login: string } | null> {
      if (!token) return null;
      const valid = await options.store.hasSession(digest(token).toString("hex"), now());
      return valid ? { login: options.login } : null;
    },
    async logout(token: string | undefined): Promise<void> {
      if (token) await options.store.deleteSession(digest(token).toString("hex"));
    },
  };
}

export type Authentication = ReturnType<typeof createAuthentication>;

export function createAuthStore(pool: Pool): AuthStore {
  return {
    async createSession(tokenDigest, expiresAt) {
      await pool.query("INSERT INTO owner_session (token_digest, expires_at, created_at) VALUES ($1, $2, $3)", [
        tokenDigest,
        expiresAt.toISOString(),
        new Date().toISOString(),
      ]);
    },
    async hasSession(tokenDigest, now) {
      const result = await pool.query("SELECT 1 FROM owner_session WHERE token_digest = $1 AND expires_at > $2", [
        tokenDigest,
        now.toISOString(),
      ]);
      return result.rowCount === 1;
    },
    async deleteSession(tokenDigest) {
      await pool.query("DELETE FROM owner_session WHERE token_digest = $1", [tokenDigest]);
    },
    async deleteExpiredSessions(now) {
      await pool.query("DELETE FROM owner_session WHERE expires_at <= $1", [now.toISOString()]);
    },
  };
}

function createInstance(): Authentication {
  const environment = readAuthenticationEnvironment(process.env);
  const pool = getDatabasePool(environment.DATABASE_URL);
  return createAuthentication({
    login: environment.INITIAL_USER_LOGIN,
    password: environment.INITIAL_USER_PASSWORD,
    store: createAuthStore(pool),
  });
}

// Lazy singleton: importing this module never throws, so pages can render an
// actionable setup state when deployment variables are missing. Every auth
// call fails closed with the missing-variable list instead of half-config.
let cached: Authentication | undefined;

export function getAuth(): Authentication {
  if (!cached) cached = createInstance();
  return cached;
}
