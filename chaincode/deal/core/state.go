package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// KV is one ledger entry returned by Range.
type KV struct {
	Key   string
	Value []byte
}

// Caller is the authenticated client (derived from the X.509 cert by the adapter).
type Caller struct {
	Org        Org
	Role       Role
	UserID     string
	SupplierID string // cert attribute "supplierId" (supplier users only)
	BuyerID    string // cert attribute "buyerId" (optional)
}

// Ledger is everything the engine needs from Fabric. The adapter implements it with the
// stub; tests implement it in memory. Time MUST come from Now() (tx timestamp), never time.Now().
type Ledger interface {
	Get(key string) ([]byte, error) // (nil, nil) when absent
	Put(key string, val []byte) error
	Del(key string) error
	Range(prefix string) ([]KV, error) // ascending key order
	GetPrivate(coll, key string) ([]byte, error)
	PutPrivate(coll, key string, val []byte) error
	Transient(key string) ([]byte, bool)
	Now() time.Time
	TxID() string
	Caller() (Caller, error)
	SetKeyEndorsement(key string, orgs []Org) error
	History(key string) ([]HistoryEntry, error)
}

// Engine runs one transaction. Create a fresh Engine per transaction.
type Engine struct {
	L      Ledger
	events []Event
	pend   map[string]pendVal
}

type pendVal struct {
	v   []byte
	del bool
}

// NewEngine returns a per-transaction engine. Fabric reads never see writes made earlier in
// the SAME transaction, so the engine keeps its own write-through overlay (pend) and every
// read goes through it. Do not call e.L.Get/Range/GetPrivate directly.
func NewEngine(l Ledger) *Engine { return &Engine{L: l, pend: map[string]pendVal{}} }

func privKey(coll, key string) string { return "\x00priv|" + coll + "|" + key }

func (e *Engine) get(key string) ([]byte, error) {
	if p, ok := e.pend[key]; ok {
		if p.del {
			return nil, nil
		}
		return p.v, nil
	}
	return e.L.Get(key)
}

func (e *Engine) put(key string, v []byte) error {
	if err := e.L.Put(key, v); err != nil {
		return err
	}
	e.pend[key] = pendVal{v: v}
	return nil
}

func (e *Engine) del(key string) error {
	if err := e.L.Del(key); err != nil {
		return err
	}
	e.pend[key] = pendVal{del: true}
	return nil
}

