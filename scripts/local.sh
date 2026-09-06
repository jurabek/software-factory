#!/usr/bin/env bash
# Local run: postgres (compose) + migrations + application (next dev).
# Daemon runs separately: go -C daemon run .
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

POSTGRES_USER="${POSTGRES_USER:-factory}"
POSTGRES_DB="${POSTGRES_DB:-factory_application}"

docker compose up -d postgres

echo "Waiting for postgres..."
until docker compose exec -T postgres pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null 2>&1; do
  sleep 1
done

npm run migrations --workspace @software-factory/application

echo "Daemon not running? Start it in another terminal: go -C daemon run . (http://127.0.0.1:8080)"
npm run dev --workspace @software-factory/application
