"""sim-bank (:8101): lien marking and payment rails for the gateway's bank outbox.

Contract (BUILD.md section 11):
  POST /bank/v1/liens               {dealId, amountPaise, buyerAccountRef} -> {lienRef}
  POST /bank/v1/payments            {instructionId, dealId, trancheNo, amountPaise, rail, supplierAccountRef}
                                    -> {bankRef, status: ACCEPTED}; a repeated instructionId returns the same bankRef
  GET  /bank/v1/payments/{bankRef}  -> {status: ACCEPTED | SETTLED | FAILED}
  POST /bank/v1/_chaos              {failNext: n} -> the next n payment calls fail with HTTP 503

A payment settles SETTLE_AFTER_SECONDS (default 2) after it was accepted. _chaos can also make the
next n payments end FAILED (failSettlementNext), which exercises the outbox's failure path.
"""

from __future__ import annotations

import os
import threading
import time
from datetime import UTC, datetime
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field

from simulators.common import SimError, new_app

DEAL_ID = r"^DL-\d{8}-\d{4}$"


class LienIn(BaseModel):
    model_config = ConfigDict(extra="ignore")
    dealId: str = Field(pattern=DEAL_ID)
    amountPaise: int = Field(gt=0)
    buyerAccountRef: str = Field(min_length=1, max_length=64)


class PaymentIn(BaseModel):
    model_config = ConfigDict(extra="ignore")
    instructionId: str = Field(min_length=1, max_length=128)
    dealId: str = Field(pattern=DEAL_ID)
    trancheNo: int = Field(ge=1)
    amountPaise: int = Field(gt=0)
    rail: Literal["UPI", "IMPS", "NEFT", "RTGS"]
    supplierAccountRef: str = Field(min_length=1, max_length=64)


class ChaosIn(BaseModel):
    model_config = ConfigDict(extra="forbid")
    failNext: int = Field(default=0, ge=0, le=1000)
    failSettlementNext: int = Field(default=0, ge=0, le=1000)


def _iso(ts: float) -> str:
    return datetime.fromtimestamp(ts, UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


def create_app(settle_after: float | None = None, clock=time.time):
    settle = float(os.getenv("SETTLE_AFTER_SECONDS", "2")) if settle_after is None else settle_after
    app = new_app("sim-bank", "Anubandh sim-bank")
    lock = threading.Lock()
    liens: dict[str, dict] = {}
    payments: dict[str, dict] = {}  # by instructionId
    by_ref: dict[str, dict] = {}
    chaos = {"failNext": 0, "failSettlementNext": 0}

    def status_of(p: dict) -> str:
        if p["outcome"] == "FAILED" and clock() - p["acceptedAt"] >= settle:
            return "FAILED"
        return "SETTLED" if p["outcome"] == "SETTLED" and clock() - p["acceptedAt"] >= settle else "ACCEPTED"

    def view(p: dict) -> dict:
        st = status_of(p)
        out = {k: p[k] for k in ("bankRef", "instructionId", "dealId", "trancheNo", "amountPaise", "rail", "supplierAccountRef")}
        out.update(status=st, acceptedAt=_iso(p["acceptedAt"]))
        if st != "ACCEPTED":
            out["completedAt"] = _iso(p["acceptedAt"] + settle)
        if st == "FAILED":
            out["reason"] = "beneficiary account rejected the credit (simulated)"
        return out

    @app.post("/bank/v1/liens")
    def mark_lien(body: LienIn) -> dict:
        with lock:
            if (lien := liens.get(body.dealId)) is not None:
                if lien["amountPaise"] != body.amountPaise:
                    raise SimError(409, "ERR_DUPLICATE", f"deal {body.dealId} already has a lien of {lien['amountPaise']} paise")
                return {"lienRef": lien["lienRef"], "dealId": body.dealId, "amountPaise": lien["amountPaise"]}
            lien = {
                "lienRef": f"LIEN-{len(liens) + 1:04d}",
                "amountPaise": body.amountPaise,
                "buyerAccountRef": body.buyerAccountRef,
            }
            liens[body.dealId] = lien
            return {"lienRef": lien["lienRef"], "dealId": body.dealId, "amountPaise": body.amountPaise}

    @app.post("/bank/v1/payments")
    def pay(body: PaymentIn) -> dict:
        with lock:
            if chaos["failNext"] > 0:
                chaos["failNext"] -= 1
                raise SimError(503, "ERR_INTERNAL", "bank rail unavailable (chaos)", {"remaining": chaos["failNext"]})
            if (p := payments.get(body.instructionId)) is not None:
                same = (p["dealId"], p["trancheNo"], p["amountPaise"], p["supplierAccountRef"]) == (
                    body.dealId,
                    body.trancheNo,
                    body.amountPaise,
                    body.supplierAccountRef,
                )
                if not same:
                    msg = f"instructionId {body.instructionId} was used for a different payment"
                    raise SimError(409, "ERR_DUPLICATE", msg)
                return {"bankRef": p["bankRef"], "status": status_of(p)}
            outcome = "SETTLED"
            if chaos["failSettlementNext"] > 0:
                chaos["failSettlementNext"] -= 1
                outcome = "FAILED"
            p = body.model_dump() | {"bankRef": f"UTR-{len(payments) + 1:06d}", "acceptedAt": clock(), "outcome": outcome}
            payments[body.instructionId] = p
            by_ref[p["bankRef"]] = p
            return {"bankRef": p["bankRef"], "status": "ACCEPTED"}

    @app.get("/bank/v1/payments/{bank_ref}")
    def payment_status(bank_ref: str) -> dict:
        with lock:
            p = by_ref.get(bank_ref)
            if p is None:
                raise SimError(404, "ERR_NOT_FOUND", f"no payment {bank_ref}")
            return view(p)

    @app.get("/bank/v1/payments")
    def list_payments(dealId: str | None = None) -> dict:
        with lock:
            items = [view(p) for p in payments.values() if dealId is None or p["dealId"] == dealId]
        return {"items": items, "total": len(items)}

    @app.post("/bank/v1/_chaos")
    def set_chaos(body: ChaosIn) -> dict:
        with lock:
            chaos.update(failNext=body.failNext, failSettlementNext=body.failSettlementNext)
            return dict(chaos)

    return app


app = create_app()
