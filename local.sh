#!/usr/bin/env bash
# Local run: configuration + postgres + migrations + application.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

ENV_FILE="$ROOT/local.env"

if [[ ! -f "$ENV_FILE" ]]; then
  umask 077
  postgres_password="$(openssl rand -hex 32)"
  initial_user_password="$(openssl rand -hex 24)"
  daemon_credential_key="$(openssl rand -hex 32)"

  cat > "$ENV_FILE" <<EOF
POSTGRES_USER=factory
POSTGRES_PASSWORD=$postgres_password
POSTGRES_DB=factory_application
POSTGRES_PORT=5432
APPLICATION_ORIGIN=http://localhost:3000
DATABASE_URL=postgresql://factory:$postgres_password@localhost:5432/factory_application
INITIAL_USER_LOGIN=owner
INITIAL_USER_PASSWORD=$initial_user_password
DAEMON_CREDENTIAL_KEY=$daemon_credential_key
DAEMON_ALLOWED_ORIGINS=http://127.0.0.1:8080
EOF
  echo "Created $ENV_FILE with generated local credentials."
fi

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

: "${POSTGRES_USER:?Set POSTGRES_USER in local.env}"
: "${POSTGRES_PASSWORD:?Set POSTGRES_PASSWORD in local.env}"
: "${POSTGRES_DB:?Set POSTGRES_DB in local.env}"
: "${DATABASE_URL:?Set DATABASE_URL in local.env}"
: "${INITIAL_USER_LOGIN:?Set INITIAL_USER_LOGIN in local.env}"
: "${INITIAL_USER_PASSWORD:?Set INITIAL_USER_PASSWORD in local.env}"
: "${DAEMON_CREDENTIAL_KEY:?Set DAEMON_CREDENTIAL_KEY in local.env}"
: "${DAEMON_ALLOWED_ORIGINS:?Set DAEMON_ALLOWED_ORIGINS in local.env}"

docker compose up -d postgres

echo "Waiting for postgres..."
until docker compose exec -T postgres pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null 2>&1; do
  sleep 1
done

# POSTGRES_PASSWORD only initializes a new volume; keep an existing local role in sync.
docker compose exec -T postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  --set=password="$POSTGRES_PASSWORD" >/dev/null <<'SQL'
SELECT format('ALTER ROLE %I WITH PASSWORD %L', current_user, :'password') \gexec
SQL

npm run migrations --workspace @software-factory/application

echo "Daemon not running? Start it in another terminal: go -C daemon run . (http://127.0.0.1:8080)"
npm run dev --workspace @software-factory/application
