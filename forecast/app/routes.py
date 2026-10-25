from __future__ import annotations

import os
from datetime import datetime, timezone
from threading import RLock

import numpy as np
import pandas as pd
from fastapi import APIRouter, HTTPException, Query

from forecast.app.schemas import WhatIfRequest
from forecast.data.generator import generate_synthetic_year
from forecast.engine.actions import rank_actions
from forecast.engine.alerts import evaluate_alerts
from forecast.engine.montecarlo import shortfall_probability, simulate_cash_paths, to_band
from forecast.eval.walkforward import walk_forward_eval
from forecast.features.build import build_release_timing_features
from forecast.models.timing_lgbm import train_quantile_models
from forecast.store.pg import read_table, save_records

router = APIRouter(prefix="/forecast/v1")
lock = RLock()
state: dict = {"data": None, "bands": {}, "alerts": {}, "models": None, "evaluation": None}


def get_data() -> dict:
    with lock:
        if state["data"] is None:
            state["data"] = generate_synthetic_year()
        return state["data"]


def output_band(org_id: str, horizon: str, shift_ids: list[str] | None = None, days: int = 0) -> dict:
    if horizon not in {"14d", "intraday"}:
        raise HTTPException(400, {"error": {"code": "ERR_BAD_REQUEST", "message": "Unsupported horizon", "details": {}}})
    data = get_data()
    today = pd.Timestamp.now(tz="UTC").floor("D")
    deals = data["open_deals"].copy()
    # Synthetic history is shifted to current demo date without modifying stored fixtures.
    offset = today - deals["createdAt"].min().floor("D") - pd.Timedelta(days=350)
    deals["dueAt"] = deals["dueAt"] + offset
    if shift_ids:
        mask = deals.dealId.isin(shift_ids)
        if deals.loc[mask, "mseCategory"].isin(["MICRO", "SMALL"]).any():
            raise HTTPException(400, detail={"error": {"code": "ERR_BAD_REQUEST", "message": "Cannot defer MSE obligations", "details": {}}})
        deals.loc[mask, "dueAt"] += pd.Timedelta(days=days)
    obligations = deals.loc[(deals.dueAt >= today) & (deals.dueAt < today + pd.Timedelta(days=15)), ["dueAt", "amountPaise"]]
    recv = data["receivables"].copy()
    recv["dueAt"] = recv["dueAt"] + offset
    recur = data["recurring"].copy()
    recur["dueAt"] += offset
    accounts = data["cash_accounts"]
    balance = int(accounts.balancePaise.sum())
    buffer = int(accounts.minBufferPaise.sum())
    paths = simulate_cash_paths(balance, obligations, recv, recur, horizon=horizon)
    band = to_band(paths)
    if horizon == "intraday":
        band["ts"] = pd.date_range(pd.Timestamp.now(tz="UTC").floor("h"), periods=24, freq="h")
    probs = shortfall_probability(paths, buffer)
    alerts = evaluate_alerts(org_id, band, probs, .3)
    for alert in alerts:
        alert["minBufferPaise"] = buffer
        alert["p10Paise"] = int(band.loc[band.ts.astype(str).str.startswith(alert["windowStart"][:10]), "p10"].iloc[0]) if horizon == "14d" else int(band.p10.min())
    result = {"orgId": org_id, "horizon": horizon, "currency": "INR", "points": [{"ts": row.ts.isoformat().replace("+00:00", "Z"), "p10": int(row.p10), "p50": int(row.p50), "p90": int(row.p90)} for row in band.itertuples()], "minBufferPaise": buffer}
    with lock:
        state["bands"][(org_id, horizon)] = result
        old = {a["alertId"]: a for a in state["alerts"].values()}
        for a in alerts:
            a["acked"] = old.get(a["alertId"], {}).get("acked", False)
            state["alerts"][a["alertId"]] = a
    save_records("cash_band", org_id, [{"recordId": f"{org_id}:{horizon}:{point['ts']}", **point} for point in result["points"]], "recordId")
    save_records("forecast_alerts", org_id, alerts, "alertId")
    return result


@router.get("/health")
def health() -> dict:
    return {"status": "ok", "svc": "forecast"}


@router.get("/band")
def band(orgId: str = Query(min_length=1), horizon: str = "14d") -> dict:
    with lock:
        result = state["bands"].get((orgId, horizon))
    return result if result is not None else output_band(orgId, horizon)


@router.get("/alerts")
def alerts(orgId: str = Query(min_length=1), acked: bool | None = None) -> dict:
    if not any(a["orgId"] == orgId for a in state["alerts"].values()):
        output_band(orgId, "14d")
    with lock:
        items = [a.copy() for a in state["alerts"].values() if a["orgId"] == orgId and (acked is None or a["acked"] == acked)]
    return {"items": items}


@router.get("/alerts/{alertId}/actions")
def actions(alertId: str) -> dict:
    with lock:
        alert = state["alerts"].get(alertId)
    if alert is None:
        raise HTTPException(404, detail={"error": {"code": "ERR_NOT_FOUND", "message": "Unknown alert", "details": {}}})
    items = rank_actions(alert, get_data()["cash_accounts"].to_dict("records"), pd.DataFrame())
    save_records("forecast_actions", alert["orgId"], items, "actionId")
    return {"items": items}


@router.post("/alerts/{alertId}/ack")
def acknowledge(alertId: str) -> dict:
    with lock:
        if alertId not in state["alerts"]:
            raise HTTPException(404, detail={"error": {"code": "ERR_NOT_FOUND", "message": "Unknown alert", "details": {}}})
        state["alerts"][alertId]["acked"] = True
        record = state["alerts"][alertId].copy()
    save_records("forecast_alerts", record["orgId"], [record], "alertId")
    return {"alertId": alertId, "acked": True}


@router.post("/refresh")
def refresh(orgId: str = Query(min_length=1)) -> dict:
    if os.getenv("PG_DSN"):
        # DB read integration is deliberately opt-in; no guessed physical column mappings.
        for table in ("deals", "tranches", "receivables", "cash_accounts", "suppliers_profile"):
            read_table(table, orgId)
        raise HTTPException(501, detail={"error": {"code": "ERR_INTERNAL", "message": "Database schema-to-forecast adapter not yet configured", "details": {}}})
    output_band(orgId, "14d")
    return {"status": "refreshed", "orgId": orgId, "source": "synthetic"}


@router.post("/retrain")
def retrain() -> dict:
    data = get_data()
    feature_df = build_release_timing_features(data["open_deals"], data["profiles"])
    models = train_quantile_models(feature_df)
    with lock:
        state["models"] = models
    return {"status": "trained", "rows": len(feature_df), "quantiles": [.1, .5, .9]}


@router.post("/whatif")
def whatif(body: WhatIfRequest) -> dict:
    return output_band(body.orgId, "14d", body.shiftDealIds, body.days)


@router.get("/metrics/evaluation")
def evaluation() -> dict:
    with lock:
        if state["evaluation"] is not None:
            return state["evaluation"]
    data = get_data()
    features = build_release_timing_features(data["open_deals"], data["profiles"])
    features["createdAt"] = data["open_deals"]["createdAt"].to_numpy()
    result = walk_forward_eval(features)
    with lock:
        state["evaluation"] = result
    return result


@router.post("/seed/synthetic")
def seed() -> dict:
    with lock:
        state["data"] = generate_synthetic_year()
        state["bands"].clear()
        state["alerts"].clear()
        state["evaluation"] = None
        state["models"] = None
    return {"status": "seeded", "source": "synthetic", "suppliers": 50, "customers": 30, "deals": len(state["data"]["open_deals"])}
