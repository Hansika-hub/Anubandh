// Package fabric talks to the Drunix network through the Hyperledger Fabric Gateway SDK.
//
// Drunix is "an enhanced fork of Hyperledger Fabric", backwards compatible with HLF v2.5.x, whose
// Lite Peers endorse and delegate CommitStatus to Committing Peers (github.com/npci/drunix,
// docs/drunix-arch.md). Its own Go sample application uses github.com/hyperledger/fabric-gateway,
// so this package uses that SDK and nothing Drunix-specific.
package fabric

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hyperledger/fabric-gateway/pkg/client"
	"github.com/hyperledger/fabric-gateway/pkg/hash"
	"github.com/hyperledger/fabric-gateway/pkg/identity"
	gwproto "github.com/hyperledger/fabric-protos-go-apiv2/gateway"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
	"github.com/Hansika-hub/anubandh/gateway/internal/model"
)

// ChaincodeEvent is the SDK's chaincode event type (listener.handle takes it, BUILD.md §8.2).
type ChaincodeEvent = client.ChaincodeEvent

// Identity is the enrolled chain identity a request is signed with. Org and Role mirror the
// MSP id and the `role` certificate attribute that the chaincode reads via GetClientIdentity.
type Identity struct {
	UserID   string
	Org      model.Org
	Role     model.Role
	PartyID  string // BUYER-001 / SUP-007 for party users; empty for bank, auditor and service users
	MSPID    string
	CertPath string
	KeyPath  string
}

type identityKey struct{}

// WithIdentity returns a context whose chain calls are signed as id.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom returns the identity carried by ctx.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok && id.UserID != ""
}

// SubmitRequest is one chaincode transaction invocation.
type SubmitRequest struct {
	Fn        string
	Args      []string
	Transient map[string][]byte
}

// SubmitResult is the outcome of a committed transaction.
type SubmitResult struct {
	Payload     []byte
	TxID        string
	BlockNumber uint64
	Attempts    int
}

// Client is the chain access interface. GatewayClient talks to real Drunix/Fabric peers;
// MockClient (FABRIC_MOCK=true) is an in-memory stand-in with the same behaviour contract.
type Client interface {
	// SubmitOnce endorses, orders and waits for the commit of one transaction (no retry).
	SubmitOnce(ctx context.Context, id Identity, req SubmitRequest) (SubmitResult, error)
	// Evaluate runs a read-only query on one peer.
	Evaluate(ctx context.Context, id Identity, fn string, args ...string) ([]byte, error)
	// ChaincodeEvents streams chaincode events from startBlock (inclusive). Only events of
	// valid transactions are delivered; the channel closes when ctx ends or the stream breaks.
	ChaincodeEvents(ctx context.Context, id Identity, startBlock uint64) (<-chan *ChaincodeEvent, error)
	// Mode is "mock" or "drunix".
	Mode() string
	Close() error
}

// OrgEndpoint is how one organisation reaches its own gateway peer.
type OrgEndpoint struct {
	MSPID         string `json:"mspId"`
	PeerEndpoint  string `json:"peerEndpoint"`
	HostOverride  string `json:"hostOverride"`
	TLSCACertPath string `json:"tlsCACert"`
}

// GatewayConfig configures GatewayClient.
type GatewayConfig struct {
	Channel             string
	Chaincode           string
	Orgs                map[model.Org]OrgEndpoint
	EvaluateTimeout     time.Duration
	EndorseTimeout      time.Duration
	SubmitTimeout       time.Duration
	CommitStatusTimeout time.Duration
}

// GatewayClient is the real chain client. It keeps one gRPC connection per org peer and one
// Gateway per user identity, as recommended by the fabric-gateway SDK.
type GatewayClient struct {
	cfg      GatewayConfig
	mu       sync.Mutex
	conns    map[model.Org]*grpc.ClientConn
	gateways map[string]*client.Gateway
}

