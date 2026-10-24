# gateway (M2): chain gateway, read model, bank outbox, REST :8080
Go 1.26 · Fabric Gateway SDK (Drunix-compatible) · PostgreSQL · chi. API: [openapi.yaml](openapi.yaml). Design and evidence: [DESIGN.md](DESIGN.md).

**Run without a Drunix network (demo fallback):** `PG_DSN=postgres://u:p@localhost:5432/anubandh?sslmode=disable FABRIC_MOCK=true go run ./cmd/gateway`
**Docker** (context = repo root): `docker build -f gateway/Dockerfile -t anubandh/gateway .` · migrations only: `gateway -migrate-only`
**Smoke** (demo scenario, needs mock + sim-bank): `go run ./cmd/smoke -url http://localhost:8080`
**Tests:** `go test ./...` (unit). End-to-end with PostgreSQL: `PG_TEST_DSN=postgres://... go test ./internal/e2e/` (wipes that DB).

| Env | Default | Meaning |
|---|---|---|
| `PG_DSN` | required | PostgreSQL read model |
| `FABRIC_MOCK` / `FABRIC_MOCK_STATE` | `false` / unset | in-memory chaincode / file that persists it across restarts |
| `FABRIC_CHANNEL` / `FABRIC_CC_NAME` | `anubandh-ch` / `deal` | channel and chaincode |
| `GATEWAY_USERS_FILE` / `FABRIC_CRYPTO_DIR` | `config/users.json` | X-User → enrolled identity, org peer endpoints (`FABRIC_PEER_<ORG>`, `FABRIC_HOST_<ORG>`) |
| `SIM_BANK_URL` / `BANK_SETTLE_ON_ACCEPT` | `http://sim-bank:8101` / `false` | bank rails; `true` = treat ACCEPTED as settled |
| `MIGRATIONS_DIR` / `MIGRATE_ON_START` | `../db/migrations` / `true` | idempotent SQL migrations |
| `TICK_INTERVAL` / `OUTBOX_POLL_INTERVAL` / `OUTBOX_MAX_ATTEMPTS` | `1m` / `1s` / `10` | scheduler and payment outbox |
| `LISTENER_ID` / `LISTENER_START_BLOCK` | `gateway` / `0` | checkpoint key / first block when no checkpoint |
| `CORS_ORIGINS` / `INTERNAL_TOKEN` / `AUTO_RELEASE` | `http://localhost:3000` / unset / `true` | web origin / guards `/internal/v1/*` / release on approval |
| `GATEWAY_ADDR` / `LOG_LEVEL` | `:8080` / `info` | listen address / JSON log level |
