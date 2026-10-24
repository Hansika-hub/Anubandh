# ANUBANDH — BUILD.md (SOURCE OF TRUTH)

> Every name, path, port, JSON field, enum and function signature in this file is **frozen**.
> If the code and this file disagree, **this file wins** and the code is wrong.
> Changing anything here requires a PR labelled `contract-change` that all 5 owners approve (see §14).

Project: Anubandh — Programmable B2B Payment Deals with MSME compliance and cash forecasting on Drunix.
Team: 5 members, 5 branches, daily merges to `main`.

---

## 0. TL;DR for each member

| # | Branch | Owner area | Language | Folders you OWN (only you edit these) |
|---|--------|-----------|----------|----------------------------------------|
| M1 | `feat/chaincode` | Deal smart contract | Go | `chaincode/deal/` |
| M2 | `feat/gateway` | Chain gateway, listener, read model, bank adapter, REST API for UI | Go | `gateway/`, `db/` |
| M3 | `feat/ingest-infra` | Drunix network, API gateway, Kafka workers, DLQ, Docker, CI, burst test | Go + YAML | `network/`, `ingest/`, `deploy/`, `.github/`, `tests/burst/` |
| M4 | `feat/forecast` | Forecast service, synthetic data, models, alerts | Python | `forecast/`, `tests/forecast/` |
| M5 | `feat/web-sim` | React dashboards, simulators, docs, demo | TS + Python | `web/`, `simulators/`, `docs/`, `tests/e2e/` |

Shared (frozen, edit only through `contract-change` PR): `contracts/`, `BUILD.md`, `README.md` (M5 maintains text).

Rule of thumb: **never edit a folder you do not own.** If you need a change there, open an issue tagged with the owner's M-number.

---

## 1. Repository layout (create exactly this on day 0)

```
anubandh/
├── BUILD.md                      <- this file
├── README.md
├── Makefile                      <- top-level targets (§13)
├── contracts/                    <- FROZEN shared contracts
│   ├── types.json                <- JSON schemas for Deal, Line, Tranche, etc. (§4)
│   ├── events.md                 <- chain event names + payloads (§6)
│   ├── gateway.openapi.yaml      <- REST API served by gateway (§8)
│   ├── ingest.openapi.yaml       <- REST API served by ingest (§9)
│   ├── forecast.openapi.yaml     <- REST API served by forecast (§10)
│   └── simulators.openapi.yaml   <- REST API served by simulators (§11)
├── network/                      <- M3
├── chaincode/deal/               <- M1
├── gateway/                      <- M2
├── db/migrations/                <- M2  (SQL migrations, §7)
├── ingest/                       <- M3
├── forecast/                     <- M4
├── simulators/                   <- M5
├── web/                          <- M5
├── deploy/                       <- M3  (docker-compose.yml, env files)
├── tests/{burst,forecast,e2e}/
└── docs/                         <- M5
```

---

## 2. Global conventions (everyone follows these)

| Topic | Rule |
|-------|------|
| Money | **int64 paise** (₹1 = 100 paise). Never floats. JSON field names end in `Paise`. |
| Quantity | int64 whole units (`qty`). |
| Time | RFC 3339 UTC strings in JSON (`2026-10-20T07:54:00Z`). Go: `time.Time`. Python: timezone-aware `datetime`. |
| IDs | `dealId` = `DL-<YYYYMMDD>-<4 digit seq>` e.g. `DL-20261020-0001`. `trancheNo` = int starting at 1. `lineNo` = int starting at 1. |
| Idempotency key | `idemKey = "<source>:<sourceEventId>"` e.g. `warehouse:GR-88123`. Mandatory on every write that can be retried. |
| JSON | `camelCase` keys. Enums are `UPPER_SNAKE_CASE` strings. |
| Errors (REST) | `{ "error": { "code": "ERR_XXX", "message": "...", "details": {} } }` — codes in §12. |
| Auth (prototype) | Header `X-Org: BUYER|SUPPLIER|BANK|AUDITOR` and `X-User: <userId>` set by web app; real MSP identity is used on the chain side by gateway. |
| Go | Go 1.22, module path `github.com/<team>/anubandh/<folder>`, `gofmt` + `golangci-lint`. |
| Python | 3.11, `ruff` + `black`, type hints required. |
| TS | React 18 + TypeScript strict, ECharts, Vite. |
| Commits | Conventional: `feat(chaincode): ...`, `fix(gateway): ...`. |
| Logging | JSON logs with fields `ts, level, svc, dealId, trancheNo, idemKey, msg`. |

---

## 3. Ports and service names (docker-compose service = DNS name)

| Service | Compose name | Port | Owner |
|---------|-------------|------|-------|
| Fabric/Drunix peers, orderer, CA | per `network/` | per script | M3 |
| Gateway REST API | `gateway` | **8080** | M2 |
| Ingest API gateway (webhooks) | `ingest-api` | **8081** | M3 |
| Ingest workers | `ingest-worker` | (no port) | M3 |
| Redpanda (Kafka) | `redpanda` | 9092 | M3 |
| PostgreSQL | `postgres` | 5432 | M3 (compose) / M2 (schema) |
| Forecast API | `forecast` | **8090** | M4 |
| Bank simulator | `sim-bank` | **8101** | M5 |
| Goods-receipt simulator | `sim-grn` | **8102** | M5 |
| GST IRN simulator | `sim-gst` | **8103** | M5 |
| Udyam simulator | `sim-udyam` | **8104** | M5 |
| Web (Vite dev / nginx) | `web` | **3000** | M5 |

Env vars (all in `deploy/.env.example`, M3 owns the file): `PG_DSN`, `KAFKA_BROKERS`, `GATEWAY_URL`, `INGEST_URL`, `FORECAST_URL`, `SIM_BANK_URL`, `SIM_GRN_URL`, `SIM_GST_URL`, `SIM_UDYAM_URL`, `HMAC_SECRET_<SOURCE>`, `FABRIC_CHANNEL=anubandh-ch`, `FABRIC_CC_NAME=deal`.

