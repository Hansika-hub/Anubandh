package core

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

// ---- BUILD §5.5 (names frozen) ----

func TestPartialAcceptance80of100(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{mse: true})
	c.grn(t, id, 1, 100, "warehouse:GR-1")
	ev := c.qc(t, id, 1, 80, 20, "erp:QC-1")
	tr := c.tranche(t, id, 1)
	if tr.AcceptedValuePaise != 8*lakh || tr.HeldValuePaise != 2*lakh {
		t.Fatalf("want 8L accepted / 2L held, got %d / %d", tr.AcceptedValuePaise, tr.HeldValuePaise)
	}
	if tr.Status != TrAccepted || c.deal(t, id).Status != DealPartiallyAccepted {
		t.Fatalf("tranche %s deal %s", tr.Status, c.deal(t, id).Status)
	}
	want := []string{"QualityRecorded", "HeldPortionCreated", "TrancheAccepted", "MseClockStarted"}
	if !reflect.DeepEqual(names(ev), want) {
		t.Fatalf("events %v want %v", names(ev), want)
	}
	rel := c.approveAndRelease(t, id, 1, []Caller{plantHead}, "bank:REL-1")
	if rel[0].Payload["amountPaise"].(int64) != 8*lakh {
		t.Fatalf("released %v", rel[0].Payload["amountPaise"])
	}
	d := c.deal(t, id)
	if d.TotalReleasedPaise != 8*lakh || d.TotalHeldPaise != 2*lakh {
		t.Fatalf("totals %d %d", d.TotalReleasedPaise, d.TotalHeldPaise)
	}
	if h := c.held(t, id, 1, 1); h.State != HeldStateHeld || h.ValuePaise != 2*lakh {
		t.Fatalf("held %+v", h)
	}
	c.invariant(t, id)
}

func TestNoDoubleReleaseSameTranche(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{})
	c.grn(t, id, 1, 100, "warehouse:GR-1")
	c.qc(t, id, 1, 100, 0, "erp:QC-1")
	c.approveAndRelease(t, id, 1, []Caller{plantHead}, "bank:REL-1")
	c.wantErr(t, ErrAlreadyReleased, procure, func(e *Engine) error { return e.ReleaseTranche(id, 1, "bank:REL-2") })
	c.wantErr(t, ErrDuplicate, procure, func(e *Engine) error { return e.ReleaseTranche(id, 1, "bank:REL-1") })
	if got := c.deal(t, id).TotalReleasedPaise; got != 10*lakh {
		t.Fatalf("released %d, want exactly 10L", got)
	}
}

func TestReleasedPlusHeldNeverExceedsLien(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{})
	c.grn(t, id, 1, 100, "warehouse:GR-1")
	c.invariant(t, id)
	c.qc(t, id, 1, 80, 20, "erp:QC-1")
	c.invariant(t, id)
	c.approveAndRelease(t, id, 1, []Caller{plantHead}, "bank:REL-1")
	c.invariant(t, id)
	c.must(t, supplier7, func(e *Engine) error { return e.ShipReplacement(id, 1, 1, 20) })
	c.invariant(t, id)
	c.grn(t, id, 2, 20, "warehouse:GR-2")
	c.qc(t, id, 2, 20, 0, "erp:QC-2")
	c.invariant(t, id)
	c.approveAndRelease(t, id, 2, []Caller{plantHead}, "bank:REL-2")
	c.invariant(t, id)
	if d := c.deal(t, id); d.TotalReleasedPaise != d.LienAmountPaise || d.TotalHeldPaise != 0 {
		t.Fatalf("lien not fully and exactly used: %+v", d)
	}
	// Tamper: shrink the lien in state; a release must now be refused.
	id2 := c.mkDeal(t, dealOpts{id: "DL-20261020-0002"})
	c.grn(t, id2, 1, 100, "warehouse:GR-9")
	c.qc(t, id2, 1, 100, 0, "erp:QC-9")
	c.must(t, plantHead, func(e *Engine) error { return e.ApproveRelease(id2, 1) })
	d2 := c.deal(t, id2)
	d2.LienAmountPaise = 9 * lakh
	b, _ := json.Marshal(d2)
	c.state["deal:"+id2] = b
	c.wantErr(t, ErrLienExceeded, procure, func(e *Engine) error { return e.ReleaseTranche(id2, 1, "bank:REL-9") })
}

