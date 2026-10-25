# tests/e2e (M5): the demo scenario against a running stack

These tests drive the stack the way production traffic does: `sim-grn` / `sim-gst` → ingest (HMAC, Kafka, idempotent workers) → gateway → chaincode → read model → bank outbox → `sim-bank`. They only read state through the gateway API, and skip themselves when the stack is down.

```bash
make up                                   # or: docker compose -f deploy/docker-compose.yml --env-file deploy/.env up -d --build
pip install -r tests/e2e/requirements.txt
set -a; . deploy/.env; set +a             # INTERNAL_TOKEN for the mock-clock test
python -m pytest tests/e2e -v             # everything
python -m pytest tests/e2e -m partial     # make test-partial
python -m pytest tests/e2e -m deadline    # make test-deadline
```

| Test | Covers |
|---|---|
| `test_partial_release_80_of_100` | demo steps 1–6: GRN ×3 shuffled counts once, QC 80/20, ₹8L released over NEFT and settled by sim-bank, 20 replacements, full PO paid exactly once |
| `test_deemed_acceptance_and_two_approver_policy` | step 7: 15-day MICRO deal deemed accepted, MSE clock, ₹60L needs PLANT_HEAD + CFO, compliance report |
| `test_bank_outage_is_retried_by_the_outbox` | sim-bank refuses two payment calls; the outbox retries until confirmed; GST invoice event |
| `test_out_of_order_qc_waits_for_its_grn` | a QC before its GRN is requeued and applied once the GRN lands |

**The deadline test runs on the mock chain only.** A Fabric transaction's timestamp comes from the client, so a real Drunix network's clock cannot be moved forward 15 days; with `FABRIC_MOCK=false` that test skips itself. URLs default to `localhost`; override them with `E2E_GATEWAY_URL`, `E2E_SIM_GRN_URL`, `E2E_SIM_BANK_URL`, `E2E_SIM_GST_URL` and `E2E_WEB_URL`.