---

## 4. Shared data model (`contracts/types.json` mirrors this)

### 4.1 Enums

```
DealStatus      : CREATED | FUNDED | DELIVERED | ACCEPTED | PARTIALLY_ACCEPTED | RELEASED | SETTLED | DISPUTED | EXPIRED | CLOSED
TrancheStatus   : PENDING | DELIVERED | ACCEPTED | DEEMED_ACCEPTED | APPROVED | RELEASED | SETTLED | HELD | CLOSED
HeldExit        : REPLACEMENT | CREDIT_NOTE | PRICE_ADJUSTMENT | DISPUTE
MseCategory     : MICRO | SMALL | MEDIUM | NONE            // NONE = trader/not covered
MseTermDays     : 15 | 45                                    // int
Rail            : UPI | IMPS | NEFT | RTGS
Org             : BUYER | SUPPLIER | BANK | AUDITOR
Role            : WAREHOUSE | PLANT_HEAD | CFO | PROCUREMENT | TREASURER | SUPPLIER_USER | BANK_OFFICER | ARBITER | AUDITOR
Resolution      : RELEASE_TO_SUPPLIER | REFUND_TO_BUYER
AlertSeverity   : INFO | WARN | CRITICAL
ActionType      : SWEEP | DRAW_CREDIT_LINE | RESCHEDULE_NON_MSE | PARK_SURPLUS
```

### 4.2 Core objects

```jsonc
// Deal  (public part, on channel ledger)
{
  "dealId": "DL-20261020-0001",
  "poNumber": "PO-4411",
  "buyerId": "BUYER-001",
  "supplierId": "SUP-007",
  "status": "FUNDED",
  "currency": "INR",
  "lienAmountPaise": 1000000000,         // ₹1 crore
  "lienRef": "LIEN-9921",                // set by bank
  "totalReleasedPaise": 0,
  "totalHeldPaise": 0,
  "mse": { "category": "SMALL", "termDays": 45, "udyamNo": "UDYAM-KL-00-0000007", "applies": true },
  "approvalPolicy": [                    // evaluated at release time, highest matching band wins
    { "minPaise": 0,          "requiredRoles": ["PLANT_HEAD"] },
    { "minPaise": 500000000,  "requiredRoles": ["PLANT_HEAD", "CFO"] }   // ≥ ₹50 lakh
  ],
  "toleranceBps": 0,                     // QC variance tolerance in basis points
  "deliveryDeadline": "2026-11-30T00:00:00Z",
  "invoice": { "irn": null, "verified": false },
  "createdAt": "...", "updatedAt": "..."
}

// Line  (PRIVATE data collection: price + terms, hash on channel)
{ "dealId": "...", "lineNo": 1, "sku": "SKU-STEEL-12", "orderedQty": 100,
  "unitPricePaise": 1000000, "deliveredQty": 0, "acceptedQty": 0, "rejectedQty": 0, "heldQty": 0 }

// Tranche  (one per delivery batch)
{ "dealId": "...", "trancheNo": 1, "status": "ACCEPTED",
  "lines": [ { "lineNo": 1, "deliveredQty": 100, "acceptedQty": 80, "rejectedQty": 20 } ],
  "acceptedValuePaise": 800000000, "heldValuePaise": 200000000,
  "deliveredAt": "...", "acceptedAt": "...", "deemedAcceptance": false,
  "approvals": [ { "role": "PLANT_HEAD", "userId": "u1", "signedAt": "..." } ],
  "releaseIdemKey": null, "bankRef": null, "rail": null,
  "mseClockStart": "...", "mseDeadline": "...",
  "heldExit": null, "objection": null }

// HeldPortion
{ "dealId": "...", "trancheNo": 1, "lineNo": 1, "qty": 20, "valuePaise": 200000000,
  "exit": null /* HeldExit once chosen */, "state": "HELD" }
```

---

## 5. CHAINCODE — `chaincode/deal/` (owner: M1)

Go chaincode, contract name `DealContract`, package `main`, chaincode name `deal`, channel `anubandh-ch`.

### 5.1 File layout (M1)
```
chaincode/deal/
  main.go                 // starts contract
  contract.go             // DealContract struct + all exported tx functions
  types.go                // Go structs matching §4
  state.go                // get/put helpers, composite keys, private data helpers
  statemachine.go         // allowed transitions table
  release.go              // partial release maths, tranche logic
  mse.go                  // MSE clock, deemed acceptance, deadlines
  policy.go               // approval policy evaluation + key-level endorsement
  idempotency.go          // idem key store
  errors.go               // ERR_* codes (§12)
  events.go               // SetEvent wrappers (§6)
  *_test.go               // unit tests with shimtest/mock stub
```

### 5.2 Ledger keys
```
deal:{dealId}                      -> Deal JSON
tranche:{dealId}:{trancheNo}       -> Tranche JSON
held:{dealId}:{trancheNo}:{lineNo} -> HeldPortion JSON
idem:{idemKey}                     -> "1"
Private collection `collBuyerSupplierBank`:  line:{dealId}:{lineNo} -> Line JSON
```

### 5.3 Transaction functions (EXACT names and signatures)

All args are strings (Fabric requirement); JSON args are JSON strings. All writes return `error` (nil on success). Caller identity comes from `ctx.GetClientIdentity()` (MSP id + attribute `role`).

