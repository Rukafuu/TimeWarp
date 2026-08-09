# Timewarp

Local-first causal recording and safe HTTP replay for distributed systems.

## Quick start

Requires Go 1.23+.

```bash
go build -o timewarp ./cmd/timewarp
./timewarp serve
```

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
