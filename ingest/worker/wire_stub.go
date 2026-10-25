//go:build !adapters

package main

import (
	"context"
	"errors"
	"os"

	"github.com/Hansika-hub/anubandh/ingest/pipeline"
)

// Built without `-tags adapters`: no Kafka or Postgres client. INGEST_MEMORY=true uses in-process
// stand-ins for local experiments only (state is lost on exit and not shared with ingest-api).
func newBackend(context.Context) (*backend, error) {
	if os.Getenv("INGEST_MEMORY") == "true" {
		b := pipeline.NewMemBroker()
		return &backend{prod: b, reader: b, inbound: b.Consumer(pipeline.TopicInbound), retry: b.Consumer(pipeline.TopicRetry),
			store: pipeline.NewMemStore(), close: b.Close}, nil
	}
	return nil, errors.New("built without the Kafka/Postgres adapters: rebuild with -tags adapters (the Dockerfile does), or set INGEST_MEMORY=true for local dev only")
}
