package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Hansika-hub/anubandh/ingest/pipeline"
)

// dlqTool implements `ingest-worker dlq list|replay`. It returns the process exit code.
func dlqTool(ctx context.Context, be *backend, args []string, out, errw io.Writer) int {
	d := &pipeline.DLQ{Reader: be.reader, Out: be.prod, Now: time.Now}
	switch {
	case len(args) == 1 && args[0] == "list":
		entries, err := d.List(ctx, 0, false)
		if err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(entries)
		return 0
	case len(args) == 2 && args[0] == "replay":
		switch err := d.Replay(ctx, args[1]); {
		case err == nil:
			fmt.Fprintln(out, "replayed", args[1])
			return 0
		case errors.Is(err, pipeline.ErrDLQNotFound), errors.Is(err, pipeline.ErrDLQReplayed):
			fmt.Fprintln(errw, "error:", err)
			return 2
		default:
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
	}
	fmt.Fprintln(errw, "usage: ingest-worker dlq list | ingest-worker dlq replay <id>")
	return 2
}
