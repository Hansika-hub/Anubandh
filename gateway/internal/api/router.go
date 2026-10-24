// Package api is the gateway REST API (BUILD.md §8.3) on :8080.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Hansika-hub/anubandh/gateway/internal/clock"
	"github.com/Hansika-hub/anubandh/gateway/internal/config"
	"github.com/Hansika-hub/anubandh/gateway/internal/events"
	"github.com/Hansika-hub/anubandh/gateway/internal/fabric"
	"github.com/Hansika-hub/anubandh/gateway/internal/listener"
	"github.com/Hansika-hub/anubandh/gateway/internal/projector"
	"github.com/Hansika-hub/anubandh/gateway/internal/store"
)

// Chain is the chain access the API needs (satisfied by *fabric.Submitter).
type Chain interface {
	Submit(ctx context.Context, req fabric.SubmitRequest) (fabric.SubmitResult, error)
	Evaluate(ctx context.Context, fn string, args ...string) ([]byte, error)
}

// Ticker runs one pass of time rules (satisfied by *scheduler.Scheduler).
type Ticker interface {
	TickOnce(ctx context.Context) (int, error)
	LastRun() *time.Time
}

// ListenerStatus reports listener health (satisfied by *listener.Listener).
type ListenerStatus interface {
	Status() listener.Status
}

// Deps wires the API.
type Deps struct {
	Store         *store.Store
	Chain         Chain
	ChainMode     string
	Channel       string
	Chaincode     string
	Registry      *config.Registry
	Projector     *projector.Projector
	Hub           *events.Hub
	Clock         clock.Clock
	Mock          *fabric.MockClient // nil unless FABRIC_MOCK=true
	Scheduler     Ticker
	Listener      ListenerStatus
	AutoRelease   bool
	InternalToken string
	CORSOrigins   []string
	Version       string
	Log           *slog.Logger
}

// Handlers holds the API dependencies.
type Handlers struct {
	store         *store.Store
	chain         Chain
	chainMode     string
	channel       string
	chaincode     string
	registry      *config.Registry
	proj          *projector.Projector
	hub           *events.Hub
	clock         clock.Clock
	mock          *fabric.MockClient
	sched         Ticker
	listener      ListenerStatus
	autoRelease   bool
	internalToken string
	version       string
	reader        fabric.Identity
	log           *slog.Logger
}

// NewRouter builds the HTTP handler.
func NewRouter(d Deps) http.Handler {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Clock == nil {
		d.Clock = clock.System{}
	}
	h := &Handlers{
		store: d.Store, chain: d.Chain, chainMode: d.ChainMode, channel: d.Channel, chaincode: d.Chaincode,
		registry: d.Registry, proj: d.Projector, hub: d.Hub, clock: d.Clock, mock: d.Mock, sched: d.Scheduler,
		listener: d.Listener, autoRelease: d.AutoRelease, internalToken: d.InternalToken, version: d.Version,
		reader: d.Registry.MustService(d.Registry.Service.Reader), log: d.Log,
	}
	r := chi.NewRouter()
	r.Use(requestLog(d.Log), cors(d.CORSOrigins))
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, h.log, errNotFound("no such endpoint"))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, h.log, errBadRequest("method not allowed"))
	})
	r.Get("/health", h.health)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", h.health)
		r.Group(func(r chi.Router) {
			r.Use(h.authenticate)
			r.Post("/deals", h.createDeal)
			r.Get("/deals", h.listDeals)
			r.Get("/deals/{dealId}", h.getDeal)
			r.Get("/deals/{dealId}/history", h.dealHistory)
			r.Post("/deals/{dealId}/lien", h.confirmLien)

			r.Post("/deals/{dealId}/tranches/{n}/ack", h.ackDelivery)
			r.Post("/deals/{dealId}/tranches/{n}/approve", h.approve)
			r.Post("/deals/{dealId}/tranches/{n}/release", h.releaseTranche)
			r.Post("/deals/{dealId}/tranches/{n}/objection", h.raiseObjection)
			r.Post("/deals/{dealId}/tranches/{n}/objection/resolve", h.resolveObjection)

			r.Post("/deals/{dealId}/held/{n}/{lineNo}/replacement", h.shipReplacement)
			r.Post("/deals/{dealId}/held/{n}/{lineNo}/credit-note", h.proposeCreditNote)
			r.Post("/deals/{dealId}/held/{n}/{lineNo}/credit-note/sign", h.signCreditNote)
			r.Post("/deals/{dealId}/held/{n}/{lineNo}/price-adjustment", h.proposePriceAdjustment)
			r.Post("/deals/{dealId}/held/{n}/{lineNo}/price-adjustment/sign", h.signPriceAdjustment)
			r.Post("/deals/{dealId}/held/{n}/{lineNo}/dispute", h.raiseDispute)
			r.Post("/deals/{dealId}/held/{n}/{lineNo}/dispute/resolve", h.resolveDispute)

			r.Get("/mse/clocks", h.mseClocks)
			r.Get("/mse/compliance-report", h.complianceReport)
			r.Get("/payments/outbox", h.outbox)
			r.Get("/events/stream", h.eventStream)
		})
	})

	// Internal endpoints: compose-network only (never route them through a public ingress).
	r.Route("/internal/v1", func(r chi.Router) {
		r.Use(h.internalAuth)
		r.Post("/submit", h.internalSubmit)
		r.Post("/mock/clock", h.mockClock)
	})
	return r
}
