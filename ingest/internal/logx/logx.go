// Package logx builds the JSON logger required by BUILD.md: ts, level, svc, dealId, trancheNo, idemKey, msg.
package logx

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// New returns a JSON logger for service svc. Level comes from LOG_LEVEL (debug|info|warn|error).
func New(svc string, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	lvl := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			a.Key = "ts"
		}
		return a
	}})
	return slog.New(h).With("svc", svc)
}