func TestReleaseAmountComputedOnChain(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{})
	c.grn(t, id, 1, 100, "warehouse:GR-1")
	c.qc(t, id, 1, 70, 30, "erp:QC-1")
	c.must(t, plantHead, func(e *Engine) error { return e.ApproveRelease(id, 1) })
	// Corrupt the stored value: release recomputes qty x price and must refuse the mismatch.
	tr := c.tranche(t, id, 1)
	tr.AcceptedValuePaise = 99 * lakh
	b, _ := json.Marshal(tr)
	c.state["tranche:"+id+":1"] = b
	c.wantErr(t, ErrInternal, procure, func(e *Engine) error { return e.ReleaseTranche(id, 1, "bank:REL-1") })
	tr.AcceptedValuePaise = 7 * lakh
	b, _ = json.Marshal(tr)
	c.state["tranche:"+id+":1"] = b
	ev := c.must(t, procure, func(e *Engine) error { return e.ReleaseTranche(id, 1, "bank:REL-1") })
	if ev[0].Payload["amountPaise"].(int64) != 7*lakh || ev[0].Payload["supplierAccountRef"] != "ACC-SUP-007" {
		t.Fatalf("payload %v", ev[0].Payload)
	}
}

func TestDeemedAcceptanceAfter15Days(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{mse: true})
	c.grn(t, id, 1, 100, "warehouse:GR-1")
	delivered := c.now
	c.now = delivered.Add(14 * day)
	c.must(t, bank, func(e *Engine) error { return e.TickDeadlines(id) })
	if s := c.tranche(t, id, 1).Status; s != TrDelivered {
		t.Fatalf("day 14 must not deem-accept, got %s", s)
	}
	c.now = delivered.Add(16 * day) // tick runs late; acceptance date must still be day 15
	ev := c.must(t, bank, func(e *Engine) error { return e.TickDeadlines(id) })
	tr := c.tranche(t, id, 1)
	if tr.Status != TrDeemedAccepted || !tr.DeemedAcceptance || tr.AcceptedValuePaise != 10*lakh {
		t.Fatalf("%+v", tr)
	}
	if !tr.AcceptedAt.Equal(delivered.Add(15*day)) || !tr.MseClockStart.Equal(delivered.Add(15*day)) || !tr.MseDeadline.Equal(delivered.Add(60*day)) {
		t.Fatalf("clock %v %v %v", tr.AcceptedAt, tr.MseClockStart, tr.MseDeadline)
	}
	if !reflect.DeepEqual(names(ev), []string{"DeemedAcceptance", "MseClockStarted"}) {
		t.Fatalf("events %v", names(ev))
	}
	c.must(t, bank, func(e *Engine) error { return e.TickDeadlines(id) }) // idempotent
	if c.deal(t, id).Status != DealAccepted {
		t.Fatalf("deal status %s", c.deal(t, id).Status)
	}
	c.wantErr(t, ErrObjectionWindowShut, procure, func(e *Engine) error { return e.RaiseObjection(id, 1, "late") })
}

func TestObjectionRestartsMseClock(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{mse: true})
	c.grn(t, id, 1, 100, "warehouse:GR-1")
	c.qc(t, id, 1, 100, 0, "erp:QC-1")
	t0 := c.now
	c.now = t0.Add(5 * day)
	c.must(t, procure, func(e *Engine) error { return e.RaiseObjection(id, 1, "paperwork mismatch") })
	c.wantErr(t, ErrInvalidTransition, plantHead, func(e *Engine) error { return e.ApproveRelease(id, 1) }) // paused
	c.now = t0.Add(10 * day)
	c.must(t, procure, func(e *Engine) error { return e.ResolveObjection(id, 1) })
	c.wantErr(t, ErrForbiddenRole, auditor, func(e *Engine) error { return e.ResolveObjection(id, 1) })
	ev := c.must(t, supplier7, func(e *Engine) error { return e.ResolveObjection(id, 1) })
	tr := c.tranche(t, id, 1)
	if !tr.MseClockStart.Equal(t0.Add(10*day)) || !tr.MseDeadline.Equal(t0.Add(55*day)) {
		t.Fatalf("clock not restarted: %v %v", tr.MseClockStart, tr.MseDeadline)
	}
	if !reflect.DeepEqual(names(ev), []string{"ObjectionResolved", "MseClockRestarted"}) {
		t.Fatalf("events %v", names(ev))
	}
	c.must(t, plantHead, func(e *Engine) error { return e.ApproveRelease(id, 1) }) // unblocked
}

