-- Application-owned daemon registrations. Credentials are encrypted before
-- insertion and are never returned by browser-facing queries.

CREATE TABLE factory_application.daemon_connection (
  id text PRIMARY KEY,
  name text NOT NULL UNIQUE,
  endpoint text NOT NULL UNIQUE,
  daemon_identity char(32) NOT NULL UNIQUE,
  credential_ciphertext text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