```go
// ---- Deal lifecycle ----
func (c *DealContract) CreateDeal(ctx contractapi.TransactionContextInterface, dealJSON string) error
    // transient map key "lines" carries []Line JSON (private). Org: BUYER. Status -> CREATED
func (c *DealContract) ConfirmLien(ctx ..., dealID string, lienRef string, amountPaise int64) error
    // Org: BANK. CREATED -> FUNDED. amountPaise must equal sum(lines) or deal.lienAmountPaise
func (c *DealContract) VerifyInvoice(ctx ..., dealID string, irn string, idemKey string) error
    // Org: BUYER (called after GST sim check by ingest). sets invoice.verified=true
func (c *DealContract) ExpireDeal(ctx ..., dealID string) error
    // time rule: delivery deadline passed with nothing delivered -> EXPIRED, releases lien

// ---- Delivery & QC ----
func (c *DealContract) PostGoodsReceipt(ctx ..., dealID string, trancheNo int64, linesJSON string, idemKey string) error
    // linesJSON: [{"lineNo":1,"deliveredQty":100}]. Org: BUYER (role WAREHOUSE). creates tranche DELIVERED
func (c *DealContract) AcknowledgeDelivery(ctx ..., dealID string, trancheNo int64) error
    // Org: SUPPLIER. co-sign of delivery. Records deliveredAt-ack (anchors MSE clock evidence)
func (c *DealContract) RecordQualityCheck(ctx ..., dealID string, trancheNo int64, resultsJSON string, idemKey string) error
    // resultsJSON: [{"lineNo":1,"acceptedQty":80,"rejectedQty":20}]. Org: BUYER.
    // all accepted (or within toleranceBps) -> tranche ACCEPTED ; else tranche ACCEPTED for accepted part + HeldPortion created, deal PARTIALLY_ACCEPTED

// ---- Approval & release ----
func (c *DealContract) ApproveRelease(ctx ..., dealID string, trancheNo int64) error
    // Org: BUYER. role from cert attr. Adds approval; when policy satisfied -> tranche APPROVED
func (c *DealContract) ReleaseTranche(ctx ..., dealID string, trancheNo int64, idemKey string) error
    // Org: BUYER. APPROVED -> RELEASED. Amount computed on-chain = acceptedQty*unitPrice. Rejects 2nd release of same tranche.
    // emits TrancheReleased (gateway/bank adapter picks it up)
func (c *DealContract) ConfirmSettlement(ctx ..., dealID string, trancheNo int64, bankRef string, rail string) error
    // Org: BANK. RELEASED -> SETTLED

// ---- Held portion exits ----
func (c *DealContract) ShipReplacement(ctx ..., dealID string, trancheNo int64, lineNo int64, qty int64) error
    // Org: SUPPLIER. HeldPortion exit=REPLACEMENT; creates NEW tranche (trancheNo = next) status PENDING
func (c *DealContract) ProposeCreditNote(ctx ..., dealID string, trancheNo int64, lineNo int64, valuePaise int64) error  // either party
func (c *DealContract) SignCreditNote(ctx ..., dealID string, trancheNo int64, lineNo int64) error                      // other party; both signed -> lien on that portion released
func (c *DealContract) ProposePriceAdjustment(ctx ..., dealID string, trancheNo int64, lineNo int64, adjustedUnitPricePaise int64) error
func (c *DealContract) SignPriceAdjustment(ctx ..., dealID string, trancheNo int64, lineNo int64) error                 // both signed -> portion accepted at new price, becomes releasable tranche

// ---- Disputes & objections ----
func (c *DealContract) RaiseObjection(ctx ..., dealID string, trancheNo int64, reason string) error
    // Org: BUYER within 15 days of delivery. Pauses deemed acceptance. 
func (c *DealContract) ResolveObjection(ctx ..., dealID string, trancheNo int64) error
    // Org: BUYER+SUPPLIER co-sign. Restarts MSE clock for that tranche at resolution time (mseClockStart = now)
func (c *DealContract) RaiseDispute(ctx ..., dealID string, trancheNo int64, lineNo int64, reason string) error  // BUYER or SUPPLIER
func (c *DealContract) ResolveDispute(ctx ..., dealID string, trancheNo int64, lineNo int64, resolution string) error // ARBITER (role ARBITER on BANK org in prototype)
func (c *DealContract) ApplyDisputeTimeout(ctx ..., dealID string, trancheNo int64, lineNo int64) error  // time rule -> default resolution

// ---- Time rules (called by scheduler in gateway, once per minute per active deal; idempotent) ----
func (c *DealContract) TickDeadlines(ctx ..., dealID string) error
    // 1) DELIVERED & no rejection within 15 days -> DEEMED_ACCEPTED, emits DeemedAcceptance + MseClockStarted
    // 2) FUNDED past deliveryDeadline w/o delivery -> ExpireDeal
    // 3) emits MseDeadlineApproaching when (mseDeadline - now) <= 5 days (once per tranche)
    // Uses ctx.GetStub().GetTxTimestamp() ONLY (never wall clock).

// ---- Queries (evaluate, read-only) ----
func (c *DealContract) GetDeal(ctx ..., dealID string) (*Deal, error)
func (c *DealContract) GetTranche(ctx ..., dealID string, trancheNo int64) (*Tranche, error)
func (c *DealContract) ListTranches(ctx ..., dealID string) ([]*Tranche, error)
func (c *DealContract) GetHeldPortions(ctx ..., dealID string) ([]*HeldPortion, error)
func (c *DealContract) GetDealHistory(ctx ..., dealID string) ([]*HistoryEntry, error)   // GetHistoryForKey
func (c *DealContract) QueryDealsByStatus(ctx ..., status string) ([]*Deal, error)       // rich query on SQL state DB
func (c *DealContract) QueryMseDealsNearDeadline(ctx ..., withinDays int64) ([]*Tranche, error)
func (c *DealContract) GetMseClock(ctx ..., dealID string, trancheNo int64) (*MseClock, error)
```

### 5.4 State transition table (implemented in `statemachine.go`, function `CanTransition(from, to string) bool`)

| From | Event | To | Allowed Org/Role |
|------|-------|----|------------------|
| CREATED | ConfirmLien | FUNDED | BANK |
| FUNDED | PostGoodsReceipt | DELIVERED | BUYER/WAREHOUSE |
| DELIVERED | RecordQualityCheck (all pass) | ACCEPTED | BUYER |
| DELIVERED | RecordQualityCheck (some rejected) | PARTIALLY_ACCEPTED | BUYER |
| DELIVERED | TickDeadlines (15d no rejection) | ACCEPTED (deemed) | CONTRACT |
| ACCEPTED / PARTIALLY_ACCEPTED | policy satisfied + ReleaseTranche | RELEASED | BUYER |
| RELEASED | ConfirmSettlement | SETTLED | BANK |
| FUNDED | TickDeadlines (deadline passed) | EXPIRED | CONTRACT |
| any held portion | Replacement / CreditNote / Dispute | per §4 HeldExit | see functions |