func TestApprovalPolicyAboveFiftyLakhNeedsCFO(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{qty: 600}) // 60 lakh
	c.grn(t, id, 1, 600, "warehouse:GR-1")
	c.qc(t, id, 1, 600, 0, "erp:QC-1")
	c.wantErr(t, ErrForbiddenRole, procure, func(e *Engine) error { return e.ApproveRelease(id, 1) })
	ev := c.must(t, plantHead, func(e *Engine) error { return e.ApproveRelease(id, 1) })
	if ev[0].Payload["satisfied"].(bool) {
		t.Fatal("plant head alone must not satisfy a >=50L band")
	}
	c.wantErr(t, ErrPolicyNotSatisfied, procure, func(e *Engine) error { return e.ReleaseTranche(id, 1, "bank:REL-1") })
	c.wantErr(t, ErrDuplicate, plantHead, func(e *Engine) error { return e.ApproveRelease(id, 1) })
	ev = c.must(t, cfo, func(e *Engine) error { return e.ApproveRelease(id, 1) })
	if !ev[0].Payload["satisfied"].(bool) || c.tranche(t, id, 1).Status != TrApproved {
		t.Fatal("PLANT_HEAD + CFO must satisfy")
	}
	c.must(t, procure, func(e *Engine) error { return e.ReleaseTranche(id, 1, "bank:REL-1") })
}

func TestIdempotentDuplicateRejected(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{})
	c.grn(t, id, 1, 40, "warehouse:GR-1")
	c.wantErr(t, ErrDuplicate, warehouse, func(e *Engine) error {
		return e.PostGoodsReceipt(id, 2, `[{"lineNo":1,"deliveredQty":40}]`, "warehouse:GR-1")
	})
	if _, ok := c.state["tranche:"+id+":2"]; ok {
		t.Fatal("duplicate must not create a tranche")
	}
	c.wantErr(t, ErrBadRequest, warehouse, func(e *Engine) error {
		return e.PostGoodsReceipt(id, 2, `[{"lineNo":1,"deliveredQty":1}]`, "no-colon-key")
	})
}

func TestOutOfOrderQCBeforeReceiptRejected(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{})
	c.wantErr(t, ErrInvalidTransition, procure, func(e *Engine) error {
		return e.RecordQualityCheck(id, 1, `[{"lineNo":1,"acceptedQty":100,"rejectedQty":0}]`, "erp:QC-1")
	})
	c.grn(t, id, 1, 100, "warehouse:GR-1")
	c.qc(t, id, 1, 100, 0, "erp:QC-1") // same key as the failed attempt: it was NOT burned
	// GRN before lien is also an ordering error, not a hard failure
	d2 := c.mkDeal(t, dealOpts{id: "DL-20261020-0002"})
	_ = d2
	c2 := newChain()
	lines, _ := json.Marshal([]Line{{LineNo: 1, SKU: "S", OrderedQty: 1, UnitPricePaise: 100}})
	dj, _ := json.Marshal(Deal{DealID: "DL-20261020-0003", PONumber: "P", BuyerID: "BUYER-001", SupplierID: "SUP-007", LienAmountPaise: 100,
		DeliveryDeadline: c2.now.Add(day), ApprovalPolicy: []PolicyBand{{0, []Role{RolePlantHead}}}})
	if _, err := c2.do(procure, map[string][]byte{"lines": lines}, func(e *Engine) error { return e.CreateDeal(string(dj)) }); err != nil {
		t.Fatal(err)
	}
	c2.wantErr(t, ErrInvalidTransition, warehouse, func(e *Engine) error {
		return e.PostGoodsReceipt("DL-20261020-0003", 1, `[{"lineNo":1,"deliveredQty":1}]`, "warehouse:GR-1")
	})
	c.wantErr(t, ErrInvalidTransition, warehouse, func(e *Engine) error { // skipping tranche numbers
		return e.PostGoodsReceipt(id, 5, `[{"lineNo":1,"deliveredQty":1}]`, "warehouse:GR-5")
	})
}

