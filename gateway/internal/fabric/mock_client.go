package fabric

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
	"github.com/Hansika-hub/anubandh/gateway/internal/clock"
	"github.com/Hansika-hub/anubandh/gateway/internal/model"
)

// MockClient is an in-memory stand-in for the deal chaincode (BUILD.md §14.4, FABRIC_MOCK=true).
//
// It reproduces the Fabric semantics the gateway depends on, so code proven against the mock
// behaves the same against Drunix peers:
//   - a failed transaction writes nothing (all-or-nothing rollback);
//   - a transaction carries at most ONE chaincode event (shim SetEvent keeps only the last one);
//   - events are delivered only for committed transactions, in block order, replayable from a block;
//   - the chaincode sees only the transaction timestamp, never a wall clock.
//
// It is also the live-demo fallback: with FABRIC_MOCK_STATE set, the ledger survives restarts.
type MockClient struct {
	mu        sync.Mutex
	clk       *clock.Offset
	st        *mockState
	notify    chan struct{}
	statePath string
	failMVCC  int
}

type mockState struct {
	Height         uint64                                `json:"height"`
	Deals          map[string]*model.Deal                `json:"deals"`
	Extra          map[string]map[string]json.RawMessage `json:"extra"`
	Lines          map[string][]*model.Line              `json:"lines"`
	Tranches       map[string][]*mockTranche             `json:"tranches"`
	Held           map[string][]*mockHeld                `json:"held"`
	Idem           map[string]bool                       `json:"idem"`
	History        map[string][]model.HistoryEntry       `json:"history"`
	Events         []mockEvent                           `json:"events"`
	ClockOffsetSec int64                                 `json:"clockOffsetSec"`
}

type mockEvent struct {
	Block     uint64          `json:"block"`
	TxID      string          `json:"txId"`
	EventName string          `json:"eventName"`
	Payload   json.RawMessage `json:"payload"`
}

type mockObjection struct {
	Reason         string     `json:"reason"`
	RaisedAt       time.Time  `json:"raisedAt"`
	Open           bool       `json:"open"`
	BuyerSigned    bool       `json:"buyerSigned"`
	SupplierSigned bool       `json:"supplierSigned"`
	ResolvedAt     *time.Time `json:"resolvedAt"`
}

type mockTranche struct {
	model.Tranche
	Obj      *mockObjection  `json:"obj,omitempty"`
	Expected map[int64]int64 `json:"expected,omitempty"` // replacement tranche: lineNo -> qty awaited
	Prices   map[int64]int64 `json:"prices,omitempty"`   // tranche accepted at an adjusted unit price
	Warned   bool            `json:"warned,omitempty"`
	Acked    bool            `json:"acked,omitempty"`
}

type mockProposal struct {
	Amount     int64     `json:"amount"`
	ProposedBy model.Org `json:"proposedBy"`
	ProposedAt time.Time `json:"proposedAt"`
}

type mockDispute struct {
	Reason   string    `json:"reason"`
	RaisedBy model.Org `json:"raisedBy"`
	RaisedAt time.Time `json:"raisedAt"`
}

type mockHeld struct {
	model.HeldPortion
	CreditNote *mockProposal `json:"creditNote,omitempty"`
	PriceAdj   *mockProposal `json:"priceAdj,omitempty"`
	Dispute    *mockDispute  `json:"dispute,omitempty"`
}

// MockOptions configures NewMockClient.
type MockOptions struct {
	Clock     *clock.Offset // shared with the API so days-left matches the chain's notion of now
	StatePath string        // optional JSON file; empty keeps the ledger in memory only
}

// NewMockClient creates the mock, loading StatePath if it exists.
func NewMockClient(opts MockOptions) (*MockClient, error) {
	if opts.Clock == nil {
		opts.Clock = clock.NewOffset(nil)
	}
	m := &MockClient{clk: opts.Clock, statePath: opts.StatePath, notify: make(chan struct{}), st: newMockState()}
	if opts.StatePath != "" {
		b, err := os.ReadFile(opts.StatePath)
		switch {
		case err == nil:
			st := newMockState()
			if err := json.Unmarshal(b, st); err != nil {
				return nil, fmt.Errorf("mock: corrupt state file %s: %w", opts.StatePath, err)
			}
			st.ensureMaps()
			m.st = st
			m.clk.SetOffset(time.Duration(st.ClockOffsetSec) * time.Second)
		case !os.IsNotExist(err):
			return nil, fmt.Errorf("mock: read state file: %w", err)
		}
	}
	return m, nil
}

