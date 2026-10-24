// Command gateway is the Anubandh chain gateway: REST API for the dashboards, checkpointed chain
// event listener, PostgreSQL read model, payment outbox and deadline scheduler (BUILD.md §8).
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Hansika-hub/anubandh/gateway/internal/app"
	"github.com/Hansika-hub/anubandh/gateway/internal/config"
	"github.com/Hansika-hub/anubandh/gateway/internal/store"
)

var version = "dev" // set with -ldflags "-X main.version=..."

func main() { os.Exit(run()) }

// newLogger emits JSON with the fields of BUILD.md §2: ts, level, svc, msg (+ dealId, trancheNo, idemKey where known).
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.ToUpper(level))); err != nil {
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl, ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && a.Key == slog.TimeKey {
			a.Key = "ts"
			a.Value = slog.StringValue(a.Value.Time().UTC().Format(time.RFC3339Nano))
		}
		if len(groups) == 0 && a.Key == slog.LevelKey {
			a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
		}
		return a
	}})
	return slog.New(h).With("svc", "gateway")
}

func openStore(ctx context.Context, dsn string, log *slog.Logger) (*store.Store, error) {
	deadline := time.Now().Add(60 * time.Second)
	for {
		st, err := store.Open(ctx, dsn)
		if err == nil {
			return st, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, err
		}
		log.Warn("waiting for PostgreSQL", "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// healthcheck probes the local /health endpoint (Docker HEALTHCHECK in a shell-less image).
// It only ever probes loopback; just the port is taken from GATEWAY_ADDR.
func healthcheck() int {
	port := "8080"
	if _, p, err := net.SplitHostPort(os.Getenv("GATEWAY_ADDR")); err == nil && p != "" {
		port = p
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/health", nil) //nolint:gosec // loopback only, numeric port
	if err != nil {
		return 1
	}
	res, err := http.DefaultClient.Do(req) //nolint:gosec // loopback only, numeric port
	if err != nil {
		return 1
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run() int {
	migrateOnly := flag.Bool("migrate-only", false, "apply db/migrations and exit")
	health := flag.Bool("healthcheck", false, "probe the local /health endpoint and exit 0 if healthy")
	flag.Parse()
	if *health {
		return healthcheck()
	}

	cfg, err := config.FromEnv()
	log := newLogger(cfg.LogLevel)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := openStore(ctx, cfg.PGDSN, log)
	if err != nil {
		log.Error("cannot connect to PostgreSQL", "err", err)
		return 1
	}
	defer st.Close()
	if cfg.MigrateOnStart || *migrateOnly {
		applied, err := st.Migrate(ctx, cfg.MigrationsDir)
		if err != nil {
			log.Error("migration failed", "err", err)
			return 1
		}
		log.Info("migrations applied", "files", applied)
	}
	if *migrateOnly {
		return 0
	}

	a, err := app.Build(ctx, cfg, st, log, version, app.Overrides{})
	if err != nil {
		log.Error("startup failed", "err", err)
		return 2
	}
	defer a.Close()
	if a.Mock != nil {
		log.Warn("FABRIC_MOCK=true: using the in-memory deal chaincode, not a Drunix network", "stateFile", cfg.MockStatePath)
		if cp, found, err := store.GetCheckpoint(ctx, st.Pool, cfg.ListenerID); err == nil && found && cp.BlockNo > a.Mock.Height() {
			log.Error("read model is ahead of the mock ledger (mock state lost?): set FABRIC_MOCK_STATE to a persistent file or reset the database",
				"checkpointBlock", cp.BlockNo, "mockHeight", a.Mock.Height())
		}
	}
	a.StartBackground(ctx)
	if err := a.Serve(ctx); err != nil {
		log.Error("http server failed", "err", err)
		return 1
	}
	return 0
}
