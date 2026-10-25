from __future__ import annotations

import os

import pandas as pd


def read_table(table: str, org_id: str | None = None) -> pd.DataFrame:
    allowed = {"deals", "tranches", "receivables", "cash_accounts", "suppliers_profile"}
    if table not in allowed:
        raise ValueError("Unknown table")
    dsn = os.getenv("PG_DSN")
    if not dsn:
        raise RuntimeError("PG_DSN not configured")
    import psycopg
    with psycopg.connect(dsn) as conn, conn.cursor() as cursor:
        cursor.execute(f'SELECT * FROM "{table}"' + (' WHERE "orgId" = %s' if org_id and table != 'suppliers_profile' else ''), (org_id,) if org_id and table != 'suppliers_profile' else ())
        return pd.DataFrame(cursor.fetchall(), columns=[d.name for d in cursor.description])


def ensure_forecast_tables() -> None:
    dsn = os.getenv("PG_DSN")
    if not dsn:
        return
    import psycopg
    with psycopg.connect(dsn) as conn:
        for name in ("cash_band", "forecast_alerts", "forecast_actions"):
            with conn.cursor() as cur:
                cur.execute(f'CREATE TABLE IF NOT EXISTS "{name}" ("recordId" TEXT PRIMARY KEY, "orgId" TEXT NOT NULL, "payload" JSONB NOT NULL, "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT now())')
        conn.commit()


def save_records(table: str, org_id: str, records: list[dict], key_field: str) -> None:
    if table not in {"cash_band", "forecast_alerts", "forecast_actions"}:
        raise ValueError("Writes restricted to forecast-owned tables")
    if not os.getenv("PG_DSN"):
        return
    import json

    import psycopg
    ensure_forecast_tables()
    with psycopg.connect(os.environ["PG_DSN"]) as conn:
        with conn.cursor() as cur:
            for r in records:
                cur.execute(f'INSERT INTO "{table}" ("recordId","orgId","payload") VALUES (%s,%s,%s::jsonb) ON CONFLICT ("recordId") DO UPDATE SET "payload"=EXCLUDED."payload", "updatedAt"=now()', (str(r[key_field]), org_id, json.dumps(r)))
        conn.commit()
