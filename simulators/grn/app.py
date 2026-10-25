"""sim-grn (:8102): a warehouse system that sends signed goods-receipt and quality-check webhooks.

Contract (BUILD.md section 11):
  POST /sim/grn/emit  {dealId, trancheNo, lines: [{lineNo, deliveredQty}], dupes, shuffle}
  POST /sim/qc/emit   {dealId, trancheNo, results: [{lineNo, acceptedQty, rejectedQty}], dupes, shuffle}

Each call builds one InboundEvent (BUILD.md section 9.2) and delivers it 1 + dupes times to
ingest, signed with HMAC_SECRET_WAREHOUSE. With shuffle the copies go out concurrently, in random
order and with jitter, which is what a flaky warehouse integration looks like to ingest.
"""

from __future__ import annotations

import asyncio
import random
import secrets
from datetime import UTC, datetime

import httpx
from pydantic import BaseModel, ConfigDict, Field

from simulators.common import SimError, new_app, send_webhook

DEAL_ID = r"^DL-\d{8}-\d{4}$"
EVENT_ID = r"^[A-Za-z0-9._-]{1,64}$"


class GRLine(BaseModel):
    model_config = ConfigDict(extra="forbid")
    lineNo: int = Field(ge=1)
    deliveredQty: int = Field(ge=1)


class QCResult(BaseModel):
    model_config = ConfigDict(extra="forbid")
    lineNo: int = Field(ge=1)
    acceptedQty: int = Field(ge=0)
    rejectedQty: int = Field(ge=0)


class Delivery(BaseModel):
    model_config = ConfigDict(extra="forbid")
    dealId: str = Field(pattern=DEAL_ID)
    trancheNo: int = Field(ge=1)
    dupes: int = Field(default=0, ge=0, le=50)
    shuffle: bool = False
    eventId: str | None = Field(default=None, pattern=EVENT_ID, description="source event id; random if omitted")


class GRNIn(Delivery):
    lines: list[GRLine] = Field(min_length=1, max_length=500)


class QCIn(Delivery):
    results: list[QCResult] = Field(min_length=1, max_length=500)


def create_app(transport: httpx.AsyncBaseTransport | None = None, jitter: float = 0.05):
    app = new_app("sim-grn", "Anubandh sim-grn")

    async def emit(path: str, event_type: str, prefix: str, body: Delivery, payload: list[dict]) -> dict:
        idem = f"warehouse:{body.eventId or prefix + '-' + secrets.token_hex(4)}"
        envelope = {
            "eventType": event_type,
            "idemKey": idem,
            "dealId": body.dealId,
            "trancheNo": body.trancheNo,
            "payload": payload,
            "occurredAt": datetime.now(UTC).strftime("%Y-%m-%dT%H:%M:%SZ"),
        }
        copies = 1 + body.dupes
        async with httpx.AsyncClient(timeout=10, transport=transport) as client:
            if body.shuffle:

                async def one() -> dict:
                    await asyncio.sleep(random.uniform(0, jitter))
                    return await send_webhook(client, path, "warehouse", envelope)

                responses = list(await asyncio.gather(*(one() for _ in range(copies))))
            else:
                responses = [await send_webhook(client, path, "warehouse", envelope) for _ in range(copies)]
        accepted = sum(1 for r in responses if r["status"] == 202)
        result = {"idemKey": idem, "sent": copies, "accepted": accepted, "responses": responses}
        if accepted == 0:
            raise SimError(502, "ERR_INTERNAL", "ingest accepted none of the copies", result)
        return result

    @app.post("/sim/grn/emit")
    async def emit_grn(body: GRNIn) -> dict:
        return await emit("goods-receipt", "GOODS_RECEIPT", "GR", body, [ln.model_dump() for ln in body.lines])

    @app.post("/sim/qc/emit")
    async def emit_qc(body: QCIn) -> dict:
        return await emit("quality-check", "QUALITY_CHECK", "QC", body, [r.model_dump() for r in body.results])

    return app


app = create_app()
