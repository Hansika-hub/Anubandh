#!/usr/bin/env bash
# `make demo`: run the scripted live demo through the ingest path against a running stack.
# Prereq: `make up` with FABRIC_MOCK=true. Secrets are read from deploy/.env.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
envfile="$root/deploy/.env"
[ -f "$envfile" ] || { echo "deploy/.env missing: run 'make up' first" >&2; exit 1; }
set -a
# shellcheck disable=SC1090
. "$envfile"
set +a
for u in "${GATEWAY_PUBLIC_URL:-http://localhost:8080}/health" "${INGEST_PUBLIC_URL:-http://localhost:8081}/ingest/v1/health"; do
  curl -fsS --max-time 5 "$u" >/dev/null || { echo "not reachable: $u  (is the stack up? try: make up)" >&2; exit 1; }
done
cd "$root/tests/burst"
exec go run ./cmd/demo "$@"