func TestMseClock45DayRequiresWrittenAgreementFlag(t *testing.T) {
	for _, tc := range []struct {
		name    string
		term    int
		written bool
		wantErr Code
	}{{"45 without agreement", 45, false, ErrBadRequest}, {"45 with agreement", 45, true, ""}, {"15 no agreement needed", 15, false, ""}, {"30 invalid", 30, true, ErrBadRequest}} {
		c := newChain()
		d := Deal{DealID: "DL-20261020-0001", PONumber: "P", BuyerID: "BUYER-001", SupplierID: "SUP-007", LienAmountPaise: 100,
			DeliveryDeadline: c.now.Add(day), ApprovalPolicy: []PolicyBand{{0, []Role{RolePlantHead}}},
			MSE: MSE{Category: MseSmall, TermDays: tc.term, UdyamNo: "UDYAM-1", Applies: true, WrittenAgreement: tc.written}}
		b, _ := json.Marshal(d)
		lines, _ := json.Marshal([]Line{{LineNo: 1, SKU: "S", OrderedQty: 1, UnitPricePaise: 100}})
		_, err := c.do(procure, map[string][]byte{"lines": lines}, func(e *Engine) error { return e.CreateDeal(string(b)) })
		if tc.wantErr == "" && err != nil || tc.wantErr != "" && CodeOf(err) != tc.wantErr {
			t.Fatalf("%s: got %v", tc.name, err)
		}
	}
	// MEDIUM / trader cannot have the guard switched on
	c := newChain()
	d := Deal{DealID: "DL-20261020-0001", PONumber: "P", BuyerID: "BUYER-001", SupplierID: "S", LienAmountPaise: 100, DeliveryDeadline: c.now.Add(day),
		ApprovalPolicy: []PolicyBand{{0, []Role{RolePlantHead}}}, MSE: MSE{Category: MseMedium, TermDays: 45, UdyamNo: "U", Applies: true, WrittenAgreement: true}}
	b, _ := json.Marshal(d)
	lines, _ := json.Marshal([]Line{{LineNo: 1, SKU: "S", OrderedQty: 1, UnitPricePaise: 100}})
	if _, err := c.do(procure, map[string][]byte{"lines": lines}, func(e *Engine) error { return e.CreateDeal(string(b)) }); CodeOf(err) != ErrMseNotApplicable {
		t.Fatalf("medium enterprise: %v", err)
	}
}

func TestNonMseDealSkipsGuard(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{mse: false})
	c.grn(t, id, 1, 100, "warehouse:GR-1")
	c.now = c.now.Add(40 * day)
	if ev := c.must(t, bank, func(e *Engine) error { return e.TickDeadlines(id) }); len(ev) != 0 {
		t.Fatalf("non-MSE deal must emit nothing, got %v", names(ev))
	}
	tr := c.tranche(t, id, 1)
	if tr.Status != TrDelivered || tr.MseClockStart != nil || tr.MseDeadline != nil {
		t.Fatalf("non-MSE deal must not be deemed/clocked: %+v", tr)
	}
	ev := c.qc(t, id, 1, 100, 0, "erp:QC-1")
	if len(ev) != 2 || ev[1].Name != "TrancheAccepted" {
		t.Fatalf("events %v", names(ev))
	}
	clk := c.must(t, auditor, func(e *Engine) error {
		x, err := e.GetMseClock(id, 1)
		if x.State != "NOT_APPLICABLE" {
			t.Fatalf("state %s", x.State)
		}
		return err
	})
	_ = clk
}

// ---- extra safety / security ----

func TestSupplierCannotActOnAnotherSuppliersDeal(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{})
	c.grn(t, id, 1, 100, "warehouse:GR-1")
	c.wantErr(t, ErrForbiddenRole, supplier9, func(e *Engine) error { return e.AcknowledgeDelivery(id, 1) })
	c.must(t, supplier7, func(e *Engine) error { return e.AcknowledgeDelivery(id, 1) })
	c.wantErr(t, ErrInvalidTransition, supplier7, func(e *Engine) error { return e.AcknowledgeDelivery(id, 1) })
}