func newMockState() *mockState {
	s := &mockState{}
	s.ensureMaps()
	return s
}

func (s *mockState) ensureMaps() {
	if s.Deals == nil {
		s.Deals = map[string]*model.Deal{}
	}
	if s.Extra == nil {
		s.Extra = map[string]map[string]json.RawMessage{}
	}
	if s.Lines == nil {
		s.Lines = map[string][]*model.Line{}
	}
	if s.Tranches == nil {
		s.Tranches = map[string][]*mockTranche{}
	}
	if s.Held == nil {
		s.Held = map[string][]*mockHeld{}
	}
	if s.Idem == nil {
		s.Idem = map[string]bool{}
	}
	if s.History == nil {
		s.History = map[string][]model.HistoryEntry{}
	}
}

// Mode implements Client.
func (m *MockClient) Mode() string { return "mock" }

// Close implements Client.
func (m *MockClient) Close() error { return nil }

// Clock returns the mock's adjustable clock.
func (m *MockClient) Clock() *clock.Offset { return m.clk }

// Advance fast-forwards the mock chain's clock (demo/testing of 15-day and 45-day rules).
func (m *MockClient) Advance(d time.Duration) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clk.Advance(d)
	m.st.ClockOffsetSec = int64(m.clk.Offset() / time.Second)
	return now, m.persistLocked()
}

// FailNextWithMVCC makes the next n submits fail with an MVCC read conflict (retry testing).
func (m *MockClient) FailNextWithMVCC(n int) {
	m.mu.Lock()
	m.failMVCC = n
	m.mu.Unlock()
}

// Height returns the current block height.
func (m *MockClient) Height() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.Height
}

// mtx is one simulated transaction.
type mtx struct {
	m         *MockClient
	id        Identity
	txID      string
	now       time.Time
	transient map[string][]byte
	events    []mockEmit
	idem      []string
	touched   map[string]bool
}

type mockFn struct {
	nargs int
	write bool
	run   func(t *mtx, a []string) ([]byte, error)
}

func newTxID() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (m *MockClient) newTx(id Identity, transient map[string][]byte) *mtx {
	return &mtx{m: m, id: id, txID: newTxID(), now: m.clk.Now().Truncate(time.Second), transient: transient, touched: map[string]bool{}}
}

