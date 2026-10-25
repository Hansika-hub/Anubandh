// Command ingest-worker consumes validated events from Kafka and applies them to the chain through
// the gateway, exactly once per idemKey (BUILD.md section 9).
//
//	ingest-worker                run the workers
//	ingest-worker dlq list       list dead-lettered events
//	ingest-worker dlq replay ID  re-enqueue one dead-lettered event
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/Hansika-hub/anubandh/ingest/internal/logx"
	"github.com/Hansika-hub/anubandh/ingest/pipeline"
)

type backend struct {
	prod    pipeline.Producer
	reader  pipeline.TopicReader
	inbound pipeline.Consumer
	retry   pipeline.Consumer
	store   pipeline.ProcessedStore
	close   func()
}

func main() {
	log := logx.New("ingest-worker", nil)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	be, err := newBackend(ctx)
	if err != nil {
		log.Error("backend unavailable", "msg", "backend", "err", err.Error())
		os.Exit(1)
	}
	defer be.close()

	if len(os.Args) > 1 && os.Args[1] == "dlq" {
		os.Exit(dlqTool(ctx, be, os.Args[2:], os.Stdout, os.Stderr))
	}

	var chain pipeline.Chain
	if os.Getenv("GATEWAY_MOCK") == "true" {
		log.Warn("GATEWAY_MOCK=true: events are NOT sent to the gateway", "msg", "mock gateway")
		chain = pipeline.MockChain{}
	} else {
		url := os.Getenv("GATEWAY_URL")
		if url == "" {
			log.Error("GATEWAY_URL is required unless GATEWAY_MOCK=true", "msg", "config")
			os.Exit(1)
		}
		chain = pipeline.NewHTTPChain(url, os.Getenv("INTERNAL_TOKEN"))
	}
	w := pipeline.NewWorker(chain, be.store, be.prod, pipeline.WorkerConfig{
		MaxAttempts:  envInt("WORKER_MAX_ATTEMPTS", 5),
		RequeueDelay: envDur("WORKER_REQUEUE_DELAY", 2*time.Second),
		BackoffBase:  envDur("WORKER_BACKOFF_BASE", time.Second),
		BackoffMax:   envDur("WORKER_BACKOFF_MAX", 30*time.Second),
	}, log)

	n := envInt("WORKER_CONCURRENCY", 4)
	log.Info("ingest-worker started", "msg", "started", "concurrency", n)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); w.Run(ctx, be.inbound, n, false) }()
	go func() { defer wg.Done(); w.Run(ctx, be.retry, n, true) }()
	wg.Wait()
	log.Info("ingest-worker stopped", "msg", "stopped")
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		fmt.Fprintf(os.Stderr, "ignoring invalid %s=%q\n", k, v)
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		fmt.Fprintf(os.Stderr, "ignoring invalid %s=%q\n", k, v)
	}
	return def
}
