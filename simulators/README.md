# simulators (M5): bank, warehouse, GST and Udyam stand-ins

Python 3.12 · FastAPI. Contract: BUILD.md §11 (`/health` on each). One image runs all four; `SIM` picks the app.

| Service | Port | Endpoints |
|---|---|---|
| `sim-bank` | 8101 | `POST /bank/v1/liens`, `POST /bank/v1/payments` (same `instructionId` → same `bankRef`), `GET /bank/v1/payments/{bankRef}` (ACCEPTED → SETTLED after `SETTLE_AFTER_SECONDS`, default 2), `POST /bank/v1/_chaos {failNext, failSettlementNext}` |
| `sim-grn` | 8102 | `POST /sim/grn/emit`, `POST /sim/qc/emit` `{dealId, trancheNo, lines/results, dupes, shuffle, eventId?}` → signed webhooks to ingest |
| `sim-gst` | 8103 | `GET /gst/v1/irn/{irn}` (IRNs starting `BAD` are invalid); `POST /sim/invoice/emit {dealId, irn}` → INVOICE webhook |
| `sim-udyam` | 8104 | `GET /udyam/v1/{udyamNo}`, `GET /udyam/v1/by-supplier/{supplierId}` |

**Run:** `pip install -r simulators/requirements.txt`, then from the repo root `uvicorn simulators.bank.app:app --port 8101` (same for `grn`, `gst`, `udyam`).
**Docker:** `docker build -f simulators/Dockerfile -t anubandh/simulators .` and `docker run -e SIM=grn -e PORT=8102 ...`.
**Tests:** `pip install -r simulators/requirements-dev.txt && python -m pytest simulators/tests && (cd simulators && ruff check . && ruff format --check .)`.

| Env | Used by | Meaning |
|---|---|---|
| `INGEST_URL` | grn, gst | ingest base URL (default `http://ingest-api:8081`) |
| `HMAC_SECRET_WAREHOUSE` / `HMAC_SECRET_GST` | grn / gst | webhook signing secrets, the same values ingest verifies with |
| `SETTLE_AFTER_SECONDS` | bank | seconds from ACCEPTED to SETTLED |

**Fixtures:** `fixtures/parties.json` holds BUYER-001, SUP-001..SUP-050 (SUP-007 SMALL with a 45-day written agreement, SUP-012 MICRO on 15 days), CUS-001..CUS-030 and the two demo deals. It is deterministic; regenerate it with `python -m simulators.fixtures.generate`. `fixtures/wiremock/mappings/` holds static WireMock stubs for the bank, GST and Udyam APIs (BUILD.md §14.4).