### 5.5 Safety invariants (must have unit tests, names fixed)
```
TestNoDoubleReleaseSameTranche
TestReleasedPlusHeldNeverExceedsLien
TestReleaseAmountComputedOnChain
TestPartialAcceptance80of100
TestDeemedAcceptanceAfter15Days
TestObjectionRestartsMseClock
TestApprovalPolicyAboveFiftyLakhNeedsCFO
TestIdempotentDuplicateRejected
TestOutOfOrderQCBeforeReceiptRejected
TestMseClock45DayRequiresWrittenAgreementFlag
TestNonMseDealSkipsGuard
```

---

## 6. CHAIN EVENTS (`contracts/events.md`, emitted via `SetEvent`, JSON payload)

Every payload includes `dealId`, `txId`, `ts`. Event names are frozen:

| Event name | Extra payload fields |
|-----------|----------------------|
| `DealCreated` | `buyerId, supplierId, lienAmountPaise` |
| `LienConfirmed` | `lienRef, amountPaise` |
| `InvoiceVerified` | `irn` |
| `GoodsReceived` | `trancheNo, lines[]` |
| `DeliveryAcknowledged` | `trancheNo` |
| `QualityRecorded` | `trancheNo, acceptedValuePaise, heldValuePaise` |
| `HeldPortionCreated` | `trancheNo, lineNo, qty, valuePaise` |
| `TrancheAccepted` | `trancheNo, deemed:false, mseClockStart, mseDeadline` |
| `DeemedAcceptance` | `trancheNo, mseClockStart, mseDeadline` |
| `MseClockStarted` | `trancheNo, termDays, mseDeadline` |
| `MseClockRestarted` | `trancheNo, mseClockStart, mseDeadline` |
| `MseDeadlineApproaching` | `trancheNo, mseDeadline, daysLeft` |
| `ApprovalRecorded` | `trancheNo, role, userId, satisfied:bool` |
| `TrancheReleased` | `trancheNo, amountPaise, supplierId, supplierAccountRef, rail?` |
| `SettlementConfirmed` | `trancheNo, bankRef, rail` |
| `ReplacementShipped` | `trancheNo, newTrancheNo, lineNo, qty` |
| `CreditNoteProposed` / `CreditNoteSigned` | `trancheNo, lineNo, valuePaise` |
| `PriceAdjustmentProposed` / `PriceAdjustmentSigned` | `trancheNo, lineNo, adjustedUnitPricePaise` |
| `ObjectionRaised` / `ObjectionResolved` | `trancheNo, reason?` |
| `DisputeRaised` / `DisputeResolved` | `trancheNo, lineNo, resolution?` |
| `DealExpired` | `reason` |

---

## 7. DATABASE — PostgreSQL read model (`db/migrations/`, owner: M2)

Migration files: `0001_core.sql`, `0002_outbox.sql`, `0003_forecast.sql`. Tables (frozen names/columns; add columns only via contract-change):

```sql
-- 0001_core.sql
deals(deal_id PK, po_number, buyer_id, supplier_id, status, lien_amount_paise bigint, lien_ref,
      total_released_paise bigint, total_held_paise bigint, mse_category, mse_term_days int, mse_applies bool,
      udyam_no, delivery_deadline timestamptz, invoice_irn, invoice_verified bool, created_at, updated_at)
deal_lines(deal_id, line_no, sku, ordered_qty, unit_price_paise, delivered_qty, accepted_qty, rejected_qty, held_qty, PK(deal_id,line_no))
tranches(deal_id, tranche_no, status, accepted_value_paise, held_value_paise, delivered_at, accepted_at,
         deemed bool, mse_clock_start, mse_deadline, bank_ref, rail, released_at, settled_at, PK(deal_id,tranche_no))
approvals(deal_id, tranche_no, role, user_id, signed_at, PK(deal_id,tranche_no,role))
held_portions(deal_id, tranche_no, line_no, qty, value_paise, exit, state, PK(deal_id,tranche_no,line_no))
deal_events(id bigserial PK, deal_id, tranche_no, event_name, payload jsonb, tx_id, block_no, ts)
chain_checkpoint(listener_id PK, block_no bigint, tx_index int)

-- 0002_outbox.sql
payment_outbox(deal_id, tranche_no, amount_paise, supplier_account_ref, rail, status,   -- PENDING|SENT|CONFIRMED|FAILED
               bank_ref, attempts int, last_error, created_at, updated_at, PK(deal_id,tranche_no))
processed_keys(idem_key PK, processed_at)         -- used by ingest workers

-- 0003_forecast.sql  (owned by M2 for DDL, written by M4)
cash_accounts(account_id PK, org_id, name, balance_paise bigint, min_buffer_paise bigint, credit_line_limit_paise bigint, credit_line_used_paise bigint)
receivables(receivable_id PK, customer_id, invoice_no, amount_paise, due_date, status, disputed bool)
suppliers_profile(supplier_id PK, mean_delay_days float, std_delay_days float, qc_reject_rate float, mse_category)
cash_band(org_id, ts timestamptz, granularity,       -- HOURLY|DAILY
          p10_paise bigint, p50_paise bigint, p90_paise bigint, generated_at, PK(org_id,ts,granularity))
forecast_alerts(alert_id PK, org_id, severity, shortfall_prob float, window_start, window_end, drivers jsonb, created_at, acked bool)
forecast_actions(action_id PK, alert_id FK, action_type, amount_paise, est_cost_paise, rank int, rationale, mse_safe bool)
```

---

