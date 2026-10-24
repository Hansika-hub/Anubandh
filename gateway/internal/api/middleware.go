package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
	"github.com/Hansika-hub/anubandh/gateway/internal/fabric"
	"github.com/Hansika-hub/anubandh/gateway/internal/model"
)

// Principal is the caller as asserted by the web app (BUILD.md §2: X-Org + X-User).
// User is nil when only X-Org was sent (read-only access).
type Principal struct {
	Org  model.Org
	User *fabric.Identity
}

type principalKey struct{}

func principalFrom(ctx context.Context) Principal {
	p, _ := ctx.Value(principalKey{}).(Principal)
	return p
}

var reqIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// requestLog assigns a request id, recovers panics as ERR_INTERNAL and logs one line per request.
func requestLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-Id")
			if !reqIDRe.MatchString(id) {
				b := make([]byte, 8)
				_, _ = rand.Read(b)
				id = hex.EncodeToString(b)
			}
			w.Header().Set("X-Request-Id", id)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			defer func() {
				if v := recover(); v != nil {
					log.Error("panic", "reqId", id, "panic", v, "stack", string(debug.Stack()))
					writeErr(rec, log, apperr.New(apperr.ErrInternal, "internal error"))
				}
				lvl := slog.LevelInfo
				if r.URL.Path == "/health" || r.URL.Path == "/api/v1/health" {
					lvl = slog.LevelDebug
				}
				log.Log(r.Context(), lvl, "http", "reqId", id, "method", r.Method, "path", r.URL.Path, "status", rec.status,
					"durMs", time.Since(start).Milliseconds(), "org", r.Header.Get("X-Org"), "user", r.Header.Get("X-User"))
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// cors allows the configured web origins (the dashboard on :3000 calls the gateway on :8080).
func cors(origins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (slices.Contains(origins, origin) || slices.Contains(origins, "*")) {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
				h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type, X-Org, X-User, X-Idempotency-Key, X-Request-Id, Last-Event-ID")
				h.Set("Access-Control-Expose-Headers", "X-Request-Id, Retry-After")
				h.Set("Access-Control-Max-Age", "600")
			}
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// authenticate resolves X-Org / X-User. EventSource cannot set headers, so the SSE route also
// accepts ?org= and ?user=.
func (h *Handlers) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		org, user := r.Header.Get("X-Org"), r.Header.Get("X-User")
		if org == "" && strings.HasSuffix(r.URL.Path, "/events/stream") {
			org, user = r.URL.Query().Get("org"), r.URL.Query().Get("user")
		}
		o := model.Org(strings.ToUpper(strings.TrimSpace(org)))
		if !o.Valid() {
			writeErr(w, h.log, apperr.New(apperr.ErrUnauthorized, "X-Org header must be BUYER, SUPPLIER, BANK or AUDITOR"))
			return
		}
		p := Principal{Org: o}
		if user = strings.TrimSpace(user); user != "" {
			id, ok := h.registry.User(user)
			if !ok {
				writeErr(w, h.log, apperr.Newf(apperr.ErrUnauthorized, "unknown user %q", user))
				return
			}
			if id.Org != o {
				writeErr(w, h.log, apperr.Newf(apperr.ErrForbiddenRole, "user %s belongs to %s, not %s", user, id.Org, o))
				return
			}
			p.User = &id
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

// requireUser returns the caller's chain identity if they are a known user of one of orgs.
func requireUser(p Principal, orgs ...model.Org) (fabric.Identity, error) {
	if p.User == nil {
		return fabric.Identity{}, apperr.New(apperr.ErrUnauthorized, "X-User header is required for this action")
	}
	if !slices.Contains(orgs, p.Org) {
		names := make([]string, len(orgs))
		for i, o := range orgs {
			names[i] = string(o)
		}
		return fabric.Identity{}, apperr.Newf(apperr.ErrForbiddenRole, "this action requires org %s", strings.Join(names, " or "))
	}
	return *p.User, nil
}

func requireOrg(p Principal, orgs ...model.Org) error {
	if !slices.Contains(orgs, p.Org) {
		return apperr.Newf(apperr.ErrForbiddenRole, "org %s may not read this resource", p.Org)
	}
	return nil
}

func (h *Handlers) internalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.internalToken != "" && !constantTimeEq(r.Header.Get("X-Internal-Token"), h.internalToken) {
			writeErr(w, h.log, apperr.New(apperr.ErrUnauthorized, "invalid internal token"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func constantTimeEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
