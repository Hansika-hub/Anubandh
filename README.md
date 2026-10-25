# Anubandh 

**Programmable B2B payment deals with built-in MSME compliance and cash forecasting on Drunix.**

> Money moves only when the goods do, and the treasurer knows before it happens.

Built for the DRUNIX Hackathon.

## Problem

High-value B2B payments run on rails that cannot check conditions like goods received or quality passed. Approvals live outside the payment, partial deliveries are handled by slow manual debit notes, small suppliers are paid late without proof of the acceptance date, and treasurers find cash shortfalls only on the day.

## Solution

Anubandh turns every B2B purchase into a **Smart Payment Deal** on Drunix. Money is released automatically, line by line, when agreed conditions are verified by the right parties.

- **Line-level partial release:** if 80 of 100 units pass inspection, 80% is released at once and the rest is held.
- **MSE payment guard:** tracks the 15/45-day legal payment clock and marks deemed acceptance automatically.
- **Multi-party approvals:** each deal has its own on-chain endorsement policy.
- **Reliable event intake:** gateway, queue and idempotent workers prevent duplicate releases.
- **Liquidity forecast:** LightGBM models give P10/P50/P90 cash bands with ranked actions.

## Tech Stack

Drunix (Go chaincode), Kafka/Redpanda, PostgreSQL, Python (LightGBM, FastAPI), React + TypeScript, Docker Compose.

## Simulated in the Prototype

Bank rails, warehouse goods receipt, GST e-invoice check and Udyam lookup are simulated. Forecast models are trained on synthetic data.

## Getting Started

Needs Docker with Compose v2 (and `make`; on Windows use Git Bash or WSL).

```bash
git clone https://github.com/Hansika-hub/Anubandh.git
cd Anubandh
make up        # copies deploy/.env.example to deploy/.env, builds and starts all 12 services
```

Open **http://localhost:3000** and pick a persona in the top-right corner. [docs/demo-script.md](docs/demo-script.md) walks through the demo. `make demo` runs the same story from the terminal.

By default the gateway uses its in-memory chaincode (`FABRIC_MOCK=true`), so the whole stack runs on a laptop. For a real Drunix network: `make network && make cc-deploy`, then set `FABRIC_MOCK=false` (see [network/README.md](network/README.md)).

| Service | Port | Owner | Code |
|---|---|---|---|
| Web UI (nginx; proxies the APIs) | 3000 | M5 | [web/](web) |
| Gateway REST + SSE | 8080 | M2 | [gateway/](gateway), [db/](db) |
| Ingest webhooks + workers | 8081 | M3 | [ingest/](ingest) |
| Forecast | 8090 | M4 | [forecast/](forecast) |
| sim-bank, sim-grn, sim-gst, sim-udyam | 8101–8104 | M5 | [simulators/](simulators) |
| Chaincode `deal` | (Drunix) | M1 | [chaincode/deal/](chaincode/deal) |

Architecture: [docs/architecture.md](docs/architecture.md). The frozen contracts are in [BUILD.md](BUILD.md).

## Tests

| Command | What it proves |
|---|---|
| `make test-burst` | 500 duplicate and out-of-order events, zero double releases |
| `make test-partial` | 80 of 100 units accepted: exactly ₹8L released and settled by the bank, ₹2L held, the full PO paid once after replacements |
| `make test-deadline` | 15 days with no written objection: deemed acceptance, the MSE clock, and the PLANT_HEAD + CFO policy for ₹60L (mock chain only, since a real network's clock cannot be moved) |
| `make test-e2e` | the above, plus bank-outage retries, out-of-order QC and the web proxy |
| `make test-forecast` | forecast unit tests and walk-forward evaluation against baselines |
| `make test-cc`, `make test-ingest`, `make test-sims`, `make test-web` | per-module suites (`cd gateway && go test ./...` for the gateway) |
