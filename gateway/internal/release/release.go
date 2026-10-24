// Package release submits ReleaseTranche with a deterministic idempotency key.
//
// The approve handler (synchronous auto-release) and the listener (ApprovalRecorded satisfied:true
// fallback) may both try to release the same tranche; with the same idem key the chaincode accepts
// exactly one and rejects the other as ERR_DUPLICATE / ERR_ALREADY_RELEASED.
package release

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
	"github.com/Hansika-hub/anubandh/gateway/internal/fabric"
)

// Submitter is the chain write path (satisfied by *fabric.Submitter).
type Submitter interface {
	Submit(ctx context.Context, req fabric.SubmitRequest) (fabric.SubmitResult, error)
}

// IdemKey is the gateway's release idempotency key ("<source>:<sourceEventId>", BUILD.md §2).
func IdemKey(dealID string, trancheNo int64) string {
	return fmt.Sprintf("gateway:release-%s-%d", dealID, trancheNo)
}

// Release submits ReleaseTranche signed by the identity in ctx.
func Release(ctx context.Context, chain Submitter, dealID string, trancheNo int64, idemKey string) (fabric.SubmitResult, error) {
	if idemKey == "" {
		idemKey = IdemKey(dealID, trancheNo)
	}
	return chain.Submit(ctx, fabric.SubmitRequest{Fn: "ReleaseTranche", Args: []string{dealID, strconv.FormatInt(trancheNo, 10), idemKey}})
}

// AlreadyDone reports whether err means the tranche was already released (not a failure).
func AlreadyDone(err error) bool {
	return apperr.Is(err, apperr.ErrDuplicate) || apperr.Is(err, apperr.ErrAlreadyReleased)
}
