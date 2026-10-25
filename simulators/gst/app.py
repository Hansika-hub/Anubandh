"""sim-gst (:8103): GST e-invoice (IRN) registry.

Contract (BUILD.md section 11):
  GET /gst/v1/irn/{irn} -> {irn, valid: true, gstin, invoiceValuePaise, invoiceDate}

The demo deals' IRNs come from fixtures/parties.json. Any other well-formed IRN is valid with
deterministic details, except IRNs that start with "BAD", which are reported invalid. A malformed
IRN is a 400.

Demo helper (not in BUILD.md): POST /sim/invoice/emit {dealId, irn} checks the IRN and, if valid,
sends the signed INVOICE webhook to ingest (source gst), which ends in VerifyInvoice on chain.
"""

from __future__ import annotations

import hashlib
import re
from datetime import UTC, date, datetime, timedelta

import httpx
from pydantic import BaseModel, ConfigDict, Field

from simulators.common import SimError, fixture, new_app, send_webhook

IRN_RE = re.compile(r"^[A-Za-z0-9-]{4,64}$")


class EmitIn(BaseModel):
    model_config = ConfigDict(extra="forbid")
    dealId: str = Field(pattern=r"^DL-\d{8}-\d{4}$")
    irn: str = Field(min_length=4, max_length=64)


def lookup(irn: str) -> dict:
    if not IRN_RE.match(irn):
        raise SimError(400, "ERR_BAD_REQUEST", "irn must be 4-64 characters of [A-Za-z0-9-]")
    data = fixture("parties.json")
    suppliers = {s["supplierId"]: s for s in data["suppliers"]}
    for deal in data["demoDeals"]:
        if deal["invoiceIrn"] == irn:
            value = sum(ln["orderedQty"] * ln["unitPricePaise"] for ln in deal["lines"])
            return {
                "irn": irn,
                "valid": True,
                "gstin": suppliers[deal["supplierId"]]["gstin"],
                "invoiceValuePaise": value,
                "invoiceDate": "2026-10-20",
                "supplierId": deal["supplierId"],
            }
    if irn.upper().startswith("BAD"):
        return {"irn": irn, "valid": False, "reason": "IRN cancelled or not registered on the IRP (simulated)"}
    h = int(hashlib.sha256(irn.encode()).hexdigest(), 16)
    supplier = data["suppliers"][h % len(data["suppliers"])]
    day = date(2026, 10, 1) + timedelta(days=h % 25)
    return {
        "irn": irn,
        "valid": True,
        "gstin": supplier["gstin"],
        "invoiceValuePaise": (h % 9_000 + 1_000) * 10_000,
        "invoiceDate": day.isoformat(),
        "supplierId": supplier["supplierId"],
    }


def create_app(transport: httpx.AsyncBaseTransport | None = None):
    app = new_app("sim-gst", "Anubandh sim-gst")

    @app.get("/gst/v1/irn/{irn}")
    def get_irn(irn: str) -> dict:
        return lookup(irn)

    @app.post("/sim/invoice/emit")
    async def emit_invoice(body: EmitIn) -> dict:
        info = lookup(body.irn)
        if not info["valid"]:
            raise SimError(422, "ERR_BAD_REQUEST", f"IRN {body.irn} is not valid; no invoice event sent", info)
        envelope = {
            "eventType": "INVOICE",
            "idemKey": f"gst:{body.irn}",
            "dealId": body.dealId,
            "payload": {"irn": body.irn},
            "occurredAt": datetime.now(UTC).strftime("%Y-%m-%dT%H:%M:%SZ"),
        }
        async with httpx.AsyncClient(timeout=10, transport=transport) as client:
            res = await send_webhook(client, "invoice", "gst", envelope)
        if res["status"] != 202:
            raise SimError(502, "ERR_INTERNAL", "ingest rejected the invoice event", res)
        return {"idemKey": envelope["idemKey"], "irn": info, "ingest": res}

    return app


app = create_app()
