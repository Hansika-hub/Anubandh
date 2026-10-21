package core

import (
	"encoding/json"
	"testing"
)

func TestEncodeEventsSingleKeepsFrozenName(t *testing.T) {
	n, p, err := EncodeEvents([]Event{{Name: "LienConfirmed", Payload: map[string]any{"dealId": "D", "txId": "t", "ts": "x", "lienRef": "L"}}})
	if err != nil || n != "LienConfirmed" {
		t.Fatal(n, err)
	}
	var m map[string]any
	_ = json.Unmarshal(p, &m)
	if m["lienRef"] != "L" || m["dealId"] != "D" {
		t.Fatalf("%v", m)
	}
}

func TestEncodeEventsBatchPreservesOrderAndIsDeterministic(t *testing.T) {
	evs := []Event{{"QualityRecorded", map[string]any{"dealId": "D", "txId": "t", "ts": "x", "b": 1, "a": 2}}, {"TrancheAccepted", map[string]any{"dealId": "D", "txId": "t", "ts": "x"}}}
	n, p1, _ := EncodeEvents(evs)
	_, p2, _ := EncodeEvents(evs)
	if n != BatchEventName || string(p1) != string(p2) {
		t.Fatal("batch name / determinism (endorsers must produce identical bytes)")
	}
	var env struct {
		DealID string
		Events []Event
	}
	_ = json.Unmarshal(p1, &env)
	if env.DealID != "D" || len(env.Events) != 2 || env.Events[0].Name != "QualityRecorded" || env.Events[1].Name != "TrancheAccepted" {
		t.Fatalf("%+v", env)
	}
	if n, p, _ := EncodeEvents(nil); n != "" || p != nil {
		t.Fatal("no events => no SetEvent")
	}
}

func TestOrgFromIdentity(t *testing.T) {
	for _, tc := range []struct {
		msp  string
		role Role
		want Org
		ok   bool
	}{
		{"BuyerMSP", RoleCFO, OrgBuyer, true},
		{"BuyerMSP", RoleWarehouse, OrgBuyer, true},
		{"BankMSP", RoleSupplierUser, OrgSupplier, true}, // supplier identity issued by the bank's CA
		{"SupplierMSP", RoleSupplierUser, OrgSupplier, true},
		{"BankMSP", RoleArbiter, OrgBank, true},
		{"AuditorMSP", RoleAuditor, OrgAuditor, true},
		{"BuyerMSP", RoleArbiter, "", false}, // buyer cannot mint an arbiter
		{"BankMSP", RoleCFO, "", false},      // bank cannot impersonate a CFO
		{"SupplierMSP", RoleBankOfficer, "", false},
		{"EvilMSP", RoleCFO, "", false},
		{"BuyerMSP", "", "", false}, // missing role attribute
	} {
		got, err := OrgFromIdentity(tc.msp, tc.role)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("%s/%s => %v,%v", tc.msp, tc.role, got, err)
		}
	}
}