func (e *Engine) rng(prefix string) ([]KV, error) {
	base, err := e.L.Range(prefix)
	if err != nil {
		return nil, err
	}
	m := map[string][]byte{}
	for _, kv := range base {
		m[kv.Key] = kv.Value
	}
	for k, p := range e.pend {
		if strings.HasPrefix(k, prefix) {
			if p.del {
				delete(m, k)
			} else {
				m[k] = p.v
			}
		}
	}
	out := make([]KV, 0, len(m))
	for k, v := range m {
		out = append(out, KV{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (e *Engine) getPriv(coll, key string) ([]byte, error) {
	if p, ok := e.pend[privKey(coll, key)]; ok {
		if p.del {
			return nil, nil
		}
		return p.v, nil
	}
	return e.L.GetPrivate(coll, key)
}

func (e *Engine) putPriv(coll, key string, v []byte) error {
	if err := e.L.PutPrivate(coll, key, v); err != nil {
		return err
	}
	e.pend[privKey(coll, key)] = pendVal{v: v}
	return nil
}

// Events returns the logical events emitted so far (adapter flushes them once on success).
func (e *Engine) Events() []Event { return e.events }

func (e *Engine) now() time.Time { return e.L.Now().UTC() }

func (e *Engine) emit(name, dealID string, extra map[string]any) {
	p := map[string]any{"dealId": dealID, "txId": e.L.TxID(), "ts": e.now().Format(time.RFC3339)}
	for k, v := range extra {
		p[k] = v
	}
	e.events = append(e.events, Event{Name: name, Payload: p})
}

// ---- keys (BUILD §5.2) ----
func dealKey(id string) string             { return "deal:" + id }
func trancheKey(id string, n int64) string { return fmt.Sprintf("tranche:%s:%d", id, n) }
func tranchePrefix(id string) string       { return "tranche:" + id + ":" }
func heldKey(id string, n, l int64) string { return fmt.Sprintf("held:%s:%d:%d", id, n, l) }
func heldPrefix(id string) string          { return "held:" + id + ":" }
func lineKey(id string, l int64) string    { return fmt.Sprintf("line:%s:%d", id, l) }
func idemStoreKey(k string) string         { return "idem:" + k }
func statusIdx(s DealStatus, id string) string {
	return "idx:status:" + string(s) + ":" + id
}

func (e *Engine) getJSON(key string, out any) (bool, error) {
	b, err := e.get(key)
	if err != nil {
		return false, fail(ErrInternal, "read %s: %v", key, err)
	}
	if b == nil {
		return false, nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return false, fail(ErrInternal, "decode %s: %v", key, err)
	}
	return true, nil
}

func (e *Engine) putJSON(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fail(ErrInternal, "encode %s: %v", key, err)
	}
	if err := e.put(key, b); err != nil {
		return fail(ErrInternal, "write %s: %v", key, err)
	}
	return nil
}

func (e *Engine) loadDeal(id string) (*Deal, error) {
	var d Deal
	ok, err := e.getJSON(dealKey(id), &d)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fail(ErrNotFound, "deal %s", id)
	}
	return &d, nil
}

// saveDeal writes the deal and keeps the status index (used by QueryDealsByStatus) in sync.
// It deliberately avoids state-DB rich queries: whether Drunix's SQL state DB accepts
// CouchDB selectors is an open question (BUILD §16), key-range scans work on any state DB.
func (e *Engine) saveDeal(d *Deal, oldStatus DealStatus) error {
	d.UpdatedAt = e.now()
	if oldStatus != "" && oldStatus != d.Status {
		if !CanTransition(string(oldStatus), string(d.Status)) {
			return fail(ErrInvalidTransition, "deal %s: %s -> %s not allowed", d.DealID, oldStatus, d.Status)
		}
		if err := e.del(statusIdx(oldStatus, d.DealID)); err != nil {
			return fail(ErrInternal, "index: %v", err)
		}
	}
	if err := e.put(statusIdx(d.Status, d.DealID), []byte("1")); err != nil {
		return fail(ErrInternal, "index: %v", err)
	}
	return e.putJSON(dealKey(d.DealID), d)
}

func (e *Engine) loadTranche(id string, n int64) (*Tranche, error) {
	var t Tranche
	ok, err := e.getJSON(trancheKey(id, n), &t)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fail(ErrNotFound, "tranche %s/%d", id, n)
	}
	return &t, nil
}

func (e *Engine) saveTranche(t *Tranche) error {
	return e.putJSON(trancheKey(t.DealID, t.TrancheNo), t)
}

func (e *Engine) listTranches(id string) ([]*Tranche, error) {
	kvs, err := e.rng(tranchePrefix(id))
	if err != nil {
		return nil, fail(ErrInternal, "range: %v", err)
	}
	out := make([]*Tranche, 0, len(kvs))
	for _, kv := range kvs {
		var t Tranche
		if err := json.Unmarshal(kv.Value, &t); err != nil {
			return nil, fail(ErrInternal, "decode %s: %v", kv.Key, err)
		}
		out = append(out, &t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TrancheNo < out[j].TrancheNo })
	return out, nil
}

func (e *Engine) nextTrancheNo(id string) (int64, error) {
	trs, err := e.listTranches(id)
	if err != nil {
		return 0, err
	}
	var max int64
	for _, t := range trs {
		if t.TrancheNo > max {
			max = t.TrancheNo
		}
	}
	return max + 1, nil
}

func (e *Engine) loadHeld(id string, n, l int64) (*HeldPortion, error) {
	var h HeldPortion
	ok, err := e.getJSON(heldKey(id, n, l), &h)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fail(ErrNotFound, "held portion %s/%d/%d", id, n, l)
	}
	return &h, nil
}