func TestRoleAndOrgEnforcement(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{})
	c.wantErr(t, ErrForbiddenRole, procure, func(e *Engine) error {
		return e.PostGoodsReceipt(id, 1, `[{"lineNo":1,"deliveredQty":1}]`, "warehouse:X")
	}) // not WAREHOUSE
	c.wantErr(t, ErrForbiddenRole, supplier7, func(e *Engine) error {
		return e.PostGoodsReceipt(id, 1, `[{"lineNo":1,"deliveredQty":1}]`, "warehouse:X")
	})
	c.wantErr(t, ErrForbiddenRole, procure, func(e *Engine) error { return e.ConfirmLien(id, "L", 1) })
	c.wantErr(t, ErrForbiddenRole, auditor, func(e *Engine) error { return e.ApproveRelease(id, 1) })
	c.wantErr(t, ErrForbiddenRole, supplier7, func(e *Engine) error { return e.TickDeadlines(id) })
	c.wantErr(t, ErrForbiddenRole, bank, func(e *Engine) error { return e.ResolveDispute(id, 1, 1, ResolutionRefund) }) // bank officer is not ARBITER
	c.wantErr(t, ErrNotFound, bank, func(e *Engine) error { return e.TickDeadlines("DL-20261020-9999") })
}

func TestOverDeliveryAndQtyMismatchRejected(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{})
	c.wantErr(t, ErrQtyMismatch, warehouse, func(e *Engine) error {
		return e.PostGoodsReceipt(id, 1, `[{"lineNo":1,"deliveredQty":101}]`, "warehouse:G1")
	})
	c.grn(t, id, 1, 100, "warehouse:G2")
	c.wantErr(t, ErrQtyMismatch, warehouse, func(e *Engine) error {
		return e.PostGoodsReceipt(id, 2, `[{"lineNo":1,"deliveredQty":1}]`, "warehouse:G3")
	}) // none owed
	c.wantErr(t, ErrQtyMismatch, procure, func(e *Engine) error {
		return e.RecordQualityCheck(id, 1, `[{"lineNo":1,"acceptedQty":90,"rejectedQty":5}]`, "erp:Q1")
	})
	c.wantErr(t, ErrQtyMismatch, procure, func(e *Engine) error {
		return e.RecordQualityCheck(id, 1, `[{"lineNo":1,"acceptedQty":110,"rejectedQty":-10}]`, "erp:Q2")
	})
}

func TestToleranceAcceptsWholeLot(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{tolBps: 300}) // 3%
	c.grn(t, id, 1, 100, "warehouse:G1")
	c.qc(t, id, 1, 98, 2, "erp:Q1")
	tr := c.tranche(t, id, 1)
	if tr.AcceptedValuePaise != 10*lakh || tr.HeldValuePaise != 0 || c.deal(t, id).TotalHeldPaise != 0 {
		t.Fatalf("within tolerance the lot is accepted: %+v", tr)
	}
	id2 := c.mkDeal(t, dealOpts{id: "DL-20261020-0002", tolBps: 100}) // 1% < 2% rejected
	c.grn(t, id2, 1, 100, "warehouse:G2")
	c.qc(t, id2, 1, 98, 2, "erp:Q2")
	if c.tranche(t, id2, 1).HeldValuePaise != 2*10000*rupee {
		t.Fatal("beyond tolerance must hold the rejected part")
	}
}

// ---- held-portion exits ----