// NewGatewayClient validates cfg. Connections are opened lazily on first use per org.
func NewGatewayClient(cfg GatewayConfig) (*GatewayClient, error) {
	if cfg.Channel == "" || cfg.Chaincode == "" {
		return nil, errors.New("fabric: channel and chaincode name are required")
	}
	if len(cfg.Orgs) == 0 {
		return nil, errors.New("fabric: at least one org endpoint is required (users file \"orgs\")")
	}
	for org, ep := range cfg.Orgs {
		if ep.MSPID == "" || ep.PeerEndpoint == "" || ep.TLSCACertPath == "" {
			return nil, fmt.Errorf("fabric: org %s needs mspId, peerEndpoint and tlsCACert", org)
		}
	}
	setDefault(&cfg.EvaluateTimeout, 5*time.Second)
	setDefault(&cfg.EndorseTimeout, 15*time.Second)
	setDefault(&cfg.SubmitTimeout, 5*time.Second)
	setDefault(&cfg.CommitStatusTimeout, time.Minute)
	return &GatewayClient{
		cfg:      cfg,
		conns:    map[model.Org]*grpc.ClientConn{},
		gateways: map[string]*client.Gateway{},
	}, nil
}

func setDefault(d *time.Duration, v time.Duration) {
	if *d <= 0 {
		*d = v
	}
}

// Mode implements Client.
func (g *GatewayClient) Mode() string { return "drunix" }

// Close implements Client.
func (g *GatewayClient) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, gw := range g.gateways {
		_ = gw.Close()
		delete(g.gateways, k)
	}
	errs := make([]error, 0, len(g.conns))
	for k, c := range g.conns {
		errs = append(errs, c.Close())
		delete(g.conns, k)
	}
	return errors.Join(errs...)
}

func (g *GatewayClient) conn(org model.Org) (*grpc.ClientConn, error) {
	if c, ok := g.conns[org]; ok {
		return c, nil
	}
	ep, ok := g.cfg.Orgs[org]
	if !ok {
		return nil, fmt.Errorf("fabric: no peer endpoint configured for org %s", org)
	}
	pemBytes, err := os.ReadFile(ep.TLSCACertPath) //nolint:gosec // operator-supplied crypto path from the users file
	if err != nil {
		return nil, fmt.Errorf("fabric: read TLS CA cert for %s: %w", org, err)
	}
	cert, err := identity.CertificateFromPEM(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("fabric: parse TLS CA cert for %s: %w", org, err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	c, err := grpc.NewClient(ep.PeerEndpoint, grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(pool, ep.HostOverride)))
	if err != nil {
		return nil, fmt.Errorf("fabric: dial %s: %w", ep.PeerEndpoint, err)
	}
	g.conns[org] = c
	return c, nil
}

func (g *GatewayClient) gateway(id Identity) (*client.Gateway, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if gw, ok := g.gateways[id.UserID]; ok {
		return gw, nil
	}
	conn, err := g.conn(id.Org)
	if err != nil {
		return nil, err
	}
	mspID := id.MSPID
	if mspID == "" {
		mspID = g.cfg.Orgs[id.Org].MSPID
	}
	certPEM, err := readPEM(id.CertPath)
	if err != nil {
		return nil, fmt.Errorf("fabric: certificate for %s: %w", id.UserID, err)
	}
	cert, err := identity.CertificateFromPEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("fabric: parse certificate for %s: %w", id.UserID, err)
	}
	x509ID, err := identity.NewX509Identity(mspID, cert)
	if err != nil {
		return nil, err
	}
	keyPEM, err := readPEM(id.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("fabric: private key for %s: %w", id.UserID, err)
	}
	key, err := identity.PrivateKeyFromPEM(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("fabric: parse private key for %s: %w", id.UserID, err)
	}
	sign, err := identity.NewPrivateKeySign(key)
	if err != nil {
		return nil, err
	}
	gw, err := client.Connect(x509ID,
		client.WithSign(sign),
		client.WithHash(hash.SHA256),
		client.WithClientConnection(conn),
		client.WithEvaluateTimeout(g.cfg.EvaluateTimeout),
		client.WithEndorseTimeout(g.cfg.EndorseTimeout),
		client.WithSubmitTimeout(g.cfg.SubmitTimeout),
		client.WithCommitStatusTimeout(g.cfg.CommitStatusTimeout),
	)
	if err != nil {
		return nil, fmt.Errorf("fabric: connect gateway for %s: %w", id.UserID, err)
	}
	g.gateways[id.UserID] = gw
	return gw, nil
}