// SubmitOnce implements Client.
func (m *MockClient) SubmitOnce(ctx context.Context, id Identity, req SubmitRequest) (SubmitResult, error) {
	if err := ctx.Err(); err != nil {
		return SubmitResult{}, apperr.Wrap(apperr.ErrInternal, err, "request cancelled").WithDetail("unavailable", true)
	}
	fn, ok := mockFns[req.Fn]
	if !ok {
		return SubmitResult{}, apperr.Newf(apperr.ErrBadRequest, "unknown chaincode function %q", req.Fn)
	}
	if len(req.Args) != fn.nargs {
		return SubmitResult{}, apperr.Newf(apperr.ErrBadRequest, "%s expects %d args, got %d", req.Fn, fn.nargs, len(req.Args))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.newTx(id, req.Transient)
	if m.failMVCC > 0 {
		m.failMVCC--
		return SubmitResult{TxID: t.txID}, &apperr.Error{Code: apperr.ErrMvccConflict, Message: "transaction " + t.txID + " invalidated: MVCC_READ_CONFLICT", TxID: t.txID, Retryable: true}
	}
	if !fn.write {
		payload, err := fn.run(t, req.Args)
		return SubmitResult{Payload: payload, TxID: t.txID}, err
	}
	key := mockDealKey(req.Fn, req.Args)
	snap := m.snapshot(key)
	payload, err := fn.run(t, req.Args)
	if err != nil {
		m.restore(key, snap)
		if ae, ok := apperr.As(err); ok {
			ae.TxID = t.txID
			return SubmitResult{TxID: t.txID}, ae
		}
		return SubmitResult{TxID: t.txID}, apperr.Wrap(apperr.ErrInternal, err, "chaincode error")
	}
	block := m.commit(t)
	if perr := m.persistLocked(); perr != nil {
		return SubmitResult{}, apperr.Wrap(apperr.ErrInternal, perr, "mock ledger persist failed")
	}
	return SubmitResult{Payload: payload, TxID: t.txID, BlockNumber: block}, nil
}

// Evaluate implements Client. Writes are simulated and rolled back, as a peer would.
func (m *MockClient) Evaluate(ctx context.Context, id Identity, fn string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, apperr.Wrap(apperr.ErrInternal, err, "request cancelled")
	}
	f, ok := mockFns[fn]
	if !ok {
		return nil, apperr.Newf(apperr.ErrBadRequest, "unknown chaincode function %q", fn)
	}
	if len(args) != f.nargs {
		return nil, apperr.Newf(apperr.ErrBadRequest, "%s expects %d args, got %d", fn, f.nargs, len(args))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.newTx(id, nil)
	if !f.write {
		return f.run(t, args)
	}
	key := mockDealKey(fn, args)
	snap := m.snapshot(key)
	out, err := f.run(t, args)
	m.restore(key, snap)
	return out, err
}

// mockDealKey returns the deal a write touches, so only that deal is snapshotted for rollback.
func mockDealKey(fn string, args []string) string {
	if len(args) == 0 {
		return ""
	}
	if fn == "CreateDeal" {
		var d struct {
			DealID string `json:"dealId"`
		}
		_ = json.Unmarshal([]byte(args[0]), &d)
		return d.DealID
	}
	return args[0]
}

type mockSnap struct {
	present bool
	data    []byte
}

type mockDealRecords struct {
	Deal     *model.Deal                `json:"deal"`
	Extra    map[string]json.RawMessage `json:"extra"`
	Lines    []*model.Line              `json:"lines"`
	Tranches []*mockTranche             `json:"tranches"`
	Held     []*mockHeld                `json:"held"`
}

func (m *MockClient) snapshot(dealID string) mockSnap {
	d, ok := m.st.Deals[dealID]
	if !ok {
		return mockSnap{}
	}
	b, _ := json.Marshal(mockDealRecords{Deal: d, Extra: m.st.Extra[dealID], Lines: m.st.Lines[dealID], Tranches: m.st.Tranches[dealID], Held: m.st.Held[dealID]})
	return mockSnap{present: true, data: b}
}

func (m *MockClient) restore(dealID string, s mockSnap) {
	if dealID == "" {
		return
	}
	if !s.present {
		delete(m.st.Deals, dealID)
		delete(m.st.Extra, dealID)
		delete(m.st.Lines, dealID)
		delete(m.st.Tranches, dealID)
		delete(m.st.Held, dealID)
		return
	}
	var r mockDealRecords
	_ = json.Unmarshal(s.data, &r)
	m.st.Deals[dealID] = r.Deal
	if r.Extra != nil {
		m.st.Extra[dealID] = r.Extra
	}
	m.st.Lines[dealID] = r.Lines
	m.st.Tranches[dealID] = r.Tranches
	m.st.Held[dealID] = r.Held
}

func (m *MockClient) commit(t *mtx) uint64 {
	m.st.Height++
	block := m.st.Height
	for _, k := range t.idem {
		m.st.Idem[k] = true
	}
	for dealID := range t.touched {
		if d, ok := m.st.Deals[dealID]; ok {
			ts := t.now
			d.UpdatedAt = &ts
			b, _ := json.Marshal(d)
			m.st.History[dealID] = append(m.st.History[dealID], model.HistoryEntry{TxID: t.txID, Timestamp: &ts, Value: b})
		}
	}
	if name, payload := encodeEvents(t.events); name != "" {
		m.st.Events = append(m.st.Events, mockEvent{Block: block, TxID: t.txID, EventName: name, Payload: payload})
		close(m.notify)
		m.notify = make(chan struct{})
	}
	return block
}

func (m *MockClient) persistLocked() error {
	if m.statePath == "" {
		return nil
	}
	b, err := json.Marshal(m.st)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(m.statePath); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	tmp := m.statePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.statePath)
}