func TestReplacementFlowEndToEnd(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{mse: true})
	c.grn(t, id, 1, 100, "warehouse:G1")
	c.qc(t, id, 1, 80, 20, "erp:Q1")
	c.approveAndRelease(t, id, 1, []Caller{plantHead}, "bank:R1")
	c.must(t, bank, func(e *Engine) error { return e.ConfirmSettlement(id, 1, "UTR-1", "NEFT") })
	c.wantErr(t, ErrQtyMismatch, supplier7, func(e *Engine) error { return e.ShipReplacement(id, 1, 1, 19) })
	c.wantErr(t, ErrForbiddenRole, procure, func(e *Engine) error { return e.ShipReplacement(id, 1, 1, 20) })
	ev := c.must(t, supplier7, func(e *Engine) error { return e.ShipReplacement(id, 1, 1, 20) })
	if ev[0].Name != "ReplacementShipped" || ev[0].Payload["newTrancheNo"].(int64) != 2 {
		t.Fatalf("%v", ev)
	}
	if c.tranche(t, id, 2).Status != TrPending || c.held(t, id, 1, 1).State != HeldStateReplacing {
		t.Fatal("replacement not pending")
	}
	c.wantErr(t, ErrQtyMismatch, warehouse, func(e *Engine) error {
		return e.PostGoodsReceipt(id, 2, `[{"lineNo":1,"deliveredQty":19}]`, "warehouse:G2")
	})
	c.grn(t, id, 2, 20, "warehouse:G3")
	c.qc(t, id, 2, 20, 0, "erp:Q2")
	if c.held(t, id, 1, 1).State != HeldStateClosed || c.deal(t, id).TotalHeldPaise != 0 {
		t.Fatal("original held portion must close when the replacement is QC'd")
	}
	c.approveAndRelease(t, id, 2, []Caller{plantHead}, "bank:R2")
	c.must(t, bank, func(e *Engine) error { return e.ConfirmSettlement(id, 2, "UTR-2", "NEFT") })
	if c.deal(t, id).Status != DealSettled {
		t.Fatalf("deal %s want SETTLED", c.deal(t, id).Status)
	}
	if d := c.deal(t, id); d.TotalReleasedPaise != 10*lakh {
		t.Fatalf("released %d", d.TotalReleasedPaise)
	}
	c.invariant(t, id)
}

func TestCreditNoteFlow(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{})
	c.grn(t, id, 1, 100, "warehouse:G1")
	c.qc(t, id, 1, 80, 20, "erp:Q1")
	c.wantErr(t, ErrQtyMismatch, procure, func(e *Engine) error { return e.ProposeCreditNote(id, 1, 1, 1*lakh) })
	c.must(t, supplier7, func(e *Engine) error { return e.ProposeCreditNote(id, 1, 1, 2*lakh) })
	c.wantErr(t, ErrForbiddenRole, supplier7, func(e *Engine) error { return e.SignCreditNote(id, 1, 1) }) // proposer cannot self-sign
	c.must(t, procure, func(e *Engine) error { return e.SignCreditNote(id, 1, 1) })
	if c.held(t, id, 1, 1).State != HeldStateClosed || c.deal(t, id).TotalHeldPaise != 0 {
		t.Fatal("credit note must free the lien on that portion")
	}
	c.approveAndRelease(t, id, 1, []Caller{plantHead}, "bank:R1")
	c.must(t, bank, func(e *Engine) error { return e.ConfirmSettlement(id, 1, "UTR-1", "NEFT") })
	if c.deal(t, id).Status != DealSettled {
		t.Fatalf("status %s", c.deal(t, id).Status)
	}
}

func TestPriceAdjustmentFlow(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{mse: true})
	c.grn(t, id, 1, 100, "warehouse:G1")
	c.qc(t, id, 1, 80, 20, "erp:Q1")
	c.wantErr(t, ErrBadRequest, procure, func(e *Engine) error { return e.ProposePriceAdjustment(id, 1, 1, 10000*rupee) }) // not lower
	c.must(t, procure, func(e *Engine) error { return e.ProposePriceAdjustment(id, 1, 1, 9000*rupee) })                    // 90%
	ev := c.must(t, supplier7, func(e *Engine) error { return e.SignPriceAdjustment(id, 1, 1) })
	if !reflect.DeepEqual(names(ev), []string{"PriceAdjustmentSigned", "TrancheAccepted", "MseClockStarted"}) {
		t.Fatalf("%v", names(ev))
	}
	tr := c.tranche(t, id, 2)
	if tr.AcceptedValuePaise != 20*9000*rupee || tr.Status != TrAccepted {
		t.Fatalf("%+v", tr)
	}
	rel := c.approveAndRelease(t, id, 2, []Caller{plantHead}, "bank:R2")
	if rel[0].Payload["amountPaise"].(int64) != 18*lakh/10 {
		t.Fatalf("adjusted release %v", rel[0].Payload["amountPaise"])
	}
	c.invariant(t, id)
}

