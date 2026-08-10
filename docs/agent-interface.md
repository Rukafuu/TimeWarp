# Universal agent interface

Timewarp exposes a local MCP server for evidence-based debugging by any MCP
client. The interface is service-agnostic: it understands traces, events,
causal relationships, consent scopes, replay plans, and audit sessions. Product
or company concepts belong in instrumentation adapters, not in this server.

## Run the MCP server

Build the collector and MCP binaries:

```bash
go build -o timewarp ./cmd/timewarp
go build -o timewarp-mcp ./cmd/timewarp-mcp
```

Start the MCP process with no default data scopes:

```bash
TIMEWARP_DB=./timewarp.db \
TIMEWARP_MCP_ACTOR=local-user \
./timewarp-mcp
```

The server uses MCP stdio and the official Go SDK. It negotiates current and
compatible MCP protocol revisions through the SDK.

For a Codex client, register the local process after building it:

```bash
codex mcp add timewarp \
  --env TIMEWARP_DB=/absolute/path/timewarp.db \
  --env TIMEWARP_MCP_ACTOR=local-user \
  -- /absolute/path/timewarp-mcp
```

Other MCP hosts can use the same executable as a stdio server.

## Consent scopes

| Scope | Permits |
|---|---|
| `trace:read` | Search summaries, read redacted traces, and inspect causal graphs. |
| `payload:read` | Include captured HTTP headers/bodies and sensitive browser diagnostic metadata such as DOM snapshots when explicitly requested. |
| `replay:build` | Build a recorded-only replay manifest. It does not execute replay. |
| `checkpoint:read` | List and inspect checkpoint metadata and hashes for the current session. It never returns file contents or performs reversal. |

No scope is enabled by default. `request_consent` persists a `PENDING` grant and
returns its ID, but it cannot activate it. Review and approve it from the
separate operator CLI:

```bash
TIMEWARP_DB=./timewarp.db ./timewarp consent list --status PENDING
TIMEWARP_DB=./timewarp.db ./timewarp consent approve <grant-id> --ttl 15m
```

Approval requires typing the exact grant ID after reviewing its session,
requesting actor, scopes, reason, and TTL. Grants last from one minute to 24
hours. Every protected MCP call resolves the active grants directly from
SQLite, so approval applies without restarting the MCP and expiration blocks
new calls automatically. Revoke early with:

```bash
TIMEWARP_DB=./timewarp.db ./timewarp consent revoke <grant-id>
```

`TIMEWARP_MCP_SCOPES` remains available only for operator-controlled bootstrap
environments where non-expiring process-wide scopes are intentional.

## Tools

- `get_capabilities`: show bootstrap scopes plus active grants for a session.
- `request_consent`: create an audited request without granting access.
- `search_traces`: search summaries by service, time, and limit.
- `get_trace`: read events with HTTP headers, bodies, DOM/HTML snapshots, screenshots, and form-value metadata redacted by default.
- `inspect_trace`: reconstruct a causal graph and return diagnostics.
- `build_replay`: return a recorded-only manifest without starting a server.
- `list_checkpoints`: list this session's checkpoints without file contents.
- `get_checkpoint`: inspect paths, hashes, status, and safety linkage; it cannot revert.

Protected tools require a stable, non-secret `session_id`. Every allowed or
denied protected call writes a sanitized `CUSTOM_EVENT` under the trace
`agent-session:<session_id>`. Audit metadata contains the actor, action, target,
outcome, active grant IDs, and effective scopes; it excludes tool results and
captured payloads. Approval and revocation are written to the same audit trace.
If a successful read cannot be audited, the MCP call fails instead of returning
the data.

## Checkpoints and reversal

The operator CLI can capture up to 100 explicitly named, workspace-relative
regular files before an agent edits them. Directories, symlinks, traversal,
and files larger than 4 MiB are rejected; total captured content is limited to
20 MiB. Checkpoint contents are stored locally in SQLite and may contain
sensitive source or configuration, so select only files required for the task.

```bash
TIMEWARP_DB=./timewarp.db ./timewarp checkpoint create \
  --workspace /absolute/project --session agent-debug-1 \
  --label "before fix" --file internal/a.go --file config/example.json
TIMEWARP_DB=./timewarp.db ./timewarp checkpoint list --session agent-debug-1
TIMEWARP_DB=./timewarp.db ./timewarp checkpoint inspect <checkpoint-id>
TIMEWARP_DB=./timewarp.db ./timewarp checkpoint revert <checkpoint-id> \
  --workspace /absolute/project
```

Creation requires typing the session ID. Reversal first prints the exact
restore/delete/unchanged plan and requires typing the checkpoint ID. Before
applying it, Timewarp automatically captures the current versions of the same
paths as a safety checkpoint. A partially failed reversal is compensated from
that safety snapshot. Reversal never deletes directories or unlisted files.

MCP exposes checkpoint metadata only. It deliberately has no create, write,
or revert tool, so the agent cannot cross the human confirmation boundary.
Future mutation tools must add a separate write scope, dry run, verified
checkpoint, scoped compensating action, and post-action readback.

The versioned skill at `skills/debug-with-timewarp` teaches agents this consent,
minimization, investigation, and reversibility workflow. Enforcement remains in
the MCP server; the skill is guidance, not an authorization boundary.
