# Anubandh architecture

Anubandh turns a B2B purchase order into a **Smart Payment Deal** on Drunix, NPCI's Hyperledger Fabric 2.5–compatible network. The bank locks the PO value as a lien. Money is released line by line when the warehouse's goods receipt and quality check, and the approvers the deal names, say so. The MSMED Act payment clock is enforced on chain, and a forecast shows the treasurer what all this does to cash.

## Components

```mermaid
flowchart LR
  subgraph Parties
    WMS[Warehouse / ERP<br/>sim-grn]
    GST[GST IRP<br/>sim-gst]
    UI[Web UI :3000<br/>buyer · supplier · bank · treasury · MSE · auditor]
  end
  subgraph Intake["Ingest (M3) :8081"]
    API[webhook API<br/>HMAC · schema · rate limit]
    K[(Redpanda<br/>inbound · retry · DLQ)]
    W[workers<br/>idempotency · requeue · DLQ]
  end
  subgraph Core["Gateway (M2) :8080"]
    GW[REST /api/v1<br/>internal submit]
    L[listener<br/>checkpointed, exactly-once]
    OB[payment outbox]
    S[deadline scheduler]
  end
  subgraph Drunix["Drunix network (M3) · chaincode deal (M1)"]
    CC[deal chaincode<br/>state machine · policies · MSE clock]
    PDC[(private data<br/>line prices)]
  end
  PG[(PostgreSQL<br/>read model)]
  BANK[Bank rails<br/>sim-bank :8101]
  FC[Forecast (M4) :8090<br/>LightGBM · Monte Carlo]

  WMS -- signed webhooks --> API --> K --> W -- /internal/v1/submit --> GW
  GST -- INVOICE --> API
  UI -- REST + SSE --> GW
  UI --> FC
  GW -- Fabric Gateway SDK --> CC
  CC --- PDC
  CC -- chaincode events --> L --> PG
  L --> OB -- instructions --> BANK
  OB -- ConfirmSettlement --> CC
  S -- TickDeadlines --> CC
  FC -. read model .-> PG
```

| Layer | Owner | Code | Key guarantee |
|---|---|---|---|
| Chaincode `deal` | M1 | `chaincode/deal` | the state machine and every money rule live on chain; released + held never exceeds the lien |
| Network | M3 | `network/` | 4 orgs (Buyer, Supplier, Bank, Auditor), Fabric CA with `role`/`supplierId` cert attributes, a private data collection for line prices |
| Gateway | M2 | `gateway/`, `db/` | signs as the acting user; MVCC retry; exactly-once projection (`tx_id, event_name, event_seq`); transactional outbox |
| Ingest | M3 | `ingest/` | HMAC-verified webhooks, Kafka, idempotent workers that requeue out-of-order events and park poison ones in a DLQ |
| Forecast | M4 | `forecast/` | P10/P50/P90 cash band; ranked actions that never defer an MSE payment |
| Web, simulators | M5 | `web/`, `simulators/` | one origin for the browser (nginx proxy); simulators that speak the real contracts |

## One delivery, end to end

1. **Deal.** Procurement creates the deal (`POST /api/v1/deals`). Line prices travel in the transient map into the private collection, and the chaincode sets key-level endorsement: Buyer and Bank must both endorse writes to the deal.
2. **Lien.** The bank confirms the lien (`ConfirmLien`) for exactly the PO value. Until then the deal accepts no goods.
3. **Goods receipt.** The warehouse sends a signed webhook. Ingest verifies the HMAC, validates the schema and the idempotency key, and publishes to Kafka. A worker submits `PostGoodsReceipt` through the gateway. Duplicates are dropped by key at three layers: ingest's `processed_keys`, the chaincode's idempotency keys, and the listener's projection keys.
4. **Quality check.** `RecordQualityCheck` accepts 80 units and holds 20. One transaction emits several events in a single `TxEventBatch`. The listener unwraps it and projects each event exactly once.
5. **Approval and release.** The approval band for ₹8L is PLANT_HEAD. Once that approval is signed, the gateway calls `ReleaseTranche` with a deterministic idempotency key. In the same database transaction as that event, the listener writes an outbox row. The outbox picks the rail (NEFT here), pays through the bank, polls until SETTLED, and confirms the settlement back on chain.
6. **Held portion.** The 20 rejected units leave the held state through one exit: a replacement shipment (a new tranche), a credit note or price adjustment signed by both parties, or a dispute decided by the bank's arbiter.
7. **MSME guard.** For MICRO and SMALL suppliers the chaincode keeps the MSMED Act §15 clock: 15 days, or up to 45 with a written agreement, counted from acceptance. If the buyer raises no written objection within 15 days of delivery, the scheduler's `TickDeadlines` deems the goods accepted.

## Why the numbers can be trusted

- **No double payment.** The release idempotency key is deterministic (`gateway:release-<deal>-<n>`), the chaincode refuses a second release (`ERR_ALREADY_RELEASED`), and bank instruction ids are deterministic (the bank dedupes them). The burst test sends 500 duplicate and shuffled events: 0 double releases.
- **No lost event.** The listener checkpoints the block and transaction it reached in the same database transaction as the projection, so a crash replays without duplicates. Ingest moves events it cannot process to the DLQ, where an operator can replay them.
- **No hidden prices.** Unit prices exist only in the private data collection. The auditor sees quantities and values, not prices.
- **Audit trail.** Every state change is a ledger transaction (`GET /deals/{id}/history`), and the compliance report (`GET /mse/compliance-report`) is built from it.

## Running modes

| Mode | How | Use |
|---|---|---|
| Demo fallback | `FABRIC_MOCK=true` (the default in `deploy/.env.example`) | `make up`: the whole stack on a laptop; the gateway's in-memory chaincode mirrors `chaincode/deal` and can fast-forward its clock |
| Drunix | `make network && make cc-deploy`, then `FABRIC_MOCK=false` with `network/crypto/gateway.env` | the real network: 4 orgs, Fabric CA, private data |

Contracts: `BUILD.md` (frozen), `gateway/openapi.yaml`, `chaincode/deal/CONTRACT_CHANGE_001.md`. Design notes: `gateway/DESIGN.md`.
