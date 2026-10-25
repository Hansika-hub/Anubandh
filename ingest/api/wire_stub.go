//go:build !adapters

package main

import (
	"context"
	"errors"
	"os"

	"github.com/Hansika-hub/anubandh/ingest/pipeline"
)

// Built without `-tags adapters`: there is no Kafka client. INGEST_MEMORY=true runs against an
// in-process broker for local experiments only (events never reach a worker in another process).
func newBackend(context.Context) (*backend, error) {
	if os.Getenv("INGEST_MEMORY") == "true" {
		b := pipeline.NewMemBroker()
		return &backend{prod: b, reader: b, close: b.Close}, nil
	}
	return nil, errors.New("built without the Kafka adapter: rebuild with -tags adapters (the Dockerfile does), or set INGEST_MEMORY=true for local dev only")
}
