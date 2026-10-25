import hashlib
import hmac
import json

import httpx
import pytest
from fastapi.testclient import TestClient

from simulators.bank.app import create_app as bank_app
from simulators.grn.app import create_app as grn_app
from simulators.gst.app import create_app as gst_app
from simulators.udyam.app import create_app as udyam_app

SECRET = "test-warehouse-secret-0123456789"
GST_SECRET = "test-gst-secret-0123456789abcdef"


class Clock:
    def __init__(self):
        self.t = 1_000_000.0

    def __call__(self):
        return self.t


def payment(**kw):
    p = {
        "instructionId": "DL-20261020-0001/1",
        "dealId": "DL-20261020-0001",
        "trancheNo": 1,
        "amountPaise": 80_000_000,
        "rail": "NEFT",
        "supplierAccountRef": "ACCT-SUP-007",
    }
    p.update(kw)
    return p


def test_health_on_every_simulator():
    for app, svc in ((bank_app(), "sim-bank"), (grn_app(), "sim-grn"), (gst_app(), "sim-gst"), (udyam_app(), "sim-udyam")):
        assert TestClient(app).get("/health").json() == {"status": "ok", "svc": svc}


def test_bank_payment_is_idempotent_and_settles():
    clock = Clock()
    c = TestClient(bank_app(settle_after=2, clock=clock))
    first = c.post("/bank/v1/payments", json=payment())
    assert first.status_code == 200 and first.json()["status"] == "ACCEPTED"
    ref = first.json()["bankRef"]
    again = c.post("/bank/v1/payments", json=payment())
    assert again.json()["bankRef"] == ref, "a repeated instructionId must return the same bankRef"
    assert c.get(f"/bank/v1/payments/{ref}").json()["status"] == "ACCEPTED"
    clock.t += 2
    st = c.get(f"/bank/v1/payments/{ref}").json()
    assert st["status"] == "SETTLED" and st["amountPaise"] == 80_000_000
    assert c.get("/bank/v1/payments").json()["total"] == 1


def test_bank_rejects_reused_instruction_id_for_another_payment():
    c = TestClient(bank_app())
    c.post("/bank/v1/payments", json=payment())
    r = c.post("/bank/v1/payments", json=payment(amountPaise=1))
    assert r.status_code == 409 and r.json()["error"]["code"] == "ERR_DUPLICATE"


def test_bank_validates_body():
    c = TestClient(bank_app())
    for bad in (payment(rail="SWIFT"), payment(amountPaise=0), payment(dealId="DL-1"), {"instructionId": "x"}):
        r = c.post("/bank/v1/payments", json=bad)
        assert r.status_code == 400 and r.json()["error"]["code"] == "ERR_BAD_REQUEST", bad
    assert c.get("/bank/v1/payments/UTR-999999").status_code == 404


def test_bank_chaos_fails_next_calls_then_recovers():
    clock = Clock()
    c = TestClient(bank_app(settle_after=1, clock=clock))
    chaos = {"failNext": 2, "failSettlementNext": 1}
    assert c.post("/bank/v1/_chaos", json=chaos).json() == chaos
    assert c.post("/bank/v1/payments", json=payment()).status_code == 503
    assert c.post("/bank/v1/payments", json=payment()).status_code == 503
    ok = c.post("/bank/v1/payments", json=payment())
    assert ok.status_code == 200
    clock.t += 1
    assert c.get(f"/bank/v1/payments/{ok.json()['bankRef']}").json()["status"] == "FAILED"
    second = c.post("/bank/v1/payments", json=payment(instructionId="DL-20261020-0001/2", trancheNo=2))
    clock.t += 1
    assert c.get(f"/bank/v1/payments/{second.json()['bankRef']}").json()["status"] == "SETTLED"


def test_bank_lien_is_idempotent_per_deal():
    c = TestClient(bank_app())
    body = {"dealId": "DL-20261020-0001", "amountPaise": 100_000_000, "buyerAccountRef": "ACCT-BUYER-001"}
    a, b = c.post("/bank/v1/liens", json=body).json(), c.post("/bank/v1/liens", json=body).json()
    assert a["lienRef"] == b["lienRef"] and a["lienRef"].startswith("LIEN-")
    assert c.post("/bank/v1/liens", json=body | {"amountPaise": 5}).status_code == 409


class Recorder:
    """httpx transport that plays ingest: checks the signature and records each webhook."""

    def __init__(self, secret, status=202):
        self.secret, self.status, self.calls = secret, status, []

    def __call__(self, request: httpx.Request) -> httpx.Response:
        body = request.read()
        good = hmac.new(self.secret.encode(), body, hashlib.sha256).hexdigest()
        if request.headers["X-Signature"] != good:
            return httpx.Response(401, json={"error": {"code": "ERR_BAD_SIGNATURE"}})
        env = json.loads(body)
        assert request.headers["X-Idempotency-Key"] == env["idemKey"]
        self.calls.append((request.url.path, request.headers["X-Source"], env))
        return httpx.Response(self.status, json={"accepted": True, "idemKey": env["idemKey"]})


