"""End-to-end tests against a running stack (`make up`). Owner: M5.

Every event takes the production path: sim-grn / sim-gst -> ingest (HMAC, Kafka, idempotent
workers) -> gateway -> chaincode -> listener -> read model -> bank outbox -> sim-bank. The tests
only read state through the gateway's public API. They are skipped when the stack is not up.
"""

from __future__ import annotations

import os
import random
import time
from collections.abc import Callable
from datetime import UTC, datetime, timedelta
from typing import Any

import httpx
import pytest

GATEWAY = os.getenv("E2E_GATEWAY_URL", "http://localhost:8080").rstrip("/")
SIM_BANK = os.getenv("E2E_SIM_BANK_URL", "http://localhost:8101").rstrip("/")
SIM_GRN = os.getenv("E2E_SIM_GRN_URL", "http://localhost:8102").rstrip("/")
SIM_GST = os.getenv("E2E_SIM_GST_URL", "http://localhost:8103").rstrip("/")
WEB = os.getenv("E2E_WEB_URL", "http://localhost:3000").rstrip("/")
INTERNAL_TOKEN = os.getenv("INTERNAL_TOKEN", "")
TIMEOUT = float(os.getenv("E2E_TIMEOUT_SECONDS", "60"))

USERS = {
    "buyer-procurement": "BUYER",
    "buyer-planthead": "BUYER",
    "buyer-cfo": "BUYER",
    "supplier-007": "SUPPLIER",
    "supplier-012": "SUPPLIER",
    "bank-officer": "BANK",
    "auditor": "AUDITOR",
}


class Stack:
    def __init__(self) -> None:
        self.http = httpx.Client(timeout=20)

    # ---- gateway ----
    def gw(self, method: str, path: str, user: str | None = None, body: Any = None, expect: int | None = 200) -> Any:
        headers = {"X-Org": USERS[user], "X-User": user} if user else {}
        r = self.http.request(method, f"{GATEWAY}/api/v1{path}", headers=headers, json=body)
        if expect is not None:
            assert r.status_code == expect, f"{method} {path} as {user}: HTTP {r.status_code} {r.text}"
        return r.json() if r.content else None

    def deal(self, deal_id: str, user: str = "buyer-procurement") -> dict:
        return self.gw("GET", f"/deals/{deal_id}", user)

    def tranche(self, deal_id: str, n: int) -> dict | None:
        return next((t for t in self.deal(deal_id)["tranches"] if t["trancheNo"] == n), None)

    def outbox(self, deal_id: str) -> list[dict]:
        return self.gw("GET", f"/payments/outbox?dealId={deal_id}", "bank-officer")["items"]

    def advance_mock_clock(self, days: int) -> None:
        r = self.http.post(f"{GATEWAY}/internal/v1/mock/clock", headers={"X-Internal-Token": INTERNAL_TOKEN}, json={"advanceDays": days})
        assert r.status_code == 200, f"mock clock: HTTP {r.status_code} {r.text}"

    # ---- simulators ----
    def grn(self, deal_id: str, n: int, qty: int, dupes: int = 0, shuffle: bool = False) -> dict:
        body = {"dealId": deal_id, "trancheNo": n, "lines": [{"lineNo": 1, "deliveredQty": qty}], "dupes": dupes, "shuffle": shuffle}
        r = self.http.post(f"{SIM_GRN}/sim/grn/emit", json=body)
        assert r.status_code == 200, r.text
        return r.json()

    def qc(self, deal_id: str, n: int, accepted: int, rejected: int, dupes: int = 0) -> dict:
        body = {
            "dealId": deal_id,
            "trancheNo": n,
            "results": [{"lineNo": 1, "acceptedQty": accepted, "rejectedQty": rejected}],
            "dupes": dupes,
        }
        r = self.http.post(f"{SIM_GRN}/sim/qc/emit", json=body)
        assert r.status_code == 200, r.text
        return r.json()

    def invoice(self, deal_id: str, irn: str) -> dict:
        r = self.http.post(f"{SIM_GST}/sim/invoice/emit", json={"dealId": deal_id, "irn": irn})
        assert r.status_code == 200, r.text
        return r.json()

    def chaos(self, **kw: int) -> dict:
        return self.http.post(f"{SIM_BANK}/bank/v1/_chaos", json=kw).json()

    # ---- helpers ----
    def create_deal(self, supplier: str, category: str, term: int, qty: int, price_paise: int, applies: bool = True) -> str:
        deal_id = f"DL-{datetime.now(UTC):%Y%m%d}-{random.randint(1000, 9999)}"
        mse: dict[str, Any] = {"category": category, "termDays": term, "applies": applies}
        if applies:
            mse.update(udyamNo=f"UDYAM-KL-00-{int(supplier[4:]):07d}", writtenAgreement=term == 45)
        body = {
            "dealId": deal_id,
            "poNumber": f"PO-E2E-{deal_id[-4:]}",
            "supplierId": supplier,
            "deliveryDeadline": (datetime.now(UTC) + timedelta(days=60)).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "mse": mse,
            "lines": [{"lineNo": 1, "sku": "SKU-STEEL-12", "orderedQty": qty, "unitPricePaise": price_paise}],
        }
        r = self.http.post(f"{GATEWAY}/api/v1/deals", headers={"X-Org": "BUYER", "X-User": "buyer-procurement"}, json=body)
        if r.status_code == 400 and "already exists" in r.text:
            return self.create_deal(supplier, category, term, qty, price_paise, applies)
        assert r.status_code == 201, r.text
        self.gw("POST", f"/deals/{deal_id}/lien", "bank-officer", {"lienRef": f"LIEN-{deal_id[-4:]}", "amountPaise": qty * price_paise})
        return deal_id


def eventually(what: str, check: Callable[[], Any], timeout: float = TIMEOUT) -> Any:
    deadline = time.monotonic() + timeout
    last: Any = None
    while time.monotonic() < deadline:
        try:
            last = check()
            if last:
                return last
        except (AssertionError, httpx.HTTPError, KeyError, TypeError) as e:
            last = e
        time.sleep(0.5)
    raise AssertionError(f"timed out after {timeout:.0f}s waiting for: {what} (last: {last!r})")


@pytest.fixture(scope="session")
def stack() -> Stack:
    try:
        health = httpx.get(f"{GATEWAY}/health", timeout=3).json()
        for url in (f"{SIM_GRN}/health", f"{SIM_BANK}/health"):
            httpx.get(url, timeout=3).raise_for_status()
    except (httpx.HTTPError, ValueError) as e:
        pytest.skip(f"stack not running ({e}); start it with `make up`")
    s = Stack()
    s.mode = health.get("chain", {}).get("mode", "")  # type: ignore[attr-defined]
    return s