## 8. GATEWAY SERVICE — `gateway/` (owner: M2) — REST on :8080

### 8.1 File/package layout
```
gateway/
  cmd/gateway/main.go
  internal/fabric/client.go        // Fabric Gateway SDK wrapper
  internal/fabric/submit.go        // SubmitWithRetry (MVCC retry, 5x, exp backoff+jitter)
  internal/listener/listener.go    // checkpointed chain event listener
  internal/projector/projector.go  // chain event -> Postgres read model
  internal/bank/adapter.go         // outbox poller -> sim-bank / real rails
  internal/bank/rail.go            // SelectRail
  internal/scheduler/tick.go       // calls TickDeadlines per active deal each minute
  internal/api/router.go           // chi router
  internal/api/handlers_*.go
  internal/store/pg.go
```

### 8.2 Go function names (frozen; other Go code may call these)
```go
// internal/fabric/submit.go
func SubmitWithRetry(ctx context.Context, fn string, args ...string) ([]byte, error)
func Evaluate(ctx context.Context, fn string, args ...string) ([]byte, error)

// internal/bank/rail.go
// Rules: amount<=100000_00 paise & category allows -> UPI; <=200000_00 -> IMPS; <=2_00_000_00 ... see below
// Prototype rule: <= ₹1L: UPI ; <= ₹2L: IMPS ; <= ₹2cr: NEFT ; > ₹2cr or ≥₹2L urgent flag: RTGS.  NEVER split payments.
func SelectRail(amountPaise int64, urgent bool) Rail

// internal/bank/adapter.go
func (a *Adapter) EnqueueFromEvent(ev TrancheReleasedEvent) error      // writes payment_outbox row (idempotent on deal+tranche)
func (a *Adapter) RunOutboxLoop(ctx context.Context)                   // sends via sim-bank, then calls ConfirmSettlement

// internal/listener/listener.go
func (l *Listener) Start(ctx context.Context) error                    // resumes from chain_checkpoint
func (l *Listener) handle(ev *client.ChaincodeEvent) error             // fan out to projector + adapter

// internal/projector/projector.go
func (p *Projector) Apply(eventName string, payload []byte) error

// internal/scheduler/tick.go
func (s *Scheduler) Run(ctx context.Context)
```

### 8.3 REST endpoints (all JSON, base `/api/v1`)

| Method | Path | Purpose | Calls chaincode fn | Role/Org |
|--------|------|---------|--------------------|----------|
| POST | `/deals` | create deal (body = Deal + lines) | `CreateDeal` | BUYER |
| GET | `/deals` | list (query: `status, buyerId, supplierId, mse=true, limit, offset`) | read model | any |
| GET | `/deals/{dealId}` | deal detail + lines + tranches | read model | any (price hidden for AUDITOR) |
| GET | `/deals/{dealId}/history` | full ledger history | `GetDealHistory` | any |
| POST | `/deals/{dealId}/lien` | body `{lienRef, amountPaise}` | `ConfirmLien` | BANK |
| POST | `/deals/{dealId}/tranches/{n}/ack` | supplier acknowledges delivery | `AcknowledgeDelivery` | SUPPLIER |
| POST | `/deals/{dealId}/tranches/{n}/approve` | approver signs | `ApproveRelease` | BUYER |
| POST | `/deals/{dealId}/tranches/{n}/release` | trigger release (auto after approval) | `ReleaseTranche` | BUYER |
| POST | `/deals/{dealId}/tranches/{n}/objection` | `{reason}` | `RaiseObjection` | BUYER |
| POST | `/deals/{dealId}/tranches/{n}/objection/resolve` | | `ResolveObjection` | BUYER/SUPPLIER |
| POST | `/deals/{dealId}/held/{n}/{lineNo}/replacement` | `{qty}` | `ShipReplacement` | SUPPLIER |
| POST | `/deals/{dealId}/held/{n}/{lineNo}/credit-note` | `{valuePaise}` (propose) | `ProposeCreditNote` | any party |
| POST | `/deals/{dealId}/held/{n}/{lineNo}/credit-note/sign` | | `SignCreditNote` | other party |
| POST | `/deals/{dealId}/held/{n}/{lineNo}/price-adjustment` | `{adjustedUnitPricePaise}` | `ProposePriceAdjustment` | any party |
| POST | `/deals/{dealId}/held/{n}/{lineNo}/price-adjustment/sign` | | `SignPriceAdjustment` | other party |
| POST | `/deals/{dealId}/held/{n}/{lineNo}/dispute` | `{reason}` | `RaiseDispute` | BUYER/SUPPLIER |
| POST | `/deals/{dealId}/held/{n}/{lineNo}/dispute/resolve` | `{resolution}` | `ResolveDispute` | ARBITER |
| GET | `/mse/clocks` | all MSE tranches with `daysLeft`, query `riskDays=5` | read model | any |
| GET | `/mse/compliance-report` | auditor export: all MSE tranches, acceptance date, paid date, on-time flag | read model | AUDITOR |
| GET | `/payments/outbox` | outbox status | read model | BANK/BUYER |
| GET | `/events/stream` | Server-Sent Events of chain events (for live UI) | listener | any |
| GET | `/health` | liveness | | |

Responses: object or `{ "items": [...], "total": n }` for lists.

---

## 9. INGEST SERVICE — `ingest/` (owner: M3) — REST on :8081, Kafka workers

### 9.1 Layout
```
ingest/
  api/main.go                 // HTTP server (chi) + middleware
  api/middleware_hmac.go      // VerifyHMAC(secret) 
  api/middleware_ratelimit.go // token bucket per source: 50 rps, burst 100 -> 429 + Retry-After
  api/handlers.go
  worker/main.go
  worker/process.go           // idempotent processing, retry/backoff, DLQ
  worker/dlq_tool.go          // list/replay CLI
  internal/events/types.go    // InboundEvent
```