func TestDisputeResolutionAndTimeout(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{})
	c.grn(t, id, 1, 100, "warehouse:G1")
	c.qc(t, id, 1, 80, 20, "erp:Q1")
	c.must(t, supplier7, func(e *Engine) error { return e.RaiseDispute(id, 1, 1, "units were fine") })
	if c.deal(t, id).Status != DealDisputed {
		t.Fatal("deal should be DISPUTED")
	}
	c.wantErr(t, ErrInvalidTransition, bank, func(e *Engine) error { return e.ApplyDisputeTimeout(id, 1, 1) }) // too early
	ev := c.must(t, arbiter, func(e *Engine) error { return e.ResolveDispute(id, 1, 1, ResolutionRelease) })
	if ev[0].Name != "DisputeResolved" || ev[1].Name != "TrancheAccepted" {
		t.Fatalf("%v", names(ev))
	}
	if c.tranche(t, id, 2).AcceptedValuePaise != 2*lakh {
		t.Fatal("release-to-supplier should create a 2L releasable tranche")
	}
	// timeout path on a second deal
	id2 := c.mkDeal(t, dealOpts{id: "DL-20261020-0002"})
	c.grn(t, id2, 1, 100, "warehouse:G2")
	c.qc(t, id2, 1, 80, 20, "erp:Q2")
	c.must(t, procure, func(e *Engine) error { return e.RaiseDispute(id2, 1, 1, "defects") })
	c.now = c.now.Add(31 * day)
	c.must(t, bank, func(e *Engine) error { return e.ApplyDisputeTimeout(id2, 1, 1) })
	if c.held(t, id2, 1, 1).State != HeldStateClosed || c.deal(t, id2).TotalHeldPaise != 0 {
		t.Fatal("timeout default must close the portion")
	}
	c.wantErr(t, ErrInvalidTransition, bank, func(e *Engine) error { return e.ApplyDisputeTimeout(id2, 1, 1) })
	c.invariant(t, id)
	c.invariant(t, id2)
}

func TestExpireDeal(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{deadline: newChain().now.Add(10 * day)})
	c.wantErr(t, ErrInvalidTransition, bank, func(e *Engine) error { return e.ExpireDeal(id) })
	c.now = c.now.Add(11 * day)
	ev := c.must(t, bank, func(e *Engine) error { return e.TickDeadlines(id) })
	if len(ev) != 1 || ev[0].Name != "DealExpired" || c.deal(t, id).Status != DealExpired {
		t.Fatalf("%v", names(ev))
	}
	c.wantErr(t, ErrInvalidTransition, warehouse, func(e *Engine) error {
		return e.PostGoodsReceipt(id, 1, `[{"lineNo":1,"deliveredQty":1}]`, "warehouse:G")
	})
}

func TestMseDeadlineApproachingFiresOnce(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{mse: true, termDays: 15})
	c.grn(t, id, 1, 100, "warehouse:G1")
	c.qc(t, id, 1, 100, 0, "erp:Q1")
	t0 := c.now
	c.now = t0.Add(9 * day)
	if ev := c.must(t, bank, func(e *Engine) error { return e.TickDeadlines(id) }); len(ev) != 0 {
		t.Fatalf("too early: %v", names(ev))
	}
	c.now = t0.Add(10 * day)
	ev := c.must(t, bank, func(e *Engine) error { return e.TickDeadlines(id) })
	if len(ev) != 1 || ev[0].Name != "MseDeadlineApproaching" || ev[0].Payload["daysLeft"].(int64) != 5 {
		t.Fatalf("%+v", ev)
	}
	if ev := c.must(t, bank, func(e *Engine) error { return e.TickDeadlines(id) }); len(ev) != 0 {
		t.Fatal("must alert only once")
	}
	near := c.must(t, auditor, func(e *Engine) error {
		x, err := e.QueryMseDealsNearDeadline(5)
		if len(x) != 1 {
			t.Fatalf("near-deadline %d", len(x))
		}
		clk, _ := e.GetMseClock(id, 1)
		if clk.State != "RUNNING" || clk.DaysLeft != 5 {
			t.Fatalf("%+v", clk)
		}
		return err
	})
	_ = near
	c.now = t0.Add(16 * day)
	c.must(t, auditor, func(e *Engine) error {
		clk, _ := e.GetMseClock(id, 1)
		if clk.State != "OVERDUE" || clk.DaysLeft >= 0 {
			t.Fatalf("%+v", clk)
		}
		return nil
	})
}

