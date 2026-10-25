//go:build adapters

package main

import (
	"context"
	"errors"
	"os"

	"github.com/Hansika-hub/anubandh/ingest/internal/kafkaq"
	"github.com/Hansika-hub/anubandh/ingest/internal/pgstore"
	"github.com/Hansika-hub/anubandh/ingest/pipeline"
)

func newBackend(ctx context.Context) (*backend, error) {
	brokers := kafkaq.Brokers(os.Getenv("KAFKA_BROKERS"))
	if len(brokers) == 0 {
		return nil, errors.New("KAFKA_BROKERS is required")
	}
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		return nil, errors.New("PG_DSN is required")
	}
	group := os.Getenv("WORKER_GROUP")
	if group == "" {
		group = "anubandh-ingest-worker"
	}
	store, err := pgstore.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	prod, err := kafkaq.NewProducer(brokers)
	if err != nil {
		store.Close()
		return nil, err
	}
	in, err := kafkaq.NewConsumer(brokers, group+"-inbound", pipeline.TopicInbound)
	if err != nil {
		prod.Close()
		store.Close()
		return nil, err
	}
	rt, err := kafkaq.NewConsumer(brokers, group+"-retry", pipeline.TopicRetry)
	if err != nil {
		in.Close()
		prod.Close()
		store.Close()
		return nil, err
	}
	return &backend{prod: prod, reader: kafkaq.NewReader(brokers), inbound: in, retry: rt, store: store,
		close: func() { rt.Close(); in.Close(); prod.Close(); store.Close() }}, nil
}
