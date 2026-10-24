package fabric

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hyperledger/fabric-gateway/pkg/client"
	gwproto "github.com/hyperledger/fabric-protos-go-apiv2/gateway"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
	"github.com/Hansika-hub/anubandh/gateway/internal/model"
)

type scriptedClient struct {
	mu    sync.Mutex
	errs  []error
	calls int
	ids   []Identity
}

func (c *scriptedClient) SubmitOnce(_ context.Context, id Identity, _ SubmitRequest) (SubmitResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.ids = append(c.ids, id)
	if len(c.errs) > 0 {
		err := c.errs[0]
		c.errs = c.errs[1:]
		if err != nil {
			return SubmitResult{}, err
		}
	}
	return SubmitResult{TxID: "tx-ok", Payload: []byte("ok")}, nil
}
func (c *scriptedClient) Evaluate(context.Context, Identity, string, ...string) ([]byte, error) {
	return []byte("{}"), nil
}
func (c *scriptedClient) ChaincodeEvents(context.Context, Identity, uint64) (<-chan *ChaincodeEvent, error) {
	return nil, nil
}
func (c *scriptedClient) Mode() string { return "test" }
func (c *scriptedClient) Close() error { return nil }

var testID = Identity{UserID: "u1", Org: model.OrgBuyer, Role: model.RolePlantHead}

func fastSubmitter(c Client) (*Submitter, *[]time.Duration) {
	s := NewSubmitter(c, DefaultRetryPolicy, nil, nil)
	var delays []time.Duration
	s.sleep = func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	return s, &delays
}

func mvcc() error {
	return &apperr.Error{Code: apperr.ErrMvccConflict, Retryable: true, Message: "MVCC_READ_CONFLICT"}
}

func TestSubmitRetriesMVCCThenSucceeds(t *testing.T) {
	c := &scriptedClient{errs: []error{mvcc(), mvcc(), nil}}
	s, delays := fastSubmitter(c)
	res, err := s.Submit(WithIdentity(context.Background(), testID), SubmitRequest{Fn: "ReleaseTranche"})
	if err != nil || res.Attempts != 3 || c.calls != 3 {
		t.Fatalf("want success on 3rd attempt, got err=%v attempts=%d calls=%d", err, res.Attempts, c.calls)
	}
	if len(*delays) != 2 {
		t.Fatalf("want 2 backoff sleeps, got %d", len(*delays))
	}
	if (*delays)[1] < (*delays)[0]/2 {
		t.Errorf("backoff should grow: %v", *delays)
	}
}

func TestSubmitGivesUpAfterFiveAttempts(t *testing.T) {
	c := &scriptedClient{errs: []error{mvcc(), mvcc(), mvcc(), mvcc(), mvcc(), mvcc()}}
	s, delays := fastSubmitter(c)
	_, err := s.Submit(WithIdentity(context.Background(), testID), SubmitRequest{Fn: "ApproveRelease"})
	if !apperr.Is(err, apperr.ErrMvccConflict) || c.calls != 5 || len(*delays) != 4 {
		t.Fatalf("want ERR_MVCC_CONFLICT after exactly 5 attempts, got %v calls=%d sleeps=%d", err, c.calls, len(*delays))
	}
	for _, d := range *delays {
		if d > DefaultRetryPolicy.MaxDelay || d <= 0 {
			t.Errorf("delay %v outside (0, %v]", d, DefaultRetryPolicy.MaxDelay)
		}
	}
}

func TestSubmitNeverRetriesBusinessOrAmbiguousErrors(t *testing.T) {
	for _, e := range []error{
		apperr.New(apperr.ErrDuplicate, "seen"),
		apperr.New(apperr.ErrInvalidTransition, "QC before GRN"),
		&apperr.Error{Code: apperr.ErrInternal, Message: "outcome unknown", Details: map[string]any{"ambiguous": true}},
	} {
		c := &scriptedClient{errs: []error{e}}
		s, _ := fastSubmitter(c)
		_, err := s.Submit(WithIdentity(context.Background(), testID), SubmitRequest{Fn: "X"})
		if err == nil || c.calls != 1 {
			t.Errorf("%v: want single attempt and error, got calls=%d err=%v", e, c.calls, err)
		}
	}
}

