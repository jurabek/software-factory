-- Initial-user password login. Credentials remain server-side environment
-- values; only hashes of random session tokens are persisted.
-- Never edit applied migrations; add the next numbered file instead.

CREATE TABLE factory_application.owner_session (
  token_digest char(64) PRIMARY KEY,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX owner_session_expires_at_idx
  ON factory_application.owner_session (expires_at);
