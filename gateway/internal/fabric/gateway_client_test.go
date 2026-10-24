package fabric

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	gwproto "github.com/hyperledger/fabric-protos-go-apiv2/gateway"
	"github.com/hyperledger/fabric-protos-go-apiv2/msp"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
	"github.com/Hansika-hub/anubandh/gateway/internal/model"
)

// This test runs the real GatewayClient and the real fabric-gateway SDK against an in-process
// gRPC server that implements the Fabric Gateway service (the protocol a Drunix Lite Peer serves),
// over TLS with X.509 identities. It proves the wire path: proposal building, signing, transient
// data, endorse -> submit -> commit status, chaincode error relay, MVCC retry and event streaming.

type fakeGateway struct {
	gwproto.UnimplementedGatewayServer
	mu          sync.Mutex
	mvccLeft    map[string]int // fn -> MVCC failures still to return
	commitFn    map[string]string
	endorsed    []endorsed
	submitCalls int
}

type endorsed struct {
	fn        string
	args      []string
	mspID     string
	channel   string
	transient map[string][]byte
}

func parseProposal(sp *peer.SignedProposal) (txID, channel, mspID string, args []string, transient map[string][]byte) {
	prop := &peer.Proposal{}
	_ = proto.Unmarshal(sp.GetProposalBytes(), prop)
	hdr := &common.Header{}
	_ = proto.Unmarshal(prop.GetHeader(), hdr)
	ch := &common.ChannelHeader{}
	_ = proto.Unmarshal(hdr.GetChannelHeader(), ch)
	sh := &common.SignatureHeader{}
	_ = proto.Unmarshal(hdr.GetSignatureHeader(), sh)
	sid := &msp.SerializedIdentity{}
	_ = proto.Unmarshal(sh.GetCreator(), sid)
	pl := &peer.ChaincodeProposalPayload{}
	_ = proto.Unmarshal(prop.GetPayload(), pl)
	spec := &peer.ChaincodeInvocationSpec{}
	_ = proto.Unmarshal(pl.GetInput(), spec)
	for _, a := range spec.GetChaincodeSpec().GetInput().GetArgs() {
		args = append(args, string(a))
	}
	return ch.GetTxId(), ch.GetChannelId(), sid.GetMspid(), args, pl.GetTransientMap()
}

func preparedEnvelope(channel, txID string, result []byte) *common.Envelope {
	chdr, _ := proto.Marshal(&common.ChannelHeader{ChannelId: channel, TxId: txID, Type: int32(common.HeaderType_ENDORSER_TRANSACTION)})
	action, _ := proto.Marshal(&peer.ChaincodeAction{Response: &peer.Response{Status: 200, Payload: result}})
	prp, _ := proto.Marshal(&peer.ProposalResponsePayload{Extension: action})
	actionPayload, _ := proto.Marshal(&peer.ChaincodeActionPayload{Action: &peer.ChaincodeEndorsedAction{ProposalResponsePayload: prp}})
	tx, _ := proto.Marshal(&peer.Transaction{Actions: []*peer.TransactionAction{{Payload: actionPayload}}})
	payload, _ := proto.Marshal(&common.Payload{Header: &common.Header{ChannelHeader: chdr}, Data: tx})
	return &common.Envelope{Payload: payload}
}

func chaincodeFailure(msg string) error {
	st, _ := status.New(codes.Aborted, "failed to endorse transaction, see attached details for more info").WithDetails(
		&gwproto.ErrorDetail{Address: "peer0.buyer.anubandh:7051", MspId: "BuyerMSP", Message: "chaincode response 500, " + msg})
	return st.Err()
}

func (g *fakeGateway) Evaluate(_ context.Context, req *gwproto.EvaluateRequest) (*gwproto.EvaluateResponse, error) {
	_, _, _, args, _ := parseProposal(req.GetProposedTransaction())
	if len(args) > 1 && args[1] == "DL-20261020-0404" {
		st, _ := status.New(codes.Unknown, "evaluate call to endorser returned error").WithDetails(
			&gwproto.ErrorDetail{Message: "chaincode response 500, ERR_NOT_FOUND: deal DL-20261020-0404 not found"})
		return nil, st.Err()
	}
	return &gwproto.EvaluateResponse{Result: &peer.Response{Status: 200, Payload: []byte(`{"dealId":"` + args[1] + `","status":"FUNDED"}`)}}, nil
}

func (g *fakeGateway) Endorse(_ context.Context, req *gwproto.EndorseRequest) (*gwproto.EndorseResponse, error) {
	txID, channel, mspID, args, transient := parseProposal(req.GetProposedTransaction())
	g.mu.Lock()
	g.endorsed = append(g.endorsed, endorsed{fn: args[0], args: args[1:], mspID: mspID, channel: channel, transient: transient})
	g.commitFn[txID] = args[0]
	g.mu.Unlock()
	if args[0] == "PostGoodsReceipt" && len(args) > 4 && args[4] == "warehouse:DUP" {
		return nil, chaincodeFailure("ERR_DUPLICATE: idem key warehouse:DUP already processed")
	}
	return &gwproto.EndorseResponse{PreparedTransaction: preparedEnvelope(channel, txID, []byte("ok:"+args[0]))}, nil
}