func TestSubmitRequiresIdentity(t *testing.T) {
	c := &scriptedClient{}
	s, _ := fastSubmitter(c)
	if _, err := s.Submit(context.Background(), SubmitRequest{Fn: "X"}); !apperr.Is(err, apperr.ErrUnauthorized) || c.calls != 0 {
		t.Fatalf("want ERR_UNAUTHORIZED before any chain call, got %v (calls %d)", err, c.calls)
	}
	def := Identity{UserID: "svc", Org: model.OrgBank, Role: model.RoleBankOfficer}
	s2 := NewSubmitter(c, DefaultRetryPolicy, &def, nil)
	if _, err := s2.Submit(context.Background(), SubmitRequest{Fn: "X"}); err != nil || c.ids[0].UserID != "svc" {
		t.Fatalf("default identity not used: %v %+v", err, c.ids)
	}
}

func TestPackageLevelSubmitWithRetry(t *testing.T) {
	c := &scriptedClient{errs: []error{mvcc(), nil}}
	s, _ := fastSubmitter(c)
	SetDefault(s)
	defer SetDefault(nil)
	out, err := SubmitWithRetry(WithIdentity(context.Background(), testID), "ReleaseTranche", "DL-20261020-0001", "1", "gateway:k")
	if err != nil || string(out) != "ok" || c.calls != 2 {
		t.Fatalf("SubmitWithRetry: out=%q err=%v calls=%d", out, err, c.calls)
	}
	if _, err := Evaluate(WithIdentity(context.Background(), testID), "GetDeal", "DL-20261020-0001"); err != nil {
		t.Fatal(err)
	}
}

func TestClassify(t *testing.T) {
	// MVCC invalidation: definitely not committed -> retryable.
	err := Classify(&client.CommitError{TransactionID: "t1", Code: peer.TxValidationCode_MVCC_READ_CONFLICT}, "t1")
	ae, _ := apperr.As(err)
	if ae == nil || ae.Code != apperr.ErrMvccConflict || !ae.Retryable || ae.TxID != "t1" {
		t.Fatalf("MVCC: %+v", ae)
	}
	err = Classify(&client.CommitError{TransactionID: "t2", Code: peer.TxValidationCode_PHANTOM_READ_CONFLICT}, "t2")
	if !apperr.Is(err, apperr.ErrMvccConflict) {
		t.Fatalf("phantom read must be MVCC: %v", err)
	}
	err = Classify(&client.CommitError{TransactionID: "t3", Code: peer.TxValidationCode_ENDORSEMENT_POLICY_FAILURE}, "t3")
	if !apperr.Is(err, apperr.ErrPolicyNotSatisfied) {
		t.Fatalf("endorsement policy failure: %v", err)
	}

	// Chaincode error relayed in gRPC ErrorDetail.
	st, _ := status.New(codes.Aborted, "failed to endorse transaction, see attached details for more info").WithDetails(
		&gwproto.ErrorDetail{Address: "peer0.buyer:7051", MspId: "BuyerMSP", Message: "chaincode response 500, ERR_ALREADY_RELEASED: tranche 1 already released"})
	err = Classify(st.Err(), "t4")
	ae, _ = apperr.As(err)
	if ae == nil || ae.Code != apperr.ErrAlreadyReleased || ae.Retryable || ae.Message != "tranche 1 already released" {
		t.Fatalf("chaincode error: %+v", ae)
	}

	// Peer unreachable on a read.
	err = Classify(status.Error(codes.Unavailable, "connection refused"), "")
	ae, _ = apperr.As(err)
	if ae == nil || ae.Code != apperr.ErrInternal || ae.Details["unavailable"] != true {
		t.Fatalf("unavailable: %+v", ae)
	}
	if Classify(nil, "") != nil {
		t.Fatal("nil stays nil")
	}
	if !errors.Is(Classify(context.Canceled, ""), context.Canceled) {
		t.Fatal("cause must be preserved")
	}
}
