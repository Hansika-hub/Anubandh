"""The BUILD.md demo scenario, end to end, through the real event path."""

from __future__ import annotations

import pytest

from conftest import INTERNAL_TOKEN, Stack, eventually

LAKH = 100_000 * 100  # paise


@pytest.mark.partial
def test_partial_release_80_of_100(stack: Stack) -> None:
    """Steps 1-6: GRN sent three times and shuffled, QC 80/20, Rs 8L released and settled by the
    bank, Rs 2L held, 20 replacements close it, and the full PO is paid exactly once."""
    deal = stack.create_deal("SUP-007", "SMALL", 45, qty=100, price_paise=1_000_000)

    sent = stack.grn(deal, 1, 100, dupes=2, shuffle=True)
    assert sent["sent"] == 3 and sent["accepted"] == 3, sent
    eventually("tranche 1 delivered", lambda: (stack.tranche(deal, 1) or {}).get("status") == "DELIVERED")
    stack.qc(deal, 1, 80, 20, dupes=1)

    d = eventually("deal PARTIALLY_ACCEPTED", lambda: (x := stack.deal(deal))["status"] == "PARTIALLY_ACCEPTED" and x)
    t1 = d["tranches"][0]
    assert t1["lines"][0]["deliveredQty"] == 100, "three GRN copies must count once"
    assert (t1["acceptedValuePaise"], t1["heldValuePaise"], d["totalHeldPaise"]) == (8 * LAKH, 2 * LAKH, 2 * LAKH)
    assert d["heldPortions"][0]["qty"] == 20

    approve = stack.gw("POST", f"/deals/{deal}/tranches/1/approve", "buyer-planthead")
    assert approve["satisfied"] is True and approve["release"]["txId"], approve

    def settled() -> list[dict]:
        rows = stack.outbox(deal)
        return rows if rows and rows[0]["status"] == "CONFIRMED" else []

    row = eventually("payment 1 confirmed by sim-bank", settled)[0]
    assert (row["amountPaise"], row["rail"]) == (8 * LAKH, "NEFT") and row["bankRef"].startswith("UTR-")
    eventually("tranche 1 SETTLED on chain", lambda: stack.tranche(deal, 1)["status"] == "SETTLED")

    stack.gw("POST", f"/deals/{deal}/held/1/1/replacement", "supplier-007", {"qty": 20})
    stack.grn(deal, 2, 20)
    eventually("replacement tranche delivered", lambda: (stack.tranche(deal, 2) or {}).get("status") == "DELIVERED")
    stack.qc(deal, 2, 20, 0)
    eventually("tranche 2 accepted", lambda: stack.tranche(deal, 2)["status"] == "ACCEPTED")
    stack.gw("POST", f"/deals/{deal}/tranches/2/approve", "buyer-planthead")

    def all_confirmed() -> list[dict]:
        rows = stack.outbox(deal)
        return rows if len(rows) == 2 and all(r["status"] == "CONFIRMED" for r in rows) else []

    eventually("both payments confirmed", all_confirmed)
    final = eventually("deal SETTLED", lambda: (x := stack.deal(deal))["status"] == "SETTLED" and x)
    assert final["totalReleasedPaise"] == final["lienAmountPaise"] == 10 * LAKH, "the full PO is paid exactly once"
    assert final["totalHeldPaise"] == 0


@pytest.mark.deadline
def test_deemed_acceptance_and_two_approver_policy(stack: Stack) -> None:
    """Step 7: a 15-day MICRO deal is deemed accepted when the buyer stays silent, and Rs 60L needs
    PLANT_HEAD + CFO. Needs the mock chain (its clock can be moved) and INTERNAL_TOKEN."""
    if getattr(stack, "mode", "") != "mock" or not INTERNAL_TOKEN:
        pytest.skip("needs FABRIC_MOCK=true and INTERNAL_TOKEN: a real network's clock cannot be fast-forwarded")
    deal = stack.create_deal("SUP-012", "MICRO", 15, qty=60, price_paise=10_000_000)
    stack.grn(deal, 1, 60)
    eventually("tranche delivered", lambda: (stack.tranche(deal, 1) or {}).get("status") == "DELIVERED")

    stack.advance_mock_clock(15)  # the gateway runs TickDeadlines right after moving the mock clock
    t = eventually("deemed acceptance", lambda: (x := stack.tranche(deal, 1))["deemedAcceptance"] and x)
    assert t["acceptedValuePaise"] == 60 * LAKH and t["mseDeadline"], t

    clocks = stack.gw("GET", f"/mse/clocks?dealId={deal}", "buyer-procurement")["items"]
    assert clocks and clocks[0]["termDays"] == 15 and clocks[0]["deemedAcceptance"]

    stack.gw("POST", f"/deals/{deal}/tranches/1/approve", "buyer-planthead")
    refused = stack.gw("POST", f"/deals/{deal}/tranches/1/release", "buyer-procurement", {}, expect=409)
    assert refused["error"]["code"] == "ERR_POLICY_NOT_SATISFIED"
    out = stack.gw("POST", f"/deals/{deal}/tranches/1/approve", "buyer-cfo")
    assert out["satisfied"] is True
    eventually("Rs 60L released", lambda: stack.tranche(deal, 1)["status"] in ("RELEASED", "SETTLED"))
    report = stack.gw("GET", "/mse/compliance-report", "auditor")["items"]
    assert any(r["dealId"] == deal for r in report)


def test_bank_outage_is_retried_by_the_outbox(stack: Stack) -> None:
    """sim-bank refuses the next two payment calls; the outbox retries until the bank accepts."""
    deal = stack.create_deal("SUP-007", "SMALL", 45, qty=10, price_paise=100_000)
    stack.invoice(deal, f"IRN-E2E-{deal[-4:]}")
    stack.grn(deal, 1, 10)
    eventually("delivered", lambda: (stack.tranche(deal, 1) or {}).get("status") == "DELIVERED")
    stack.qc(deal, 1, 10, 0)
    eventually("accepted", lambda: stack.tranche(deal, 1)["status"] == "ACCEPTED")
    stack.chaos(failNext=2)
    try:
        stack.gw("POST", f"/deals/{deal}/tranches/1/approve", "buyer-planthead")
        eventually("payment confirmed after retries", lambda: any(r["status"] == "CONFIRMED" for r in stack.outbox(deal)))
    finally:
        stack.chaos(failNext=0)
    assert stack.deal(deal)["invoice"]["verified"] is True, "the GST invoice event went through ingest too"


def test_out_of_order_qc_waits_for_its_grn(stack: Stack) -> None:
    """A QC that arrives before its goods receipt is requeued by the worker, not lost and not
    applied early; once the GRN lands, the QC applies."""
    deal = stack.create_deal("SUP-007", "SMALL", 45, qty=5, price_paise=100_000)
    stack.qc(deal, 1, 5, 0)  # arrives before the GRN
    stack.grn(deal, 1, 5)
    t = eventually("QC applied after the late GRN", lambda: (x := stack.tranche(deal, 1)) and x["status"] == "ACCEPTED" and x)
    assert t["lines"][0]["acceptedQty"] == 5
