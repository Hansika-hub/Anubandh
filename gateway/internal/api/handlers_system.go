package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
	"github.com/Hansika-hub/anubandh/gateway/internal/fabric"
	"github.com/Hansika-hub/anubandh/gateway/internal/model"
	"github.com/Hansika-hub/anubandh/gateway/internal/store"
)

// ---- GET /payments/outbox ----

func (h *Handlers) outbox(w http.ResponseWriter, r *http.Request) {
	if err := requireOrg(principalFrom(r.Context()), model.OrgBank, model.OrgBuyer); err != nil {
		writeErr(w, h.log, err)
		return
	}
	limit, err := queryInt(r, "limit", 100, 1, 500)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1<<30)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	status := strings.ToUpper(r.URL.Query().Get("status"))
	switch status {
	case "", store.OutboxPending, store.OutboxSent, store.OutboxConfirmed, store.OutboxFailed:
	default:
		writeErr(w, h.log, errBadRequest("status must be PENDING, SENT, CONFIRMED or FAILED"))
		return
	}
	items, total, err := store.ListOutbox(r.Context(), h.store.Pool, status, r.URL.Query().Get("dealId"), limit, offset)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	writeList(w, items, total)
}

// ---- GET /events/stream (Server-Sent Events) ----

// maxSSESubscribers bounds open streams so idle connections cannot exhaust the gateway.
const maxSSESubscribers = 500

func (h *Handlers) eventStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, h.log, apperr.New(apperr.ErrInternal, "streaming unsupported"))
		return
	}
	if h.hub.Subscribers() >= maxSSESubscribers {
		writeErr(w, h.log, apperr.New(apperr.ErrRateLimited, "too many live event streams; retry later"))
		return
	}
	dealID := r.URL.Query().Get("dealId")
	var last int64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		last, _ = strconv.ParseInt(v, 10, 64)
	} else if v := r.URL.Query().Get("lastEventId"); v != "" {
		last, _ = strconv.ParseInt(v, 10, 64)
	}
	if last < 0 {
		last = 0
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{}) //nolint:bodyclose // server side; disables the write deadline for SSE
	sub, cancel := h.hub.Subscribe(dealID)
	defer cancel()

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-cache, no-transform")
	hdr.Set("Connection", "keep-alive")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, "retry: 3000\n: connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	// Replay what the client missed (subscribe first, so nothing falls in the gap).
	if last > 0 {
		for {
			evs, err := store.EventsAfter(r.Context(), h.store.Pool, last, dealID, 500)
			if err != nil || len(evs) == 0 {
				break
			}
			for _, e := range evs {
				if writeSSE(w, e.ID, e.EventName, mustJSON(e)) != nil {
					return
				}
				last = e.ID
			}
			flusher.Flush()
			if len(evs) < 500 {
				break
			}
		}
	}
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return // client went away
			}
			flusher.Flush()
		case m, ok := <-sub:
			if !ok {
				return
			}
			if m.ID <= last {
				continue
			}
			if writeSSE(w, m.ID, m.Event, m.Data) != nil {
				return
			}
			last = m.ID
			flusher.Flush()
		}
	}
}

// writeSSE writes one event. Not HTML: text/event-stream with nosniff; data is json.Marshal
// output (HTML-escaped) and the event name is stripped of newlines, so no field can be injected.
func writeSSE(w http.ResponseWriter, id int64, event string, data []byte) error {
	_, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, sanitizeSSE(event), strings.ReplaceAll(string(data), "\n", "")) //nolint:gosec // see above
	return err
}

func sanitizeSSE(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
}

// ---- POST /internal/v1/submit (ingest workers, BUILD.md §9.4) ----

// internalFns are the chaincode functions ingest may call, with their arity and idemKey position.
var internalFns = map[string]struct{ nargs, idemArg int }{
	"PostGoodsReceipt":   {4, 3},
	"RecordQualityCheck": {4, 3},
	"VerifyInvoice":      {3, 2},
	"ConfirmLien":        {3, -1},
}

