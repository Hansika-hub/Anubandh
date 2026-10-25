package fabric

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
	"github.com/Hansika-hub/anubandh/gateway/internal/clock"
	"github.com/Hansika-hub/anubandh/gateway/internal/model"
)

// The mock mirrors the chaincode safety invariants of BUILD.md §5.5 (same test names), so the
// demo fallback cannot behave differently from the contract the real chaincode is tested against.

var (
	idWarehouse = Identity{UserID: "buyer-warehouse", Org: model.OrgBuyer, Role: model.RoleWarehouse, PartyID: "BUYER-001"}
	idPlantHead = Identity{UserID: "buyer-planthead", Org: model.OrgBuyer, Role: model.RolePlantHead, PartyID: "BUYER-001"}
	idCFO       = Identity{UserID: "buyer-cfo", Org: model.OrgBuyer, Role: model.RoleCFO, PartyID: "BUYER-001"}
	idProc      = Identity{UserID: "buyer-procurement", Org: model.OrgBuyer, Role: model.RoleProcurement, PartyID: "BUYER-001"}
	idSup7      = Identity{UserID: "supplier-007", Org: model.OrgSupplier, Role: model.RoleSupplierUser, PartyID: "SUP-007"}
	idSup12     = Identity{UserID: "supplier-012", Org: model.OrgSupplier, Role: model.RoleSupplierUser, PartyID: "SUP-012"}
	idBank      = Identity{UserID: "bank-officer", Org: model.OrgBank, Role: model.RoleBankOfficer}
	idArbiter   = Identity{UserID: "bank-arbiter", Org: model.OrgBank, Role: model.RoleArbiter}
)