// ChaincodeEvents implements Client: replays from startBlock, then follows new commits.
func (m *MockClient) ChaincodeEvents(ctx context.Context, _ Identity, startBlock uint64) (<-chan *ChaincodeEvent, error) {
	out := make(chan *ChaincodeEvent, 64)
	go func() {
		defer close(out)
		m.mu.Lock()
		idx := sort.Search(len(m.st.Events), func(i int) bool { return m.st.Events[i].Block >= startBlock })
		m.mu.Unlock()
		for {
			m.mu.Lock()
			pending := append([]mockEvent(nil), m.st.Events[idx:]...)
			wait := m.notify
			m.mu.Unlock()
			for _, e := range pending {
				ev := &ChaincodeEvent{BlockNumber: e.Block, TransactionID: e.TxID, ChaincodeName: "deal", EventName: e.EventName, Payload: e.Payload}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
			idx += len(pending)
			if len(pending) > 0 {
				continue
			}
			select {
			case <-wait:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// ---- transaction helpers ----

func (t *mtx) st() *mockState { return t.m.st }

// mockEmit is one logical event of a transaction.
type mockEmit struct {
	Name    string         `json:"name"`
	Payload map[string]any `json:"payload"`
}

// emit records one logical event. dealId, txId and ts are added to every payload (BUILD.md §6).
func (t *mtx) emit(name, dealID string, fields map[string]any) {
	p := map[string]any{"dealId": dealID, "txId": t.txID, "ts": t.now}
	for k, v := range fields {
		p[k] = v
	}
	t.events = append(t.events, mockEmit{Name: name, Payload: p})
}

// encodeEvents turns a transaction's logical events into its single chaincode event, exactly as
// the real chaincode does (chaincode/deal/core/events.go): one event goes out as itself, two or
// more are wrapped in a TxEventBatch envelope in emission order.
func encodeEvents(evs []mockEmit) (string, []byte) {
	switch len(evs) {
	case 0:
		return "", nil
	case 1:
		b, _ := json.Marshal(evs[0].Payload)
		return evs[0].Name, b
	}
	first := evs[0].Payload
	b, _ := json.Marshal(map[string]any{"dealId": first["dealId"], "txId": first["txId"], "ts": first["ts"], "events": evs})
	return model.EvTxEventBatch, b
}

func (t *mtx) useIdem(key string) error {
	if !model.IdemKeyRe.MatchString(key) {
		return apperr.Newf(apperr.ErrBadRequest, "idemKey must look like <source>:<id>, got %q", key)
	}
	if t.st().Idem[key] {
		return apperr.Newf(apperr.ErrDuplicate, "idem key %s already processed", key)
	}
	for _, k := range t.idem {
		if k == key {
			return apperr.Newf(apperr.ErrDuplicate, "idem key %s already processed", key)
		}
	}
	t.idem = append(t.idem, key)
	return nil
}

func (t *mtx) requireOrg(orgs ...model.Org) error {
	for _, o := range orgs {
		if t.id.Org == o {
			return nil
		}
	}
	return apperr.Newf(apperr.ErrForbiddenRole, "org %s may not call this function", t.id.Org)
}

// requireParty checks that a buyer/supplier user acts only on their own deals.
func (t *mtx) requireParty(d *model.Deal) error {
	if t.id.PartyID == "" {
		return nil
	}
	switch t.id.Org {
	case model.OrgBuyer:
		if d.BuyerID != t.id.PartyID {
			return apperr.Newf(apperr.ErrForbiddenRole, "%s is not the buyer on %s", t.id.PartyID, d.DealID)
		}
	case model.OrgSupplier:
		if d.SupplierID != t.id.PartyID {
			return apperr.Newf(apperr.ErrForbiddenRole, "%s is not the supplier on %s", t.id.PartyID, d.DealID)
		}
	}
	return nil
}

func (t *mtx) deal(dealID string) (*model.Deal, error) {
	d, ok := t.st().Deals[dealID]
	if !ok {
		return nil, apperr.Newf(apperr.ErrNotFound, "deal %s not found", dealID)
	}
	return d, nil
}

func (t *mtx) touch(dealID string) { t.touched[dealID] = true }

func (t *mtx) tranche(dealID string, n int64) (*mockTranche, error) {
	for _, tr := range t.st().Tranches[dealID] {
		if tr.TrancheNo == n {
			return tr, nil
		}
	}
	return nil, apperr.Newf(apperr.ErrNotFound, "tranche %s/%d not found", dealID, n)
}

func (t *mtx) held(dealID string, n, lineNo int64) (*mockHeld, error) {
	for _, h := range t.st().Held[dealID] {
		if h.TrancheNo == n && h.LineNo == lineNo {
			return h, nil
		}
	}
	return nil, apperr.Newf(apperr.ErrNotFound, "held portion %s/%d/%d not found", dealID, n, lineNo)
}

func (t *mtx) line(dealID string, lineNo int64) (*model.Line, error) {
	for _, l := range t.st().Lines[dealID] {
		if l.LineNo == lineNo {
			return l, nil
		}
	}
	return nil, apperr.Newf(apperr.ErrBadRequest, "deal %s has no line %d", dealID, lineNo)
}

func argInt(s, name string, lo int64) (int64, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < lo {
		return 0, apperr.Newf(apperr.ErrBadRequest, "%s must be an integer >= %d", name, lo)
	}
	return v, nil
}

func jsonOut(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, apperr.Wrap(apperr.ErrInternal, err, "marshal result")
	}
	return b, nil
}

// EventCount returns how many chaincode events the mock ledger holds.
func (m *MockClient) EventCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.st.Events)
}