func (h *Handlers) internalSubmit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Fn      string   `json:"fn"`
		Args    []string `json:"args"`
		IdemKey string   `json:"idemKey"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, h.log, err)
		return
	}
	spec, ok := internalFns[body.Fn]
	if !ok {
		writeErr(w, h.log, apperr.Newf(apperr.ErrForbiddenRole, "fn %q is not allowed on the internal submit endpoint", body.Fn))
		return
	}
	if len(body.Args) != spec.nargs {
		writeErr(w, h.log, apperr.Newf(apperr.ErrBadRequest, "%s expects %d args, got %d", body.Fn, spec.nargs, len(body.Args)))
		return
	}
	if !dealIDRe.MatchString(body.Args[0]) {
		writeErr(w, h.log, errBadRequest("args[0] must be a dealId (DL-YYYYMMDD-NNNN)"))
		return
	}
	if spec.idemArg >= 0 {
		arg := body.Args[spec.idemArg]
		if body.IdemKey != "" && body.IdemKey != arg {
			writeErr(w, h.log, apperr.Newf(apperr.ErrBadRequest, "idemKey %q does not match args[%d] %q", body.IdemKey, spec.idemArg, arg))
			return
		}
		if !model.IdemKeyRe.MatchString(arg) {
			writeErr(w, h.log, errBadRequest(idemKeyMsg))
			return
		}
	}
	id := h.registry.MustService(h.registry.Service.Internal[body.Fn])
	ctx, cancel := context.WithTimeout(fabric.WithIdentity(context.WithoutCancel(r.Context()), id), 60*time.Second)
	defer cancel()
	res, err := h.chain.Submit(ctx, fabric.SubmitRequest{Fn: body.Fn, Args: body.Args})
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	h.log.Info("internal submit", "fn", body.Fn, "dealId", body.Args[0], "idemKey", body.IdemKey, "txId", res.TxID)
	writeJSON(w, http.StatusOK, map[string]any{"txId": res.TxID})
}

// ---- POST /internal/v1/mock/clock (FABRIC_MOCK only) ----

func (h *Handlers) mockClock(w http.ResponseWriter, r *http.Request) {
	if h.mock == nil {
		writeErr(w, h.log, errNotFound("mock clock is only available with FABRIC_MOCK=true"))
		return
	}
	var body struct {
		AdvanceDays  int64 `json:"advanceDays"`
		AdvanceHours int64 `json:"advanceHours"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, h.log, err)
		return
	}
	if body.AdvanceDays < 0 || body.AdvanceHours < 0 || body.AdvanceDays > 400 || body.AdvanceHours > 24*400 {
		writeErr(w, h.log, errBadRequest("advanceDays (0..400) and advanceHours (0..9600) move time forward only"))
		return
	}
	d := time.Duration(body.AdvanceDays)*24*time.Hour + time.Duration(body.AdvanceHours)*time.Hour
	now, err := h.mock.Advance(d)
	if err != nil {
		writeErr(w, h.log, apperr.Wrap(apperr.ErrInternal, err, "persist mock clock"))
		return
	}
	ticked := 0
	if h.sched != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
		ticked, _ = h.sched.TickOnce(ctx)
		cancel()
	}
	h.log.Info("mock clock advanced", "byHours", d.Hours(), "now", now, "dealsTicked", ticked)
	writeJSON(w, http.StatusOK, map[string]any{"now": now.Truncate(time.Second), "offsetSeconds": int64(h.mock.Clock().Offset() / time.Second), "dealsTicked": ticked})
}

// ---- GET /health ----

func (h *Handlers) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	out := map[string]any{"svc": "gateway", "version": h.version, "now": h.clock.Now().Truncate(time.Second),
		"chain": map[string]any{"mode": h.chainMode, "channel": h.channel, "chaincode": h.chaincode}}
	status, code := "ok", http.StatusOK
	if err := h.store.Ping(ctx); err != nil {
		out["db"] = "down"
		status, code = "down", http.StatusServiceUnavailable
	} else {
		out["db"] = "ok"
		if counts, err := store.OutboxCounts(ctx, h.store.Pool); err == nil {
			out["outbox"] = counts
		}
	}
	if h.listener != nil {
		ls := h.listener.Status()
		out["listener"] = ls
		if !ls.Running && status == "ok" {
			status = "degraded"
		}
	}
	if h.sched != nil {
		out["scheduler"] = map[string]any{"lastRun": h.sched.LastRun()}
	}
	out["sseSubscribers"] = h.hub.Subscribers()
	out["status"] = status
	writeJSON(w, code, out)
}
