package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
)

const maxBody = 1 << 20 // 1 MiB

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    apperr.Code    `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

type listBody[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeList[T any](w http.ResponseWriter, items []T, total int) {
	if items == nil {
		items = []T{}
	}
	writeJSON(w, http.StatusOK, listBody[T]{Items: items, Total: total})
}

// writeErr renders the shared error envelope (BUILD.md §2, §12). Unexpected errors are logged
// and reported as ERR_INTERNAL without leaking internals.
func writeErr(w http.ResponseWriter, log *slog.Logger, err error) {
	ae, ok := apperr.As(err)
	if !ok {
		log.Error("unhandled error", "err", err)
		ae = apperr.New(apperr.ErrInternal, "internal error")
	}
	details := map[string]any{}
	for k, v := range ae.Details {
		details[k] = v
	}
	if ae.TxID != "" {
		details["txId"] = ae.TxID
	}
	status := apperr.HTTPStatus(ae.Code)
	if ae.Code == apperr.ErrInternal && details["unavailable"] == true {
		status = http.StatusServiceUnavailable
	}
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	if status >= 500 {
		log.Warn("request failed", "code", ae.Code, "err", ae.Error())
	}
	msg := ae.Message
	if msg == "" {
		msg = string(ae.Code)
	}
	writeJSON(w, status, errorBody{Error: errorDetail{Code: ae.Code, Message: msg, Details: details}})
}

// decode reads a JSON body (max 1 MiB). An empty body decodes as {}.
func decode(r *http.Request, v any) error {
	body := http.MaxBytesReader(nil, r.Body, maxBody)
	dec := json.NewDecoder(body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return apperr.New(apperr.ErrBadRequest, "request body exceeds 1 MiB")
		}
		return apperr.Wrap(apperr.ErrBadRequest, err, "request body is not valid JSON: "+err.Error())
	}
	if dec.More() {
		return apperr.New(apperr.ErrBadRequest, "request body must be a single JSON object")
	}
	return nil
}

func queryInt(r *http.Request, key string, def, lo, hi int) (int, error) {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return def, nil
	}
	i, err := strconv.Atoi(v)
	if err != nil || i < lo || i > hi {
		return 0, apperr.Newf(apperr.ErrBadRequest, "%s must be an integer in [%d, %d]", key, lo, hi)
	}
	return i, nil
}

func queryBool(r *http.Request, key string) (*bool, error) {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil, apperr.Newf(apperr.ErrBadRequest, "%s must be true or false", key)
	}
	return &b, nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}
