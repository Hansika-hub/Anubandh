// Package app wires the gateway components. cmd/gateway and the end-to-end tests share it, so
// the tests exercise exactly the production wiring.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Hansika-hub/anubandh/gateway/internal/api"
	"github.com/Hansika-hub/anubandh/gateway/internal/bank"
	"github.com/Hansika-hub/anubandh/gateway/internal/clock"
	"github.com/Hansika-hub/anubandh/gateway/internal/config"
	"github.com/Hansika-hub/anubandh/gateway/internal/events"
	"github.com/Hansika-hub/anubandh/gateway/internal/fabric"
	"github.com/Hansika-hub/anubandh/gateway/internal/listener"
	"github.com/Hansika-hub/anubandh/gateway/internal/projector"
	"github.com/Hansika-hub/anubandh/gateway/internal/release"
	"github.com/Hansika-hub/anubandh/gateway/internal/scheduler"
	"github.com/Hansika-hub/anubandh/gateway/internal/store"
)

// Overrides replace external dependencies (tests).
type Overrides struct {
	Bank        bank.Client   // default: HTTP client for SIM_BANK_URL
	Chain       fabric.Client // default: mock or Drunix client per FABRIC_MOCK
	RetryPolicy *fabric.RetryPolicy
}

// App is a wired gateway.
type App struct {
	Cfg       config.Config
	Log       *slog.Logger
	Store     *store.Store
	Registry  *config.Registry
	Chain     fabric.Client
	Mock      *fabric.MockClient
	Submitter *fabric.Submitter
	Hub       *events.Hub
	Projector *projector.Projector
	Adapter   *bank.Adapter
	Listener  *listener.Listener
	Scheduler *scheduler.Scheduler
	Handler   http.Handler
	Clock     *clock.Offset
	Version   string
}

// Build wires every component from cfg. The caller owns st (already migrated).
func Build(ctx context.Context, cfg config.Config, st *store.Store, log *slog.Logger, version string, ov Overrides) (*App, error) {
	reg, err := config.LoadRegistry(cfg.UsersFile, !cfg.FabricMock && ov.Chain == nil)
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, Log: log, Store: st, Registry: reg, Hub: events.NewHub(), Clock: clock.NewOffset(nil), Version: version}
	switch {
	case ov.Chain != nil:
		a.Chain = ov.Chain
		if m, ok := ov.Chain.(*fabric.MockClient); ok {
			a.Mock, a.Clock = m, m.Clock()
		}
	case cfg.FabricMock:
		a.Mock, err = fabric.NewMockClient(fabric.MockOptions{Clock: a.Clock, StatePath: cfg.MockStatePath})
		if err != nil {
			return nil, err
		}
		a.Chain = a.Mock
	default:
		a.Chain, err = fabric.NewGatewayClient(fabric.GatewayConfig{Channel: cfg.Channel, Chaincode: cfg.Chaincode, Orgs: reg.Orgs})
		if err != nil {
			return nil, err
		}
	}
	policy := fabric.DefaultRetryPolicy
	if ov.RetryPolicy != nil {
		policy = *ov.RetryPolicy
	}
	a.Submitter = fabric.NewSubmitter(a.Chain, policy, nil, log)
	fabric.SetDefault(a.Submitter)
	a.Projector = projector.New(st, a.Submitter, reg.MustService(reg.Service.Reader), log)

	bankClient := ov.Bank
	if bankClient == nil {
		bankClient = bank.NewHTTPClient(cfg.SimBankURL)
	}
	a.Adapter = bank.NewAdapter(st, bankClient, a.Submitter, reg.MustService(reg.Service.Bank), bank.Options{
		PollInterval: cfg.OutboxPoll, MaxAttempts: cfg.OutboxMaxAttempts, SettleOnAccept: cfg.BankSettleOnAccept,
		OnSettled: func(dealID string) {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			_ = a.Projector.Reconcile(rctx, dealID)
		},
	}, log)

	var onApproval func(string, int64)
	if cfg.AutoRelease {
		relID := reg.MustService(reg.Service.AutoRelease)
		onApproval = func(dealID string, n int64) {
			rctx, cancel := context.WithTimeout(fabric.WithIdentity(context.WithoutCancel(ctx), relID), 60*time.Second)
			defer cancel()
			res, err := release.Release(rctx, a.Submitter, dealID, n, "")
			switch {
			case err == nil:
				log.Info("tranche auto-released by listener", "dealId", dealID, "trancheNo", n, "txId", res.TxID)
			case release.AlreadyDone(err):
				log.Debug("auto-release: already released", "dealId", dealID, "trancheNo", n)
			default:
				log.Warn("auto-release by listener failed", "dealId", dealID, "trancheNo", n, "err", err)
			}
		}
	}
	a.Listener = listener.New(a.Chain, reg.MustService(reg.Service.Listener), st, a.Projector, a.Adapter, a.Hub, listener.Options{
		ListenerID: cfg.ListenerID, StartBlock: cfg.ListenerStartBlock, OnApprovalSigned: onApproval,
	}, log)
	a.Scheduler = scheduler.New(st, a.Submitter, reg.MustService(reg.Service.Scheduler), cfg.TickInterval, log)
	a.Handler = api.NewRouter(api.Deps{
		Store: st, Chain: a.Submitter, ChainMode: a.Chain.Mode(), Channel: cfg.Channel, Chaincode: cfg.Chaincode,
		Registry: reg, Projector: a.Projector, Hub: a.Hub, Clock: a.Clock, Mock: a.Mock, Scheduler: a.Scheduler,
		Listener: a.Listener, AutoRelease: cfg.AutoRelease, InternalToken: cfg.InternalToken, CORSOrigins: cfg.CORSOrigins,
		Version: version, Log: log,
	})
	return a, nil
}

// StartBackground runs the listener, outbox loop and scheduler until ctx ends.
func (a *App) StartBackground(ctx context.Context) {
	go a.Listener.Run(ctx)
	go a.Adapter.RunOutboxLoop(ctx)
	go a.Scheduler.Run(ctx)
}

// Serve runs the HTTP server until ctx ends, then shuts down gracefully.
func (a *App) Serve(ctx context.Context) error {
	srv := &http.Server{
		Addr: a.Cfg.Addr, Handler: a.Handler,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	a.Log.Info("gateway listening", "addr", a.Cfg.Addr, "chain", a.Chain.Mode(), "channel", a.Cfg.Channel, "chaincode", a.Cfg.Chaincode, "version", a.Version)
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
	a.Log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

// Close releases the chain client.
func (a *App) Close() { _ = a.Chain.Close() }
