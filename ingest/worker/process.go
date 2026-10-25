package main

// The idempotent processing core (ProcessEvent, AlreadyProcessed, MarkProcessed, SubmitToChain,
// RequeueWithDelay, SendToDLQ — the frozen names of BUILD.md 9.4) lives in ingest/pipeline/worker.go
// so that tests/burst can import and drive exactly the code that runs in production. This file
// documents the contract and keeps the BUILD.md layout recognisable.
//
// Per message:
//   1. processed_keys hit              -> dropped, committed.
//   2. gateway 200                     -> MarkProcessed.
//   3. ERR_DUPLICATE                   -> MarkProcessed (the chain already has it).
//   4. ERR_INVALID_TRANSITION          -> RequeueWithDelay (out-of-order; retry topic, RequeueDelay).
//   5. any other error / 503           -> RequeueWithDelay with exponential backoff.
//   6. malformed event                 -> SendToDLQ immediately (retrying cannot help).
//   7. 5th failure of any event        -> SendToDLQ.
