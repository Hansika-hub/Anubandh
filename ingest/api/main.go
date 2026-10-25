// Command ingest-api accepts signed webhooks from warehouse/ERP/GST/bank, validates them and
// publishes them to Kafka (BUILD.md section 9). It never talks to the chain: workers do.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Hansika-hub/anubandh/ingest/internal/logx"
	"github.com/Hansika-hub/anubandh/ingest/pipeline"
)

var sources = []string{"warehouse", "erp", "gst", "bank"}

type backend struct {
	prod   pipeline.Producer
	reader pipeline.TopicReader
	close  func()
}

func main() {
	log := logx.New("ingest-api", nil)
	secrets := map[string][]byte{}
	for _, s := range sources {
		v := os.Getenv("HMAC_SECRET_" + upper(s))
		if v == "" {
			log.Warn("no HMAC secret for source; its webhooks will be rejected", "msg", "no HMAC secret", "source", s)
			continue
		}
		if len(v) < 16 {
			log.Error("HMAC secret too short (min 16 bytes)", "msg", "weak HMAC secret", "source", s)
			os.Exit(1)
		}
		secrets[s] = []byte(v)
	}
	if len(secrets) == 0 {
		log.Error("no HMAC_SECRET_<SOURCE> configured", "msg", "no secrets")
		os.Exit(1)
	}
	rps := envFloat("INGEST_RATE_RPS", 50)
	burst := int(envFloat("INGEST_RATE_BURST", 100))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	be, err := newBackend(ctx)
	if err != nil {
		log.Error("queue backend unavailable", "msg", "backend", "err", err.Error())
		os.Exit(1)
	}
	defer be.close()

	s := &server{
		prod: be.prod, secrets: secrets, limiter: newRateLimiter(rps, burst, nil),
		dlq:           &pipeline.DLQ{Reader: be.reader, Out: be.prod, Now: time.Now},
		operatorToken: os.Getenv("INGEST_OPERATOR_TOKEN"), log: log,
	}
	addr := os.Getenv("INGEST_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	srv := &http.Server{Addr: addr, Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		sc, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = srv.Shutdown(sc)
	}()
	log.Info("ingest-api listening", "msg", "listening", "addr", addr, "rps", rps, "burst", burst)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server failed", "msg", "server failed", "err", err.Error())
		os.Exit(1)
	}
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func envFloat(k string, def float64) float64 {
	if v := os.Getenv(k); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
		fmt.Fprintf(os.Stderr, "ignoring invalid %s=%q\n", k, v)
	}
	return def
}
