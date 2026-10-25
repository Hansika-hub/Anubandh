# Anubandh top-level Makefile. Owner: M3 (BUILD.md section 13).
# Targets whose owner has not landed yet fail loudly instead of pretending to pass.
COMPOSE := docker compose -f deploy/docker-compose.yml --env-file deploy/.env
PSQL    := $(COMPOSE) exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -U $$POSTGRES_USER -d $$POSTGRES_DB'

# ingest-tidy: see below
.PHONY: ingest-tidy help env up-infra up down network network-down cc-deploy migrate seed \
        test-cc test-ingest test-burst test-partial test-deadline test-forecast test-e2e test-sims test-web demo lint-network

help:
	@grep -E '^[a-z-]+:' Makefile | cut -d: -f1 | tr '\n' ' '; echo

env:
	@test -f deploy/.env || (cp deploy/.env.example deploy/.env && echo "created deploy/.env from .env.example")

up-infra: env
	$(COMPOSE) up -d postgres redpanda redpanda-init
	@$(MAKE) --no-print-directory migrate

up: env
	$(COMPOSE) up -d --build
	@echo "web :3000   gateway :8080   ingest-api :8081   forecast :8090   sims :8101-8104"

down:
	$(COMPOSE) down

network:
	./network/scripts/network.sh up

network-down:
	./network/scripts/network.sh down

cc-deploy:
	./network/scripts/deploy-cc.sh

# Applies db/migrations/*.sql in order. All migrations are idempotent (M2).
migrate: env
	@for f in $$(ls db/migrations/*.sql | sort); do \
	  echo "migrate $$f"; $(PSQL) < $$f || exit 1; \
	done

seed:
	@test -f db/seed.sql && $(PSQL) < db/seed.sql || echo "seed: db/seed.sql not present (owner M2)"

test-cc:
	cd chaincode/deal && go test ./...

test-ingest:
	cd ingest && go vet ./... && go test -race -count=1 ./...

# Needs network. Resolves franz-go + pgx and writes ingest/go.mod + go.sum; commit the result.
ingest-tidy:
	cd ingest && go get github.com/twmb/franz-go@v1.18.1 github.com/jackc/pgx/v5@v5.7.2 && go mod tidy && go vet -tags adapters ./...

test-burst:
	cd tests/burst && go vet ./... && go test -race -count=1 -v ./...

# End-to-end (M5): need `make up`. The deadline test needs the mock chain (FABRIC_MOCK=true): a
# real network's transaction clock cannot be fast-forwarded 15 days.
E2E := set -a && . deploy/.env && set +a && python3 -m pytest tests/e2e

test-e2e: env
	$(E2E) -v

test-partial: env
	$(E2E) -v -m partial

test-deadline: env
	$(E2E) -v -m deadline

test-forecast:
	python3 -m pytest tests/forecast -q

test-sims:
	python3 -m pytest simulators/tests -q && cd simulators && ruff check . && ruff format --check .

test-web:
	cd web && npm ci --no-audit --no-fund && npm run lint && npm test && npm run build

# Scripted live demo (BUILD.md demo scenario). Needs `make up` first.
demo:
	./tests/burst/demo.sh

lint-network:
	shellcheck -x -P SCRIPTDIR network/scripts/*.sh tests/burst/*.sh
	python3 -c "import json;json.load(open('network/config/collections_config.json'))"
