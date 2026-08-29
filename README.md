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

## Agent and MCP interface

Build the universal local MCP server:

```bash
go build -o timewarp-mcp ./cmd/timewarp-mcp
```

No data scope is enabled by default. Start the MCP with the same SQLite file as
the collector:

```bash
TIMEWARP_DB=./timewarp.db \
./timewarp-mcp
```

An agent can request scopes but cannot approve them. Review pending requests
and create a temporary grant from the separate operator CLI:

```bash
TIMEWARP_DB=./timewarp.db ./timewarp consent list --status PENDING
TIMEWARP_DB=./timewarp.db ./timewarp consent approve <grant-id> --ttl 15m
TIMEWARP_DB=./timewarp.db ./timewarp consent revoke <grant-id>
```

The server exposes consent discovery, trace search, redacted trace reads,
causal inspection, and recorded-only replay manifests. Protected calls require
a stable session ID and an active, unexpired grant. They are audited under
`agent-session:<session_id>` together with approval and revocation events.
Captured headers and bodies remain redacted unless the operator separately
approves `payload:read`.

For reversible agent work, the operator can create a local checkpoint over an
explicit file list, then inspect or revert it with typed confirmation. Revert
automatically preserves the pre-revert state in a safety checkpoint; MCP can
read checkpoint metadata after `checkpoint:read` but cannot create or apply a
reversal. See the interface guide for commands and limits.

See [the universal agent interface](docs/agent-interface.md) for tools, scopes,
client setup, audit behavior, and the reversibility boundary. The reusable
agent workflow is versioned at `skills/debug-with-timewarp`.

## Editor extension

The first-party extension in [`extensions/vscode`](extensions/vscode) adds a
TimeWarp activity-bar view to VS Code-compatible editors such as Cursor. It
lists local traces, opens an event timeline, and starts or stops the collector
while sharing the same SQLite database with the CLI and MCP server.

The extension calls the stable JSON CLI surface:

```bash
timewarp traces --json --limit 100
timewarp inspect <trace-id> --json
```

See the [extension development guide](extensions/vscode/README.md) to run it
locally.

For the proposed AI-agent gateway deployment model, see the
[Ghostwire architecture and readiness plan](docs/ghostwire.md). Ghostwire is
currently a design proposal, not a runnable gateway.