func (g *fakeGateway) Submit(_ context.Context, req *gwproto.SubmitRequest) (*gwproto.SubmitResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.submitCalls++
	if g.commitFn[req.GetTransactionId()] == "ExpireDeal" {
		return nil, status.Error(codes.Unavailable, "no orderer reachable")
	}
	return &gwproto.SubmitResponse{}, nil
}

func (g *fakeGateway) CommitStatus(_ context.Context, req *gwproto.SignedCommitStatusRequest) (*gwproto.CommitStatusResponse, error) {
	r := &gwproto.CommitStatusRequest{}
	_ = proto.Unmarshal(req.GetRequest(), r)
	g.mu.Lock()
	defer g.mu.Unlock()
	fn := g.commitFn[r.GetTransactionId()]
	if g.mvccLeft[fn] > 0 {
		g.mvccLeft[fn]--
		return &gwproto.CommitStatusResponse{Result: peer.TxValidationCode_MVCC_READ_CONFLICT, BlockNumber: 7}, nil
	}
	return &gwproto.CommitStatusResponse{Result: peer.TxValidationCode_VALID, BlockNumber: 9}, nil
}

func (g *fakeGateway) ChaincodeEvents(req *gwproto.SignedChaincodeEventsRequest, stream gwproto.Gateway_ChaincodeEventsServer) error {
	r := &gwproto.ChaincodeEventsRequest{}
	_ = proto.Unmarshal(req.GetRequest(), r)
	start := r.GetStartPosition().GetSpecified().GetNumber()
	for b := start; b < start+2; b++ {
		if err := stream.Send(&gwproto.ChaincodeEventsResponse{BlockNumber: b, Events: []*peer.ChaincodeEvent{
			{ChaincodeId: r.GetChaincodeId(), TxId: "tx-" + string(rune('a'+b)), EventName: model.EvLienConfirmed, Payload: []byte(`{"dealId":"DL-20261020-0001"}`)},
		}}); err != nil {
			return err
		}
	}
	<-stream.Context().Done()
	return nil
}

func writePEM(t *testing.T, dir, name, typ string, der []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func selfSigned(t *testing.T, cn string, isServer bool) (certDER []byte, key *ecdsa.PrivateKey) {
	t.Helper()
	key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign}
	if isServer {
		tmpl.IsCA, tmpl.DNSNames, tmpl.IPAddresses = true, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")}
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der, key
}

func startFakeGateway(t *testing.T) (*fakeGateway, string, string) {
	t.Helper()
	dir := t.TempDir()
	der, key := selfSigned(t, "localhost", true)
	caPath := writePEM(t, dir, "tlsca.pem", "CERTIFICATE", der)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewServerTLSFromCert(&cert)))
	fg := &fakeGateway{mvccLeft: map[string]int{}, commitFn: map[string]string{}}
	gwproto.RegisterGatewayServer(srv, fg)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return fg, lis.Addr().String(), caPath
}

func userIdentity(t *testing.T, userID string, org model.Org, role model.Role, mspID string) Identity {
	t.Helper()
	dir := t.TempDir()
	der, key := selfSigned(t, userID, false)
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	keyDir := filepath.Join(dir, "keystore") // MSP layout: a directory holding one generated key file
	_ = os.MkdirAll(keyDir, 0o700)
	writePEM(t, keyDir, "9f3c_sk", "PRIVATE KEY", pkcs8)
	return Identity{UserID: userID, Org: org, Role: role, MSPID: mspID, CertPath: writePEM(t, dir, "cert.pem", "CERTIFICATE", der), KeyPath: keyDir}
}

