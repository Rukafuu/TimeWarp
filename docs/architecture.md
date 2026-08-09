# Timewarp MVP architecture

The MVP proves one vertical slice: **record -> reconstruct -> replay**.

## Boundaries

- `pkg/protocol` owns the stable ingestion and storage model.
- `pkg/sdk` is the dependency-light Go instrumentation client.
- `internal/collector` validates HTTP ingestion and batches writes.
- `internal/storage` implements the `EventStore` port with SQLite.
- `internal/graph` deterministically reconstructs and diagnoses causal graphs.
- `internal/replay` builds a safe manifest and serves recorded HTTP responses.
- `cmd/timewarp` is the composition root and CLI.

The core depends on the `EventStore` interface, never on SQLite. Events use
opaque metadata plus explicit HTTP request/response records so replay does not
need to infer protocol semantics from arbitrary attributes.

## Ingestion protocol

`POST /v1/events` accepts either one JSON event or `{ "events": [...] }` and
returns `202 Accepted`. Required fields are `trace_id`, `event_id`, `service`,
`type`, and a positive Unix-millisecond `timestamp`. Parent events may arrive
later. Duplicate `(trace_id,event_id)` records are idempotently ignored.

## Replay safety model

Replay is recorded-only by default. The manifest enumerates dependencies and
must explicitly opt a service into `live` mode in a future version. The MVP
mock server matches recorded outbound HTTP interactions by method and URL,
returns their captured status/headers/body, and can preserve latency. It never
contacts the captured upstream.

## Incremental roadmap

1. Core types, SQLite store, validation, graph diagnostics.
2. Batched HTTP collector, CLI listing and ASCII inspection.
3. Go SDK and explicit HTTP request/response capture.
4. Replay manifest and recorded-response mock server.
5. Demo services and end-to-end fixture.
6. After the slice is stable: gRPC ingestion, OpenTelemetry bridge, failure
   injection, execution diff, and alternative stores.
