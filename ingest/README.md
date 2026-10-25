# ingest/ — signed webhooks → Kafka → idempotent workers → gateway (owner: M3)

```
warehouse / erp / gst / bank ──HMAC webhook──▶ ingest-api :8081 ──▶ anubandh.events.inbound (key = dealId)
                                                                          │
                          ┌───────────────── ingest-worker ◀──────────────┘
                          │  processed_keys? → drop           ERR_INVALID_TRANSITION → anubandh.events.retry (+delay)
                          │  POST gateway /internal/v1/submit other failure           → anubandh.events.retry (backoff)
                          │  200 / ERR_DUPLICATE → mark processed      5th failure / malformed → anubandh.events.dlq
```

## Webhook contract (BUILD.md 9.2)
`POST /ingest/v1/events/{goods-receipt|quality-check|invoice|bank-lien}`

Headers: `X-Source: warehouse|erp|gst|bank`, `X-Signature: hex(HMAC-SHA256(body, HMAC_SECRET_<SOURCE>))`,
`X-Idempotency-Key: <source>:<id>` (must equal the body `idemKey`, and start with the caller's source).

| Source | May post |
|---|---|
| warehouse, erp | goods-receipt, quality-check |
| gst | invoice |
| bank | bank-lien |

Payloads (`payload` field):

| eventType | payload | gateway call |
|---|---|---|
| `GOODS_RECEIPT` | `{"lines":[{"lineNo":1,"deliveredQty":100}]}` (bare array also accepted) | `PostGoodsReceipt` |
| `QUALITY_CHECK` | `{"results":[{"lineNo":1,"acceptedQty":80,"rejectedQty":20}]}` | `RecordQualityCheck` |
| `INVOICE` | `{"irn":"..."}` | `VerifyInvoice` |
| `BANK_LIEN` | `{"lienRef":"...","amountPaise":100000000}` | `ConfirmLien` |

| Result | HTTP | code |
|---|---|---|
| accepted | 202 `{"accepted":true,"idemKey":"..."}` | |
| bad/missing signature, unknown source | 401 | `ERR_BAD_SIGNATURE` |
| schema/header problem | 400 | `ERR_BAD_REQUEST` |
| source not allowed for this endpoint | 403 | `ERR_FORBIDDEN_ROLE` |
| over 50 rps / burst 100 per source | 429 + `Retry-After` | `ERR_RATE_LIMITED` |
| Kafka unavailable (never acknowledged) | 503 + `Retry-After` | `ERR_INTERNAL` |

Signature is checked **before** rate limiting, so forged traffic cannot drain a real source's bucket.

Operator endpoints (`X-Operator-Token: $INGEST_OPERATOR_TOKEN`; disabled when unset):
`GET /ingest/v1/dlq[?limit=&includeReplayed=true]`, `POST /ingest/v1/dlq/{id}/replay`.
CLI equivalent: `ingest-worker dlq list`, `ingest-worker dlq replay <id>`.

## Guarantees and their limits
- **Exactly-once effect per `idemKey` within a worker process**: dedupe on `processed_keys`, a per-key lock around
  *check → submit → mark*, and the chain's own `ERR_DUPLICATE`. Across processes, events of one deal share a Kafka
  partition (key = `dealId`), so one consumer handles them; the chain is the final guard.
- **`ConfirmLien` has no idempotency-key argument on chain.** A crash between the chain commit and
  `MarkProcessed` makes the retry hit `ERR_INVALID_TRANSITION` (deal already FUNDED), which ends in the DLQ after 5
  tries. It is visible and replay-safe, not silent. Fixing it needs a chaincode change (contract-change PR).
- Out-of-order tolerance is bounded: `WORKER_MAX_ATTEMPTS` (5) × `WORKER_REQUEUE_DELAY` (2 s) ≈ 8 s before a
  dependent event is dead-lettered. Raise the delay if upstream skew is larger; DLQ replay recovers the rest.
- The retry consumer sleeps until `retryAt` while holding the message, so a long delay occupies that worker.
- A malformed event (cannot succeed on retry) goes straight to the DLQ instead of burning 5 attempts.
- DLQ is the Kafka topic itself; a replay appends a `replayed` marker record, so the view survives restarts.

## Deviations from BUILD.md section 9 (flagged, not silent)
| BUILD.md | Here | Why |
|---|---|---|
| `worker/process.go` holds the worker functions | they are methods in `pipeline/worker.go` (same names/signatures) | `tests/burst` must import and run the production code; `package main` cannot be imported |
| chi router | stdlib `net/http` ServeMux (Go 1.22 patterns) | chi could not be downloaded when this was written; no behavioural difference |
| Go 1.22 | `go 1.22` in `ingest/go.mod` (CI/Docker use 1.24 toolchains) | matches BUILD.md; the gateway is on 1.26 separately |
| `VerifyHMAC(secret)` | provided, plus `VerifyHMACBySource(map)` used by the router | per-source secrets |
| Kafka/Postgres | behind `-tags adapters` (`internal/kafkaq`, `internal/pgstore`) | see below |

## Verification status (read this)
Verified here (`make test-ingest test-burst`, `-race`): event validation, HMAC, rate limit, handlers, worker
retry/requeue/DLQ, DLQ replay, offset low-water-mark, the gateway HTTP client, and the 500-event burst test.
The burst test was also **mutation-checked**: with the dedupe check disabled it fails with double releases.

**Not verified here** (no module access in the authoring sandbox; CI job `ingest-adapters` is their first real check):
- `internal/kafkaq` (franz-go) and `internal/pgstore` (pgx) have been parsed and gofmt-checked but **never compiled
  or run**. Run `make ingest-tidy` on a networked machine, then `make up-infra`, and commit `ingest/go.mod`/`go.sum`.
- End-to-end through real Redpanda/Postgres/gateway, and `make demo`, have not been run.
- The burst test uses an HTTP **fake** of the gateway's state machine, not the real chaincode.
