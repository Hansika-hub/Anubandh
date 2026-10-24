package main

import (
	"fmt"
	"time"

	"github.com/hyperledger/fabric-chaincode-go/pkg/statebased"
	"github.com/hyperledger/fabric-chaincode-go/shim"
	"github.com/hyperledger/fabric-contract-api-go/contractapi"

	"github.com/Hansika-hub/Anubandh/chaincode/deal/core"
)

// fabricLedger adapts one Fabric transaction context to core.Ledger.
// Note: Fabric reads never see this transaction's own writes; core.Engine handles that.
type fabricLedger struct {
	ctx contractapi.TransactionContextInterface
	ts  time.Time
}

func newFabricLedger(ctx contractapi.TransactionContextInterface) (*fabricLedger, error) {
	t, err := ctx.GetStub().GetTxTimestamp()
	if err != nil || t == nil {
		return nil, fmt.Errorf("ERR_INTERNAL: no transaction timestamp: %v", err)
	}
	// The timestamp is chosen by the CLIENT in the proposal header (Fabric does not verify it
	// against a wall clock). It is the only deterministic time source a chaincode has; time.Now()
	// would make endorsers disagree. See HANDOFF.md "timekeeper".
	return &fabricLedger{ctx: ctx, ts: time.Unix(t.GetSeconds(), int64(t.GetNanos())).UTC()}, nil
}

func (l *fabricLedger) stub() shim.ChaincodeStubInterface { return l.ctx.GetStub() }

func (l *fabricLedger) Get(key string) ([]byte, error) { return l.stub().GetState(key) }
func (l *fabricLedger) Put(key string, v []byte) error { return l.stub().PutState(key, v) }
func (l *fabricLedger) Del(key string) error           { return l.stub().DelState(key) }
func (l *fabricLedger) Now() time.Time                 { return l.ts }
func (l *fabricLedger) TxID() string                   { return l.stub().GetTxID() }
func (l *fabricLedger) GetPrivate(c, k string) ([]byte, error) {
	return l.stub().GetPrivateData(c, k)
}
func (l *fabricLedger) PutPrivate(c, k string, v []byte) error {
	return l.stub().PutPrivateData(c, k, v)
}

func (l *fabricLedger) Transient(key string) ([]byte, bool) {
	m, err := l.stub().GetTransient()
	if err != nil {
		return nil, false
	}
	v, ok := m[key]
	return v, ok
}

// Range returns every key with the given prefix, ascending. "\U0010FFFF" is the highest valid
// UTF-8 code point (Fabric requires keys to be valid UTF-8).
func (l *fabricLedger) Range(prefix string) ([]core.KV, error) {
	it, err := l.stub().GetStateByRange(prefix, prefix+"\U0010FFFF")
	if err != nil {
		return nil, err
	}
	defer it.Close()
	var out []core.KV
	for it.HasNext() {
		kv, err := it.Next()
		if err != nil {
			return nil, err
		}
		out = append(out, core.KV{Key: kv.Key, Value: kv.Value})
	}
	return out, nil
}

func (l *fabricLedger) History(key string) ([]core.HistoryEntry, error) {
	it, err := l.stub().GetHistoryForKey(key)
	if err != nil {
		return nil, err
	}
	defer it.Close()
	var out []core.HistoryEntry
	for it.HasNext() {
		km, err := it.Next()
		if err != nil {
			return nil, err
		}
		ts := km.GetTimestamp()
		out = append(out, core.HistoryEntry{
			TxID: km.GetTxId(), IsDelete: km.GetIsDelete(), Value: km.GetValue(),
			Timestamp: time.Unix(ts.GetSeconds(), int64(ts.GetNanos())).UTC(),
		})
	}
	return out, nil
}

// Caller builds the acting identity from the client certificate. Required cert attributes
// (register them in the CA with ":ecert"): role; supplierId for supplier users; optional
// userId and buyerId.
func (l *fabricLedger) Caller() (core.Caller, error) {
	id := l.ctx.GetClientIdentity()
	msp, err := id.GetMSPID()
	if err != nil {
		return core.Caller{}, err
	}
	attr := func(n string) string { v, _, _ := id.GetAttributeValue(n); return v }
	role := core.Role(attr("role"))
	org, err := core.OrgFromIdentity(msp, role)
	if err != nil {
		return core.Caller{}, err
	}
	uid := attr("userId")
	if uid == "" {
		if uid, err = id.GetID(); err != nil {
			return core.Caller{}, err
		}
	}
	return core.Caller{Org: org, Role: role, UserID: uid, SupplierID: attr("supplierId"), BuyerID: attr("buyerId")}, nil
}

// SetKeyEndorsement requires peers of the given orgs to endorse any later write to key.
// This is org-level (peer) endorsement. Roles such as PLANT_HEAD / CFO are enforced inside the
// chaincode from the client certificate (core/policy.go) — NOT by this mechanism.
func (l *fabricLedger) SetKeyEndorsement(key string, orgs []core.Org) error {
	ep, err := statebased.NewStateEP(nil)
	if err != nil {
		return err
	}
	var msps []string
	for _, o := range orgs {
		msps = append(msps, core.OrgMSPs[o]...)
	}
	if err := ep.AddOrgs(statebased.RoleTypePeer, msps...); err != nil {
		return err
	}
	pol, err := ep.Policy()
	if err != nil {
		return err
	}
	return l.stub().SetStateValidationParameter(key, pol)
}