### 9.2 Webhook endpoints (base `/ingest/v1`)
| Method | Path | Source | Maps to chaincode |
|--------|------|--------|-------------------|
| POST | `/events/goods-receipt` | warehouse/ERP | `PostGoodsReceipt` |
| POST | `/events/quality-check` | warehouse/ERP | `RecordQualityCheck` |
| POST | `/events/invoice` | GST check result | `VerifyInvoice` |
| POST | `/events/bank-lien` | bank | `ConfirmLien` |
| GET | `/dlq` | operator | list dead-letter events |
| POST | `/dlq/{id}/replay` | operator | re-enqueue |
| GET | `/health` | | |

Headers: `X-Source: warehouse|erp|gst|bank`, `X-Signature: hex(HMAC-SHA256(body, secret))`, `X-Idempotency-Key: <source>:<id>`.

Body (unified envelope — `InboundEvent`):
```json
{ "eventType": "GOODS_RECEIPT|QUALITY_CHECK|INVOICE|BANK_LIEN",
  "idemKey": "warehouse:GR-88123",
  "dealId": "DL-20261020-0001",
  "trancheNo": 1,
  "payload": { /* type-specific, same shape as chaincode linesJSON/resultsJSON/etc. */ },
  "occurredAt": "2026-10-20T07:54:00Z" }
```
Success: `202 {"accepted":true,"idemKey":"..."}`. Bad signature `401 ERR_BAD_SIGNATURE`. Rate-limited `429 ERR_RATE_LIMITED`.

### 9.3 Kafka topics (Redpanda)
| Topic | Partitions | Key | Purpose |
|-------|-----------|-----|---------|
| `anubandh.events.inbound` | 12 | `dealId` | validated inbound events |
| `anubandh.events.retry` | 12 | `dealId` | delayed requeue for out-of-order events (header `retryAt`) |
| `anubandh.events.dlq` | 3 | `dealId` | after 5 failures |

### 9.4 Worker function names (Go)
```go
func ProcessEvent(ctx context.Context, ev InboundEvent) error       // dedupe -> map -> gateway submit
func AlreadyProcessed(ctx context.Context, idemKey string) (bool, error)   // table processed_keys
func MarkProcessed(ctx context.Context, idemKey string) error
func SubmitToChain(ctx context.Context, ev InboundEvent) error      // calls gateway internal endpoint POST /internal/v1/submit
func RequeueWithDelay(ctx context.Context, ev InboundEvent, d time.Duration) error
func SendToDLQ(ctx context.Context, ev InboundEvent, reason string) error
```
Workers call the gateway via an internal-only endpoint (M2 must provide it):
`POST http://gateway:8080/internal/v1/submit` body `{ "fn": "PostGoodsReceipt", "args": ["..."], "idemKey": "..." }` → `200 {"txId":"..."}` or error code `ERR_INVALID_TRANSITION` (worker requeues), `ERR_DUPLICATE` (worker marks processed), others (retry).

---

## 10. FORECAST SERVICE — `forecast/` (owner: M4) — FastAPI on :8090

### 10.1 Layout
```
forecast/
  app/main.py                   # FastAPI
  app/routes.py
  app/schemas.py                # pydantic models
  data/generator.py             # synthetic year: 50 suppliers, 30 customers
  features/build.py
  models/baselines.py           # moving_average, prophet_baseline
  models/timing_lgbm.py         # LightGBM quantile (P10,P50,P90)
  models/intraday.py
  engine/montecarlo.py          # 1000 paths -> bands
  engine/alerts.py
  engine/actions.py             # ranked actions, MSE-safe
  engine/explain.py             # SHAP drivers
  eval/walkforward.py
  store/pg.py
```

### 10.2 Python function names (frozen)
```python
# data/generator.py
def generate_synthetic_year(seed: int = 42, n_suppliers: int = 50, n_customers: int = 30) -> dict[str, "pd.DataFrame"]
def inject_shock(df, customer_id: str, days_late: int) -> "pd.DataFrame"
# features/build.py
def build_release_timing_features(open_deals: "pd.DataFrame", profiles: "pd.DataFrame") -> "pd.DataFrame"
def build_inflow_features(receivables: "pd.DataFrame") -> "pd.DataFrame"
# models/timing_lgbm.py
def train_quantile_models(train_df, target_col: str = "days_to_release") -> dict[float, "lgb.Booster"]   # keys 0.1,0.5,0.9
def predict_quantiles(models: dict, X: "pd.DataFrame") -> "pd.DataFrame"                                # cols p10,p50,p90
# models/baselines.py
def moving_average_forecast(history: "pd.Series", horizon: int) -> "pd.Series"
def prophet_forecast(history: "pd.DataFrame", horizon: int) -> "pd.DataFrame"
# engine/montecarlo.py
def simulate_cash_paths(opening_balance_paise: int, obligations: "pd.DataFrame", inflows: "pd.DataFrame",
                        recurring: "pd.DataFrame", n_paths: int = 1000, horizon: str = "14d") -> "np.ndarray"
def to_band(paths: "np.ndarray") -> "pd.DataFrame"                  # cols ts,p10,p50,p90
def shortfall_probability(paths: "np.ndarray", min_buffer_paise: int) -> "pd.Series"
# engine/alerts.py
def evaluate_alerts(org_id: str, band: "pd.DataFrame", probs: "pd.Series", threshold: float) -> list[dict]
# engine/actions.py
def rank_actions(alert: dict, accounts: list[dict], payments: "pd.DataFrame") -> list[dict]
def is_mse_safe(action: dict, mse_tranches: "pd.DataFrame") -> bool   # never defer MSE past deadline
# engine/explain.py
def top_drivers(model, X, k: int = 3) -> list[dict]
# eval/walkforward.py
def walk_forward_eval(df, n_splits: int = 6) -> dict      # pinball loss, MAE P50, coverage, precision, recall, lead_days
```

