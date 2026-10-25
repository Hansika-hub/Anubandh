# Gateway design notes (M2)

What the gateway guarantees, why it can run against NPCI's Drunix unchanged, and what the other
owners need to know at merge time. Every external claim below links to its source.

## 1. Drunix compatibility: evidence, not assumption

| Fact | Source | Consequence in this code |
|---|---|---|
| Drunix is "an enhanced fork of Hyperledger Fabric", "Backwards compatible with HLF v2.5.x" | [npci/drunix README](https://github.com/npci/drunix) | Standard Fabric client protocol; no Drunix-specific client code |
| Drunix's own Go sample app uses `github.com/hyperledger/fabric-gateway` | `drunix-network/asset-transfer-basic/application-gateway-go` in the same repo | `internal/fabric/client.go` uses that SDK (v1.13.0) |
| Lite Peers endorse; they "delegate CommitStatus" to Committing Peers | [docs/drunix-arch.md](https://github.com/npci/drunix/blob/main/docs/drunix-arch.md) | endorse → submit → commit-status flow works through one gateway peer |
| The gateway peer's chaincode-event stream skips invalid transactions | `internal/pkg/gateway/event/chaincodeiterator.go` (`if !transaction.Valid() { continue }`) | the listener never sees events from failed transactions |
| `SetEvent`: "Only a single event can be included in a transaction" | vendored `fabric-chaincode-go/shim/interfaces.go` | the chaincode wraps several events in one `TxEventBatch`; the listener unwraps it, and the projector re-reads the ledger after every event (see §2) |
| Drunix's own `go.mod` is Go 1.26 | `go.mod` in npci/drunix | the gateway builds with Go 1.26 (see §6) |

**Verified:** the real `GatewayClient` passes `TestGatewayClientAgainstFabricGatewayProtocol`. That
test runs the real SDK against an in-process Fabric Gateway gRPC server, over TLS, with X.509 identities.
It covers proposal signing per org MSP, transient data, MVCC retry, chaincode errors relayed in
`ErrorDetail`, ambiguous orderer failures and event streaming from a start block.
**Not yet verified:** a live Drunix network. M3's `network/` is not merged yet. Once it lands, run the
gateway with `FABRIC_MOCK=false`, then run `go run ./cmd/smoke` with a sim-bank.

## 2. Correctness guarantees

* **The read model matches the ledger.** Each event is projected from its payload with guards that
  only move status forward. The deal is then re-read from the ledger (`GetDeal`, `ListTranches`,
  `GetHeldPortions`), so the payload alone is never the source of truth.
* **Batched events are unwrapped.** Fabric carries one event per transaction, and QC changes the
  tranche, held portions and MSE clock at once. The chaincode therefore sends a transaction's
  several events as one `TxEventBatch` (`{dealId, txId, ts, events:[{name, payload}]}`, agreed in
  `chaincode/deal/CONTRACT_CHANGE_001.md`). The listener records, projects, pays and publishes
  each inner event on its own, in order, in the same database transaction. Tested: a QC with
  rejections on two lines yields five rows (two `HeldPortionCreated`), five SSE messages, and no
  duplicates on a full replay.
* **Each event takes effect once.** Delivery from the chain is at-least-once. Three writes commit in
  one PostgreSQL transaction: the projection, the `payment_outbox` row and `chain_checkpoint`.
  `deal_events` is unique on (txId, event, position in the batch). Tested: a restart resumes at
  the right block, and a full replay from block 0 duplicates nothing.
* **Each payment is made once.** The outbox key is (deal, tranche). The bank instruction id is
  deterministic (`PAY-<deal>-<tranche>`), so a resend after a crash gets the same `bankRef`.
  Replicas claim rows with `FOR UPDATE SKIP LOCKED`. Settlement goes back on chain via
  `ConfirmSettlement`. Tested: 40 concurrent release clicks plus 4 concurrent outbox workers produce
  1 release and 1 bank instruction.
* **Retries are only automatic when nothing was committed.** `SubmitWithRetry` retries
  `MVCC_READ_CONFLICT`/`PHANTOM_READ_CONFLICT` and transport failures *before* ordering: 5 attempts,
  exponential backoff with jitter. When the outcome is unknown (orderer or commit-status failure),
  it returns `ERR_INTERNAL` with `details.ambiguous=true` and the `txId`, and the caller retries with
  the same idempotency key.
* **Release is idempotent by construction.** Without a key, the gateway uses
  `gateway:release-<deal>-<n>`. A double click, or the approval handler and the listener racing,
  cannot release twice.
* **A write survives the client disconnecting.** Chain writes are detached from the HTTP request
  context, so a closed browser tab cannot leave a transaction half-tracked.

## 3. MSME law the code follows (MSMED Act, 2006)

* **When the clock starts (§2(b)).** Acceptance, or deemed acceptance when no written objection
  comes within 15 days, falls on the **day of actual delivery**. If the buyer objected in writing,
  it falls on the day the objection is removed. The mock anchors `mseClockStart` there, not at the
  QC timestamp. The compliance report's `acceptanceDate` is that statutory date. Deemed acceptance
  is a §2(b) rule, so it applies to MSE deals only.
* **The deadline (§15).** Payment is due by the agreed date, at most 45 days, or, without an
  agreement, before the "appointed day" (15 days). `termDays` is restricted to 15 or 45, and 45
  needs `mse.writtenAgreement=true`.
* **Who is covered (§2(n)).** "Supplier" covers micro and small enterprises only, so a MEDIUM
  category with `applies:true` is rejected (`ERR_MSE_NOT_APPLICABLE`).

Sources: [Taxmann: buyer's liability u/s 15](https://taxmann.com/post/blog/faqs-liability-of-buyer-u-s-15-of-msmed-act-to-make-timely-payments-to-mse-suppliers),
[SCC Online: interest u/s 16](https://www.scconline.com/blog/post/2022/12/14/when-is-the-interest-rate-payable-under-section-16-of-the-msmed-act-2006-applicable/),
[Legal Wires: delayed-payment remedies](https://legal-wires.com/lex-o-pedia/msme-delayed-payment-remedies/).

## 4. Integration notes per owner

* **M1 (chaincode).**
  * Put the frozen code at the start of the chaincode error, e.g. `ERR_DUPLICATE: ...`. The gateway
    extracts the first known `ERR_*` token from the peer's error text.
  * Checked against `feat/chaincode` (b5b96af): function names, argument order, transient
    `lines`, ledger JSON fields, status values and event payloads all match the gateway.
  * Multi-event transactions use `TxEventBatch` exactly as in `CONTRACT_CHANGE_001.md`; the
    gateway unwraps it and the mock emits the identical encoding.
  * `TickDeadlines` is signed by `svc-bank` (BANK), as `CONTRACT_CHANGE_001.md` §4 requires.
  * `TrancheReleased` needs `amountPaise` and `supplierAccountRef`. If they are missing, the
    listener reads the amount from `GetTranche`.
  * QC before receipt must return `ERR_INVALID_TRANSITION`, so the worker requeues it.
  * The API rejects with 400 what the chaincode would reject: `idemKey` uses the chaincode regex
    `^[a-z0-9_-]+:[A-Za-z0-9._-]+$` (so `ERP:QC-1` fails), `termDays` 45 needs
    `mse.writtenAgreement`, and the lien must equal the PO value. The mock enforces the same rules,
    as well as deemed acceptance for MSE deals only and exact quantities on replacement receipts.
  * A replacement receipt that arrives before `ShipReplacement` gets `ERR_QTY_MISMATCH` (nothing
    is owed yet), not `ERR_INVALID_TRANSITION`, so ingest does not requeue it by default.
  * **Open difference: the MSE clock start.** The chaincode starts the clock at the QC timestamp
    and, on deemed acceptance, at delivery + 15 days. MSMED Act §2(b) puts acceptance on the
    day of delivery (see §3), which the mock follows. A 15-day deal therefore shows a deadline 15
    days later on a real network than on the mock, and the e2e clock assertions hold only on the
    mock. The fix belongs in the chaincode.
  * The mock (`internal/fabric/mock_chaincode.go`) mirrors the §5.5 invariants with the same test
    names; use it as an executable reference.
* **M3 (infra/ingest).**
  * Compose: `build: {context: .., dockerfile: gateway/Dockerfile}`. The image runs migrations on
    start and has a `HEALTHCHECK`.
  * Workers call `POST /internal/v1/submit`. Only the four ingest functions are accepted.
    `ERR_INVALID_TRANSITION` → requeue, `ERR_DUPLICATE` → mark processed, 503 → retry.
  * Set `INTERNAL_TOKEN` in compose and never expose `/internal/*` publicly.
  * For a real network: set `FABRIC_CRYPTO_DIR`, `FABRIC_PEER_<ORG>` and `FABRIC_HOST_<ORG>`, or ship
    your own users file. Certificates must carry the `role` attribute.
* **M4 (forecast).** Tables from `0003_forecast.sql`. Enum and range `CHECK`s match §4.1, and
  `forecast_actions.alert_id` has an FK with cascade delete.
* **M5 (web/sim).**
  * `openapi.yaml` covers every route, cross-checked against the router. Send `X-Org` always and
    `X-User` on writes (demo users are in `config/users.json`).
  * SSE: `/api/v1/events/stream?org=BUYER`, with `Last-Event-ID` replay.
  * Demo fast-forward: `POST /internal/v1/mock/clock {"advanceDays":15}`.
  * sim-bank: `POST /bank/v1/payments` must dedupe `instructionId`. `GET /bank/v1/payments/{ref}`
    must eventually return `SETTLED`; otherwise set `BANK_SETTLE_ON_ACCEPT=true`.

## 5. Deviations and items for a `contract-change` PR

1. **Go 1.26 instead of 1.22 (§2).** Go 1.22 is end-of-life. On Go 1.22, `govulncheck` reported 53
   reachable vulnerabilities, 48 of them in the standard library, with no fixes coming. On Go 1.26
   it reports 0. Only `gateway/` uses it.
2. **HTTP status for two codes.** §12 leaves `ERR_OBJECTION_WINDOW_CLOSED` and
   `ERR_MSE_NOT_APPLICABLE` unmapped ("others" means 500/503). The gateway returns 409 and 400,
   because these are business-rule rejections, not server faults.
3. **`openapi.yaml` lives in `gateway/`.** It should be promoted to `contracts/gateway.openapi.yaml`.
4. **Additive response fields** (no frozen field renamed or removed):
   * the approve response carries `release`;
   * the compliance report carries `summary`;
   * MSE clocks carry `secondsLeft`, `atRisk` and `overdue`;
   * tranches carry `releasedAt` and `settledAt` from the read model;
   * `deal_events` has an `event_seq` column (position inside a `TxEventBatch`).
   The internal-only route `/internal/v1/mock/clock` is also new.

## 6. Security review (summary)

* **Inputs.** SQL is fully parameterised. Path and body inputs are validated. Bodies are capped at
  1 MiB and headers at 64 KiB. `ReadHeaderTimeout` guards against slowloris.
* **Money.** Money is int64 paise with overflow checks. The release amount is never taken from the
  client.
* **Privacy.** Line prices travel in transient data. AUDITOR responses omit `unitPricePaise`.
  Party users can act only on their own deals; the gateway enforces this at the edge and the
  chaincode checks again.
* **Abuse limits.** CSV export is formula-injection safe. SSE is capped at 500 streams. CORS is an
  allow-list. Internal errors are not leaked.
* **Gates.** golangci-lint v2 with gosec reports 0 issues, and govulncheck reports 0
  vulnerabilities. The image is distroless and runs as non-root.
* **Known prototype limit:** `X-Org`/`X-User` are trusted headers (BUILD.md §2). In production,
  replace them with OIDC/mTLS at the edge. The chain side always signs with real MSP identities.
