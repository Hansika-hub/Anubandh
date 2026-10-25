package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
)

const maxBody = 1 << 20 // 1 MiB

type ctxKey int

const (
	ctxBody ctxKey = iota
	ctxSource
)

// VerifyHMAC checks X-Signature == hex(HMAC-SHA256(body, secret)) in constant time and, on
// success, hands the verified body to the next handler through the request context (the body
// stream has been consumed). The signature is computed over the exact bytes received.
func VerifyHMAC(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, ok := readVerified(w, r, secret)
			if !ok {
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxBody, body)))
		})
	}
}

// VerifyHMACBySource selects the secret by the X-Source header (warehouse|erp|gst|bank). An unknown
// or missing source gets the same 401 as a bad signature so callers learn nothing about which
// sources exist.
func VerifyHMACBySource(secrets map[string][]byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			src := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Source")))
			secret, known := secrets[src]
			if !known || len(secret) == 0 {
				drain(r)
				writeErr(w, http.StatusUnauthorized, "ERR_BAD_SIGNATURE", "invalid signature", nil)
				return
			}
			body, ok := readVerified(w, r, secret)
			if !ok {
				return
			}
			ctx := context.WithValue(context.WithValue(r.Context(), ctxBody, body), ctxSource, src)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func drain(r *http.Request) { _, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxBody)) }

func readVerified(w http.ResponseWriter, r *http.Request, secret []byte) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "ERR_BAD_REQUEST", "request body too large or unreadable", nil)
		return nil, false
	}
	got, err := hex.DecodeString(strings.TrimSpace(r.Header.Get("X-Signature")))
	if err != nil || len(got) != sha256.Size {
		writeErr(w, http.StatusUnauthorized, "ERR_BAD_SIGNATURE", "invalid signature", nil)
		return nil, false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		writeErr(w, http.StatusUnauthorized, "ERR_BAD_SIGNATURE", "invalid signature", nil)
		return nil, false
	}
	return bytes.Clone(body), true
}

// Sign is the sender-side counterpart (used by tests, the demo and the burst test).
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
