-- Anubandh forecast tables (BUILD.md §7). DDL owned by M2, rows written by the forecast service (M4).
-- Idempotent. Enum CHECKs mirror BUILD.md §4.1 so contract drift fails loudly at write time.

CREATE TABLE IF NOT EXISTS cash_accounts (
    account_id              text   PRIMARY KEY,
    org_id                  text   NOT NULL,
    name                    text   NOT NULL DEFAULT '',
    balance_paise           bigint NOT NULL DEFAULT 0,
    min_buffer_paise        bigint NOT NULL DEFAULT 0 CHECK (min_buffer_paise >= 0),
    credit_line_limit_paise bigint NOT NULL DEFAULT 0 CHECK (credit_line_limit_paise >= 0),
    credit_line_used_paise  bigint NOT NULL DEFAULT 0 CHECK (credit_line_used_paise >= 0)
);
CREATE INDEX IF NOT EXISTS cash_accounts_org_idx ON cash_accounts (org_id);

CREATE TABLE IF NOT EXISTS receivables (
    receivable_id text    PRIMARY KEY,
    customer_id   text    NOT NULL,
    invoice_no    text    NOT NULL DEFAULT '',
    amount_paise  bigint  NOT NULL CHECK (amount_paise >= 0),
    due_date      date    NOT NULL,
    status        text    NOT NULL DEFAULT 'OPEN',
    disputed      boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS receivables_due_idx ON receivables (due_date);

CREATE TABLE IF NOT EXISTS suppliers_profile (
    supplier_id     text             PRIMARY KEY,
    mean_delay_days double precision NOT NULL DEFAULT 0,
    std_delay_days  double precision NOT NULL DEFAULT 0,
    qc_reject_rate  double precision NOT NULL DEFAULT 0 CHECK (qc_reject_rate >= 0 AND qc_reject_rate <= 1),
    mse_category    text CHECK (mse_category IN ('MICRO', 'SMALL', 'MEDIUM', 'NONE'))
);

CREATE TABLE IF NOT EXISTS cash_band (
    org_id       text        NOT NULL,
    ts           timestamptz NOT NULL,
    granularity  text        NOT NULL CHECK (granularity IN ('HOURLY', 'DAILY')),
    p10_paise    bigint      NOT NULL,
    p50_paise    bigint      NOT NULL,
    p90_paise    bigint      NOT NULL,
    generated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, ts, granularity)
);

CREATE TABLE IF NOT EXISTS forecast_alerts (
    alert_id       text             PRIMARY KEY,
    org_id         text             NOT NULL,
    severity       text             NOT NULL CHECK (severity IN ('INFO', 'WARN', 'CRITICAL')),
    shortfall_prob double precision NOT NULL CHECK (shortfall_prob >= 0 AND shortfall_prob <= 1),
    window_start   timestamptz      NOT NULL,
    window_end     timestamptz      NOT NULL,
    drivers        jsonb            NOT NULL DEFAULT '[]'::jsonb,
    created_at     timestamptz      NOT NULL DEFAULT now(),
    acked          boolean          NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS forecast_alerts_org_idx ON forecast_alerts (org_id, acked, created_at DESC);

CREATE TABLE IF NOT EXISTS forecast_actions (
    action_id      text    PRIMARY KEY,
    alert_id       text    NOT NULL REFERENCES forecast_alerts (alert_id) ON DELETE CASCADE,
    action_type    text    NOT NULL CHECK (action_type IN ('SWEEP', 'DRAW_CREDIT_LINE', 'RESCHEDULE_NON_MSE', 'PARK_SURPLUS')),
    amount_paise   bigint  NOT NULL CHECK (amount_paise >= 0),
    est_cost_paise bigint  NOT NULL DEFAULT 0 CHECK (est_cost_paise >= 0),
    rank           int     NOT NULL CHECK (rank >= 1),
    rationale      text    NOT NULL DEFAULT '',
    mse_safe       boolean NOT NULL DEFAULT true
);
CREATE INDEX IF NOT EXISTS forecast_actions_alert_idx ON forecast_actions (alert_id, rank);