func (e *Engine) saveHeld(h *HeldPortion) error {
	return e.putJSON(heldKey(h.DealID, h.TrancheNo, h.LineNo), h)
}

func (e *Engine) listHeld(id string) ([]*HeldPortion, error) {
	kvs, err := e.rng(heldPrefix(id))
	if err != nil {
		return nil, fail(ErrInternal, "range: %v", err)
	}
	out := make([]*HeldPortion, 0, len(kvs))
	for _, kv := range kvs {
		var h HeldPortion
		if err := json.Unmarshal(kv.Value, &h); err != nil {
			return nil, fail(ErrInternal, "decode %s: %v", kv.Key, err)
		}
		out = append(out, &h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TrancheNo != out[j].TrancheNo {
			return out[i].TrancheNo < out[j].TrancheNo
		}
		return out[i].LineNo < out[j].LineNo
	})
	return out, nil
}

func (e *Engine) loadLine(id string, l int64) (*Line, error) {
	b, err := e.getPriv(PrivateCollection, lineKey(id, l))
	if err != nil {
		return nil, fail(ErrInternal, "private read: %v", err)
	}
	if b == nil {
		return nil, fail(ErrBadRequest, "line %d does not exist on deal %s", l, id)
	}
	var ln Line
	if err := json.Unmarshal(b, &ln); err != nil {
		return nil, fail(ErrInternal, "decode line: %v", err)
	}
	return &ln, nil
}

func (e *Engine) saveLine(ln *Line) error {
	b, err := json.Marshal(ln)
	if err != nil {
		return fail(ErrInternal, "encode line: %v", err)
	}
	if err := e.putPriv(PrivateCollection, lineKey(ln.DealID, ln.LineNo), b); err != nil {
		return fail(ErrInternal, "private write: %v", err)
	}
	return nil
}

// ---- checked money maths (int64 paise, never floats) ----
func mul(a, b int64) (int64, error) {
	if a < 0 || b < 0 {
		return 0, fail(ErrBadRequest, "negative amount")
	}
	if a != 0 && b > (1<<62)/a {
		return 0, fail(ErrBadRequest, "amount overflow")
	}
	return a * b, nil
}

func add(a, b int64) (int64, error) {
	if a > (1<<62)-b {
		return 0, fail(ErrBadRequest, "amount overflow")
	}
	return a + b, nil
}

// ---- caller checks ----
func (e *Engine) requireOrg(orgs ...Org) (Caller, error) {
	c, err := e.L.Caller()
	if err != nil {
		return c, fail(ErrUnauthorized, "%v", err)
	}
	for _, o := range orgs {
		if c.Org == o {
			return c, nil
		}
	}
	return c, fail(ErrForbiddenRole, "org %s may not call this function", c.Org)
}

func requireRole(c Caller, roles ...Role) error {
	for _, r := range roles {
		if c.Role == r {
			return nil
		}
	}
	return fail(ErrForbiddenRole, "role %q may not perform this action", c.Role)
}

// requireParty stops supplier A acting on supplier B's deal (and a foreign buyer id).
func requireParty(c Caller, d *Deal) error {
	if c.Org == OrgSupplier && c.SupplierID != d.SupplierID {
		return fail(ErrForbiddenRole, "supplier identity does not belong to deal %s", d.DealID)
	}
	if c.Org == OrgBuyer && c.BuyerID != "" && c.BuyerID != d.BuyerID {
		return fail(ErrForbiddenRole, "buyer identity does not belong to deal %s", d.DealID)
	}
	return nil
}

func isTerminal(s DealStatus) bool { return s == DealExpired || s == DealClosed }

func trim(s string) string { return strings.TrimSpace(s) }
