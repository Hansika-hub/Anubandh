package core

import "encoding/json"

// BatchEventName wraps several logical events in one transaction.
//
// Fabric keeps ONE chaincode event per transaction (a later SetEvent replaces an earlier one),
// but BUILD §6 has transactions that emit several (RecordQualityCheck -> QualityRecorded +
// HeldPortionCreated + TrancheAccepted + MseClockStarted). Single events are emitted
// exactly as frozen in §6. Multi-event transactions use this envelope; the listener must
// unwrap it. See CONTRACT_CHANGE_001.md.
const BatchEventName = "TxEventBatch"

// EncodeEvents turns the logical events of one transaction into the single SetEvent call.
func EncodeEvents(evs []Event) (name string, payload []byte, err error) {
	switch len(evs) {
	case 0:
		return "", nil, nil
	case 1:
		b, err := json.Marshal(evs[0].Payload)
		return evs[0].Name, b, err
	}
	first := evs[0].Payload
	env := map[string]any{"dealId": first["dealId"], "txId": first["txId"], "ts": first["ts"], "events": evs}
	b, err := json.Marshal(env)
	return BatchEventName, b, err
}
