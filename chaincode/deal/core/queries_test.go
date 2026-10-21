package core

import (
	"strings"
	"testing"
)

func TestVerifyInvoiceAndOptionalReleaseGate(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{})
	c.grn(t, id, 1, 100, "warehouse:G1")
	c.qc(t, id, 1, 100, 0, "erp:Q1")
	c.must(t, plantHead, func(e *Engine) error { return e.ApproveRelease(id, 1) })

	RequireVerifiedInvoice = true
	defer func() { RequireVerifiedInvoice = false }()
	c.wantErr(t, ErrInvalidTransition, procure, func(e *Engine) error { return e.ReleaseTranche(id, 1, "bank:R1") })
	c.wantErr(t, ErrForbiddenRole, supplier7, func(e *Engine) error { return e.VerifyInvoice(id, "IRN-1", "gst:INV-1") })
	c.must(t, procure, func(e *Engine) error { return e.VerifyInvoice(id, "IRN-1", "gst:INV-1") })
	c.wantErr(t, ErrDuplicate, procure, func(e *Engine) error { return e.VerifyInvoice(id, "IRN-1", "gst:INV-1") })
	if d := c.deal(t, id); !d.Invoice.Verified || d.Invoice.IRN == nil || *d.Invoice.IRN != "IRN-1" {
		t.Fatalf("%+v", d.Invoice)
	}
	c.must(t, procure, func(e *Engine) error { return e.ReleaseTranche(id, 1, "bank:R1") })
}

func TestReadQueriesAndHistory(t *testing.T) {
	c := newChain()
	id := c.mkDeal(t, dealOpts{})
	c.grn(t, id, 1, 100, "warehouse:G1")
	c.qc(t, id, 1, 80, 20, "erp:Q1")
	c.must(t, auditor, func(e *Engine) error {
		d, err := e.GetDeal(id)
		if err != nil || d.DealID != id {
			t.Fatal(d, err)
		}
		if tr, err := e.GetTranche(id, 1); err != nil || tr.AcceptedValuePaise != 8*lakh {
			t.Fatal(tr, err)
		}
		if trs, err := e.ListTranches(id); err != nil || len(trs) != 1 {
			t.Fatal(trs, err)
		}
		if h, err := e.GetHeldPortions(id); err != nil || len(h) != 1 || h[0].ValuePaise != 2*lakh {
			t.Fatal(h, err)
		}
		hist, err := e.GetDealHistory(id)
		if err != nil || len(hist) < 3 { // created, funded, delivered, accepted...
			t.Fatalf("history %d %v", len(hist), err)
		}
		if _, err := e.GetDeal("DL-20261020-9999"); CodeOf(err) != ErrNotFound {
			t.Fatal(err)
		}
		if _, err := e.ListTranches("DL-20261020-9999"); CodeOf(err) != ErrNotFound {
			t.Fatal(err)
		}
		return nil
	})
}

func TestErrorsStartWithFrozenCodeForGatewayMapping(t *testing.T) {
	err := fail(ErrDuplicate, "key %s", "x")
	if !strings.HasPrefix(err.Error(), "ERR_DUPLICATE: ") || CodeOf(err) != ErrDuplicate {
		t.Fatal(err)
	}
}