// readPEM reads a PEM file, or the first file of a directory (Fabric MSP keystore/signcerts
// directories hold one file with a generated name).
func readPEM(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("path not configured")
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return os.ReadFile(path) //nolint:gosec // operator-supplied crypto path from the users file
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no file in %s", path)
	}
	sort.Strings(names)
	return os.ReadFile(filepath.Join(path, names[0])) //nolint:gosec // inside the configured MSP directory
}

func (g *GatewayClient) contract(id Identity) (*client.Network, *client.Contract, error) {
	gw, err := g.gateway(id)
	if err != nil {
		return nil, nil, apperr.Wrap(apperr.ErrInternal, err, "chain identity unavailable").WithDetail("unavailable", true)
	}
	network := gw.GetNetwork(g.cfg.Channel)
	return network, network.GetContract(g.cfg.Chaincode), nil
}

// SubmitOnce implements Client: endorse -> submit to orderer -> wait for commit status.
func (g *GatewayClient) SubmitOnce(ctx context.Context, id Identity, req SubmitRequest) (SubmitResult, error) {
	_, contract, err := g.contract(id)
	if err != nil {
		return SubmitResult{}, err
	}
	opts := []client.ProposalOption{client.WithArguments(req.Args...)}
	if len(req.Transient) > 0 {
		opts = append(opts, client.WithTransient(req.Transient))
	}
	proposal, err := contract.NewProposal(req.Fn, opts...)
	if err != nil {
		return SubmitResult{}, apperr.Wrap(apperr.ErrBadRequest, err, "build proposal")
	}
	txID := proposal.TransactionID()
	tx, err := proposal.EndorseWithContext(ctx)
	if err != nil {
		return SubmitResult{TxID: txID}, Classify(err, txID)
	}
	commit, err := tx.SubmitWithContext(ctx)
	if err != nil {
		return SubmitResult{TxID: txID}, Classify(err, txID)
	}
	st, err := commit.StatusWithContext(ctx)
	if err != nil {
		return SubmitResult{TxID: txID}, Classify(err, txID)
	}
	if !st.Successful {
		return SubmitResult{TxID: txID, BlockNumber: st.BlockNumber}, classifyValidationCode(st.Code, txID)
	}
	return SubmitResult{Payload: tx.Result(), TxID: txID, BlockNumber: st.BlockNumber}, nil
}

// Evaluate implements Client.
func (g *GatewayClient) Evaluate(ctx context.Context, id Identity, fn string, args ...string) ([]byte, error) {
	_, contract, err := g.contract(id)
	if err != nil {
		return nil, err
	}
	out, err := contract.EvaluateWithContext(ctx, fn, client.WithArguments(args...))
	if err != nil {
		return nil, Classify(err, "")
	}
	return out, nil
}

// ChaincodeEvents implements Client.
func (g *GatewayClient) ChaincodeEvents(ctx context.Context, id Identity, startBlock uint64) (<-chan *ChaincodeEvent, error) {
	network, _, err := g.contract(id)
	if err != nil {
		return nil, err
	}
	ch, err := network.ChaincodeEvents(ctx, g.cfg.Chaincode, client.WithStartBlock(startBlock))
	if err != nil {
		return nil, Classify(err, "")
	}
	return ch, nil
}

