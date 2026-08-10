# Timewarp

Local-first causal recording and safe HTTP replay for distributed systems.

## Quick start

Requires Go 1.25+.

```bash
go build -o timewarp ./cmd/timewarp
./timewarp serve
```

The collector admits each HTTP batch atomically: an overloaded batch receives
`503 Service Unavailable` with `Retry-After` and persists no events. Queue size
and maximum batching delay are configurable:

```bash
./timewarp serve -buffer-events 1024 -flush-interval 25ms
```

`GET /metrics` returns JSON counters for received, persisted, and rejected
batches/events, rejection reasons, batch-size distribution, and current queue
occupancy.

Send a captured outbound HTTP interaction:

```bash
curl -X POST http://localhost:7777/v1/events \
  -H 'content-type: application/json' \
  -d '{"trace_id":"tw_demo","event_id":"evt_1","service":"payment-api","type":"HTTP_CLIENT","timestamp":1723143029123,"duration_ms":20,"http":{"method":"POST","url":"https://pay.test/charge","status_code":503,"response_body":"dW5hdmFpbGFibGU="}}'
```

Then inspect and replay it without contacting the original dependency:

```bash
./timewarp traces
./timewarp inspect tw_demo
./timewarp replay tw_demo --addr :7778
curl -X POST http://localhost:7778 -H 'X-Timewarp-Original-URL: https://pay.test/charge'
```

See [the MVP architecture](docs/architecture.md) for boundaries, protocol,
safety rules, and the incremental roadmap.

## End-to-end demo

Start the collector and the demo services in separate terminals:

```bash
go run ./cmd/timewarp serve
go run ./cmd/timewarp-demo
```

Trigger a checkout whose recorded payment dependency returns `503`:

```bash
curl -X POST http://localhost:7780/checkout \
  -H 'content-type: application/json' \
  -d '{"amount":4200}'
```

The response includes `X-Timewarp-Trace-ID`. Use that value to inspect the
causal graph and start a recorded-only replay server:

```bash
go run ./cmd/timewarp inspect <trace-id>
go run ./cmd/timewarp replay <trace-id>
curl -X POST http://localhost:7778 \
  -H 'X-Timewarp-Original-URL: http://localhost:7781/charge'
```

The replay returns the captured payment response without contacting the demo
payment service. Run `go test ./...` to execute the same flow automatically.

For the proposed AI-agent gateway deployment model, see the
[Ghostwire architecture and readiness plan](docs/ghostwire.md). Ghostwire is
currently a design proposal, not a runnable gateway.