func TestQueryByStatusIndexStaysInSync(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{})
	count := func(s string) int {
		n := 0
		c.must(t, auditor, func(e *Engine) error { x, err := e.QueryDealsByStatus(s); n = len(x); return err })
		return n
	}
	if count("FUNDED") != 1 || count("CREATED") != 0 {
		t.Fatal("index after funding")
	}
	c.grn(t, id, 1, 100, "warehouse:G1")
	if count("FUNDED") != 0 || count("DELIVERED") != 1 {
		t.Fatal("index after delivery")
	}
	c.wantErr(t, ErrBadRequest, auditor, func(e *Engine) error { _, err := e.QueryDealsByStatus("BOGUS"); return err })
}

func TestPrivateLinesNeverOnPublicState(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{})
	for k, v := range c.state {
		if len(k) > 5 && k[:5] == "line:" {
			t.Fatalf("line leaked to public state: %s", k)
		}
		if jsonContains(v, "unitPricePaise") {
			t.Fatalf("unit price leaked in %s", k)
		}
	}
	_ = id
	if len(c.priv) != 1 {
		t.Fatalf("expected 1 private line, got %d", len(c.priv))
	}
}

func jsonContains(b []byte, s string) bool {
	return len(b) > 0 && string(b) != "" && contains(string(b), s)
}
func contains(a, b string) bool {
	for i := 0; i+len(b) <= len(a); i++ {
		if a[i:i+len(b)] == b {
			return true
		}
	}
	return false
}

// ---- burst / chaos at contract level (mirrors `make test-burst` expectation) ----

func TestBurst500DuplicateAndShuffledEventsZeroDoubleRelease(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{mse: true})
	type ev struct {
		kind string
		key  string
	}
	var queue []ev
	for i := 0; i < 250; i++ {
		queue = append(queue, ev{"grn", "warehouse:GR-1"}, ev{"qc", "erp:QC-1"})
	}
	rand.New(rand.NewSource(42)).Shuffle(len(queue), func(i, j int) { queue[i], queue[j] = queue[j], queue[i] })
	ok := map[string]int{}
	// worker semantics: ERR_INVALID_TRANSITION => requeue, ERR_DUPLICATE => done
	for rounds := 0; rounds < 600 && len(queue) > 0; rounds++ {
		e0 := queue[0]
		queue = queue[1:]
		var err error
		switch e0.kind {
		case "grn":
			_, err = c.do(warehouse, nil, func(e *Engine) error { return e.PostGoodsReceipt(id, 1, `[{"lineNo":1,"deliveredQty":100}]`, e0.key) })
		case "qc":
			_, err = c.do(procure, nil, func(e *Engine) error {
				return e.RecordQualityCheck(id, 1, `[{"lineNo":1,"acceptedQty":80,"rejectedQty":20}]`, e0.key)
			})
		}
		switch CodeOf(err) {
		case ErrInternal:
			if err == nil {
				ok[e0.kind]++
			} else {
				t.Fatalf("unexpected: %v", err)
			}
		case ErrInvalidTransition:
			queue = append(queue, e0)
		case ErrDuplicate:
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok["grn"] != 1 || ok["qc"] != 1 {
		t.Fatalf("each event must apply exactly once, got %v", ok)
	}
	// 500 release attempts, mixed keys
	c.must(t, plantHead, func(e *Engine) error { return e.ApproveRelease(id, 1) })
	released := 0
	for i := 0; i < 500; i++ {
		key := fmt.Sprintf("bank:REL-%d", i%7)
		if _, err := c.do(procure, nil, func(e *Engine) error { return e.ReleaseTranche(id, 1, key) }); err == nil {
			released++
		}
	}
	if released != 1 || c.deal(t, id).TotalReleasedPaise != 8*lakh {
		t.Fatalf("released %d times, total %d: want exactly once / 8L", released, c.deal(t, id).TotalReleasedPaise)
	}
	c.invariant(t, id)
}