### 10.3 REST (base `/forecast/v1`)
| Method | Path | Notes |
|--------|------|-------|
| GET | `/band?orgId=BUYER-001&horizon=intraday|14d` | returns `{orgId, horizon, points:[{ts,p10Paise,p50Paise,p90Paise}], minBufferPaise}` |
| GET | `/alerts?orgId=...&acked=false` | `{items:[{alertId,severity,shortfallProb,windowStart,windowEnd,drivers:[{feature,impact,text}]}]}` |
| GET | `/alerts/{alertId}/actions` | `{items:[{actionId,actionType,amountPaise,estCostPaise,rank,rationale,mseSafe}]}` |
| POST | `/alerts/{alertId}/ack` | |
| POST | `/refresh?orgId=...` | recompute from current ledger read model |
| POST | `/retrain` | retrain models on synthetic/real history |
| POST | `/whatif` | body `{orgId, shiftDealIds:[...], days:int}` -> new band |
| GET | `/metrics/evaluation` | walk-forward results vs baselines (for slide + dashboard) |
| POST | `/seed/synthetic` | load synthetic year into DB (demo) |
| GET | `/health` | |

Forecast reads `deals`, `tranches`, `receivables`, `cash_accounts`, `suppliers_profile` **read-only** from Postgres; writes only `cash_band`, `forecast_alerts`, `forecast_actions`.

---

## 11. SIMULATORS — `simulators/` (owner: M5) — all FastAPI

| Service / Port | Method + Path | Request → Response |
|----------------|--------------|--------------------|
| sim-bank :8101 | `POST /bank/v1/liens` | `{dealId, amountPaise, buyerAccountRef}` → `{lienRef:"LIEN-xxxx"}` |
| | `POST /bank/v1/payments` | `{instructionId, dealId, trancheNo, amountPaise, rail, supplierAccountRef}` → `{bankRef:"UTR-xxxx", status:"ACCEPTED"}` (duplicate `instructionId` returns the same bankRef) |
| | `GET /bank/v1/payments/{bankRef}` | → `{status: ACCEPTED|SETTLED|FAILED}` |
| | `POST /bank/v1/_chaos` | `{failNext:n}` for testing outbox |
| sim-grn :8102 | `POST /sim/grn/emit` | `{dealId, trancheNo, lines:[{lineNo,deliveredQty}], dupes:int, shuffle:bool}` → sends signed webhooks to ingest (used by burst test) |
| | `POST /sim/qc/emit` | `{dealId, trancheNo, results:[{lineNo,acceptedQty,rejectedQty}]}` |
| sim-gst :8103 | `GET /gst/v1/irn/{irn}` | → `{irn, valid:true, gstin, invoiceValuePaise, invoiceDate}` |
| sim-udyam :8104 | `GET /udyam/v1/{udyamNo}` | → `{udyamNo, category:"MICRO|SMALL|MEDIUM", activity:"MANUFACTURING|SERVICES|TRADING", valid:true}` |

Seed fixtures (M5 creates in `simulators/fixtures/`, everyone uses these IDs in demos/tests):
```
Buyer      : BUYER-001   "Malabar Auto Components Ltd"
Suppliers  : SUP-001..SUP-050  (SUP-007 = SMALL, MANUFACTURING, 45-day agreement; SUP-012 = MICRO, 15-day)
Customers  : CUS-001..CUS-030
Demo deal  : DL-20261020-0001  PO-4411  SUP-007  line1: SKU-STEEL-12 qty 100 @ ₹10,000 -> ₹10L lien
Big deal   : DL-20261020-0002  SUP-012 ₹60L, needs PLANT_HEAD + CFO
```

---

## 12. Error codes (shared)

```
ERR_BAD_REQUEST, ERR_UNAUTHORIZED, ERR_FORBIDDEN_ROLE, ERR_NOT_FOUND,
ERR_INVALID_TRANSITION, ERR_DUPLICATE (idem key seen), ERR_LIEN_EXCEEDED,
ERR_ALREADY_RELEASED, ERR_POLICY_NOT_SATISFIED, ERR_QTY_MISMATCH,
ERR_OBJECTION_WINDOW_CLOSED, ERR_MSE_NOT_APPLICABLE, ERR_BAD_SIGNATURE,
ERR_RATE_LIMITED, ERR_MVCC_CONFLICT (retryable), ERR_INTERNAL
```
HTTP mapping: 400 `BAD_REQUEST/QTY_MISMATCH`, 401 `UNAUTHORIZED/BAD_SIGNATURE`, 403 `FORBIDDEN_ROLE`, 404 `NOT_FOUND`, 409 `INVALID_TRANSITION/DUPLICATE/ALREADY_RELEASED/LIEN_EXCEEDED/POLICY_NOT_SATISFIED`, 429 `RATE_LIMITED`, 500/503 others.

---

## 13. Makefile targets (top-level, M3 owns file)

```
make up          # docker compose up everything (network, kafka, pg, gateway, ingest, forecast, sims, web)
make down
make network     # bring up Drunix network only
make cc-deploy   # package + install + commit chaincode
make migrate     # run db/migrations
make seed        # sims fixtures + synthetic forecast data
make test-cc     # M1 unit tests
make test-burst  # 500 duplicate/out-of-order events; expect 0 double releases
make test-partial# 80/100 partial delivery e2e
make test-deadline
make test-forecast
make demo        # scripted demo scenario
```

---

## 14. Team workflow, merges, and anti-conflict rules

### 14.1 Branches
`main` (protected) ← `feat/chaincode` (M1), `feat/gateway` (M2), `feat/ingest-infra` (M3), `feat/forecast` (M4), `feat/web-sim` (M5).
Each member works **only in own folders**, so file-level merge conflicts are impossible by construction.

### 14.2 Daily merge ritual
1. **18:00** — everyone: `git fetch && git rebase origin/main` on own branch, run own tests, open PR → `main`.
2. **18:30** — merge order (dependencies first): **M1 → M2 → M3 → M5 → M4**. Squash-merge. CI must be green.
3. **19:00** — everyone pulls `main` and runs `make up` smoke test. Breakage is fixed by the owner of the failing folder next morning first thing.
4. No direct pushes to `main`. No force-push on shared branches.

