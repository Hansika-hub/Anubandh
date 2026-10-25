package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Hansika-hub/anubandh/ingest/internal/events"
	"github.com/Hansika-hub/anubandh/ingest/pipeline"
)

type errorBody struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr uses the same error envelope as the gateway: {"error":{"code","message","details"}}.
func writeErr(w http.ResponseWriter, status int, code, msg string, details map[string]any) {
	var b errorBody
	b.Error.Code, b.Error.Message = code, msg
	if details == nil {
		details = map[string]any{}
	}
	b.Error.Details = details
	writeJSON(w, status, b)
}

// endpoint binds a webhook path to its event type and the sources allowed to call it.
type endpoint struct {
	path    string
	typ     events.EventType
	sources map[string]bool
}

var endpoints = []endpoint{
	{"/ingest/v1/events/goods-receipt", events.GoodsReceipt, set("warehouse", "erp")},
	{"/ingest/v1/events/quality-check", events.QualityCheck, set("warehouse", "erp")},
	{"/ingest/v1/events/invoice", events.Invoice, set("gst")},
	{"/ingest/v1/events/bank-lien", events.BankLien, set("bank")},
}

func set(v ...string) map[string]bool {
	m := map[string]bool{}
	for _, s := range v {
		m[s] = true
	}
	return m
}

type server struct {
	prod          pipeline.Producer
	dlq           *pipeline.DLQ
	secrets       map[string][]byte
	limiter       *rateLimiter
	operatorToken string
	log           *slog.Logger
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	chain := func(h http.Handler) http.Handler {
		return VerifyHMACBySource(s.secrets)(s.limiter.RateLimit(h))
	}
	for _, ep := range endpoints {
		mux.Handle("POST "+ep.path, chain(s.eventHandler(ep)))
	}
	mux.HandleFunc("GET /ingest/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /ingest/v1/dlq", s.operator(http.HandlerFunc(s.listDLQ)))
	mux.Handle("POST /ingest/v1/dlq/{id}/replay", s.operator(http.HandlerFunc(s.replayDLQ)))
	return recoverer(s.log, mux)
}

func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic in handler", "msg", "panic", "panic", rec)
				writeErr(w, http.StatusInternalServerError, "ERR_INTERNAL", "internal error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *server) eventHandler(ep endpoint) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		src, _ := r.Context().Value(ctxSource).(string)
		body, _ := r.Context().Value(ctxBody).([]byte)

		if !ep.sources[src] {
			writeErr(w, http.StatusForbidden, "ERR_FORBIDDEN_ROLE", "source "+src+" may not post this event type", nil)
			return
		}
		var ev pipeline.InboundEvent
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&ev); err != nil || dec.More() {
			writeErr(w, http.StatusBadRequest, "ERR_BAD_REQUEST", "body is not a valid event envelope", nil)
			return
		}
		if ev.EventType != ep.typ {
			writeErr(w, http.StatusBadRequest, "ERR_BAD_REQUEST", "eventType must be "+string(ep.typ)+" for this endpoint", nil)
			return
		}
		if err := ev.Validate(); err != nil {
			var ve *events.ValidationError
			msg := "invalid event"
			if errors.As(err, &ve) {
				msg = ve.Msg
			}
			writeErr(w, http.StatusBadRequest, "ERR_BAD_REQUEST", msg, nil)
			return
		}
		// The idempotency key is namespaced by source so one source can never collide with, or
		// pre-empt, another source's key in processed_keys.
		if !strings.HasPrefix(ev.IdemKey, src+":") {
			writeErr(w, http.StatusBadRequest, "ERR_BAD_REQUEST", "idemKey must start with \""+src+":\"", nil)
			return
		}
		if h := r.Header.Get("X-Idempotency-Key"); h != ev.IdemKey {
			writeErr(w, http.StatusBadRequest, "ERR_BAD_REQUEST", "X-Idempotency-Key header must equal the body idemKey", nil)
			return
		}
		val, _ := json.Marshal(ev)
		err := s.prod.Publish(r.Context(), pipeline.Message{Topic: pipeline.TopicInbound, Key: ev.DealID, Value: val,
			Headers: map[string]string{pipeline.HdrIdemKey: ev.IdemKey}})
		if err != nil {
			s.log.Error("publish failed", "msg", "publish failed", "dealId", ev.DealID, "trancheNo", ev.TrancheNo, "idemKey", ev.IdemKey, "err", err.Error())
			w.Header().Set("Retry-After", "1")
			writeErr(w, http.StatusServiceUnavailable, "ERR_INTERNAL", "event queue unavailable; retry", nil)
			return
		}
		s.log.Info("event accepted", "msg", "event accepted", "dealId", ev.DealID, "trancheNo", ev.TrancheNo, "idemKey", ev.IdemKey, "source", src)
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "idemKey": ev.IdemKey})
	})
}

// operator guards the DLQ endpoints with X-Operator-Token. With no token configured they are
// disabled rather than left open.
func (s *server) operator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.operatorToken == "" {
			writeErr(w, http.StatusForbidden, "ERR_FORBIDDEN_ROLE", "operator endpoints are disabled: INGEST_OPERATOR_TOKEN is not set", nil)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Operator-Token")), []byte(s.operatorToken)) != 1 {
			writeErr(w, http.StatusUnauthorized, "ERR_UNAUTHORIZED", "invalid operator token", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) listDLQ(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeErr(w, http.StatusBadRequest, "ERR_BAD_REQUEST", "limit must be 1..1000", nil)
			return
		}
		limit = n
	}
	incl := r.URL.Query().Get("includeReplayed") == "true"
	entries, err := s.dlq.List(r.Context(), limit, incl)
	if err != nil {
		s.log.Error("dlq list failed", "msg", "dlq list failed", "err", err.Error())
		writeErr(w, http.StatusServiceUnavailable, "ERR_INTERNAL", "dead-letter queue unavailable", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries, "count": len(entries)})
}

func (s *server) replayDLQ(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.dlq.Replay(r.Context(), id)
	switch {
	case err == nil:
		s.log.Info("dlq entry replayed", "msg", "dlq entry replayed", "dlqId", id)
		writeJSON(w, http.StatusAccepted, map[string]any{"replayed": true, "id": id})
	case errors.Is(err, pipeline.ErrDLQNotFound):
		writeErr(w, http.StatusNotFound, "ERR_NOT_FOUND", "no such dead-letter entry", nil)
	case errors.Is(err, pipeline.ErrDLQReplayed):
		writeErr(w, http.StatusConflict, "ERR_DUPLICATE", "entry was already replayed", nil)
	default:
		s.log.Error("dlq replay failed", "msg", "dlq replay failed", "dlqId", id, "err", err.Error())
		writeErr(w, http.StatusServiceUnavailable, "ERR_INTERNAL", "dead-letter queue unavailable", nil)
	}
}
