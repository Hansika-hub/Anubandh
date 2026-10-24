-- Anubandh payment outbox + ingest idempotency (BUILD.md §7). Owner: M2. Idempotent.

-- One row per released tranche. The PK makes enqueueing idempotent: a replayed TrancheReleased
-- event can never create a second payment instruction for the same (deal, tranche).
CREATE TABLE IF NOT EXISTS payment_outbox (
    deal_id              text        NOT NULL,
    tranche_no           int         NOT NULL,
    amount_paise         bigint      NOT NULL CHECK (amount_paise > 0),
    supplier_account_ref text        NOT NULL DEFAULT '',
    rail                 text        NOT NULL CHECK (rail IN ('UPI', 'IMPS', 'NEFT', 'RTGS')),
    status               text        NOT NULL DEFAULT 'PENDING'
                                     CHECK (status IN ('PENDING', 'SENT', 'CONFIRMED', 'FAILED')),
    bank_ref             text,
    attempts             int         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error           text,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (deal_id, tranche_no)
);
CREATE INDEX IF NOT EXISTS payment_outbox_status_idx ON payment_outbox (status, updated_at);

-- Written by ingest workers (M3): an idem key is recorded once its chain submit has succeeded
-- or the chain has reported ERR_DUPLICATE.
CREATE TABLE IF NOT EXISTS processed_keys (
    idem_key     text        PRIMARY KEY,
    processed_at timestamptz NOT NULL DEFAULT now()
);