### 14.3 Contract-change PRs
Anything in `contracts/` or `BUILD.md` (function names, JSON fields, endpoints, topics, tables):
- Branch `contract/<short-name>`, label `contract-change`.
- Needs 👍 from **all 5 owners** (or those whose area is touched + M-lead).
- Merge it **first**, before that day's feature merges. Announce in chat.

### 14.4 Parallel-work stubs (so nobody waits)
| If you depend on... | ...use this until it lands |
|---|---|
| M1 chaincode | M2 uses `gateway/internal/fabric/mock_client.go` (in-memory fake implementing same `Submit/Evaluate` interface); env `FABRIC_MOCK=true` |
| M2 REST API | M5 uses `web/src/mocks/` (MSW handlers generated from `contracts/gateway.openapi.yaml`) |
| M2 `/internal/v1/submit` | M3 workers use env `GATEWAY_MOCK=true` returning 200 |
| M3 Kafka/Postgres | everyone uses `make up-infra` (compose subset, ready day 1 by M3) |
| M4 forecast | M5 uses `web/src/mocks/forecast.ts` fixed JSON |
| M5 simulators | M2/M3 use WireMock files in `simulators/fixtures/wiremock/` (M5 commits these day 1) |
| M2 Postgres schema | M4 uses `forecast/tests/fixtures/*.csv`, loaded by `make seed` once migrations exist |

### 14.5 Definition of Done (per PR)
- Function/endpoint names exactly as in this file.
- Unit tests for your area pass; linter clean.
- OpenAPI/types updated **only** if a contract-change PR was already merged.
- README snippet for your folder (max 20 lines: how to run, env vars).

---

## 15. Build plan by member (phase order matches proposal §4)

### M1 — Chaincode (branch `feat/chaincode`)
1. Day 1–2: `types.go`, `state.go`, `statemachine.go`, `errors.go`, `idempotency.go`, `CreateDeal`, `ConfirmLien`, queries.
2. Day 3–4: `PostGoodsReceipt`, `AcknowledgeDelivery`, `RecordQualityCheck`, `release.go` partial release, `ReleaseTranche`, `ConfirmSettlement`.
3. Day 5–6: `policy.go` (approval bands + key-level endorsement), `ApproveRelease`.
4. Day 7–8: `mse.go`, `TickDeadlines`, objection functions, `QueryMseDealsNearDeadline`.
5. Day 9–10: held-portion exits (replacement, credit note, price adjustment, dispute), full test suite (§5.5), events.

### M2 — Gateway (branch `feat/gateway`)
1. Day 1: SQL migrations (§7) + mock Fabric client + REST skeleton with all routes returning stub data (so M5 can integrate).
2. Day 2–4: real Fabric client, `SubmitWithRetry`, listener with checkpoint, projector for each event (§6).
3. Day 5–6: REST handlers wired to chaincode; `/internal/v1/submit`; SSE stream.
4. Day 7: bank adapter + `SelectRail` + outbox + `ConfirmSettlement` callback.
5. Day 8: scheduler for `TickDeadlines`; `/mse/*` endpoints and compliance report.

### M3 — Infra & Ingest (branch `feat/ingest-infra`)
1. Day 1: `deploy/docker-compose.yml` with Postgres + Redpanda (`make up-infra`), `.env.example`, CI skeleton.
2. Day 2–4: `network/` scripts: 4 orgs (Buyer, Supplier-via-bank, Bank, Auditor), channel `anubandh-ch`, CA, TLS, private data collection config `collections_config.json`, `make cc-deploy`.
3. Day 5–6: ingest API (HMAC, schema validation, rate limit), Kafka producer.
4. Day 7–8: workers (idempotency, retry, requeue-with-delay, DLQ) + DLQ endpoints.
5. Day 9: `tests/burst` (500 duplicate + out-of-order events → 0 double releases), `make demo`.

### M4 — Forecast (branch `feat/forecast`)
1. Day 1–2: `generator.py` + CSV fixtures (commit to `forecast/tests/fixtures/`), FastAPI skeleton with fixed responses (§10.3).
2. Day 3–4: features, baselines (moving average, Prophet).
3. Day 5–6: LightGBM quantile models, `montecarlo.py`, bands (hourly + daily).
4. Day 7: alerts + SHAP drivers; actions with MSE-safe filter.
5. Day 8–9: walk-forward eval vs baselines, `/metrics/evaluation`, `/whatif`; DB read/write.

### M5 — Web, Simulators, Docs (branch `feat/web-sim`)
1. Day 1: simulators (§11) + fixtures + WireMock files; web scaffold with MSW mocks from OpenAPI.
2. Day 2–4: Buyer view (deal create, approvals, tranche status), Supplier view (deliveries, acknowledgments, held portion actions).
3. Day 5–6: Treasurer view (P10/P50/P90 ECharts band, alerts, ranked actions), MSE guard view (countdown clocks).
4. Day 7: Auditor view (compliance report, history, hashes), live SSE feed.
5. Day 8–9: README, architecture docs, demo script, GIF, pitch deck support.

### Demo scenario (everyone's integration target)
1. Buyer creates `DL-20261020-0001` (₹10L, SUP-007). 2. Bank confirms lien. 3. Warehouse GRN 100 units (sent twice + shuffled → no dupes). 4. QC: 80 accepted / 20 rejected. 5. ₹8L approvals met → auto release over NEFT; ₹2L held. 6. Supplier ships 20 replacements → new tranche. 7. Fast-forward 15 days on another deal → deemed acceptance + MSE clock. 8. Treasurer sees shortfall alert + ranked actions that exclude deferring MSE payments.

---

## 16. Open questions (resolve in first standup; record answer here)

- [ ] Drunix version/image and peer topology confirmed by M3 → `network/README.md`.
- [ ] Is SQL state DB rich-query syntax identical to CouchDB selectors? (M1 + M3)
- [ ] Final GitHub org/repo name → update module paths.
- [ ] Hackathon submission deadline → freeze date for `contracts/`.