func TestGatewayClientAgainstFabricGatewayProtocol(t *testing.T) {
	fg, addr, ca := startFakeGateway(t)
	gc, err := NewGatewayClient(GatewayConfig{Channel: "anubandh-ch", Chaincode: "deal", Orgs: map[model.Org]OrgEndpoint{
		model.OrgBuyer: {MSPID: "BuyerMSP", PeerEndpoint: "dns:///" + addr, HostOverride: "localhost", TLSCACertPath: ca},
		model.OrgBank:  {MSPID: "BankMSP", PeerEndpoint: addr, HostOverride: "localhost", TLSCACertPath: ca},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gc.Close() }()
	buyer := userIdentity(t, "buyer-warehouse", model.OrgBuyer, model.RoleWarehouse, "")
	bankID := userIdentity(t, "bank-officer", model.OrgBank, model.RoleBankOfficer, "BankMSP")
	sub := NewSubmitter(gc, RetryPolicy{MaxAttempts: 5, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}, nil, nil)
	ctx := context.Background()

	// Submit with transient data, signed by the buyer's identity (MSP id taken from the org config).
	res, err := sub.Submit(WithIdentity(ctx, buyer), SubmitRequest{Fn: "CreateDeal", Args: []string{`{"dealId":"DL-20261020-0001"}`},
		Transient: map[string][]byte{"lines": []byte(`[{"lineNo":1}]`)}})
	if err != nil || string(res.Payload) != "ok:CreateDeal" || res.TxID == "" || res.BlockNumber != 9 {
		t.Fatalf("submit: %+v %v", res, err)
	}
	fg.mu.Lock()
	e := fg.endorsed[0]
	fg.mu.Unlock()
	if e.fn != "CreateDeal" || e.mspID != "BuyerMSP" || e.channel != "anubandh-ch" || string(e.transient["lines"]) != `[{"lineNo":1}]` {
		t.Fatalf("proposal on the wire: %+v", e)
	}

	// MVCC_READ_CONFLICT at commit -> re-endorsed and resubmitted with a new txId, then succeeds.
	fg.mu.Lock()
	fg.mvccLeft["ConfirmSettlement"] = 2
	fg.mu.Unlock()
	res, err = sub.Submit(WithIdentity(ctx, bankID), SubmitRequest{Fn: "ConfirmSettlement", Args: []string{"DL-20261020-0001", "1", "UTR-1", "NEFT"}})
	if err != nil || res.Attempts != 3 {
		t.Fatalf("MVCC retry through the real SDK: attempts %d err %v", res.Attempts, err)
	}
	fg.mu.Lock()
	if last := fg.endorsed[len(fg.endorsed)-1]; last.mspID != "BankMSP" {
		t.Fatalf("bank call signed by %s", last.mspID)
	}
	fg.mu.Unlock()

	// Chaincode business error relayed in the gRPC ErrorDetail -> ERR_DUPLICATE, not retried.
	before := len(fg.endorsed)
	_, err = sub.Submit(WithIdentity(ctx, buyer), SubmitRequest{Fn: "PostGoodsReceipt", Args: []string{"DL-20261020-0001", "1", "[]", "warehouse:DUP"}})
	if !apperr.Is(err, apperr.ErrDuplicate) || len(fg.endorsed) != before+1 {
		t.Fatalf("duplicate: %v (endorse calls %d)", err, len(fg.endorsed)-before)
	}

	// Orderer unreachable after endorsement: outcome ambiguous, reported, never auto-retried.
	before = len(fg.endorsed)
	_, err = sub.Submit(WithIdentity(ctx, buyer), SubmitRequest{Fn: "ExpireDeal", Args: []string{"DL-20261020-0001"}})
	ae, _ := apperr.As(err)
	if ae == nil || ae.Code != apperr.ErrInternal || ae.Details["ambiguous"] != true || ae.TxID == "" || len(fg.endorsed) != before+1 {
		t.Fatalf("ambiguous submit: %+v", ae)
	}

	// Evaluate: result and a relayed ERR_NOT_FOUND.
	out, err := sub.Evaluate(WithIdentity(ctx, buyer), "GetDeal", "DL-20261020-0001")
	if err != nil || !strings.Contains(string(out), `"FUNDED"`) {
		t.Fatalf("evaluate: %s %v", out, err)
	}
	if _, err := sub.Evaluate(WithIdentity(ctx, buyer), "GetDeal", "DL-20261020-0404"); !apperr.Is(err, apperr.ErrNotFound) {
		t.Fatalf("evaluate not found: %v", err)
	}

	// Chaincode events from a start block (listener resume).
	ectx, cancel := context.WithCancel(ctx)
	ch, err := gc.ChaincodeEvents(ectx, buyer, 5)
	if err != nil {
		t.Fatal(err)
	}
	for want := uint64(5); want < 7; want++ {
		select {
		case ev := <-ch:
			if ev.BlockNumber != want || ev.EventName != model.EvLienConfirmed || ev.ChaincodeName != "deal" {
				t.Fatalf("event: %+v", ev)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no chaincode event")
		}
	}
	cancel()

	// Misconfiguration fails clearly instead of hanging.
	if _, err := sub.Submit(WithIdentity(ctx, Identity{UserID: "x", Org: model.OrgAuditor}), SubmitRequest{Fn: "GetDeal"}); err == nil {
		t.Fatal("an org without a peer endpoint must fail")
	}
	if _, err := NewGatewayClient(GatewayConfig{Channel: "c", Chaincode: "d", Orgs: map[model.Org]OrgEndpoint{model.OrgBuyer: {MSPID: "M"}}}); err == nil {
		t.Fatal("incomplete org endpoint must be rejected at startup")
	}
}