type fixture struct {
	t   *testing.T
	m   *MockClient
	clk *clock.Offset
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clk := clock.NewOffset(nil)
	m, err := NewMockClient(MockOptions{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, m: m, clk: clk}
}

func (f *fixture) submit(id Identity, fn string, args ...string) (SubmitResult, error) {
	return f.m.SubmitOnce(context.Background(), id, SubmitRequest{Fn: fn, Args: args})
}

func (f *fixture) ok(id Identity, fn string, args ...string) SubmitResult {
	f.t.Helper()
	res, err := f.submit(id, fn, args...)
	if err != nil {
		f.t.Fatalf("%s(%v): %v", fn, args, err)
	}
	return res
}

func (f *fixture) fails(code apperr.Code, id Identity, fn string, args ...string) {
	f.t.Helper()
	_, err := f.submit(id, fn, args...)
	if !apperr.Is(err, code) {
		f.t.Fatalf("%s(%v): want %s, got %v", fn, args, code, err)
	}
}

func (f *fixture) eval(fn string, out any, args ...string) {
	f.t.Helper()
	b, err := f.m.Evaluate(context.Background(), idProc, fn, args...)
	if err != nil {
		f.t.Fatalf("%s: %v", fn, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) deal(id string) model.Deal {
	var d model.Deal
	f.eval("GetDeal", &d, id)
	return d
}

func (f *fixture) tranche(id string, n int) model.Tranche {
	var t model.Tranche
	f.eval("GetTranche", &t, id, fmt.Sprint(n))
	return t
}

type dealSpec struct {
	id, supplier, category string
	termDays               int
	applies                bool
	qty, price             int64
}

var demo = dealSpec{"DL-20261020-0001", "SUP-007", model.MseSmall, 45, true, 100, 1000000}    // ₹10L
var bigDeal = dealSpec{"DL-20261020-0002", "SUP-012", model.MseMicro, 15, true, 60, 10000000} // ₹60L

func (f *fixture) create(s dealSpec) {
	f.t.Helper()
	deadline := f.clk.Now().Add(60 * 24 * time.Hour).UTC().Format(time.RFC3339)
	deal := fmt.Sprintf(`{"dealId":%q,"poNumber":"PO-4411","buyerId":"BUYER-001","supplierId":%q,"deliveryDeadline":%q,
		"mse":{"category":%q,"termDays":%d,"udyamNo":"UDYAM-KL-00-0000007","applies":%v,"writtenAgreement":%v},
		"approvalPolicy":[{"minPaise":0,"requiredRoles":["PLANT_HEAD"]},{"minPaise":500000000,"requiredRoles":["PLANT_HEAD","CFO"]}]}`,
		s.id, s.supplier, deadline, s.category, s.termDays, s.applies, s.termDays == 45)
	lines := fmt.Sprintf(`[{"lineNo":1,"sku":"SKU-STEEL-12","orderedQty":%d,"unitPricePaise":%d}]`, s.qty, s.price)
	if _, err := f.m.SubmitOnce(context.Background(), idProc, SubmitRequest{Fn: "CreateDeal", Args: []string{deal}, Transient: map[string][]byte{"lines": []byte(lines)}}); err != nil {
		f.t.Fatalf("CreateDeal: %v", err)
	}
}

func (f *fixture) funded(s dealSpec) {
	f.create(s)
	f.ok(idBank, "ConfirmLien", s.id, "LIEN-9921", fmt.Sprint(s.qty*s.price))
}

func (f *fixture) grn(id string, n int, qty int64, key string) {
	f.t.Helper()
	f.ok(idWarehouse, "PostGoodsReceipt", id, fmt.Sprint(n), fmt.Sprintf(`[{"lineNo":1,"deliveredQty":%d}]`, qty), key)
}

func (f *fixture) qc(id string, n int, acc, rej int64, key string) {
	f.t.Helper()
	f.ok(idWarehouse, "RecordQualityCheck", id, fmt.Sprint(n), fmt.Sprintf(`[{"lineNo":1,"acceptedQty":%d,"rejectedQty":%d}]`, acc, rej), key)
}

func TestPartialAcceptance80of100(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	f.grn(demo.id, 1, 100, "warehouse:GR-1")
	f.qc(demo.id, 1, 80, 20, "warehouse:QC-1")
	tr := f.tranche(demo.id, 1)
	if tr.Status != model.TrancheAccepted || tr.AcceptedValuePaise != 80_000_000 || tr.HeldValuePaise != 20_000_000 {
		t.Fatalf("tranche: %+v", tr)
	}
	d := f.deal(demo.id)
	if d.Status != model.DealPartiallyAccepted || d.TotalHeldPaise != 20_000_000 {
		t.Fatalf("deal: status %s held %d", d.Status, d.TotalHeldPaise)
	}
	var held []model.HeldPortion
	f.eval("GetHeldPortions", &held, demo.id)
	if len(held) != 1 || held[0].Qty != 20 || held[0].ValuePaise != 20_000_000 || held[0].State != model.TrancheHeld {
		t.Fatalf("held: %+v", held)
	}
	f.ok(idPlantHead, "ApproveRelease", demo.id, "1")
	f.ok(idProc, "ReleaseTranche", demo.id, "1", "gateway:release-1")
	if d := f.deal(demo.id); d.TotalReleasedPaise != 80_000_000 {
		t.Fatalf("exactly 80%% must be released, got %d", d.TotalReleasedPaise)
	}
}

func TestNoDoubleReleaseSameTranche(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	f.grn(demo.id, 1, 100, "warehouse:GR-1")
	f.qc(demo.id, 1, 100, 0, "warehouse:QC-1")
	f.ok(idPlantHead, "ApproveRelease", demo.id, "1")
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("web:release-%d", i%3) // mixture of same and different idem keys
			if _, err := f.submit(idProc, "ReleaseTranche", demo.id, "1", key); err == nil {
				mu.Lock()
				okCount++
				mu.Unlock()
			} else if !apperr.Is(err, apperr.ErrAlreadyReleased) && !apperr.Is(err, apperr.ErrDuplicate) {
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if okCount != 1 {
		t.Fatalf("want exactly 1 successful release, got %d", okCount)
	}
	if d := f.deal(demo.id); d.TotalReleasedPaise != 100_000_000 {
		t.Fatalf("released %d", d.TotalReleasedPaise)
	}
}

func TestReleaseAmountComputedOnChain(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	f.grn(demo.id, 1, 30, "warehouse:GR-1")
	f.qc(demo.id, 1, 25, 5, "warehouse:QC-1")
	f.ok(idPlantHead, "ApproveRelease", demo.id, "1")
	before := len(f.m.st.Events)
	f.ok(idProc, "ReleaseTranche", demo.id, "1", "gateway:r")
	ev := f.m.st.Events[before]
	var p struct {
		AmountPaise        int64  `json:"amountPaise"`
		SupplierAccountRef string `json:"supplierAccountRef"`
	}
	_ = json.Unmarshal(ev.Payload, &p)
	if ev.EventName != model.EvTrancheReleased || p.AmountPaise != 25*1000000 || p.SupplierAccountRef == "" {
		t.Fatalf("release event %s %+v: amount must be acceptedQty x private unit price", ev.EventName, p)
	}
}

func TestReleasedPlusHeldNeverExceedsLien(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	check := func(stage string) {
		d := f.deal(demo.id)
		if d.TotalReleasedPaise+d.TotalHeldPaise > d.LienAmountPaise || d.TotalReleasedPaise < 0 || d.TotalHeldPaise < 0 {
			t.Fatalf("%s: released %d + held %d > lien %d", stage, d.TotalReleasedPaise, d.TotalHeldPaise, d.LienAmountPaise)
		}
	}
	f.grn(demo.id, 1, 100, "warehouse:GR-1")
	f.qc(demo.id, 1, 80, 20, "warehouse:QC-1")
	check("after QC")
	f.ok(idPlantHead, "ApproveRelease", demo.id, "1")
	f.ok(idProc, "ReleaseTranche", demo.id, "1", "gateway:r1")
	check("after release 1")
	f.ok(idSup7, "ShipReplacement", demo.id, "1", "1", "20")
	check("after replacement shipped")
	f.fails(apperr.ErrQtyMismatch, idWarehouse, "PostGoodsReceipt", demo.id, "2", `[{"lineNo":1,"deliveredQty":21}]`, "warehouse:GR-2x")
	// Like the chaincode, a replacement receipt must deliver exactly what was shipped.
	f.fails(apperr.ErrQtyMismatch, idWarehouse, "PostGoodsReceipt", demo.id, "2", `[{"lineNo":1,"deliveredQty":19}]`, "warehouse:GR-2y")
	f.grn(demo.id, 2, 20, "warehouse:GR-2")
	f.qc(demo.id, 2, 20, 0, "warehouse:QC-2")
	f.ok(idPlantHead, "ApproveRelease", demo.id, "2")
	f.ok(idProc, "ReleaseTranche", demo.id, "2", "gateway:r2")
	check("after release 2")
	d := f.deal(demo.id)
	if d.TotalReleasedPaise != d.LienAmountPaise || d.TotalHeldPaise != 0 {
		t.Fatalf("full PO should be released exactly once: %+v", d)
	}
	// No delivery beyond the PO can ever become releasable.
	f.fails(apperr.ErrInvalidTransition, idWarehouse, "PostGoodsReceipt", demo.id, "4", `[{"lineNo":1,"deliveredQty":1}]`, "warehouse:GR-4")
	f.fails(apperr.ErrQtyMismatch, idWarehouse, "PostGoodsReceipt", demo.id, "3", `[{"lineNo":1,"deliveredQty":1}]`, "warehouse:GR-3")
}

func TestApprovalPolicyAboveFiftyLakhNeedsCFO(t *testing.T) {
	f := newFixture(t)
	f.funded(bigDeal)
	f.grn(bigDeal.id, 1, 60, "warehouse:GR-1")
	f.qc(bigDeal.id, 1, 60, 0, "warehouse:QC-1")
	f.ok(idPlantHead, "ApproveRelease", bigDeal.id, "1")
	if tr := f.tranche(bigDeal.id, 1); tr.Status != model.TrancheAccepted {
		t.Fatalf("₹60L with only PLANT_HEAD must not be approved, got %s", tr.Status)
	}
	f.fails(apperr.ErrPolicyNotSatisfied, idProc, "ReleaseTranche", bigDeal.id, "1", "gateway:r")
	f.fails(apperr.ErrDuplicate, idPlantHead, "ApproveRelease", bigDeal.id, "1")
	f.fails(apperr.ErrForbiddenRole, idWarehouse, "ApproveRelease", bigDeal.id, "1")
	f.ok(idCFO, "ApproveRelease", bigDeal.id, "1")
	if tr := f.tranche(bigDeal.id, 1); tr.Status != model.TrancheApproved || len(tr.Approvals) != 2 {
		t.Fatalf("PLANT_HEAD + CFO must approve: %+v", tr)
	}
	f.ok(idProc, "ReleaseTranche", bigDeal.id, "1", "gateway:r")
}

func TestIdempotentDuplicateRejected(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	f.grn(demo.id, 1, 100, "warehouse:GR-88123")
	f.fails(apperr.ErrDuplicate, idWarehouse, "PostGoodsReceipt", demo.id, "1", `[{"lineNo":1,"deliveredQty":100}]`, "warehouse:GR-88123")
	f.fails(apperr.ErrDuplicate, idWarehouse, "PostGoodsReceipt", demo.id, "2", `[{"lineNo":1,"deliveredQty":1}]`, "warehouse:GR-88123")
	if tr := f.tranche(demo.id, 1); tr.Lines[0].DeliveredQty != 100 {
		t.Fatalf("duplicate must not change state: %+v", tr)
	}
	// The chaincode's key format: lower-case source, no dots, slashes or extra colons.
	for _, key := range []string{"", "ERP:QC-1", "warehouse", "ware.house:GR-1", "erp/x:GR-1", "warehouse:GR:1", "warehouse:GR 1"} {
		f.fails(apperr.ErrBadRequest, idWarehouse, "PostGoodsReceipt", demo.id, "2", `[{"lineNo":1,"deliveredQty":1}]`, key)
	}
}

// TestOutOfOrderReplacementReceipt: a replacement GRN that arrives before ShipReplacement finds
// nothing owed and fails with ERR_QTY_MISMATCH (as the chaincode does), and succeeds once shipped.
func TestOutOfOrderReplacementReceipt(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	f.grn(demo.id, 1, 100, "warehouse:GR-1")
	f.qc(demo.id, 1, 80, 20, "warehouse:QC-1")
	f.fails(apperr.ErrQtyMismatch, idWarehouse, "PostGoodsReceipt", demo.id, "2", `[{"lineNo":1,"deliveredQty":20}]`, "warehouse:GR-2")
	f.ok(idSup7, "ShipReplacement", demo.id, "1", "1", "20")
	f.grn(demo.id, 2, 20, "warehouse:GR-2") // the failed attempt did not consume the key
}

func TestOutOfOrderQCBeforeReceiptRejected(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	f.fails(apperr.ErrInvalidTransition, idWarehouse, "RecordQualityCheck", demo.id, "1", `[{"lineNo":1,"acceptedQty":100,"rejectedQty":0}]`, "warehouse:QC-1")
	// The failed QC must not burn its idem key: the requeued event succeeds after the receipt.
	f.grn(demo.id, 1, 100, "warehouse:GR-1")
	f.qc(demo.id, 1, 100, 0, "warehouse:QC-1")
	// Goods before the lien are rejected too.
	f.create(dealSpec{"DL-20261020-0003", "SUP-007", model.MseSmall, 45, true, 10, 100})
	f.fails(apperr.ErrInvalidTransition, idWarehouse, "PostGoodsReceipt", "DL-20261020-0003", "1", `[{"lineNo":1,"deliveredQty":1}]`, "warehouse:GR-9")
}

// logical returns the logical event names one committed chain event carries (TxEventBatch unwrapped).
func logical(t *testing.T, e mockEvent) []string {
	t.Helper()
	if e.EventName != model.EvTxEventBatch {
		return []string{e.EventName}
	}
	var b struct {
		Events []struct {
			Name string `json:"name"`
		} `json:"events"`
	}
	if err := json.Unmarshal(e.Payload, &b); err != nil {
		t.Fatalf("TxEventBatch payload: %v", err)
	}
	names := make([]string, len(b.Events))
	for i, x := range b.Events {
		names[i] = x.Name
	}
	return names
}

// The mock must encode events exactly like chaincode/deal/core/events.go: a lone event goes out as
// itself; several go out as one TxEventBatch, in emission order, each with a complete payload.
func TestEventsEncodedLikeChaincode(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	before := len(f.m.st.Events)
	f.grn(demo.id, 1, 100, "warehouse:GR-1")
	if got := f.m.st.Events[before].EventName; got != model.EvGoodsReceived {
		t.Fatalf("a lone event must not be wrapped, got %s", got)
	}
	before = len(f.m.st.Events)
	f.qc(demo.id, 1, 80, 20, "warehouse:QC-1")
	if len(f.m.st.Events) != before+1 {
		t.Fatalf("one transaction must commit one chain event, got %d", len(f.m.st.Events)-before)
	}
	ev := f.m.st.Events[before]
	want := []string{model.EvQualityRecorded, model.EvHeldPortionCreated, model.EvTrancheAccepted, model.EvMseClockStarted}
	if got := logical(t, ev); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("QC events: want %v, got %v", want, got)
	}
	var b struct {
		DealID string `json:"dealId"`
		TxID   string `json:"txId"`
		Ts     string `json:"ts"`
		Events []struct {
			Payload map[string]any `json:"payload"`
		} `json:"events"`
	}
	if err := json.Unmarshal(ev.Payload, &b); err != nil {
		t.Fatal(err)
	}
	if b.DealID != demo.id || b.TxID != ev.TxID || b.Ts == "" {
		t.Fatalf("batch envelope must carry dealId, txId and ts: %+v", b)
	}
	for i, e := range b.Events {
		if e.Payload["dealId"] != demo.id || e.Payload["txId"] != ev.TxID || e.Payload["ts"] == nil || e.Payload["trancheNo"] != float64(1) {
			t.Fatalf("events[%d] payload incomplete: %v", i, e.Payload)
		}
	}
	held := b.Events[1].Payload
	if held["lineNo"] != float64(1) || held["qty"] != float64(20) || held["valuePaise"] != float64(20*1000000) {
		t.Fatalf("HeldPortionCreated payload: %v", held)
	}
	if b.Events[2].Payload["deemed"] != false || b.Events[2].Payload["mseDeadline"] == nil {
		t.Fatalf("TrancheAccepted payload: %v", b.Events[2].Payload)
	}
}

func TestDeemedAcceptanceAfter15Days(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	f.grn(demo.id, 1, 100, "warehouse:GR-1")
	delivered := *f.tranche(demo.id, 1).DeliveredAt
	f.clk.Advance(14 * 24 * time.Hour)
	f.ok(idProc, "TickDeadlines", demo.id)
	if tr := f.tranche(demo.id, 1); tr.Status != model.TrancheDelivered {
		t.Fatalf("day 14: still DELIVERED, got %s", tr.Status)
	}
	f.clk.Advance(24 * time.Hour)
	// The client proposes the transaction timestamp, so only BANK and BUYER may drive the clock.
	f.fails(apperr.ErrForbiddenRole, idSup7, "TickDeadlines", demo.id)
	before := len(f.m.st.Events)
	f.ok(idProc, "TickDeadlines", demo.id)
	tr := f.tranche(demo.id, 1)
	if tr.Status != model.TrancheDeemedAccepted || !tr.DeemedAcceptance || tr.AcceptedValuePaise != 100_000_000 {
		t.Fatalf("day 15: deemed acceptance expected: %+v", tr)
	}
	// MSMED Act §2(b)(ii): deemed acceptance day = day of actual delivery; clock runs from there.
	if !tr.MseClockStart.Equal(delivered) || !tr.MseDeadline.Equal(delivered.Add(45*24*time.Hour)) {
		t.Fatalf("clock must start at delivery %v: start %v deadline %v", delivered, tr.MseClockStart, tr.MseDeadline)
	}
	if got := logical(t, f.m.st.Events[before]); fmt.Sprint(got) != fmt.Sprint([]string{model.EvDeemedAcceptance, model.EvMseClockStarted}) {
		t.Fatalf("deemed acceptance must announce DeemedAcceptance + MseClockStarted (BUILD.md §5.3), got %v", got)
	}
	// Idempotent: a second tick changes nothing and emits nothing new about this tranche.
	f.ok(idProc, "TickDeadlines", demo.id)
	if f.tranche(demo.id, 1).Status != model.TrancheDeemedAccepted {
		t.Fatal("tick must be idempotent")
	}
}

func TestObjectionRestartsMseClock(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	f.grn(demo.id, 1, 100, "warehouse:GR-1")
	f.qc(demo.id, 1, 100, 0, "warehouse:QC-1")
	f.clk.Advance(3 * 24 * time.Hour)
	f.ok(idProc, "RaiseObjection", demo.id, "1", "surface rust on 12 coils")
	f.fails(apperr.ErrInvalidTransition, idPlantHead, "ApproveRelease", demo.id, "1")
	f.clk.Advance(4 * 24 * time.Hour)
	f.ok(idProc, "ResolveObjection", demo.id, "1")
	if tr := f.tranche(demo.id, 1); tr.MseClockStart.After(f.clk.Now().Add(-24 * time.Hour)) {
		t.Fatal("clock must not restart on the first co-signature")
	}
	f.fails(apperr.ErrDuplicate, idProc, "ResolveObjection", demo.id, "1")
	before := len(f.m.st.Events)
	f.ok(idSup7, "ResolveObjection", demo.id, "1")
	tr := f.tranche(demo.id, 1)
	now := f.clk.Now().Truncate(time.Second)
	if !tr.MseClockStart.Equal(now) || !tr.MseDeadline.Equal(now.Add(45*24*time.Hour)) {
		t.Fatalf("clock must restart at resolution %v: %v / %v", now, tr.MseClockStart, tr.MseDeadline)
	}
	if got := logical(t, f.m.st.Events[before]); fmt.Sprint(got) != fmt.Sprint([]string{model.EvObjectionResolved, model.EvMseClockRestarted}) {
		t.Fatalf("resolution must announce ObjectionResolved + MseClockRestarted, got %v", got)
	}
	f.ok(idPlantHead, "ApproveRelease", demo.id, "1")
}

func TestObjectionWindowCloses(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	f.grn(demo.id, 1, 100, "warehouse:GR-1")
	f.clk.Advance(15*24*time.Hour + time.Hour)
	f.fails(apperr.ErrObjectionWindowClosed, idProc, "RaiseObjection", demo.id, "1", "too late")
}

func TestMseClock45DayRequiresWrittenAgreementFlag(t *testing.T) {
	f := newFixture(t)
	// A 45-day term is only lawful with a written agreement; anything else is rejected outright.
	deal := fmt.Sprintf(`{"dealId":"DL-20261020-0005","poNumber":"PO-1","buyerId":"BUYER-001","supplierId":"SUP-007","deliveryDeadline":%q,
		"mse":{"category":"SMALL","termDays":60,"udyamNo":"UDYAM-KL-00-0000007","applies":true},"approvalPolicy":[{"minPaise":0,"requiredRoles":["PLANT_HEAD"]}]}`,
		f.clk.Now().Add(48*time.Hour).Format(time.RFC3339))
	_, err := f.m.SubmitOnce(context.Background(), idProc, SubmitRequest{Fn: "CreateDeal", Args: []string{deal}, Transient: map[string][]byte{"lines": []byte(`[{"lineNo":1,"sku":"S","orderedQty":1,"unitPricePaise":1}]`)}})
	if !apperr.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("termDays 60 must be rejected: %v", err)
	}
	for agreed, want := range map[bool]bool{false: false, true: true} {
		d := strings.Replace(strings.Replace(deal, `"termDays":60`, `"termDays":45`, 1), `"applies":true`, fmt.Sprintf(`"applies":true,"writtenAgreement":%v`, agreed), 1)
		_, err := f.m.SubmitOnce(context.Background(), idProc, SubmitRequest{Fn: "CreateDeal", Args: []string{d}, Transient: map[string][]byte{"lines": []byte(`[{"lineNo":1,"sku":"S","orderedQty":1,"unitPricePaise":1}]`)}})
		if (err == nil) != want || (err != nil && !apperr.Is(err, apperr.ErrBadRequest)) {
			t.Fatalf("termDays 45, writtenAgreement=%v: %v", agreed, err)
		}
	}
	// MEDIUM enterprises are outside MSMED Act §15.
	deal2 := fmt.Sprintf(`{"dealId":"DL-20261020-0006","poNumber":"PO-1","buyerId":"BUYER-001","supplierId":"SUP-007","deliveryDeadline":%q,
		"mse":{"category":"MEDIUM","termDays":45,"applies":true},"approvalPolicy":[{"minPaise":0,"requiredRoles":["PLANT_HEAD"]}]}`,
		f.clk.Now().Add(48*time.Hour).Format(time.RFC3339))
	_, err = f.m.SubmitOnce(context.Background(), idProc, SubmitRequest{Fn: "CreateDeal", Args: []string{deal2}, Transient: map[string][]byte{"lines": []byte(`[{"lineNo":1,"sku":"S","orderedQty":1,"unitPricePaise":1}]`)}})
	if !apperr.Is(err, apperr.ErrMseNotApplicable) {
		t.Fatalf("MEDIUM + applies must be ERR_MSE_NOT_APPLICABLE: %v", err)
	}
}

func TestNonMseDealSkipsGuard(t *testing.T) {
	f := newFixture(t)
	s := dealSpec{"DL-20261020-0007", "SUP-007", model.MseNone, 0, false, 10, 100}
	f.funded(s)
	f.grn(s.id, 1, 10, "warehouse:GR-1")
	f.qc(s.id, 1, 10, 0, "warehouse:QC-1")
	tr := f.tranche(s.id, 1)
	if tr.MseClockStart != nil || tr.MseDeadline != nil {
		t.Fatalf("non-MSE tranche must have no MSE clock: %+v", tr)
	}
	if _, err := f.m.Evaluate(context.Background(), idProc, "GetMseClock", s.id, "1"); !apperr.Is(err, apperr.ErrMseNotApplicable) {
		t.Fatalf("GetMseClock on non-MSE deal: %v", err)
	}
}

// TestNonMseDealNotDeemedAccepted: deemed acceptance is an MSMED Act rule, so (as in the chaincode)
// a non-MSE delivery with no QC stays DELIVERED after 15 days.
func TestNonMseDealNotDeemedAccepted(t *testing.T) {
	f := newFixture(t)
	s := dealSpec{"DL-20261020-0008", "SUP-007", model.MseNone, 0, false, 10, 100}
	f.funded(s)
	f.grn(s.id, 1, 10, "warehouse:GR-1")
	f.clk.Advance(16 * 24 * time.Hour)
	f.ok(idBank, "TickDeadlines", s.id)
	if tr := f.tranche(s.id, 1); tr.Status != model.TrancheDelivered {
		t.Fatalf("non-MSE tranche must not be deemed accepted, got %s", tr.Status)
	}
}

func TestLienMustEqualPOValue(t *testing.T) {
	f := newFixture(t)
	f.create(demo)
	f.fails(apperr.ErrLienExceeded, idBank, "ConfirmLien", demo.id, "LIEN-1", fmt.Sprint(demo.qty*demo.price+1))
	f.fails(apperr.ErrLienExceeded, idBank, "ConfirmLien", demo.id, "LIEN-1", fmt.Sprint(demo.qty*demo.price-1))
	f.ok(idBank, "ConfirmLien", demo.id, "LIEN-1", fmt.Sprint(demo.qty*demo.price))
}

func TestFailedTransactionWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	f.grn(demo.id, 1, 100, "warehouse:GR-1")
	height := f.m.Height()
	snapshot, _ := json.Marshal(f.m.st.Deals[demo.id])
	// accepted+rejected mismatch fails after partially walking the lines: nothing may stick.
	f.fails(apperr.ErrQtyMismatch, idWarehouse, "RecordQualityCheck", demo.id, "1", `[{"lineNo":1,"acceptedQty":70,"rejectedQty":20}]`, "warehouse:QC-bad")
	after, _ := json.Marshal(f.m.st.Deals[demo.id])
	if string(snapshot) != string(after) || f.m.Height() != height {
		t.Fatal("failed tx changed state or height")
	}
	f.qc(demo.id, 1, 80, 20, "warehouse:QC-bad") // the idem key of the failed tx is still usable
}

func TestOneEventPerTransaction(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	f.grn(demo.id, 1, 100, "warehouse:GR-1")
	before := len(f.m.st.Events)
	f.qc(demo.id, 1, 80, 20, "warehouse:QC-1") // changes tranche, held portions and MSE clock
	if got := len(f.m.st.Events) - before; got != 1 {
		t.Fatalf("Fabric carries one chaincode event per transaction; mock emitted %d", got)
	}
	f.fails(apperr.ErrForbiddenRole, idSup12, "AcknowledgeDelivery", demo.id, "1")
	f.ok(idSup7, "AcknowledgeDelivery", demo.id, "1")
}

func TestEventsReplayFromBlockAndFollow(t *testing.T) {
	f := newFixture(t)
	f.funded(demo) // 2 blocks
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := f.m.ChaincodeEvents(ctx, idProc, 2)
	first := <-ch
	if first.BlockNumber != 2 || first.EventName != model.EvLienConfirmed {
		t.Fatalf("replay from block 2: got %d %s", first.BlockNumber, first.EventName)
	}
	f.grn(demo.id, 1, 1, "warehouse:GR-1")
	select {
	case ev := <-ch:
		if ev.EventName != model.EvGoodsReceived || ev.BlockNumber != 3 {
			t.Fatalf("follow: %d %s", ev.BlockNumber, ev.EventName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live event not delivered")
	}
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel must close when ctx ends")
	}
}

func TestMockStatePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	clk := clock.NewOffset(nil)
	m, err := NewMockClient(MockOptions{Clock: clk, StatePath: path})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, m: m, clk: clk}
	f.funded(demo)
	if _, err := m.Advance(48 * time.Hour); err != nil {
		t.Fatal(err)
	}
	clk2 := clock.NewOffset(nil)
	m2, err := NewMockClient(MockOptions{Clock: clk2, StatePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if m2.Height() != 2 || m2.st.Deals[demo.id].Status != model.DealFunded || clk2.Offset() != 48*time.Hour {
		t.Fatalf("restored: height %d status %s offset %v", m2.Height(), m2.st.Deals[demo.id].Status, clk2.Offset())
	}
}

func TestHeldPortionExits(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	f.grn(demo.id, 1, 50, "warehouse:GR-1")
	f.qc(demo.id, 1, 35, 15, "warehouse:QC-1")
	// Credit note must be co-signed by the counter-party and cover the full held value.
	f.fails(apperr.ErrBadRequest, idProc, "ProposeCreditNote", demo.id, "1", "1", "1")
	f.ok(idProc, "ProposeCreditNote", demo.id, "1", "1", "15000000")
	f.fails(apperr.ErrForbiddenRole, idProc, "SignCreditNote", demo.id, "1", "1")
	f.ok(idSup7, "SignCreditNote", demo.id, "1", "1")
	d := f.deal(demo.id)
	if d.TotalHeldPaise != 0 {
		t.Fatalf("credit note releases the held lien: %d", d.TotalHeldPaise)
	}
	f.fails(apperr.ErrInvalidTransition, idSup7, "ShipReplacement", demo.id, "1", "1", "15")

	// Price adjustment on a second delivery: accepted at a lower price, becomes releasable.
	f.grn(demo.id, 2, 30, "warehouse:GR-2")
	f.qc(demo.id, 2, 20, 10, "warehouse:QC-2")
	f.fails(apperr.ErrBadRequest, idSup7, "ProposePriceAdjustment", demo.id, "2", "1", "1000000")
	f.ok(idSup7, "ProposePriceAdjustment", demo.id, "2", "1", "600000")
	f.ok(idProc, "SignPriceAdjustment", demo.id, "2", "1")
	tr := f.tranche(demo.id, 3)
	if tr.Status != model.TrancheAccepted || tr.AcceptedValuePaise != 10*600000 {
		t.Fatalf("price-adjusted tranche: %+v", tr)
	}
	f.ok(idPlantHead, "ApproveRelease", demo.id, "3")
	f.ok(idProc, "ReleaseTranche", demo.id, "3", "gateway:r3")

	// Dispute: only an ARBITER resolves it.
	f.funded(bigDeal)
	f.grn(bigDeal.id, 1, 60, "warehouse:B-GR-1")
	f.qc(bigDeal.id, 1, 50, 10, "warehouse:B-QC-1")
	f.ok(idSup12, "RaiseDispute", bigDeal.id, "1", "1", "QC sample size too small")
	if f.deal(bigDeal.id).Status != model.DealDisputed {
		t.Fatal("deal must be DISPUTED")
	}
	f.fails(apperr.ErrForbiddenRole, idBank, "ResolveDispute", bigDeal.id, "1", "1", model.ResolutionReleaseToSupplier)
	f.ok(idArbiter, "ResolveDispute", bigDeal.id, "1", "1", model.ResolutionReleaseToSupplier)
	if tr := f.tranche(bigDeal.id, 2); tr.Status != model.TrancheAccepted || tr.AcceptedValuePaise != 10*10000000 {
		t.Fatalf("dispute released to supplier must create an accepted tranche: %+v", tr)
	}
}

func TestExpireDealPastDeadline(t *testing.T) {
	f := newFixture(t)
	f.funded(demo)
	f.ok(idProc, "TickDeadlines", demo.id)
	if f.deal(demo.id).Status != model.DealFunded {
		t.Fatal("not yet expired")
	}
	f.clk.Advance(61 * 24 * time.Hour)
	f.ok(idProc, "TickDeadlines", demo.id)
	if f.deal(demo.id).Status != model.DealExpired {
		t.Fatal("funded deal past its delivery deadline must expire")
	}
	f.fails(apperr.ErrInvalidTransition, idWarehouse, "PostGoodsReceipt", demo.id, "1", `[{"lineNo":1,"deliveredQty":1}]`, "warehouse:late")
}

func TestMseDeadlineApproachingEmittedOnce(t *testing.T) {
	f := newFixture(t)
	f.funded(bigDeal) // MICRO, 15-day term
	f.grn(bigDeal.id, 1, 60, "warehouse:GR-1")
	f.qc(bigDeal.id, 1, 60, 0, "warehouse:QC-1")
	f.clk.Advance(10*24*time.Hour + time.Hour) // < 5 days left
	count := func() int {
		n := 0
		for _, e := range f.m.st.Events {
			for _, name := range logical(t, e) {
				if name == model.EvMseDeadlineApproaching {
					n++
				}
			}
		}
		return n
	}
	f.ok(idProc, "TickDeadlines", bigDeal.id)
	f.ok(idProc, "TickDeadlines", bigDeal.id)
	if count() != 1 {
		t.Fatalf("MseDeadlineApproaching must be emitted once per tranche, got %d", count())
	}
	var near []model.Tranche
	f.eval("QueryMseDealsNearDeadline", &near, "5")
	if len(near) != 1 || near[0].DealID != bigDeal.id {
		t.Fatalf("near-deadline query: %+v", near)
	}
}
