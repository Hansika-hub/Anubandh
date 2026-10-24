package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// chain emulates Fabric semantics that matter here: a transaction reads ONLY committed state
// (never its own staged writes), writes are applied atomically on success, a failed tx
// changes nothing. If the engine relied on read-your-writes these tests would fail.
type chain struct {
	state map[string][]byte
	priv  map[string][]byte
	ep    map[string][]Org
	hist  map[string][]HistoryEntry
	now   time.Time
	n     int
}

func newChain() *chain {
	return &chain{state: map[string][]byte{}, priv: map[string][]byte{}, ep: map[string][]Org{}, hist: map[string][]HistoryEntry{},
		now: time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)}
}

type tx struct {
	c         *chain
	caller    Caller
	transient map[string][]byte
	puts      map[string][]byte
	dels      map[string]bool
	pputs     map[string][]byte
	id        string
}

func (t *tx) Get(k string) ([]byte, error) { return t.c.state[k], nil }
func (t *tx) Put(k string, v []byte) error { t.puts[k] = v; delete(t.dels, k); return nil }
func (t *tx) Del(k string) error           { t.dels[k] = true; delete(t.puts, k); return nil }
func (t *tx) Range(p string) ([]KV, error) {
	var out []KV
	for k, v := range t.c.state {
		if strings.HasPrefix(k, p) {
			out = append(out, KV{k, v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
func (t *tx) GetPrivate(coll, k string) ([]byte, error) { return t.c.priv[coll+"|"+k], nil }
func (t *tx) PutPrivate(coll, k string, v []byte) error { t.pputs[coll+"|"+k] = v; return nil }
func (t *tx) Transient(k string) ([]byte, bool)         { v, ok := t.transient[k]; return v, ok }
func (t *tx) Now() time.Time                            { return t.c.now }
func (t *tx) TxID() string                              { return t.id }
func (t *tx) Caller() (Caller, error)                   { return t.caller, nil }
func (t *tx) SetKeyEndorsement(k string, o []Org) error { t.c.ep[k] = o; return nil }
func (t *tx) History(k string) ([]HistoryEntry, error)  { return t.c.hist[k], nil }

// do runs one transaction; commits only on success.
func (c *chain) do(caller Caller, transient map[string][]byte, f func(e *Engine) error) ([]Event, error) {
	c.n++
	t := &tx{c: c, caller: caller, transient: transient, puts: map[string][]byte{}, dels: map[string]bool{}, pputs: map[string][]byte{}, id: fmt.Sprintf("tx%04d", c.n)}
	e := NewEngine(t)
	if err := f(e); err != nil {
		return nil, err
	}
	for k, v := range t.puts {
		c.state[k] = v
		c.hist[k] = append(c.hist[k], HistoryEntry{TxID: t.id, Timestamp: c.now, Value: v})
	}
	for k := range t.dels {
		delete(c.state, k)
	}
	for k, v := range t.pputs {
		c.priv[k] = v
	}
	return e.Events(), nil
}

var (
	warehouse = Caller{Org: OrgBuyer, Role: RoleWarehouse, UserID: "wh1", BuyerID: "BUYER-001"}
	plantHead = Caller{Org: OrgBuyer, Role: RolePlantHead, UserID: "ph1", BuyerID: "BUYER-001"}
	cfo       = Caller{Org: OrgBuyer, Role: RoleCFO, UserID: "cfo1", BuyerID: "BUYER-001"}
	procure   = Caller{Org: OrgBuyer, Role: RoleProcurement, UserID: "pr1", BuyerID: "BUYER-001"}
	supplier7 = Caller{Org: OrgSupplier, Role: RoleSupplierUser, UserID: "s7", SupplierID: "SUP-007"}
	supplier9 = Caller{Org: OrgSupplier, Role: RoleSupplierUser, UserID: "s9", SupplierID: "SUP-009"}
	bank      = Caller{Org: OrgBank, Role: RoleBankOfficer, UserID: "b1"}
	arbiter   = Caller{Org: OrgBank, Role: RoleArbiter, UserID: "arb"}
	auditor   = Caller{Org: OrgAuditor, Role: RoleAuditor, UserID: "aud"}
)

const (
	rupee = int64(100)
	lakh  = 100000 * rupee
)

type dealOpts struct {
	id       string
	qty      int64
	mse      bool
	termDays int
	written  bool
	tolBps   int64
	supplier string
	deadline time.Time
}

func (c *chain) mkDeal(t *testing.T, o dealOpts) string {
	t.Helper()
	if o.id == "" {
		o.id = "DL-20261020-0001"
	}
	if o.qty == 0 {
		o.qty = 100
	}
	if o.supplier == "" {
		o.supplier = "SUP-007"
	}
	if o.termDays == 0 {
		o.termDays, o.written = 45, true
	}
	if o.deadline.IsZero() {
		o.deadline = c.now.Add(40 * day)
	}
	d := Deal{DealID: o.id, PONumber: "PO-4411", BuyerID: "BUYER-001", SupplierID: o.supplier, SupplierAccountRef: "ACC-" + o.supplier,
		LienAmountPaise: o.qty * 10000 * rupee, ToleranceBps: o.tolBps, DeliveryDeadline: o.deadline,
		MSE:            MSE{Category: MseSmall, TermDays: o.termDays, UdyamNo: "UDYAM-KL-00-0000007", Applies: o.mse, WrittenAgreement: o.written},
		ApprovalPolicy: []PolicyBand{{0, []Role{RolePlantHead}}, {50 * lakh, []Role{RolePlantHead, RoleCFO}}}}
	b, _ := json.Marshal(d)
	lines, _ := json.Marshal([]Line{{LineNo: 1, SKU: "SKU-STEEL-12", OrderedQty: o.qty, UnitPricePaise: 10000 * rupee}})
	if _, err := c.do(procure, map[string][]byte{"lines": lines}, func(e *Engine) error { return e.CreateDeal(string(b)) }); err != nil {
		t.Fatalf("CreateDeal: %v", err)
	}
	c.must(t, bank, func(e *Engine) error { return e.ConfirmLien(o.id, "LIEN-9921", d.LienAmountPaise) })
	return o.id
}

func (c *chain) must(t *testing.T, who Caller, f func(e *Engine) error) []Event {
	t.Helper()
	ev, err := c.do(who, nil, f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return ev
}

func (c *chain) wantErr(t *testing.T, code Code, who Caller, f func(e *Engine) error) {
	t.Helper()
	_, err := c.do(who, nil, f)
	if err == nil {
		t.Fatalf("expected %s, got success", code)
	}
	if CodeOf(err) != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func (c *chain) grn(t *testing.T, id string, n, qty int64, key string) {
	t.Helper()
	c.must(t, warehouse, func(e *Engine) error {
		return e.PostGoodsReceipt(id, n, fmt.Sprintf(`[{"lineNo":1,"deliveredQty":%d}]`, qty), key)
	})
}

func (c *chain) qc(t *testing.T, id string, n, acc, rej int64, key string) []Event {
	t.Helper()
	return c.must(t, procure, func(e *Engine) error {
		return e.RecordQualityCheck(id, n, fmt.Sprintf(`[{"lineNo":1,"acceptedQty":%d,"rejectedQty":%d}]`, acc, rej), key)
	})
}

func (c *chain) deal(t *testing.T, id string) *Deal {
	t.Helper()
	var d Deal
	if err := json.Unmarshal(c.state["deal:"+id], &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

func (c *chain) tranche(t *testing.T, id string, n int64) *Tranche {
	t.Helper()
	var tr Tranche
	if err := json.Unmarshal(c.state[fmt.Sprintf("tranche:%s:%d", id, n)], &tr); err != nil {
		t.Fatalf("tranche %d: %v", n, err)
	}
	return &tr
}

func (c *chain) held(t *testing.T, id string, n, l int64) *HeldPortion {
	t.Helper()
	var h HeldPortion
	if err := json.Unmarshal(c.state[fmt.Sprintf("held:%s:%d:%d", id, n, l)], &h); err != nil {
		t.Fatalf("held: %v", err)
	}
	return &h
}

// invariant: released + held never exceeds the lien (checked after flows).
func (c *chain) invariant(t *testing.T, id string) {
	t.Helper()
	d := c.deal(t, id)
	if d.TotalReleasedPaise+d.TotalHeldPaise > d.LienAmountPaise {
		t.Fatalf("INVARIANT BROKEN: released %d + held %d > lien %d", d.TotalReleasedPaise, d.TotalHeldPaise, d.LienAmountPaise)
	}
	if d.TotalHeldPaise < 0 || d.TotalReleasedPaise < 0 {
		t.Fatalf("negative totals")
	}
}

func names(ev []Event) []string {
	var n []string
	for _, e := range ev {
		n = append(n, e.Name)
	}
	return n
}

func (c *chain) approveAndRelease(t *testing.T, id string, n int64, approvers []Caller, key string) []Event {
	t.Helper()
	for _, a := range approvers {
		c.must(t, a, func(e *Engine) error { return e.ApproveRelease(id, n) })
	}
	return c.must(t, procure, func(e *Engine) error { return e.ReleaseTranche(id, n, key) })
}
