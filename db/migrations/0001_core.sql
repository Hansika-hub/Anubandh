-- Anubandh read model: core ledger projection (BUILD.md §7).
-- Owner: M2. Idempotent: every statement is safe to re-run (psql -f or the gateway migrator).
-- Projection tables deliberately carry no foreign keys: rows arrive from the chain event stream
-- in block order, and a child row (e.g. a tranche) may be projected before its parent is refreshed.

CREATE TABLE IF NOT EXISTS deals (
    deal_id              text        PRIMARY KEY,
    po_number            text        NOT NULL DEFAULT '',
    buyer_id             text        NOT NULL DEFAULT '',
    supplier_id          text        NOT NULL DEFAULT '',
    status               text        NOT NULL DEFAULT 'CREATED',
    lien_amount_paise    bigint      NOT NULL DEFAULT 0 CHECK (lien_amount_paise >= 0),
    lien_ref             text,
    total_released_paise bigint      NOT NULL DEFAULT 0 CHECK (total_released_paise >= 0),
    total_held_paise     bigint      NOT NULL DEFAULT 0 CHECK (total_held_paise >= 0),
    mse_category         text,
    mse_term_days        int,
    mse_applies          boolean     NOT NULL DEFAULT false,
    udyam_no             text,
    delivery_deadline    timestamptz,
    invoice_irn          text,
    invoice_verified     boolean     NOT NULL DEFAULT false,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS deals_status_idx      ON deals (status);
CREATE INDEX IF NOT EXISTS deals_buyer_idx       ON deals (buyer_id);
CREATE INDEX IF NOT EXISTS deals_supplier_idx    ON deals (supplier_id);
CREATE INDEX IF NOT EXISTS deals_created_at_idx  ON deals (created_at DESC);

CREATE TABLE IF NOT EXISTS deal_lines (
    deal_id          text   NOT NULL,
    line_no          int    NOT NULL CHECK (line_no >= 1),
    sku              text   NOT NULL DEFAULT '',
    ordered_qty      bigint NOT NULL DEFAULT 0 CHECK (ordered_qty >= 0),
    unit_price_paise bigint NOT NULL DEFAULT 0 CHECK (unit_price_paise >= 0),
    delivered_qty    bigint NOT NULL DEFAULT 0 CHECK (delivered_qty >= 0),
    accepted_qty     bigint NOT NULL DEFAULT 0 CHECK (accepted_qty >= 0),
    rejected_qty     bigint NOT NULL DEFAULT 0 CHECK (rejected_qty >= 0),
    held_qty         bigint NOT NULL DEFAULT 0 CHECK (held_qty >= 0),
    PRIMARY KEY (deal_id, line_no)
);

CREATE TABLE IF NOT EXISTS tranches (
    deal_id              text        NOT NULL,
    tranche_no           int         NOT NULL CHECK (tranche_no >= 1),
    status               text        NOT NULL DEFAULT 'PENDING',
    accepted_value_paise bigint      NOT NULL DEFAULT 0 CHECK (accepted_value_paise >= 0),
    held_value_paise     bigint      NOT NULL DEFAULT 0 CHECK (held_value_paise >= 0),
    delivered_at         timestamptz,
    accepted_at          timestamptz,
    deemed               boolean     NOT NULL DEFAULT false,
    mse_clock_start      timestamptz,
    mse_deadline         timestamptz,
    bank_ref             text,
    rail                 text,
    released_at          timestamptz,
    settled_at           timestamptz,
    PRIMARY KEY (deal_id, tranche_no)
);
CREATE INDEX IF NOT EXISTS tranches_mse_deadline_idx ON tranches (mse_deadline) WHERE mse_deadline IS NOT NULL;
CREATE INDEX IF NOT EXISTS tranches_status_idx       ON tranches (status);

CREATE TABLE IF NOT EXISTS approvals (
    deal_id    text        NOT NULL,
    tranche_no int         NOT NULL,
    role       text        NOT NULL,
    user_id    text        NOT NULL DEFAULT '',
    signed_at  timestamptz,
    PRIMARY KEY (deal_id, tranche_no, role)
);

CREATE TABLE IF NOT EXISTS held_portions (
    deal_id     text   NOT NULL,
    tranche_no  int    NOT NULL,
    line_no     int    NOT NULL,
    qty         bigint NOT NULL DEFAULT 0 CHECK (qty >= 0),
    value_paise bigint NOT NULL DEFAULT 0 CHECK (value_paise >= 0),
    exit        text,
    state       text   NOT NULL DEFAULT 'HELD',
    PRIMARY KEY (deal_id, tranche_no, line_no)
);

CREATE TABLE IF NOT EXISTS deal_events (
    id         bigserial   PRIMARY KEY,
    deal_id    text        NOT NULL DEFAULT '',
    tranche_no int,
    event_name text        NOT NULL,
    payload    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    tx_id      text,
    block_no   bigint,
    ts         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS deal_events_deal_idx ON deal_events (deal_id, id);
-- One chaincode event per Fabric transaction: (tx_id, event_name) makes projection idempotent on replay.
CREATE UNIQUE INDEX IF NOT EXISTS deal_events_tx_event_uq ON deal_events (tx_id, event_name) WHERE tx_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS chain_checkpoint (
    listener_id text   PRIMARY KEY,
    block_no    bigint NOT NULL DEFAULT 0 CHECK (block_no >= 0),
    tx_index    int    NOT NULL DEFAULT 0 CHECK (tx_index >= 0)
);
