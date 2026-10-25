//go:build adapters

package main

import (
	"context"
	"errors"
	"os"

	"github.com/Hansika-hub/anubandh/ingest/internal/kafkaq"
)

func newBackend(context.Context) (*backend, error) {
	brokers := kafkaq.Brokers(os.Getenv("KAFKA_BROKERS"))
	if len(brokers) == 0 {
		return nil, errors.New("KAFKA_BROKERS is required")
	}
	p, err := kafkaq.NewProducer(brokers)
	if err != nil {
		return nil, err
	}
	return &backend{prod: p, reader: kafkaq.NewReader(brokers), close: p.Close}, nil
}
