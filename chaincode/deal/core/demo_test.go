package core

import "testing"

// TestDemoScenarioSteps1to7 replays BUILD.md §15 with the frozen fixture IDs
// (DL-20261020-0001 / PO-4411 / SUP-007 / 100 x Rs10,000 = Rs10L). Steps 5's rail choice and
// step 8 (forecast) belong to M2/M4; everything the chain must guarantee is asserted here.
func TestDemoScenarioSteps1to7(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{id: "DL-20261020-0001", mse: true}) // 1 create + 2 lien
	if c.deal(t, id).Status != DealFunded {
		t.Fatal("step 2: FUNDED")
	}
	// 3: the same goods receipt arrives twice (and a stale copy after QC) -> applied once
	c.grn(t, id, 1, 100, "warehouse:GR-88123")
	c.wantErr(t, ErrDuplicate, warehouse, func(e *Engine) error {
		return e.PostGoodsReceipt(id, 1, `[{"lineNo":1,"deliveredQty":100}]`, "warehouse:GR-88123")
	})
	c.must(t, supplier7, func(e *Engine) error { return e.AcknowledgeDelivery(id, 1) })
	// 4: QC 80 accepted / 20 rejected
	c.qc(t, id, 1, 80, 20, "erp:QC-5521")
	c.wantErr(t, ErrDuplicate, procure, func(e *Engine) error {
		return e.RecordQualityCheck(id, 1, `[{"lineNo":1,"acceptedQty":80,"rejectedQty":20}]`, "erp:QC-5521")
	})
	// 5: Rs8L approvals met -> release; Rs2L held
	ev := c.approveAndRelease(t, id, 1, []Caller{plantHead}, "bank:REL-DL1-T1")
	if ev[0].Payload["amountPaise"].(int64) != 8*lakh {
		t.Fatal("step 5: 8L")
	}
	c.must(t, bank, func(e *Engine) error { return e.ConfirmSettlement(id, 1, "UTR-0001", "NEFT") })
	if h := c.held(t, id, 1, 1); h.ValuePaise != 2*lakh || h.State != HeldStateHeld {
		t.Fatal("step 5: 2L held")
	}
	// 6: supplier ships 20 replacements -> new tranche
	c.must(t, supplier7, func(e *Engine) error { return e.ShipReplacement(id, 1, 1, 20) })
	if c.tranche(t, id, 2).Status != TrPending {
		t.Fatal("step 6: new PENDING tranche")
	}
	// 7: fast-forward 15 days on another deal (SUP-012, MICRO, 15-day) -> deemed acceptance + clock
	id2 := c.mkDeal(t, dealOpts{id: "DL-20261020-0002", mse: true, termDays: 15, supplier: "SUP-012", qty: 600})
	c.grn(t, id2, 1, 600, "warehouse:GR-88200")
	c.now = c.now.Add(15 * day)
	c.must(t, bank, func(e *Engine) error { return e.TickDeadlines(id2) })
	tr := c.tranche(t, id2, 1)
	if tr.Status != TrDeemedAccepted || tr.MseDeadline == nil || !tr.MseDeadline.Equal(tr.MseClockStart.Add(15*day)) {
		t.Fatalf("step 7: %+v", tr)
	}
	// the Rs60L deemed tranche needs PLANT_HEAD + CFO
	c.must(t, plantHead, func(e *Engine) error { return e.ApproveRelease(id2, 1) })
	c.wantErr(t, ErrPolicyNotSatisfied, procure, func(e *Engine) error { return e.ReleaseTranche(id2, 1, "bank:REL-DL2-T1") })
	c.approveAndRelease(t, id2, 1, []Caller{cfo}, "bank:REL-DL2-T1")
	c.invariant(t, id)
	c.invariant(t, id2)
}