// Classify converts an SDK / gRPC error into an *apperr.Error.
//
// Retry safety (apperr.Error.Retryable) is decided by where the failure happened:
//   - endorsement failed for a transport reason        -> nothing was ordered      -> retryable
//   - commit status MVCC_READ_CONFLICT / PHANTOM_READ   -> tx invalidated, no write  -> retryable
//   - submit to orderer failed / commit status unknown  -> outcome ambiguous         -> NOT retryable;
//     the caller must retry with the same idempotency key so the chaincode can dedupe.
func Classify(err error, txID string) error {
	if err == nil {
		return nil
	}
	if ae, ok := apperr.As(err); ok {
		return ae
	}
	text := errorText(err)
	if code, ok := apperr.ExtractCode(text); ok {
		return &apperr.Error{Code: code, Message: chaincodeMessage(text, code), TxID: txID, Retryable: code == apperr.ErrMvccConflict, Err: err}
	}

	var commitErr *client.CommitError
	if errors.As(err, &commitErr) {
		return classifyValidationCode(commitErr.Code, txID)
	}
	var endorseErr *client.EndorseError
	var submitErr *client.SubmitError
	var statusErr *client.CommitStatusError
	switch {
	case errors.As(err, &endorseErr):
		return fromGRPC(err, txID, true)
	case errors.As(err, &submitErr), errors.As(err, &statusErr):
		return &apperr.Error{
			Code: apperr.ErrInternal, TxID: txID, Err: err,
			Message: "transaction outcome unknown; retry with the same idempotency key",
			Details: map[string]any{"ambiguous": true, "txId": txID},
		}
	}
	return fromGRPC(err, txID, false)
}

func fromGRPC(err error, txID string, retrySafe bool) error {
	st, _ := status.FromError(err)
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted:
		return &apperr.Error{Code: apperr.ErrInternal, Message: "chain temporarily unavailable: " + st.Message(), TxID: txID,
			Retryable: retrySafe, Err: err, Details: map[string]any{"unavailable": true}}
	case codes.PermissionDenied:
		return &apperr.Error{Code: apperr.ErrForbiddenRole, Message: st.Message(), TxID: txID, Err: err}
	case codes.InvalidArgument:
		return &apperr.Error{Code: apperr.ErrBadRequest, Message: st.Message(), TxID: txID, Err: err}
	case codes.NotFound:
		return &apperr.Error{Code: apperr.ErrNotFound, Message: st.Message(), TxID: txID, Err: err}
	case codes.Canceled:
		return &apperr.Error{Code: apperr.ErrInternal, Message: "request cancelled", TxID: txID, Err: err}
	}
	return &apperr.Error{Code: apperr.ErrInternal, Message: st.Message(), TxID: txID, Err: err}
}

func classifyValidationCode(code peer.TxValidationCode, txID string) error {
	msg := fmt.Sprintf("transaction %s invalidated: %s", txID, code.String())
	switch code {
	case peer.TxValidationCode_MVCC_READ_CONFLICT, peer.TxValidationCode_PHANTOM_READ_CONFLICT:
		return &apperr.Error{Code: apperr.ErrMvccConflict, Message: msg, TxID: txID, Retryable: true}
	case peer.TxValidationCode_ENDORSEMENT_POLICY_FAILURE:
		return &apperr.Error{Code: apperr.ErrPolicyNotSatisfied, Message: msg, TxID: txID}
	case peer.TxValidationCode_DUPLICATE_TXID:
		return &apperr.Error{Code: apperr.ErrDuplicate, Message: msg, TxID: txID}
	}
	return &apperr.Error{Code: apperr.ErrInternal, Message: msg, TxID: txID}
}

// errorText concatenates the gRPC status message and every per-peer ErrorDetail message, which is
// where the chaincode's own error text (and its ERR_* code) is relayed.
func errorText(err error) string {
	parts := []string{err.Error()}
	if st, ok := status.FromError(err); ok {
		for _, d := range st.Details() {
			if ed, ok := d.(*gwproto.ErrorDetail); ok {
				parts = append(parts, ed.GetMessage())
			}
		}
	}
	return strings.Join(parts, " | ")
}

// chaincodeMessage trims the peer's wrapping so the API returns the chaincode's own sentence.
func chaincodeMessage(text string, code apperr.Code) string {
	i := strings.Index(text, string(code))
	if i < 0 {
		return text
	}
	msg := text[i:]
	if j := strings.Index(msg, " | "); j >= 0 {
		msg = msg[:j]
	}
	msg = strings.TrimSpace(strings.TrimPrefix(msg, string(code)))
	msg = strings.TrimSpace(strings.TrimLeft(msg, ":"))
	if msg == "" {
		return string(code)
	}
	return msg
}
