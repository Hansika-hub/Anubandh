# CONTRACT_CHANGE_001 — paste into the `contract/chaincode-v1` PR (label `contract-change`)
Needs a thumbs-up from M2, M3, M4, M5. Merge BEFORE the day's feature merges (BUILD §14.3).

## 1. One chain event per transaction  (affects M2 listener/projector)
Fabric keeps a single chaincode event per tx; a later `SetEvent` silently replaces an earlier one. BUILD §6 has txs
that emit several (RecordQualityCheck, TickDeadlines, SignPriceAdjustment ...). Rule now:
- exactly one event -> emitted with its frozen §6 name and payload (nothing changes);
- two or more -> name `TxEventBatch`, payload
  `{"dealId","txId","ts","events":[{"name":"QualityRecorded","payload":{...}}, ...]}` in emission order.
M2: `Listener.handle` must unwrap `TxEventBatch` and call `Projector.Apply` once per inner event.
Add `TxEventBatch` to `contracts/events.md`.

## 2. Additive fields (ledger JSON). All optional for readers.
| Where | Field | Why |
|---|---|---|
| Deal | `supplierAccountRef` | `TrancheReleased` must carry it; the bank adapter needs it. Set by buyer at CreateDeal |
| Deal.mse | `writtenAgreement` bool | `TestMseClock45DayRequiresWrittenAgreementFlag`: termDays 45 is rejected unless true. **Demo fixtures for SUP-007 must send `true`** (M5) |
| Tranche | `ackedAt, releasedAt, settledAt, replaces{trancheNo,lineNo}, mseAlerted` | evidence + alert-once |
| Tranche.lines[] | `adjustedUnitPricePaise` | co-signed price adjustment |
| HeldPortion | `proposal, disputeRaisedAt, replacementTrancheNo` | credit-note / adjustment / dispute flows |
| HeldPortion.state | `HELD \| REPLACING \| DISPUTED \| CLOSED` | §4.2 only showed `HELD` |
| `MseClock` (new) | `{dealId,trancheNo,applies,termDays,state,clockStart,deadline,daysLeft,deemedAcceptanceAt}` | return type of `GetMseClock`; state = NOT_APPLICABLE\|NOT_STARTED\|RUNNING\|OVERDUE\|PAID |
`TrancheAccepted`/`DeemedAcceptance` omit the clock fields on non-MSE deals. `MseClockStarted` is only emitted when `mse.applies`.
New DB columns M2 may want: `tranches.acked_at, released_at, settled_at`, `held_portions.replacement_tranche_no`.

## 3. Query functions return JSON text
`GetDeal, GetTranche, ListTranches, GetHeldPortions, GetDealHistory, QueryDealsByStatus, QueryMseDealsNearDeadline,
GetMseClock` return a `string` containing the same JSON a struct return would produce (contractapi sends strings
verbatim — verified in its serializer). Reason: no schema generation for `*time.Time`/`RawMessage` at chaincode start-up.
`QueryDealsByStatus` uses a key index, not a rich query (BUILD §16 open question on SQL-DB selectors stays open).

## 4. Who may drive the clock
`TickDeadlines`, `ExpireDeal`, `ApplyDisputeTimeout`: org BANK or BUYER only (not SUPPLIER/AUDITOR). Fabric's tx
timestamp is chosen by the client; restricting callers to the endorsing orgs stops a supplier from fast-forwarding time.
M2 scheduler must therefore connect as BANK (preferred) or BUYER.

## 5. Decisions the team must confirm at stand-up (code defaults in brackets)
- D-1 require verified invoice before release [off; `core.RequireVerifiedInvoice`]
- D-2 unresolved dispute after 30 days [refund to buyer; `core.DisputeTimeout/DisputeDefault`]
- D-3 QC variance within `toleranceBps` [whole lot accepted, AQL-style]
- D-4 `lienAmountPaise` must equal the sum of lines at CreateDeal
- D-5 partial replacement/credit notes are refused; use a price adjustment (keeps held maths exact)

## 6. BUILD §5.1 layout
Logic lives in `core/` (same file names: types, state, statemachine, release, mse, policy, idempotency, errors, events)
so it is testable without Fabric. Root keeps `main.go` + `contract.go` (+ `fabric_ledger.go`). Folder stays M1-owned.