def test_grn_emit_sends_signed_duplicates(monkeypatch):
    monkeypatch.setenv("HMAC_SECRET_WAREHOUSE", SECRET)
    monkeypatch.setenv("INGEST_URL", "http://ingest.test")
    rec = Recorder(SECRET)
    c = TestClient(grn_app(transport=httpx.MockTransport(rec), jitter=0))
    r = c.post(
        "/sim/grn/emit",
        json={
            "dealId": "DL-20261020-0001",
            "trancheNo": 1,
            "eventId": "GR-88123",
            "lines": [{"lineNo": 1, "deliveredQty": 100}],
            "dupes": 2,
            "shuffle": True,
        },
    )
    assert r.status_code == 200, r.text
    assert r.json()["idemKey"] == "warehouse:GR-88123" and r.json()["accepted"] == 3
    assert len(rec.calls) == 3
    path, source, env = rec.calls[0]
    assert path == "/ingest/v1/events/goods-receipt" and source == "warehouse"
    assert env["eventType"] == "GOODS_RECEIPT" and env["payload"] == [{"lineNo": 1, "deliveredQty": 100}]
    assert env["trancheNo"] == 1 and env["occurredAt"].endswith("Z")


def test_qc_emit_and_ingest_failure(monkeypatch):
    monkeypatch.setenv("HMAC_SECRET_WAREHOUSE", SECRET)
    rec = Recorder(SECRET)
    c = TestClient(grn_app(transport=httpx.MockTransport(rec)))
    r = c.post(
        "/sim/qc/emit",
        json={"dealId": "DL-20261020-0001", "trancheNo": 1, "results": [{"lineNo": 1, "acceptedQty": 80, "rejectedQty": 20}]},
    )
    assert r.status_code == 200 and rec.calls[0][0] == "/ingest/v1/events/quality-check"
    assert rec.calls[0][2]["idemKey"].startswith("warehouse:QC-")
    down = TestClient(grn_app(transport=httpx.MockTransport(Recorder(SECRET, status=503))))
    r = down.post(
        "/sim/qc/emit",
        json={"dealId": "DL-20261020-0001", "trancheNo": 1, "results": [{"lineNo": 1, "acceptedQty": 80, "rejectedQty": 20}]},
    )
    assert r.status_code == 502 and r.json()["error"]["details"]["accepted"] == 0


def test_grn_requires_secret_and_valid_body(monkeypatch):
    monkeypatch.delenv("HMAC_SECRET_WAREHOUSE", raising=False)
    c = TestClient(grn_app(transport=httpx.MockTransport(Recorder(SECRET))))
    body = {"dealId": "DL-20261020-0001", "trancheNo": 1, "lines": [{"lineNo": 1, "deliveredQty": 1}]}
    assert c.post("/sim/grn/emit", json=body).json()["error"]["code"] == "ERR_INTERNAL"
    for bad in ({**body, "lines": []}, {**body, "dealId": "x"}, {**body, "eventId": "a:b"}):
        assert c.post("/sim/grn/emit", json=bad).status_code == 400


def test_gst_lookup():
    c = TestClient(gst_app())
    demo = c.get("/gst/v1/irn/IRN-PO-4411-0001").json()
    assert demo["valid"] and demo["invoiceValuePaise"] == 100_000_000 and demo["supplierId"] == "SUP-007"
    other = c.get("/gst/v1/irn/IRN-ANY-42").json()
    assert other["valid"] and other == c.get("/gst/v1/irn/IRN-ANY-42").json(), "deterministic"
    assert c.get("/gst/v1/irn/BAD-0001").json()["valid"] is False
    assert c.get("/gst/v1/irn/a$b").status_code in (400, 404)


def test_gst_emit_invoice(monkeypatch):
    monkeypatch.setenv("HMAC_SECRET_GST", GST_SECRET)
    rec = Recorder(GST_SECRET)
    c = TestClient(gst_app(transport=httpx.MockTransport(rec)))
    r = c.post("/sim/invoice/emit", json={"dealId": "DL-20261020-0001", "irn": "IRN-PO-4411-0001"})
    assert r.status_code == 200, r.text
    path, source, env = rec.calls[0]
    assert (path, source, env["eventType"], env["idemKey"], env["payload"]) == (
        "/ingest/v1/events/invoice",
        "gst",
        "INVOICE",
        "gst:IRN-PO-4411-0001",
        {"irn": "IRN-PO-4411-0001"},
    )
    assert "trancheNo" not in env
    bad = c.post("/sim/invoice/emit", json={"dealId": "DL-20261020-0001", "irn": "BAD-1"})
    assert bad.status_code == 422 and len(rec.calls) == 1


def test_udyam_lookup():
    c = TestClient(udyam_app())
    sup7 = c.get("/udyam/v1/UDYAM-KL-00-0000007").json()
    assert (sup7["category"], sup7["activity"], sup7["valid"], sup7["supplierId"]) == ("SMALL", "MANUFACTURING", True, "SUP-007")
    assert c.get("/udyam/v1/by-supplier/SUP-012").json()["category"] == "MICRO"
    assert c.get("/udyam/v1/UDYAM-KL-00-9999999").status_code == 404
    assert c.get("/udyam/v1/12345").status_code == 400


@pytest.mark.parametrize("sid,category,term,agreement", [("SUP-007", "SMALL", 45, True), ("SUP-012", "MICRO", 15, False)])
def test_fixtures_match_build_md(sid, category, term, agreement):
    from simulators.common import suppliers

    s = suppliers()[sid]
    assert (s["mseCategory"], s["termDays"], s["writtenAgreement"], s["accountRef"]) == (category, term, agreement, f"ACCT-{sid}")
    assert len(suppliers()) == 50
