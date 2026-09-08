-- Single portable schema. Plain SQL only: no schemas, no server defaults.
-- Timestamps are ISO-8601 UTC text set by the application, so string
-- comparison orders chronologically and works on any database.
-- Only login sessions are stored; raw tokens and passwords never touch the DB.
CREATE TABLE IF NOT EXISTS owner_session (
  token_digest TEXT PRIMARY KEY,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS owner_session_expires_at_idx ON owner_session (expires_at);

-- Daemon registrations. Credentials are encrypted before insertion and are
-- never returned by browser-facing queries.
CREATE TABLE IF NOT EXISTS daemon_connection (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  endpoint TEXT NOT NULL UNIQUE,
  daemon_identity TEXT NOT NULL UNIQUE,
  credential_ciphertext TEXT NOT NULL,
  created_at TEXT NOT NULL
);
